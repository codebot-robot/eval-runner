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
	"math"
	"sort"
	"strconv"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

// CSS classes for run deltas. They encode the direction of a change only. Statistical
// significance is carried by the verdict chip, so deltas never use chip or badge styling.
const (
	deltaClassGood    = "delta-good"
	deltaClassBad     = "delta-bad"
	deltaClassZero    = "delta-zero"
	deltaClassMissing = "delta-missing"
)

// minusSign is U+2212, used for negative deltas so the sign is as wide as "+".
const minusSign = "\u2212"

// MetricDelta is one signed change from the baseline Run to the candidate Run.
type MetricDelta struct {
	// Text is the display value, e.g. "+6.7 pp", "−3.2%" or "+$0.07 (+1.8%)", or "—" when
	// one or both Runs lack the data.
	Text string

	// Title describes the inputs behind Text, e.g. both totals and the number of shared tasks.
	Title string

	// Class is the CSS class for the direction of the change: delta-good, delta-bad,
	// delta-zero or delta-missing.
	Class string
}

// RunDeltas holds the Quality, Tokens and Cost changes between the baseline and candidate
// Runs of a Comparison.
type RunDeltas struct {
	Quality MetricDelta
	Tokens  MetricDelta
	Cost    MetricDelta
}

func missingDelta(title string) MetricDelta {
	return MetricDelta{Text: "—", Title: title, Class: deltaClassMissing}
}

// ComputeRunDeltas compares candRun with baseRun over the task IDs that both Runs report in
// status.tasks. When comp lists tasks in status.tasks, only those task IDs are considered.
//
//   - Quality is the mean candidate success rate minus the mean baseline success rate, in
//     percentage points, over shared tasks that have a rate in both Runs. When the Runs have
//     no such tasks (for example, a Run was deleted), it falls back to the Comparison's
//     per-task baseRate and candidateRate.
//   - Tokens is the relative change in the sum of tasks[].tokens.total over shared tasks that
//     record tokens in both Runs.
//   - Cost is the absolute and relative change in the sum of tasks[].costUSD over shared tasks
//     that record a cost in both Runs, falling back to each Run's status.costUSD.
//
// Higher quality and lower tokens or cost are classed delta-good.
func ComputeRunDeltas(comp *v1alpha1.Comparison, baseRun, candRun *v1alpha1.Run) RunDeltas {
	base := tasksByID(baseRun)
	cand := tasksByID(candRun)
	ids := sharedTaskIDs(comp, base, cand)
	return RunDeltas{
		Quality: qualityDelta(comp, ids, base, cand),
		Tokens:  tokensDelta(ids, base, cand),
		Cost:    costDelta(ids, base, cand, baseRun, candRun),
	}
}

func tasksByID(run *v1alpha1.Run) map[string]v1alpha1.TaskStatus {
	if run == nil {
		return nil
	}
	tasks := make(map[string]v1alpha1.TaskStatus, len(run.Status.Tasks))
	for _, t := range run.Status.Tasks {
		tasks[t.ID] = t
	}
	return tasks
}

