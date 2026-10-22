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

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/stats"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestServerRepoSuiteDetail_TwoEvalsTwoModels(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now().Truncate(time.Second)

	rate1A1 := 0.80
	rate1B1 := 0.50
	run1 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-1001",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "1111111111111111111111111111111111111111",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "1111111111111111111111111111111111111111",
			},
			Model: "model-a",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "docs/task-1", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 8, SuccessRate: &rate1A1},
				{ID: "docs/task-2", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 5, SuccessRate: &rate1B1},
			},
		},
	}

	rate2A1 := 0.60
	rate2B1 := 0.70
	run2 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-1002",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-90 * time.Minute)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "1111111111111111111111111111111111111111",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "1111111111111111111111111111111111111111",
			},
			Model: "model-b",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "docs/task-1", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 6, SuccessRate: &rate2A1},
				{ID: "docs/task-2", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 7, SuccessRate: &rate2B1},
			},
		},
	}

	rate3A2 := 0.90
	rate3B2 := 0.60
	run3 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-2001",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-30 * time.Minute)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "2222222222222222222222222222222222222222",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "2222222222222222222222222222222222222222",
			},
			Model: "model-a",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "docs/task-1", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 9, SuccessRate: &rate3A2},
				{ID: "docs/task-2", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 6, SuccessRate: &rate3B2},
			},
		},
	}

	rate4A2 := 0.70
	rate4B2 := 0.80
	run4 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-2002",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "2222222222222222222222222222222222222222",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "2222222222222222222222222222222222222222",
			},
			Model: "model-b",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "docs/task-1", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 7, SuccessRate: &rate4A2},
				{ID: "docs/task-2", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 8, SuccessRate: &rate4B2},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run1, run2, run3, run4).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/evals/docs", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()

	// Assert header
	if !strings.Contains(body, `<span class="headline-score">`) {
		t.Errorf("expected headline-score in body")
	}
	if !strings.Contains(body, "2 evals") {
		t.Errorf("expected 2 evals count in header")
	}

	// Assert both eval sections render
	if !strings.Contains(body, "docs/task-1") {
		t.Errorf("expected docs/task-1 in body")
	}
	if !strings.Contains(body, "docs/task-2") {
		t.Errorf("expected docs/task-2 in body")
	}

	// Assert model breakdown and per-model scores in <div class="model-legend">
	if !strings.Contains(body, `<div class="model-legend">`) {
		t.Fatalf("expected model-legend block in body")
	}
	for _, expectedLegendItem := range []string{
		`<span class="mono" style="font-weight:600">model-a:</span>`,
		`<span class="mono" style="font-weight:600">model-b:</span>`,
		`<span class="mono">0.90</span>`,
		`<span class="mono">0.70</span>`,
		`<span class="mono">0.60</span>`,
		`<span class="mono">0.80</span>`,
	} {
		if !strings.Contains(body, expectedLegendItem) {
			t.Errorf("expected legend item %q in body", expectedLegendItem)
		}
	}

	// Assert two lines per chart: polyline stroke with var(--indigo) and var(--teal)
	indigoCount := strings.Count(body, `stroke="var(--indigo)"`)
	tealCount := strings.Count(body, `stroke="var(--teal)"`)
	if indigoCount < 2 {
		t.Errorf("expected at least 2 stroke=var(--indigo), got %d", indigoCount)
	}
	if tealCount < 2 {
		t.Errorf("expected at least 2 stroke=var(--teal), got %d", tealCount)
	}

	// Assert observation ticks: each eval has 4 observations total (2 commits x 2 models)
	// So total obs-ticks across the entire page is 8 (4 per eval)
	tickCount := strings.Count(body, `class="obs-tick"`)
	if tickCount != 8 {
		t.Errorf("expected exactly 8 obs-ticks on page (4 per eval), got %d", tickCount)
	}
}

