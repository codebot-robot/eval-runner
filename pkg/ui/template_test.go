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
	"bytes"
	"flag"
	"html"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/pmezard/go-difflib/difflib"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var update = flag.Bool("update", false, "update golden files")

func assertGolden(t *testing.T, filename string, actual string) {
	t.Helper()
	goldenPath := filepath.Join("testdata", filename)
	if *update {
		if err := os.WriteFile(goldenPath, []byte(actual), 0644); err != nil {
			t.Fatalf("failed to update golden file %s: %v", goldenPath, err)
		}
		return
	}

	expected, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("failed to read golden file %s: %v (run with -update to generate)", goldenPath, err)
	}

	if string(expected) != actual {
		diff, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A:        difflib.SplitLines(string(expected)),
			B:        difflib.SplitLines(actual),
			FromFile: "Expected",
			ToFile:   "Actual",
			Context:  3,
		})
		t.Errorf("rendered HTML does not match golden file %s:\n%s", goldenPath, diff)
	}
}

func TestCalculateComparisonBarGeometry(t *testing.T) {
	barWidth := 340.0
	// Zero is at 170.0px.
	// scale is 340px / 1.0 unit.

	t.Run("PositiveDelta", func(t *testing.T) {
		delta := 0.23
		ci := &v1alpha1.DeltaConfidenceInterval{
			Lower: 0.11,
			Upper: 0.35,
		}
		leftPx, widthPx, dotPx, hasCI := CalculateComparisonBarGeometry(&delta, ci, barWidth)
		if !hasCI {
			t.Fatal("expected hasCI to be true")
		}

		// left: 170 + 0.11 * 340 = 207.4px
		expectedLeft := 170.0 + 0.11*340.0
		if math.Abs(leftPx-expectedLeft) > 1e-6 {
			t.Errorf("expected leftPx %.2f, got %.2f", expectedLeft, leftPx)
		}

		// right: 170 + 0.35 * 340 = 289.0px
		expectedWidth := (0.35 - 0.11) * 340.0
		if math.Abs(widthPx-expectedWidth) > 1e-6 {
			t.Errorf("expected widthPx %.2f, got %.2f", expectedWidth, widthPx)
		}

		// dot: 170 + 0.23 * 340 - 7 = 241.2px
		expectedDot := 170.0 + 0.23*340.0 - 7.0
		if math.Abs(dotPx-expectedDot) > 1e-6 {
			t.Errorf("expected dotPx %.2f, got %.2f", expectedDot, dotPx)
		}
	})

	t.Run("NegativeDelta", func(t *testing.T) {
		delta := -0.20
		ci := &v1alpha1.DeltaConfidenceInterval{
			Lower: -0.30,
			Upper: -0.10,
		}
		leftPx, widthPx, dotPx, hasCI := CalculateComparisonBarGeometry(&delta, ci, barWidth)
		if !hasCI {
			t.Fatal("expected hasCI to be true")
		}

		// left: 170 - 0.30 * 340 = 68.0px
		expectedLeft := 170.0 - 0.30*340.0
		if math.Abs(leftPx-expectedLeft) > 1e-6 {
			t.Errorf("expected leftPx %.2f, got %.2f", expectedLeft, leftPx)
		}

		expectedWidth := 0.20 * 340.0
		if math.Abs(widthPx-expectedWidth) > 1e-6 {
			t.Errorf("expected widthPx %.2f, got %.2f", expectedWidth, widthPx)
		}

		expectedDot := 170.0 - 0.20*340.0 - 7.0
		if math.Abs(dotPx-expectedDot) > 1e-6 {
			t.Errorf("expected dotPx %.2f, got %.2f", expectedDot, dotPx)
		}
	})

	t.Run("NilCI", func(t *testing.T) {
		delta := 0.10
		_, _, _, hasCI := CalculateComparisonBarGeometry(&delta, nil, barWidth)
		if hasCI {
			t.Errorf("expected hasCI to be false for nil CI")
		}
	})

	t.Run("ClampingExtremeDelta", func(t *testing.T) {
		delta := 0.90
		ci := &v1alpha1.DeltaConfidenceInterval{
			Lower: 0.40,
			Upper: 0.95,
		}
		leftPx, widthPx, dotPx, hasCI := CalculateComparisonBarGeometry(&delta, ci, barWidth)
		if !hasCI {
			t.Fatal("expected hasCI to be true")
		}
		if leftPx < 0 || leftPx > barWidth {
			t.Errorf("leftPx %.2f out of bounds [0, %.2f]", leftPx, barWidth)
		}
		if leftPx+widthPx > barWidth {
			t.Errorf("right bound %.2f exceeds barWidth %.2f", leftPx+widthPx, barWidth)
		}
		if dotPx < 0 || dotPx > barWidth-14.0 {
			t.Errorf("dotPx %.2f out of bounds", dotPx)
		}
	})
}

func TestCalculateRunBarGeometry(t *testing.T) {
	barWidth := 240.0

	t.Run("NormalRate", func(t *testing.T) {
		rate := 0.80
		ci := &v1alpha1.ConfidenceInterval{
			Lower: 0.60,
			Upper: 0.90,
		}
		leftPx, widthPx, dotPx, hasCI := CalculateRunBarGeometry(&rate, nil, ci, barWidth)
		if !hasCI {
			t.Fatal("expected hasCI to be true")
		}

		expectedLeft := 0.60 * 240.0 // 144.0px
		if math.Abs(leftPx-expectedLeft) > 1e-6 {
			t.Errorf("expected leftPx %.2f, got %.2f", expectedLeft, leftPx)
		}

		expectedWidth := (0.90 - 0.60) * 240.0 // 72.0px
		if math.Abs(widthPx-expectedWidth) > 1e-6 {
			t.Errorf("expected widthPx %.2f, got %.2f", expectedWidth, widthPx)
		}

		expectedDot := 0.80*240.0 - 7.0 // 185.0px
		if math.Abs(dotPx-expectedDot) > 1e-6 {
			t.Errorf("expected dotPx %.2f, got %.2f", expectedDot, dotPx)
		}
	})

	t.Run("NilCI", func(t *testing.T) {
		rate := 0.50
		_, _, _, hasCI := CalculateRunBarGeometry(&rate, nil, nil, barWidth)
		if hasCI {
			t.Errorf("expected hasCI to be false")
		}
	})
}

func sampleRunDetailView() RunDetailView {
	return RunDetailView{
		ActiveTab:       NavTabRuns,
		Name:            "docs-eval-cand-05ce4d8",
		Namespace:       "eval-runner",
		Phase:           v1alpha1.RunPhaseCompleted,
		PhaseChip:       "good",
		RepoURL:         "https://github.com/gke-labs/eval-runner.git",
		Commit:          "05ce4d8222222222222222222222222222222222",
		CommitShort:     "05ce4d8",
		Model:           "google/gemini-3.5-flash-lite",
		Age:             "10m",
		ArtifactsPrefix: "gs://eval-bucket/runs/05ce4d8/",
		TasksSummary:    "1/1",
		TasksCompleted:  1,
		TasksFailed:     0,
		TasksTotal:      1,
		TotalSamples:    48,
		MeanSuccessRate: "0.84",
		Tasks: []RunTaskRow{
			{
				ID:          "triage/runbook_lookup",
				Phase:       v1alpha1.TaskPhaseCompleted,
				PhaseChip:   "good",
				RateStr:     "0.84",
				HasCI:       true,
				CILeftPx:    170.4,
				CIWidthPx:   50.4,
				CIDotPx:     194.6,
				CILabel:     "[0.71, 0.92]",
				Samples:     48,
				Successes:   40,
				Method:      "wilson",
				LogPath:     "gs://eval-bucket/runs/05ce4d8/triage/runbook_lookup/inspect.json",
				SamplesLink: "/runs/eval-runner/docs-eval-cand-05ce4d8/tasks/triage%2Frunbook_lookup/samples",
			},
		},
	}
}

func sampleComparisonDetailView() ComparisonDetailView {
	return ComparisonDetailView{
		ActiveTab:    NavTabComparisons,
		Name:         "docs-eval-same-commit",
		Namespace:    "eval-runner",
		Verdict:      v1alpha1.ComparisonVerdictImproved,
		VerdictChip:  "good",
		Headline:     "05ce4d8 vs a81d0c4 · P(improvement) = 0.98",
		PImprovement: "0.98",
		Tasks: []ComparisonTaskRow{
			{
				ID:          "triage/runbook_lookup",
				BaseRate:    "0.61",
				CandRate:    "0.84",
				Delta:       "+0.23",
				HasCI:       true,
				CILeftPx:    207.4,
				CIWidthPx:   81.6,
				CIDotPx:     241.2,
				CILabel:     "[+0.11, +0.35]",
				StatusClass: "good",
				Label:       "improved",
				Pairs:       48,
				DroppedCand: 0,
				DroppedBase: 0,
			},
		},
		BaseRun:         "docs-eval-base-05ce4d8",
		BaseCommitShort: "a81d0c4",
		CandRun:         "docs-eval-cand-05ce4d8",
		CandCommitShort: "05ce4d8",
		TotalSamples:    96,
		TotalPairs:      48,
	}
}

