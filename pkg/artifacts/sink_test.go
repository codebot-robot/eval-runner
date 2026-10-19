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

package artifacts_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/eval-runner/internal/fakes"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
)

func TestNoopSink(t *testing.T) {
	sink := &artifacts.NoopSink{}
	meta := artifacts.RunMetadata{
		RunName:      "test-run",
		RunNamespace: "eval-runner",
		TaskID:       "docs/readme_comprehension",
	}

	path, err := sink.StoreArtifact(t.Context(), meta, "eval.json", []byte("data"))
	if err != nil {
		t.Fatalf("NoopSink returned error: %v", err)
	}
	if path != "" {
		t.Errorf("Expected empty path from NoopSink, got %q", path)
	}
}

func TestLocalDirSink(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "artifacts-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sink := &artifacts.LocalDirSink{BaseDir: tempDir}
	meta := artifacts.RunMetadata{
		RunName:      "run-123",
		RunNamespace: "test-ns",
		TaskID:       "docs/readme_comprehension",
		Model:        "mockllm/model",
		Commit:       "abc1234",
		Samples:      11,
		SampleRange:  &artifacts.SampleRange{Start: 5, End: 15},
	}

	payload := []byte(`{"version": 2}`)
	savedPath, err := sink.StoreArtifact(t.Context(), meta, "result.json", payload)
	if err != nil {
		t.Fatalf("StoreArtifact failed: %v", err)
	}

	expectedPath := filepath.Join(tempDir, "runs", "abc1234", "docs%2Freadme_comprehension", "result.json")
	if savedPath != expectedPath {
		t.Errorf("Expected path %q, got %q", expectedPath, savedPath)
	}

	readBytes, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("Failed to read saved file: %v", err)
	}
	if string(readBytes) != string(payload) {
		t.Errorf("Expected content %s, got %s", payload, readBytes)
	}

	// Verify summary.json was written with SampleRange
	summaryPath := filepath.Join(tempDir, "runs", "abc1234", "docs%2Freadme_comprehension", "summary.json")
	summaryBytes, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("Failed to read summary.json: %v", err)
	}
	var summary artifacts.TaskSummary
	if err := json.Unmarshal(summaryBytes, &summary); err != nil {
		t.Fatalf("Failed to unmarshal summary.json: %v", err)
	}
	if summary.SampleRange == nil || summary.SampleRange.Start != 5 || summary.SampleRange.End != 15 {
		t.Errorf("Expected summary.SampleRange {5, 15}, got %+v", summary.SampleRange)
	}
}

func TestLocalDirSink_DirectoryTraversalRejected(t *testing.T) {
	tempDir := t.TempDir()
	sink := &artifacts.LocalDirSink{BaseDir: tempDir}

	for _, tt := range fakes.ArtifactPathValidationTestCases() {
		t.Run(tt.Name, func(t *testing.T) {
			_, err := sink.StoreArtifact(t.Context(), tt.Meta, tt.Filename, []byte("data"))
			if err == nil {
				t.Errorf("Expected error for %s, got nil", tt.Name)
			}
		})
	}
}

func TestInMemorySink_DirectoryTraversalRejected(t *testing.T) {
	sink := artifacts.NewInMemorySink()

	for _, tt := range fakes.ArtifactPathValidationTestCases() {
		t.Run(tt.Name, func(t *testing.T) {
			_, err := sink.StoreArtifact(t.Context(), tt.Meta, tt.Filename, []byte("data"))
			if err == nil {
				t.Errorf("Expected error for %s, got nil", tt.Name)
			}
		})
	}
}

func TestNoopSink_StoreComparisonAndLoad(t *testing.T) {
	sink := &artifacts.NoopSink{}
	path, err := sink.StoreComparison(t.Context(), "my-comp", []byte("{}"))
	if err != nil {
		t.Fatalf("StoreComparison returned error: %v", err)
	}
	if path != "" {
		t.Errorf("Expected empty path, got %q", path)
	}

	if _, err := sink.Load(t.Context(), "any-path"); err == nil {
		t.Errorf("Expected error from NoopSink.Load, got nil")
	}
}

