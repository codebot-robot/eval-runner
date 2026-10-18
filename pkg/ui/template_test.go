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
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/pmezard/go-difflib/difflib"
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
				ID:        "triage/runbook_lookup",
				Phase:     v1alpha1.TaskPhaseCompleted,
				PhaseChip: "good",
				RateStr:   "0.84",
				HasCI:     true,
				CILeftPx:  170.4,
				CIWidthPx: 50.4,
				CIDotPx:   194.6,
				CILabel:   "[0.71, 0.92]",
				Samples:   48,
				Successes: 40,
				Method:    "wilson",
				LogPath:   "gs://eval-bucket/runs/05ce4d8/triage/runbook_lookup/inspect.json",
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

func TestTemplateRenderingComparisonTableGolden(t *testing.T) {
	tasks := sampleComparisonDetailView().Tasks

	var buf bytes.Buffer
	if err := RenderComparisonTable(&buf, tasks); err != nil {
		t.Fatalf("failed to execute comparison table sub-template: %v", err)
	}

	assertGolden(t, "comparison_table.golden.html", buf.String())
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
		if err := RenderComparisonTable(&buf, tasks); err != nil {
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
}
