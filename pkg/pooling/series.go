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

package pooling

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/stats"
)

const minusSign = "\u2212"

// RunCommitTime returns the commit time from the Run's annotations,
// falling back to CreationTimestamp.
func RunCommitTime(run *v1alpha1.Run) (time.Time, bool) {
	if run == nil {
		return time.Time{}, true
	}
	if run.Annotations != nil {
		if raw, ok := run.Annotations["evalrunner.gke-labs.dev/commit-time"]; ok && raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return run.CreationTimestamp.Time, true
			}
			return t, false
		}
	}
	return run.CreationTimestamp.Time, true
}

// signedFixed formats v with decimals and an explicit + or unicode minus sign.
func signedFixed(v float64, decimals int) (string, int) {
	s := strconv.FormatFloat(math.Abs(v), 'f', decimals, 64)
	if f, err := strconv.ParseFloat(s, 64); err == nil && f == 0 {
		return s, 0
	}
	if v > 0 {
		return "+" + s, 1
	}
	return minusSign + s, -1
}

// DeltaChipClass maps a confidence interval to a CSS chip class.
func DeltaChipClass(ci stats.Interval) string {
	if ci.Lower > 0 {
		return "good"
	}
	if ci.Upper < 0 {
		return "bad"
	}
	return "neutral"
}

// FormatDeltaCI formats a delta point estimate and confidence interval,
// e.g. "Δ −0.05 [−0.21, +0.11]".
func FormatDeltaCI(delta, lower, upper float64) string {
	dStr, _ := signedFixed(delta, 2)
	lStr, _ := signedFixed(lower, 2)
	uStr, _ := signedFixed(upper, 2)
	return fmt.Sprintf("Δ %s [%s, %s]", dStr, lStr, uStr)
}

// FormatMethodTooltip formats a statistical method for a UI tooltip.
func FormatMethodTooltip(method string) string {
	switch method {
	case "newcombe":
		return "Newcombe hybrid Wilson interval (unpaired; different sample windows)"
	case "welch":
		return "Welch t interval from (mean, sd, n) (unpaired)"
	case "pooled-wilson", "pooled_wilson":
		return "Pooled Wilson score interval over summed successes and samples across cohort tasks"
	case "paired-t":
		return "Paired Student t-interval (95%)"
	case "unpaired-welch":
		return "Unpaired Welch t-interval (95%)"
	case "bootstrap":
		return "Percentile bootstrap interval (95%)"
	default:
		return method
	}
}

// FormatSignedInt formats an integer with explicit + or unicode minus sign.
func FormatSignedInt(v int) string {
	if v >= 0 {
		return fmt.Sprintf("+%d", v)
	}
	return fmt.Sprintf("%s%d", minusSign, -v)
}

// FormatTitle formats the check-run title for a branch head evaluation:
// e.g. "63% ± 21 · n=16 · Δ vs parent +2 (95% CI −9…+13)"
func FormatTitle(est *stats.Estimate, lastMove *stats.DeltaEstimate) string {
	if est == nil {
		return ""
	}
	pct := int(math.Round(est.Point * 100))
	halfWidth := int(math.Round(((est.CI.Upper - est.CI.Lower) / 2.0) * 100))
	title := fmt.Sprintf("%d%% ± %d · n=%d", pct, halfWidth, est.N)
	if lastMove != nil {
		deltaPct := int(math.Round(lastMove.Delta * 100))
		lowerPct := int(math.Round(lastMove.CI.Lower * 100))
		upperPct := int(math.Round(lastMove.CI.Upper * 100))
		title += fmt.Sprintf(" · Δ vs parent %s (95%% CI %s…%s)",
			FormatSignedInt(deltaPct),
			FormatSignedInt(lowerPct),
			FormatSignedInt(upperPct),
		)
	}
	return title
}

// EstimateFromTaskStatus converts a TaskStatus into a stats.Estimate.
func EstimateFromTaskStatus(t *v1alpha1.TaskStatus) stats.Estimate {
	if t == nil || t.SuccessRate == nil {
		return stats.Estimate{}
	}
	est := stats.Estimate{
		Point: *t.SuccessRate,
		N:     t.Samples,
		SD:    t.SuccessRateSD,
	}
	if t.SuccessRateCI != nil {
		est.CI = stats.Interval{Lower: t.SuccessRateCI.Lower, Upper: t.SuccessRateCI.Upper}
		est.Confidence = t.SuccessRateCI.Confidence
		est.Method = t.SuccessRateCI.Method
	}
	return est
}

// ComputeSeriesLastMove computes the pooled delta between the latest commit and its predecessor
// in the series, matching the controller and UI definitions across top-ups.
func ComputeSeriesLastMove(runs []*v1alpha1.Run) (*stats.DeltaEstimate, string, string, string) {
	if len(runs) < 2 {
		return nil, "—", "", ""
	}

	type commitGroup struct {
		sha        string
		commitTime time.Time
		runs       []*v1alpha1.Run
	}
	commitMap := make(map[string]*commitGroup)
	for _, r := range runs {
		if r == nil {
			continue
		}
		sha := RunCommitSHA(r)
		if sha == "" {
			continue
		}
		cg, ok := commitMap[sha]
		if !ok {
			ct, _ := RunCommitTime(r)
			cg = &commitGroup{
				sha:        sha,
				commitTime: ct,
			}
			commitMap[sha] = cg
		}
		cg.runs = append(cg.runs, r)
	}

	var groups []*commitGroup
	for _, cg := range commitMap {
		groups = append(groups, cg)
	}
	if len(groups) < 2 {
		return nil, "—", "", ""
	}

	sort.Slice(groups, func(i, j int) bool {
		if !groups[i].commitTime.Equal(groups[j].commitTime) {
			return groups[i].commitTime.Before(groups[j].commitTime)
		}
		return groups[i].sha < groups[j].sha
	})

	// Collect unique task IDs
	taskSet := make(map[string]bool)
	for _, cg := range groups {
		for _, r := range cg.runs {
			for _, t := range r.Status.Tasks {
				if t.ID != "" {
					taskSet[t.ID] = true
				}
			}
			for _, tid := range r.Spec.Tasks {
				if tid != "" {
					taskSet[tid] = true
				}
			}
		}
	}
	var allTaskIDs []string
	for tid := range taskSet {
		allTaskIDs = append(allTaskIDs, tid)
	}
	sort.Strings(allTaskIDs)

	var prevPool *CommitPool
	var lastMove *stats.DeltaEstimate

	for _, cg := range groups {
		pool := PoolRunsForCommit(cg.runs)
		if pool.OverallEstimate != nil {
			if prevPool != nil && prevPool.OverallEstimate != nil {
				delta := ComputeCommitDelta(prevPool, pool)
				lastMove = delta
			}
			prevPool = pool
		}
	}

	if lastMove == nil {
		return nil, "—", "", ""
	}

	chip := DeltaChipClass(lastMove.CI)
	str := FormatDeltaCI(lastMove.Delta, lastMove.CI.Lower, lastMove.CI.Upper)
	tooltip := FormatMethodTooltip(lastMove.Method)
	if len(allTaskIDs) == 1 {
		tooltip = fmt.Sprintf("%s · %s", allTaskIDs[0], FormatMethodTooltip(lastMove.Method))
	}
	return lastMove, str, chip, tooltip
}
