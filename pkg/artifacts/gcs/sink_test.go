// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gcs

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/pkg/artifacts"
)

func TestGCSSink_ObjectNamingAndSummary(t *testing.T) {
	fakeClient := NewFakeStorageClient()
	sink, err := New(t.Context(), "gs://my-eval-bucket", WithStorageClient(fakeClient))
	if err != nil {
		t.Fatalf("Failed to create GCSSink: %v", err)
	}

	if sink.Bucket() != "my-eval-bucket" {
		t.Errorf("Expected bucket %q, got %q", "my-eval-bucket", sink.Bucket())
	}

	score := 0.85
	startTime := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	finishTime := time.Date(2026, 10, 17, 12, 5, 0, 0, time.UTC)

	meta := artifacts.RunMetadata{
		RunName:      "test-run",
		RunNamespace: "eval-runner",
		TaskID:       "docs/readme_comprehension",
		Model:        "google/gemini-3.5-flash-lite",
		Commit:       "abcdef123456",
		Samples:      10,
		Successes:    8,
		MeanScore:    &score,
		StartedAt:    &startTime,
		FinishedAt:   &finishTime,
	}

	logFilename := "2026-10-17_readme_eval.json"
	logContent := []byte(`{"results": {"score": 0.85}}`)

	logURI, err := sink.StoreArtifact(t.Context(), meta, logFilename, logContent)
	if err != nil {
		t.Fatalf("StoreArtifact failed: %v", err)
	}

	expectedLogURI := "gs://my-eval-bucket/runs/abcdef123456/docs/readme_comprehension/2026-10-17_readme_eval.json"
	if logURI != expectedLogURI {
		t.Errorf("Expected log URI %q, got %q", expectedLogURI, logURI)
	}

	expectedPrefix := "gs://my-eval-bucket/runs/abcdef123456/"
	if prefix := sink.ArtifactPrefix(meta.Commit); prefix != expectedPrefix {
		t.Errorf("Expected artifact prefix %q, got %q", expectedPrefix, prefix)
	}

	// Verify log object in fake client
	logObj, exists := fakeClient.GetObject("my-eval-bucket", "runs/abcdef123456/docs/readme_comprehension/2026-10-17_readme_eval.json")
	if !exists {
		t.Fatalf("Log object was not written to storage")
	}
	if string(logObj.Data) != string(logContent) {
		t.Errorf("Log object data mismatch: expected %s, got %s", logContent, logObj.Data)
	}
	if logObj.ContentType != "application/json" {
		t.Errorf("Expected content type %q, got %q", "application/json", logObj.ContentType)
	}

	// Verify summary.json in fake client
	summaryObj, exists := fakeClient.GetObject("my-eval-bucket", "runs/abcdef123456/docs/readme_comprehension/summary.json")
	if !exists {
		t.Fatalf("summary.json was not written to storage")
	}
	if summaryObj.ContentType != "application/json" {
		t.Errorf("Expected summary content type %q, got %q", "application/json", summaryObj.ContentType)
	}

	var summary artifacts.TaskSummary
	if err := json.Unmarshal(summaryObj.Data, &summary); err != nil {
		t.Fatalf("Failed to parse summary.json: %v", err)
	}

	if summary.TaskID != "docs/readme_comprehension" {
		t.Errorf("Expected TaskID %q, got %q", "docs/readme_comprehension", summary.TaskID)
	}
	if summary.Model != "google/gemini-3.5-flash-lite" {
		t.Errorf("Expected Model %q, got %q", "google/gemini-3.5-flash-lite", summary.Model)
	}
	if summary.Commit != "abcdef123456" {
		t.Errorf("Expected Commit %q, got %q", "abcdef123456", summary.Commit)
	}
	if summary.Samples != 10 {
		t.Errorf("Expected Samples 10, got %d", summary.Samples)
	}
	if summary.Successes != 8 {
		t.Errorf("Expected Successes 8, got %d", summary.Successes)
	}
	if summary.MeanScore == nil || *summary.MeanScore != 0.85 {
		t.Errorf("Expected MeanScore 0.85, got %v", summary.MeanScore)
	}
	if summary.StartedAt == nil || !summary.StartedAt.Equal(startTime) {
		t.Errorf("Expected StartedAt %v, got %v", startTime, summary.StartedAt)
	}
	if summary.FinishedAt == nil || !summary.FinishedAt.Equal(finishTime) {
		t.Errorf("Expected FinishedAt %v, got %v", finishTime, summary.FinishedAt)
	}
	if summary.Capped {
		t.Errorf("Expected Capped=false when under cap limit")
	}
	if summary.CapBytes != 0 {
		t.Errorf("Expected CapBytes=0 when under cap limit, got %d", summary.CapBytes)
	}
}

