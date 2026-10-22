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

package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHarness_DumpDiagnostics_CreatesDirAndWritesWhenCommandsRun(t *testing.T) {
	tempArtifacts := t.TempDir()
	t.Setenv("ARTIFACTS", tempArtifacts)

	h := NewHarness(t, "test-cluster")
	h.DumpDiagnostics()

	diagDir := filepath.Join(tempArtifacts, "diagnostics")
	if info, err := os.Stat(diagDir); err != nil || !info.IsDir() {
		t.Fatalf("expected diagnostics dir %s to exist as a directory, err: %v", diagDir, err)
	}
}
