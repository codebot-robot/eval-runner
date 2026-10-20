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
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// LabelVsBaseline marks a Comparison whose baseline Run is the fixed reference Run
	// for a series of candidate Runs. The /comparisons page groups Comparisons that carry
	// this label with the value "true" by candidate Run.
	LabelVsBaseline = "evalrunner.gke-labs.dev/vs-baseline"

	// LabelVsPrevious marks a Comparison whose baseline Run is the Run that preceded the
	// candidate in its series. A Comparison may carry both LabelVsBaseline and LabelVsPrevious.
	LabelVsPrevious = "evalrunner.gke-labs.dev/vs-previous"

	// reasonMaxRunes is the display length of the reason line on /comparisons. The full
	// text is kept in a title attribute.
	reasonMaxRunes = 140

	// regressionSummaryTasks is the number of regressed tasks named in a derived reason.
	regressionSummaryTasks = 3
)

// ComparisonKind selects which labeled Comparisons the /comparisons page shows.
type ComparisonKind string

const (
	// ComparisonKindAll shows labeled Comparisons grouped by candidate Run and the flat list
	// of Comparisons that carry neither label.
	ComparisonKindAll ComparisonKind = ""

	// ComparisonKindBaseline shows only Comparisons labeled with LabelVsBaseline.
	ComparisonKindBaseline ComparisonKind = "baseline"

	// ComparisonKindPrevious shows only Comparisons labeled with LabelVsPrevious.
	ComparisonKindPrevious ComparisonKind = "previous"
)

// ParseComparisonKind maps the value of the ?kind= query parameter to a ComparisonKind.
// Unknown values select ComparisonKindAll.
func ParseComparisonKind(s string) ComparisonKind {
	switch ComparisonKind(strings.ToLower(strings.TrimSpace(s))) {
	case ComparisonKindBaseline:
		return ComparisonKindBaseline
	case ComparisonKindPrevious:
		return ComparisonKindPrevious
	default:
		return ComparisonKindAll
	}
}

// kindLabelKey returns the label that selects Comparisons of kind, or "" for ComparisonKindAll.
func kindLabelKey(kind ComparisonKind) string {
	switch kind {
	case ComparisonKindBaseline:
		return LabelVsBaseline
	case ComparisonKindPrevious:
		return LabelVsPrevious
	default:
		return ""
	}
}

// comparisonsKindLink builds the /comparisons URL that selects kind. It keeps the namespace
// and limit query parameters and drops the offset so that the filtered view starts on page one.
func comparisonsKindLink(query url.Values, kind ComparisonKind) string {
	q := make(url.Values)
	for _, key := range []string{"namespace", "limit"} {
		if v := query.Get(key); v != "" {
			q.Set(key, v)
		}
	}
	if kind != ComparisonKindAll {
		q.Set("kind", string(kind))
	}
	if encoded := q.Encode(); encoded != "" {
		return "/comparisons?" + encoded
	}
	return "/comparisons"
}

func hasTrueLabel(comp *v1alpha1.Comparison, key string) bool {
	return strings.EqualFold(strings.TrimSpace(comp.Labels[key]), "true")
}

// IsVsBaseline reports whether comp carries LabelVsBaseline with the value "true".
func IsVsBaseline(comp *v1alpha1.Comparison) bool {
	return comp != nil && hasTrueLabel(comp, LabelVsBaseline)
}

// IsVsPrevious reports whether comp carries LabelVsPrevious with the value "true".
func IsVsPrevious(comp *v1alpha1.Comparison) bool {
	return comp != nil && hasTrueLabel(comp, LabelVsPrevious)
}

// hasLabeledComparison reports whether any Comparison carries LabelVsBaseline or LabelVsPrevious.
func hasLabeledComparison(items []v1alpha1.Comparison) bool {
	for i := range items {
		if IsVsBaseline(&items[i]) || IsVsPrevious(&items[i]) {
			return true
		}
	}
	return false
}

// FormatUTCTimestamp formats t as an absolute UTC time such as "2026-10-20 10:35 UTC",
// or returns "—" when t is nil or zero.
func FormatUTCTimestamp(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return "—"
	}
	return formatUTC(t.Time)
}

func formatUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04") + " UTC"
}