func sampleRunsListView() RunsListView {
	return RunsListView{
		ActiveTab: NavTabRuns,
		Namespace: "eval-runner",
		Runs: []RunListItem{
			{
				Name:      "docs-eval-cand-05ce4d8",
				Namespace: "eval-runner",
				Phase:     v1alpha1.RunPhaseCompleted,
				PhaseChip: "good",
				Tasks:     "1/1",
				Samples:   48,
				Rates:     "triage/runbook_lookup=0.84[0.71,0.92]",
				Commit:    "05ce4d8",
				Age:       "10m",
				Link:      "/runs/eval-runner/docs-eval-cand-05ce4d8",
			},
			{
				Name:      "docs-eval-base-05ce4d8",
				Namespace: "eval-runner",
				Phase:     v1alpha1.RunPhaseCompleted,
				PhaseChip: "good",
				Tasks:     "1/1",
				Samples:   48,
				Rates:     "triage/runbook_lookup=0.61[0.46,0.74]",
				Commit:    "a81d0c4",
				Age:       "15m",
				Link:      "/runs/eval-runner/docs-eval-base-05ce4d8",
			},
		},
		TotalCount:   2,
		Offset:       0,
		Limit:        100,
		HasMore:      false,
		HasPrev:      false,
		ShowingRange: "Showing 1–2 of 2",
	}
}

func sampleComparisonsListView() ComparisonsListView {
	return ComparisonsListView{
		ActiveTab: NavTabComparisons,
		Namespace: "eval-runner",
		Comparisons: []ComparisonListItem{
			{
				Name:         "docs-eval-same-commit",
				Namespace:    "eval-runner",
				Verdict:      v1alpha1.ComparisonVerdictImproved,
				VerdictChip:  "good",
				PImprovement: "0.98",
				Tasks:        "1/1",
				BaselineRun:  "docs-eval-base-05ce4d8",
				CandidateRun: "docs-eval-cand-05ce4d8",
				Age:          "5m",
				Link:         "/comparisons/eval-runner/docs-eval-same-commit",
			},
		},
		TotalCount:   1,
		Offset:       0,
		Limit:        100,
		HasMore:      false,
		HasPrev:      false,
		ShowingRange: "Showing 1–1 of 1",
	}
}

func samplePullRequestsListView() PullRequestsListView {
	return PullRequestsListView{
		ActiveTab: NavTabPulls,
		Namespace: "eval-runner",
		Pulls: []PullRequestListItem{
			{
				Repository:     "gke-labs/eval-runner",
				RepoHash:       RepoHash("gke-labs/eval-runner"),
				Number:         42,
				GitHubURL:      "https://github.com/gke-labs/eval-runner/pull/42",
				HeadSHA:        "05ce4d8222222222222222222222222222222222",
				HeadSHAShort:   "05ce4d8",
				Verdict:        v1alpha1.ComparisonVerdictImproved,
				VerdictChip:    "good",
				PImprovement:   "0.98",
				SamplesRun:     96,
				Batches:        2,
				CompletionTime: "5m",
				ComparisonName: "pr-42",
				ComparisonLink: "/comparisons/eval-runner/pr-42",
				HistoryLink:    "/pulls/" + RepoHash("gke-labs/eval-runner") + "/42",
				Namespace:      "eval-runner",
			},
		},
		TotalCount:   1,
		Offset:       0,
		Limit:        100,
		HasMore:      false,
		HasPrev:      false,
		ShowingRange: "Showing 1–1 of 1",
	}
}

func samplePullRequestDetailView() PullRequestDetailView {
	return PullRequestDetailView{
		ActiveTab:  NavTabPulls,
		Namespace:  "eval-runner",
		Repository: "gke-labs/eval-runner",
		RepoHash:   RepoHash("gke-labs/eval-runner"),
		Number:     42,
		GitHubURL:  "https://github.com/gke-labs/eval-runner/pull/42",
		BackLink:   "/pulls",
		TotalCount: 2,
		Comparisons: []PullRequestHistoryItem{
			{
				Name:           "pr-42-head2",
				Namespace:      "eval-runner",
				HeadSHA:        "05ce4d8222222222222222222222222222222222",
				HeadSHAShort:   "05ce4d8",
				BaseSHA:        "a81d0c4111111111111111111111111111111111",
				BaseSHAShort:   "a81d0c4",
				Verdict:        v1alpha1.ComparisonVerdictImproved,
				VerdictChip:    "good",
				PImprovement:   "0.98",
				SamplesRun:     96,
				Batches:        2,
				CompletionTime: "5m",
				Age:            "10m",
				Link:           "/comparisons/eval-runner/pr-42-head2",
			},
			{
				Name:           "pr-42-head1",
				Namespace:      "eval-runner",
				HeadSHA:        "1111111222222222222222222222222222222222",
				HeadSHAShort:   "1111111",
				BaseSHA:        "a81d0c4111111111111111111111111111111111",
				BaseSHAShort:   "a81d0c4",
				Verdict:        v1alpha1.ComparisonVerdictInconclusive,
				VerdictChip:    "neutral",
				PImprovement:   "0.50",
				SamplesRun:     48,
				Batches:        1,
				CompletionTime: "15m",
				Age:            "20m",
				Link:           "/comparisons/eval-runner/pr-42-head1",
			},
		},
	}
}

func TestTemplateRenderingRunDetailGolden(t *testing.T) {
	view := sampleRunDetailView()

	var buf bytes.Buffer
	if err := tmplRunDetail.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute run detail template: %v", err)
	}

	assertGolden(t, "run_detail.golden.html", buf.String())
}

func TestTemplateRenderingComparisonDetailGolden(t *testing.T) {
	view := sampleComparisonDetailView()

	var buf bytes.Buffer
	if err := tmplComparisonDetail.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute comparison detail template: %v", err)
	}

	assertGolden(t, "comparison_detail.golden.html", buf.String())
}

func batchesTokensComparisonFixture() (*v1alpha1.Comparison, *v1alpha1.Run, *v1alpha1.Run) {
	rateImprovedBase := 0.60
	rateImprovedCand := 0.90
	deltaImproved := 0.30

	rateInconclusiveBase := 0.20
	rateInconclusiveCand := 0.15
	deltaInconclusive := -0.05

	comp := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "docs-eval-batches-tokens",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.ComparisonSpec{
			Baseline: v1alpha1.ComparisonRunRef{
				Run: "docs-eval-base-05ce4d8",
			},
			Candidate: v1alpha1.ComparisonRunRef{
				Run: "docs-eval-cand-05ce4d8",
			},
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "gke-labs/eval-runner",
				Number:     98,
				HeadSHA:    "05ce4d8222222222222222222222222222222222",
				BaseSHA:    "a81d0c4111111111111111111111111111111111",
			},
		},
		Status: v1alpha1.ComparisonStatus{
			Phase:       v1alpha1.ComparisonPhaseRunning,
			Verdict:     v1alpha1.ComparisonVerdictRunning,
			Message:     "Running batch 2 (1 tasks continuing)",
			SummaryPath: "gs://eval-bucket/comparisons/docs-eval-batches-tokens.json",
			Summary: &v1alpha1.ComparisonSummary{
				Tasks: "1/3",
			},
			Batches: []v1alpha1.ComparisonBatchStatus{
				{
					Index:        1,
					Phase:        v1alpha1.ComparisonPhaseCompleted,
					SampleRange:  &v1alpha1.SampleRange{Start: 1, End: 8},
					CandidateRun: "docs-eval-cand-05ce4d8-b1",
					BaselineRun:  "docs-eval-base-05ce4d8-b1",
				},
				{
					Index:        2,
					Phase:        v1alpha1.ComparisonPhaseRunning,
					SampleRange:  &v1alpha1.SampleRange{Start: 9, End: 16},
					CandidateRun: "docs-eval-cand-05ce4d8-b2",
					BaselineRun:  "docs-eval-base-05ce4d8-b2",
				},
			},
			Tasks: []v1alpha1.ComparisonTaskStatus{
				{
					ID:            "docs/readme_comprehension",
					BaseRate:      &rateImprovedBase,
					CandidateRate: &rateImprovedCand,
					Delta:         &deltaImproved,
					DeltaCI: &v1alpha1.DeltaConfidenceInterval{
						Lower: 0.10,
						Upper: 0.50,
					},
					Label:    v1alpha1.ComparisonTaskLabelImproved,
					Decision: v1alpha1.ComparisonTaskLabelImproved,
					Pairs:    16,
					Wins:     8,
					Losses:   2,
					Ties:     6,
					Tokens: &v1alpha1.TaskTokens{
						Input:     600,
						Output:    200,
						Total:     800,
						Reasoning: 100,
					},
				},
				{
					ID:            "docs/readme_comprehension_blind",
					BaseRate:      &rateInconclusiveBase,
					CandidateRate: &rateInconclusiveCand,
					Delta:         &deltaInconclusive,
					DeltaCI: &v1alpha1.DeltaConfidenceInterval{
						Lower: -0.15,
						Upper: 0.00,
					},
					Label:         v1alpha1.ComparisonTaskLabelInconclusive,
					Decision:      v1alpha1.ComparisonTaskLabelContinue,
					Pairs:         16,
					Wins:          1,
					Losses:        2,
					Ties:          13,
					SuggestedNext: 16,
				},
				{
					ID:    "docs/readme_summary",
					Label: v1alpha1.ComparisonTaskLabelRunning,
				},
			},
		},
	}

	baseRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "docs-eval-base-05ce4d8",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "a81d0c4111111111111111111111111111111111",
			},
			Model: "google/gemini-3.5-flash-lite",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
		},
	}

	candRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "docs-eval-cand-05ce4d8",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "05ce4d8222222222222222222222222222222222",
			},
			Model: "google/gemini-3.5-flash-lite",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseRunning,
		},
	}

	return comp, baseRun, candRun
}

