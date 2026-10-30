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
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/stats"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSuiteOf(t *testing.T) {
	tests := []struct {
		taskID string
		want   string
	}{
		{"docs/readme_comprehension", "docs"},
		{"docs/a", "docs"},
		{"docs/b", "docs"},
		{"other/c", "other"},
		{"solo", "solo"},
		{"nested/path/task", "nested"},
		{"", ""},
	}
	for _, tc := range tests {
		got := SuiteOf(tc.taskID)
		if got != tc.want {
			t.Errorf("SuiteOf(%q) = %q, want %q", tc.taskID, got, tc.want)
		}
	}
}

func TestRepoOverview_SuiteGrouping(t *testing.T) {
	// Unit test: suite grouping (docs/a, docs/b, other/c, solo) → three suites
	scheme := newTestScheme(t)
	now := time.Now()
	rate := 0.80

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-grouping-run",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "abcdef1",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "docs/a", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, SuccessRate: &rate},
				{ID: "docs/b", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, SuccessRate: &rate},
				{ID: "other/c", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, SuccessRate: &rate},
				{ID: "solo", Phase: v1alpha1.TaskPhaseCompleted, Samples: 10, SuccessRate: &rate},
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

	// Verify exactly three suites render: docs, other, solo
	for _, suite := range []string{
		`<a href="/github.com/gke-labs/eval-runner/evals/docs" class="mono" style="font-weight:600">docs</a>`,
		`<a href="/github.com/gke-labs/eval-runner/evals/other" class="mono" style="font-weight:600">other</a>`,
		`<a href="/github.com/gke-labs/eval-runner/evals/solo" class="mono" style="font-weight:600">solo</a>`,
	} {
		if !strings.Contains(body, suite) {
			t.Errorf("expected body to contain suite %q", suite)
		}
	}

	// Verify all four evals render
	for _, eval := range []string{"docs/a", "docs/b", "other/c", "solo"} {
		if !strings.Contains(body, eval) {
			t.Errorf("expected body to contain eval %q", eval)
		}
	}

	// Verify count of suite rows is 3
	suiteRowCount := strings.Count(body, `class="suite-row"`)
	if suiteRowCount != 3 {
		t.Errorf("expected 3 suite rows, got %d", suiteRowCount)
	}

	// Verify count of eval rows is 4
	evalRowCount := strings.Count(body, `class="eval-row"`)
	if evalRowCount != 4 {
		t.Errorf("expected 4 eval rows, got %d", evalRowCount)
	}
}

