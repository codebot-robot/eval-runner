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
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/controller"
	"github.com/gke-labs/eval-runner/pkg/discovery"
	"github.com/gke-labs/eval-runner/pkg/webhook"
	corev1 "k8s.io/api/core/v1"
)

func TestRunSampleExecution(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("skipping e2e test: RUN_E2E is not set")
	}

	harness := GetSharedHarness(t)
	t.Cleanup(func() {
		harness.runCmd("kubectl", "delete", "run", "--all", "-n", "eval-runner", "--ignore-not-found")
		harness.runCmd("kubectl", "delete", "comparison", "--all", "-n", "eval-runner", "--ignore-not-found")
		harness.WaitForAllSandboxesDeleted("eval-runner-sandbox", 30*time.Second)
	})

	commit := harness.GetGitServerCommit("eval-runner-sandbox", "docs-eval.git")
	t.Logf("docs-eval fixture repository commit: %s", commit)

	t.Run("ParallelExecutions", func(t *testing.T) {
		t.Run("DocsComprehensionCompleted", func(t *testing.T) {
			t.Parallel()
			harness := harness.WithT(t)

			modelCredentialsSecret := `
apiVersion: v1
kind: Secret
metadata:
  name: model-credentials
  namespace: eval-runner-sandbox
type: Opaque
stringData:
  GEMINI_API_KEY: dummy-key
`
			harness.KubectlApplyContent(modelCredentialsSecret)
			t.Cleanup(func() {
				_ = exec.Command("kubectl", "delete", "secret", "model-credentials", "-n", "eval-runner-sandbox", "--ignore-not-found").Run()
			})

			runName := "docs-comprehension-sample-run"
			manifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  limit: 8
`, runName, commit)

			harness.KubectlApplyContent(manifest)

			// Poll the Run every 500ms until Completed; check for live samplesDone progress
			startTime := time.Now()
			var observedProgress bool
			var progressObservedAt time.Duration
			var firstSeenRunning time.Time
			var run *v1alpha1.Run
			var podObserved bool
			for {
				cmd := exec.Command("kubectl", "get", "run", runName, "-n", "eval-runner", "-o", "json")
				out, err := cmd.Output()
				if err == nil {
					var r v1alpha1.Run
					if json.Unmarshal(out, &r) == nil {
						for _, task := range r.Status.Tasks {
							if task.Phase == v1alpha1.TaskPhaseRunning {
								if firstSeenRunning.IsZero() {
									if task.StartedAt != nil {
										firstSeenRunning = task.StartedAt.Time
									} else {
										firstSeenRunning = time.Now()
									}
								}
								if task.SamplesDone > 0 && !observedProgress {
									observedProgress = true
									progressObservedAt = time.Since(firstSeenRunning)
									t.Logf("Observed live task progress: task=%s, phase=%s, samplesDone=%d, samplesPlanned=%d, lastProgressAt=%v after %v",
										task.ID, task.Phase, task.SamplesDone, task.SamplesPlanned, task.LastProgressAt, progressObservedAt)
								}
								if !podObserved {
									sandboxName := discovery.TaskSandboxNameForRun(runName, task.ID)
									podOut, podErr := exec.Command("kubectl", "get", "pod", sandboxName, "-n", "eval-runner-sandbox", "-o", "json").CombinedOutput()
									if podErr == nil {
										var pod corev1.Pod
										if json.Unmarshal(podOut, &pod) == nil && len(pod.Spec.Containers) > 0 {
											podObserved = true
											harness.AssertPodHasPlaceholderModelCredentials(pod, "sample-runner task")
										}
									}
								}
							}
						}
						if string(r.Status.Phase) == string(v1alpha1.RunPhaseCompleted) {
							run = &r
							break
						}
					}
				}
				if time.Since(startTime) > 240*time.Second {
					t.Fatalf("Timeout waiting for Run %s to complete", runName)
				}
				time.Sleep(500 * time.Millisecond)
			}
			if !podObserved {
				t.Fatalf("Expected to observe sample-runner sandbox pod during run %s", runName)
			}
			totalDuration := time.Since(startTime)
			var taskRunningDuration time.Duration
			if !firstSeenRunning.IsZero() {
				taskRunningDuration = time.Since(firstSeenRunning)
			}

			t.Logf("Live progress observed: %v (progress observed after %v of task running; task running duration: %v, total run duration: %v)",
				observedProgress, progressObservedAt, taskRunningDuration, totalDuration)

			if !observedProgress {
				t.Errorf("expected a poll to observe task with phase: Running and samplesDone > 0 (task running duration %v, total run duration %v)", taskRunningDuration, totalDuration)
			}

			if run.Status.Discovery == nil {
				t.Fatalf("Expected status.discovery to be populated")
			}
			if run.Status.Discovery.TaskCount != 2 {
				t.Fatalf("Expected 2 tasks in discovery, got %d", run.Status.Discovery.TaskCount)
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
				if task.SamplesDone != task.Samples {
					t.Errorf("Expected task %s samplesDone (%d) to equal samples (%d)", task.ID, task.SamplesDone, task.Samples)
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
				sandboxName := discovery.TaskSandboxNameForRun(runName, task.ID)
				secretName := "sb-tls-" + sandboxName
				tlsSecret := harness.runCmd("kubectl", "get", "secret", secretName, "-n", "eval-runner-sandbox", "--ignore-not-found")
				if strings.TrimSpace(tlsSecret) != "" {
					t.Errorf("Expected server cert secret %s to be deleted, but found:\n%s", secretName, tlsSecret)
				}
			}
		})

		t.Run("ComparisonMockLLMExecution", func(t *testing.T) {
			t.Parallel()
			harness := harness.WithT(t)
			commit := harness.GetGitServerCommit("eval-runner-sandbox", "docs-eval.git")
			baseRunName := "docs-comp-base"
			candRunName := "docs-comp-cand"
			compName := "docs-mockllm-comparison"

			// Clean up any existing resources from prior runs
			harness.runCmd("kubectl", "delete", "comparison", compName, "-n", "eval-runner", "--ignore-not-found")
			harness.runCmd("kubectl", "delete", "run", baseRunName, candRunName, "-n", "eval-runner", "--ignore-not-found")

			baseManifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  limit: 10
`, baseRunName, commit)

			candManifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  limit: 10
`, candRunName, commit)

			compManifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Comparison
metadata:
  name: %s
  namespace: eval-runner
spec:
  baseline:
    run: %s
  candidate:
    run: %s
  equivalenceMargin: 0.05
`, compName, baseRunName, candRunName)

			harness.KubectlApplyContent(baseManifest)
			harness.KubectlApplyContent(candManifest)
			harness.KubectlApplyContent(compManifest)

			// Wait for both Runs to complete
			harness.WaitForRunPhase("eval-runner", baseRunName, string(v1alpha1.RunPhaseCompleted), 240*time.Second)
			harness.WaitForRunPhase("eval-runner", candRunName, string(v1alpha1.RunPhaseCompleted), 240*time.Second)

			// Wait for Comparison to reach terminal verdict Unchanged
			comp := harness.WaitForComparisonVerdict("eval-runner", compName, string(v1alpha1.ComparisonVerdictUnchanged), 60*time.Second)

			if comp.Status.Phase != v1alpha1.ComparisonPhaseCompleted {
				t.Errorf("Expected comparison phase Completed, got %s", comp.Status.Phase)
			}
			if comp.Status.Verdict != v1alpha1.ComparisonVerdictUnchanged {
				t.Errorf("Expected comparison verdict UNCHANGED, got %s", comp.Status.Verdict)
			}
			if comp.Status.PImprovement == nil || math.Abs(*comp.Status.PImprovement-0.50) > 1e-4 {
				t.Errorf("Expected pImprovement 0.50, got %v", comp.Status.PImprovement)
			}
			if len(comp.Status.Tasks) != 2 {
				t.Fatalf("Expected 2 tasks in comparison status, got %d", len(comp.Status.Tasks))
			}
			for _, task := range comp.Status.Tasks {
				if task.Label != v1alpha1.ComparisonTaskLabelUnchanged {
					t.Errorf("Expected task %s label unchanged, got %s", task.ID, task.Label)
				}
				if task.Pairs != 10 {
					t.Errorf("Expected task %s pairs 10, got %d", task.ID, task.Pairs)
				}
				if task.Wins != 0 || task.Losses != 0 || task.Ties != 10 {
					t.Errorf("Expected task %s wins/losses/ties 0/0/10, got %d/%d/%d", task.ID, task.Wins, task.Losses, task.Ties)
				}
				if task.Delta == nil || math.Abs(*task.Delta) > 1e-4 {
					t.Errorf("Expected task %s delta 0.0, got %v", task.ID, task.Delta)
				}
			}

			if comp.Status.SummaryPath == "" {
				t.Errorf("Expected non-empty summaryPath in comparison status")
			}

			// Verify evalstats -from-comparison output
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatalf("Failed to get home dir: %v", err)
			}
			kubeconfigPath := filepath.Join(home, ".kube", "config")
			statsOut := harness.runCmd("go", "run", "github.com/gke-labs/eval-runner/cmd/evalstats", "-kubeconfig", kubeconfigPath, "-from-comparison", compName, "-namespace", "eval-runner")
			if !strings.Contains(statsOut, "UNCHANGED") {
				t.Errorf("Expected evalstats output to contain UNCHANGED, got:\n%s", statsOut)
			}
			if !strings.Contains(statsOut, "eval") || !strings.Contains(statsOut, "base") || !strings.Contains(statsOut, "this PR") || !strings.Contains(statsOut, "wins/losses/ties") {
				t.Errorf("Expected evalstats output to contain table header with wins/losses/ties, got:\n%s", statsOut)
			}

			// Clean up
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", baseRunName, 30*time.Second)
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", candRunName, 30*time.Second)
		})

		t.Run("SampleRangeExecution", func(t *testing.T) {
			t.Parallel()
			harness := harness.WithT(t)
			run1Name := "sample-range-run-1"
			run2Name := "sample-range-run-2"
			candRunName := "sample-range-run-cand"
			compName := "sample-range-comparison"

			// Clean up any previous runs or comparisons
			harness.runCmd("kubectl", "delete", "run", run1Name, run2Name, candRunName, "-n", "eval-runner", "--ignore-not-found")
			harness.runCmd("kubectl", "delete", "comparison", compName, "-n", "eval-runner", "--ignore-not-found")

			run1Manifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  sampleRange:
    start: 1
    end: 5
`, run1Name, commit)

			run2Manifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  sampleRange:
    start: 6
    end: 10
`, run2Name, commit)

			runCandManifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  sampleRange:
    start: 1
    end: 5
`, candRunName, commit)

			compManifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Comparison
metadata:
  name: %s
  namespace: eval-runner
spec:
  baseline:
    run: %s
  candidate:
    run: %s
  equivalenceMargin: 0.05
`, compName, run1Name, candRunName)

			harness.KubectlApplyContent(run1Manifest)
			harness.KubectlApplyContent(run2Manifest)
			harness.KubectlApplyContent(runCandManifest)
			harness.KubectlApplyContent(compManifest)

			// Wait for all three Runs to complete
			run1 := harness.WaitForRunPhase("eval-runner", run1Name, string(v1alpha1.RunPhaseCompleted), 240*time.Second)
			run2 := harness.WaitForRunPhase("eval-runner", run2Name, string(v1alpha1.RunPhaseCompleted), 240*time.Second)
			harness.WaitForRunPhase("eval-runner", candRunName, string(v1alpha1.RunPhaseCompleted), 240*time.Second)

			// Assert status.tasks[].samples is 5 each
			if len(run1.Status.Tasks) == 0 {
				t.Fatalf("Expected tasks in run1 status.tasks, got 0")
			}
			for _, task := range run1.Status.Tasks {
				if task.Phase != v1alpha1.TaskPhaseCompleted {
					t.Errorf("Expected run1 task %s to be Completed, got %s", task.ID, task.Phase)
				}
				if task.Samples != 5 {
					t.Errorf("Expected run1 task %s to have samples=5, got %d", task.ID, task.Samples)
				}
				if task.SampleRange == nil || task.SampleRange.Start != 1 || task.SampleRange.End != 5 {
					t.Errorf("Expected run1 task %s to have sampleRange {1, 5}, got %+v", task.ID, task.SampleRange)
				}
			}

			if len(run2.Status.Tasks) == 0 {
				t.Fatalf("Expected tasks in run2 status.tasks, got 0")
			}
			for _, task := range run2.Status.Tasks {
				if task.Phase != v1alpha1.TaskPhaseCompleted {
					t.Errorf("Expected run2 task %s to be Completed, got %s", task.ID, task.Phase)
				}
				if task.Samples != 5 {
					t.Errorf("Expected run2 task %s to have samples=5, got %d", task.ID, task.Samples)
				}
				if task.SampleRange == nil || task.SampleRange.Start != 6 || task.SampleRange.End != 10 {
					t.Errorf("Expected run2 task %s to have sampleRange {6, 10}, got %+v", task.ID, task.SampleRange)
				}
			}

			// Read stored Inspect logs for run1 and run2 to assert disjoint sample IDs
			nodeContainer := fmt.Sprintf("%s-control-plane", harness.clusterName)
			loadInspectSampleIDs := func(task v1alpha1.TaskStatus) map[string]bool {
				logSuffix := strings.TrimPrefix(task.LogPath, "/tmp/")
				findCmd := fmt.Sprintf("find /var/lib/kubelet/pods -ignore_readdir_race -path '*/%s' -type f", logSuffix)
				foundFile := strings.TrimSpace(harness.runCmd("docker", "exec", nodeContainer, "sh", "-c", findCmd))
				if foundFile == "" {
					t.Fatalf("Log file not found on node for %s", task.LogPath)
				}
				firstFile := strings.Split(foundFile, "\n")[0]
				logContent := harness.runCmd("docker", "exec", nodeContainer, "cat", firstFile)
				scores, err := artifacts.ParseInspectSampleScores([]byte(logContent))
				if err != nil {
					t.Fatalf("Failed to parse sample scores for %s: %v", task.ID, err)
				}
				ids := make(map[string]bool)
				for _, s := range scores {
					ids[s.ID] = true
				}
				return ids
			}

			idsRun1 := loadInspectSampleIDs(run1.Status.Tasks[0])
			idsRun2 := loadInspectSampleIDs(run2.Status.Tasks[0])

			if len(idsRun1) != 5 {
				t.Errorf("Expected 5 sample IDs in run1, got %d: %+v", len(idsRun1), idsRun1)
			}
			if len(idsRun2) != 5 {
				t.Errorf("Expected 5 sample IDs in run2, got %d: %+v", len(idsRun2), idsRun2)
			}

			for id := range idsRun1 {
				if idsRun2[id] {
					t.Errorf("Expected disjoint sample IDs, but ID %s appears in both run1 and run2", id)
				}
			}

			// Wait for Comparison to reach Completed phase
			comp := harness.WaitForComparisonPhase("eval-runner", compName, string(v1alpha1.ComparisonPhaseCompleted), 60*time.Second)
			if len(comp.Status.Tasks) == 0 {
				t.Fatalf("Expected comparison tasks, got 0")
			}
			for _, task := range comp.Status.Tasks {
				if task.Pairs != 5 {
					t.Errorf("Expected task %s comparison pairs=5, got %d", task.ID, task.Pairs)
				}
			}

			// Clean up
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", run1Name, 30*time.Second)
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", run2Name, 30*time.Second)
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", candRunName, 30*time.Second)
		})

		t.Run("ForkCandidateComparisonExecution", func(t *testing.T) {
			t.Parallel()
			harness := harness.WithT(t)

			// 1. Verify eval-runner-controller Role permissions via kubectl auth can-i
			for _, action := range []struct {
				verb     string
				resource string
			}{
				{"create", "comparisons"},
				{"update", "comparisons"},
				{"patch", "comparisons"},
				{"delete", "comparisons"},
				{"create", "runs"},
				{"update", "runs"},
				{"patch", "runs"},
				{"delete", "runs"},
			} {
				out := harness.runCmd("kubectl", "auth", "can-i", action.verb, action.resource,
					"--as=system:serviceaccount:eval-runner:eval-runner-controller", "-n", "eval-runner")
				if strings.TrimSpace(out) != "yes" {
					t.Errorf("expected eval-runner-controller to have %s %s permission in eval-runner, got %s",
						action.verb, action.resource, out)
				}
			}

			// 2. Fetch base and fork commits from in-cluster git server
			baseCommit := harness.GetGitServerCommit("eval-runner-sandbox", "gke-labs/eval-runner.git")
			forkCommit := harness.GetGitServerCommit("eval-runner-sandbox", "codebot-robot/eval-runner.git")
			if baseCommit == "" || forkCommit == "" {
				t.Fatalf("failed to retrieve git commits: base=%q, fork=%q", baseCommit, forkCommit)
			}
			if baseCommit == forkCommit {
				t.Fatalf("expected fork commit to differ from base commit, both are %s", baseCommit)
			}

			// 3. Port-forward to webhook service
			baseURL, webhookCleanup := harness.PortForwardWebhook(0)
			defer webhookCleanup()

			prNum := 9876
			candRunName := fmt.Sprintf("pr-%d-%s", prNum, forkCommit[:7])
			baseRunName := fmt.Sprintf("base-%s", baseCommit[:7])
			compName := fmt.Sprintf("pr-%d", prNum)

			t.Cleanup(func() {
				harness.runCmd("kubectl", "delete", "comparison", compName, "-n", "eval-runner", "--ignore-not-found")
				harness.runCmd("kubectl", "delete", "run", candRunName, baseRunName, "-n", "eval-runner", "--ignore-not-found")
			})

			// 4. Post signed pull_request event driving the consumer through the webhook path
			payload := fmt.Sprintf(`{
			"action": "opened",
			"number": %d,
			"pull_request": {
			"number": %d,
			"state": "open",
			"head": {
			"sha": "%s",
			"ref": "feature",
			"repo": {
			"name": "eval-runner",
			"full_name": "codebot-robot/eval-runner",
			"clone_url": "http://git-server.eval-runner-sandbox.svc/codebot-robot/eval-runner.git",
			"owner": {"login": "codebot-robot"}
			}
			},
			"base": {
			"sha": "%s",
			"ref": "main",
			"repo": {
			"name": "eval-runner",
			"full_name": "gke-labs/eval-runner",
			"clone_url": "http://git-server.eval-runner-sandbox.svc/gke-labs/eval-runner.git",
			"owner": {"login": "gke-labs"}
			}
			}
			},
			"repository": {
			"name": "eval-runner",
			"full_name": "gke-labs/eval-runner",
			"clone_url": "http://git-server.eval-runner-sandbox.svc/gke-labs/eval-runner.git",
			"owner": {"login": "gke-labs"}
			}
			}`, prNum, prNum, forkCommit, baseCommit)

			sig := webhook.ComputeSignature([]byte(payload), []byte("e2e-webhook-secret"))
			req, err := http.NewRequest(http.MethodPost, baseURL+"/webhook", strings.NewReader(payload))
			if err != nil {
				t.Fatalf("failed to create webhook request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(webhook.HeaderGitHubEvent, webhook.EventPullRequest)
			req.Header.Set(webhook.HeaderHubSignature256, sig)
			req.Header.Set(webhook.HeaderGitHubDelivery, "e2e-fork-candidate-delivery")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("failed to POST pull_request event: %v", err)
			}
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("webhook returned HTTP %d: %s", resp.StatusCode, string(respBody))
			}
			if !strings.Contains(string(respBody), "ok: enqueued pull_request") {
				t.Fatalf("expected webhook to enqueue pull_request, got: %s", string(respBody))
			}

			// 5. Assert Candidate Run, Baseline Run, and Comparison are created by the consumer
			candRun := harness.WaitForRunExists("eval-runner", candRunName, 30*time.Second)
			if candRun.Spec.Repo.URL != "http://git-server.eval-runner-sandbox.svc/codebot-robot/eval-runner.git" {
				t.Errorf("expected candidate repo URL to be the fork, got %s", candRun.Spec.Repo.URL)
			}
			if candRun.Spec.Repo.Commit != forkCommit {
				t.Errorf("expected candidate repo commit %s, got %s", forkCommit, candRun.Spec.Repo.Commit)
			}

			baseRun := harness.WaitForRunExists("eval-runner", baseRunName, 30*time.Second)
			if baseRun.Spec.Repo.URL != "http://git-server.eval-runner-sandbox.svc/gke-labs/eval-runner.git" {
				t.Errorf("expected baseline repo URL to be the base repo, got %s", baseRun.Spec.Repo.URL)
			}

			comp := harness.WaitForComparisonExists("eval-runner", compName, 30*time.Second)
			if comp.Spec.Candidate.Run != candRunName {
				t.Errorf("expected comparison candidate run %s, got %s", candRunName, comp.Spec.Candidate.Run)
			}
			if comp.Spec.Baseline.Run != baseRunName {
				t.Errorf("expected comparison baseline run %s, got %s", baseRunName, comp.Spec.Baseline.Run)
			}

			// 6. Wait for candidate Run (cloning from fork bare repo) and baseline Run to reach Completed
			harness.WaitForRunPhase("eval-runner", candRunName, string(v1alpha1.RunPhaseCompleted), 240*time.Second)
			harness.WaitForRunPhase("eval-runner", baseRunName, string(v1alpha1.RunPhaseCompleted), 240*time.Second)

			// 7. Wait for Comparison to reach terminal verdict
			harness.WaitForComparisonVerdict("eval-runner", compName, string(v1alpha1.ComparisonVerdictUnchanged), 60*time.Second)

			// 8. Clean up sandboxes
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", candRunName, 30*time.Second)
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", baseRunName, 30*time.Second)
		})

		t.Run("SamplingPairedBatchLoop", func(t *testing.T) {
			t.Parallel()
			harness := harness.WithT(t)

			candRunName := "batch-cand-run"
			baseRunName := "batch-base-run"
			compName := "batch-loop-comp"

			// Clean up any previous runs or comparisons
			harness.runCmd("kubectl", "delete", "run", candRunName, baseRunName, fmt.Sprintf("%s-b2-cand", compName), fmt.Sprintf("%s-b2-base", compName), "-n", "eval-runner", "--ignore-not-found")
			harness.runCmd("kubectl", "delete", "comparison", compName, "-n", "eval-runner", "--ignore-not-found")

			candManifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  sampleRange:
    start: 1
    end: 4
`, candRunName, commit)

			baseManifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  sampleRange:
    start: 1
    end: 4
`, baseRunName, commit)

			compManifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Comparison
metadata:
  name: %s
  namespace: eval-runner
spec:
  candidate:
    run: %s
  baseline:
    run: %s
  sampling:
    batchSize: 4
    maxSamplesPerTask: 12
  equivalenceMargin: 0.05
`, compName, candRunName, baseRunName)

			harness.KubectlApplyContent(candManifest)
			harness.KubectlApplyContent(baseManifest)
			harness.KubectlApplyContent(compManifest)

			// Wait for Comparison to reach terminal Completed phase (having executed multiple batches)
			comp := harness.WaitForComparisonPhase("eval-runner", compName, string(v1alpha1.ComparisonPhaseCompleted), 240*time.Second)

			if len(comp.Status.Batches) < 2 {
				t.Fatalf("Expected at least 2 batches created, got %d (batches: %+v)", len(comp.Status.Batches), comp.Status.Batches)
			}

			b1 := comp.Status.Batches[0]
			if b1.Index != 1 || b1.CandidateRun != candRunName || b1.BaselineRun != baseRunName {
				t.Errorf("Unexpected batch 1 status: %+v", b1)
			}

			b2 := comp.Status.Batches[1]
			if b2.Index != 2 {
				t.Errorf("Expected batch 2 index 2, got %d", b2.Index)
			}
			if b2.SampleRange == nil || b2.SampleRange.Start != 5 || b2.SampleRange.End != 8 {
				t.Errorf("Expected batch 2 range [5, 8], got %v", b2.SampleRange)
			}

			if len(comp.Status.Tasks) == 0 {
				t.Fatalf("Expected status.tasks to be populated, got 0")
			}
			for _, task := range comp.Status.Tasks {
				if task.SamplesRun < 8 {
					t.Errorf("Expected task %s samplesRun >= 8 across batches, got %d", task.ID, task.SamplesRun)
				}
				if task.Decision == "" {
					t.Errorf("Expected task %s decision to be recorded, got empty", task.ID)
				}
			}

			// Clean up
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", candRunName, 30*time.Second)
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", baseRunName, 30*time.Second)
			b2CandName := fmt.Sprintf("%s-b2-cand", compName)
			b2BaseName := fmt.Sprintf("%s-b2-base", compName)
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", b2CandName, 30*time.Second)
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", b2BaseName, 30*time.Second)
		})

		t.Run("BaselineBatchRunReuse", func(t *testing.T) {
			t.Parallel()
			harness := harness.WithT(t)

			// Use an isolated commit so batch-key labels and artifact paths never collide
			// with SamplingPairedBatchLoop running concurrently on HEAD.
			reuseBranchName := fmt.Sprintf("reuse-test-%d", time.Now().UnixNano()%1000000)
			reuseCommit, cleanupBranch := harness.CreateGitServerCommit("eval-runner-sandbox", "docs-eval.git", reuseBranchName, "feat: baseline batch reuse commit")
			t.Cleanup(cleanupBranch)

			baseRunName := "reuse-base-run"
			cand1RunName := "reuse-cand1-run"
			cand2RunName := "reuse-cand2-run"
			comp1Name := "reuse-comp-1"
			comp2Name := "reuse-comp-2"

			// Clean up any existing resources
			harness.runCmd("kubectl", "delete", "comparison", comp1Name, comp2Name, "-n", "eval-runner", "--ignore-not-found")
			harness.runCmd("kubectl", "delete", "run", baseRunName, cand1RunName, cand2RunName, "-n", "eval-runner", "--ignore-not-found")
			harness.runCmd("kubectl", "delete", "run", fmt.Sprintf("%s-b2-cand", comp1Name), fmt.Sprintf("%s-b2-base", comp1Name), "-n", "eval-runner", "--ignore-not-found")
			harness.runCmd("kubectl", "delete", "run", fmt.Sprintf("%s-b2-cand", comp2Name), fmt.Sprintf("%s-b2-base", comp2Name), "-n", "eval-runner", "--ignore-not-found")

			manifest1 := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  sampleRange:
    start: 1
    end: 4
---
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  sampleRange:
    start: 1
    end: 4
---
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Comparison
metadata:
  name: %s
  namespace: eval-runner
spec:
  candidate:
    run: %s
  baseline:
    run: %s
  sampling:
    batchSize: 4
    maxSamplesPerTask: 12
`, cand1RunName, reuseCommit, baseRunName, reuseCommit, comp1Name, cand1RunName, baseRunName)

			harness.KubectlApplyContent(manifest1)

			// Wait for first Comparison to reach Completed (batch 1 + batch 2)
			comp1 := harness.WaitForComparisonPhase("eval-runner", comp1Name, string(v1alpha1.ComparisonPhaseCompleted), 300*time.Second)

			if len(comp1.Status.Batches) < 2 {
				t.Fatalf("Expected comparison 1 to run at least 2 batches, got %d", len(comp1.Status.Batches))
			}
			comp1B2Base := comp1.Status.Batches[1].BaselineRun

			// Now launch second Comparison sharing the same baseline commit and model with a new candidate run
			manifest2 := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  sampleRange:
    start: 1
    end: 4
