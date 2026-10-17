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
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/discovery"
)

func TestRunSampleExecution(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("skipping e2e test: RUN_E2E is not set")
	}

	clusterName := os.Getenv("KIND_CLUSTER_NAME")
	if clusterName == "" {
		clusterName = "kind"
	}

	harness := NewHarness(t, clusterName)
	harness.Setup()

	// Deploy eval-runner controller from k8s manifests
	harness.DeployEvalRunner()

	// Deploy git server in eval-runner-sandbox namespace
	harness.DeployGitServer("eval-runner-sandbox")

	commit := harness.GetGitServerCommit("eval-runner-sandbox", "docs-eval.git")
	t.Logf("docs-eval fixture repository commit: %s", commit)

	t.Run("DocsComprehensionCompleted", func(t *testing.T) {
		runName := "docs-comprehension-sample-run"
		manifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: git://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  limit: 2
`, runName, commit)

		harness.KubectlApplyContent(manifest)

		// Wait for Run to reach Completed
		run := harness.WaitForRunPhase("eval-runner", runName, string(v1alpha1.RunPhaseCompleted), 180*time.Second)

		if run.Status.Manifest == nil {
			t.Fatalf("Expected status.manifest to be populated")
		}

		if len(run.Status.Tasks) != 2 {
			t.Fatalf("Expected 2 tasks in status.tasks, got %d: %+v", len(run.Status.Tasks), run.Status.Tasks)
		}

		expectedTasks := map[string]bool{
			"docs/readme_comprehension":       false,
			"docs/readme_comprehension_blind": false,
		}

		for _, task := range run.Status.Tasks {
			if _, ok := expectedTasks[task.ID]; !ok {
				t.Errorf("Unexpected task ID in status.tasks: %s", task.ID)
			} else {
				expectedTasks[task.ID] = true
			}

			if task.Phase != v1alpha1.TaskPhaseCompleted {
				t.Errorf("Expected task %s to be Completed, got %s (message: %s)", task.ID, task.Phase, task.Message)
			}
			if task.Samples <= 0 {
				t.Errorf("Expected task %s to have samples > 0, got %d", task.ID, task.Samples)
			}
		}

		for taskID, found := range expectedTasks {
			if !found {
				t.Errorf("Expected task %q was not found in status.tasks", taskID)
			}
		}

		// Assert task sandboxes are gone from eval-runner-sandbox
		harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", runName, 30*time.Second)

		// Assert that per-sandbox server cert secrets were deleted
		for _, task := range run.Status.Tasks {
			sbName := discovery.TaskSandboxNameForRun(runName, task.ID)
			secName := "sb-tls-" + sbName
			tlsSec := harness.runCmd("kubectl", "get", "secret", secName, "-n", "eval-runner-sandbox", "--ignore-not-found")
			if strings.TrimSpace(tlsSec) != "" {
				t.Errorf("Expected server cert secret %s to be deleted, but found:\n%s", secName, tlsSec)
			}
		}

		// Assert that no model Secret was mounted
		secretsOut := harness.runCmd("kubectl", "get", "secrets", "-n", "eval-runner-sandbox", "-o", "jsonpath={.items[*].metadata.name}")
		if strings.Contains(secretsOut, "model") || strings.Contains(secretsOut, "gemini") {
			t.Errorf("Unexpected model secret found in eval-runner-sandbox: %s", secretsOut)
		}
	})
}