func TestTemplateRenderingComparisonDetail_BatchesTokensGolden(t *testing.T) {
	comp, baseRun, candRun := batchesTokensComparisonFixture()
	view := NewComparisonDetailView(comp, baseRun, candRun)

	var buf bytes.Buffer
	if err := tmplComparisonDetail.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute comparison detail template: %v", err)
	}

	assertGolden(t, "comparison_detail_batches_tokens.golden.html", buf.String())
}

func TestTemplateRenderingRunsListGolden(t *testing.T) {
	view := sampleRunsListView()

	var buf bytes.Buffer
	if err := tmplRunsList.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute runs list template: %v", err)
	}

	assertGolden(t, "runs_list.golden.html", buf.String())
}

func TestTemplateRenderingComparisonsListGolden(t *testing.T) {
	view := sampleComparisonsListView()

	var buf bytes.Buffer
	if err := tmplComparisonsList.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute comparisons list template: %v", err)
	}

	assertGolden(t, "comparisons_list.golden.html", buf.String())
}

func TestTemplateRenderingPullsListGolden(t *testing.T) {
	view := samplePullRequestsListView()

	var buf bytes.Buffer
	if err := tmplPullsList.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute pulls list template: %v", err)
	}

	assertGolden(t, "pulls_list.golden.html", buf.String())
}

func TestTemplateRenderingPullDetailGolden(t *testing.T) {
	view := samplePullRequestDetailView()

	var buf bytes.Buffer
	if err := tmplPullDetail.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute pull detail template: %v", err)
	}

	assertGolden(t, "pull_detail.golden.html", buf.String())
}

func TestTemplateRenderingComparisonTableGolden(t *testing.T) {
	tasks := sampleComparisonDetailView().Tasks

	var buf bytes.Buffer
	if err := RenderComparisonTable(&buf, ComparisonTableView{Tasks: tasks}); err != nil {
		t.Fatalf("failed to execute comparison table sub-template: %v", err)
	}

	assertGolden(t, "comparison_table.golden.html", buf.String())
}

func TestTemplateRenderingComparisonTable_WithAffinity(t *testing.T) {
	tasks := []ComparisonTaskRow{
		{
			ID:          "docs/readme_comprehension",
			Affinity:    "matched",
			BaseRate:    "0.60",
			CandRate:    "0.80",
			Delta:       "+0.20",
			HasCI:       true,
			CILeftPx:    160.0,
			CIWidthPx:   40.0,
			CIDotPx:     180.0,
			CILabel:     "[+0.05, +0.35]",
			StatusClass: "good",
			Label:       "improved",
			SamplesRun:  16,
		},
		{
			ID:          "src/other_task",
			Affinity:    "unmatched",
			BaseRate:    "0.50",
			CandRate:    "0.50",
			Delta:       "+0.00",
			StatusClass: "neutral",
			Label:       "unchanged",
			SamplesRun:  16,
		},
	}

	var buf bytes.Buffer
	if err := RenderComparisonTable(&buf, ComparisonTableView{Tasks: tasks, HasAffinity: true}); err != nil {
		t.Fatalf("failed to execute comparison table with affinity: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "<th>affinity</th>") {
		t.Errorf("expected <th>affinity</th> in table header, got:\n%s", output)
	}
	if !strings.Contains(output, "<td class=\"mono\">matched</td>") {
		t.Errorf("expected matched affinity cell, got:\n%s", output)
	}
	if !strings.Contains(output, "<td class=\"mono\">unmatched</td>") {
		t.Errorf("expected unmatched affinity cell, got:\n%s", output)
	}
}

func TestTemplateRenderingRunDetail_RunningProgress(t *testing.T) {
	view := sampleRunDetailView()
	view.Phase = v1alpha1.RunPhaseRunning
	view.Tasks = []RunTaskRow{
		{
			ID:             "triage/runbook_lookup",
			Phase:          v1alpha1.TaskPhaseRunning,
			PhaseChip:      "warn",
			RateStr:        "—",
			SamplesDone:    8,
			SamplesPlanned: 96,
			ProgressPct:    8.333333,
			ProgressText:   "8 of 96 samples",
		},
	}

	var buf bytes.Buffer
	if err := tmplRunDetail.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute run detail template: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `<meta http-equiv="refresh" content="15">`) {
		t.Errorf("expected meta refresh on running run page, got:\n%s", html)
	}
	if !strings.Contains(html, "8 of 96 samples") {
		t.Errorf("expected '8 of 96 samples' in rendered HTML, got:\n%s", html)
	}
	if !strings.Contains(html, `class="progress-bar"`) {
		t.Errorf("expected progress-bar in rendered HTML")
	}
	if !strings.Contains(html, `class="progress-fill"`) {
		t.Errorf("expected progress-fill in rendered HTML")
	}
}

func TestTemplateRenderingRunDetail_CompletedNoMetaRefresh(t *testing.T) {
	view := sampleRunDetailView()
	view.Phase = v1alpha1.RunPhaseCompleted

	var buf bytes.Buffer
	if err := tmplRunDetail.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute run detail template: %v", err)
	}

	html := buf.String()
	if strings.Contains(html, `http-equiv="refresh"`) {
		t.Errorf("expected NO meta refresh on completed run page")
	}
}

func TestTemplateRenderingRunsList_RunningTasks(t *testing.T) {
	view := sampleRunsListView()
	view.Runs = append(view.Runs, RunListItem{
		Name:      "running-run",
		Namespace: "eval-runner",
		Phase:     "Running",
		PhaseChip: "warn",
		Tasks:     "1/2 tasks running",
		Link:      "/runs/eval-runner/running-run",
	})

	var buf bytes.Buffer
	if err := tmplRunsList.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute runs list template: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "1/2 tasks running") {
		t.Errorf("expected '1/2 tasks running' in rendered HTML, got:\n%s", html)
	}
}