---
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Comparison
metadata:
  name: %s
  namespace: eval-runner
spec:
  candidate:
    run: %s
  baseline:
    run: %s
  sampling:
    batchSize: 4
    maxSamplesPerTask: 12
`, cand2RunName, reuseCommit, comp2Name, cand2RunName, baseRunName)

			harness.KubectlApplyContent(manifest2)

			// Wait for second Comparison to reach Completed
			comp2 := harness.WaitForComparisonPhase("eval-runner", comp2Name, string(v1alpha1.ComparisonPhaseCompleted), 300*time.Second)

			if len(comp2.Status.Batches) < 2 {
				t.Fatalf("Expected comparison 2 to run at least 2 batches, got %d", len(comp2.Status.Batches))
			}

			// Verify batch 2 of comparison 2 reused the baseline batch Run from comparison 1!
			if comp2.Status.Batches[1].BaselineRun != comp1B2Base {
				t.Errorf("Expected comparison 2 to reuse baseline run %q, got %q", comp1B2Base, comp2.Status.Batches[1].BaselineRun)
			}

			// Verify that a new baseline run was NOT created for comparison 2
			unwantedBase := fmt.Sprintf("%s-b2-base", comp2Name)
			out := harness.runCmd("kubectl", "get", "run", unwantedBase, "-n", "eval-runner", "--ignore-not-found")
			if strings.TrimSpace(out) != "" {
				t.Errorf("Expected %s not to be created, but found: %s", unwantedBase, out)
			}

			// Clean up sandboxes
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", cand1RunName, 30*time.Second)
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", cand2RunName, 30*time.Second)
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", baseRunName, 30*time.Second)
		})

		t.Run("PollingWithoutWebhook_NewBranchCreatesRun", func(t *testing.T) {
			t.Parallel()
			harness := harness.WithT(t)

			branchName := fmt.Sprintf("poll-test-%d", time.Now().UnixNano()%1000000)
			pollInterval := 5 * time.Second
			// Track only the unique test branch so the syncer does not spawn an unrelated Run for main.
			configPatch := fmt.Sprintf(`{"data":{"lsRemoteInterval":"%v","repositories":"- name: http://git-server.eval-runner-sandbox.svc/docs-eval.git\n  branches: [%s]\n"}}`, pollInterval, branchName)
			harness.runCmd("kubectl", "patch", "configmap", "eval-runner-config", "-n", "eval-runner", "--type=merge", "-p", configPatch)
			t.Cleanup(func() {
				restorePatch := `{"data":{"lsRemoteInterval":"60s","repositories":""}}`
				harness.runCmd("kubectl", "patch", "configmap", "eval-runner-config", "-n", "eval-runner", "--type=merge", "-p", restorePatch)
			})
			time.Sleep(2 * time.Second)

			// Create a new commit and push a new branch in git-server with author identity set
			pushStart := time.Now()
			newCommit, cleanupBranch := harness.CreateGitServerCommit("eval-runner-sandbox", "docs-eval.git", branchName, "feat: poll commit")
			t.Cleanup(cleanupBranch)
			newShortSHA := newCommit[:7]
			baseRunName := fmt.Sprintf("base-%s", newShortSHA)

			maxWait := 3 * pollInterval
			timingStartMsg := fmt.Sprintf("poll timing: push initiated at %s, waiting for baseline Run %s within three poll intervals (%v)",
				pushStart.Format(time.RFC3339), baseRunName, maxWait)
			t.Log(timingStartMsg)
			fmt.Println(timingStartMsg)

			r := harness.WaitForRunExists("eval-runner", baseRunName, maxWait)
			if r == nil {
				t.Fatalf("expected baseline run %s to be created within three poll intervals (%v), but was not found", baseRunName, maxWait)
			}
			duration := time.Since(pushStart)
			timingEndMsg := fmt.Sprintf("poll timing: branch %s pushed at %s, Run %s created at %s (elapsed %v <= max %v)",
				branchName, pushStart.Format(time.RFC3339), baseRunName, time.Now().Format(time.RFC3339), duration, maxWait)
			t.Log(timingEndMsg)
			fmt.Println(timingEndMsg)

			// Wait for Run to complete and clean up sandboxes
			harness.WaitForRunPhase("eval-runner", baseRunName, "Completed", 4*time.Minute)
			harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", baseRunName, 30*time.Second)
		})
	})

	harness.runCmd("kubectl", "delete", "run", "--all", "-n", "eval-runner", "--ignore-not-found")
	harness.runCmd("kubectl", "delete", "comparison", "--all", "-n", "eval-runner", "--ignore-not-found")
	harness.WaitForAllSandboxesDeleted("eval-runner-sandbox", 30*time.Second)

	t.Run("GCSEmulatorDiscoveryAndExecution", func(t *testing.T) {
		bucket := "test-eval-bucket"
		harness.DeployFakeGCSServer("eval-runner", bucket)
		harness.ConfigureGCSSink(bucket)
		t.Cleanup(func() {
			harness.ConfigureLocalDirSink()
		})

		runName := "base-gcs-sample-run"
		compCandRun := "gcs-batch-cand"
		compBaseRun := "gcs-batch-base"
		samplingCompName := "gcs-sampling-comp"
		samplingBatchSelector := fmt.Sprintf("%s=%s", controller.LabelComparison, samplingCompName)

		// Use a separate commit for the sampling comparison so its GCS artifact paths and
		// batch-key labels are completely independent from base-gcs-sample-run, allowing
		// both workloads to execute concurrently against fake-gcs-server.
		samplingBranchName := fmt.Sprintf("gcs-sampling-%d", time.Now().UnixNano()%1000000)
		samplingCommit, cleanupSamplingBranch := harness.CreateGitServerCommit("eval-runner-sandbox", "docs-eval.git", samplingBranchName, "feat: gcs sampling commit")
		t.Cleanup(cleanupSamplingBranch)

		// Clean up any existing resources
		harness.runCmd("kubectl", "delete", "comparison", samplingCompName, "-n", "eval-runner", "--ignore-not-found")
		harness.runCmd("kubectl", "delete", "run", runName, compCandRun, compBaseRun, "-n", "eval-runner", "--ignore-not-found")
		harness.runCmd("kubectl", "delete", "run", "-n", "eval-runner", "-l", samplingBatchSelector, "--ignore-not-found")

		// The objects created below live in the fake-gcs sink (gs:// log paths). Delete them,
		// and wait until they are gone, before the sink is restored. t.Cleanup runs LIFO, so
		// registering this after the ConfigureLocalDirSink cleanup above guarantees the ordering,
		// including on test failure.
		t.Cleanup(func() {
			harness.runCmd("kubectl", "delete", "comparison", samplingCompName, "-n", "eval-runner", "--ignore-not-found", "--wait=false")
			harness.runCmd("kubectl", "delete", "run", runName, compCandRun, compBaseRun, "-n", "eval-runner", "--ignore-not-found", "--wait=false")
			harness.runCmd("kubectl", "delete", "run", "-n", "eval-runner", "-l", samplingBatchSelector, "--ignore-not-found", "--wait=false")
			harness.WaitForComparisonDeleted("eval-runner", samplingCompName, 60*time.Second)
			harness.WaitForRunDeleted("eval-runner", runName, 60*time.Second)
			harness.WaitForRunDeleted("eval-runner", compCandRun, 60*time.Second)
			harness.WaitForRunDeleted("eval-runner", compBaseRun, 60*time.Second)
			harness.WaitForRunsWithLabelDeleted("eval-runner", samplingBatchSelector, 60*time.Second)
		})

		manifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
  labels:
    evalrunner.gke-labs.dev/branch: main
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  limit: 1
`, runName, commit)

		compCandManifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  sampleRange:
    start: 1
    end: 4
