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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/stats"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("failed to add v1alpha1 to scheme: %v", err)
	}
	return s
}

func sampleFixtures() (*v1alpha1.Run, *v1alpha1.Run, *v1alpha1.Comparison) {
	rate084 := 0.84
	rate061 := 0.61
	delta23 := 0.23
	pImp := 0.98

	baseRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "docs-eval-base-05ce4d8",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-15 * time.Minute)),
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "a81d0c4111111111111111111111111111111111",
			},
			Model: "google/gemini-3.5-flash-lite",
		},
		Status: v1alpha1.RunStatus{
			Phase:     v1alpha1.RunPhaseCompleted,
			Artifacts: "gs://eval-bucket/runs/a81d0c4/",
			Summary: &v1alpha1.RunSummary{
				Tasks:          "1/1",
				TasksCompleted: 1,
				TasksTotal:     1,
				TotalSamples:   48,
				Rates:          "triage/runbook_lookup=0.61[0.46,0.74]",
			},
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "triage/runbook_lookup",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     48,
					Successes:   29,
					SuccessRate: &rate061,
					SuccessRateCI: &v1alpha1.ConfidenceInterval{
						Lower:      0.46,
						Upper:      0.74,
						Confidence: 0.95,
						Method:     "wilson",
					},
					LogPath: "gs://eval-bucket/runs/a81d0c4/triage/runbook_lookup/inspect.json",
				},
			},
		},
	}

	candRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "docs-eval-cand-05ce4d8",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Minute)),
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "05ce4d8222222222222222222222222222222222",
			},
			Model: "google/gemini-3.5-flash-lite",
		},
		Status: v1alpha1.RunStatus{
			Phase:     v1alpha1.RunPhaseCompleted,
			Artifacts: "gs://eval-bucket/runs/05ce4d8/",
			Summary: &v1alpha1.RunSummary{
				Tasks:          "1/1",
				TasksCompleted: 1,
				TasksTotal:     1,
				TotalSamples:   48,
				Rates:          "triage/runbook_lookup=0.84[0.71,0.92]",
			},
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "triage/runbook_lookup",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     48,
					Successes:   40,
					SuccessRate: &rate084,
					SuccessRateCI: &v1alpha1.ConfidenceInterval{
						Lower:      0.71,
						Upper:      0.92,
						Confidence: 0.95,
						Method:     "wilson",
					},
					LogPath: "gs://eval-bucket/runs/05ce4d8/triage/runbook_lookup/inspect.json",
				},
			},
		},
	}

	comp := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "docs-eval-same-commit",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
		},
		Spec: v1alpha1.ComparisonSpec{
			Baseline: v1alpha1.ComparisonRunRef{
				Run: "docs-eval-base-05ce4d8",
			},
			Candidate: v1alpha1.ComparisonRunRef{
				Run: "docs-eval-cand-05ce4d8",
			},
		},
		Status: v1alpha1.ComparisonStatus{
			Phase:        v1alpha1.ComparisonPhaseCompleted,
			Verdict:      v1alpha1.ComparisonVerdictImproved,
			PImprovement: &pImp,
			SummaryPath:  "gs://eval-bucket/comparisons/docs-eval-same-commit.json",
			Summary: &v1alpha1.ComparisonSummary{
				Tasks: "1/1",
			},
			Tasks: []v1alpha1.ComparisonTaskStatus{
				{
					ID:            "triage/runbook_lookup",
					BaseRate:      &rate061,
					CandidateRate: &rate084,
					Delta:         &delta23,
					DeltaCI: &v1alpha1.DeltaConfidenceInterval{
						Lower: 0.11,
						Upper: 0.35,
					},
					Pairs: 48,
					Dropped: v1alpha1.ComparisonDropped{
						Candidate: 0,
						Baseline:  0,
					},
					Label: v1alpha1.ComparisonTaskLabelImproved,
				},
			},
		},
	}

	return baseRun, candRun, comp
}

func TestServerHealthz(t *testing.T) {
	scheme := newTestScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ok") {
		t.Errorf("expected body to contain 'ok', got %q", w.Body.String())
	}
}

func TestServerRootRedirect(t *testing.T) {
	scheme := newTestScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 Found, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "/runs" {
		t.Errorf("expected redirect to /runs, got %q", loc)
	}
}

func TestServerRunsList(t *testing.T) {
	scheme := newTestScheme(t)
	baseRun, candRun, comp := sampleFixtures()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseRun, candRun, comp).Build()
	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/runs", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "docs-eval-base-05ce4d8") {
		t.Errorf("expected body to contain base run name, got: %s", body)
	}
	if !strings.Contains(body, "docs-eval-cand-05ce4d8") {
		t.Errorf("expected body to contain candidate run name, got: %s", body)
	}
	if !strings.Contains(body, "chip good") {
		t.Errorf("expected Completed phase chip to have 'chip good'")
	}
	if !strings.Contains(body, "/runs/eval-runner/docs-eval-base-05ce4d8") {
		t.Errorf("expected link to run detail")
	}
}

func TestServerRunDetail(t *testing.T) {
	scheme := newTestScheme(t)
	baseRun, candRun, comp := sampleFixtures()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseRun, candRun, comp).Build()
	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/docs-eval-cand-05ce4d8", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// Check header
	if !strings.Contains(body, "docs-eval-cand-05ce4d8") {
		t.Errorf("expected run name in header")
	}
	if !strings.Contains(body, "google/gemini-3.5-flash-lite") {
		t.Errorf("expected model name in header")
	}
	if !strings.Contains(body, "05ce4d8") {
		t.Errorf("expected commit in header")
	}
	if !strings.Contains(body, "https://github.com/gke-labs/eval-runner.git") {
		t.Errorf("expected repo URL in header")
	}

	// Check task row
	if !strings.Contains(body, "triage/runbook_lookup") {
		t.Errorf("expected task id in table")
	}
	if !strings.Contains(body, "0.84") {
		t.Errorf("expected rate 0.84 in table")
	}
	if !strings.Contains(body, "wilson") {
		t.Errorf("expected method 'wilson' in table")
	}
	if !strings.Contains(body, "cibar-01") {
		t.Errorf("expected 0-1 CI bar element")
	}
	expectedLink := "/runs/eval-runner/docs-eval-cand-05ce4d8/tasks/triage%2Frunbook_lookup/samples"
	if !strings.Contains(body, expectedLink) {
		t.Errorf("expected link to task samples list %q in table", expectedLink)
	}
	if !strings.Contains(body, "gs://eval-bucket/runs/05ce4d8/triage/runbook_lookup/inspect.json") {
		t.Errorf("expected log path in table")
	}

	// Test 404 for missing run
	reqNotFound := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/non-existent-run", nil)
	wNotFound := httptest.NewRecorder()
	server.ServeHTTP(wNotFound, reqNotFound)
	if wNotFound.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing run, got %d", wNotFound.Code)
	}
	if strings.TrimSpace(wNotFound.Body.String()) != "run not found" {
		t.Errorf("expected 'run not found', got %q", wNotFound.Body.String())
	}
	if strings.Contains(wNotFound.Body.String(), "non-existent-run") {
		t.Errorf("expected no reflected input in body, got %q", wNotFound.Body.String())
	}
}

func TestServerRunsList_RunningRunTasksRunningString(t *testing.T) {
	scheme := newTestScheme(t)
	runningRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-running-run",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Model: "mockllm/model",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseRunning,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:    "task-1",
					Phase: v1alpha1.TaskPhaseRunning,
				},
				{
					ID:    "task-2",
					Phase: v1alpha1.TaskPhasePending,
				},
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runningRun).Build()
	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/runs", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "1/2 tasks running") {
		t.Errorf("expected '1/2 tasks running' in Runs list, got:\n%s", body)
	}
}

func TestServerRunDetail_LiveProgressAndMetaRefresh(t *testing.T) {
	scheme := newTestScheme(t)
	runningRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-running-run",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Model: "mockllm/model",
			Limit: func() *int { i := 50; return &i }(),
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseRunning,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:             "docs/readme_comprehension",
					Phase:          v1alpha1.TaskPhaseRunning,
					SamplesDone:    12,
					SamplesPlanned: 50,
				},
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runningRun).Build()
	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/test-running-run", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `<meta http-equiv="refresh" content="15">`) {
		t.Errorf("expected meta refresh tag in running run detail page, got:\n%s", body)
	}
	if !strings.Contains(body, "12 of 50 samples") {
		t.Errorf("expected '12 of 50 samples' progress bar in run detail page, got:\n%s", body)
	}
	if !strings.Contains(body, "progress-bar") {
		t.Errorf("expected progress-bar in run detail page, got:\n%s", body)
	}
}

func TestServerRunDetail_LiveProgressUnknownPlanned(t *testing.T) {
	scheme := newTestScheme(t)
	runningRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-running-unknown-planned",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Model: "mockllm/model",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseRunning,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-1",
					Phase:       v1alpha1.TaskPhaseRunning,
					SamplesDone: 3,
				},
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runningRun).Build()
	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/test-running-unknown-planned", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "3 samples") {
		t.Errorf("expected '3 samples' progress text, got:\n%s", body)
	}
	if strings.Contains(body, "3 of 0 samples") {
		t.Errorf("expected NOT to contain '3 of 0 samples', got:\n%s", body)
	}
}

func TestServerRunDetail_LiveProgressClampsToPossibleSamples(t *testing.T) {
	scheme := newTestScheme(t)
	limit := 50
	runningRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-running-clamp",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Model: "mockllm/model",
			Limit: &limit,
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseRunning,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:              "task-1",
					Phase:           v1alpha1.TaskPhaseRunning,
					SamplesDone:     10,
					PossibleSamples: 21,
				},
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runningRun).Build()
	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/test-running-clamp", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "10 of 21 samples") {
		t.Errorf("expected '10 of 21 samples' progress text clamped to dataset size, got:\n%s", body)
	}
}

func TestServerComparisonsList(t *testing.T) {
	scheme := newTestScheme(t)
	baseRun, candRun, comp := sampleFixtures()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseRun, candRun, comp).Build()
	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/comparisons", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "docs-eval-same-commit") {
		t.Errorf("expected comparison name in list")
	}
	if !strings.Contains(body, "IMPROVED") {
		t.Errorf("expected verdict IMPROVED in list")
	}
	if !strings.Contains(body, "0.98") {
		t.Errorf("expected P(imp) 0.98 in list")
	}
	if !strings.Contains(body, "/comparisons/eval-runner/docs-eval-same-commit") {
		t.Errorf("expected link to comparison detail")
	}
}