func TestEscapingHostileFixtures(t *testing.T) {
	const (
		hostileScript = `<script>alert('pwned')</script>`
		hostileImg    = `'"><img src=x onerror=alert('img')>`
		hostileIframe = `"><iframe src="javascript:alert('frame')"></iframe>`
	)

	t.Run("RunDetailEscaping", func(t *testing.T) {
		view := RunDetailView{
			ActiveTab:       NavTabRuns,
			Name:            "hostile-run",
			Namespace:       "eval-runner",
			Phase:           v1alpha1.RunPhaseFailed,
			PhaseChip:       "bad",
			RepoURL:         "https://github.com/evil/repo" + hostileImg,
			Commit:          "commit" + hostileScript,
			CommitShort:     "c" + hostileScript,
			Model:           "model" + hostileIframe,
			Age:             "1m",
			ArtifactsPrefix: "gs://bucket/" + hostileScript,
			Message:         "run failed: " + hostileScript + hostileImg,
			Tasks: []RunTaskRow{
				{
					ID:        "task/" + hostileScript,
					Phase:     v1alpha1.TaskPhaseFailed,
					PhaseChip: "bad",
					RateStr:   "0.00",
					Samples:   10,
					Successes: 0,
					Method:    hostileScript,
					LogPath:   "gs://bucket/" + hostileImg,
					Message:   "task message: " + hostileScript,
				},
			},
		}

		var buf bytes.Buffer
		if err := tmplRunDetail.Execute(&buf, view); err != nil {
			t.Fatalf("template render error: %v", err)
		}
		html := buf.String()

		for _, dangerous := range []string{"<script", "<img", "<iframe"} {
			if strings.Contains(html, dangerous) {
				t.Errorf("unescaped dangerous tag found in RunDetail HTML: %q", dangerous)
			}
		}

		// Ensure escaped entities exist
		if !strings.Contains(html, "&lt;script&gt;") {
			t.Errorf("expected escaped &lt;script&gt; in HTML")
		}
		if !strings.Contains(html, "&lt;img") {
			t.Errorf("expected escaped &lt;img in HTML")
		}
		if !strings.Contains(html, "&lt;iframe") {
			t.Errorf("expected escaped &lt;iframe in HTML")
		}
	})

	t.Run("ComparisonDetailEscaping", func(t *testing.T) {
		view := ComparisonDetailView{
			ActiveTab:    NavTabComparisons,
			Name:         "hostile-comp",
			Namespace:    "eval-runner",
			Verdict:      v1alpha1.ComparisonVerdictRegressed,
			VerdictChip:  "bad",
			Headline:     "headline " + hostileScript,
			PImprovement: "0.00",
			SummaryPath:  "gs://bucket/" + hostileImg,
			Message:      "status: " + hostileIframe,
			Tasks: []ComparisonTaskRow{
				{
					ID:          "task/" + hostileScript,
					BaseRate:    "0.50",
					CandRate:    "0.20",
					Delta:       "-0.30" + hostileScript,
					HasCI:       true,
					CILeftPx:    50.0,
					CIWidthPx:   30.0,
					CIDotPx:     60.0,
					CILabel:     "[-0.40, -0.20]" + hostileImg,
					StatusClass: "bad",
					Label:       "regressed",
					Pairs:       20,
				},
			},
			BaseRun:         "base" + hostileScript,
			BaseCommitShort: "b" + hostileScript,
			CandRun:         "cand" + hostileScript,
			CandCommitShort: "c" + hostileScript,
		}

		var buf bytes.Buffer
		if err := tmplComparisonDetail.Execute(&buf, view); err != nil {
			t.Fatalf("template render error: %v", err)
		}
		html := buf.String()

		for _, dangerous := range []string{"<script", "<img", "<iframe"} {
			if strings.Contains(html, dangerous) {
				t.Errorf("unescaped dangerous tag found in ComparisonDetail HTML: %q", dangerous)
			}
		}

		if !strings.Contains(html, "&lt;script&gt;") {
			t.Errorf("expected escaped &lt;script&gt; in HTML")
		}
		if !strings.Contains(html, "&lt;img") {
			t.Errorf("expected escaped &lt;img in HTML")
		}
		if !strings.Contains(html, "&lt;iframe") {
			t.Errorf("expected escaped &lt;iframe in HTML")
		}
	})

	t.Run("ComparisonTableSubTemplateEscaping", func(t *testing.T) {
		tasks := []ComparisonTaskRow{
			{
				ID:          "hostile/" + hostileScript,
				BaseRate:    "0.60",
				CandRate:    "0.40",
				Delta:       "-0.20" + hostileScript,
				HasCI:       true,
				CILeftPx:    100.0,
				CIWidthPx:   40.0,
				CIDotPx:     120.0,
				CILabel:     "[-0.30, -0.10]" + hostileImg,
				StatusClass: "bad",
				Label:       "regressed",
				Pairs:       15,
			},
		}

		var buf bytes.Buffer
		if err := RenderComparisonTable(&buf, ComparisonTableView{Tasks: tasks}); err != nil {
			t.Fatalf("RenderComparisonTable error: %v", err)
		}
		html := buf.String()

		for _, dangerous := range []string{"<script", "<img", "<iframe"} {
			if strings.Contains(html, dangerous) {
				t.Errorf("unescaped dangerous content in comparison table sub-template: %q", dangerous)
			}
		}
		if !strings.Contains(html, "&lt;script&gt;") {
			t.Errorf("expected escaped &lt;script&gt; in sub-template HTML")
		}
	})

	t.Run("TaskSamplesListEscaping", func(t *testing.T) {
		view := TaskSamplesListView{
			ActiveTab:     NavTabRuns,
			Namespace:     "eval-runner",
			RunName:       "hostile-run" + hostileScript,
			TaskID:        "task/" + hostileImg,
			EscapedTaskID: "task%2Fescaped",
			Commit:        "c" + hostileScript,
			Model:         "model" + hostileIframe,
			LogPath:       "gs://bucket/" + hostileScript,
			Samples: []TaskSampleRow{
				{
					ID:            "sample-" + hostileScript,
					Score:         "0.00",
					InputPreview:  "input " + hostileScript,
					OutputPreview: "output " + hostileImg,
					LatencyStr:    "1.0s",
					TokensStr:     "100",
					Link:          "/runs/eval-runner/hostile/samples/1",
				},
			},
		}

		var buf bytes.Buffer
		if err := RenderTaskSamplesList(&buf, view); err != nil {
			t.Fatalf("RenderTaskSamplesList error: %v", err)
		}
		html := buf.String()

		for _, dangerous := range []string{"<script", "<img", "<iframe"} {
			if strings.Contains(html, dangerous) {
				t.Errorf("unescaped dangerous content in task samples list template: %q", dangerous)
			}
		}
		if !strings.Contains(html, "&lt;script&gt;") {
			t.Errorf("expected escaped &lt;script&gt; in task samples list HTML")
		}
	})

	t.Run("SampleTranscriptEscaping", func(t *testing.T) {
		view := SampleTranscriptView{
			ActiveTab:   NavTabRuns,
			Namespace:   "eval-runner",
			RunName:     "hostile-run" + hostileScript,
			TaskID:      "task/" + hostileImg,
			SampleID:    "sample-" + hostileScript,
			Score:       "0.00",
			Status:      "fail",
			StatusUpper: "FAIL",
			StatusClass: "bad",
			LatencyStr:  "1.0s",
			TokensStr:   "100",
			LogPath:     "gs://bucket/" + hostileScript,
			Rows: []TranscriptRowView{
				{Kind: "user", Content: "user " + hostileScript},
				{Kind: "tool_call", Class: "tool", Content: "call " + hostileImg},
				{Kind: "tool", Class: "tool", Content: "tool " + hostileIframe},
				{Kind: "assistant", Class: "bad", Content: "assistant " + hostileScript},
			},
		}

		var buf bytes.Buffer
		if err := RenderSampleTranscript(&buf, view); err != nil {
			t.Fatalf("RenderSampleTranscript error: %v", err)
		}
		html := buf.String()

		for _, dangerous := range []string{"<script", "<img", "<iframe"} {
			if strings.Contains(html, dangerous) {
				t.Errorf("unescaped dangerous content in sample transcript template: %q", dangerous)
			}
		}
		if !strings.Contains(html, "&lt;script&gt;") {
			t.Errorf("expected escaped &lt;script&gt; in sample transcript HTML")
		}
	})

	t.Run("PullsListEscaping", func(t *testing.T) {
		view := PullRequestsListView{
			ActiveTab: NavTabPulls,
			Namespace: "eval-runner",
			Pulls: []PullRequestListItem{
				{
					Repository:     "org/repo" + hostileScript,
					RepoHash:       "badhash",
					Number:         12,
					GitHubURL:      "https://github.com/evil/repo" + hostileImg,
					HeadSHA:        "head" + hostileScript,
					HeadSHAShort:   "h" + hostileScript,
					Verdict:        v1alpha1.ComparisonVerdictImproved,
					VerdictChip:    "good",
					PImprovement:   "0.99" + hostileIframe,
					SamplesRun:     10,
					Batches:        1,
					CompletionTime: "1m" + hostileScript,
					ComparisonName: "comp" + hostileScript,
					ComparisonLink: "/comparisons/eval-runner/" + hostileImg,
					HistoryLink:    "/pulls/badhash/12" + hostileScript,
					Namespace:      "eval-runner",
				},
			},
		}

		var buf bytes.Buffer
		if err := tmplPullsList.Execute(&buf, view); err != nil {
			t.Fatalf("tmplPullsList error: %v", err)
		}
		html := buf.String()

		for _, dangerous := range []string{"<script", "<img", "<iframe"} {
			if strings.Contains(html, dangerous) {
				t.Errorf("unescaped dangerous content in pulls list template: %q", dangerous)
			}
		}
		if !strings.Contains(html, "&lt;script&gt;") {
			t.Errorf("expected escaped &lt;script&gt; in pulls list HTML")
		}
	})

	t.Run("PullDetailEscaping", func(t *testing.T) {
		view := PullRequestDetailView{
			ActiveTab:  NavTabPulls,
			Namespace:  "eval-runner",
			Repository: "org/repo" + hostileScript,
			RepoHash:   "badhash",
			Number:     12,
			GitHubURL:  "https://github.com/evil/repo" + hostileImg,
			BackLink:   "/pulls" + hostileIframe,
			TotalCount: 1,
			Comparisons: []PullRequestHistoryItem{
				{
					Name:           "comp" + hostileScript,
					Namespace:      "eval-runner",
					HeadSHA:        "head" + hostileScript,
					HeadSHAShort:   "h" + hostileScript,
					BaseSHA:        "base" + hostileScript,
					BaseSHAShort:   "b" + hostileScript,
					Verdict:        v1alpha1.ComparisonVerdictImproved,
					VerdictChip:    "good",
					PImprovement:   "0.99" + hostileIframe,
					SamplesRun:     10,
					Batches:        1,
					CompletionTime: "1m" + hostileScript,
					Age:            "5m" + hostileImg,
					Link:           "/comparisons/eval-runner/comp" + hostileScript,
				},
			},
		}

		var buf bytes.Buffer
		if err := tmplPullDetail.Execute(&buf, view); err != nil {
			t.Fatalf("tmplPullDetail error: %v", err)
		}
		html := buf.String()

		for _, dangerous := range []string{"<script", "<img", "<iframe"} {
			if strings.Contains(html, dangerous) {
				t.Errorf("unescaped dangerous content in pull detail template: %q", dangerous)
			}
		}
		if !strings.Contains(html, "&lt;script&gt;") {
			t.Errorf("expected escaped &lt;script&gt; in pull detail HTML")
		}
	})
}