func TestRepoOverview_StatisticsFixture(t *testing.T) {
	// Unit test: overall/suite/eval numbers pinned on a small fixture
	// docs/a: 10 samples, 8 successes -> 0.80
	// docs/b: 10 samples, 6 successes -> 0.60
	// solo: 20 samples, 18 successes -> 0.90
	scheme := newTestScheme(t)
	now := time.Now()

	rateA := 0.80
	rateB := 0.60
	rateSolo := 0.90

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-pinned-run",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": now.UTC().Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "c0ffee1",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "docs/a",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     10,
					Successes:   8,
					SuccessRate: &rateA,
				},
				{
					ID:          "docs/b",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     10,
					Successes:   6,
					SuccessRate: &rateB,
				},
				{
					ID:          "solo",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     20,
					Successes:   18,
					SuccessRate: &rateSolo,
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

	// Pinned calculations:
	// Eval docs/a: 8/10 -> Point: 0.80
	estA := stats.PooledWilsonFromTotals(8, 10, 0.95)
	expectedAStr := fmt.Sprintf("%.2f", estA.Point)
	expectedACIStr := fmt.Sprintf("[%.2f, %.2f]", estA.CI.Lower, estA.CI.Upper)

	// Eval docs/b: 6/10 -> Point: 0.60
	estB := stats.PooledWilsonFromTotals(6, 10, 0.95)
	expectedBStr := fmt.Sprintf("%.2f", estB.Point)
	expectedBCIStr := fmt.Sprintf("[%.2f, %.2f]", estB.CI.Lower, estB.CI.Upper)

	// Suite docs: mean(0.80, 0.60) = 0.70; pooled Wilson 14/20
	estSuiteDocs := stats.PooledWilsonFromTotals(14, 20, 0.95)
	expectedSuiteDocsStr := "0.70"
	expectedSuiteDocsCIStr := fmt.Sprintf("[%.2f, %.2f]", estSuiteDocs.CI.Lower, estSuiteDocs.CI.Upper)

	// Eval solo: 18/20 -> Point: 0.90
	estSolo := stats.PooledWilsonFromTotals(18, 20, 0.95)
	expectedSoloStr := fmt.Sprintf("%.2f", estSolo.Point)
	expectedSoloCIStr := fmt.Sprintf("[%.2f, %.2f]", estSolo.CI.Lower, estSolo.CI.Upper)

	// Suite solo: mean(0.90) = 0.90; pooled Wilson 18/20
	expectedSuiteSoloStr := "0.90"
	expectedSuiteSoloCIStr := expectedSoloCIStr

	// Overall score: mean(0.70, 0.90) = 0.80; pooled Wilson 32/40
	estOverall := stats.PooledWilsonFromTotals(32, 40, 0.95)
	expectedOverallStr := "0.80"
	expectedOverallCIStr := fmt.Sprintf("[%.2f, %.2f]", estOverall.CI.Lower, estOverall.CI.Upper)

	// Assert headline overall numbers
	headlineScoreSub := fmt.Sprintf(`<span class="headline-score">%s</span>`, expectedOverallStr)
	headlineCISub := fmt.Sprintf(`<span class="headline-ci">%s</span>`, expectedOverallCIStr)
	if !strings.Contains(body, headlineScoreSub) {
		t.Errorf("expected headline overall score %q in body", headlineScoreSub)
	}
	if !strings.Contains(body, headlineCISub) {
		t.Errorf("expected headline overall CI %q in body", headlineCISub)
	}

	// Assert suite docs numbers
	suiteDocsScoreSub := fmt.Sprintf(`<span class="mono" style="font-weight:600">%s</span>`, expectedSuiteDocsStr)
	suiteDocsCISub := fmt.Sprintf(`<span style="font-size:12px;color:var(--muted);margin-left:4px">%s</span>`, expectedSuiteDocsCIStr)
	if !strings.Contains(body, suiteDocsScoreSub) {
		t.Errorf("expected suite docs score %q in body", suiteDocsScoreSub)
	}
	if !strings.Contains(body, suiteDocsCISub) {
		t.Errorf("expected suite docs CI %q in body", suiteDocsCISub)
	}

	// Assert suite solo numbers
	suiteSoloScoreSub := fmt.Sprintf(`<span class="mono" style="font-weight:600">%s</span>`, expectedSuiteSoloStr)
	suiteSoloCISub := fmt.Sprintf(`<span style="font-size:12px;color:var(--muted);margin-left:4px">%s</span>`, expectedSuiteSoloCIStr)
	if !strings.Contains(body, suiteSoloScoreSub) {
		t.Errorf("expected suite solo score %q in body", suiteSoloScoreSub)
	}
	if !strings.Contains(body, suiteSoloCISub) {
		t.Errorf("expected suite solo CI %q in body", suiteSoloCISub)
	}

	// Assert eval docs/a and docs/b numbers
	if !strings.Contains(body, expectedAStr) || !strings.Contains(body, expectedACIStr) {
		t.Errorf("expected eval docs/a score %s and CI %s in body", expectedAStr, expectedACIStr)
	}
	if !strings.Contains(body, expectedBStr) || !strings.Contains(body, expectedBCIStr) {
		t.Errorf("expected eval docs/b score %s and CI %s in body", expectedBStr, expectedBCIStr)
	}

	// Assert solo eval numbers
	if !strings.Contains(body, expectedSoloStr) || !strings.Contains(body, expectedSoloCIStr) {
		t.Errorf("expected solo score %s and CI %s in body", expectedSoloStr, expectedSoloCIStr)
	}
}