func TestServerComparisonDetail(t *testing.T) {
	scheme := newTestScheme(t)
	baseRun, candRun, comp := sampleFixtures()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseRun, candRun, comp).Build()
	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/comparisons/eval-runner/docs-eval-same-commit", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// Check verdict chip and P(improvement)
	if !strings.Contains(body, "IMPROVED") {
		t.Errorf("expected verdict IMPROVED in detail")
	}
	if !strings.Contains(body, "P(improvement) = 0.98") {
		t.Errorf("expected P(improvement) in headline")
	}

	// Check PR-comment table headers
	expectedHeaders := []string{"eval", "base", "this PR", "Δ (95% CI)", "label", "samples"}
	for _, h := range expectedHeaders {
		if !strings.Contains(body, h) {
			t.Errorf("expected PR-comment header %q in table", h)
		}
	}

	// Check row content
	if !strings.Contains(body, "triage/runbook_lookup") {
		t.Errorf("expected task id in table")
	}
	if !strings.Contains(body, "0.61") {
		t.Errorf("expected base rate 0.61")
	}
	if !strings.Contains(body, "0.84") {
		t.Errorf("expected pr rate 0.84")
	}
	if !strings.Contains(body, "&#43;0.23") {
		t.Errorf("expected delta &#43;0.23")
	}
	if !strings.Contains(body, "[&#43;0.11, &#43;0.35]") {
		t.Errorf("expected CI label in cell")
	}
	if !strings.Contains(body, "improved") {
		t.Errorf("expected label 'improved'")
	}
	if !strings.Contains(body, "48") {
		t.Errorf("expected runs count '48'")
	}

	// Check CI bar with zero line
	if !strings.Contains(body, `class="zero" style="left:170px"`) {
		t.Errorf("expected CI bar zero line at 170px")
	}
	if !strings.Contains(body, "good-c") {
		t.Errorf("expected good-c class for improved CI bar")
	}

	// Check footer
	if !strings.Contains(body, "a81d0c4") {
		t.Errorf("expected base commit in footer")
	}
	if !strings.Contains(body, "05ce4d8") {
		t.Errorf("expected candidate commit in footer")
	}
	if !strings.Contains(body, "paired") {
		t.Errorf("expected paired samples note in footer")
	}

	// Test 404 for missing comparison
	reqNotFound := httptest.NewRequest(http.MethodGet, "/comparisons/eval-runner/missing-comp", nil)
	wNotFound := httptest.NewRecorder()
	server.ServeHTTP(wNotFound, reqNotFound)
	if wNotFound.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing comparison, got %d", wNotFound.Code)
	}
	if strings.TrimSpace(wNotFound.Body.String()) != "comparison not found" {
		t.Errorf("expected 'comparison not found', got %q", wNotFound.Body.String())
	}
	if strings.Contains(wNotFound.Body.String(), "missing-comp") {
		t.Errorf("expected no reflected input in body, got %q", wNotFound.Body.String())
	}
}

func TestServerComparisonDetail_BatchesAndTokens(t *testing.T) {
	scheme := newTestScheme(t)
	baseRun, candRun, comp := sampleFixtures()

	// Add batches and tokens to comp status
	comp.Name = "comp-batches-tokens"
	comp.Status.Batches = []v1alpha1.ComparisonBatchStatus{
		{
			Index:        1,
			Phase:        v1alpha1.ComparisonPhaseCompleted,
			SampleRange:  &v1alpha1.SampleRange{Start: 1, End: 8},
			CandidateRun: "cand-b1-run",
			BaselineRun:  "base-b1-run",
		},
		{
			Index:        2,
			Phase:        v1alpha1.ComparisonPhaseRunning,
			SampleRange:  &v1alpha1.SampleRange{Start: 9, End: 16},
			CandidateRun: "cand-b2-run",
			BaselineRun:  "base-b2-run",
		},
	}
	comp.Status.Tasks[0].Tokens = &v1alpha1.TaskTokens{
		Input:  600,
		Output: 250,
		Total:  850,
	}
	comp.Status.Tasks[0].Label = v1alpha1.ComparisonTaskLabelInconclusive
	comp.Status.Tasks[0].SuggestedNext = 16

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseRun, candRun, comp).Build()
	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/comparisons/eval-runner/comp-batches-tokens", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// Check table headers have tokens and samples
	if !strings.Contains(body, "<th>tokens</th>") {
		t.Errorf("expected <th>tokens</th> header in table")
	}
	if !strings.Contains(body, "<th>samples</th>") {
		t.Errorf("expected <th>samples</th> header in table")
	}
	if !strings.Contains(body, "850") {
		t.Errorf("expected 850 tokens in table")
	}

	// Check Batches section rendered
	if !strings.Contains(body, "Batches</h2>") {
		t.Errorf("expected Batches section in detail view")
	}
	if !strings.Contains(body, "cand-b1-run") || !strings.Contains(body, "base-b1-run") {
		t.Errorf("expected batch run links in detail view")
	}
	if !strings.Contains(body, "<span><b>Batches:</b> 2</span>") {
		t.Errorf("expected Batches: 2 in footer")
	}

	// Check action hint
	if !strings.Contains(body, "<li>run 16 more samples") {
		t.Errorf("expected extraSamples hint in detail view")
	}
}

func TestServerPullsList(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now().Truncate(time.Second)
	pImp95 := 0.95
	pImp05 := 0.05
	compTime := metav1.NewTime(now.Add(-8 * time.Minute))

	// comp1: gke-labs/eval-runner PR 10 (head1, older)
	comp1 := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pr-10-head1",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-20 * time.Minute)),
		},
		Spec: v1alpha1.ComparisonSpec{
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "gke-labs/eval-runner",
				Number:     10,
				HeadSHA:    "1111111222222222222222222222222222222222",
				BaseSHA:    "0000000111111111111111111111111111111111",
			},
		},
		Status: v1alpha1.ComparisonStatus{
			Verdict: v1alpha1.ComparisonVerdictInconclusive,
		},
	}

	// comp2: gke-labs/eval-runner PR 10 (head2, newer push)
	comp2 := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pr-10-head2",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)),
		},
		Spec: v1alpha1.ComparisonSpec{
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "gke-labs/eval-runner",
				Number:     10,
				HeadSHA:    "2222222222222222222222222222222222222222",
				BaseSHA:    "0000000111111111111111111111111111111111",
			},
		},
		Status: v1alpha1.ComparisonStatus{
			Verdict:        v1alpha1.ComparisonVerdictImproved,
			PImprovement:   &pImp95,
			CompletionTime: &compTime,
			Batches: []v1alpha1.ComparisonBatchStatus{
				{Index: 1}, {Index: 2},
			},
			Tasks: []v1alpha1.ComparisonTaskStatus{
				{ID: "task-1", SamplesRun: 96, Pairs: 48},
			},
		},
	}

	// comp3: gke-labs/eval-runner PR 11 (newest in gke-labs/eval-runner)
	comp3 := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pr-11",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Minute)),
		},
		Spec: v1alpha1.ComparisonSpec{
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "gke-labs/eval-runner",
				Number:     11,
				HeadSHA:    "3333333222222222222222222222222222222222",
				BaseSHA:    "0000000111111111111111111111111111111111",
			},
		},
		Status: v1alpha1.ComparisonStatus{
			Verdict:      v1alpha1.ComparisonVerdictRegressed,
			PImprovement: &pImp05,
		},
	}

	// comp4: other-org/other-repo PR 10 (newest overall)
	comp4 := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pr-other-10",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Minute)),
		},
		Spec: v1alpha1.ComparisonSpec{
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "other-org/other-repo",
				Number:     10,
				HeadSHA:    "4444444222222222222222222222222222222222",
				BaseSHA:    "0000000111111111111111111111111111111111",
			},
		},
		Status: v1alpha1.ComparisonStatus{
			Verdict: v1alpha1.ComparisonVerdictUnchanged,
		},
	}

	// compNonPR: comparison without spec.pullRequest
	compNonPR := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "comp-non-pr",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
		},
		Spec: v1alpha1.ComparisonSpec{
			Baseline:  v1alpha1.ComparisonRunRef{Run: "base"},
			Candidate: v1alpha1.ComparisonRunRef{Run: "cand"},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(comp1, comp2, comp3, comp4, compNonPR).Build()
	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/pulls", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	body := w.Body.String()

	// 1. Assert non-PR comparison is excluded
	if strings.Contains(body, "comp-non-pr") {
		t.Errorf("expected comp-non-pr to be excluded from /pulls")
	}

	// 2. Assert grouping: gke-labs/eval-runner PR 10 should have only one row (with latest head2)
	if strings.Contains(body, "pr-10-head1") {
		t.Errorf("did not expect older comp pr-10-head1 in /pulls list view")
	}
	if !strings.Contains(body, "pr-10-head2") {
		t.Errorf("expected latest comparison pr-10-head2 in /pulls list view")
	}

	// 3. Assert ordering: other-org/other-repo (1m ago) before PR 11 (5m ago) before PR 10 (10m ago)
	posOther := strings.Index(body, "pr-other-10")
	posPR11 := strings.Index(body, "pr-11")
	posPR10 := strings.Index(body, "pr-10-head2")
	if posOther == -1 || posPR11 == -1 || posPR10 == -1 {
		t.Fatalf("missing PR comparisons in body: other=%d pr11=%d pr10=%d", posOther, posPR11, posPR10)
	}
	if !(posOther < posPR11 && posPR11 < posPR10) {
		t.Errorf("expected newest head first ordering: posOther=%d < posPR11=%d < posPR10=%d", posOther, posPR11, posPR10)
	}

	// 4. Assert links: GitHub link, comparison link, history link
	if !strings.Contains(body, "https://github.com/gke-labs/eval-runner/pull/10") {
		t.Errorf("expected GitHub link for PR 10")
	}
	if !strings.Contains(body, "/comparisons/eval-runner/pr-10-head2") {
		t.Errorf("expected Comparison link for PR 10")
	}
	hashGke := RepoHash("gke-labs/eval-runner")
	if !strings.Contains(body, fmt.Sprintf("/pulls/%s/10", hashGke)) {
		t.Errorf("expected history link for PR 10: /pulls/%s/10", hashGke)
	}

	// 5. Assert columns: Head 2222222, Verdict IMPROVED, P=0.95, Samples 96, Batches 2
	if !strings.Contains(body, "2222222") {
		t.Errorf("expected short head 2222222")
	}
	if !strings.Contains(body, "IMPROVED") {
		t.Errorf("expected verdict IMPROVED")
	}
	if !strings.Contains(body, "0.95") {
		t.Errorf("expected P(imp) 0.95")
	}
	if !strings.Contains(body, ">96<") {
		t.Errorf("expected samples run 96")
	}
	if !strings.Contains(body, ">2<") {
		t.Errorf("expected batches 2")
	}
}

