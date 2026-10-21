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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRepoOverview_ThreeRunsMainWithDelta(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now()

	rate1 := 0.50
	rate2 := 0.70
	rate3 := 0.85
	samples := 20
	mean3 := 0.85

	run1 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-commit-1",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-3 * time.Hour)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": now.Add(-3 * time.Hour).UTC().Format(time.RFC3339),
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
				{
					ID:          "task-eval",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     samples,
					SuccessRate: &rate1,
				},
			},
		},
	}

	run2 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-commit-2",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": now.Add(-2 * time.Hour).UTC().Format(time.RFC3339),
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
				{
					ID:          "task-eval",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     samples,
					SuccessRate: &rate2,
				},
			},
		},
	}

	run3 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-commit-3",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": now.Add(-1 * time.Hour).UTC().Format(time.RFC3339),
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
			Summary: &v1alpha1.RunSummary{
				MeanSuccessRate: &mean3,
			},
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-eval",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     samples,
					SuccessRate: &rate3,
					SuccessRateCI: &v1alpha1.ConfidenceInterval{
						Lower:      0.70,
						Upper:      0.95,
						Confidence: 0.95,
						Method:     "wilson",
					},
					Tokens: &v1alpha1.TaskTokens{
						Total: 5400,
					},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run1, run2, run3).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()

	// Verify Header Tiles
	for _, expected := range []string{
		"Tasks Tracked",
		"Mean Success Rate",
		"0.85",
		"Last Commit Evaluated",
		"3333333",
		"Runs on Branch",
		">3<",
		"Pull Requests",
		"/github.com/gke-labs/eval-runner/pulls",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("expected body to contain %q, body:\n%s", expected, body)
		}
	}

	// Verify task row: rate 0.85, CI [0.70, 0.95], delta +0.15 with good class
	for _, expected := range []string{
		"task-eval",
		">0.85<",
		"[0.70, 0.95]",
		"cibar-01",
		"chip good",
		"&#43;0.15",
		"5,400",
		"/github.com/gke-labs/eval-runner/runs/base-commit-3/tasks/task-eval/samples",
		"/github.com/gke-labs/eval-runner/series/main?task=task-eval",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("expected body to contain %q, body:\n%s", expected, body)
		}
	}
}

func TestRepoOverview_TaskMissingFromLatestRun(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now()

	rateA1 := 0.60
	rateB1 := 0.75
	rateA2 := 0.80

	run1 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-commit-1",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": now.Add(-2 * time.Hour).UTC().Format(time.RFC3339),
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
				{
					ID:          "task-a",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     10,
					SuccessRate: &rateA1,
				},
				{
					ID:          "task-b",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     15,
					SuccessRate: &rateB1,
				},
			},
		},
	}

	run2 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-commit-2",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": now.Add(-1 * time.Hour).UTC().Format(time.RFC3339),
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
				{
					ID:          "task-a",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     12,
					SuccessRate: &rateA2,
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run1, run2).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()

	// Task A has 2 points: latest 0.80, delta +0.20
	if !strings.Contains(body, "task-a") {
		t.Errorf("expected task-a in table")
	}
	if !strings.Contains(body, ">0.80<") {
		t.Errorf("expected task-a latest rate 0.80")
	}
	if !strings.Contains(body, "&#43;0.20") {
		t.Errorf("expected task-a delta +0.20")
	}

	// Task B is missing from run2, so it falls back to run1: rate 0.75, no delta ("—")
	if !strings.Contains(body, "task-b") {
		t.Errorf("expected task-b in table")
	}
	if !strings.Contains(body, ">0.75<") {
		t.Errorf("expected task-b latest rate 0.75 from run1")
	}
	if !strings.Contains(body, "/github.com/gke-labs/eval-runner/runs/base-commit-1/tasks/task-b/samples") {
		t.Errorf("expected task-b link to base-commit-1")
	}
}