func TestRepoOverview_DistractorsExcluded(t *testing.T) {
	// Unit test: distractor Runs (other repository, other branch, candidate Runs) excluded
	scheme := newTestScheme(t)
	now := time.Now()
	rate := 0.85

	// Valid main-branch baseline run for target repository
	validRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-target-main",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "valid01",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "docs/valid_eval",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     100,
					Successes:   85,
					SuccessRate: &rate,
				},
			},
		},
	}

	// Distractor 1: other repository
	otherRepoRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-other-repo",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/foreign-org/foreign-repo.git",
				Commit: "other01",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "distractor/other_repo_eval",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     10,
					SuccessRate: &rate,
				},
			},
		},
	}

	// Distractor 2: other branch
	otherBranchRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-feature-branch",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "feature-xyz",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "dev01",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "distractor/dev_branch_eval",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     10,
					SuccessRate: &rate,
				},
			},
		},
	}

	// Distractor 3: candidate run (pr- prefix, not a base- run)
	candidateRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pr-42-cand",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "cand01",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "distractor/candidate_eval",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     10,
					SuccessRate: &rate,
				},
			},
		},
	}

	rateRunning := 0.10
	// Distractor 4: Running baseline run on a newer commit
	runningRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-running-main",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(1 * time.Hour)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": now.Add(1 * time.Hour).UTC().Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "running99",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseRunning,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "docs/valid_eval",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     100,
					Successes:   10,
					SuccessRate: &rateRunning,
				},
				{
					ID:    "docs/in_flight",
					Phase: v1alpha1.TaskPhaseRunning,
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(validRun, otherRepoRun, otherBranchRun, candidateRun, runningRun).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()

	// Target eval must be present with score from completed run (0.85), not running run (0.10)
	if !strings.Contains(body, "docs/valid_eval") {
		t.Errorf("expected docs/valid_eval in body")
	}
	if strings.Contains(body, ">0.10<") {
		t.Errorf("running run partial score should not be used")
	}
	if !strings.Contains(body, ">0.85<") {
		t.Errorf("expected score 0.85 for docs/valid_eval")
	}

	// Headline commit must be valid01, not running99
	if strings.Contains(body, "running99") {
		t.Errorf("running run commit should not appear in headline")
	}
	if !strings.Contains(body, "valid01") {
		t.Errorf("expected valid01 in headline")
	}

	// Distractor tasks must be excluded
	if strings.Contains(body, "distractor/other_repo_eval") {
		t.Errorf("distractor from other repository should not be rendered")
	}
	if strings.Contains(body, "distractor/dev_branch_eval") {
		t.Errorf("distractor from other branch should not be rendered")
	}
	if strings.Contains(body, "distractor/candidate_eval") {
		t.Errorf("distractor candidate run should not be rendered in suites table")
	}
	if strings.Contains(body, "docs/in_flight") {
		t.Errorf("in-flight task should not be rendered")
	}

	// Total Runs tile should count runs for gke-labs/eval-runner (validRun, otherBranchRun, candidateRun, runningRun = 4 runs)
	// and exclude otherRepoRun
	if !strings.Contains(body, `<div class="value num"><a href="/github.com/gke-labs/eval-runner/runs">4</a></div>`) {
		t.Errorf("expected total runs count 4 for this repo in tiles")
	}
}