func TestServerPullDetail(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now().Truncate(time.Second)
	pImp98 := 0.98
	compTime := metav1.NewTime(now.Add(-4 * time.Minute))

	comp1 := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pr-42-head1",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-20 * time.Minute)),
		},
		Spec: v1alpha1.ComparisonSpec{
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "gke-labs/eval-runner",
				Number:     42,
				HeadSHA:    "1111111222222222222222222222222222222222",
				BaseSHA:    "0000000111111111111111111111111111111111",
			},
		},
		Status: v1alpha1.ComparisonStatus{
			Verdict: v1alpha1.ComparisonVerdictInconclusive,
			Batches: []v1alpha1.ComparisonBatchStatus{{Index: 1}},
			Tasks:   []v1alpha1.ComparisonTaskStatus{{ID: "task-1", SamplesRun: 48}},
		},
	}

	comp2 := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pr-42-head2",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Minute)),
		},
		Spec: v1alpha1.ComparisonSpec{
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "gke-labs/eval-runner",
				Number:     42,
				HeadSHA:    "2222222222222222222222222222222222222222",
				BaseSHA:    "0000000111111111111111111111111111111111",
			},
		},
		Status: v1alpha1.ComparisonStatus{
			Verdict:        v1alpha1.ComparisonVerdictImproved,
			PImprovement:   &pImp98,
			CompletionTime: &compTime,
			Batches:        []v1alpha1.ComparisonBatchStatus{{Index: 1}, {Index: 2}},
			Tasks:          []v1alpha1.ComparisonTaskStatus{{ID: "task-1", SamplesRun: 96}},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(comp1, comp2).Build()
	server := NewServer(Config{Client: fakeClient})
	hash := RepoHash("gke-labs/eval-runner")

	t.Run("ByRepoHashAndNumberPath", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/pulls/%s/42", hash), nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()

		// Title and links
		if !strings.Contains(body, "gke-labs/eval-runner") || !strings.Contains(body, "#42") {
			t.Errorf("expected repo and PR number in title")
		}
		if !strings.Contains(body, "https://github.com/gke-labs/eval-runner/pull/42") {
			t.Errorf("expected GitHub link")
		}
		if !strings.Contains(body, "← Back to Pull Requests") {
			t.Errorf("expected back link")
		}

		// Both comparisons present
		if !strings.Contains(body, "pr-42-head2") || !strings.Contains(body, "pr-42-head1") {
			t.Errorf("expected both comparisons in history")
		}

		// Ordering: newer comp2 before older comp1
		pos2 := strings.Index(body, "pr-42-head2")
		pos1 := strings.Index(body, "pr-42-head1")
		if !(pos2 < pos1) {
			t.Errorf("expected comp2 (newer) before comp1 (older)")
		}

		// Verdicts
		if !strings.Contains(body, "IMPROVED") || !strings.Contains(body, "INCONCLUSIVE") {
			t.Errorf("expected both verdicts in history")
		}
	})

	t.Run("ByRepoAndNumberQuery", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/pulls?repo=gke-labs/eval-runner&number=42", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "pr-42-head2") || !strings.Contains(body, "pr-42-head1") {
			t.Errorf("expected both comparisons when queried via ?repo=...&number=...")
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/pulls/%s/999", hash), nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found, got %d", w.Code)
		}
		if strings.Contains(w.Body.String(), "999") || strings.Contains(w.Body.String(), hash) {
			t.Errorf("expected no reflected parameters in 404 body: %q", w.Body.String())
		}
	})

	t.Run("InvalidRouteParams", func(t *testing.T) {
		for _, urlPath := range []string{
			fmt.Sprintf("/pulls/%s/-1", hash),
			fmt.Sprintf("/pulls/%s/bad", hash),
			"/pulls/bad%20hash/42",
		} {
			req := httptest.NewRequest(http.MethodGet, urlPath, nil)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("path %s: expected 400 Bad Request, got %d", urlPath, w.Code)
			}
			for _, leak := range []string{"bad", "bad%20hash", "-1"} {
				if strings.Contains(w.Body.String(), leak) {
					t.Errorf("path %s: leaked input %q in body: %s", urlPath, leak, w.Body.String())
				}
			}
		}
	})
}

func TestServerPullsPagination(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now().Truncate(time.Second)

	comps := make([]client.Object, 5)
	for i := 0; i < 5; i++ {
		comps[i] = &v1alpha1.Comparison{
			ObjectMeta: metav1.ObjectMeta{
				Name:              fmt.Sprintf("pr-comp-%d", i+1),
				Namespace:         "eval-runner",
				CreationTimestamp: metav1.NewTime(now.Add(-time.Duration(5-i) * time.Minute)), // 5 is newest
			},
			Spec: v1alpha1.ComparisonSpec{
				PullRequest: &v1alpha1.PullRequestSpec{
					Repository: "gke-labs/eval-runner",
					Number:     i + 1,
					HeadSHA:    fmt.Sprintf("head%d1111111111111111111111111111111", i+1),
				},
			},
			Status: v1alpha1.ComparisonStatus{
				Verdict: v1alpha1.ComparisonVerdictImproved,
			},
		}
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(comps...).Build()
	server := NewServer(Config{Client: fakeClient})

	t.Run("Page1", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/pulls?limit=2&offset=0", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()

		// PR 5 and 4 should be present
		if !strings.Contains(body, ">#5<") || !strings.Contains(body, ">#4<") {
			t.Errorf("expected #5 and #4 on page 1")
		}
		if strings.Contains(body, ">#3<") {
			t.Errorf("did not expect #3 on page 1")
		}
		if !strings.Contains(body, "More →") {
			t.Errorf("expected More link on page 1")
		}
		if !strings.Contains(body, "offset=2") {
			t.Errorf("expected offset=2 in Next link")
		}
		if strings.Contains(body, "← Previous") {
			t.Errorf("did not expect Previous link on page 1")
		}
		if !strings.Contains(body, "Showing 1–2 of 5") {
			t.Errorf("expected Showing 1–2 of 5")
		}
	})

	t.Run("Page2", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/pulls?limit=2&offset=2", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()

		if !strings.Contains(body, ">#3<") || !strings.Contains(body, ">#2<") {
			t.Errorf("expected #3 and #2 on page 2")
		}
		if strings.Contains(body, ">#5<") || strings.Contains(body, ">#1<") {
			t.Errorf("unexpected PR on page 2")
		}
		if !strings.Contains(body, "More →") || !strings.Contains(body, "← Previous") {
			t.Errorf("expected both More and Previous links on page 2")
		}
		if !strings.Contains(body, "Showing 3–4 of 5") {
			t.Errorf("expected Showing 3–4 of 5")
		}
	})

	t.Run("Page3", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/pulls?limit=2&offset=4", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()

		if !strings.Contains(body, ">#1<") {
			t.Errorf("expected #1 on page 3")
		}
		if strings.Contains(body, "More →") {
			t.Errorf("did not expect More link on last page")
		}
		if !strings.Contains(body, "← Previous") {
			t.Errorf("expected Previous link on page 3")
		}
		if !strings.Contains(body, "Showing 5–5 of 5") {
			t.Errorf("expected Showing 5–5 of 5")
		}
	})
}

func TestServerPullsNamespaceFilterAndEmptyState(t *testing.T) {
	scheme := newTestScheme(t)

	compDefault := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-default",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.ComparisonSpec{
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "gke-labs/eval-runner",
				Number:     1,
				HeadSHA:    "1111111222222222222222222222222222222222",
			},
		},
		Status: v1alpha1.ComparisonStatus{Verdict: v1alpha1.ComparisonVerdictImproved},
	}

	compCustom := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-custom",
			Namespace: "custom-ns",
		},
		Spec: v1alpha1.ComparisonSpec{
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "custom-org/custom-repo",
				Number:     2,
				HeadSHA:    "2222222222222222222222222222222222222222",
			},
		},
		Status: v1alpha1.ComparisonStatus{Verdict: v1alpha1.ComparisonVerdictImproved},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(compDefault, compCustom).Build()
	server := NewServer(Config{Client: fakeClient})

	t.Run("DefaultNamespace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/pulls", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "gke-labs/eval-runner") {
			t.Errorf("expected default ns PR")
		}
		if strings.Contains(body, "custom-org/custom-repo") {
			t.Errorf("did not expect custom ns PR in default ns view")
		}
	})

	t.Run("CustomNamespace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/pulls?namespace=custom-ns", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "custom-org/custom-repo") {
			t.Errorf("expected custom ns PR")
		}
		if strings.Contains(body, "gke-labs/eval-runner") {
			t.Errorf("did not expect default ns PR in custom ns view")
		}
	})

	t.Run("EmptyNamespace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/pulls?namespace=empty-ns", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "No pull requests found in namespace empty-ns.") {
			t.Errorf("expected empty state message for empty-ns")
		}
	})

	t.Run("InvalidNamespace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/pulls?namespace=INVALID_NS", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d", w.Code)
		}
		if strings.Contains(w.Body.String(), "INVALID_NS") {
			t.Errorf("expected no reflected namespace in 400 body")
		}
	})
}

func TestServerPullsEscaping(t *testing.T) {
	scheme := newTestScheme(t)
	const hostile = `<script>alert('pwned')</script>`

	comp := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-hostile",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.ComparisonSpec{
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "evil/repo" + hostile,
				Number:     13,
				HeadSHA:    "head" + hostile,
			},
		},
		Status: v1alpha1.ComparisonStatus{
			Verdict: v1alpha1.ComparisonVerdictImproved,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(comp).Build()
	server := NewServer(Config{Client: fakeClient})

	t.Run("PullsListEscaping", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/pulls", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, "<script") {
			t.Errorf("unescaped <script found in /pulls HTML")
		}
		if !strings.Contains(body, "&lt;script&gt;") {
			t.Errorf("expected &lt;script&gt; in /pulls HTML")
		}
	})

	t.Run("PullDetailEscaping", func(t *testing.T) {
		hash := RepoHash("evil/repo" + hostile)
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/pulls/%s/13", hash), nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, "<script") {
			t.Errorf("unescaped <script found in /pulls/{repohash}/{number} HTML")
		}
		if !strings.Contains(body, "&lt;script&gt;") {
			t.Errorf("expected &lt;script&gt; in /pulls/{repohash}/{number} HTML")
		}
	})
}

func TestServerWebhookE2EFixture(t *testing.T) {
	scheme := newTestScheme(t)

	// Fixture matching the webhook e2e fixture from pkg/webhook/testdata/pull_request.json
	comp := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-305",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.ComparisonSpec{
			Baseline: v1alpha1.ComparisonRunRef{
				Run: "base-bbbbbbb",
			},
			Candidate: v1alpha1.ComparisonRunRef{
				Run: "pr-305-aaaaaaa",
			},
			Sampling: &v1alpha1.ComparisonSamplingSpec{
				BatchSize:         8,
				MaxSamplesPerTask: 64,
			},
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "gke-labs/eval-runner",
				Number:     305,
				HeadSHA:    "aaaaaaa111111122222223333333444444455555",
				BaseSHA:    "bbbbbbb111111122222223333333444444455555",
			},
		},
		Status: v1alpha1.ComparisonStatus{
			Phase:   v1alpha1.ComparisonPhaseCompleted,
			Verdict: v1alpha1.ComparisonVerdictImproved,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(comp).Build()
	server := NewServer(Config{Client: fakeClient})
	hash := RepoHash("gke-labs/eval-runner")

	// Both pages render for Comparisons created by the webhook e2e fixture
	t.Run("PullsListRendersWebhookFixture", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/pulls", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "gke-labs/eval-runner") {
			t.Errorf("expected repository in body")
		}
		if !strings.Contains(body, "#305") {
			t.Errorf("expected #305 in body")
		}
		if !strings.Contains(body, "aaaaaaa") {
			t.Errorf("expected short head aaaaaaa in body")
		}
		if !strings.Contains(body, "pr-305") {
			t.Errorf("expected pr-305 comparison in body")
		}
	})

	t.Run("PullDetailRendersWebhookFixture", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/pulls/%s/305", hash), nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "gke-labs/eval-runner") || !strings.Contains(body, "#305") {
			t.Errorf("expected repo and #305 in title")
		}
		if !strings.Contains(body, "pr-305") {
			t.Errorf("expected comparison name pr-305")
		}
		if !strings.Contains(body, "aaaaaaa") {
			t.Errorf("expected head SHA aaaaaaa")
		}
		if !strings.Contains(body, "bbbbbbb") {
			t.Errorf("expected base SHA bbbbbbb")
		}
	})
}

