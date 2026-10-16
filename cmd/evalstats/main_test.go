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
	"encoding/json"
	"strings"
	"testing"

	"github.com/gke-labs/eval-runner/pkg/stats"
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