func TestServerRepoSuiteDetail_SingleObservationNoNaN(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now().Truncate(time.Second)

	rate := 1.0
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-solo",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "abcdef1234567890abcdef1234567890abcdef12",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "abcdef1234567890abcdef1234567890abcdef12",
			},
			Model: "single-model",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "single/task-solo", Phase: v1alpha1.TaskPhaseCompleted, Samples: 5, Successes: 5, SuccessRate: &rate},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/evals/single", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()

	// Assert NO NaN in output
	if strings.Contains(strings.ToLower(body), "nan") {
		t.Errorf("expected no NaN in single-observation page, got body:\n%s", body)
	}

	// Assert exactly 1 tick mark
	tickCount := strings.Count(body, `class="obs-tick"`)
	if tickCount != 1 {
		t.Errorf("expected exactly 1 obs-tick, got %d", tickCount)
	}
}

func TestServerRepoSuiteDetail_UnknownSuite404(t *testing.T) {
	scheme := newTestScheme(t)
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-run",
			Namespace: "eval-runner",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "1111111111111111111111111111111111111111",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL: "https://github.com/gke-labs/eval-runner.git",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "docs/readme", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 8},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/evals/nonexistent", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found, got %d", w.Code)
	}

	if strings.TrimSpace(w.Body.String()) != "suite not found" {
		t.Errorf("expected fixed string 'suite not found', got %q", w.Body.String())
	}
}

func TestServerRepoSuiteDetail_Isolation(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now().Truncate(time.Second)

	// Repo A main run (target)
	runTarget := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-target",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL: "https://github.com/gke-labs/repo-a.git",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "suite-x/target-eval", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 9},
			},
		},
	}

	// Other repo run
	runOtherRepo := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-other-repo",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL: "https://github.com/other-org/repo-b.git",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "suite-x/other-repo-eval", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 5},
			},
		},
	}

	// Other branch run (dev)
	runOtherBranch := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-dev-branch",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "dev",
				"evalrunner.gke-labs.dev/head-sha": "cccc3333cccc3333cccc3333cccc3333cccc3333",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL: "https://github.com/gke-labs/repo-a.git",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "suite-x/dev-branch-eval", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 5},
			},
		},
	}

	// Candidate run (not base-*)
	runCandidate := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cand-run",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "dddd4444dddd4444dddd4444dddd4444dddd4444",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL: "https://github.com/gke-labs/repo-a.git",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "suite-x/candidate-eval", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 5},
			},
		},
	}

	// Running-phase run (not completed)
	runRunning := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-running",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "eeee5555eeee5555eeee5555eeee5555eeee5555",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL: "https://github.com/gke-labs/repo-a.git",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseRunning,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "suite-x/running-eval", Phase: v1alpha1.TaskPhaseRunning, Samples: 10, Successes: 5},
			},
		},
	}

	// PR run (pr-*)
	runPR := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pr-101",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "ffff6666ffff6666ffff6666ffff6666ffff6666",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL: "https://github.com/gke-labs/repo-a.git",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "suite-x/pr-eval", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 5},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runTarget, runOtherRepo, runOtherBranch, runCandidate, runRunning, runPR).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/repo-a/evals/suite-x", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()

	if !strings.Contains(body, "suite-x/target-eval") {
		t.Errorf("expected suite-x/target-eval in body")
	}
	if strings.Contains(body, "other-repo-eval") {
		t.Errorf("cross-repo leak: other-repo-eval found in body")
	}
	if strings.Contains(body, "dev-branch-eval") {
		t.Errorf("cross-branch leak: dev-branch-eval found in body")
	}
	if strings.Contains(body, "candidate-eval") {
		t.Errorf("candidate run leak: candidate-eval found in body")
	}
	if strings.Contains(body, "running-eval") {
		t.Errorf("running run leak: running-eval found in body")
	}
	if strings.Contains(body, "pr-eval") {
		t.Errorf("pr run leak: pr-eval found in body")
	}
}