func TestServerRouteParamValidation(t *testing.T) {
	scheme := newTestScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	server := NewServer(Config{Client: fakeClient})

	invalidRoutes := []string{
		// Invalid namespace in path
		"/runs/UPPERCASE/run-1",
		"/runs/eval_runner/run-1",
		"/runs/-leading/run-1",
		"/runs/trailing-/run-1",
		"/runs/with..dots/run-1",
		// Invalid name in path
		"/runs/eval-runner/UPPERCASE",
		"/runs/eval-runner/-leading-hyphen",
		"/runs/eval-runner/trailing-hyphen-",
		"/runs/eval-runner/with..double.dot",
		"/runs/eval-runner/with_underscore",
		"/runs/eval-runner/with%20spaces",
		// Invalid query params on /runs
		"/runs?namespace=UPPERCASE",
		"/runs?namespace=bad_ns",
		"/runs?limit=-1",
		"/runs?limit=bad",
		"/runs?offset=-1",
		"/runs?offset=notanumber",
		// Invalid namespace in path on /comparisons
		"/comparisons/UPPERCASE/comp-1",
		"/comparisons/eval_runner/comp-1",
		// Invalid name in path on /comparisons
		"/comparisons/eval-runner/COMP_UPPERCASE",
		"/comparisons/eval-runner/-leading",
		"/comparisons/eval-runner/with_underscore",
		// Invalid query params on /comparisons
		"/comparisons?namespace=UPPERCASE",
		"/comparisons?namespace=bad_ns",
		"/comparisons?limit=-5",
		"/comparisons?limit=notanumber",
		"/comparisons?offset=-2",
		"/comparisons?offset=invalid",
		// Invalid query params on /pulls
		"/pulls?namespace=UPPERCASE",
		"/pulls?namespace=bad_ns",
		"/pulls?limit=-1",
		"/pulls?limit=bad",
		"/pulls?offset=-1",
		"/pulls?offset=notanumber",
		"/pulls?repo=gke-labs/eval-runner&number=-1",
		"/pulls?repo=gke-labs/eval-runner&number=notanumber",
		// Invalid route params on /pulls/{repohash}/{number}
		"/pulls/bad%20hash/1",
		"/pulls/2cc24c2/-1",
		"/pulls/2cc24c2/notanumber",
		"/pulls/2cc24c2/0",
	}

	for _, route := range invalidRoutes {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("route %q: expected 400 Bad Request, got %d", route, w.Code)
		}
		for _, leak := range []string{"UPPERCASE", "bad_ns", "notanumber", "with_underscore", "eval_runner", "bad%20hash", "2cc24c2"} {
			if strings.Contains(w.Body.String(), leak) {
				t.Errorf("route %q: expected no reflected input %q in body, got %q", route, leak, w.Body.String())
			}
		}
	}
}

type forbiddenClient struct {
	client.Reader
}

func (f *forbiddenClient) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	return apierrors.NewForbidden(schema.GroupResource{Group: "evalrunner.gke-labs.dev", Resource: "runs"}, key.Name, fmt.Errorf("access denied"))
}

func (f *forbiddenClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return apierrors.NewForbidden(schema.GroupResource{Group: "evalrunner.gke-labs.dev", Resource: "runs"}, "", fmt.Errorf("access denied"))
}

func TestServerForbidden(t *testing.T) {
	server := NewServer(Config{Client: &forbiddenClient{}})

	forbiddenRequests := []string{
		"/runs?namespace=secret-namespace",
		"/runs/eval-runner/run-1",
		"/comparisons?namespace=secret-namespace",
		"/comparisons/eval-runner/comp-1",
		"/pulls?namespace=secret-namespace",
		"/pulls/2cc24c2/42?namespace=secret-namespace",
	}

	for _, route := range forbiddenRequests {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("route %q: expected 403 Forbidden, got %d", route, w.Code)
		}
		for _, leak := range []string{"secret-namespace", "run-1", "comp-1", "access denied", "2cc24c2"} {
			if strings.Contains(w.Body.String(), leak) {
				t.Errorf("route %q: expected no leaked details %q in body, got %q", route, leak, w.Body.String())
			}
		}
	}
}

func TestServerPagination(t *testing.T) {
	scheme := newTestScheme(t)

	// Create 5 runs with distinct timestamps
	now := time.Now()
	runs := make([]client.Object, 5)
	for i := 0; i < 5; i++ {
		runs[i] = &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name:              fmt.Sprintf("run-%d", i+1),
				Namespace:         "eval-runner",
				CreationTimestamp: metav1.NewTime(now.Add(-time.Duration(5-i) * time.Minute)), // run-5 is newest
			},
			Spec: v1alpha1.RunSpec{
				Repo:  v1alpha1.RepoSpec{Commit: fmt.Sprintf("commit%d", i+1)},
				Model: "model",
			},
			Status: v1alpha1.RunStatus{
				Phase: v1alpha1.RunPhaseCompleted,
			},
		}
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runs...).Build()
	server := NewServer(Config{Client: fakeClient})

	t.Run("Page1", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs?limit=2&offset=0", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()

		// run-5 and run-4 should be present (newest first)
		if !strings.Contains(body, "run-5") || !strings.Contains(body, "run-4") {
			t.Errorf("expected run-5 and run-4 on page 1")
		}
		if strings.Contains(body, "run-3") {
			t.Errorf("did not expect run-3 on page 1")
		}

		// Pagination controls: More link should be present, Previous should not
		if !strings.Contains(body, "More →") {
			t.Errorf("expected More link on page 1")
		}
		if !strings.Contains(body, "offset=2") {
			t.Errorf("expected More link to contain offset=2")
		}
		if strings.Contains(body, "← Previous") {
			t.Errorf("did not expect Previous link on page 1")
		}
		if !strings.Contains(body, "Showing 1–2 of 5") {
			t.Errorf("expected range 'Showing 1–2 of 5'")
		}
	})

	t.Run("Page2", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs?limit=2&offset=2", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()

		// run-3 and run-2 should be present
		if !strings.Contains(body, "run-3") || !strings.Contains(body, "run-2") {
			t.Errorf("expected run-3 and run-2 on page 2")
		}
		if strings.Contains(body, "run-5") || strings.Contains(body, "run-1") {
			t.Errorf("unexpected run on page 2")
		}

		if !strings.Contains(body, "More →") {
			t.Errorf("expected More link on page 2")
		}
		if !strings.Contains(body, "offset=4") {
			t.Errorf("expected More link to contain offset=4")
		}
		if !strings.Contains(body, "← Previous") {
			t.Errorf("expected Previous link on page 2")
		}
		if !strings.Contains(body, "Showing 3–4 of 5") {
			t.Errorf("expected range 'Showing 3–4 of 5'")
		}
	})

	t.Run("Page3", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs?limit=2&offset=4", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()

		// run-1 should be present
		if !strings.Contains(body, "run-1") {
			t.Errorf("expected run-1 on page 3")
		}
		if strings.Contains(body, "run-2") {
			t.Errorf("did not expect run-2 on page 3")
		}

		// Last page: no More link
		if strings.Contains(body, "More →") {
			t.Errorf("did not expect More link on last page")
		}
		if !strings.Contains(body, "← Previous") {
			t.Errorf("expected Previous link on page 3")
		}
		if !strings.Contains(body, "offset=2") {
			t.Errorf("expected Previous link to contain offset=2")
		}
		if !strings.Contains(body, "Showing 5–5 of 5") {
			t.Errorf("expected range 'Showing 5–5 of 5'")
		}
	})

	t.Run("ComparisonsPagination", func(t *testing.T) {
		comps := make([]client.Object, 3)
		for i := 0; i < 3; i++ {
			comps[i] = &v1alpha1.Comparison{
				ObjectMeta: metav1.ObjectMeta{
					Name:              fmt.Sprintf("comp-%d", i+1),
					Namespace:         "eval-runner",
					CreationTimestamp: metav1.NewTime(now.Add(-time.Duration(3-i) * time.Minute)),
				},
				Spec: v1alpha1.ComparisonSpec{
					Baseline:  v1alpha1.ComparisonRunRef{Run: "base"},
					Candidate: v1alpha1.ComparisonRunRef{Run: "cand"},
				},
				Status: v1alpha1.ComparisonStatus{
					Verdict: v1alpha1.ComparisonVerdictImproved,
				},
			}
		}

		compClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(comps...).Build()
		compServer := NewServer(Config{Client: compClient})

		req := httptest.NewRequest(http.MethodGet, "/comparisons?limit=2&offset=0", nil)
		w := httptest.NewRecorder()
		compServer.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()

		if !strings.Contains(body, "comp-3") || !strings.Contains(body, "comp-2") {
			t.Errorf("expected comp-3 and comp-2 on page 1")
		}
		if strings.Contains(body, "comp-1") {
			t.Errorf("did not expect comp-1 on page 1")
		}
		if !strings.Contains(body, "More →") {
			t.Errorf("expected More link on comparisons page 1")
		}
	})
}