func TestComparisonRows(t *testing.T) {
	t.Run("NilComparison", func(t *testing.T) {
		rows := ComparisonRows(nil)
		if rows != nil {
			t.Errorf("expected nil rows for nil comparison, got %v", rows)
		}
	})

	t.Run("EmptyTasks", func(t *testing.T) {
		comp := &v1alpha1.Comparison{}
		rows := ComparisonRows(comp)
		if rows == nil || len(rows) != 0 {
			t.Errorf("expected empty non-nil slice for empty tasks, got %v", rows)
		}
	})

	t.Run("FormattingAndGeometry", func(t *testing.T) {
		baseRate := 0.612
		candRate := 0.842
		delta := 0.230
		comp := &v1alpha1.Comparison{
			Status: v1alpha1.ComparisonStatus{
				Tasks: []v1alpha1.ComparisonTaskStatus{
					{
						ID:            "task/improved",
						BaseRate:      &baseRate,
						CandidateRate: &candRate,
						Delta:         &delta,
						DeltaCI: &v1alpha1.DeltaConfidenceInterval{
							Lower: 0.11,
							Upper: 0.35,
						},
						Dropped: v1alpha1.ComparisonDropped{
							Candidate: 2,
							Baseline:  1,
						},
						Label: v1alpha1.ComparisonTaskLabelImproved,
						Pairs: 20,
					},
					{
						ID:      "task/running",
						Label:   v1alpha1.ComparisonTaskLabelRunning,
						Pairs:   0,
						Dropped: v1alpha1.ComparisonDropped{},
					},
				},
			},
		}

		rows := ComparisonRows(comp)
		if len(rows) != 2 {
			t.Fatalf("expected 2 rows, got %d", len(rows))
		}

		r0 := rows[0]
		if r0.ID != "task/improved" {
			t.Errorf("expected ID 'task/improved', got %q", r0.ID)
		}
		if r0.BaseRate != "0.61" {
			t.Errorf("expected BaseRate '0.61', got %q", r0.BaseRate)
		}
		if r0.CandRate != "0.84" {
			t.Errorf("expected CandRate '0.84', got %q", r0.CandRate)
		}
		if r0.Delta != "+0.23" {
			t.Errorf("expected Delta '+0.23', got %q", r0.Delta)
		}
		if !r0.HasCI {
			t.Errorf("expected HasCI to be true")
		}
		if r0.CILabel != "[+0.11, +0.35]" {
			t.Errorf("expected CILabel '[+0.11, +0.35]', got %q", r0.CILabel)
		}
		if r0.StatusClass != "good" {
			t.Errorf("expected StatusClass 'good', got %q", r0.StatusClass)
		}
		if r0.Label != "improved" {
			t.Errorf("expected Label 'improved', got %q", r0.Label)
		}
		if r0.Pairs != 20 {
			t.Errorf("expected Pairs 20, got %d", r0.Pairs)
		}
		if r0.DroppedCand != 2 {
			t.Errorf("expected DroppedCand 2, got %d", r0.DroppedCand)
		}
		if r0.DroppedBase != 1 {
			t.Errorf("expected DroppedBase 1, got %d", r0.DroppedBase)
		}
		if r0.CILeftPx <= 0 || r0.CIWidthPx <= 0 || r0.CIDotPx <= 0 {
			t.Errorf("expected non-zero geometry values, got left=%.1f width=%.1f dot=%.1f", r0.CILeftPx, r0.CIWidthPx, r0.CIDotPx)
		}

		r1 := rows[1]
		if r1.BaseRate != "—" || r1.CandRate != "—" || r1.Delta != "—" || r1.CILabel != "—" {
			t.Errorf("expected dashes for nil rates/delta/CI, got base=%s cand=%s delta=%s ci=%s", r1.BaseRate, r1.CandRate, r1.Delta, r1.CILabel)
		}
		if r1.HasCI {
			t.Errorf("expected HasCI false for nil CI")
		}
		if r1.StatusClass != "pending" {
			t.Errorf("expected StatusClass 'pending' for running, got %q", r1.StatusClass)
		}
	})
}

func TestComparisonRows_SubTemplateConsumes(t *testing.T) {
	baseRate := 0.50
	candRate := 0.75
	delta := 0.25
	comp := &v1alpha1.Comparison{
		Status: v1alpha1.ComparisonStatus{
			Tasks: []v1alpha1.ComparisonTaskStatus{
				{
					ID:            "eval/test_task",
					BaseRate:      &baseRate,
					CandidateRate: &candRate,
					Delta:         &delta,
					DeltaCI: &v1alpha1.DeltaConfidenceInterval{
						Lower: 0.10,
						Upper: 0.40,
					},
					Label: v1alpha1.ComparisonTaskLabelImproved,
					Pairs: 12,
				},
			},
		},
	}

	rows := ComparisonRows(comp)
	var buf bytes.Buffer
	if err := RenderComparisonTable(&buf, ComparisonTableView{Tasks: rows}); err != nil {
		t.Fatalf("failed to render comparison table sub-template with ComparisonRows: %v", err)
	}

	rawHTML := buf.String()
	unescapedHTML := html.UnescapeString(rawHTML)
	expectedSubstrings := []string{
		"eval/test_task",
		"0.50",
		"0.75",
		"+0.25",
		"[+0.10, +0.40]",
		"chip good",
		"improved",
		"12",
	}
	for _, expected := range expectedSubstrings {
		if !strings.Contains(unescapedHTML, expected) {
			t.Errorf("expected sub-template HTML to contain %q, but got:\n%s", expected, unescapedHTML)
		}
	}
}

