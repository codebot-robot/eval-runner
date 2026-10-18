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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
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
	if !strings.Contains(body, "gs://eval-bucket/runs/05ce4d8/triage/runbook_lookup/inspect.json") {
		t.Errorf("expected log path as text in table")
	}

	// Test 404 for missing run
	reqNotFound := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/non-existent-run", nil)
	wNotFound := httptest.NewRecorder()
	server.ServeHTTP(wNotFound, reqNotFound)
	if wNotFound.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing run, got %d", wNotFound.Code)
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
	}

	for _, route := range invalidRoutes {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("route %q: expected 400 Bad Request, got %d", route, w.Code)
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
	}

	for _, route := range forbiddenRequests {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("route %q: expected 403 Forbidden, got %d", route, w.Code)
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