`, compCandRun, samplingCommit)

		compBaseManifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: mockllm/model
  sampleRange:
    start: 1
    end: 4
`, compBaseRun, samplingCommit)

		samplingCompManifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Comparison
metadata:
  name: %s
  namespace: eval-runner
spec:
  candidate:
    run: %s
  baseline:
    run: %s
  sampling:
    batchSize: 4
    maxSamplesPerTask: 12
  equivalenceMargin: 0.05
`, samplingCompName, compCandRun, compBaseRun)

		// Apply both the single-run and sampling-comparison workloads up front so they run concurrently.
		harness.KubectlApplyContent(manifest)
		harness.KubectlApplyContent(compCandManifest)
		harness.KubectlApplyContent(compBaseManifest)
		harness.KubectlApplyContent(samplingCompManifest)

		// Wait for Run to reach Completed
		run := harness.WaitForRunPhase("eval-runner", runName, string(v1alpha1.RunPhaseCompleted), 240*time.Second)

		// Verify discovery populated GCS manifest path and hash
		if run.Status.Discovery == nil {
			t.Fatalf("Expected status.discovery to be populated")
		}
		expectedManifestPath := fmt.Sprintf("gs://%s/runs/%s/manifest.json", bucket, commit)
		if run.Status.Discovery.ManifestPath != expectedManifestPath {
			t.Errorf("Expected manifestPath %q, got %q", expectedManifestPath, run.Status.Discovery.ManifestPath)
		}
		if !strings.HasPrefix(run.Status.Discovery.ManifestHash, "sha256:") {
			t.Errorf("Expected ManifestHash to start with sha256:, got %s", run.Status.Discovery.ManifestHash)
		}

		// Verify sample task completed and stored log in GCS
		if len(run.Status.Tasks) == 0 {
			t.Fatalf("Expected tasks in status.tasks, got 0")
		}
		for _, task := range run.Status.Tasks {
			if task.Phase != v1alpha1.TaskPhaseCompleted {
				t.Errorf("Expected task %s to be Completed, got %s (message: %s)", task.ID, task.Phase, task.Message)
			}
			expectedLogPrefix := fmt.Sprintf("gs://%s/runs/%s/%s/", bucket, commit, task.ID)
			if !strings.HasPrefix(task.LogPath, expectedLogPrefix) {
				t.Errorf("Expected task logPath to start with %q, got %q", expectedLogPrefix, task.LogPath)
			}
		}

		// Verify objects actually exist in fake-gcs-server
		objectsJSON := harness.GetGCSObjects("eval-runner", bucket)
		t.Logf("Objects in fake-gcs-server:\n%s", objectsJSON)
		if !strings.Contains(objectsJSON, fmt.Sprintf("runs/%s/manifest.json", commit)) {
			t.Errorf("Expected manifest.json to be stored in fake-gcs-server, but object list was:\n%s", objectsJSON)
		}
		for _, task := range run.Status.Tasks {
			if !strings.Contains(objectsJSON, fmt.Sprintf("runs/%s/%s/summary.json", commit, task.ID)) {
				t.Errorf("Expected summary.json for task %s to be stored in fake-gcs-server", task.ID)
			}
		}

		var objectListing struct {
			Items []struct {
				Name            string `json:"name"`
				ContentEncoding string `json:"contentEncoding"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(objectsJSON), &objectListing); err != nil {
			t.Fatalf("failed to parse GCS objects listing: %v", err)
		}
		encodingByName := make(map[string]string)
		for _, item := range objectListing.Items {
			encodingByName[item.Name] = item.ContentEncoding
		}

		// Verify trajectory viewer pages via UI port-forward
		runUISmoke(t, harness, runName, true)

		baseURL, uiCleanup := harness.PortForwardUI(0)
		defer uiCleanup()

		for _, task := range run.Status.Tasks {
			objectPath := strings.TrimPrefix(task.LogPath, fmt.Sprintf("gs://%s/", bucket))
			if encoding := encodingByName[objectPath]; encoding != "gzip" {
				t.Errorf("expected GCS object %s contentEncoding to be %q, got %q", objectPath, "gzip", encoding)
			}
			rawLog := harness.GetGCSObject("eval-runner", bucket, objectPath)
			expectedScores, err := artifacts.ParseInspectSampleScores(rawLog)
			if err != nil {
				t.Fatalf("failed to parse sample scores for task %s from GCS: %v", task.ID, err)
			}
			if len(expectedScores) == 0 {
				t.Fatalf("expected at least 1 sample score for task %s, got 0", task.ID)
			}

			// 1. GET /runs/{ns}/{name}/tasks/{task}/samples
			samplesURL := fmt.Sprintf("%s/runs/eval-runner/%s/tasks/%s/samples", baseURL, runName, url.PathEscape(task.ID))
			resp, err := http.Get(samplesURL)
			if err != nil {
				t.Fatalf("failed to GET %s: %v", samplesURL, err)
			}
			samplesBody, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("unexpected status %d for %s: %s", resp.StatusCode, samplesURL, string(samplesBody))
			}
			for _, sc := range expectedScores {
				if !strings.Contains(string(samplesBody), sc.ID) {
					t.Errorf("expected samples list to contain sample ID %q, got body:\n%s", sc.ID, string(samplesBody))
				}
			}

			// 2. GET /runs/{ns}/{name}/tasks/{task}/samples/{sample} for each sample
			for _, sc := range expectedScores {
				transcriptURL := fmt.Sprintf("%s/runs/eval-runner/%s/tasks/%s/samples/%s", baseURL, runName, url.PathEscape(task.ID), url.PathEscape(sc.ID))
				respT, err := http.Get(transcriptURL)
				if err != nil {
					t.Fatalf("failed to GET %s: %v", transcriptURL, err)
				}
				transcriptBody, err := io.ReadAll(respT.Body)
				respT.Body.Close()
				if respT.StatusCode != http.StatusOK {
					t.Fatalf("unexpected status %d for %s: %s", respT.StatusCode, transcriptURL, string(transcriptBody))
				}
				if !strings.Contains(string(transcriptBody), sc.ID) {
					t.Errorf("expected sample transcript to contain sample ID %q, got body:\n%s", sc.ID, string(transcriptBody))
				}
			}
		}

		// Clean up sandboxes for base-gcs-sample-run
		harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", runName, 30*time.Second)

		// Wait for the concurrent sampling Comparison to complete and verify its CI history table
		comp := harness.WaitForComparisonPhase("eval-runner", samplingCompName, string(v1alpha1.ComparisonPhaseCompleted), 240*time.Second)
		if len(comp.Status.Batches) < 2 {
			t.Fatalf("Expected sampling comparison to run at least 2 batches, got %d", len(comp.Status.Batches))
		}

		// GET the Comparison page for the sampling Comparison and assert history table rows exist
		compURL := fmt.Sprintf("%s/comparisons/eval-runner/%s", baseURL, samplingCompName)
		respComp, err := http.Get(compURL)
		if err != nil {
			t.Fatalf("failed to GET %s: %v", compURL, err)
		}
		compBodyBytes, err := io.ReadAll(respComp.Body)
		respComp.Body.Close()
		if respComp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected status %d for %s: %s", respComp.StatusCode, compURL, string(compBodyBytes))
		}
		compBody := string(compBodyBytes)
		if !strings.Contains(compBody, "Confidence Interval History") {
			t.Errorf("expected Comparison page to contain 'Confidence Interval History', got body:\n%s", compBody)
		}
		tableRegex := regexp.MustCompile(`<table class="ci history-table">[\s\S]*?</table>`)
		historyTable := tableRegex.FindString(compBody)
		if historyTable == "" {
			t.Fatalf("expected Comparison page to contain 'history-table', got body:\n%s", compBody)
		}
		nCellRegex := regexp.MustCompile(`<td class="num">(\d+)</td>`)
		nMatches := nCellRegex.FindAllStringSubmatch(historyTable, -1)
		if len(nMatches) < 2 {
			t.Errorf("expected at least 2 numeric n history table rows, got %d", len(nMatches))
		}

		// Clean up sampling runs
		harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", compCandRun, 30*time.Second)
		harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", compBaseRun, 30*time.Second)
		harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", fmt.Sprintf("%s-b2-cand", samplingCompName), 30*time.Second)
		harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", fmt.Sprintf("%s-b2-base", samplingCompName), 30*time.Second)
	})

	// Consolidated end-to-end controller-restart test covering real Kubernetes pod rolling restart,
	// leader-election lease handoff, and mid-run resumption without duplicate execution.
	// Discovery-phase restart and in-flight fresh-sandbox rescue edge cases are covered by unit tests
	// in pkg/controller/run_controller_test.go and pkg/controller/run_controller_async_test.go.
	t.Run("ControllerRestartMidRunResumesWithoutDuplication", func(t *testing.T) {
		shortSHA := commit[:7]
		baseRunName := fmt.Sprintf("base-%s", shortSHA)

		// 1. Configure repositories in ConfigMap to in-cluster git server
		configPatch := `{"data":{"defaultModel":"mockllm/model","repositories":"- name: http://git-server.eval-runner-sandbox.svc/docs-eval.git\n  branches: [main]\n"}}`
		harness.runCmd("kubectl", "patch", "configmap", "eval-runner-config", "-n", "eval-runner", "--type=merge", "-p", configPatch)

		t.Cleanup(func() {
			// Revert repositories configuration
			cleanPatch := `{"data":{"repositories":"","defaultModel":"mockllm/model"}}`
			harness.runCmd("kubectl", "patch", "configmap", "eval-runner-config", "-n", "eval-runner", "--type=merge", "-p", cleanPatch)
			harness.runCmd("kubectl", "delete", "run", baseRunName, "-n", "eval-runner", "--ignore-not-found")
		})

		// 2. Wait for BranchAdvanced to create the baseline Run
		r := harness.WaitForRunExists("eval-runner", baseRunName, 30*time.Second)
		if r == nil {
			t.Fatalf("expected baseline run %s to be created by BranchAdvanced", baseRunName)
		}

		// 3. Wait until the Run is Running with live progress (task running and samplesDone > 0)
		t.Logf("Waiting for Run %s to enter Running phase with live progress...", baseRunName)
		startWaitProgress := time.Now()
		var observedProgress bool
		for time.Since(startWaitProgress) < 120*time.Second {
			out, err := exec.Command("kubectl", "get", "run", baseRunName, "-n", "eval-runner", "-o", "json").Output()
			if err == nil {
				var runObj v1alpha1.Run
				if json.Unmarshal(out, &runObj) == nil {
					if runObj.Status.Phase == v1alpha1.RunPhaseRunning {
						for _, task := range runObj.Status.Tasks {
							if task.Phase == v1alpha1.TaskPhaseRunning && task.SamplesDone > 0 {
								observedProgress = true
								break
							}
						}
						if observedProgress {
							break
						}
					}
				}
			}
			time.Sleep(1 * time.Second)
		}
		if !observedProgress {
			t.Fatalf("timed out waiting for Run %s to reach Running phase with live progress", baseRunName)
		}

		// Record controller pods before rolling restart so we can verify lease handoff
		previousPods := make(map[string]bool)
		for _, podName := range strings.Fields(harness.runCmd("kubectl", "get", "pods", "-n", "eval-runner", "-l", "app=eval-runner-controller", "--field-selector=status.phase=Running", "-o", "jsonpath={.items[*].metadata.name}")) {
			previousPods[podName] = true
		}

		// 4. Restart the controller deployment mid-run
		t.Logf("Restarting eval-runner-controller deployment mid-run...")
		harness.runCmd("kubectl", "rollout", "restart", "deployment/eval-runner-controller", "-n", "eval-runner")
		harness.runCmd("kubectl", "rollout", "status", "deployment/eval-runner-controller", "-n", "eval-runner", "--timeout=120s")

		// Verify leader-election lease transferred to a new controller pod
		var newControllerPod string
		for _, podName := range strings.Fields(harness.runCmd("kubectl", "get", "pods", "-n", "eval-runner", "-l", "app=eval-runner-controller", "--field-selector=status.phase=Running", "-o", "jsonpath={.items[*].metadata.name}")) {
			if !previousPods[podName] {
				newControllerPod = podName
				break
			}
		}
		if newControllerPod == "" {
			t.Fatalf("expected a new Running eval-runner-controller pod after rollout restart (previous: %v)", previousPods)
		}
		var leaseHolder string
		leaseDeadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(leaseDeadline) {
			leaseHolder = strings.TrimSpace(harness.runCmd("kubectl", "get", "lease", "eval-runner-leader-election", "-n", "eval-runner", "-o", "jsonpath={.spec.holderIdentity}"))
			if strings.HasPrefix(leaseHolder, newControllerPod) {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !strings.HasPrefix(leaseHolder, newControllerPod) {
			t.Fatalf("expected lease eval-runner-leader-election holderIdentity to have prefix %q, got %q", newControllerPod, leaseHolder)
		}

		// 5. Wait for the Run to reach Completed
		harness.WaitForRunPhase("eval-runner", baseRunName, string(v1alpha1.RunPhaseCompleted), 180*time.Second)

		// 6. Assert exactly one Run exists with head-sha label
		out := harness.runCmd("kubectl", "get", "runs", "-n", "eval-runner", "-l", fmt.Sprintf("evalrunner.gke-labs.dev/head-sha=%s", commit), "-o", "jsonpath={.items[*].metadata.name}")
		names := strings.Fields(out)
		if len(names) != 1 {
			t.Fatalf("expected exactly 1 Run with head-sha %s, got %d: %v", commit, len(names), names)
		}
		if names[0] != baseRunName {
			t.Errorf("expected Run name %s, got %s", baseRunName, names[0])
		}

		// 7. Force a real re-sync by removing and re-adding the repository in ConfigMap
		emptyPatch := `{"data":{"repositories":""}}`
		harness.runCmd("kubectl", "patch", "configmap", "eval-runner-config", "-n", "eval-runner", "--type=merge", "-p", emptyPatch)
		time.Sleep(2 * time.Second)

		reSyncStartTime := time.Now().UTC().Format(time.RFC3339)

		// Re-add repository to trigger immediate SyncRepo in reloader
		reAddPatch := `{"data":{"repositories":"- name: http://git-server.eval-runner-sandbox.svc/docs-eval.git\n  branches: [main]\n"}}`
		harness.runCmd("kubectl", "patch", "configmap", "eval-runner-config", "-n", "eval-runner", "--type=merge", "-p", reAddPatch)

		// Wait and verify that the re-sync actually executed
		var syncVerified bool
		waitSyncStart := time.Now()
		for time.Since(waitSyncStart) < 30*time.Second {
			controllerLogs := harness.runCmd("kubectl", "logs", "-n", "eval-runner", "deployment/eval-runner-controller", fmt.Sprintf("--since-time=%s", reSyncStartTime))
			if strings.Contains(controllerLogs, "syncing newly added repository") ||
				strings.Contains(controllerLogs, "docs-eval.git") {
				syncVerified = true
				break
			}
			time.Sleep(1 * time.Second)
		}
		if !syncVerified {
			t.Fatalf("expected controller to execute immediate re-sync of re-added repository, but log confirmation was not found")
		}

		// Assert still exactly one Run and nothing new created after re-sync
		outAfter := harness.runCmd("kubectl", "get", "runs", "-n", "eval-runner", "-l", fmt.Sprintf("evalrunner.gke-labs.dev/head-sha=%s", commit), "-o", "jsonpath={.items[*].metadata.name}")
		namesAfter := strings.Fields(outAfter)
		if len(namesAfter) != 1 {
			t.Errorf("expected still exactly 1 Run after re-sync, got %d: %v", len(namesAfter), namesAfter)
		}

		// Clean up sandboxes
		harness.WaitForAllSandboxesDeletedForRun("eval-runner-sandbox", baseRunName, 30*time.Second)
	})
}
