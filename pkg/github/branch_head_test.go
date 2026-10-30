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

package github

import (
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/gke-labs/eval-runner/pkg/pooling"
	"github.com/gke-labs/eval-runner/pkg/stats"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBranchHeadDetailsURL(t *testing.T) {
	ref := forge.RepoRef{
		Host:  "github.com",
		Owner: "gke-labs",
		Name:  "eval-runner",
	}

	url := BranchHeadDetailsURL("https://dotevals.com", ref, "main")
	want := "https://dotevals.com/github.com/gke-labs/eval-runner/series/main"
	if url != want {
		t.Errorf("BranchHeadDetailsURL = %q, want %q", url, want)
	}

	// Empty UI base URL
	urlEmpty := BranchHeadDetailsURL("", ref, "main")
	wantEmpty := "/github.com/gke-labs/eval-runner/series/main"
	if urlEmpty != wantEmpty {
		t.Errorf("BranchHeadDetailsURL empty UI = %q, want %q", urlEmpty, wantEmpty)
	}
}

func TestCheckRunStateForBranchHead_FullCycle(t *testing.T) {
	rate := 10.0 / 16.0
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name: "base-abc1234",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/head-sha": "abc1234",
				"evalrunner.gke-labs.dev/branch":   "main",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-1",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     16,
					Successes:   10,
					SuccessRate: &rate,
				},
			},
		},
	}

	pool := pooling.PoolRunsForCommit([]*v1alpha1.Run{run})
	ref := forge.RepoRef{
		Host:  "github.com",
		Owner: "gke-labs",
		Name:  "eval-runner",
	}
	lastMove := &stats.DeltaEstimate{
		Delta:      0.02,
		CI:         stats.Interval{Lower: -0.09, Upper: 0.13},
		Confidence: 0.95,
	}

	// 1. in_progress (queued vs running)
	queuedState := CheckRunStateForBranchHead(pool, lastMove, "https://dotevals.com", ref, "main", v1alpha1.RunPhasePending, false, 0)
	if queuedState.Status != "in_progress" || queuedState.Conclusion != "" || queuedState.Title != "queued" {
		t.Errorf("unexpected queued state: %+v", queuedState)
	}

	runningState := CheckRunStateForBranchHead(pool, lastMove, "https://dotevals.com", ref, "main", v1alpha1.RunPhaseRunning, false, 0)
	if runningState.Status != "in_progress" || runningState.Conclusion != "" || runningState.Title != "running" {
		t.Errorf("unexpected running state: %+v", runningState)
	}

	// 2. completed with parent delta
	compState := CheckRunStateForBranchHead(pool, lastMove, "https://dotevals.com", ref, "main", v1alpha1.RunPhaseCompleted, true, 45*time.Minute)
	if compState.Status != "completed" || compState.Conclusion != "success" {
		t.Errorf("unexpected completed state: %+v", compState)
	}
	wantTitle := "63% ± 21 · n=16 · Δ vs parent +2 (95% CI \u22129…+13)"
	if compState.Title != wantTitle {
		t.Errorf("Title = %q, want %q", compState.Title, wantTitle)
	}
	if !strings.Contains(compState.Summary, "next top-up in ~45m") {
		t.Errorf("Summary missing top-up notice: %s", compState.Summary)
	}
	if !strings.Contains(compState.Summary, "task-1") || !strings.Contains(compState.Summary, "16") {
		t.Errorf("Summary missing task row: %s", compState.Summary)
	}

	// 3. failed seed
	failRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name: "base-abc1234",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseFailed,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:      "task-1",
					Phase:   v1alpha1.TaskPhaseFailed,
					Message: "syntax error",
				},
			},
		},
	}
	failPool := pooling.PoolRunsForCommit([]*v1alpha1.Run{failRun})
	failState := CheckRunStateForBranchHead(failPool, nil, "https://dotevals.com", ref, "main", v1alpha1.RunPhaseFailed, false, 0)
	if failState.Status != "completed" || failState.Conclusion != "failure" {
		t.Errorf("unexpected failure state: %+v", failState)
	}
	if failState.Title != "evals failed: 1 task failed" {
		t.Errorf("failure Title = %q, want 'evals failed: 1 task failed'", failState.Title)
	}
}

func TestHashCheckRunState_NoCountdownChurn(t *testing.T) {
	ref := forge.RepoRef{
		Host:  "github.com",
		Owner: "gke-labs",
		Name:  "eval-runner",
	}
	rate := 1.0
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name: "base-abc",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/head-sha": "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-1",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     10,
					Successes:   10,
					SuccessRate: &rate,
				},
			},
		},
	}
	pool := pooling.PoolRunsForCommit([]*v1alpha1.Run{run})

	// Two states at different countdown durations: 45m vs 38m
	state1 := CheckRunStateForBranchHead(pool, nil, "https://dotevals.com", ref, "main", v1alpha1.RunPhaseCompleted, true, 45*time.Minute)
	state2 := CheckRunStateForBranchHead(pool, nil, "https://dotevals.com", ref, "main", v1alpha1.RunPhaseCompleted, true, 38*time.Minute)

	hash1 := HashCheckRunState(state1)
	hash2 := HashCheckRunState(state2)

	if hash1 != hash2 {
		t.Errorf("HashCheckRunState churned on countdown: hash1=%s hash2=%s", hash1, hash2)
	}
}

func TestReportBranchHead_RecreateOn404(t *testing.T) {
	server := NewFakeGitHubServer()
	defer server.Close()

	client := server.Client("test-token")
	rep := NewReporter(client, ReporterOptions{UIBaseURL: "https://dotevals.com"})

	ref := forge.RepoRef{
		Host:  "github.com",
		Owner: "gke-labs",
		Name:  "eval-runner",
	}

	state := CheckRunState{
		Name:       CheckRunName,
		Status:     "completed",
		Conclusion: "success",
		Title:      "100% ± 0 · n=10",
		Summary:    "all passed",
	}

	// First create check run
	createdID, _, err := rep.ReportBranchHead(t.Context(), ref, "abc1234", state, 0)
	if err != nil {
		t.Fatalf("ReportBranchHead create error: %v", err)
	}

	// Inject 404 on the next update call (simulating deleted check run)
	server.InjectPathError("PATCH", "/repos/gke-labs/eval-runner/check-runs/", 404, `{"message":"Not Found"}`)

	// Calling with old createdID should handle 404 and create a new check run
	newID, _, err := rep.ReportBranchHead(t.Context(), ref, "abc1234", state, createdID)
	if err != nil {
		t.Fatalf("ReportBranchHead update with 404 error: %v", err)
	}
	if newID == createdID {
		t.Errorf("expected new check run ID after 404 recreate, got same ID %d", newID)
	}
}