// sharedTaskIDs returns the task IDs present in both base and cand. When comp lists tasks,
// the result keeps the Comparison's task order and drops IDs missing from either Run;
// otherwise the IDs are sorted.
func sharedTaskIDs(comp *v1alpha1.Comparison, base, cand map[string]v1alpha1.TaskStatus) []string {
	seen := make(map[string]bool)
	var ids []string
	add := func(id string) {
		if seen[id] {
			return
		}
		if _, ok := base[id]; !ok {
			return
		}
		if _, ok := cand[id]; !ok {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if comp != nil && len(comp.Status.Tasks) > 0 {
		for _, t := range comp.Status.Tasks {
			add(t.ID)
		}
		return ids
	}
	for id := range base {
		add(id)
	}
	sort.Strings(ids)
	return ids
}

func taskRate(t v1alpha1.TaskStatus) *float64 {
	if t.SuccessRate != nil {
		return t.SuccessRate
	}
	return t.MeanScore
}

func pluralTasks(n int) string {
	if n == 1 {
		return "1 shared task"
	}
	return fmt.Sprintf("%d shared tasks", n)
}

func qualityDelta(comp *v1alpha1.Comparison, ids []string, base, cand map[string]v1alpha1.TaskStatus) MetricDelta {
	var sumBase, sumCand float64
	n := 0
	for _, id := range ids {
		b, c := taskRate(base[id]), taskRate(cand[id])
		if b == nil || c == nil {
			continue
		}
		sumBase += *b
		sumCand += *c
		n++
	}
	source := "Run success rates"
	if n == 0 && comp != nil {
		for _, t := range comp.Status.Tasks {
			if t.BaseRate == nil || t.CandidateRate == nil {
				continue
			}
			sumBase += *t.BaseRate
			sumCand += *t.CandidateRate
			n++
		}
		source = "Comparison task rates"
	}
	if n == 0 {
		return missingDelta("No shared task has a success rate in both Runs")
	}
	meanBase := sumBase / float64(n) * 100
	meanCand := sumCand / float64(n) * 100
	text, sign := signedFixed(meanCand-meanBase, 1)
	return MetricDelta{
		Text:  text + " pp",
		Title: fmt.Sprintf("Mean success rate %.1f%% → %.1f%% over %s (%s)", meanBase, meanCand, pluralTasks(n), source),
		Class: directionClass(sign, true),
	}
}

func tokensDelta(ids []string, base, cand map[string]v1alpha1.TaskStatus) MetricDelta {
	var sumBase, sumCand int64
	n := 0
	for _, id := range ids {
		b, c := base[id].Tokens, cand[id].Tokens
		if b == nil || c == nil {
			continue
		}
		sumBase += int64(b.Total)
		sumCand += int64(c.Total)
		n++
	}
	if n == 0 {
		return missingDelta("Token counts are missing from one or both Runs")
	}
	diff := sumCand - sumBase
	absText := signedCompactCount(diff)
	title := fmt.Sprintf("%s tokens (%s → %s) over %s", absText, compactCount(sumBase), compactCount(sumCand), pluralTasks(n))
	if sumBase == 0 {
		// The relative change is undefined, so show the absolute difference instead.
		sign := 0
		switch {
		case diff > 0:
			sign = 1
		case diff < 0:
			sign = -1
		}
		if sign == 0 {
			return MetricDelta{Text: "0.0%", Title: title, Class: deltaClassZero}
		}
		return MetricDelta{Text: absText, Title: title, Class: directionClass(sign, false)}
	}
	text, sign := signedFixed(float64(diff)/float64(sumBase)*100, 1)
	return MetricDelta{Text: text + "%", Title: title, Class: directionClass(sign, false)}
}

func costDelta(ids []string, base, cand map[string]v1alpha1.TaskStatus, baseRun, candRun *v1alpha1.Run) MetricDelta {
	var sumBase, sumCand float64
	n := 0
	for _, id := range ids {
		b, c := base[id].CostUSD, cand[id].CostUSD
		if b == nil || c == nil {
			continue
		}
		sumBase += *b
		sumCand += *c
		n++
	}
	basis := "over " + pluralTasks(n)
	if n == 0 {
		if baseRun == nil || candRun == nil || baseRun.Status.CostUSD == nil || candRun.Status.CostUSD == nil {
			return missingDelta("Cost is missing from one or both Runs")
		}
		sumBase, sumCand = *baseRun.Status.CostUSD, *candRun.Status.CostUSD
		basis = "from Run totals"
	}
	diff := sumCand - sumBase
	text, sign := signedUSD(diff)
	if sumBase > 0 {
		pct, _ := signedFixed(diff/sumBase*100, 1)
		text += " (" + pct + "%)"
	}
	return MetricDelta{
		Text:  text,
		Title: fmt.Sprintf("$%.4f → $%.4f %s", sumBase, sumCand, basis),
		Class: directionClass(sign, false),
	}
}

// directionClass maps the sign of a change to a CSS class. higherIsBetter selects whether an
// increase (quality) or a decrease (tokens, cost) is classed delta-good.
func directionClass(sign int, higherIsBetter bool) string {
	switch {
	case sign == 0:
		return deltaClassZero
	case (sign > 0) == higherIsBetter:
		return deltaClassGood
	default:
		return deltaClassBad
	}
}

// signedFixed formats v with the given number of decimals and an explicit sign. Values that
// round to zero are formatted without a sign. It returns the text and the sign of the
// rounded value (-1, 0 or 1).
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

// signedUSD formats a dollar difference with an explicit sign, using cents for differences
// of at least $0.01 and four decimals below that. Differences that round to zero format as
// "$0.00".
func signedUSD(d float64) (string, int) {
	decimals := 2
	if math.Abs(d) < 0.01 {
		decimals = 4
	}
	s, sign := signedFixed(d, decimals)
	switch sign {
	case 0:
		return "$0.00", 0
	case 1:
		return "+$" + s[1:], 1
	default:
		return minusSign + "$" + s[len(minusSign):], -1
	}
}

// compactCount formats a token count in compact units: "999", "12.3K", "0.18M" or "1.20B".
// Counts from 100,000 upward are shown in millions. The sign is dropped.
func compactCount(n int64) string {
	a := n
	if a < 0 {
		a = -a
	}
	switch {
	case a >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(a)/1e9)
	case a >= 100_000:
		return fmt.Sprintf("%.2fM", float64(a)/1e6)
	case a >= 1_000:
		return fmt.Sprintf("%.1fK", float64(a)/1e3)
	default:
		return strconv.FormatInt(a, 10)
	}
}

// signedCompactCount formats a token difference in compact units with an explicit sign.
func signedCompactCount(d int64) string {
	switch {
	case d > 0:
		return "+" + compactCount(d)
	case d < 0:
		return minusSign + compactCount(d)
	default:
		return "0"
	}
}