func TestComparisonSummary(t *testing.T) {
	t.Run("NilComparison", func(t *testing.T) {
		sum := ComparisonSummary(nil, nil, nil)
		if sum.Headline != "" {
			t.Errorf("expected empty headline for nil comp, got %q", sum.Headline)
		}
		if sum.PImprovement != "—" {
			t.Errorf("expected PImprovement '—' for nil comp, got %q", sum.PImprovement)
		}
	})

	t.Run("ComparisonOnlyWithoutRuns", func(t *testing.T) {
		pImp := 0.26
		comp := &v1alpha1.Comparison{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-comp",
				Namespace: "default",
			},
			Spec: v1alpha1.ComparisonSpec{
				Baseline:  v1alpha1.ComparisonRunRef{Run: "base-run"},
				Candidate: v1alpha1.ComparisonRunRef{Run: "cand-run"},
			},
			Status: v1alpha1.ComparisonStatus{
				PImprovement: &pImp,
				Tasks: []v1alpha1.ComparisonTaskStatus{
					{
						ID:    "task-1",
						Pairs: 10,
						Dropped: v1alpha1.ComparisonDropped{
							Candidate: 1,
							Baseline:  2,
						},
					},
				},
			},
		}

		sum := ComparisonSummary(comp, nil, nil)
		expectedHeadline := "cand-run vs base-run · P(improvement) = 0.26"
		if sum.Headline != expectedHeadline {
			t.Errorf("expected headline %q, got %q", expectedHeadline, sum.Headline)
		}
		if sum.PImprovement != "0.26" {
			t.Errorf("expected PImprovement '0.26', got %q", sum.PImprovement)
		}
		if sum.BaseRun != "base-run" || sum.CandRun != "cand-run" {
			t.Errorf("expected runs base-run/cand-run, got %s/%s", sum.BaseRun, sum.CandRun)
		}
		if sum.BaseCommitShort != "" || sum.CandCommitShort != "" {
			t.Errorf("expected empty commit shorts, got base=%q cand=%q", sum.BaseCommitShort, sum.CandCommitShort)
		}
		if sum.TotalPairs != 10 {
			t.Errorf("expected TotalPairs 10, got %d", sum.TotalPairs)
		}
		if sum.TotalDroppedCand != 1 || sum.TotalDroppedBase != 2 {
			t.Errorf("expected dropped 1/2, got %d/%d", sum.TotalDroppedCand, sum.TotalDroppedBase)
		}
		// totalSamples = (10+1) + (10+2) = 23
		if sum.TotalSamples != 23 {
			t.Errorf("expected TotalSamples 23, got %d", sum.TotalSamples)
		}
		expectedSamples := "23 run · 10 paired · dropped: 1 cand, 2 base"
		if sum.SamplesSummary != expectedSamples {
			t.Errorf("expected SamplesSummary %q, got %q", expectedSamples, sum.SamplesSummary)
		}
		if sum.BaseSummary != "base-run" {
			t.Errorf("expected BaseSummary 'base-run', got %q", sum.BaseSummary)
		}
		if sum.CandSummary != "cand-run" {
			t.Errorf("expected CandSummary 'cand-run', got %q", sum.CandSummary)
		}
	})

	t.Run("WithRunsAndCommits", func(t *testing.T) {
		pImp := 0.98
		comp := &v1alpha1.Comparison{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-comp-runs",
				Namespace: "default",
			},
			Spec: v1alpha1.ComparisonSpec{
				Baseline:  v1alpha1.ComparisonRunRef{Run: "run-base"},
				Candidate: v1alpha1.ComparisonRunRef{Run: "run-cand"},
			},
			Status: v1alpha1.ComparisonStatus{
				PImprovement: &pImp,
				Tasks: []v1alpha1.ComparisonTaskStatus{
					{
						ID:    "task-1",
						Pairs: 48,
					},
				},
			},
		}

		baseRun := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "run-base",
				Namespace: "default",
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					Commit: "a81d0c4111111111111111111111111111111111",
				},
			},
			Status: v1alpha1.RunStatus{
				Summary: &v1alpha1.RunSummary{
					TotalSamples: 48,
				},
			},
		}

		candRun := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "run-cand",
				Namespace: "default",
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					Commit: "05ce4d8222222222222222222222222222222222",
				},
			},
			Status: v1alpha1.RunStatus{
				Summary: &v1alpha1.RunSummary{
					TotalSamples: 48,
				},
			},
		}

		sum := ComparisonSummary(comp, baseRun, candRun)
		expectedHeadline := "05ce4d8 vs a81d0c4 · P(improvement) = 0.98"
		if sum.Headline != expectedHeadline {
			t.Errorf("expected headline %q, got %q", expectedHeadline, sum.Headline)
		}
		if sum.BaseCommitShort != "a81d0c4" {
			t.Errorf("expected BaseCommitShort 'a81d0c4', got %q", sum.BaseCommitShort)
		}
		if sum.CandCommitShort != "05ce4d8" {
			t.Errorf("expected CandCommitShort '05ce4d8', got %q", sum.CandCommitShort)
		}
		if sum.TotalSamples != 96 {
			t.Errorf("expected TotalSamples 96, got %d", sum.TotalSamples)
		}
		if sum.TotalPairs != 48 {
			t.Errorf("expected TotalPairs 48, got %d", sum.TotalPairs)
		}
		if sum.SamplesSummary != "96 run · 48 paired" {
			t.Errorf("expected SamplesSummary '96 run · 48 paired', got %q", sum.SamplesSummary)
		}
		expectedBaseSummary := "commit a81d0c4 (run-base)"
		if sum.BaseSummary != expectedBaseSummary {
			t.Errorf("expected BaseSummary %q, got %q", expectedBaseSummary, sum.BaseSummary)
		}
		expectedCandSummary := "commit 05ce4d8 (run-cand)"
		if sum.CandSummary != expectedCandSummary {
			t.Errorf("expected CandSummary %q, got %q", expectedCandSummary, sum.CandSummary)
		}

		// Also verify reversed order in args is resolved correctly by name
		sumReversed := ComparisonSummary(comp, candRun, baseRun)
		if sumReversed.Headline != expectedHeadline {
			t.Errorf("expected headline %q with reversed runs, got %q", expectedHeadline, sumReversed.Headline)
		}
	})

	t.Run("NilPImprovement", func(t *testing.T) {
		comp := &v1alpha1.Comparison{
			Spec: v1alpha1.ComparisonSpec{
				Baseline:  v1alpha1.ComparisonRunRef{Run: "run-b"},
				Candidate: v1alpha1.ComparisonRunRef{Run: "run-c"},
			},
		}
		sum := ComparisonSummary(comp, nil, nil)
		if sum.PImprovement != "—" {
			t.Errorf("expected PImprovement '—', got %q", sum.PImprovement)
		}
		if sum.Headline != "run-c vs run-b" {
			t.Errorf("expected headline 'run-c vs run-b', got %q", sum.Headline)
		}
	})

	t.Run("PositionalFallbackWhenRunsHaveNoName", func(t *testing.T) {
		comp := &v1alpha1.Comparison{}
		r1 := &v1alpha1.Run{
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{Commit: "111111111111"},
			},
		}
		r2 := &v1alpha1.Run{
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{Commit: "222222222222"},
			},
		}
		sum := ComparisonSummary(comp, r1, r2)
		if sum.BaseCommitShort != "1111111" {
			t.Errorf("expected BaseCommitShort '1111111', got %q", sum.BaseCommitShort)
		}
		if sum.CandCommitShort != "2222222" {
			t.Errorf("expected CandCommitShort '2222222', got %q", sum.CandCommitShort)
		}
		if sum.Headline != "2222222 vs 1111111" {
			t.Errorf("expected headline '2222222 vs 1111111', got %q", sum.Headline)
		}
	})

	t.Run("CostAndPossibleSamples", func(t *testing.T) {
		cost := 4.10
		comp := &v1alpha1.Comparison{
			Status: v1alpha1.ComparisonStatus{
				CostUSD:         &cost,
				PossibleSamples: 3200,
				Tasks: []v1alpha1.ComparisonTaskStatus{
					{
						ID:         "task-1",
						Pairs:      48,
						SamplesRun: 48,
					},
				},
			},
		}
		baseRun := &v1alpha1.Run{
			Status: v1alpha1.RunStatus{
				Summary: &v1alpha1.RunSummary{TotalSamples: 48},
			},
		}
		candRun := &v1alpha1.Run{
			Status: v1alpha1.RunStatus{
				Summary: &v1alpha1.RunSummary{TotalSamples: 48},
			},
		}
		sum := ComparisonSummary(comp, baseRun, candRun)
		expected := "$4.10 spent, 48 of 3200 possible samples"
		if sum.SamplesSummary != expected {
			t.Errorf("expected SamplesSummary %q, got %q", expected, sum.SamplesSummary)
		}
	})

	t.Run("CostOnlyWithDropped", func(t *testing.T) {
		cost := 0.05
		comp := &v1alpha1.Comparison{
			Status: v1alpha1.ComparisonStatus{
				CostUSD: &cost,
				Tasks: []v1alpha1.ComparisonTaskStatus{
					{
						ID:    "task-1",
						Pairs: 10,
						Dropped: v1alpha1.ComparisonDropped{
							Candidate: 1,
							Baseline:  2,
						},
					},
				},
			},
		}
		sum := ComparisonSummary(comp, nil, nil)
		expected := "$0.05 spent, 23 run · 10 paired · dropped: 1 cand, 2 base"
		if sum.SamplesSummary != expected {
			t.Errorf("expected SamplesSummary %q, got %q", expected, sum.SamplesSummary)
		}
	})

	t.Run("PossibleSamplesOnly", func(t *testing.T) {
		comp := &v1alpha1.Comparison{
			Status: v1alpha1.ComparisonStatus{
				PossibleSamples: 100,
				Tasks: []v1alpha1.ComparisonTaskStatus{
					{
						ID:         "task-1",
						Pairs:      10,
						SamplesRun: 10,
					},
				},
			},
		}
		sum := ComparisonSummary(comp, nil, nil)
		expected := "10 of 100 possible samples"
		if sum.SamplesSummary != expected {
			t.Errorf("expected SamplesSummary %q, got %q", expected, sum.SamplesSummary)
		}
	})

	t.Run("StatusCostNilDoesNotResumTasks", func(t *testing.T) {
		taskCost := 0.50
		comp := &v1alpha1.Comparison{
			Status: v1alpha1.ComparisonStatus{
				CostUSD: nil, // Controller omitted total on purpose because another task had no price
				Tasks: []v1alpha1.ComparisonTaskStatus{
					{
						ID:      "task-1",
						Pairs:   10,
						CostUSD: &taskCost,
					},
					{
						ID:      "task-2",
						Pairs:   10,
						CostUSD: nil,
					},
				},
			},
		}
		sum := ComparisonSummary(comp, nil, nil)
		expected := "40 run · 20 paired"
		if sum.SamplesSummary != expected {
			t.Errorf("expected SamplesSummary %q without partial spent, got %q", expected, sum.SamplesSummary)
		}
		if sum.CostUSD != nil {
			t.Errorf("expected nil sum.CostUSD when status.CostUSD is nil, got %v", sum.CostUSD)
		}
	})
}

