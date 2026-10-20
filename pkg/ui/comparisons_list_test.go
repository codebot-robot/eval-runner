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
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func floatPtr(v float64) *float64 { return &v }

func TestFormatUTCTimestamp(t *testing.T) {
	toronto := time.FixedZone("EDT", -4*60*60)
	tests := []struct {
		name string
		in   *metav1.Time
		want string
	}{
		{name: "nil", in: nil, want: "—"},
		{name: "zero", in: &metav1.Time{}, want: "—"},
		{name: "utc", in: &metav1.Time{Time: time.Date(2026, 10, 20, 10, 35, 14, 0, time.UTC)}, want: "2026-10-20 10:35 UTC"},
		{name: "converted from another zone", in: &metav1.Time{Time: time.Date(2026, 10, 20, 6, 35, 59, 0, toronto)}, want: "2026-10-20 10:35 UTC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatUTCTimestamp(tt.in); got != tt.want {
				t.Errorf("FormatUTCTimestamp() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateAtWord(t *testing.T) {
	t.Run("short text is returned with whitespace collapsed", func(t *testing.T) {
		if got := TruncateAtWord("  two\n tasks\tregressed  ", 140); got != "two tasks regressed" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("text at the limit is kept", func(t *testing.T) {
		in := strings.Repeat("a", 140)
		if got := TruncateAtWord(in, 140); got != in {
			t.Errorf("expected text of exactly 140 runes to be kept, got %d runes", utf8.RuneCountInString(got))
		}
	})

	t.Run("long text is cut at a word boundary", func(t *testing.T) {
		in := strings.Repeat("word ", 40) // 200 runes
		got := TruncateAtWord(in, 140)
		if !strings.HasSuffix(got, "word…") {
			t.Errorf("expected cut after a whole word, got %q", got)
		}
		if n := utf8.RuneCountInString(got); n > 140 {
			t.Errorf("expected at most 140 runes, got %d", n)
		}
	})

	t.Run("long text without spaces is cut hard", func(t *testing.T) {
		got := TruncateAtWord(strings.Repeat("é", 200), 140)
		if n := utf8.RuneCountInString(got); n != 140 {
			t.Errorf("expected 140 runes, got %d", n)
		}
		if !strings.HasSuffix(got, "é…") {
			t.Errorf("expected ellipsis after a whole rune, got %q", got)
		}
	})

	t.Run("trailing punctuation before the cut is dropped", func(t *testing.T) {
		in := "Regressed on 5 of 21 tasks: alpha (60% → 20%), " + strings.Repeat("b", 30) + " (60% → 20%), gamma"
		got := TruncateAtWord(in, 60)
		if strings.Contains(got, ",…") {
			t.Errorf("expected trailing comma to be trimmed, got %q", got)
		}
	})
}

func TestRegressedTasksAndFallback(t *testing.T) {
	comp := &v1alpha1.Comparison{
		Status: v1alpha1.ComparisonStatus{
			Verdict: v1alpha1.ComparisonVerdictRegressed,
			Tasks: []v1alpha1.ComparisonTaskStatus{
				{ID: "prompt-11", BaseRate: floatPtr(0.6), CandidateRate: floatPtr(0.4), Delta: floatPtr(-0.2), Label: v1alpha1.ComparisonTaskLabelRegressed},
				{ID: "prompt-13", BaseRate: floatPtr(0.0), CandidateRate: floatPtr(0.2), Delta: floatPtr(0.2), Label: v1alpha1.ComparisonTaskLabelImproved},
				{ID: "prompt-14", BaseRate: floatPtr(0.6), CandidateRate: floatPtr(0.2), Delta: floatPtr(-0.4), Label: v1alpha1.ComparisonTaskLabelRegressed},
				{ID: "prompt-15", BaseRate: floatPtr(1.0), CandidateRate: floatPtr(0.8), Delta: floatPtr(-0.2), Label: v1alpha1.ComparisonTaskLabelRegressed},
				{ID: "prompt-18", BaseRate: floatPtr(0.6), CandidateRate: floatPtr(0.2), Delta: floatPtr(-0.4), Label: v1alpha1.ComparisonTaskLabelRegressed},
				{ID: "prompt-02", BaseRate: floatPtr(1.0), CandidateRate: floatPtr(1.0), Delta: floatPtr(0), Label: v1alpha1.ComparisonTaskLabelUnchanged},
				{ID: "prompt-30", Label: v1alpha1.ComparisonTaskLabelSkipped},
				{ID: "prompt-31", Label: v1alpha1.ComparisonTaskLabelRegressed}, // no delta: sorts last
			},
		},
	}

	var ids []string
	for _, task := range RegressedTasks(comp) {
		ids = append(ids, task.ID)
	}
	if got, want := strings.Join(ids, ","), "prompt-14,prompt-18,prompt-11,prompt-15,prompt-31"; got != want {
		t.Errorf("RegressedTasks order = %s, want %s", got, want)
	}

	want := "Regressed on 5 of 7 tasks: prompt-14 (60% → 20%), prompt-18 (60% → 20%), prompt-11 (60% → 40%)"
	if got := RegressionFallback(comp); got != want {
		t.Errorf("RegressionFallback() =\n  %q\nwant\n  %q", got, want)
	}

	rows := RegressedTaskRows(comp)
	if len(rows) != 5 {
		t.Fatalf("expected 5 regressed task rows, got %d", len(rows))
	}
	if rows[0].ID != "prompt-14" || rows[0].BaseRate != "0.60" || rows[0].CandRate != "0.20" || rows[0].Delta != "-0.40" || rows[0].CILabel != "—" {
		t.Errorf("unexpected first regressed row: %+v", rows[0])
	}
	if last := rows[4]; last.ID != "prompt-31" || last.BaseRate != "—" || last.Delta != "—" {
		t.Errorf("unexpected regressed row without rates: %+v", last)
	}

	t.Run("other verdicts have no fallback", func(t *testing.T) {
		improved := comp.DeepCopy()
		improved.Status.Verdict = v1alpha1.ComparisonVerdictInconclusive
		if got := RegressionFallback(improved); got != "" {
			t.Errorf("expected no fallback for INCONCLUSIVE, got %q", got)
		}
	})

	t.Run("regressed verdict without regressed tasks has no fallback", func(t *testing.T) {
		none := &v1alpha1.Comparison{Status: v1alpha1.ComparisonStatus{
			Verdict: v1alpha1.ComparisonVerdictRegressed,
			Tasks:   []v1alpha1.ComparisonTaskStatus{{ID: "a", Label: v1alpha1.ComparisonTaskLabelUnchanged}},
		}}
		if got := RegressionFallback(none); got != "" {
			t.Errorf("expected no fallback, got %q", got)
		}
	})
}

func TestComparisonReason(t *testing.T) {
	long := "Two tasks regressed after the prompt rewrite: " + strings.Repeat("readme comprehension answers omit the install section ", 4)
	regressed := &v1alpha1.Comparison{Status: v1alpha1.ComparisonStatus{
		Verdict: v1alpha1.ComparisonVerdictRegressed,
		Tasks: []v1alpha1.ComparisonTaskStatus{
			{ID: "a", BaseRate: floatPtr(0.8), CandidateRate: floatPtr(0.4), Delta: floatPtr(-0.4), Label: v1alpha1.ComparisonTaskLabelRegressed},
		},
	}}

	t.Run("message wins over the derived fallback", func(t *testing.T) {
		comp := regressed.DeepCopy()
		comp.Status.Message = "  Publisher explanation  "
		display, full := ComparisonReason(comp)
		if display != "Publisher explanation" || full != "Publisher explanation" {
			t.Errorf("got display=%q full=%q", display, full)
		}
	})

	t.Run("multi-line message shows only its headline", func(t *testing.T) {
		comp := regressed.DeepCopy()
		comp.Status.Message = "\r\n  Headline sentence.  \r\n\r\nDetail:\r\n- one\r\n"
		display, full := ComparisonReason(comp)
		if display != "Headline sentence." {
			t.Errorf("display = %q, want the headline only", display)
		}
		if full != "Headline sentence.  \n\nDetail:\n- one" {
			t.Errorf("full = %q, want the whole message with normalized line breaks", full)
		}
	})

	t.Run("long message is truncated for display only", func(t *testing.T) {
		comp := regressed.DeepCopy()
		comp.Status.Message = long
		display, full := ComparisonReason(comp)
		if full != strings.TrimSpace(long) {
			t.Errorf("expected the full message in the title text")
		}
		if n := utf8.RuneCountInString(display); n > reasonMaxRunes || !strings.HasSuffix(display, "…") {
			t.Errorf("expected display truncated to at most %d runes with an ellipsis, got %d runes: %q", reasonMaxRunes, n, display)
		}
	})

	t.Run("regressed without message uses the fallback", func(t *testing.T) {
		display, full := ComparisonReason(regressed)
		if display != "Regressed on 1 of 1 tasks: a (80% → 40%)" || display != full {
			t.Errorf("got display=%q full=%q", display, full)
		}
	})

	t.Run("nothing to show", func(t *testing.T) {
		comp := &v1alpha1.Comparison{Status: v1alpha1.ComparisonStatus{Verdict: v1alpha1.ComparisonVerdictImproved}}
		if display, full := ComparisonReason(comp); display != "" || full != "" {
			t.Errorf("expected empty reason, got display=%q full=%q", display, full)
		}
	})
}

func TestParseComparisonKind(t *testing.T) {
	tests := map[string]ComparisonKind{
		"":          ComparisonKindAll,
		"all":       ComparisonKindAll,
		"baseline":  ComparisonKindBaseline,
		" Baseline": ComparisonKindBaseline,
		"previous":  ComparisonKindPrevious,
		"PREVIOUS":  ComparisonKindPrevious,
		"bogus":     ComparisonKindAll,
	}
	for in, want := range tests {
		if got := ParseComparisonKind(in); got != want {
			t.Errorf("ParseComparisonKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComparisonsKindLink(t *testing.T) {
	query := url.Values{"namespace": {"team-a"}, "offset": {"100"}, "limit": {"50"}, "kind": {"previous"}}
	if got, want := comparisonsKindLink(query, ComparisonKindBaseline), "/comparisons?kind=baseline&limit=50&namespace=team-a"; got != want {
		t.Errorf("baseline link = %q, want %q", got, want)
	}
	if got, want := comparisonsKindLink(query, ComparisonKindAll), "/comparisons?limit=50&namespace=team-a"; got != want {
		t.Errorf("all link = %q, want %q", got, want)
	}
	if got, want := comparisonsKindLink(url.Values{}, ComparisonKindAll), "/comparisons"; got != want {
		t.Errorf("bare all link = %q, want %q", got, want)
	}
}

func TestIsVsLabels(t *testing.T) {
	comp := &v1alpha1.Comparison{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
		LabelVsBaseline: "True",
		LabelVsPrevious: "false",
	}}}
	if !IsVsBaseline(comp) {
		t.Errorf("expected vs-baseline label value True to match")
	}
	if IsVsPrevious(comp) {
		t.Errorf("expected vs-previous label value false not to match")
	}
	if IsVsBaseline(&v1alpha1.Comparison{}) || IsVsBaseline(nil) {
		t.Errorf("expected unlabeled and nil Comparisons not to match")
	}
}

// groupingFixtures returns Runs and Comparisons for the grouped /comparisons view. Run
// creation order is v1, v2, v3; Comparison creation order deliberately differs.
func groupingFixtures(now time.Time) ([]v1alpha1.Comparison, map[string]*v1alpha1.Run) {
	run := func(name string, age time.Duration) *v1alpha1.Run {
		return &v1alpha1.Run{ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-age)),
		}}
	}
	runs := map[string]*v1alpha1.Run{
		"run-v1": run("run-v1", 60*time.Minute),
		"run-v2": run("run-v2", 50*time.Minute),
		"run-v3": run("run-v3", 40*time.Minute),
	}
	comp := func(name, base, cand string, age time.Duration, labels map[string]string) v1alpha1.Comparison {
		return v1alpha1.Comparison{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         "eval-runner",
				CreationTimestamp: metav1.NewTime(now.Add(-age)),
				Labels:            labels,
			},
			Spec: v1alpha1.ComparisonSpec{
				Baseline:  v1alpha1.ComparisonRunRef{Run: base},
				Candidate: v1alpha1.ComparisonRunRef{Run: cand},
			},
			Status: v1alpha1.ComparisonStatus{Verdict: v1alpha1.ComparisonVerdictImproved},
		}
	}
	both := map[string]string{LabelVsBaseline: "true", LabelVsPrevious: "true"}
	baseline := map[string]string{LabelVsBaseline: "true"}
	previous := map[string]string{LabelVsPrevious: "true"}
	items := []v1alpha1.Comparison{
		// v2's only Comparison is against v1, which is both the baseline and the previous Run.
		comp("v2-vs-v1", "run-v1", "run-v2", 5*time.Minute, both),
		comp("v3-vs-v1", "run-v1", "run-v3", 30*time.Minute, baseline),
		comp("v3-vs-v1-old", "run-v1", "run-v3", 35*time.Minute, baseline),
		comp("v3-vs-v2", "run-v2", "run-v3", 20*time.Minute, previous),
		// A Run that no longer exists: ordered by its newest Comparison instead.
		comp("v4-vs-v1", "run-v1", "run-v4-deleted", 1*time.Minute, baseline),
		comp("pr-7", "base-1", "pr-7-head", 10*time.Minute, nil),
		comp("pr-8", "base-1", "pr-8-head", 45*time.Minute, map[string]string{LabelVsBaseline: "no"}),
	}
	return items, runs
}

func rowKeys(rows []comparisonListRow) string {
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.isGroup() {
			keys = append(keys, "group:"+r.candidate)
		} else {
			keys = append(keys, r.single.Name)
		}
	}
	return strings.Join(keys, ",")
}

func TestComparisonListRows(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	items, runs := groupingFixtures(now)

	t.Run("all", func(t *testing.T) {
		rows, total := comparisonListRows(items, ComparisonKindAll, runs)
		if total != len(items) {
			t.Errorf("total = %d, want %d", total, len(items))
		}
		// Groups sort by candidate Run creation time (v3 at -40m, v2 at -50m), the deleted
		// Run's group by its newest Comparison (-1m), and unlabeled rows by their own time.
		want := "group:run-v4-deleted,pr-7,group:run-v3,pr-8,group:run-v2"
		if got := rowKeys(rows); got != want {
			t.Errorf("rows = %s, want %s", got, want)
		}

		v3 := rows[2].groupRow(runs)
		if v3.VsBaseline == nil || v3.VsBaseline.Name != "v3-vs-v1" {
			t.Fatalf("expected newest vs-baseline Comparison in the v3 cell, got %+v", v3.VsBaseline)
		}
		if len(v3.VsBaseline.Older) != 1 || v3.VsBaseline.Older[0].Name != "v3-vs-v1-old" {
			t.Errorf("expected the older vs-baseline Comparison to be listed, got %+v", v3.VsBaseline.Older)
		}
		if v3.VsPrevious == nil || v3.VsPrevious.Name != "v3-vs-v2" || v3.VsPrevious.BaselineRun != "run-v2" {
			t.Errorf("unexpected v3 vs-previous cell: %+v", v3.VsPrevious)
		}
		if v3.Created != FormatUTCTimestamp(&runs["run-v3"].CreationTimestamp) {
			t.Errorf("expected the candidate Run creation time, got %q", v3.Created)
		}

		v2 := rows[4].groupRow(runs)
		if v2.VsBaseline == nil || v2.VsPrevious == nil || v2.VsBaseline.Name != "v2-vs-v1" || v2.VsPrevious.Name != "v2-vs-v1" {
			t.Errorf("expected the Comparison with both labels in both cells, got baseline=%+v previous=%+v", v2.VsBaseline, v2.VsPrevious)
		}

		deleted := rows[0].groupRow(runs)
		if deleted.Created != "—" || deleted.VsPrevious != nil {
			t.Errorf("expected em dash creation time and no vs-previous cell for the deleted Run, got %+v", deleted)
		}
	})

	t.Run("baseline filter", func(t *testing.T) {
		rows, total := comparisonListRows(items, ComparisonKindBaseline, runs)
		if total != 4 {
			t.Errorf("total = %d, want 4 vs-baseline Comparisons", total)
		}
		if got, want := rowKeys(rows), "group:run-v4-deleted,group:run-v3,group:run-v2"; got != want {
			t.Errorf("rows = %s, want %s", got, want)
		}
		for _, r := range rows {
			if len(r.previous) != 0 {
				t.Errorf("expected no vs-previous members under the baseline filter, got %d for %s", len(r.previous), r.candidate)
			}
		}
	})

	t.Run("previous filter", func(t *testing.T) {
		rows, total := comparisonListRows(items, ComparisonKindPrevious, runs)
		if total != 2 {
			t.Errorf("total = %d, want 2 vs-previous Comparisons", total)
		}
		if got, want := rowKeys(rows), "group:run-v3,group:run-v2"; got != want {
			t.Errorf("rows = %s, want %s", got, want)
		}
	})

	t.Run("without runs groups fall back to comparison times", func(t *testing.T) {
		rows, _ := comparisonListRows(items, ComparisonKindAll, nil)
		// v2 (-5m), v3 (newest member -20m), pr-7 (-10m), pr-8 (-45m), v4 (-1m).
		if got, want := rowKeys(rows), "group:run-v4-deleted,group:run-v2,pr-7,group:run-v3,pr-8"; got != want {
			t.Errorf("rows = %s, want %s", got, want)
		}
	})

	t.Run("unlabeled only keeps the previous ordering", func(t *testing.T) {
		unlabeled := []v1alpha1.Comparison{items[5], items[6]}
		rows, total := comparisonListRows(unlabeled, ComparisonKindAll, runs)
		if total != 2 || rowKeys(rows) != "pr-7,pr-8" {
			t.Errorf("rows = %s (total %d), want pr-7,pr-8", rowKeys(rows), total)
		}
		if hasLabeledComparison(unlabeled) {
			t.Errorf("expected no labeled Comparisons")
		}
	})
}

func TestNewComparisonListItem(t *testing.T) {
	completed := metav1.NewTime(time.Date(2026, 10, 20, 10, 35, 14, 0, time.UTC))
	comp, baseRun, candRun := regressedComparisonFixture()
	comp.Status.CompletionTime = &completed
	comp.Status.Message = ""

	item := NewComparisonListItem(comp, baseRun, candRun)
	if item.Completed != "2026-10-20 10:35 UTC" {
		t.Errorf("Completed = %q", item.Completed)
	}
	if want := "Regressed on 2 of 4 tasks: docs/readme_comprehension (80% → 40%), triage/runbook_lookup (70% → 54%)"; item.Reason != want || item.ReasonFull != want {
		t.Errorf("Reason = %q, ReasonFull = %q, want %q", item.Reason, item.ReasonFull, want)
	}
	if item.Deltas.Quality.Text == "" || item.Deltas.Quality.Class == deltaClassMissing {
		t.Errorf("expected a quality delta from the fixture Runs, got %+v", item.Deltas.Quality)
	}

	comp.Status.CompletionTime = nil
	if got := NewComparisonListItem(comp, nil, nil); got.Completed != "—" || got.Deltas.Tokens.Class != deltaClassMissing {
		t.Errorf("expected em dash completion and missing token delta, got Completed=%q Tokens=%+v", got.Completed, got.Deltas.Tokens)
	}
}