func TestServerSortTiebreaker(t *testing.T) {
	scheme := newTestScheme(t)
	now := time.Now().Truncate(time.Second)

	t.Run("RunsSortTiebreaker", func(t *testing.T) {
		names := []string{"run-charlie", "run-alpha", "run-bravo"}
		runs := make([]client.Object, len(names))
		for i, name := range names {
			runs[i] = &v1alpha1.Run{
				ObjectMeta: metav1.ObjectMeta{
					Name:              name,
					Namespace:         "eval-runner",
					CreationTimestamp: metav1.NewTime(now),
				},
				Spec: v1alpha1.RunSpec{
					Repo:  v1alpha1.RepoSpec{Commit: "a81d0c4"},
					Model: "model",
				},
				Status: v1alpha1.RunStatus{
					Phase: v1alpha1.RunPhaseCompleted,
				},
			}
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runs...).Build()
		server := NewServer(Config{Client: fakeClient})

		req := httptest.NewRequest(http.MethodGet, "/runs", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()

		idxAlpha := strings.Index(body, "run-alpha")
		idxBravo := strings.Index(body, "run-bravo")
		idxCharlie := strings.Index(body, "run-charlie")

		if idxAlpha == -1 || idxBravo == -1 || idxCharlie == -1 {
			t.Fatalf("expected all runs to appear in response body")
		}
		if !(idxAlpha < idxBravo && idxBravo < idxCharlie) {
			t.Errorf("expected runs to be sorted alphabetically on equal timestamp: alpha=%d bravo=%d charlie=%d", idxAlpha, idxBravo, idxCharlie)
		}
	})

	t.Run("ComparisonsSortTiebreaker", func(t *testing.T) {
		names := []string{"comp-charlie", "comp-alpha", "comp-bravo"}
		comps := make([]client.Object, len(names))
		for i, name := range names {
			comps[i] = &v1alpha1.Comparison{
				ObjectMeta: metav1.ObjectMeta{
					Name:              name,
					Namespace:         "eval-runner",
					CreationTimestamp: metav1.NewTime(now),
				},
				Spec: v1alpha1.ComparisonSpec{
					Baseline:  v1alpha1.ComparisonRunRef{Run: "base"},
					Candidate: v1alpha1.ComparisonRunRef{Run: "cand"},
				},
				Status: v1alpha1.ComparisonStatus{
					Verdict: v1alpha1.ComparisonVerdictImproved,
				},
			}
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(comps...).Build()
		server := NewServer(Config{Client: fakeClient})

		req := httptest.NewRequest(http.MethodGet, "/comparisons", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		body := w.Body.String()

		idxAlpha := strings.Index(body, "comp-alpha")
		idxBravo := strings.Index(body, "comp-bravo")
		idxCharlie := strings.Index(body, "comp-charlie")

		if idxAlpha == -1 || idxBravo == -1 || idxCharlie == -1 {
			t.Fatalf("expected all comparisons to appear in response body")
		}
		if !(idxAlpha < idxBravo && idxBravo < idxCharlie) {
			t.Errorf("expected comparisons to be sorted alphabetically on equal timestamp: alpha=%d bravo=%d charlie=%d", idxAlpha, idxBravo, idxCharlie)
		}
	})
}

func TestServerRunsList_SampleRange(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "range-run",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.Now(),
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/example/repo",
				Commit: "abcdef123456",
			},
			Model: "mockllm/model",
			SampleRange: &v1alpha1.SampleRange{
				Start: 9,
				End:   16,
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Summary: &v1alpha1.RunSummary{
				Tasks:          "1/1",
				TasksCompleted: 1,
				TasksTotal:     1,
				TotalSamples:   8,
			},
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:      "triage/task",
					Phase:   v1alpha1.TaskPhaseCompleted,
					Samples: 8,
					SampleRange: &v1alpha1.SampleRange{
						Start: 9,
						End:   16,
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		Build()

	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/runs", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "samples 9-16") {
		t.Errorf("expected Runs view to contain 'samples 9-16', got body:\n%s", body)
	}
}

func TestServerRunDetail_SampleRange(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "range-run-detail",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.Now(),
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/example/repo",
				Commit: "abcdef123456",
			},
			Model: "mockllm/model",
			SampleRange: &v1alpha1.SampleRange{
				Start: 9,
				End:   16,
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Summary: &v1alpha1.RunSummary{
				Tasks:          "1/1",
				TasksCompleted: 1,
				TasksTotal:     1,
				TotalSamples:   8,
			},
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:      "triage/task",
					Phase:   v1alpha1.TaskPhaseCompleted,
					Samples: 8,
					SampleRange: &v1alpha1.SampleRange{
						Start: 9,
						End:   16,
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		Build()

	server := NewServer(Config{Client: fakeClient})

	req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/range-run-detail", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "samples 9-16") {
		t.Errorf("expected Run Detail view to contain 'samples 9-16', got body:\n%s", body)
	}
}

type errorSink struct {
	err error
}

func (s *errorSink) StoreArtifact(ctx context.Context, meta artifacts.RunMetadata, filename string, data []byte) (string, error) {
	return "", nil
}
func (s *errorSink) StoreComparison(ctx context.Context, comparisonName string, data []byte) (string, error) {
	return "", nil
}
func (s *errorSink) StoreManifest(ctx context.Context, meta artifacts.RunMetadata, data []byte) (string, error) {
	return "", nil
}
func (s *errorSink) Load(ctx context.Context, path string) ([]byte, error) {
	return nil, s.err
}
func (s *errorSink) ArtifactPrefix(commit string) string {
	return ""
}
func (s *errorSink) IsDurable() bool {
	return false
}
func (s *errorSink) Close() error {
	return nil
}

func TestServerTaskSamplesAndTranscript(t *testing.T) {
	scheme := newTestScheme(t)
	baseDir := t.TempDir()
	sink := &artifacts.LocalDirSink{BaseDir: baseDir}

	fixturePath := filepath.Join("..", "artifacts", "testdata", "trajectory_messages.json")
	fixtureData, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("failed to read trajectory_messages fixture: %v", err)
	}

	commit := "abcdef1234567890"
	taskID := "triage/runbook_lookup"
	meanScore := 0.5
	now := time.Now()
	meta := artifacts.RunMetadata{
		TaskID:     taskID,
		Commit:     commit,
		Model:      "gemini-2.5-pro",
		Samples:    2,
		Successes:  1,
		MeanScore:  &meanScore,
		StartedAt:  &now,
		FinishedAt: &now,
	}

	logURI, err := sink.StoreArtifact(t.Context(), meta, "inspect.json", fixtureData)
	if err != nil {
		t.Fatalf("failed to store artifact: %v", err)
	}

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sample-run",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: commit,
			},
			Model: "gemini-2.5-pro",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:      taskID,
					Phase:   v1alpha1.TaskPhaseCompleted,
					LogPath: logURI,
				},
				{
					ID:    "task/no-log",
					Phase: v1alpha1.TaskPhasePending,
				},
				{
					ID:      "task/bad-prefix",
					Phase:   v1alpha1.TaskPhaseCompleted,
					LogPath: filepath.Join(baseDir, "runs", "othercommit12345", "inspect.json"),
				},
				{
					ID:      "task/file-not-found",
					Phase:   v1alpha1.TaskPhaseCompleted,
					LogPath: filepath.Join(baseDir, "runs", commit, "missing", "inspect.json"),
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		Build()

	server := NewServer(Config{
		Client:           fakeClient,
		DefaultNamespace: "eval-runner",
		ArtifactSink:     sink,
	})

	t.Run("SamplesList_Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/sample-run/tasks/triage%2Frunbook_lookup/samples", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}

		body := w.Body.String()
		for _, expected := range []string{
			"sample-017",
			"sample-019",
			"1.00",
			"0.00",
			"3.0s",
			"13.0s",
			"1,500",
			"41,200",
			"Cleared CDN cache",
			"Escalated to on-call",
			"<th>pass rule</th>",
			"<th>grader reason</th>",
			`<pre class="preview-text" title="Resolved via runbook">Resolved via runbook</pre>`,
			`<pre class="preview-text" title="failed runbook check">failed runbook check</pre>`,
			`title="pass: the reply names the cache purge and the HTTP 200 recheck"`,
		} {
			if !strings.Contains(body, expected) {
				t.Errorf("expected samples list to contain %q, got body:\n%s", expected, body)
			}
		}

		normalizedBody := strings.ReplaceAll(body, baseDir, "/tmp/eval-runner-sink")
		assertGolden(t, "task_samples.golden.html", normalizedBody)
	})

	t.Run("SampleTranscript_FailSample", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/sample-run/tasks/triage%2Frunbook_lookup/samples/sample-019", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}

		body := w.Body.String()
		for _, expected := range []string{
			"sample-019",
			"FAIL",
			"user",
			"Customer reports checkout returns HTTP 502",
			"tool_call",
			"classify_severity",
			"page_oncall",
			"tool",
			"high",
			"Paged on-call team payments",
			"assistant",
			"Escalated to on-call. Severity high. No further action.",
			"class=\"ev bad\"",
			"<span class=\"k\">pass rule</span>\n          <pre class=\"b\">Resolved via runbook</pre>",
			"<span class=\"k\">grader reason</span>\n          <pre class=\"b\">failed runbook check</pre>",
		} {
			if !strings.Contains(body, expected) {
				t.Errorf("expected sample transcript to contain %q, got body:\n%s", expected, body)
			}
		}

		normalizedBody := strings.ReplaceAll(body, baseDir, "/tmp/eval-runner-sink")
		assertGolden(t, "sample_transcript.golden.html", normalizedBody)
	})

	t.Run("SampleTranscript_PassSample", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/sample-run/tasks/triage%2Frunbook_lookup/samples/sample-017", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}

		body := w.Body.String()
		if !strings.Contains(body, "PASS") {
			t.Errorf("expected sample transcript to contain 'PASS', got body:\n%s", body)
		}
		if !strings.Contains(body, "check_cdn") {
			t.Errorf("expected sample transcript to contain 'check_cdn', got body:\n%s", body)
		}
	})

	t.Run("Negative_MissingLogPath_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/sample-run/tasks/task%2Fno-log/samples", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 for missing logPath, got %d", w.Code)
		}
	})

	t.Run("Negative_NoSuchTask_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/sample-run/tasks/nonexistent%2Ftask/samples", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 for nonexistent task, got %d", w.Code)
		}
		if strings.TrimSpace(w.Body.String()) != "log path not found for task" {
			t.Errorf("expected exact 'log path not found for task' message, got %q", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "nonexistent") {
			t.Errorf("expected no reflected task ID in body, got %q", w.Body.String())
		}
	})

	t.Run("Negative_NoSuchRun_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/no-such-run/tasks/triage%2Frunbook_lookup/samples", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 for nonexistent run, got %d", w.Code)
		}
		if strings.TrimSpace(w.Body.String()) != "run not found" {
			t.Errorf("expected exact 'run not found' message, got %q", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "no-such-run") || strings.Contains(w.Body.String(), "eval-runner") {
			t.Errorf("expected no reflected input in body, got %q", w.Body.String())
		}
	})

	t.Run("Negative_NilSink_404", func(t *testing.T) {
		serverNoSink := NewServer(Config{
			Client:           fakeClient,
			DefaultNamespace: "eval-runner",
			ArtifactSink:     nil,
		})
		req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/sample-run/tasks/triage%2Frunbook_lookup/samples", nil)
		w := httptest.NewRecorder()
		serverNoSink.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 when ArtifactSink is nil, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "artifact storage not configured") {
			t.Errorf("expected 'artifact storage not configured' message, got %q", w.Body.String())
		}
	})

	t.Run("Negative_PrefixMismatch_403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/sample-run/tasks/task%2Fbad-prefix/samples", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for prefix mismatch, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("Negative_StorageFileNotFound_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/sample-run/tasks/task%2Ffile-not-found/samples", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 for storage file not found, got %d: %s", w.Code, w.Body.String())
		}
		if strings.TrimSpace(w.Body.String()) != "task log not found on storage" {
			t.Errorf("expected exact 'task log not found on storage' message, got %q", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "missing") || strings.Contains(w.Body.String(), "file-not-found") {
			t.Errorf("expected no leaked storage path in body, got %q", w.Body.String())
		}
	})

	t.Run("Negative_NoSuchSample_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/sample-run/tasks/triage%2Frunbook_lookup/samples/no-such-sample", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 for nonexistent sample, got %d", w.Code)
		}
		if strings.TrimSpace(w.Body.String()) != "sample not found" {
			t.Errorf("expected exact 'sample not found' message, got %q", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "no-such-sample") {
			t.Errorf("expected no reflected input in body, got %q", w.Body.String())
		}
	})

	t.Run("Negative_InvalidRouteParams_400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs/invalid_namespace/sample-run/tasks/triage%2Frunbook_lookup/samples", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid namespace, got %d", w.Code)
		}
		if strings.TrimSpace(w.Body.String()) != "invalid route params" {
			t.Errorf("expected exact 'invalid route params' message, got %q", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "invalid_namespace") {
			t.Errorf("expected no reflected input in body, got %q", w.Body.String())
		}
	})

	t.Run("Negative_StorageError_WrappedGoogleAPI_FixedBodyWithoutLeaks", func(t *testing.T) {
		secretLeak := "googleapi: got HTTP response code 403 with body: <?xml version=\"1.0\"?><Error><Code>AccessDenied</Code><BucketName>super-secret-eval-bucket</BucketName></Error>"
		wrappedErr := fmt.Errorf("failed to read artifact from gs://super-secret-eval-bucket/runs/%s/triage/runbook_lookup/inspect.json: %w", commit, errors.New(secretLeak))

		serverWithErrSink := NewServer(Config{
			Client:           fakeClient,
			DefaultNamespace: "eval-runner",
			ArtifactSink:     &errorSink{err: wrappedErr},
		})

		paths := []string{
			"/runs/eval-runner/sample-run/tasks/triage%2Frunbook_lookup/samples",
			"/runs/eval-runner/sample-run/tasks/triage%2Frunbook_lookup/samples/sample-017",
		}

		for _, path := range paths {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			serverWithErrSink.ServeHTTP(w, req)

			if w.Code != http.StatusBadGateway {
				t.Fatalf("%s: expected 502 Bad Gateway, got %d: %s", path, w.Code, w.Body.String())
			}
			expectedBody := "artifact storage unavailable; see UI logs"
			if strings.TrimSpace(w.Body.String()) != expectedBody {
				t.Errorf("%s: expected fixed body %q, got %q", path, expectedBody, w.Body.String())
			}
			for _, leak := range []string{"googleapi", "AccessDenied", "super-secret-eval-bucket", "<?xml", "gs://"} {
				if strings.Contains(w.Body.String(), leak) {
					t.Errorf("%s: body leaks raw error string %q: %s", path, leak, w.Body.String())
				}
			}
		}
	})

	t.Run("Negative_CappedOrTruncatedLog_FriendlyPage", func(t *testing.T) {
		// Store truncated invalid JSON
		metaTrunc := artifacts.RunMetadata{
			TaskID:     "task/truncated",
			Commit:     commit,
			Model:      "gemini-2.5-pro",
			Samples:    1,
			StartedAt:  &now,
			FinishedAt: &now,
		}
		truncURI, err := sink.StoreArtifact(t.Context(), metaTrunc, "inspect.json", []byte(`{"version": 2, "samples": [{"id": 1, "input": "incomp`))
		if err != nil {
			t.Fatalf("failed to store truncated artifact: %v", err)
		}

		runTrunc := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "run-truncated",
				Namespace: "eval-runner",
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					Commit: commit,
				},
				Model: "gemini-2.5-pro",
			},
			Status: v1alpha1.RunStatus{
				Phase: v1alpha1.RunPhaseCompleted,
				Tasks: []v1alpha1.TaskStatus{
					{
						ID:      "task/truncated",
						Phase:   v1alpha1.TaskPhaseCompleted,
						LogPath: truncURI,
					},
				},
			},
		}

		clientTrunc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runTrunc).Build()
		serverTrunc := NewServer(Config{Client: clientTrunc, ArtifactSink: sink})

		// List page should return 200 with friendly message, not 500
		reqList := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/run-truncated/tasks/task%2Ftruncated/samples", nil)
		wList := httptest.NewRecorder()
		serverTrunc.ServeHTTP(wList, reqList)

		if wList.Code != http.StatusOK {
			t.Errorf("expected 200 OK for truncated log list page, got %d", wList.Code)
		}
		if !strings.Contains(wList.Body.String(), "Log Truncated") {
			t.Errorf("expected 'Log Truncated' message, got body:\n%s", wList.Body.String())
		}

		// Transcript page should return 200 with friendly message, not 500
		reqTranscript := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/run-truncated/tasks/task%2Ftruncated/samples/1", nil)
		wTranscript := httptest.NewRecorder()
		serverTrunc.ServeHTTP(wTranscript, reqTranscript)

		if wTranscript.Code != http.StatusOK {
			t.Errorf("expected 200 OK for truncated log transcript page, got %d", wTranscript.Code)
		}
		if !strings.Contains(wTranscript.Body.String(), "Log Truncated") {
			t.Errorf("expected 'Log Truncated' message, got body:\n%s", wTranscript.Body.String())
		}
	})

	t.Run("Escaping_HostileContent", func(t *testing.T) {
		hostileJSON := `{
			"version": 2,
			"status": "success",
			"eval": {"task": "task/hostile"},
			"samples": [
				{
					"id": "sample-xss",
					"epoch": 1,
					"input": "<script>alert('input-xss')</script>",
					"output": {"completion": "<img src=x onerror=alert('output-xss')>"},
					"messages": [
						{
							"role": "user",
							"content": "<script>alert('msg-xss')</script>"
						},
						{
							"role": "assistant",
							"tool_calls": [
								{
									"id": "c1",
									"function": "malicious<script>",
									"arguments": {"arg": "<iframe src=bad></iframe>"}
								}
							]
						}
					]
				}
			]
		}`
		metaHostile := artifacts.RunMetadata{
			TaskID:     "task/hostile",
			Commit:     commit,
			Model:      "gemini-2.5-pro",
			Samples:    1,
			StartedAt:  &now,
			FinishedAt: &now,
		}
		hostileURI, err := sink.StoreArtifact(t.Context(), metaHostile, "inspect.json", []byte(hostileJSON))
		if err != nil {
			t.Fatalf("failed to store hostile artifact: %v", err)
		}

		runHostile := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "run-hostile",
				Namespace: "eval-runner",
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{Commit: commit},
			},
			Status: v1alpha1.RunStatus{
				Phase: v1alpha1.RunPhaseCompleted,
				Tasks: []v1alpha1.TaskStatus{
					{
						ID:      "task/hostile",
						Phase:   v1alpha1.TaskPhaseCompleted,
						LogPath: hostileURI,
					},
				},
			},
		}

		clientHostile := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runHostile).Build()
		serverHostile := NewServer(Config{Client: clientHostile, ArtifactSink: sink})

		// List page
		reqList := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/run-hostile/tasks/task%2Fhostile/samples", nil)
		wList := httptest.NewRecorder()
		serverHostile.ServeHTTP(wList, reqList)

		bodyList := wList.Body.String()
		if strings.Contains(bodyList, "<script>alert") {
			t.Errorf("raw unescaped script tag found in list page HTML:\n%s", bodyList)
		}
		if !strings.Contains(bodyList, "&lt;script&gt;alert") {
			t.Errorf("expected escaped script tag in list page HTML")
		}

		// Transcript page
		reqTranscript := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/run-hostile/tasks/task%2Fhostile/samples/sample-xss", nil)
		wTranscript := httptest.NewRecorder()
		serverHostile.ServeHTTP(wTranscript, reqTranscript)

		bodyTranscript := wTranscript.Body.String()
		if strings.Contains(bodyTranscript, "<script>alert") {
			t.Errorf("raw unescaped script tag found in transcript page HTML:\n%s", bodyTranscript)
		}
		if !strings.Contains(bodyTranscript, "&lt;script&gt;alert") {
			t.Errorf("expected escaped script tag in transcript page HTML")
		}
		if strings.Contains(bodyTranscript, "<iframe src=bad>") {
			t.Errorf("raw unescaped iframe tag found in transcript page HTML:\n%s", bodyTranscript)
		}
	})
}