func TestServerRepoSuiteDetail_NamespaceAndNoLegacyHrefs(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now().Truncate(time.Second)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-staging",
			Namespace:         "custom-ns",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "1234567890abcdef1234567890abcdef12345678",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL: "https://github.com/gke-labs/eval-runner.git",
			},
			Model: "gemini-2.5-pro",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:        "docs/quickstart",
					Phase:     v1alpha1.TaskPhaseCompleted,
					Samples:   10,
					Successes: 8,
					LogPath:   "gs://bucket/runs/1234567/docs%2Fquickstart/inspect.json",
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	sink := artifacts.NewInMemorySink()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner", ArtifactSink: sink})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/evals/docs?namespace=custom-ns", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()

	// 1. ?namespace= propagation: links must preserve namespace=custom-ns
	if !strings.Contains(body, "namespace=custom-ns") {
		t.Errorf("expected namespace=custom-ns propagation in links")
	}
	expectedSamplesLink := `/github.com/gke-labs/eval-runner/runs/base-staging/tasks/docs%2Fquickstart/samples?namespace=custom-ns`
	if !strings.Contains(body, expectedSamplesLink) {
		t.Errorf("expected samples link %q in body:\n%s", expectedSamplesLink, body)
	}

	// 2. No legacy hrefs: must not contain href="/runs/
	if strings.Contains(body, `href="/runs/`) {
		t.Errorf("body contains legacy href=\"/runs/: \n%s", body)
	}
	if strings.Contains(body, `href="/comparisons/`) {
		t.Errorf("body contains legacy href=\"/comparisons/: \n%s", body)
	}

	// Must contain repository-scoped href
	if !strings.Contains(body, `href="/github.com/gke-labs/eval-runner/runs`) {
		t.Errorf("expected repository-scoped runs link")
	}
}

func TestServerRepoSuiteDetail_NoSamplesLinkWithoutLogPathOrSink(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now().Truncate(time.Second)

	runWithoutLogPath := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-no-log",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "1234567890abcdef1234567890abcdef12345678",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL: "https://github.com/gke-labs/eval-runner.git",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "docs/quickstart", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 8, LogPath: ""},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runWithoutLogPath).Build()

	// Case 1: with sink but task LogPath is empty
	sink := artifacts.NewInMemorySink()
	serverWithSink := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner", ArtifactSink: sink})
	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/evals/docs", nil)
	w := httptest.NewRecorder()
	serverWithSink.ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), "view samples →") {
		t.Errorf("expected no samples link when LogPath is empty")
	}

	// Case 2: task has LogPath but server has no artifact storage configured
	runWithLogPath := runWithoutLogPath.DeepCopy()
	runWithLogPath.Status.Tasks[0].LogPath = "gs://bucket/runs/123/task/inspect.json"
	cl2 := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runWithLogPath).Build()
	serverNoSink := NewServer(Config{Client: cl2, DefaultNamespace: "eval-runner"})
	w2 := httptest.NewRecorder()
	serverNoSink.ServeHTTP(w2, req)
	if strings.Contains(w2.Body.String(), "view samples →") {
		t.Errorf("expected no samples link when artifact sink is not configured")
	}
}