// TruncateAtWord collapses runs of whitespace to single spaces and shortens s to at most
// maxRunes runes, including a trailing "…" when text was removed. When a space falls within
// the last 20 runes before the limit, the cut happens there so that words stay whole.
// Values of maxRunes below 2 return the collapsed string unchanged.
func TruncateAtWord(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if maxRunes < 2 || len(runes) <= maxRunes {
		return s
	}
	limit := maxRunes - 1 // leave room for the ellipsis
	cut := limit
	for i := limit; i > 0 && i >= limit-20; i-- {
		if runes[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(runes[:cut]), " ,;:") + "…"
}

// evaluatedTaskCount counts the tasks of comp that were not skipped.
func evaluatedTaskCount(comp *v1alpha1.Comparison) int {
	if comp == nil {
		return 0
	}
	n := 0
	for _, t := range comp.Status.Tasks {
		if t.Label != v1alpha1.ComparisonTaskLabelSkipped {
			n++
		}
	}
	return n
}

// RegressedTasks returns the tasks of comp labeled regressed, ordered by delta from most
// negative to least negative. Tasks without a delta sort last, and ties sort by task ID.
func RegressedTasks(comp *v1alpha1.Comparison) []v1alpha1.ComparisonTaskStatus {
	if comp == nil {
		return nil
	}
	var regressed []v1alpha1.ComparisonTaskStatus
	for _, t := range comp.Status.Tasks {
		if t.Label == v1alpha1.ComparisonTaskLabelRegressed {
			regressed = append(regressed, t)
		}
	}
	sort.SliceStable(regressed, func(i, j int) bool {
		di, dj := regressed[i].Delta, regressed[j].Delta
		switch {
		case di != nil && dj != nil && *di != *dj:
			return *di < *dj
		case di != nil && dj == nil:
			return true
		case di == nil && dj != nil:
			return false
		}
		return regressed[i].ID < regressed[j].ID
	})
	return regressed
}

// formatRatePercent formats a 0-1 rate as a whole percentage such as "60%", or "—" when unset.
func formatRatePercent(r *float64) string {
	if r == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", *r*100)
}

// RegressionFallback summarizes a REGRESSED Comparison from its task results as
// "Regressed on N of M tasks: <id> (base% → cand%), ...". It names up to three regressed
// tasks with the most negative delta. N counts tasks labeled regressed and M counts tasks
// that were not skipped. It returns "" for other verdicts and when no task is labeled regressed.
func RegressionFallback(comp *v1alpha1.Comparison) string {
	if comp == nil || comp.Status.Verdict != v1alpha1.ComparisonVerdictRegressed {
		return ""
	}
	regressed := RegressedTasks(comp)
	if len(regressed) == 0 {
		return ""
	}
	named := regressed
	if len(named) > regressionSummaryTasks {
		named = named[:regressionSummaryTasks]
	}
	parts := make([]string, 0, len(named))
	for _, t := range named {
		parts = append(parts, fmt.Sprintf("%s (%s → %s)", t.ID, formatRatePercent(t.BaseRate), formatRatePercent(t.CandidateRate)))
	}
	return fmt.Sprintf("Regressed on %d of %d tasks: %s", len(regressed), evaluatedTaskCount(comp), strings.Join(parts, ", "))
}

// ComparisonReason returns the reason line for comp. It uses status.message when set and
// otherwise RegressionFallback. For a message with line breaks, display is its first line
// (the headline); otherwise display is the whole message collapsed to one line. display is
// truncated near reasonMaxRunes runes either way, and full is the untruncated text for a
// title attribute. Both are empty when there is nothing to show.
func ComparisonReason(comp *v1alpha1.Comparison) (display, full string) {
	if comp == nil {
		return "", ""
	}
	full = strings.TrimSpace(normalizeNewlines(comp.Status.Message))
	if full == "" {
		full = RegressionFallback(comp)
	}
	if full == "" {
		return "", ""
	}
	if strings.Contains(full, "\n") {
		return TruncateAtWord(ReasonHeadline(full), reasonMaxRunes), full
	}
	return TruncateAtWord(full, reasonMaxRunes), full
}

// RegressedTaskRow is one task labeled regressed on the Comparison detail page.
type RegressedTaskRow struct {
	ID       string
	BaseRate string // e.g. "0.60"
	CandRate string // e.g. "0.20"
	Delta    string // e.g. "-0.40"
	CILabel  string // e.g. "[-0.50, -0.30]", or "—" when the task has no delta CI
}

// RegressedTaskRows lists the regressed tasks of comp, most negative delta first, using the
// same number formats as the task comparison table.
func RegressedTaskRows(comp *v1alpha1.Comparison) []RegressedTaskRow {
	tasks := RegressedTasks(comp)
	if len(tasks) == 0 {
		return nil
	}
	rows := make([]RegressedTaskRow, 0, len(tasks))
	for _, t := range tasks {
		row := RegressedTaskRow{ID: t.ID, BaseRate: "—", CandRate: "—", Delta: "—", CILabel: "—"}
		if t.BaseRate != nil {
			row.BaseRate = fmt.Sprintf("%.2f", *t.BaseRate)
		}
		if t.CandidateRate != nil {
			row.CandRate = fmt.Sprintf("%.2f", *t.CandidateRate)
		}
		if t.Delta != nil {
			row.Delta = fmt.Sprintf("%+.2f", *t.Delta)
		}
		if t.DeltaCI != nil {
			row.CILabel = fmt.Sprintf("[%+.2f, %+.2f]", t.DeltaCI.Lower, t.DeltaCI.Upper)
		}
		rows = append(rows, row)
	}
	return rows
}

func formatPImprovement(p *float64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f", *p)
}

func comparisonLink(comp *v1alpha1.Comparison) string {
	return fmt.Sprintf("/comparisons/%s/%s", comp.Namespace, comp.Name)
}

func runLink(namespace, name string) string {
	return fmt.Sprintf("/runs/%s/%s", namespace, name)
}

// runsFor returns the baseline and candidate Runs of comp from runs, or nil for Runs that
// are not present.
func runsFor(comp *v1alpha1.Comparison, runs map[string]*v1alpha1.Run) (baseRun, candRun *v1alpha1.Run) {
	if comp == nil || runs == nil {
		return nil, nil
	}
	return runs[comp.Spec.Baseline.Run], runs[comp.Spec.Candidate.Run]
}

// NewComparisonListItem builds the flat /comparisons row for comp. baseRun and candRun are
// the Runs named in comp.Spec and may be nil when they are not found.
func NewComparisonListItem(comp *v1alpha1.Comparison, baseRun, candRun *v1alpha1.Run) ComparisonListItem {
	tasks := "—"
	if comp.Status.Summary != nil && comp.Status.Summary.Tasks != "" {
		tasks = comp.Status.Summary.Tasks
	}
	reason, reasonFull := ComparisonReason(comp)
	return ComparisonListItem{
		Name:         comp.Name,
		Namespace:    comp.Namespace,
		Verdict:      comp.Status.Verdict,
		VerdictChip:  verdictChipClass(comp.Status.Verdict),
		PImprovement: formatPImprovement(comp.Status.PImprovement),
		Tasks:        tasks,
		BaselineRun:  comp.Spec.Baseline.Run,
		CandidateRun: comp.Spec.Candidate.Run,
		Completed:    FormatUTCTimestamp(comp.Status.CompletionTime),
		Age:          FormatAge(comp.CreationTimestamp),
		Link:         comparisonLink(comp),
		Reason:       reason,
		ReasonFull:   reasonFull,
		Deltas:       ComputeRunDeltas(comp, baseRun, candRun),
	}
}

// ComparisonRef links to a Comparison by name.
type ComparisonRef struct {
	Name string
	Link string
}

// ComparisonCell summarizes the newest Comparison of one kind for a candidate Run in the
// grouped /comparisons view.
type ComparisonCell struct {
	Name         string
	Link         string
	Verdict      v1alpha1.ComparisonVerdict
	VerdictChip  string
	PImprovement string
	Completed    string // e.g. "2026-10-20 10:35 UTC", or "—"
	BaselineRun  string
	BaselineLink string
	Reason       string
	ReasonFull   string
	Deltas       RunDeltas

	// Older lists earlier Comparisons of the same kind for the same candidate Run, newest first.
	Older []ComparisonRef
}

// ComparisonGroupRow is one candidate Run in the grouped /comparisons view.
type ComparisonGroupRow struct {
	CandidateRun  string
	CandidateLink string

	// Created is the creation time of the candidate Run, or "—" when the Run was not found.
	Created string

	VsBaseline *ComparisonCell
	VsPrevious *ComparisonCell
}

// comparisonListRow is one paginated row of /comparisons: either all labeled Comparisons
// of one candidate Run, or a single Comparison that carries neither label.
type comparisonListRow struct {
	namespace string
	candidate string
	baseline  []*v1alpha1.Comparison // labeled vs-baseline, newest first
	previous  []*v1alpha1.Comparison // labeled vs-previous, newest first
	single    *v1alpha1.Comparison

	sortTime time.Time
	sortName string
}

func (r comparisonListRow) isGroup() bool {
	return r.single == nil
}

// sortComparisonsNewestFirst orders comps by creation time, newest first, then by name.
func sortComparisonsNewestFirst(comps []*v1alpha1.Comparison) {
	sort.SliceStable(comps, func(i, j int) bool {
		if !comps[i].CreationTimestamp.Equal(&comps[j].CreationTimestamp) {
			return comps[i].CreationTimestamp.After(comps[j].CreationTimestamp.Time)
		}
		return comps[i].Name < comps[j].Name
	})
}

// comparisonListRows orders the Comparisons for /comparisons. Comparisons labeled
// vs-baseline or vs-previous collapse into one row per candidate Run, ordered by the
// creation time of that Run, or by the newest member Comparison when the Run is not in runs.
// Comparisons that carry neither label keep one row each, ordered by their own creation
// time, and are left out when kind is not ComparisonKindAll. Rows are ordered newest first.
// total counts the Comparisons that the rows cover.
func comparisonListRows(items []v1alpha1.Comparison, kind ComparisonKind, runs map[string]*v1alpha1.Run) (rows []comparisonListRow, total int) {
	groups := make(map[string]*comparisonListRow)
	var groupOrder []string

	for i := range items {
		comp := &items[i]
		vsBaseline, vsPrevious := IsVsBaseline(comp), IsVsPrevious(comp)
		if !vsBaseline && !vsPrevious {
			if kind == ComparisonKindAll {
				rows = append(rows, comparisonListRow{
					namespace: comp.Namespace,
					single:    comp,
					sortTime:  comp.CreationTimestamp.Time,
					sortName:  comp.Name,
				})
				total++
			}
			continue
		}

		inBaseline := vsBaseline && kind != ComparisonKindPrevious
		inPrevious := vsPrevious && kind != ComparisonKindBaseline
		if !inBaseline && !inPrevious {
			continue
		}
		total++

		candidate := comp.Spec.Candidate.Run
		group, ok := groups[candidate]
		if !ok {
			group = &comparisonListRow{namespace: comp.Namespace, candidate: candidate, sortName: candidate}
			groups[candidate] = group
			groupOrder = append(groupOrder, candidate)
		}
		if inBaseline {
			group.baseline = append(group.baseline, comp)
		}
		if inPrevious {
			group.previous = append(group.previous, comp)
		}
		if comp.CreationTimestamp.After(group.sortTime) {
			group.sortTime = comp.CreationTimestamp.Time
		}
	}

	for _, candidate := range groupOrder {
		group := groups[candidate]
		sortComparisonsNewestFirst(group.baseline)
		sortComparisonsNewestFirst(group.previous)
		if run, ok := runs[candidate]; ok && run != nil && !run.CreationTimestamp.IsZero() {
			group.sortTime = run.CreationTimestamp.Time
		}
		rows = append(rows, *group)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].sortTime.Equal(rows[j].sortTime) {
			return rows[i].sortTime.After(rows[j].sortTime)
		}
		if rows[i].isGroup() != rows[j].isGroup() {
			return rows[i].isGroup()
		}
		return rows[i].sortName < rows[j].sortName
	})
	return rows, total
}

