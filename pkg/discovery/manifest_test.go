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
	"path/filepath"
	"testing"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

func TestParseManifest(t *testing.T) {
	validJSON := []byte(`{
		"images": [
			{ "name": "support-agent-env:v12", "context": "hack/env" }
		],
		"tasks": [
			{
				"id": "triage/runbook_lookup",
				"workdir": "evals",
				"spec": "runbook_lookup.py@runbook_lookup",
				"resources": [ { "type": "docker" } ]
			},
			{
				"id": "triage/escalation_policy",
				"workdir": "evals",
				"spec": "escalation.py@escalation_policy",
				"images": [ "support-agent-env:v12" ]
			}
		]
	}`)

	m, err := ParseManifest(validJSON)
	if err != nil {
		t.Fatalf("ParseManifest failed unexpectedly: %v", err)
	}

	if len(m.Images) != 1 || m.Images[0].Name != "support-agent-env:v12" {
		t.Errorf("Unexpected images: %+v", m.Images)
	}
	if len(m.Tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(m.Tasks))
	}
	if m.Tasks[0].ID != "triage/runbook_lookup" {
		t.Errorf("Unexpected task 0 ID: %s", m.Tasks[0].ID)
	}
	if m.Tasks[1].ID != "triage/escalation_policy" {
		t.Errorf("Unexpected task 1 ID: %s", m.Tasks[1].ID)
	}

	// Invalid JSON test
	_, err = ParseManifest([]byte(`{ not valid json }`))
	if err == nil {
		t.Error("ParseManifest should have failed on invalid JSON")
	}
}

func TestValidateManifest(t *testing.T) {
	tests := []struct {
		name        string
		manifest    *v1alpha1.Manifest
		expectError bool
	}{
		{
			name:        "nil manifest",
			manifest:    nil,
			expectError: true,
		},
		{
			name: "empty tasks",
			manifest: &v1alpha1.Manifest{
				Tasks: []v1alpha1.ManifestTask{},
			},
			expectError: true,
		},
		{
			name: "valid manifest",
			manifest: &v1alpha1.Manifest{
				Tasks: []v1alpha1.ManifestTask{
					{ID: "task1", Spec: "a.py@task1"},
					{ID: "task2", Spec: "b.py@task2"},
				},
			},
			expectError: false,
		},
		{
			name: "empty task id",
			manifest: &v1alpha1.Manifest{
				Tasks: []v1alpha1.ManifestTask{
					{ID: "", Spec: "a.py@task1"},
				},
			},
			expectError: true,
		},
		{
			name: "whitespace task id",
			manifest: &v1alpha1.Manifest{
				Tasks: []v1alpha1.ManifestTask{
					{ID: "   ", Spec: "a.py@task1"},
				},
			},
			expectError: true,
		},
		{
			name: "duplicate task id",
			manifest: &v1alpha1.Manifest{
				Tasks: []v1alpha1.ManifestTask{
					{ID: "task1", Spec: "a.py@task1"},
					{ID: "task1", Spec: "b.py@task2"},
				},
			},
			expectError: true,
		},
		{
			name: "empty spec",
			manifest: &v1alpha1.Manifest{
				Tasks: []v1alpha1.ManifestTask{
					{ID: "task1", Spec: ""},
				},
			},
			expectError: true,
		},
		{
			name: "spec starting with dash",
			manifest: &v1alpha1.Manifest{
				Tasks: []v1alpha1.ManifestTask{
					{ID: "task1", Spec: "--flag"},
				},
			},
			expectError: true,
		},
		{
			name: "workdir with ..",
			manifest: &v1alpha1.Manifest{
				Tasks: []v1alpha1.ManifestTask{
					{ID: "task1", Spec: "eval.py@task", Workdir: "../evals"},
				},
			},
			expectError: true,
		},
		{
			name: "workdir absolute",
			manifest: &v1alpha1.Manifest{
				Tasks: []v1alpha1.ManifestTask{
					{ID: "task1", Spec: "eval.py@task", Workdir: "/evals"},
				},
			},
			expectError: true,
		},
		{
			name: "valid workdir",
			manifest: &v1alpha1.Manifest{
				Tasks: []v1alpha1.ManifestTask{
					{ID: "task1", Spec: "eval.py@task", Workdir: "evals/sub"},
				},
			},
			expectError: false,
		},
		{
			name: "too many tasks",
			manifest: func() *v1alpha1.Manifest {
				tasks := make([]v1alpha1.ManifestTask, MaxTasks+1)
				for i := range tasks {
					tasks[i] = v1alpha1.ManifestTask{
						ID:   string(rune('a'+(i%26))) + string(rune('0'+(i/26))),
						Spec: "task.py@eval",
					}
				}
				return &v1alpha1.Manifest{Tasks: tasks}
			}(),
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateManifest(tc.manifest)
			if tc.expectError && err == nil {
				t.Errorf("ValidateManifest expected error but got none")
			}
			if !tc.expectError && err != nil {
				t.Errorf("ValidateManifest unexpected error: %v", err)
			}
		})
	}
}

func TestDiscoveryPathsAgreement(t *testing.T) {
	// Sandboxd starts with --root-dir=/workspace.
	// REST ReadFile paths are interpreted relative to /workspace.
	// Therefore, joining /workspace with DiscoveryManifestReadPath must exactly yield DiscoveryManifestWritePath.
	resolvedPath := filepath.Clean(filepath.Join("/workspace", DiscoveryManifestReadPath))
	if resolvedPath != DiscoveryManifestWritePath {
		t.Errorf("Read path %q under /workspace resolved to %q, but write path is %q",
			DiscoveryManifestReadPath, resolvedPath, DiscoveryManifestWritePath)
	}

	// Verify that DiscoveryCommand creates the parent directory and writes to DiscoveryManifestWritePath
	expectedCmd := "mkdir -p " + DiscoveryManifestWorkspaceDir + " && ./.evals/list > " + DiscoveryManifestWritePath
	if DiscoveryCommand != expectedCmd {
		t.Errorf("DiscoveryCommand mismatch: got %q, want %q", DiscoveryCommand, expectedCmd)
	}

	// Verify directory and filename match write path
	expectedWritePath := filepath.Join(DiscoveryManifestWorkspaceDir, DiscoveryManifestFilename)
	if DiscoveryManifestWritePath != expectedWritePath {
		t.Errorf("DiscoveryManifestWritePath %q does not match %q", DiscoveryManifestWritePath, expectedWritePath)
	}
}