func TestLogLRUCache(t *testing.T) {
	t.Run("GetAndSet", func(t *testing.T) {
		cache := newLogLRUCache(1024, time.Hour)
		sample := artifacts.ParsedInspectSample{ID: "s1"}
		cache.set("key1", 50, []artifacts.ParsedInspectSample{sample}, nil)

		got, err, ok := cache.get("key1")
		if !ok || err != nil || len(got) != 1 || got[0].ID != "s1" {
			t.Fatalf("expected cache hit for key1, got ok=%v, err=%v, len=%d", ok, err, len(got))
		}

		_, _, ok = cache.get("nonexistent")
		if ok {
			t.Errorf("expected cache miss for nonexistent key")
		}
	})

	t.Run("LRUEvictionByBytes", func(t *testing.T) {
		cache := newLogLRUCache(100, time.Hour)
		cache.set("k1", 60, []artifacts.ParsedInspectSample{{ID: "s1"}}, nil)
		cache.set("k2", 50, []artifacts.ParsedInspectSample{{ID: "s2"}}, nil)

		// Total bytes would be 110 > 100, so k1 should be evicted
		_, _, ok1 := cache.get("k1")
		if ok1 {
			t.Errorf("expected k1 to be evicted by byte limit")
		}
		_, _, ok2 := cache.get("k2")
		if !ok2 {
			t.Errorf("expected k2 to be present in cache")
		}
	})

	t.Run("TTLExpiration", func(t *testing.T) {
		cache := newLogLRUCache(1024, 10*time.Millisecond)
		cache.set("k1", 10, []artifacts.ParsedInspectSample{{ID: "s1"}}, nil)

		time.Sleep(20 * time.Millisecond)

		_, _, ok := cache.get("k1")
		if ok {
			t.Errorf("expected k1 to be expired after TTL")
		}
	})
}

func TestTruncateMessageBytes_UTF8(t *testing.T) {
	// Japanese text: each rune is 3 bytes in UTF-8
	s := "こんにちは世界"
	// Slicing at 5 bytes falls in the middle of the second rune.
	// TruncateMessageBytes should walk back to the rune boundary at 3 bytes ('こ').
	truncated := TruncateMessageBytes(s, 5)
	expectedPrefix := "こ\n... [content truncated]"
	if truncated != expectedPrefix {
		t.Errorf("expected %q, got %q", expectedPrefix, truncated)
	}

	// Short string under limit should remain unchanged
	if notTruncated := TruncateMessageBytes("hello", 10); notTruncated != "hello" {
		t.Errorf("expected 'hello', got %q", notTruncated)
	}
}

