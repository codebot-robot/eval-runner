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
	"encoding/json"
	"os"
	"testing"
	"time"
)

type Manifest struct {
	Images []ManifestImage `json:"images,omitempty"`
	Tasks  []ManifestTask  `json:"tasks"`
}

type ManifestImage struct {
	Name    string `json:"name"`
	Context string `json:"context"`
}

type ManifestTask struct {
	ID        string             `json:"id"`
	Workdir   string             `json:"workdir"`
	Spec      string             `json:"spec"`
	Resources []ManifestResource `json:"resources,omitempty"`
	Images    []string           `json:"images,omitempty"`
}

type ManifestResource struct {
	Type string `json:"type"`
}

func TestEvalDiscovery(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("skipping e2e test: RUN_E2E is not set")
	}

	clusterName := os.Getenv("KIND_CLUSTER_NAME")
	if clusterName == "" {
		clusterName = "kind"
	}

	harness := NewHarness(t, clusterName)
	harness.Setup()

	ns := harness.CreateNamespace()

	// Deploy git server serving examples/support-agent fixture
	harness.DeployGitServer(ns)

	// Deploy agent-sandbox Sandbox
	sandboxName := "eval-discovery-sandbox"
	harness.DeploySandbox(ns, sandboxName)

	// In the sandbox, clone fixture repository and execute .evals/list
	cloneURL := "git://git-server/support-agent.git"
	cmd := "rm -rf /tmp/repo && git clone -q " + cloneURL + " /tmp/repo && cd /tmp/repo && ./.evals/list"
	out := harness.ExecInPodWithRetry(ns, sandboxName, 60*time.Second, "sh", "-c", cmd)

	t.Logf("Captured .evals/list output:\n%s", out)

	var manifest Manifest
	if err := json.Unmarshal([]byte(out), &manifest); err != nil {
		t.Fatalf("Failed to parse .evals/list output as JSON manifest: %v\nOutput: %s", err, out)
	}

	if len(manifest.Tasks) != 3 {
		t.Fatalf("Expected 3 tasks in manifest, got %d", len(manifest.Tasks))
	}

	expectedTaskIDs := map[string]bool{
		"triage/runbook_lookup":    false,
		"triage/escalation_policy": false,
		"triage/summary_quality":   false,
	}

	for _, task := range manifest.Tasks {
		if _, ok := expectedTaskIDs[task.ID]; !ok {
			t.Errorf("Unexpected task id in manifest: %s", task.ID)
		} else {
			expectedTaskIDs[task.ID] = true
		}
	}

	for taskID, found := range expectedTaskIDs {
		if !found {
			t.Errorf("Expected task id %q was not found in manifest", taskID)
		}
	}
}