func TestBuildRepoSuiteChartSVG_DimensionHarness(t *testing.T) {
	t1 := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	commits := []CommitEntry{
		{SHA: "1111111111111111111111111111111111111111", CommitTime: t1},
	}
	obs := []EvalObservation{
		{
			CommitSHA: commits[0].SHA,
			Model:     "gemini",
			Harness:   "inspect:0.3.280",
			Samples:   10,
			Successes: 8,
			Est:       stats.PooledWilsonFromTotals(8, 10, 0.95),
		},
		{
			CommitSHA: commits[0].SHA,
			Model:     "gemini",
			Harness:   "inspect:0.3.281",
			Samples:   10,
			Successes: 5,
			Est:       stats.PooledWilsonFromTotals(5, 10, 0.95),
		},
	}
	lines := LinesPerDimension(obs, DimensionHarness, commits)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines under DimensionHarness, got %d", len(lines))
	}
	svg := BuildRepoSuiteChartSVG(lines, obs, commits, DimensionHarness)
	if len(svg.Ticks) != 2 {
		t.Fatalf("expected 2 ticks, got %d", len(svg.Ticks))
	}
	// Verify ticks carry the respective line colors rather than default indigo fallback
	if svg.Ticks[0].Stroke == "var(--indigo)" && svg.Ticks[1].Stroke == "var(--indigo)" {
		t.Errorf("expected ticks to carry distinct harness line colors, got both indigo: %v", svg.Ticks)
	}
	if svg.Ticks[0].Stroke != lines[0].Color || svg.Ticks[1].Stroke != lines[1].Color {
		t.Errorf("expected ticks to match line colors %q and %q, got %q and %q",
			lines[0].Color, lines[1].Color, svg.Ticks[0].Stroke, svg.Ticks[1].Stroke)
	}
}

func TestOverviewAndSuitePageAgreeOnSuiteScore(t *testing.T) {
	scheme := newTestScheme(t)
	t1 := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)

	rate1A := 0.70
	rate1B := 0.80
	rate2A := 0.90
	rate2B := 0.70
	rateOther := 0.50

	run1 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-1111111",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(t1),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": t1.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "1111111111111111111111111111111111111111",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "docs/eval-a", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 7, SuccessRate: &rate1A},
				{ID: "docs/eval-b", Phase: v1alpha1.TaskPhaseCompleted, Samples: 20, Successes: 16, SuccessRate: &rate1B},
			},
		},
	}

	run2 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-2222222",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(t2),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": t2.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "2222222222222222222222222222222222222222",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "docs/eval-a", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 9, SuccessRate: &rate2A},
				{ID: "docs/eval-b", Phase: v1alpha1.TaskPhaseCompleted, Samples: 20, Successes: 14, SuccessRate: &rate2B},
				{ID: "triage/issue-eval", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 5, SuccessRate: &rateOther},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run1, run2).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	// Query Overview page
	reqOverview := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner", nil)
	wOverview := httptest.NewRecorder()
	server.ServeHTTP(wOverview, reqOverview)
	if wOverview.Code != http.StatusOK {
		t.Fatalf("overview expected 200 OK, got %d: %s", wOverview.Code, wOverview.Body.String())
	}
	bodyOverview := wOverview.Body.String()

	// Query Suite Detail page
	reqSuite := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/evals/docs", nil)
	wSuite := httptest.NewRecorder()
	server.ServeHTTP(wSuite, reqSuite)
	if wSuite.Code != http.StatusOK {
		t.Fatalf("suite detail expected 200 OK, got %d: %s", wSuite.Code, wSuite.Body.String())
	}
	bodySuite := wSuite.Body.String()

	// Expected values for docs suite:
	// - eval-a = 9/10 = 0.90
	// - eval-b = 14/20 = 0.70
	// - Suite score mean = (0.90 + 0.70) / 2 = 0.80 (distinct from both evals)
	// - Pooled Wilson CI from (9+14)/(10+20) = 23/30 at 95% = [0.59, 0.88]
	// - Delta vs commit 1 (where eval-a=7/10, eval-b=16/20 -> mean=0.75, pooled=23/30):
	//   Point delta = 0.80 - 0.75 = +0.05, 95% CI = [−0.17, +0.24]
	const (
		expectedScore     = "0.80"
		expectedCI        = "[0.59, 0.88]"
		expectedDelta     = "Δ +0.05 [−0.17, +0.24]"
		expectedHTMLDelta = "Δ &#43;0.05 [−0.17, &#43;0.24]"
	)

	// 1. Assert Overview suite-row cells (not whole body) equal the expected literals
	idx := strings.Index(bodyOverview, `/evals/docs`)
	if idx == -1 {
		t.Fatalf("expected overview to contain suite row for docs:\n%s", bodyOverview)
	}
	rowStart := strings.LastIndex(bodyOverview[:idx], `<tr class="suite-row">`)
	if rowStart == -1 {
		t.Fatalf("could not find suite row start before docs link")
	}
	rowEnd := strings.Index(bodyOverview[idx:], "</tr>")
	if rowEnd == -1 {
		t.Fatalf("could not find suite row end after docs link")
	}
	docsRow := bodyOverview[rowStart : idx+rowEnd]

	expectedRowScoreSpan := `<span class="mono" style="font-weight:600">` + expectedScore + `</span>`
	if !strings.Contains(docsRow, expectedRowScoreSpan) {
		t.Errorf("expected overview suite row to contain score %s, got row:\n%s", expectedRowScoreSpan, docsRow)
	}
	if !strings.Contains(docsRow, expectedCI) {
		t.Errorf("expected overview suite row to contain CI %s, got row:\n%s", expectedCI, docsRow)
	}
	if !strings.Contains(docsRow, expectedDelta) && !strings.Contains(docsRow, expectedHTMLDelta) {
		t.Errorf("expected overview suite row to contain delta %s, got row:\n%s", expectedHTMLDelta, docsRow)
	}

	// 2. Assert Suite detail headline contains the EXACT SAME literals
	expectedHeadlineScore := `<span class="headline-score">` + expectedScore + `</span>`
	if !strings.Contains(bodySuite, expectedHeadlineScore) {
		t.Errorf("expected suite detail to contain headline score %s, got body:\n%s", expectedHeadlineScore, bodySuite)
	}
	expectedHeadlineCI := `<span class="headline-ci">` + expectedCI + `</span>`
	if !strings.Contains(bodySuite, expectedHeadlineCI) {
		t.Errorf("expected suite detail to contain headline CI %s, got body:\n%s", expectedHeadlineCI, bodySuite)
	}
	if !strings.Contains(bodySuite, expectedDelta) && !strings.Contains(bodySuite, expectedHTMLDelta) {
		t.Errorf("expected suite detail to contain delta %s, got body:\n%s", expectedHTMLDelta, bodySuite)
	}
}