func TestComparisonCIHistory(t *testing.T) {
	scheme := newTestScheme(t)
	baseDir := t.TempDir()
	sink := &artifacts.LocalDirSink{BaseDir: baseDir}

	// 1. Prepare positive fixture: 3 batches, 12 pairs
	pairs := make([]stats.PairedSampleScore, 12)
	for i := 0; i < 12; i++ {
		cand := 0.85
		base := 0.55
		pairs[i] = stats.PairedSampleScore{
			ID:             fmt.Sprintf("sample-%d", i+1),
			BaseScore:      base,
			CandidateScore: cand,
			Delta:          cand - base,
		}
	}

	taskID := "task/test-eval"
	fixtureArtifact := artifacts.ComparisonArtifact{
		Comparison: "comp-history-fixture",
		Namespace:  "eval-runner",
		Verdict:    "IMPROVED",
		Tasks: []artifacts.ComparisonTaskArtifact{
			{
				ID:         taskID,
				Pairs:      12,
				PairsTable: pairs,
			},
		},
		Batches: []v1alpha1.ComparisonBatchStatus{
			{Index: 1, SampleRange: &v1alpha1.SampleRange{Start: 1, End: 4}},
			{Index: 2, SampleRange: &v1alpha1.SampleRange{Start: 5, End: 8}},
			{Index: 3, SampleRange: &v1alpha1.SampleRange{Start: 9, End: 12}},
		},
	}
	artBytes, err := json.Marshal(fixtureArtifact)
	if err != nil {
		t.Fatalf("failed to marshal fixture artifact: %v", err)
	}

	summaryPath, err := sink.StoreComparison(t.Context(), "comp-history-fixture", artBytes)
	if err != nil {
		t.Fatalf("failed to store comparison fixture: %v", err)
	}

	compFixture := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "comp-history-fixture",
			Namespace: "eval-runner",
		},
		Status: v1alpha1.ComparisonStatus{
			Phase:       v1alpha1.ComparisonPhaseCompleted,
			Verdict:     v1alpha1.ComparisonVerdictImproved,
			SummaryPath: summaryPath,
			Tasks: []v1alpha1.ComparisonTaskStatus{
				{
					ID:         taskID,
					SamplesRun: 12,
					Decision:   v1alpha1.ComparisonTaskLabelImproved,
				},
			},
			Batches: []v1alpha1.ComparisonBatchStatus{
				{Index: 1, SampleRange: &v1alpha1.SampleRange{Start: 1, End: 4}},
				{Index: 2, SampleRange: &v1alpha1.SampleRange{Start: 5, End: 8}},
				{Index: 3, SampleRange: &v1alpha1.SampleRange{Start: 9, End: 12}},
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(compFixture).Build()
	server := NewServer(Config{Client: client, ArtifactSink: sink})

	t.Run("Positive_3Batches_12Pairs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/comparisons/eval-runner/comp-history-fixture", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}

		body := w.Body.String()
		if !strings.Contains(body, "Confidence Interval History") {
			t.Errorf("expected 'Confidence Interval History' section in HTML, got:\n%s", body)
		}

		// Assert SVG numeric attributes
		circleRegex := regexp.MustCompile(`<circle\s+cx="([^"]+)"\s+cy="([^"]+)"\s+r="([^"]+)"`)
		circleMatches := circleRegex.FindAllStringSubmatch(body, -1)
		if len(circleMatches) != 3 {
			t.Fatalf("expected 3 circle points in SVG, got %d", len(circleMatches))
		}

		for idx, m := range circleMatches {
			cx, err1 := strconv.ParseFloat(m[1], 64)
			cy, err2 := strconv.ParseFloat(m[2], 64)
			r, err3 := strconv.ParseFloat(m[3], 64)
			if err1 != nil || err2 != nil || err3 != nil || math.IsNaN(cx) || math.IsNaN(cy) || math.IsNaN(r) {
				t.Errorf("point %d: invalid numeric SVG circle attribute cx=%q cy=%q r=%q", idx, m[1], m[2], m[3])
			}
		}

		lineRegex := regexp.MustCompile(`<line\s+x1="([^"]+)"\s+y1="([^"]+)"\s+x2="([^"]+)"\s+y2="([^"]+)"`)
		lineMatches := lineRegex.FindAllStringSubmatch(body, -1)
		if len(lineMatches) == 0 {
			t.Fatalf("expected SVG line elements, found none")
		}
		for idx, m := range lineMatches {
			x1, err1 := strconv.ParseFloat(m[1], 64)
			y1, err2 := strconv.ParseFloat(m[2], 64)
			x2, err3 := strconv.ParseFloat(m[3], 64)
			y2, err4 := strconv.ParseFloat(m[4], 64)
			if err1 != nil || err2 != nil || err3 != nil || err4 != nil || math.IsNaN(x1) || math.IsNaN(y1) || math.IsNaN(x2) || math.IsNaN(y2) {
				t.Errorf("line %d: invalid numeric SVG line attribute x1=%q y1=%q x2=%q y2=%q", idx, m[1], m[2], m[3], m[4])
			}
		}

		if !strings.Contains(body, "margin ±0.05") {
			t.Errorf("expected equivalence margin reference line label, got:\n%s", body)
		}
		if !strings.Contains(body, "n=12") {
			t.Errorf("expected final point label containing n=12, got:\n%s", body)
		}

		// Assert table rows and monotone n
		tableRowRegex := regexp.MustCompile(`<td class="num">(\d+)</td>\s*<td class="num">([^<]+)</td>\s*<td class="num">([^<]+)</td>\s*<td><span class="chip [^"]+"><span class="dot"></span>([^<]+)</span></td>`)
		rowMatches := tableRowRegex.FindAllStringSubmatch(body, -1)
		if len(rowMatches) != 3 {
			t.Fatalf("expected 3 history table rows, got %d", len(rowMatches))
		}

		expectedNs := []int{4, 8, 12}
		lastN := 0
		for i, rm := range rowMatches {
			nVal, err := strconv.Atoi(rm[1])
			if err != nil {
				t.Fatalf("row %d: invalid n integer %q", i, rm[1])
			}
			if nVal != expectedNs[i] {
				t.Errorf("row %d: expected n=%d, got %d", i, expectedNs[i], nVal)
			}
			if nVal <= lastN {
				t.Errorf("expected strictly monotone increasing n, got row %d n=%d <= lastN=%d", i, nVal, lastN)
			}
			lastN = nVal
			deltaStr := rm[2]
			ciStr := rm[3]
			decisionStr := rm[4]
			if deltaStr == "" || ciStr == "" || decisionStr == "" {
				t.Errorf("row %d: missing values delta=%q ci=%q decision=%q", i, deltaStr, ciStr, decisionStr)
			}
		}
	})

	t.Run("Negative_MissingArtifact", func(t *testing.T) {
		compMissing := &v1alpha1.Comparison{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "comp-missing",
				Namespace: "eval-runner",
			},
			Status: v1alpha1.ComparisonStatus{
				Phase:       v1alpha1.ComparisonPhaseCompleted,
				SummaryPath: filepath.Join(baseDir, "comparisons", "missing.json"),
			},
		}
		clMissing := fake.NewClientBuilder().WithScheme(scheme).WithObjects(compMissing).Build()
		srvMissing := NewServer(Config{Client: clMissing, ArtifactSink: sink})

		req := httptest.NewRequest(http.MethodGet, "/comparisons/eval-runner/comp-missing", nil)
		w := httptest.NewRecorder()
		srvMissing.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for missing artifact, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "history not available") {
			t.Errorf("expected 'history not available' note, got:\n%s", body)
		}
		if strings.Contains(body, "history-table") {
			t.Errorf("expected no history table when artifact is missing")
		}
	})

	t.Run("Negative_OversizedArtifact", func(t *testing.T) {
		// Create artifact file that exceeds DefaultMaxLoadBytes (4 MiB)
		oversizedFile := filepath.Join(baseDir, "comparisons", "comp-oversized.json")
		f, err := os.Create(oversizedFile)
		if err != nil {
			t.Fatalf("failed to create oversized file: %v", err)
		}
		oversizedData := make([]byte, artifacts.DefaultMaxLoadBytes+1024)
		_, _ = f.Write(oversizedData)
		_ = f.Close()

		compOversized := &v1alpha1.Comparison{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "comp-oversized",
				Namespace: "eval-runner",
			},
			Status: v1alpha1.ComparisonStatus{
				Phase:       v1alpha1.ComparisonPhaseCompleted,
				SummaryPath: oversizedFile,
			},
		}
		clOversized := fake.NewClientBuilder().WithScheme(scheme).WithObjects(compOversized).Build()
		srvOversized := NewServer(Config{Client: clOversized, ArtifactSink: sink})

		req := httptest.NewRequest(http.MethodGet, "/comparisons/eval-runner/comp-oversized", nil)
		w := httptest.NewRecorder()
		srvOversized.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for oversized artifact, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "history not available") {
			t.Errorf("expected 'history not available' note for oversized artifact, got:\n%s", body)
		}
		if strings.Contains(body, "history-table") {
			t.Errorf("expected no history table when artifact is oversized")
		}
	})

	t.Run("Negative_PairsLessThan2", func(t *testing.T) {
		singlePairArtifact := artifacts.ComparisonArtifact{
			Comparison: "comp-single-pair",
			Namespace:  "eval-runner",
			Verdict:    "INCONCLUSIVE",
			Tasks: []artifacts.ComparisonTaskArtifact{
				{
					ID:    taskID,
					Pairs: 1,
					PairsTable: []stats.PairedSampleScore{
						{ID: "sample-1", BaseScore: 0.5, CandidateScore: 0.8, Delta: 0.3},
					},
				},
			},
		}
		artBytes, err := json.Marshal(singlePairArtifact)
		if err != nil {
			t.Fatalf("failed to marshal single pair artifact: %v", err)
		}
		p, err := sink.StoreComparison(t.Context(), "comp-single-pair", artBytes)
		if err != nil {
			t.Fatalf("failed to store single pair comparison: %v", err)
		}

		compSingle := &v1alpha1.Comparison{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "comp-single-pair",
				Namespace: "eval-runner",
			},
			Status: v1alpha1.ComparisonStatus{
				Phase:       v1alpha1.ComparisonPhaseCompleted,
				Verdict:     v1alpha1.ComparisonVerdictInconclusive,
				SummaryPath: p,
				Tasks: []v1alpha1.ComparisonTaskStatus{
					{
						ID:         taskID,
						SamplesRun: 1,
						Decision:   v1alpha1.ComparisonTaskLabelInconclusive,
					},
				},
			},
		}
		clSingle := fake.NewClientBuilder().WithScheme(scheme).WithObjects(compSingle).Build()
		srvSingle := NewServer(Config{Client: clSingle, ArtifactSink: sink})

		req := httptest.NewRequest(http.MethodGet, "/comparisons/eval-runner/comp-single-pair", nil)
		w := httptest.NewRecorder()
		srvSingle.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for pairs < 2, got %d", w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, "history-table") {
			t.Errorf("expected no history table for pairs < 2, got body:\n%s", body)
		}
	})

	t.Run("Negative_ArtifactStorageUnavailable", func(t *testing.T) {
		srvNilSink := NewServer(Config{Client: client, ArtifactSink: nil})

		req := httptest.NewRequest(http.MethodGet, "/comparisons/eval-runner/comp-history-fixture", nil)
		w := httptest.NewRecorder()
		srvNilSink.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK when storage unavailable, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "artifact storage unavailable") {
			t.Errorf("expected 'artifact storage unavailable' note, got:\n%s", body)
		}
		if strings.Contains(body, "history-table") {
			t.Errorf("expected no history table when artifact storage is unavailable")
		}
	})

	t.Run("Negative_EmptySummaryPath_QuietInProgress", func(t *testing.T) {
		compInProgress := &v1alpha1.Comparison{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "comp-in-progress",
				Namespace: "eval-runner",
			},
			Status: v1alpha1.ComparisonStatus{
				Phase:       v1alpha1.ComparisonPhaseRunning,
				SummaryPath: "",
			},
		}
		clInProgress := fake.NewClientBuilder().WithScheme(scheme).WithObjects(compInProgress).Build()
		srvInProgress := NewServer(Config{Client: clInProgress, ArtifactSink: sink})

		req := httptest.NewRequest(http.MethodGet, "/comparisons/eval-runner/comp-in-progress", nil)
		w := httptest.NewRecorder()
		srvInProgress.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for in-progress comparison, got %d", w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, "Confidence Interval History") {
			t.Errorf("expected no Confidence Interval History section for comparison without summaryPath, got:\n%s", body)
		}
	})

	t.Run("Negative_SummaryPathMissingComparisonsPrefix", func(t *testing.T) {
		compBadPrefix := &v1alpha1.Comparison{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "comp-bad-prefix",
				Namespace: "eval-runner",
			},
			Status: v1alpha1.ComparisonStatus{
				Phase:       v1alpha1.ComparisonPhaseCompleted,
				SummaryPath: "gs://eval-bucket/runs/abcdef12/manifest.json",
			},
		}
		clBadPrefix := fake.NewClientBuilder().WithScheme(scheme).WithObjects(compBadPrefix).Build()
		srvBadPrefix := NewServer(Config{Client: clBadPrefix, ArtifactSink: sink})

		req := httptest.NewRequest(http.MethodGet, "/comparisons/eval-runner/comp-bad-prefix", nil)
		w := httptest.NewRecorder()
		srvBadPrefix.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for bad prefix summaryPath, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "history not available") {
			t.Errorf("expected 'history not available' note when summaryPath lacks comparisons/ prefix, got:\n%s", body)
		}
		if strings.Contains(body, "history-table") {
			t.Errorf("expected no history table when summaryPath lacks comparisons/ prefix")
		}
	})
}