func TestTemplateRendering_SampleRange(t *testing.T) {
	t.Run("RunsList_SampleRange", func(t *testing.T) {
		view := RunsListView{
			ActiveTab: NavTabRuns,
			Namespace: "eval-runner",
			Runs: []RunListItem{
				{
					Name:        "test-range-run",
					Namespace:   "eval-runner",
					Phase:       v1alpha1.RunPhaseCompleted,
					PhaseChip:   "good",
					Tasks:       "1/1",
					Samples:     8,
					SampleRange: "samples 9-16",
					Rates:       "task=0.80",
					Commit:      "abc1234",
					Age:         "5m",
					Link:        "/runs/eval-runner/test-range-run",
				},
			},
		}

		var buf bytes.Buffer
		if err := tmplRunsList.Execute(&buf, view); err != nil {
			t.Fatalf("Template execution failed: %v", err)
		}
		output := buf.String()
		if !strings.Contains(output, `<td class="num">samples 9-16</td>`) {
			t.Errorf("Expected output to contain '<td class=\"num\">samples 9-16</td>', got:\n%s", output)
		}
	})

	t.Run("RunDetail_SampleRange", func(t *testing.T) {
		view := RunDetailView{
			ActiveTab:    NavTabRuns,
			Name:         "test-range-run",
			Namespace:    "eval-runner",
			Phase:        v1alpha1.RunPhaseCompleted,
			PhaseChip:    "good",
			TotalSamples: 8,
			SampleRange:  "samples 9-16",
			Tasks: []RunTaskRow{
				{
					ID:          "task-1",
					Phase:       v1alpha1.TaskPhaseCompleted,
					PhaseChip:   "good",
					Samples:     8,
					Successes:   7,
					SampleRange: "samples 9-16",
				},
			},
		}

		var buf bytes.Buffer
		if err := tmplRunDetail.Execute(&buf, view); err != nil {
			t.Fatalf("Template execution failed: %v", err)
		}
		output := buf.String()
		if !strings.Contains(output, "8 <span style=\"font-size:13px;font-weight:normal;color:var(--muted)\">(samples 9-16)</span>") {
			t.Errorf("Expected output to contain '8 (samples 9-16)' in tile, got:\n%s", output)
		}
		if !strings.Contains(output, "7/8 <span style=\"font-size:11px;color:var(--muted)\">(samples 9-16)</span>") {
			t.Errorf("Expected output to contain '7/8 (samples 9-16)' in table, got:\n%s", output)
		}
	})
}

func TestComparisonTable_Tokens(t *testing.T) {
	tasks := []ComparisonTaskRow{
		{
			ID:          "task-with-tokens",
			BaseRate:    "0.60",
			CandRate:    "0.80",
			Delta:       "+0.20",
			StatusClass: "good",
			Label:       "improved",
			Pairs:       16,
			SamplesRun:  16,
			Tokens: &v1alpha1.TaskTokens{
				Input:  500,
				Output: 200,
				Total:  700,
			},
		},
		{
			ID:          "task-without-tokens",
			BaseRate:    "0.50",
			CandRate:    "0.50",
			Delta:       "+0.00",
			StatusClass: "neutral",
			Label:       "unchanged",
			Pairs:       16,
			SamplesRun:  16,
			Tokens:      nil,
		},
		{
			ID:          "task-with-reasoning",
			BaseRate:    "0.70",
			CandRate:    "0.90",
			Delta:       "+0.20",
			StatusClass: "good",
			Label:       "improved",
			Pairs:       16,
			SamplesRun:  16,
			Tokens: &v1alpha1.TaskTokens{
				Input:     500,
				Output:    200,
				Total:     700,
				Reasoning: 80,
			},
		},
	}

	var buf bytes.Buffer
	if err := RenderComparisonTable(&buf, ComparisonTableView{Tasks: tasks}); err != nil {
		t.Fatalf("failed to render comparison table: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "<th>tokens</th>") {
		t.Errorf("expected <th>tokens</th> header, got:\n%s", output)
	}
	if !strings.Contains(output, "<th>samples</th>") {
		t.Errorf("expected <th>samples</th> header, got:\n%s", output)
	}
	if !strings.Contains(output, "<td class=\"num\">700</td>") {
		t.Errorf("expected total tokens 700 for task-with-tokens, got:\n%s", output)
	}
	if !strings.Contains(output, "<td class=\"num\">—</td>") {
		t.Errorf("expected '—' tokens for task-without-tokens, got:\n%s", output)
	}
	if !strings.Contains(output, "<td class=\"num\">700 (+R 80)</td>") {
		t.Errorf("expected total tokens '700 (+R 80)' for task-with-reasoning, got:\n%s", output)
	}
}

func TestComparisonDetail_BatchesAndActions(t *testing.T) {
	view := ComparisonDetailView{
		ActiveTab:   NavTabComparisons,
		Name:        "comp-batches-test",
		Namespace:   "eval-runner",
		Verdict:     v1alpha1.ComparisonVerdictRunning,
		VerdictChip: "pending",
		Headline:    "Running batch 2 (1 tasks continuing)",
		Tasks: []ComparisonTaskRow{
			{
				ID:          "task-decided",
				BaseRate:    "0.20",
				CandRate:    "0.90",
				Delta:       "+0.70",
				StatusClass: "good",
				Label:       "improved",
				Pairs:       8,
				SamplesRun:  8,
			},
			{
				ID:            "task-continuing",
				BaseRate:      "0.50",
				CandRate:      "0.50",
				Delta:         "+0.00",
				StatusClass:   "pending",
				Label:         "running",
				Pairs:         8,
				SamplesRun:    8,
				SuggestedNext: 16,
			},
		},
		Batches: []ComparisonBatchRow{
			{
				Index:        1,
				Phase:        v1alpha1.ComparisonPhaseCompleted,
				PhaseChip:    "good",
				SampleRange:  "1-8",
				CandidateRun: "cand-b1-run",
				BaselineRun:  "base-b1-run",
			},
			{
				Index:        2,
				Phase:        v1alpha1.ComparisonPhaseRunning,
				PhaseChip:    "pending",
				SampleRange:  "9-16",
				CandidateRun: "cand-b2-run",
				BaselineRun:  "base-b2-run",
			},
		},
		Actions: []ComparisonActionRow{
			{TaskID: "task-continuing", SuggestedNext: 16, ShowTaskID: true},
		},
	}

	var buf bytes.Buffer
	if err := tmplComparisonDetail.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute comparison detail template: %v", err)
	}
	output := buf.String()

	// Assert Batches section
	if !strings.Contains(output, "Batches</h2>") {
		t.Errorf("expected Batches heading, got:\n%s", output)
	}
	if !strings.Contains(output, "batch 1") || !strings.Contains(output, "batch 2") {
		t.Errorf("expected batch 1 and batch 2 in table, got:\n%s", output)
	}
	if !strings.Contains(output, "/runs/eval-runner/cand-b1-run") || !strings.Contains(output, "/runs/eval-runner/base-b1-run") {
		t.Errorf("expected batch run links, got:\n%s", output)
	}
	if !strings.Contains(output, "<span><b>Batches:</b> 2</span>") {
		t.Errorf("expected Batches footer count, got:\n%s", output)
	}

	// Assert Actions (extraSamples hint in real <ul>)
	if !strings.Contains(output, "<li>run 16 more samples (task-continuing)</li>") {
		t.Errorf("expected <li> action hint in detail view, got:\n%s", output)
	}
}