// newComparisonCell summarizes comps[0] and lists the rest as older Comparisons. It returns
// nil when comps is empty.
func newComparisonCell(comps []*v1alpha1.Comparison, runs map[string]*v1alpha1.Run) *ComparisonCell {
	if len(comps) == 0 {
		return nil
	}
	comp := comps[0]
	baseRun, candRun := runsFor(comp, runs)
	reason, reasonFull := ComparisonReason(comp)
	cell := &ComparisonCell{
		Name:         comp.Name,
		Link:         comparisonLink(comp),
		Verdict:      comp.Status.Verdict,
		VerdictChip:  verdictChipClass(comp.Status.Verdict),
		PImprovement: formatPImprovement(comp.Status.PImprovement),
		Completed:    FormatUTCTimestamp(comp.Status.CompletionTime),
		BaselineRun:  comp.Spec.Baseline.Run,
		Reason:       reason,
		ReasonFull:   reasonFull,
		Deltas:       ComputeRunDeltas(comp, baseRun, candRun),
	}
	if comp.Spec.Baseline.Run != "" {
		cell.BaselineLink = runLink(comp.Namespace, comp.Spec.Baseline.Run)
	}
	for _, older := range comps[1:] {
		cell.Older = append(cell.Older, ComparisonRef{Name: older.Name, Link: comparisonLink(older)})
	}
	return cell
}

// groupRow builds the grouped view row for a group comparisonListRow.
func (r comparisonListRow) groupRow(runs map[string]*v1alpha1.Run) ComparisonGroupRow {
	created := "—"
	if run, ok := runs[r.candidate]; ok && run != nil && !run.CreationTimestamp.IsZero() {
		created = FormatUTCTimestamp(&run.CreationTimestamp)
	}
	return ComparisonGroupRow{
		CandidateRun:  r.candidate,
		CandidateLink: runLink(r.namespace, r.candidate),
		Created:       created,
		VsBaseline:    newComparisonCell(r.baseline, runs),
		VsPrevious:    newComparisonCell(r.previous, runs),
	}
}

// listItem builds the flat list row for a single comparisonListRow.
func (r comparisonListRow) listItem(runs map[string]*v1alpha1.Run) ComparisonListItem {
	baseRun, candRun := runsFor(r.single, runs)
	return NewComparisonListItem(r.single, baseRun, candRun)
}
