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

package artifacts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNoopSink(t *testing.T) {
	sink := &NoopSink{}
	meta := RunMetadata{
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

	sink := &LocalDirSink{BaseDir: tempDir}
	meta := RunMetadata{
		RunName:      "run-123",
		RunNamespace: "test-ns",
		TaskID:       "docs/readme_comprehension",
		Model:        "mockllm/model",
		Commit:       "abc1234",
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
}

func TestLocalDirSink_DirectoryTraversalRejected(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "artifacts-traversal-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sink := &LocalDirSink{BaseDir: tempDir}
	validMeta := RunMetadata{
		RunName:      "test-run",
		RunNamespace: "eval-runner",
		TaskID:       "task-1",
	}

	tests := []struct {
		name     string
		meta     RunMetadata
		filename string
	}{
		{
			name:     "filename with double dot",
			meta:     validMeta,
			filename: "../../etc/passwd",
		},
		{
			name:     "filename with slash",
			meta:     validMeta,
			filename: "subdir/result.json",
		},
		{
			name: "taskID with double dot",
			meta: RunMetadata{
				RunName:      "test-run",
				RunNamespace: "eval-runner",
				TaskID:       "../../escape",
			},
			filename: "result.json",
		},
		{
			name: "runNamespace with double dot",
			meta: RunMetadata{
				RunName:      "test-run",
				RunNamespace: "../ns",
				TaskID:       "task-1",
			},
			filename: "result.json",
		},
		{
			name: "runName with double dot",
			meta: RunMetadata{
				RunName:      "../run",
				RunNamespace: "eval-runner",
				TaskID:       "task-1",
			},
			filename: "result.json",
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

func TestNoopSink_StoreComparisonAndLoad(t *testing.T) {
	sink := &NoopSink{}
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

	sink := &LocalDirSink{BaseDir: tempDir}
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

	// Reject loading artifacts outside runs/
	if _, err := sink.Load(t.Context(), savedPath); err == nil {
		t.Errorf("Expected error loading path outside runs/, got nil")
	}

	// Test empty BaseDir in Load
	emptySink := &LocalDirSink{BaseDir: ""}
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

	sink := &LocalDirSink{BaseDir: tempDir}
	meta := ManifestMetadata{
		RunName:      "run-manifest",
		RunNamespace: "eval-runner",
		Commit:       "commit-abc",
		TaskCount:    5,
	}

	payload := []byte(`{"tasks":[{"id":"task-1","spec":"eval.py@task1"}]}`)
	savedPath, err := sink.StoreManifest(t.Context(), meta, payload)
	if err != nil {
		t.Fatalf("StoreManifest failed: %v", err)
	}

	expectedPath := filepath.Join(tempDir, "runs", "commit-abc", "manifest.json")
	if savedPath != expectedPath {
		t.Errorf("Expected path %q, got %q", expectedPath, savedPath)
	}

	loaded, err := sink.Load(t.Context(), savedPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if string(loaded) != string(payload) {
		t.Errorf("Expected loaded content %s, got %s", payload, loaded)
	}

	// Traversal rejection
	badMeta := ManifestMetadata{Commit: "../escape"}
	if _, err := sink.StoreManifest(t.Context(), badMeta, payload); err == nil {
		t.Errorf("Expected error on traversal commit in StoreManifest")
	}
}

func TestInMemorySink(t *testing.T) {
	sink := NewInMemorySink()

	// Store and load manifest
	meta := ManifestMetadata{
		RunName:      "test-run",
		RunNamespace: "eval-runner",
		Commit:       "c12345",
		TaskCount:    2,
	}
	manifestData := []byte(`{"tasks":[{"id":"t1","spec":"eval.py@t1"},{"id":"t2","spec":"eval.py@t2"}]}`)
	manifestURI, err := sink.StoreManifest(t.Context(), meta, manifestData)
	if err != nil {
		t.Fatalf("StoreManifest failed: %v", err)
	}
	expectedURI := "mem://runs/c12345/manifest.json"
	if manifestURI != expectedURI {
		t.Errorf("Expected URI %q, got %q", expectedURI, manifestURI)
	}

	loadedManifest, err := sink.Load(t.Context(), manifestURI)
	if err != nil {
		t.Fatalf("Load with mem:// failed: %v", err)
	}
	if string(loadedManifest) != string(manifestData) {
		t.Errorf("Expected %s, got %s", manifestData, loadedManifest)
	}

	// Load without mem:// prefix
	loadedWithoutScheme, err := sink.Load(t.Context(), "runs/c12345/manifest.json")
	if err != nil {
		t.Fatalf("Load without mem:// failed: %v", err)
	}
	if string(loadedWithoutScheme) != string(manifestData) {
		t.Errorf("Expected %s, got %s", manifestData, loadedWithoutScheme)
	}

	// Store and load artifact
	artMeta := RunMetadata{
		RunName:      "test-run",
		RunNamespace: "eval-runner",
		TaskID:       "task-1",
		Commit:       "c12345",
	}
	artData := []byte(`{"version":2}`)
	artURI, err := sink.StoreArtifact(t.Context(), artMeta, "log.json", artData)
	if err != nil {
		t.Fatalf("StoreArtifact failed: %v", err)
	}
	loadedArt, err := sink.Load(t.Context(), artURI)
	if err != nil {
		t.Fatalf("Load artifact failed: %v", err)
	}
	if string(loadedArt) != string(artData) {
		t.Errorf("Expected %s, got %s", artData, loadedArt)
	}

	// Store and load comparison
	compData := []byte(`{"verdict":"IMPROVED"}`)
	compURI, err := sink.StoreComparison(t.Context(), "comp-1", compData)
	if err != nil {
		t.Fatalf("StoreComparison failed: %v", err)
	}
	loadedComp, err := sink.Load(t.Context(), compURI)
	if err != nil {
		t.Fatalf("Load comparison failed: %v", err)
	}
	if string(loadedComp) != string(compData) {
		t.Errorf("Expected %s, got %s", compData, loadedComp)
	}

	// ArtifactPrefix
	prefix := sink.ArtifactPrefix("c12345")
	if prefix != "mem://runs/c12345/" {
		t.Errorf("Expected prefix mem://runs/c12345/, got %q", prefix)
	}
	if sink.ArtifactPrefix("") != "" {
		t.Errorf("Expected empty prefix for empty commit")
	}

	// Error cases
	if _, err := sink.Load(t.Context(), "mem://runs/nonexistent/manifest.json"); err == nil {
		t.Errorf("Expected error for nonexistent path")
	}
	if _, err := sink.Load(t.Context(), ""); err == nil {
		t.Errorf("Expected error for empty path")
	}
	if _, err := sink.StoreManifest(t.Context(), ManifestMetadata{}, manifestData); err == nil {
		t.Errorf("Expected error for empty commit in StoreManifest")
	}
	if _, err := sink.StoreComparison(t.Context(), "", compData); err == nil {
		t.Errorf("Expected error for empty comparisonName")
	}
}