func TestGCSSink_CapBehavior(t *testing.T) {
	fakeClient := NewFakeStorageClient()
	capLimit := int64(30)
	sink, err := New(t.Context(), "my-eval-bucket",
		WithStorageClient(fakeClient),
		WithMaxUploadBytes(capLimit),
	)
	if err != nil {
		t.Fatalf("Failed to create GCSSink: %v", err)
	}

	meta := artifacts.RunMetadata{
		TaskID:    "docs/readme_comprehension",
		Model:     "test-model",
		Commit:    "1234567890ab",
		Samples:   5,
		Successes: 5,
	}

	largeLog := []byte(strings.Repeat("A", 100)) // 100 bytes > 30 byte cap

	logURI, err := sink.StoreArtifact(t.Context(), meta, "large.json", largeLog)
	if err != nil {
		t.Fatalf("StoreArtifact failed: %v", err)
	}

	if logURI != "gs://my-eval-bucket/runs/1234567890ab/docs/readme_comprehension/large.json" {
		t.Errorf("Unexpected log URI: %s", logURI)
	}

	logObj, exists := fakeClient.GetObject("my-eval-bucket", "runs/1234567890ab/docs/readme_comprehension/large.json")
	if !exists {
		t.Fatalf("Log object missing")
	}
	if int64(len(logObj.Data)) != capLimit {
		t.Errorf("Expected uploaded log to be capped at %d bytes, got %d bytes", capLimit, len(logObj.Data))
	}
	if string(logObj.Data) != strings.Repeat("A", int(capLimit)) {
		t.Errorf("Expected uploaded log data to match truncated prefix")
	}

	summaryObj, exists := fakeClient.GetObject("my-eval-bucket", "runs/1234567890ab/docs/readme_comprehension/summary.json")
	if !exists {
		t.Fatalf("summary.json missing")
	}

	var summary artifacts.TaskSummary
	if err := json.Unmarshal(summaryObj.Data, &summary); err != nil {
		t.Fatalf("Failed to parse summary.json: %v", err)
	}
	if !summary.Capped {
		t.Errorf("Expected summary.Capped=true when cap is exceeded")
	}
	if summary.CapBytes != capLimit {
		t.Errorf("Expected summary.CapBytes=%d, got %d", capLimit, summary.CapBytes)
	}
}

