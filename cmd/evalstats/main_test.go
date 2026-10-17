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

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/stats"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEvalRecordParsing(t *testing.T) {
	line := `{"task": "triage/runbook_lookup", "sample_id": "sample-1", "base_score": 0.0, "pr_score": 1.0}`
	var rec EvalRecord
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("Failed to unmarshal EvalRecord: %v", err)
	}

	if rec.Task != "triage/runbook_lookup" {
		t.Errorf("Task = %s, want triage/runbook_lookup", rec.Task)
	}
	if rec.SampleID != "sample-1" {
		t.Errorf("SampleID = %s, want sample-1", rec.SampleID)
	}
	if rec.BaseScore == nil || *rec.BaseScore != 0.0 {
		t.Errorf("BaseScore = %v, want 0.0", rec.BaseScore)
	}
	if rec.PRScore == nil || *rec.PRScore != 1.0 {
		t.Errorf("PRScore = %v, want 1.0", rec.PRScore)
	}
}

func TestJSONLStreamProcessing(t *testing.T) {
	jsonlData := `
{"task": "triage/runbook_lookup", "sample_id": "s1", "base_score": 0.0, "pr_score": 1.0}
{"task": "triage/runbook_lookup", "sample_id": "s2", "base_score": 1.0, "pr_score": 1.0}
{"task": "triage/escalation_policy", "sample_id": "e1", "base_score": 1.0, "pr_score": 1.0}
{"task": "triage/escalation_policy", "sample_id": "e2", "base_score": 0.0, "pr_score": 0.0}
`

	tasksCand := make(map[string][]stats.SampleScore)
	tasksBase := make(map[string][]stats.SampleScore)

	lines := strings.Split(strings.TrimSpace(jsonlData), "\n")
	for _, l := range lines {
		var rec EvalRecord
		if err := json.Unmarshal([]byte(l), &rec); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}
		if rec.PRScore != nil {
			tasksCand[rec.Task] = append(tasksCand[rec.Task], stats.SampleScore{ID: rec.SampleID, Score: *rec.PRScore})
		}
		if rec.BaseScore != nil {
			tasksBase[rec.Task] = append(tasksBase[rec.Task], stats.SampleScore{ID: rec.SampleID, Score: *rec.BaseScore})
		}
	}

	if len(tasksCand["triage/runbook_lookup"]) != 2 {
		t.Errorf("Expected 2 candidate samples for runbook_lookup, got %d", len(tasksCand["triage/runbook_lookup"]))
	}
	if len(tasksCand["triage/escalation_policy"]) != 2 {
		t.Errorf("Expected 2 candidate samples for escalation_policy, got %d", len(tasksCand["triage/escalation_policy"]))
	}

	estEscalation := stats.ComparePaired(tasksCand["triage/escalation_policy"], tasksBase["triage/escalation_policy"], 0.95)
	if estEscalation.MeanDelta != 0.0 {
		t.Errorf("Escalation mean delta = %v, want 0.0", estEscalation.MeanDelta)
	}

	// For n=2 < MinPairsToDecide (8), stopping rule requires more data and continues.
	decEscalation := stats.Decide(estEscalation, 0.05, 2, 10, 0, 10)
	if decEscalation.Label != stats.TaskLabelContinue {
		t.Errorf("Escalation label for n=2 = %v, want continue (< MinPairsToDecide)", decEscalation.Label)
	}

	// When cap is reached on unresolved data (e.g. -final mode), label is inconclusive.
	decFinal := stats.Decide(estEscalation, 0.05, 2, 2, 0, 10)
	if decFinal.Label != stats.TaskLabelInconclusive {
		t.Errorf("Escalation label for capped run = %v, want inconclusive", decFinal.Label)
	}

	// With n >= MinPairsToDecide (8) tied pairs, the task resolves to unchanged.
	cand8 := make([]stats.SampleScore, 8)
	base8 := make([]stats.SampleScore, 8)
	for i := 0; i < 8; i++ {
		cand8[i] = stats.SampleScore{ID: string(rune('a' + i)), Score: 1.0}
		base8[i] = stats.SampleScore{ID: string(rune('a' + i)), Score: 1.0}
	}
	est8 := stats.ComparePaired(cand8, base8, 0.95)
	dec8 := stats.Decide(est8, 0.05, 8, 10, 0, 10)
	if dec8.Label != stats.TaskLabelUnchanged {
		t.Errorf("Escalation label for n=8 = %v, want unchanged", dec8.Label)
	}
}

