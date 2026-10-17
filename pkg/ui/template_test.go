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
	"math"
	"strings"
	"testing"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

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

func TestTemplateRenderingComparisonGolden(t *testing.T) {
	view := ComparisonDetailView{
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

	var buf bytes.Buffer
	if err := tmplComparisonDetail.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute comparison detail template: %v", err)
	}

	html := buf.String()

	// Verify golden elements
	goldenSnippets := []string{
		`<span class="chip good" style="font-size:16px;padding:4px 14px"><span class="dot"></span>IMPROVED</span>`,
		`<span class="verdict-headline">05ce4d8 vs a81d0c4 · P(improvement) = 0.98</span>`,
		`<th colspan="2">Δ (95% CI)</th>`,
		`<td class="mono" style="font-weight:600">triage/runbook_lookup</td>`,
		`<td class="num">0.61</td>`,
		`<td class="num">0.84</td>`,
		`<td class="num">+0.23</td>`,
		`<div class="zero" style="left:170px"></div>`,
		`<div class="range good-c" style="left:207.4px; width:81.6px"></div>`,
		`<div class="pt good-c" style="left:241.2px"></div>`,
		`<span class="chip good"><span class="dot"></span>improved</span>`,
		`<td class="num">`,
		`48`,
		`<b>Base:</b> commit <span class="mono">a81d0c4</span> (<a href="/runs/eval-runner/docs-eval-base-05ce4d8">docs-eval-base-05ce4d8</a>)`,
		`<b>Candidate:</b> commit <span class="mono">05ce4d8</span> (<a href="/runs/eval-runner/docs-eval-cand-05ce4d8">docs-eval-cand-05ce4d8</a>)`,
		`96 run · 48 paired`,
	}

	for _, snippet := range goldenSnippets {
		if !strings.Contains(html, snippet) {
			t.Errorf("missing golden snippet in rendered HTML:\nSnippet: %s\nFull HTML:\n%s", snippet, html)
		}
	}
}

func TestTemplateRenderingRunGolden(t *testing.T) {
	view := RunDetailView{
		ActiveTab:       NavTabRuns,
		Name:            "docs-eval-cand-05ce4d8",
		Namespace:       "eval-runner",
		Phase:           v1alpha1.RunPhaseCompleted,
		PhaseChip:       "good",
		RepoURL:         "https://github.com/gke-labs/eval-runner.git",
		CommitShort:     "05ce4d8",
		Model:           "google/gemini-3.5-flash-lite",
		Age:             "10m",
		ArtifactsPrefix: "gs://eval-bucket/runs/05ce4d8/",
		TasksSummary:    "1/1",
		TasksCompleted:  1,
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

	var buf bytes.Buffer
	if err := tmplRunDetail.Execute(&buf, view); err != nil {
		t.Fatalf("failed to execute run detail template: %v", err)
	}

	html := buf.String()

	goldenSnippets := []string{
		`<span class="mono">docs-eval-cand-05ce4d8</span>`,
		`<span class="chip good"><span class="dot"></span>Completed</span>`,
		`google/gemini-3.5-flash-lite`,
		`gs://eval-bucket/runs/05ce4d8/`,
		`<th colspan="2">95% CI (0-1 axis)</th>`,
		`<td class="mono" style="font-weight:600">triage/runbook_lookup</td>`,
		`<td class="num">0.84</td>`,
		`[0.71, 0.92]`,
		`<div class="cibar-01"`,
		`<div class="tick-0"></div>`,
		`<div class="tick-50"></div>`,
		`<div class="tick-100"></div>`,
		`<div class="range" style="left:170.4px; width:50.4px"></div>`,
		`<div class="pt" style="left:194.6px"></div>`,
		`40/48`,
		`wilson`,
		`<span class="log-path mono" title="gs://eval-bucket/runs/05ce4d8/triage/runbook_lookup/inspect.json">gs://eval-bucket/runs/05ce4d8/triage/runbook_lookup/inspect.json</span>`,
	}

	for _, snippet := range goldenSnippets {
		if !strings.Contains(html, snippet) {
			t.Errorf("missing golden snippet in rendered HTML:\nSnippet: %s", snippet)
		}
	}
}
