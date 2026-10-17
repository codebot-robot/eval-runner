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