func TestRepoOverview_DeltaSignAndInterval(t *testing.T) {
	// Unit test: delta sign and interval for improving and regressing evals and suites
	scheme := newTestScheme(t)
	now := time.Now()

	rateA1 := 0.50
	rateB1 := 0.80
	rateA2 := 0.80
	rateB2 := 0.40

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
				Commit: "1111111",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "suite-test/a", Phase: v1alpha1.TaskPhaseCompleted, Samples: 20, Successes: 10, SuccessRate: &rateA1},
				{ID: "suite-test/b", Phase: v1alpha1.TaskPhaseCompleted, Samples: 20, Successes: 16, SuccessRate: &rateB1},
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
				Commit: "2222222",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "suite-test/a", Phase: v1alpha1.TaskPhaseCompleted, Samples: 20, Successes: 16, SuccessRate: &rateA2},
				{ID: "suite-test/b", Phase: v1alpha1.TaskPhaseCompleted, Samples: 20, Successes: 8, SuccessRate: &rateB2},
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

	// Check eval a: delta = 0.80 - 0.50 = +0.30, chip good
	estA2 := stats.PooledWilsonFromTotals(16, 20, 0.95)
	estA1 := stats.PooledWilsonFromTotals(10, 20, 0.95)
	deltaA := stats.DifferenceUnpaired(estA2, estA1, 0.95)
	formattedDeltaA := FormatDeltaCI(deltaA.Delta, deltaA.CI.Lower, deltaA.CI.Upper)
	if deltaA.Delta <= 0 {
		t.Errorf("expected positive delta for eval a, got %v", deltaA.Delta)
	}
	if !strings.Contains(body, "chip good") {
		t.Errorf("expected chip good for eval a in body")
	}
	// In HTML, '+' can be &#43;
	htmlDeltaA := strings.ReplaceAll(formattedDeltaA, "+", "&#43;")
	if !strings.Contains(body, htmlDeltaA) && !strings.Contains(body, formattedDeltaA) {
		t.Errorf("expected delta %q or %q in body", formattedDeltaA, htmlDeltaA)
	}

	// Check eval b: delta = 0.40 - 0.80 = -0.40, chip bad
	estB2 := stats.PooledWilsonFromTotals(8, 20, 0.95)
	estB1 := stats.PooledWilsonFromTotals(16, 20, 0.95)
	deltaB := stats.DifferenceUnpaired(estB2, estB1, 0.95)
	formattedDeltaB := FormatDeltaCI(deltaB.Delta, deltaB.CI.Lower, deltaB.CI.Upper)
	if deltaB.Delta >= 0 {
		t.Errorf("expected negative delta for eval b, got %v", deltaB.Delta)
	}
	if !strings.Contains(body, "chip bad") {
		t.Errorf("expected chip bad for eval b in body")
	}
	if !strings.Contains(body, formattedDeltaB) {
		t.Errorf("expected delta %q in body", formattedDeltaB)
	}
}

func TestRepoOverview_Sparkline_Ticks(t *testing.T) {
	// Unit test: sparkline with n=1 and n=0; tick marks count equals observation count.
	t.Run("n=0", func(t *testing.T) {
		spark := BuildSparklineSVG(nil)
		if spark.HasPoints {
			t.Errorf("expected HasPoints=false for n=0")
		}
		if len(spark.Ticks) != 0 {
			t.Errorf("expected 0 ticks for n=0, got %d", len(spark.Ticks))
		}
	})

	t.Run("n=1", func(t *testing.T) {
		rate := 0.75
		items := []SeriesItem{
			{
				CommitSHA:   "1111111",
				HasPoint:    true,
				SuccessRate: &rate,
				ObsCount:    1,
			},
		}
		spark := BuildSparklineSVG(items)
		if !spark.HasPoints {
			t.Fatalf("expected HasPoints=true for n=1")
		}
		if len(spark.Ticks) != 1 {
			t.Fatalf("expected 1 tick for n=1, got %d", len(spark.Ticks))
		}
		tick := spark.Ticks[0]
		if tick.X1 != 60.0 || tick.X2 != 60.0 {
			t.Errorf("expected centered tick at X=60.0, got X1=%v, X2=%v", tick.X1, tick.X2)
		}
		// Confirm no NaN
		if tick.Y1 != tick.Y1 || tick.Y2 != tick.Y2 {
			t.Errorf("tick coordinates contain NaN: Y1=%v, Y2=%v", tick.Y1, tick.Y2)
		}
	})

	t.Run("ObservationCountEqualsTickCount", func(t *testing.T) {
		// 3 commits with multiple observations: 2 at commit 1, 1 at commit 2, 3 at commit 3 = 6 observations total
		rate1 := 0.50
		rate2 := 0.60
		rate3 := 0.70
		items := []SeriesItem{
			{CommitSHA: "c1", HasPoint: true, SuccessRate: &rate1, ObsCount: 2},
			{CommitSHA: "c2", HasPoint: true, SuccessRate: &rate2, ObsCount: 1},
			{CommitSHA: "c3", HasPoint: true, SuccessRate: &rate3, ObsCount: 3},
		}
		spark := BuildSparklineSVG(items)
		expectedTicks := 2 + 1 + 3
		if len(spark.Ticks) != expectedTicks {
			t.Errorf("expected %d ticks, got %d", expectedTicks, len(spark.Ticks))
		}
	})
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
			"No observations recorded on <code class=\"mono\">main</code>",
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
		expected := "No observations recorded on <code class=\"mono\">feature-x</code>."
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
					ID:          "suite/task-nightly",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     10,
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
					ID:          "suite/task-pr",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     10,
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
	if !strings.Contains(body, "suite/task-nightly") {
		t.Errorf("expected suite/task-nightly in body")
	}
	if strings.Contains(body, "suite/task-pr") {
		t.Errorf("did not expect suite/task-pr in body for cohort=nightly")
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
					ID:          "custom/task",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     10,
					SuccessRate: &rate,
					LogPath:     "gs://bucket/runs/aaaa111/custom/task/transcript.json",
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	sink := artifacts.NewInMemorySink()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner", ArtifactSink: sink})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner?namespace=custom-ns", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	for _, expected := range []string{
		"namespace: custom-ns",
		"/github.com/gke-labs/eval-runner/runs/base-custom-ns?namespace=custom-ns",
		"/github.com/gke-labs/eval-runner/runs/base-custom-ns/tasks/custom%2Ftask/samples?namespace=custom-ns",
		"/github.com/gke-labs/eval-runner/pulls?namespace=custom-ns",
		"/github.com/gke-labs/eval-runner/comparisons?namespace=custom-ns",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("expected body to contain %q, body:\n%s", expected, body)
		}
	}
}