func TestPrintRunTable(t *testing.T) {
	rate1 := 1.00
	rate2 := 0.15
	rate3 := 0.50
	meanSuccess := 0.55

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gemini-docs-run",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "docs/readme_comprehension",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     20,
					Successes:   20,
					SuccessRate: &rate1,
					SuccessRateCI: &v1alpha1.ConfidenceInterval{
						Lower:      0.84,
						Upper:      1.00,
						Confidence: 0.95,
						Method:     "wilson",
					},
				},
				{
					ID:          "docs/readme_comprehension_blind",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     20,
					Successes:   3,
					SuccessRate: &rate2,
					SuccessRateCI: &v1alpha1.ConfidenceInterval{
						Lower:      0.05,
						Upper:      0.36,
						Confidence: 0.95,
						Method:     "wilson",
					},
				},
				{
					ID:          "task/partial",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     10,
					Successes:   0,
					SuccessRate: &rate3,
					SuccessRateCI: &v1alpha1.ConfidenceInterval{
						Lower:      0.20,
						Upper:      0.80,
						Confidence: 0.95,
						Method:     "t",
					},
				},
				{
					ID:      "task/failed",
					Phase:   v1alpha1.TaskPhaseFailed,
					Samples: 0,
					Message: "some failure",
				},
			},
			Summary: &v1alpha1.RunSummary{
				Tasks:           "3/4",
				TasksCompleted:  3,
				TasksFailed:     1,
				TasksTotal:      4,
				TotalSamples:    50,
				MeanSuccessRate: &meanSuccess,
				SuccessRateRange: &v1alpha1.ScoreRange{
					Min: 0.15,
					Max: 1.00,
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := PrintRunTable(&buf, run); err != nil {
		t.Fatalf("PrintRunTable failed: %v", err)
	}

	out := buf.String()
	// Check headers
	if !strings.Contains(out, "TASK") || !strings.Contains(out, "RATE (95% CI)") || !strings.Contains(out, "METHOD") {
		t.Errorf("Expected table headers in output, got:\n%s", out)
	}
	// Check acceptance criteria example line
	if !strings.Contains(out, "docs/readme_comprehension") || !strings.Contains(out, "1.00 [0.84, 1.00]") || !strings.Contains(out, "wilson") {
		t.Errorf("Expected docs/readme_comprehension line in output, got:\n%s", out)
	}
	if !strings.Contains(out, "docs/readme_comprehension_blind") || !strings.Contains(out, "0.15 [0.05, 0.36]") {
		t.Errorf("Expected docs/readme_comprehension_blind line in output, got:\n%s", out)
	}
	if !strings.Contains(out, "task/partial") || !strings.Contains(out, "0.50 [0.20, 0.80]") || !strings.Contains(out, "t") {
		t.Errorf("Expected task/partial line with method t in output, got:\n%s", out)
	}
	if !strings.Contains(out, "task/failed") || !strings.Contains(out, "failed") {
		t.Errorf("Expected task/failed in output, got:\n%s", out)
	}
	// Check summary
	if !strings.Contains(out, "Tasks: 3 completed, 1 failed (4 total) · Total samples: 50") {
		t.Errorf("Expected summary counts in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Mean success rate: 0.55 (range 0.15-1.00)") {
		t.Errorf("Expected mean success rate in output, got:\n%s", out)
	}
}

func TestPrintRunJSON(t *testing.T) {
	rate := 1.00
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name: "run-json",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "docs/readme_comprehension",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     20,
					Successes:   20,
					SuccessRate: &rate,
					SuccessRateCI: &v1alpha1.ConfidenceInterval{
						Lower:      0.84,
						Upper:      1.00,
						Confidence: 0.95,
						Method:     "wilson",
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := PrintRunJSON(&buf, run); err != nil {
		t.Fatalf("PrintRunJSON failed: %v", err)
	}

	var parsed SingleRunOutput
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Failed to unmarshal JSON output: %v", err)
	}
	if parsed.Run != "run-json" {
		t.Errorf("Expected Run 'run-json', got %s", parsed.Run)
	}
	if len(parsed.Tasks) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(parsed.Tasks))
	}
	if parsed.Tasks[0].CI == nil || parsed.Tasks[0].CI.Method != "wilson" {
		t.Errorf("Expected CI method wilson, got %+v", parsed.Tasks[0].CI)
	}
}