func TestComparisonActions(t *testing.T) {
	t.Run("NilComparison", func(t *testing.T) {
		actions := ComparisonActions(nil)
		if actions != nil {
			t.Errorf("expected nil actions for nil comp, got %v", actions)
		}
	})

	t.Run("NoInconclusiveTasks", func(t *testing.T) {
		comp := &v1alpha1.Comparison{
			Status: v1alpha1.ComparisonStatus{
				Tasks: []v1alpha1.ComparisonTaskStatus{
					{ID: "task-1", Label: v1alpha1.ComparisonTaskLabelImproved, SuggestedNext: 16},
					{ID: "task-2", Label: v1alpha1.ComparisonTaskLabelUnchanged},
				},
			},
		}
		actions := ComparisonActions(comp)
		if len(actions) != 0 {
			t.Errorf("expected 0 actions, got %d", len(actions))
		}
	})

	t.Run("SingleTaskInconclusive", func(t *testing.T) {
		comp := &v1alpha1.Comparison{
			Status: v1alpha1.ComparisonStatus{
				Tasks: []v1alpha1.ComparisonTaskStatus{
					{ID: "only-task", Label: v1alpha1.ComparisonTaskLabelInconclusive, SuggestedNext: 32},
				},
			},
		}
		actions := ComparisonActions(comp)
		if len(actions) != 1 {
			t.Fatalf("expected 1 action, got %d", len(actions))
		}
		if actions[0].ShowTaskID {
			t.Errorf("expected ShowTaskID to be false for single-task comparison")
		}
		if actions[0].SuggestedNext != 32 || actions[0].TaskID != "only-task" {
			t.Errorf("unexpected action: %+v", actions[0])
		}
	})

	t.Run("MultipleTasksInconclusive", func(t *testing.T) {
		comp := &v1alpha1.Comparison{
			Status: v1alpha1.ComparisonStatus{
				Tasks: []v1alpha1.ComparisonTaskStatus{
					{ID: "task-1", Label: v1alpha1.ComparisonTaskLabelImproved},
					{ID: "task-2", Label: v1alpha1.ComparisonTaskLabelInconclusive, SuggestedNext: 16},
					{ID: "task-3", Label: v1alpha1.ComparisonTaskLabelInconclusive, SuggestedNext: 0},
				},
			},
		}
		actions := ComparisonActions(comp)
		if len(actions) != 1 {
			t.Fatalf("expected 1 action, got %d", len(actions))
		}
		if !actions[0].ShowTaskID {
			t.Errorf("expected ShowTaskID to be true for multi-task comparison")
		}
		if actions[0].SuggestedNext != 16 || actions[0].TaskID != "task-2" {
			t.Errorf("unexpected action: %+v", actions[0])
		}
	})
}

func sampleSeriesListView() SeriesListView {
	return SeriesListView{
		ActiveTab: NavTabSeries,
		Namespace: "eval-runner",
		Series: []SeriesListRow{
			{
				Repository: "gke-labs/eval-runner",
				RepoHash:   "2cc24c2",
				Branch:     "main",
				RunCount:   42,
				Link:       "/series/2cc24c2/main",
			},
		},
		TotalCount: 1,
	}
}

func sampleSeriesDetailView() SeriesDetailView {
	rate1 := 0.75
	rate2 := 0.85
	cost1 := 0.05
	cost2 := 0.06
	t1, _ := time.Parse(time.RFC3339, "2026-10-20T01:00:00Z")
	t2, _ := time.Parse(time.RFC3339, "2026-10-20T02:00:00Z")

	items := []SeriesItem{
		{
			CommitSHA:      "1111111222222233333334444444555555556666",
			CommitSHAShort: "1111111",
			CommitTime:     t1,
			CommitTimeStr:  "2026-10-20T01:00:00Z",
			RunName:        "base-1111111",
			Namespace:      "eval-runner",
			Phase:          v1alpha1.TaskPhaseCompleted,
			PhaseChip:      "good",
			Samples:        "10",
			RateStr:        "0.75",
			CIStr:          "[0.60, 0.88]",
			TokensStr:      "1,200",
			CostStr:        "$0.05",
			RunLink:        "/runs/eval-runner/base-1111111",
			HasPoint:       true,
			SuccessRate:    &rate1,
			CI: &v1alpha1.ConfidenceInterval{
				Lower:      0.60,
				Upper:      0.88,
				Confidence: 0.95,
				Method:     "wilson",
			},
		},
		{
			CommitSHA:      "2222222333333344444445555555666666667777",
			CommitSHAShort: "2222222",
			CommitTime:     t2,
			CommitTimeStr:  "2026-10-20T02:00:00Z",
			RunName:        "base-2222222",
			Namespace:      "eval-runner",
			Phase:          v1alpha1.TaskPhaseCompleted,
			PhaseChip:      "good",
			Samples:        "10",
			RateStr:        "0.85",
			CIStr:          "[0.70, 0.94]",
			TokensStr:      "1,300",
			CostStr:        "$0.06",
			RunLink:        "/runs/eval-runner/base-2222222",
			HasPoint:       true,
			SuccessRate:    &rate2,
			CI: &v1alpha1.ConfidenceInterval{
				Lower:      0.70,
				Upper:      0.94,
				Confidence: 0.95,
				Method:     "wilson",
			},
		},
	}
	_ = cost1
	_ = cost2

	svg := BuildSeriesSVG(items)

	return SeriesDetailView{
		ActiveTab:    NavTabSeries,
		Namespace:    "eval-runner",
		Repository:   "gke-labs/eval-runner",
		RepoHash:     "2cc24c2",
		Branch:       "main",
		SelectedTask: "triage/runbook_lookup",
		Tasks: []SeriesTaskLink{
			{
				ID:     "triage/runbook_lookup",
				Link:   "/series/2cc24c2/main?task=triage%2Frunbook_lookup",
				Active: true,
			},
		},
		N:         50,
		SVG:       svg,
		Items:     items,
		TotalRuns: 2,
		BackLink:  "/series",
	}
}

func TestTemplateRenderingSeriesListGolden(t *testing.T) {
	view := sampleSeriesListView()

	var buf bytes.Buffer
	if err := tmplSeriesList.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute series list template: %v", err)
	}

	assertGolden(t, "series_list.golden.html", buf.String())
}

func TestTemplateRenderingSeriesDetailGolden(t *testing.T) {
	view := sampleSeriesDetailView()

	var buf bytes.Buffer
	if err := tmplSeriesDetail.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute series detail template: %v", err)
	}

	assertGolden(t, "series_detail.golden.html", buf.String())
}
func TestFormatLogURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "gs file",
			in:   "gs://my-bucket/runs/ee0a14f/task/inspect.json",
			want: "https://console.cloud.google.com/storage/browser/_details/my-bucket/runs/ee0a14f/task/inspect.json",
		},
		{
			name: "gs directory",
			in:   "gs://my-bucket/runs/ee0a14f/",
			want: "https://console.cloud.google.com/storage/browser/my-bucket/runs/ee0a14f/",
		},
		{
			name: "gs with special characters encoded",
			in:   "gs://my-bucket/runs/abc/task#frag?query%20space/inspect.json",
			want: "https://console.cloud.google.com/storage/browser/_details/my-bucket/runs/abc/task%23frag%3Fquery%2520space/inspect.json",
		},
		{
			name: "http url",
			in:   "http://localhost:8080/log.json",
			want: "http://localhost:8080/log.json",
		},
		{
			name: "https url",
			in:   "https://storage.googleapis.com/bucket/log.json",
			want: "https://storage.googleapis.com/bucket/log.json",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "local path",
			in:   "/var/log/eval/inspect.json",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatLogURL(tc.in)
			if got != tc.want {
				t.Errorf("FormatLogURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