func TestGCSSink_PathValidationAndTraversal(t *testing.T) {
	fakeClient := NewFakeStorageClient()
	sink, err := New(t.Context(), "eval-bucket", WithStorageClient(fakeClient))
	if err != nil {
		t.Fatalf("Failed to create GCSSink: %v", err)
	}

	validMeta := artifacts.RunMetadata{
		TaskID: "docs/readme_comprehension",
		Commit: "1234567890ab",
	}

	tests := []struct {
		name     string
		meta     artifacts.RunMetadata
		filename string
	}{
		{
			name: "empty commit",
			meta: artifacts.RunMetadata{
				TaskID: "docs/readme_comprehension",
				Commit: "",
			},
			filename: "log.json",
		},
		{
			name: "commit with slash",
			meta: artifacts.RunMetadata{
				TaskID: "docs/readme_comprehension",
				Commit: "foo/bar",
			},
			filename: "log.json",
		},
		{
			name: "commit with traversal",
			meta: artifacts.RunMetadata{
				TaskID: "docs/readme_comprehension",
				Commit: "../foo",
			},
			filename: "log.json",
		},
		{
			name: "empty taskID",
			meta: artifacts.RunMetadata{
				TaskID: "",
				Commit: "1234567890ab",
			},
			filename: "log.json",
		},
		{
			name: "taskID with double dot",
			meta: artifacts.RunMetadata{
				TaskID: "docs/../escape",
				Commit: "1234567890ab",
			},
			filename: "log.json",
		},
		{
			name: "taskID with leading slash",
			meta: artifacts.RunMetadata{
				TaskID: "/docs/readme",
				Commit: "1234567890ab",
			},
			filename: "log.json",
		},
		{
			name: "taskID with trailing slash",
			meta: artifacts.RunMetadata{
				TaskID: "docs/readme/",
				Commit: "1234567890ab",
			},
			filename: "log.json",
		},
		{
			name: "taskID with backslash",
			meta: artifacts.RunMetadata{
				TaskID: "docs\\readme",
				Commit: "1234567890ab",
			},
			filename: "log.json",
		},
		{
			name:     "empty filename",
			meta:     validMeta,
			filename: "",
		},
		{
			name:     "filename with slash",
			meta:     validMeta,
			filename: "sub/log.json",
		},
		{
			name:     "filename with double dot",
			meta:     validMeta,
			filename: "../log.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sink.StoreArtifact(t.Context(), tt.meta, tt.filename, []byte("data"))
			if err == nil {
				t.Errorf("Expected error for %s, got nil", tt.name)
			}
		})
	}
}

func TestGCSSink_BucketNormalizationAndValidation(t *testing.T) {
	fakeClient := NewFakeStorageClient()

	validBuckets := []struct {
		input    string
		expected string
	}{
		{"gs://my-bucket", "my-bucket"},
		{"gs://my-bucket/", "my-bucket"},
		{"my-bucket", "my-bucket"},
		{"  gs://my-bucket/  ", "my-bucket"},
	}

	for _, tc := range validBuckets {
		sink, err := New(t.Context(), tc.input, WithStorageClient(fakeClient))
		if err != nil {
			t.Errorf("New(%q) failed: %v", tc.input, err)
			continue
		}
		if sink.Bucket() != tc.expected {
			t.Errorf("New(%q) bucket = %q, expected %q", tc.input, sink.Bucket(), tc.expected)
		}
	}

	invalidBuckets := []string{
		"",
		"   ",
		"gs://",
		"gs://bucket/with/path",
		"bucket..name",
		"bucket\\name",
	}

	for _, inv := range invalidBuckets {
		_, err := New(t.Context(), inv, WithStorageClient(fakeClient))
		if err == nil {
			t.Errorf("New(%q) expected error, got nil", inv)
		}
	}
}

func TestGCSSink_ErrorPropagation(t *testing.T) {
	fakeClient := NewFakeStorageClient()
	sink, err := New(t.Context(), "eval-bucket", WithStorageClient(fakeClient))
	if err != nil {
		t.Fatalf("Failed to create GCSSink: %v", err)
	}

	meta := artifacts.RunMetadata{
		TaskID: "docs/readme_comprehension",
		Model:  "test-model",
		Commit: "1234567890ab",
	}

	// 1. Injected error on log file upload
	fakeClient.Errors["eval-bucket/runs/1234567890ab/docs/readme_comprehension/log.json"] = errors.New("permission denied")
	_, err = sink.StoreArtifact(t.Context(), meta, "log.json", []byte("data"))
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("Expected permission denied error, got %v", err)
	}

	// 2. Injected error on summary.json upload
	delete(fakeClient.Errors, "eval-bucket/runs/1234567890ab/docs/readme_comprehension/log.json")
	fakeClient.Errors["eval-bucket/runs/1234567890ab/docs/readme_comprehension/summary.json"] = errors.New("quota exceeded")
	_, err = sink.StoreArtifact(t.Context(), meta, "log.json", []byte("data"))
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("Expected quota exceeded error, got %v", err)
	}
}

