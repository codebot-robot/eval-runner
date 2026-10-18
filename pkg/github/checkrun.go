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

package github

import (
	"fmt"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

// CheckRunName is the standardized check run name for eval verdicts.
const CheckRunName = "eval-runner / evals"

// CheckRunState represents the payload state of a GitHub Check Run.
type CheckRunState struct {
	Name       string
	Status     string // "in_progress" or "completed"
	Conclusion string // "success", "neutral", "failure", or "" (when in_progress)
	Title      string // short state summary
	Summary    string // detailed summary text
}

// PlannedSamples returns the planned total samples for a Run, computed as limit x tasks.
// If limit or task count cannot be determined, it returns 0.
func PlannedSamples(run *v1alpha1.Run) int {
	if run == nil {
		return 0
	}
	limit := 0
	if run.Spec.Limit != nil && *run.Spec.Limit > 0 {
		limit = *run.Spec.Limit
	}
	if limit <= 0 {
		return 0
	}
	tasks := 0
	if len(run.Spec.Tasks) > 0 {
		tasks = len(run.Spec.Tasks)
	} else if run.Status.Discovery != nil && run.Status.Discovery.EligibleTaskCount > 0 {
		tasks = run.Status.Discovery.EligibleTaskCount
	} else if len(run.Status.Tasks) > 0 {
		tasks = len(run.Status.Tasks)
	}
	return limit * tasks
}

// CheckRunStateForComparison maps a Comparison and its candidate/baseline Runs
// to the CheckRunState for the "eval-runner / evals" check run across all 5 verdicts.
func CheckRunStateForComparison(comp *v1alpha1.Comparison, baseRun, candRun *v1alpha1.Run) CheckRunState {
	state := CheckRunState{
		Name: CheckRunName,
	}
	if comp == nil {
		state.Status = "in_progress"
		state.Title = "running"
		state.Summary = "running"
		return state
	}

	switch comp.Status.Verdict {
	case v1alpha1.ComparisonVerdictImproved:
		state.Status = "completed"
		state.Conclusion = "success"
		if comp.Status.PImprovement != nil {
			state.Title = fmt.Sprintf("IMPROVED · P(improvement) = %.2f", *comp.Status.PImprovement)
		} else {
			state.Title = "IMPROVED"
		}
		state.Summary = state.Title

	case v1alpha1.ComparisonVerdictUnchanged:
		state.Status = "completed"
		state.Conclusion = "success"
		state.Title = "UNCHANGED"
		state.Summary = state.Title

	case v1alpha1.ComparisonVerdictInconclusive:
		state.Status = "completed"
		state.Conclusion = "neutral"
		state.Title = "INCONCLUSIVE · more samples recommended"
		state.Summary = state.Title

	case v1alpha1.ComparisonVerdictRegressed:
		state.Status = "completed"
		state.Conclusion = "failure"
		state.Title = "REGRESSED"
		state.Summary = state.Title

	case v1alpha1.ComparisonVerdictRunning:
		fallthrough
	default:
		state.Status = "in_progress"
		state.Conclusion = ""

		// Calculate N (completed samples) and M (total planned samples: limit x tasks)
		completed := 0
		total := 0
		if candRun != nil {
			if candRun.Status.Summary != nil {
				completed += candRun.Status.Summary.TotalSamples
			}
			total += PlannedSamples(candRun)
		}
		if baseRun != nil {
			if baseRun.Status.Summary != nil {
				completed += baseRun.Status.Summary.TotalSamples
			}
			total += PlannedSamples(baseRun)
		}

		if total > 0 {
			state.Title = fmt.Sprintf("running · %d of %d samples", completed, total)
		} else if completed > 0 {
			state.Title = fmt.Sprintf("running · %d samples", completed)
		} else {
			state.Title = "running"
		}
		state.Summary = state.Title
	}

	if comp.Status.Message != "" {
		state.Summary = EscapeCell(comp.Status.Message)
	}

	return state
}
