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

package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTarRoundTrip(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	subDir := filepath.Join(srcDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	if err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), []byte("hello world"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "nested.txt"), []byte("nested content"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Also create an excluded directory .venv
	venvDir := filepath.Join(srcDir, ".venv")
	if err := os.MkdirAll(venvDir, 0755); err != nil {
		t.Fatalf("MkdirAll .venv failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(venvDir, "bad.txt"), []byte("should be excluded"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	tarBytes, err := CreateTarFromDir(srcDir)
	if err != nil {
		t.Fatalf("CreateTarFromDir failed: %v", err)
	}

	if err := ExtractTar(tarBytes, destDir); err != nil {
		t.Fatalf("ExtractTar failed: %v", err)
	}

	// Verify extracted files
	b, err := os.ReadFile(filepath.Join(destDir, "hello.txt"))
	if err != nil || string(b) != "hello world" {
		t.Errorf("Unexpected hello.txt: %s, %v", string(b), err)
	}

	b, err = os.ReadFile(filepath.Join(destDir, "sub", "nested.txt"))
	if err != nil || string(b) != "nested content" {
		t.Errorf("Unexpected nested.txt: %s, %v", string(b), err)
	}

	// Verify .venv was excluded
	if _, err := os.Stat(filepath.Join(destDir, ".venv")); !os.IsNotExist(err) {
		t.Errorf("Expected .venv to be excluded, but it exists in destDir")
	}
}