func TestRepoSuiteDetail_AtCommitHeader_PicksScoreCommit(t *testing.T) {
	scheme := newTestScheme(t)
	t1 := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)

	rate1 := 0.80
	rate2 := 0.60

	// Commit 1 has completed observations for suite "docs"
	run1 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-1111111",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(t1),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": t1.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "1111111111111111111111111111111111111111",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "docs/readme", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 8, SuccessRate: &rate1},
			},
		},
	}

	// Commit 2 is a newer run on main for a DIFFERENT suite ("triage")
	run2 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-2222222",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(t2),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": t2.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "2222222222222222222222222222222222222222",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "triage/lookup", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, Successes: 6, SuccessRate: &rate2},
			},
		},
	}

	// Commit 3 is an even newer run on main where tasks for "docs" are not completed (samples = 0)
	run3 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-3333333",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(t3),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": t3.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "3333333333333333333333333333333333333333",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "docs/readme", Phase: v1alpha1.TaskPhasePending, Samples: 0},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run1, run2, run3).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/evals/docs", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()

	// Assert that "at commit" in the suite header shows the commit the score came from (1111111),
	// NOT the newer commit with another suite (2222222) or the newer run without completed samples (3333333).
	expectedHeader := `<span>at commit <span class="mono">1111111</span></span>`
	if !strings.Contains(body, expectedHeader) {
		t.Errorf("expected suite header to contain %q, but body was:\n%s", expectedHeader, body)
	}

	if strings.Contains(body, `at commit <span class="mono">2222222</span>`) {
		t.Errorf("suite header incorrectly picked newer commit 2222222 from a different suite")
	}
	if strings.Contains(body, `at commit <span class="mono">3333333</span>`) {
		t.Errorf("suite header incorrectly picked newest run commit 3333333 with no completed samples")
	}
}