func TestRepoOverview_HeadTopUpObsCount2(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add v1alpha1 scheme: %v", err)
	}

	run1 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-c0ffee1",
			Namespace: "eval-runner",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/target-repo": "github.com_gke-labs_eval-runner",
				"evalrunner.gke-labs.dev/repo":        "github.com_gke-labs_eval-runner",
				"evalrunner.gke-labs.dev/branch":      "main",
				"evalrunner.gke-labs.dev/head-sha":    "c0ffee1",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": "2026-10-30T20:00:00Z",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner",
				Commit: "c0ffee1",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:        "eval/docs",
					Phase:     v1alpha1.TaskPhaseCompleted,
					Samples:   8,
					Successes: 6,
				},
			},
		},
	}

	run2 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-c0ffee1-2",
			Namespace: "eval-runner",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/target-repo": "github.com_gke-labs_eval-runner",
				"evalrunner.gke-labs.dev/repo":        "github.com_gke-labs_eval-runner",
				"evalrunner.gke-labs.dev/branch":      "main",
				"evalrunner.gke-labs.dev/head-sha":    "c0ffee1",
				"evalrunner.gke-labs.dev/topup":       "2",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": "2026-10-30T20:00:00Z",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner",
				Commit: "c0ffee1",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:        "eval/docs",
					Phase:     v1alpha1.TaskPhaseCompleted,
					Samples:   8,
					Successes: 7,
				},
			},
		},
	}

	ref := RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	obs := buildRepoObservations([]v1alpha1.Run{*run1, *run2}, ref, "main", "", "", false)
	sparklines := obs.EvalSparklineItems("eval/docs", obs.Commits)
	if len(sparklines) != 1 {
		t.Fatalf("expected 1 commit sparkline item, got %d", len(sparklines))
	}
	if sparklines[0].ObsCount != 2 {
		t.Errorf("expected ObsCount=2, got %d", sparklines[0].ObsCount)
	}
	if sparklines[0].SuccessRate == nil {
		t.Fatalf("expected SuccessRate to be set")
	}
	expectedRate := 13.0 / 16.0
	if fmt.Sprintf("%.2f", *sparklines[0].SuccessRate) != fmt.Sprintf("%.2f", expectedRate) {
		t.Errorf("expected pooled success rate %.2f, got %.2f", expectedRate, *sparklines[0].SuccessRate)
	}

	// Also verify HTTP response renders successfully
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run1, run2).Build()
	server := NewServer(Config{Client: cl, DefaultNamespace: "eval-runner"})

	req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner?branch=main", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "0.81") && !strings.Contains(body, "81%") {
		t.Errorf("expected body to contain pooled score 0.81 or 81%%, body:\n%s", body)
	}
}
