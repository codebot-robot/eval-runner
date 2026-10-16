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

package stats

import (
	"math"
	"testing"
)

func TestVerdictTableDriven(t *testing.T) {
	// Acceptance criterion:
	// Stopping rule and verdict tables covered by table-driven tests,
	// including the RUNNING and mixed inconclusive+improved cases.
	tests := []struct {
		name            string
		labels          []TaskLabel
		expectedVerdict VerdictType
	}{
		{
			name:            "empty labels",
			labels:          []TaskLabel{},
			expectedVerdict: VerdictInconclusive,
		},
		{
			name:            "single improved",
			labels:          []TaskLabel{TaskLabelImproved},
			expectedVerdict: VerdictImproved,
		},
		{
			name:            "single regressed",
			labels:          []TaskLabel{TaskLabelRegressed},
			expectedVerdict: VerdictRegressed,
		},
		{
			name:            "single unchanged",
			labels:          []TaskLabel{TaskLabelUnchanged},
			expectedVerdict: VerdictUnchanged,
		},
		{
			name:            "all unchanged",
			labels:          []TaskLabel{TaskLabelUnchanged, TaskLabelUnchanged, TaskLabelUnchanged},
			expectedVerdict: VerdictUnchanged,
		},
		{
			name:            "improved and unchanged",
			labels:          []TaskLabel{TaskLabelImproved, TaskLabelUnchanged, TaskLabelUnchanged},
			expectedVerdict: VerdictImproved,
		},
		{
			name:            "improved and regressed",
			labels:          []TaskLabel{TaskLabelImproved, TaskLabelRegressed},
			expectedVerdict: VerdictRegressed,
		},
		{
			name:            "inconclusive and regressed",
			labels:          []TaskLabel{TaskLabelInconclusive, TaskLabelRegressed},
			expectedVerdict: VerdictRegressed,
		},
		{
			name:            "mixed inconclusive and improved",
			labels:          []TaskLabel{TaskLabelImproved, TaskLabelInconclusive},
			expectedVerdict: VerdictInconclusive,
		},
		{
			name:            "mixed inconclusive and unchanged",
			labels:          []TaskLabel{TaskLabelUnchanged, TaskLabelInconclusive},
			expectedVerdict: VerdictInconclusive,
		},
		{
			name:            "all inconclusive",
			labels:          []TaskLabel{TaskLabelInconclusive, TaskLabelInconclusive},
			expectedVerdict: VerdictInconclusive,
		},
		{
			name:            "running single",
			labels:          []TaskLabel{TaskLabelRunning},
			expectedVerdict: VerdictRunning,
		},
		{
			name:            "running with improved",
			labels:          []TaskLabel{TaskLabelImproved, TaskLabelRunning},
			expectedVerdict: VerdictRunning,
		},
		{
			name:            "running with inconclusive",
			labels:          []TaskLabel{TaskLabelInconclusive, TaskLabelRunning},
			expectedVerdict: VerdictRunning,
		},
		{
			name:            "queued treated as running",
			labels:          []TaskLabel{TaskLabelQueued, TaskLabelUnchanged},
			expectedVerdict: VerdictRunning,
		},
		{
			name:            "continue treated as running",
			labels:          []TaskLabel{TaskLabelContinue, TaskLabelImproved},
			expectedVerdict: VerdictRunning,
		},
		{
			name:            "running takes precedence over regressed while sampling",
			labels:          []TaskLabel{TaskLabelRunning, TaskLabelRegressed},
			expectedVerdict: VerdictRunning,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Verdict(tc.labels)
			if got.Verdict != tc.expectedVerdict {
				t.Errorf("Verdict(%v) = %v, want %v", tc.labels, got.Verdict, tc.expectedVerdict)
			}
			if got.PImprovement != 0.0 {
				t.Errorf("Verdict(%v) returned hardcoded P %v, want 0.0 (AggregateVerdict must be used)", tc.labels, got.PImprovement)
			}
		})
	}
}

func TestAggregateVerdictWithProbabilities(t *testing.T) {
	// Concept worked example (PR comment mockup):
	// triage/runbook_lookup: improved (P = 0.99)
	// triage/escalation_policy: unchanged (P = 0.55)
	// triage/summary_quality: unchanged (P = 0.50)
	tasksImproved := []TaskReport{
		{
			ID:           "triage/runbook_lookup",
			Label:        TaskLabelImproved,
			PImprovement: 0.99,
		},
		{
			ID:           "triage/escalation_policy",
			Label:        TaskLabelUnchanged,
			PImprovement: 0.55,
		},
		{
			ID:           "triage/summary_quality",
			Label:        TaskLabelUnchanged,
			PImprovement: 0.50,
		},
	}

	vImproved := AggregateVerdict(tasksImproved)
	if vImproved.Verdict != VerdictImproved {
		t.Errorf("Verdict = %v, want IMPROVED", vImproved.Verdict)
	}
	// With triage/runbook_lookup P=0.99 (floor 0.999) and unchanged tasks having floor 0.975,
	// min(0.99, 0.975) evaluates to exactly 0.975.
	if math.Abs(vImproved.PImprovement-0.975) > 1e-6 {
		t.Errorf("Headline P(improvement) = %v, want 0.975", vImproved.PImprovement)
	}

	// Concept mixed case: improved + inconclusive
	tasksMixed := []TaskReport{
		{
			ID:           "triage/runbook_lookup",
			Label:        TaskLabelImproved,
			PImprovement: 0.98,
		},
		{
			ID:           "triage/escalation_policy",
			Label:        TaskLabelUnchanged,
			PImprovement: 0.52,
		},
		{
			ID:           "triage/summary_quality",
			Label:        TaskLabelInconclusive,
			PImprovement: 0.60,
		},
	}
	vMixed := AggregateVerdict(tasksMixed)
	if vMixed.Verdict != VerdictInconclusive {
		t.Errorf("Mixed improved+inconclusive verdict = %v, want INCONCLUSIVE", vMixed.Verdict)
	}

	// Case with regression: headline collapses
	tasksRegressed := []TaskReport{
		{
			ID:           "triage/runbook_lookup",
			Label:        TaskLabelImproved,
			PImprovement: 0.99,
		},
		{
			ID:           "triage/escalation_policy",
			Label:        TaskLabelRegressed,
			PImprovement: 0.04,
		},
	}
	vRegressed := AggregateVerdict(tasksRegressed)
	if vRegressed.Verdict != VerdictRegressed {
		t.Errorf("Verdict = %v, want REGRESSED", vRegressed.Verdict)
	}
	if math.Abs(vRegressed.PImprovement-0.04) > 1e-6 {
		t.Errorf("PImprovement with regression = %v, want 0.04", vRegressed.PImprovement)
	}
}