func TestLocalDirSink_StoreComparisonAndLoad(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "artifacts-comp-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sink := &artifacts.LocalDirSink{BaseDir: tempDir}
	payload := []byte(`{"comparison": "comp-1"}`)

	savedPath, err := sink.StoreComparison(t.Context(), "comp-1", payload)
	if err != nil {
		t.Fatalf("StoreComparison failed: %v", err)
	}

	expectedPath := filepath.Join(tempDir, "comparisons", "comp-1.json")
	if savedPath != expectedPath {
		t.Errorf("Expected path %q, got %q", expectedPath, savedPath)
	}

	// Create and load a valid run artifact under runs/
	runDir := filepath.Join(tempDir, "runs", "commit-1", "task-1")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("Failed to create runDir: %v", err)
	}
	runPath := filepath.Join(runDir, "log.json")
	runPayload := []byte(`{"version": 2, "samples": []}`)
	if err := os.WriteFile(runPath, runPayload, 0644); err != nil {
		t.Fatalf("Failed to write runPath: %v", err)
	}

	loaded, err := sink.Load(t.Context(), runPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if string(loaded) != string(runPayload) {
		t.Errorf("Expected content %s, got %s", runPayload, loaded)
	}

	// Load comparison artifact succeeds
	loadedComp, err := sink.Load(t.Context(), savedPath)
	if err != nil {
		t.Fatalf("Load comparison failed: %v", err)
	}
	if string(loadedComp) != string(payload) {
		t.Errorf("Expected content %s, got %s", payload, loadedComp)
	}

	// Reject loading artifacts outside runs/ or comparisons/
	otherDir := filepath.Join(tempDir, "other")
	if err := os.MkdirAll(otherDir, 0755); err != nil {
		t.Fatalf("Failed to create otherDir: %v", err)
	}
	otherPath := filepath.Join(otherDir, "file.json")
	if err := os.WriteFile(otherPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("Failed to write otherPath: %v", err)
	}
	if _, err := sink.Load(t.Context(), otherPath); err == nil {
		t.Errorf("Expected error loading path outside runs/ or comparisons/, got nil")
	}

	// Test empty BaseDir in Load
	emptySink := &artifacts.LocalDirSink{BaseDir: ""}
	if _, err := emptySink.Load(t.Context(), runPath); err == nil {
		t.Errorf("Expected error on empty BaseDir in Load")
	}

	// Test traversal in StoreComparison
	if _, err := sink.StoreComparison(t.Context(), "../escape", payload); err == nil {
		t.Errorf("Expected error on directory traversal in comparison name")
	}

	// Test traversal in Load
	if _, err := sink.Load(t.Context(), filepath.Join(tempDir, "../../../etc/passwd")); err == nil {
		t.Errorf("Expected error on escaping base directory in Load")
	}
}

func TestLocalDirSink_StoreManifest(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "artifacts-manifest-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sink := &artifacts.LocalDirSink{BaseDir: tempDir}
	if !sink.IsDurable() {
		t.Errorf("Expected LocalDirSink to be durable")
	}

	meta := artifacts.RunMetadata{
		RunName:   "run-1",
		Commit:    "abcd123",
		TaskCount: 3,
	}

	payload := []byte(`{"tasks":[{"id":"t1","spec":"s1"}]}`)
	uri, err := sink.StoreManifest(t.Context(), meta, payload)
	if err != nil {
		t.Fatalf("StoreManifest failed: %v", err)
	}

	expectedPath := filepath.Join(tempDir, "runs", "abcd123", "manifest.json")
	if uri != expectedPath {
		t.Errorf("Expected URI %q, got %q", expectedPath, uri)
	}

	// Read and verify
	loaded, err := sink.Load(t.Context(), uri)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if string(loaded) != string(payload) {
		t.Errorf("Expected %s, got %s", payload, loaded)
	}

	// Idempotency: second write with same content returns same path
	uri2, err := sink.StoreManifest(t.Context(), meta, payload)
	if err != nil {
		t.Fatalf("Second StoreManifest failed: %v", err)
	}
	if uri2 != uri {
		t.Errorf("Expected matching URI on idempotent write, got %q vs %q", uri2, uri)
	}

	// Missing file returns ErrNotFound
	_, err = sink.Load(t.Context(), filepath.Join(tempDir, "runs", "abcd123", "missing.json"))
	if !artifacts.IsNotFound(err) {
		t.Errorf("Expected IsNotFound(err) to be true for missing file, got: %v", err)
	}
}