func TestServerSeries_Selection(t *testing.T) {
	scheme := newTestScheme(t)
	r10 := 0.8
	ratePtr := &r10

	runValid := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-1111111",
			Namespace: "eval-runner",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/repo":     "gke-labs_eval-runner",
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "1111111222222233333334444444555555556666",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": "2026-10-20T01:00:00Z",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "1111111222222233333334444444555555556666",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-a",
					Phase:       v1alpha1.TaskPhaseCompleted,
					SuccessRate: ratePtr,
					Samples:     10,
				},
			},
		},
	}

	// Excluded: branch is dev, not main
	runOtherBranch := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-2222222",
			Namespace: "eval-runner",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/repo":     "gke-labs_eval-runner",
				"evalrunner.gke-labs.dev/branch":   "dev",
				"evalrunner.gke-labs.dev/head-sha": "2222222333333344444445555555666666667777",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "2222222333333344444445555555666666667777",
			},
		},
	}

	// Excluded: pre-existing run lacking branch label
	runPreExisting := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-3333333",
			Namespace: "eval-runner",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/repo":     "gke-labs_eval-runner",
				"evalrunner.gke-labs.dev/head-sha": "3333333444444455555556666666777777778888",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "3333333444444455555556666666777777778888",
			},
		},
	}

	// Excluded: not starting with base-
	runCandidate := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-42-4444444",
			Namespace: "eval-runner",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/repo":     "gke-labs_eval-runner",
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "4444444555555566666667777777888888889999",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "4444444555555566666667777777888888889999",
			},
		},
	}

	// Excluded: other repo
	runOtherRepo := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-5555555",
			Namespace: "eval-runner",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/repo":     "other-org_other-repo",
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/head-sha": "5555555666666677777778888888999999990000",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/other-org/other-repo.git",
				Commit: "5555555666666677777778888888999999990000",
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		runValid, runOtherBranch, runPreExisting, runCandidate, runOtherRepo,
	).Build()
	server := NewServer(Config{Client: cl})

	t.Run("SeriesList", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/series", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "gke-labs/eval-runner") {
			t.Errorf("expected gke-labs/eval-runner in series list")
		}
		if !strings.Contains(body, "/series/2cc24c2/main") {
			t.Errorf("expected link to /series/2cc24c2/main in series list")
		}
	})

	t.Run("SeriesDetail_FiltersCorrectly", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/series/2cc24c2/main", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "base-1111111") {
			t.Errorf("expected base-1111111 in series detail")
		}
		if strings.Contains(body, "base-2222222") {
			t.Errorf("did not expect base-2222222 (branch dev) in series detail")
		}
		if strings.Contains(body, "base-3333333") {
			t.Errorf("did not expect base-3333333 (pre-existing) in series detail")
		}
		if strings.Contains(body, "pr-42-4444444") {
			t.Errorf("did not expect candidate run in series detail")
		}
		if strings.Contains(body, "base-5555555") {
			t.Errorf("did not expect other repo in series detail")
		}
	})

	t.Run("SeriesDetail_NotFound", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/series/2cc24c2/nonexistent", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 for nonexistent series, got %d", w.Code)
		}
	})
}

func TestServerSeries_Ordering(t *testing.T) {
	scheme := newTestScheme(t)
	rate := 0.9

	t.Run("OrderedByCommitTimeAnnotation", func(t *testing.T) {
		// runA was created at 10:00 but commitTime is 02:00
		runA := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "base-aaaaaaa",
				Namespace:         "eval-runner",
				CreationTimestamp: metav1.NewTime(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)),
				Labels: map[string]string{
					"evalrunner.gke-labs.dev/repo":   "gke-labs_eval-runner",
					"evalrunner.gke-labs.dev/branch": "main",
				},
				Annotations: map[string]string{
					"evalrunner.gke-labs.dev/commit-time": "2026-10-20T02:00:00Z",
				},
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					URL:    "https://github.com/gke-labs/eval-runner.git",
					Commit: "aaaaaaa111111111111111111111111111111111",
				},
			},
			Status: v1alpha1.RunStatus{
				Tasks: []v1alpha1.TaskStatus{{ID: "task-1", Phase: v1alpha1.TaskPhaseCompleted, SuccessRate: &rate}},
			},
		}

		// runB was created at 11:00 but commitTime is 01:00 (earlier commit!)
		runB := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "base-bbbbbbb",
				Namespace:         "eval-runner",
				CreationTimestamp: metav1.NewTime(time.Date(2026, 10, 20, 11, 0, 0, 0, time.UTC)),
				Labels: map[string]string{
					"evalrunner.gke-labs.dev/repo":   "gke-labs_eval-runner",
					"evalrunner.gke-labs.dev/branch": "main",
				},
				Annotations: map[string]string{
					"evalrunner.gke-labs.dev/commit-time": "2026-10-20T01:00:00Z",
				},
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					URL:    "https://github.com/gke-labs/eval-runner.git",
					Commit: "bbbbbbb222222222222222222222222222222222",
				},
			},
			Status: v1alpha1.RunStatus{
				Tasks: []v1alpha1.TaskStatus{{ID: "task-1", Phase: v1alpha1.TaskPhaseCompleted, SuccessRate: &rate}},
			},
		}

		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runA, runB).Build()
		server := NewServer(Config{Client: cl})

		req := httptest.NewRequest(http.MethodGet, "/series/2cc24c2/main", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, "ordered by creation time") {
			t.Errorf("did not expect 'ordered by creation time' note when commit-time annotations are present")
		}

		idxA := strings.Index(body, "base-aaaaaaa")
		idxB := strings.Index(body, "base-bbbbbbb")
		if idxB == -1 || idxA == -1 {
			t.Fatalf("expected both runs in body")
		}
		if idxB > idxA {
			t.Errorf("expected runB (commitTime 01:00) before runA (commitTime 02:00), got idxB=%d idxA=%d", idxB, idxA)
		}
	})

	t.Run("FallbackToCreationTimeWithVisibleNote", func(t *testing.T) {
		runNoAnnotation := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "base-cccccc",
				Namespace:         "eval-runner",
				CreationTimestamp: metav1.NewTime(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)),
				Labels: map[string]string{
					"evalrunner.gke-labs.dev/repo":   "gke-labs_eval-runner",
					"evalrunner.gke-labs.dev/branch": "main",
				},
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					URL:    "https://github.com/gke-labs/eval-runner.git",
					Commit: "cccccc111111111111111111111111111111111",
				},
			},
			Status: v1alpha1.RunStatus{
				Tasks: []v1alpha1.TaskStatus{{ID: "task-1", Phase: v1alpha1.TaskPhaseCompleted, SuccessRate: &rate}},
			},
		}

		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runNoAnnotation).Build()
		server := NewServer(Config{Client: cl})

		req := httptest.NewRequest(http.MethodGet, "/series/2cc24c2/main", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "ordered by creation time") {
			t.Errorf("expected visible note 'ordered by creation time', got body:\n%s", body)
		}
	})
}

func TestServerSeries_NCap(t *testing.T) {
	scheme := newTestScheme(t)
	rate := 0.85

	var runs []client.Object
	baseTime := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 60; i++ {
		cTime := baseTime.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		run := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("base-%07d", i),
				Namespace: "eval-runner",
				Labels: map[string]string{
					"evalrunner.gke-labs.dev/repo":   "gke-labs_eval-runner",
					"evalrunner.gke-labs.dev/branch": "main",
				},
				Annotations: map[string]string{
					"evalrunner.gke-labs.dev/commit-time": cTime,
				},
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					URL:    "https://github.com/gke-labs/eval-runner.git",
					Commit: fmt.Sprintf("%040d", i),
				},
			},
			Status: v1alpha1.RunStatus{
				Tasks: []v1alpha1.TaskStatus{{ID: "eval-task", Phase: v1alpha1.TaskPhaseCompleted, SuccessRate: &rate}},
			},
		}
		runs = append(runs, run)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runs...).Build()
	server := NewServer(Config{Client: cl})

	t.Run("DefaultCap50", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/series/2cc24c2/main", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, "base-0000001") {
			t.Errorf("did not expect oldest commit base-0000001 when capped at 50")
		}
		if !strings.Contains(body, "base-0000060") {
			t.Errorf("expected newest commit base-0000060")
		}
		if !strings.Contains(body, "base-0000011") {
			t.Errorf("expected commit base-0000011")
		}
		if !strings.Contains(body, "showing last 50 of 60 commits") {
			t.Errorf("expected 'showing last 50 of 60 commits' in body")
		}
	})

	t.Run("NCapOver50_ClampedTo50", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/series/2cc24c2/main?n=100", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		body := w.Body.String()
		if strings.Contains(body, "base-0000001") {
			t.Errorf("expected n=100 to be capped at 50, but base-0000001 was included")
		}
	})

	t.Run("ExplicitSmallerN", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/series/2cc24c2/main?n=10", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		body := w.Body.String()
		if strings.Contains(body, "base-0000050") {
			t.Errorf("did not expect base-0000050 when n=10 (commits 51-60)")
		}
		if !strings.Contains(body, "base-0000051") {
			t.Errorf("expected base-0000051 when n=10")
		}
		if !strings.Contains(body, "base-0000060") {
			t.Errorf("expected base-0000060 when n=10")
		}
	})
}

func TestServerSeries_CohortFilter(t *testing.T) {
	scheme := newTestScheme(t)
	rate := 0.7

	runA := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-cohort-a",
			Namespace: "eval-runner",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/repo":   "gke-labs_eval-runner",
				"evalrunner.gke-labs.dev/branch": "main",
			},
		},
		Spec: v1alpha1.RunSpec{
			Cohort: "cohort-alpha",
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "aaaa111111111111111111111111111111111111",
			},
		},
		Status: v1alpha1.RunStatus{
			Tasks: []v1alpha1.TaskStatus{{ID: "task-1", Phase: v1alpha1.TaskPhaseCompleted, SuccessRate: &rate}},
		},
	}

	runB := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-cohort-b",
			Namespace: "eval-runner",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/repo":   "gke-labs_eval-runner",
				"evalrunner.gke-labs.dev/branch": "main",
			},
		},
		Spec: v1alpha1.RunSpec{
			Cohort: "cohort-beta",
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "bbbb222222222222222222222222222222222222",
			},
		},
		Status: v1alpha1.RunStatus{
			Tasks: []v1alpha1.TaskStatus{{ID: "task-1", Phase: v1alpha1.TaskPhaseCompleted, SuccessRate: &rate}},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runA, runB).Build()
	server := NewServer(Config{Client: cl})

	t.Run("ListFilteredByCohort", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/series?cohort=cohort-alpha", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "1 total") {
			t.Errorf("expected 1 total row for cohort-alpha filter, got body:\n%s", body)
		}
	})

	t.Run("DetailFilteredByCohort", func(t *testing.T) {
		reqAlpha := httptest.NewRequest(http.MethodGet, "/series/2cc24c2/main?cohort=cohort-alpha", nil)
		wAlpha := httptest.NewRecorder()
		server.ServeHTTP(wAlpha, reqAlpha)

		if wAlpha.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", wAlpha.Code)
		}
		bodyAlpha := wAlpha.Body.String()
		if !strings.Contains(bodyAlpha, "base-cohort-a") {
			t.Errorf("expected base-cohort-a in filtered series")
		}
		if strings.Contains(bodyAlpha, "base-cohort-b") {
			t.Errorf("did not expect base-cohort-b in filtered series")
		}
	})
}

func TestServerSeries_HostileEscaping(t *testing.T) {
	scheme := newTestScheme(t)
	rate := 0.5

	hostilePayload := "<script>alert('xss')</script>"
	runHostile := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-hostile",
			Namespace: "eval-runner",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/repo":   "gke-labs_eval-runner",
				"evalrunner.gke-labs.dev/branch": "main",
			},
		},
		Spec: v1alpha1.RunSpec{
			Cohort: hostilePayload,
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "1111111222222233333334444444555555556666",
			},
		},
		Status: v1alpha1.RunStatus{
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          hostilePayload,
					Phase:       v1alpha1.TaskPhaseCompleted,
					SuccessRate: &rate,
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runHostile).Build()
	server := NewServer(Config{Client: cl})

	t.Run("ListEscaping", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/series?cohort="+url.QueryEscape(hostilePayload), nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		body := w.Body.String()
		if strings.Contains(body, "<script>") {
			t.Errorf("found unescaped <script> in list response")
		}
	})

	t.Run("DetailEscaping", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/series/2cc24c2/main", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		body := w.Body.String()
		if strings.Contains(body, "<script>") {
			t.Errorf("found unescaped <script> in detail response")
		}
		if !strings.Contains(body, html.EscapeString(hostilePayload)) {
			t.Errorf("expected escaped hostile payload in detail response")
		}
	})
}