func TestValidateBucket(t *testing.T) {
	valid := []string{
		"gs://my-bucket",
		"gs://my-bucket/",
		"my-bucket",
		"  gs://my-bucket/  ",
	}
	for _, b := range valid {
		if err := ValidateBucket(b); err != nil {
			t.Errorf("ValidateBucket(%q) returned unexpected error: %v", b, err)
		}
	}

	invalid := []string{
		"",
		"   ",
		"gs://bucket/with/path",
		"bucket..name",
		"bucket\\name",
		"bucket name",
	}
	for _, b := range invalid {
		if err := ValidateBucket(b); err == nil {
			t.Errorf("ValidateBucket(%q) expected error, got nil", b)
		}
	}
}

func TestGCSSink_StoreComparisonAndLoad(t *testing.T) {
	fakeClient := NewFakeStorageClient()
	sink, err := New(t.Context(), "eval-bucket", WithStorageClient(fakeClient), WithMaxUploadBytes(100))
	if err != nil {
		t.Fatalf("Failed to create GCSSink: %v", err)
	}

	payload := []byte(`{"comparison": "comp-1", "verdict": "UNCHANGED"}`)
	uri, err := sink.StoreComparison(t.Context(), "comp-1", payload)
	if err != nil {
		t.Fatalf("StoreComparison failed: %v", err)
	}

	expectedURI := "gs://eval-bucket/comparisons/comp-1.json"
	if uri != expectedURI {
		t.Errorf("Expected URI %q, got %q", expectedURI, uri)
	}

	// Verify object exists in fake client
	obj, exists := fakeClient.GetObject("eval-bucket", "comparisons/comp-1.json")
	if !exists {
		t.Fatalf("Object was not written to storage client")
	}
	if string(obj.Data) != string(payload) {
		t.Errorf("Payload mismatch: got %s, want %s", obj.Data, payload)
	}

	// Store and verify run artifact for loading
	runPayload := []byte(`{"version": 2, "samples": []}`)
	fakeClient.Objects["eval-bucket/runs/commit1/task1/log.json"] = FakeObject{Data: runPayload}

	// Load via gs:// URI
	loaded, err := sink.Load(t.Context(), "gs://eval-bucket/runs/commit1/task1/log.json")
	if err != nil {
		t.Fatalf("Load via URI failed: %v", err)
	}
	if string(loaded) != string(runPayload) {
		t.Errorf("Loaded mismatch: got %s, want %s", loaded, runPayload)
	}

	// Load via object path
	loadedPath, err := sink.Load(t.Context(), "runs/commit1/task1/log.json")
	if err != nil {
		t.Fatalf("Load via object path failed: %v", err)
	}
	if string(loadedPath) != string(runPayload) {
		t.Errorf("Loaded mismatch: got %s, want %s", loadedPath, runPayload)
	}

	// Missing runs/ prefix rejected
	if _, err := sink.Load(t.Context(), "comparisons/comp-1.json"); err == nil {
		t.Errorf("Expected error for missing runs/ prefix, got nil")
	}

	// Bucket mismatch
	if _, err := sink.Load(t.Context(), "gs://other-bucket/runs/commit1/task1/log.json"); err == nil {
		t.Errorf("Expected error for bucket mismatch, got nil")
	}

	// Exceeds max bytes
	oversized := make([]byte, 200)
	fakeClient.Objects["eval-bucket/runs/oversized.json"] = FakeObject{Data: oversized}
	if _, err := sink.Load(t.Context(), "gs://eval-bucket/runs/oversized.json"); err == nil {
		t.Errorf("Expected error for oversized artifact load, got nil")
	}
}