func TestPrintComparisonTable(t *testing.T) {
	base1, pr1, d1 := 0.61, 0.84, 0.23
	base2, pr2, d2 := 0.88, 0.90, 0.02
	base3, pr3, d3 := 0.72, 0.75, 0.03
	pImp := 0.98

	comp := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-comparison",
		},
		Spec: v1alpha1.ComparisonSpec{
			Baseline:  v1alpha1.ComparisonRunRef{Run: "main-run"},
			Candidate: v1alpha1.ComparisonRunRef{Run: "pr-418-run"},
		},
		Status: v1alpha1.ComparisonStatus{
			Phase:        v1alpha1.ComparisonPhaseCompleted,
			Verdict:      v1alpha1.ComparisonVerdictImproved,
			PImprovement: &pImp,
			Message:      "IMPROVED: 1 task(s) improved, 1 task(s) unchanged; net improvement probable",
			Tasks: []v1alpha1.ComparisonTaskStatus{
				{
					ID:            "triage/runbook_lookup",
					BaseRate:      &base1,
					CandidateRate: &pr1,
					Delta:         &d1,
					DeltaCI: &v1alpha1.DeltaConfidenceInterval{
						Lower: 0.11,
						Upper: 0.35,
					},
					Pairs: 48,
					Label: v1alpha1.ComparisonTaskLabelImproved,
				},
				{
					ID:            "triage/escalation_policy",
					BaseRate:      &base2,
					CandidateRate: &pr2,
					Delta:         &d2,
					DeltaCI: &v1alpha1.DeltaConfidenceInterval{
						Lower: -0.05,
						Upper: 0.09,
					},
					Pairs: 32,
					Label: v1alpha1.ComparisonTaskLabelUnchanged,
				},
				{
					ID:            "triage/summary_quality",
					BaseRate:      &base3,
					CandidateRate: &pr3,
					Delta:         &d3,
					DeltaCI: &v1alpha1.DeltaConfidenceInterval{
						Lower: -0.08,
						Upper: 0.14,
					},
					Pairs: 16,
					Label: v1alpha1.ComparisonTaskLabelInconclusive,
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := PrintComparisonTable(&buf, comp); err != nil {
		t.Fatalf("PrintComparisonTable failed: %v", err)
	}

	out := buf.String()
	// Check headers
	if !strings.Contains(out, "eval") || !strings.Contains(out, "base") || !strings.Contains(out, "this PR") ||
		!strings.Contains(out, "Δ (95% CI)") || !strings.Contains(out, "label") || !strings.Contains(out, "runs") {
		t.Errorf("Expected table headers matching PR comment mockup, got:\n%s", out)
	}

	// Check rows
	if !strings.Contains(out, "triage/runbook_lookup") || !strings.Contains(out, "0.61") || !strings.Contains(out, "0.84") ||
		!strings.Contains(out, "+0.23 [+0.11, +0.35]") || !strings.Contains(out, "improved") || !strings.Contains(out, "48") {
		t.Errorf("Expected triage/runbook_lookup row in output, got:\n%s", out)
	}
	if !strings.Contains(out, "triage/escalation_policy") || !strings.Contains(out, "+0.02 [-0.05, +0.09]") || !strings.Contains(out, "unchanged") || !strings.Contains(out, "32") {
		t.Errorf("Expected triage/escalation_policy row in output, got:\n%s", out)
	}
	if !strings.Contains(out, "triage/summary_quality") || !strings.Contains(out, "+0.03 [-0.08, +0.14]") || !strings.Contains(out, "inconclusive") || !strings.Contains(out, "16") {
		t.Errorf("Expected triage/summary_quality row in output, got:\n%s", out)
	}

	// Check verdict
	if !strings.Contains(out, "Verdict: IMPROVED · P(improvement) = 0.98") {
		t.Errorf("Expected verdict line in output, got:\n%s", out)
	}
}

func TestPrintComparisonJSON(t *testing.T) {
	d := 0.0
	pImp := 0.50
	comp := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name: "comp-json",
		},
		Spec: v1alpha1.ComparisonSpec{
			Baseline:  v1alpha1.ComparisonRunRef{Run: "base-run"},
			Candidate: v1alpha1.ComparisonRunRef{Run: "cand-run"},
		},
		Status: v1alpha1.ComparisonStatus{
			Phase:        v1alpha1.ComparisonPhaseCompleted,
			Verdict:      v1alpha1.ComparisonVerdictUnchanged,
			PImprovement: &pImp,
			SummaryPath:  "gs://bucket/comparisons/comp-json.json",
			Tasks: []v1alpha1.ComparisonTaskStatus{
				{
					ID:    "docs/readme_comprehension",
					Delta: &d,
					DeltaCI: &v1alpha1.DeltaConfidenceInterval{
						Lower: 0.0,
						Upper: 0.0,
					},
					Pairs: 20,
					Label: v1alpha1.ComparisonTaskLabelUnchanged,
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := PrintComparisonJSON(&buf, comp); err != nil {
		t.Fatalf("PrintComparisonJSON failed: %v", err)
	}

	var parsed ComparisonOutput
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Failed to unmarshal JSON output: %v", err)
	}
	if parsed.Comparison != "comp-json" {
		t.Errorf("Expected Comparison 'comp-json', got %s", parsed.Comparison)
	}
	if parsed.Verdict != "UNCHANGED" {
		t.Errorf("Expected Verdict 'UNCHANGED', got %s", parsed.Verdict)
	}
	if parsed.PImprovement == nil || *parsed.PImprovement != 0.50 {
		t.Errorf("Expected PImprovement 0.50, got %v", parsed.PImprovement)
	}
	if len(parsed.Tasks) != 1 || parsed.Tasks[0].Label != v1alpha1.ComparisonTaskLabelUnchanged {
		t.Errorf("Expected 1 unchanged task, got %+v", parsed.Tasks)
	}
}