func TestInMemorySink(t *testing.T) {
	sink := artifacts.NewInMemorySink()
	if sink.IsDurable() {
		t.Errorf("Expected InMemorySink to be non-durable")
	}

	commit := "abcde123"
	prefix := sink.ArtifactPrefix(commit)
	if prefix != "mem://runs/abcde123/" {
		t.Errorf("Expected prefix mem://runs/abcde123/, got %q", prefix)
	}

	meta := artifacts.RunMetadata{
		RunName:      "run-inmem",
		RunNamespace: "eval-runner",
		Commit:       commit,
		TaskCount:    5,
		TaskID:       "task-1",
	}

	manifestData := []byte(`{"tasks":[{"id":"task-1","spec":"spec-1"}]}`)
	uri, err := sink.StoreManifest(t.Context(), meta, manifestData)
	if err != nil {
		t.Fatalf("StoreManifest failed: %v", err)
	}
	if uri != "mem://runs/abcde123/manifest.json" {
		t.Errorf("Expected URI mem://runs/abcde123/manifest.json, got %q", uri)
	}

	// Load manifest
	loaded, err := sink.Load(t.Context(), uri)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if string(loaded) != string(manifestData) {
		t.Errorf("Expected content %s, got %s", manifestData, loaded)
	}

	// Verify metadata
	metaMap, ok := sink.GetMetadata(uri)
	if !ok {
		t.Fatalf("Expected metadata for %s", uri)
	}
	if metaMap["run"] != "run-inmem" || metaMap["commit"] != commit || metaMap["task-count"] != "5" {
		t.Errorf("Unexpected metadata: %+v", metaMap)
	}

	// Idempotency: writing again with same data is no-op
	uri2, err := sink.StoreManifest(t.Context(), meta, manifestData)
	if err != nil {
		t.Fatalf("Second StoreManifest failed: %v", err)
	}
	if uri2 != uri {
		t.Errorf("Expected matching URI on idempotent write: %q vs %q", uri2, uri)
	}

	// Missing object returns ErrNotFound
	_, err = sink.Load(t.Context(), "mem://runs/abcde123/notfound.json")
	if !artifacts.IsNotFound(err) {
		t.Errorf("Expected IsNotFound(err) to be true for missing object, got: %v", err)
	}

	// Store and load task artifact
	artURI, err := sink.StoreArtifact(t.Context(), meta, "inspect.json", []byte("log data"))
	if err != nil {
		t.Fatalf("StoreArtifact failed: %v", err)
	}
	artLoaded, err := sink.Load(t.Context(), artURI)
	if err != nil {
		t.Fatalf("Load artifact failed: %v", err)
	}
	if string(artLoaded) != "log data" {
		t.Errorf("Expected 'log data', got %s", artLoaded)
	}

	// Store and load comparison
	compURI, err := sink.StoreComparison(t.Context(), "comp-1", []byte("comp data"))
	if err != nil {
		t.Fatalf("StoreComparison failed: %v", err)
	}
	compLoaded, err := sink.Load(t.Context(), compURI)
	if err != nil {
		t.Fatalf("Load comparison failed: %v", err)
	}
	if string(compLoaded) != "comp data" {
		t.Errorf("Expected 'comp data', got %s", compLoaded)
	}
}

func TestIsNotFound_ErrorsIsOnly(t *testing.T) {
	if artifacts.IsNotFound(nil) {
		t.Errorf("Expected IsNotFound(nil) to be false")
	}

	notFoundErr := fmt.Errorf("wrapped error: %w", artifacts.ErrNotFound)
	if !artifacts.IsNotFound(notFoundErr) {
		t.Errorf("Expected IsNotFound(wrapped ErrNotFound) to be true")
	}

	// Transient error on a commit whose hex contains 404
	transientErrWith404 := errors.New("rpc error: code = Unavailable desc = connection error for runs/c0404ab/manifest.json: 404")
	if artifacts.IsNotFound(transientErrWith404) {
		t.Errorf("Expected transient error containing 404 in commit hex to NOT be classified as not-found")
	}

	transientErrWithNotFoundString := errors.New("network transient failure: host not found")
	if artifacts.IsNotFound(transientErrWithNotFoundString) {
		t.Errorf("Expected transient error containing 'not found' text to NOT be classified as not-found")
	}

	transientErrNoSuchFile := errors.New("temporary failure: no such file or directory (transient mount blip)")
	if artifacts.IsNotFound(transientErrNoSuchFile) {
		t.Errorf("Expected transient error containing 'no such file or directory' to NOT be classified as not-found")
	}
}

func TestSinkClose(t *testing.T) {
	sinks := []struct {
		name string
		sink artifacts.ArtifactSink
	}{
		{"NoopSink", &artifacts.NoopSink{}},
		{"LocalDirSink", &artifacts.LocalDirSink{BaseDir: t.TempDir()}},
		{"InMemorySink", artifacts.NewInMemorySink()},
	}

	for _, tc := range sinks {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.sink.Close(); err != nil {
				t.Errorf("%s Close returned unexpected error: %v", tc.name, err)
			}
		})
	}
}
