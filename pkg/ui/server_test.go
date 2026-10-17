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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	expectedHeaders := []string{"eval", "base", "this PR", "Δ (95% CI)", "label", "runs"}
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
	if !strings.Contains(body, "+0.23") {
		t.Errorf("expected delta +0.23")
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