func TestRepoOverview_NoBranchFallback(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now()
	rate := 0.77

	// Ad-hoc completed run without branch label or base- prefix
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "smoke-run-fallback",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)),
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "abcdef1234567890",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "smoke/test_task",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     8,
					SuccessRate: &rate,
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	note := "no main-branch baselines yet; showing latest Runs"
	if !strings.Contains(body, note) {
		t.Errorf("expected note %q in body, got:\n%s", note, body)
	}
	if !strings.Contains(body, "smoke/test_task") {
		t.Errorf("expected task smoke/test_task in body")
	}
	if !strings.Contains(body, ">0.77<") {
		t.Errorf("expected rate 0.77 in body")
	}
}

func TestRepoOverview_EmptyState(t *testing.T) {
	scheme := newTestScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	t.Run("DefaultBranchEmpty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}

		body := w.Body.String()
		for _, expected := range []string{
			"empty-state",
			"kubectl apply",
			"No evaluations recorded for this repository",
		} {
			if !strings.Contains(body, expected) {
				t.Errorf("expected empty state to contain %q, got body:\n%s", expected, body)
			}
		}
	})

	t.Run("NonMainBranchEmpty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner?branch=feature-x", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}

		body := w.Body.String()
		expected := "No Completed Runs on branch <code class=\"mono\">feature-x</code>."
		if !strings.Contains(body, expected) {
			t.Errorf("expected %q in body, got:\n%s", expected, body)
		}
	})
}

func TestRepoOverview_CohortFilter(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now()

	rateNightly := 0.90
	ratePR := 0.40

	runNightly := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-nightly-run",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "1111111",
			},
			Cohort: "nightly",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-nightly",
					Phase:       v1alpha1.TaskPhaseCompleted,
					SuccessRate: &rateNightly,
				},
			},
		},
	}

	runPR := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-pr-run",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-30 * time.Minute)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "2222222",
			},
			Cohort: "pr",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-pr",
					Phase:       v1alpha1.TaskPhaseCompleted,
					SuccessRate: &ratePR,
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runNightly, runPR).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner?cohort=nightly", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "task-nightly") {
		t.Errorf("expected task-nightly in body")
	}
	if strings.Contains(body, "task-pr") {
		t.Errorf("did not expect task-pr in body for cohort=nightly")
	}
}

func TestRepoOverview_NamespaceLinks(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now()
	rate := 0.88

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-custom-ns",
			Namespace:         "custom-ns",
			CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "aaaa111",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-custom",
					Phase:       v1alpha1.TaskPhaseCompleted,
					SuccessRate: &rate,
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner?namespace=custom-ns", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	for _, expected := range []string{
		"namespace: <span class=\"mono\">custom-ns</span>",
		"/github.com/gke-labs/eval-runner/runs/base-custom-ns?namespace=custom-ns",
		"/github.com/gke-labs/eval-runner/runs/base-custom-ns/tasks/task-custom/samples?namespace=custom-ns",
		"/github.com/gke-labs/eval-runner/pulls?namespace=custom-ns",
		"/github.com/gke-labs/eval-runner/series/main?task=task-custom&amp;namespace=custom-ns",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("expected body to contain %q, body:\n%s", expected, body)
		}
	}
}

func TestBuildSparklineSVG(t *testing.T) {
	rate1 := 0.40
	rate2 := 0.80

	items := make([]SeriesItem, 25)
	for i := 0; i < 25; i++ {
		r := rate1
		if i%2 == 1 {
			r = rate2
		}
		items[i] = SeriesItem{
			CommitSHAShort: fmt.Sprintf("%07d", i),
			HasPoint:       true,
			SuccessRate:    &r,
		}
	}

	spark := BuildSparklineSVG(items)
	if !spark.HasPoints {
		t.Fatalf("expected sparkline to have points")
	}
	if len(spark.Circles) != 20 {
		t.Errorf("expected last 20 circles, got %d", len(spark.Circles))
	}
	if len(spark.Polylines) == 0 {
		t.Errorf("expected at least one polyline segment")
	}
}
