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
	"strings"
	"testing"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// deltaTask describes one Run task for delta fixtures. Negative tokens or cost mean unset.
type deltaTask struct {
	id     string
	rate   float64
	tokens int
	cost   float64
}

func deltaRun(name string, runCost *float64, tasks ...deltaTask) *v1alpha1.Run {
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "eval-runner"},
		Status:     v1alpha1.RunStatus{Phase: v1alpha1.RunPhaseCompleted, CostUSD: runCost},
	}
	for _, dt := range tasks {
		task := v1alpha1.TaskStatus{ID: dt.id, Phase: v1alpha1.TaskPhaseCompleted, SuccessRate: floatPtr(dt.rate)}
		if dt.tokens >= 0 {
			task.Tokens = &v1alpha1.TaskTokens{Total: dt.tokens}
		}
		if dt.cost >= 0 {
			task.CostUSD = floatPtr(dt.cost)
		}
		run.Status.Tasks = append(run.Status.Tasks, task)
	}
	return run
}

func assertDelta(t *testing.T, what string, got MetricDelta, wantText, wantClass string) {
	t.Helper()
	if got.Text != wantText || got.Class != wantClass {
		t.Errorf("%s = {Text: %q, Class: %q}, want {Text: %q, Class: %q} (title %q)", what, got.Text, got.Class, wantText, wantClass, got.Title)
	}
}

func TestComputeRunDeltas(t *testing.T) {
	t.Run("positive quality, fewer tokens, higher cost", func(t *testing.T) {
		base := deltaRun("base", nil,
			deltaTask{"a", 0.60, 100000, 1.00},
			deltaTask{"b", 0.40, 100000, 2.89},
		)
		cand := deltaRun("cand", nil,
			deltaTask{"a", 0.70, 96000, 1.03},
			deltaTask{"b", 0.434, 97600, 2.93},
		)
		d := ComputeRunDeltas(nil, base, cand)
		// Means 50.0% → 56.7%: +6.7 pp.
		assertDelta(t, "quality", d.Quality, "+6.7 pp", deltaClassGood)
		if !strings.Contains(d.Quality.Title, "Mean success rate 50.0% → 56.7% over 2 shared tasks (Run success rates)") {
			t.Errorf("unexpected quality title %q", d.Quality.Title)
		}
		// 200,000 → 193,600 tokens: −3.2%, with −6.4K in the title.
		assertDelta(t, "tokens", d.Tokens, "−3.2%", deltaClassGood)
		if !strings.Contains(d.Tokens.Title, "−6.4K tokens (0.20M → 0.19M) over 2 shared tasks") {
			t.Errorf("unexpected tokens title %q", d.Tokens.Title)
		}
		// $3.89 → $3.96: +$0.07 (+1.8%).
		assertDelta(t, "cost", d.Cost, "+$0.07 (+1.8%)", deltaClassBad)
		if !strings.Contains(d.Cost.Title, "$3.8900 → $3.9600 over 2 shared tasks") {
			t.Errorf("unexpected cost title %q", d.Cost.Title)
		}
	})

	t.Run("negative quality, more tokens, lower cost", func(t *testing.T) {
		base := deltaRun("base", nil, deltaTask{"a", 0.80, 1000, 0.0100})
		cand := deltaRun("cand", nil, deltaTask{"a", 0.40, 1500, 0.0087})
		d := ComputeRunDeltas(nil, base, cand)
		assertDelta(t, "quality", d.Quality, "−40.0 pp", deltaClassBad)
		assertDelta(t, "tokens", d.Tokens, "+50.0%", deltaClassBad)
		assertDelta(t, "cost", d.Cost, "−$0.0013 (−13.0%)", deltaClassGood)
	})

	t.Run("zero deltas are neutral", func(t *testing.T) {
		base := deltaRun("base", nil, deltaTask{"a", 0.5, 8750, 0.001875}, deltaTask{"b", 1, 8750, 0.001875})
		cand := deltaRun("cand", nil, deltaTask{"a", 0.5, 8750, 0.001875}, deltaTask{"b", 1, 8750, 0.001875})
		d := ComputeRunDeltas(nil, base, cand)
		assertDelta(t, "quality", d.Quality, "0.0 pp", deltaClassZero)
		assertDelta(t, "tokens", d.Tokens, "0.0%", deltaClassZero)
		assertDelta(t, "cost", d.Cost, "$0.00 (0.0%)", deltaClassZero)
	})

	t.Run("tokens and cost missing on one run", func(t *testing.T) {
		base := deltaRun("base", nil, deltaTask{"a", 0.5, 1000, 0.01})
		cand := deltaRun("cand", nil, deltaTask{"a", 0.6, -1, -1})
		d := ComputeRunDeltas(nil, base, cand)
		assertDelta(t, "quality", d.Quality, "+10.0 pp", deltaClassGood)
		assertDelta(t, "tokens", d.Tokens, "—", deltaClassMissing)
		assertDelta(t, "cost", d.Cost, "—", deltaClassMissing)
	})

	t.Run("tokens and cost missing on both runs", func(t *testing.T) {
		base := deltaRun("base", nil, deltaTask{"a", 0.5, -1, -1})
		cand := deltaRun("cand", nil, deltaTask{"a", 0.5, -1, -1})
		d := ComputeRunDeltas(nil, base, cand)
		assertDelta(t, "tokens", d.Tokens, "—", deltaClassMissing)
		assertDelta(t, "cost", d.Cost, "—", deltaClassMissing)
	})

	t.Run("cost falls back to run totals", func(t *testing.T) {
		base := deltaRun("base", floatPtr(4.00), deltaTask{"a", 0.5, -1, -1})
		cand := deltaRun("cand", floatPtr(3.50), deltaTask{"a", 0.5, -1, -1})
		d := ComputeRunDeltas(nil, base, cand)
		assertDelta(t, "cost", d.Cost, "−$0.50 (−12.5%)", deltaClassGood)
		if !strings.Contains(d.Cost.Title, "from Run totals") {
			t.Errorf("expected the title to name the Run totals, got %q", d.Cost.Title)
		}
	})

	t.Run("mismatched task sets use shared tasks only", func(t *testing.T) {
		base := deltaRun("base", nil,
			deltaTask{"a", 1.0, 5000, 0.5},
			deltaTask{"b", 0.5, 1000, 0.1},
			deltaTask{"c", 0.5, 1000, 0.1},
		)
		cand := deltaRun("cand", nil,
			deltaTask{"b", 0.7, 1100, 0.1},
			deltaTask{"c", 0.5, 1100, 0.1},
			deltaTask{"d", 0.0, 9000, 0.9},
		)
		d := ComputeRunDeltas(nil, base, cand)
		// Shared tasks b and c: means 50% → 60%, tokens 2,000 → 2,200, cost $0.20 → $0.20.
		assertDelta(t, "quality", d.Quality, "+10.0 pp", deltaClassGood)
		assertDelta(t, "tokens", d.Tokens, "+10.0%", deltaClassBad)
		assertDelta(t, "cost", d.Cost, "$0.00 (0.0%)", deltaClassZero)
		if !strings.Contains(d.Quality.Title, "over 2 shared tasks") {
			t.Errorf("expected two shared tasks in the title, got %q", d.Quality.Title)
		}

		// The Comparison's task list narrows the set further: only b is in both Runs.
		comp := &v1alpha1.Comparison{Status: v1alpha1.ComparisonStatus{Tasks: []v1alpha1.ComparisonTaskStatus{{ID: "a"}, {ID: "b"}}}}
		d = ComputeRunDeltas(comp, base, cand)
		assertDelta(t, "quality (comparison tasks)", d.Quality, "+20.0 pp", deltaClassGood)
		if !strings.Contains(d.Quality.Title, "over 1 shared task ") {
			t.Errorf("expected one shared task in the title, got %q", d.Quality.Title)
		}
	})

	t.Run("no shared tasks", func(t *testing.T) {
		base := deltaRun("base", nil, deltaTask{"a", 1.0, 5000, 0.5})
		cand := deltaRun("cand", nil, deltaTask{"b", 0.0, 9000, 0.9})
		d := ComputeRunDeltas(nil, base, cand)
		assertDelta(t, "quality", d.Quality, "—", deltaClassMissing)
		assertDelta(t, "tokens", d.Tokens, "—", deltaClassMissing)
		assertDelta(t, "cost", d.Cost, "—", deltaClassMissing)
	})

	t.Run("deleted runs fall back to comparison rates for quality", func(t *testing.T) {
		comp := &v1alpha1.Comparison{Status: v1alpha1.ComparisonStatus{Tasks: []v1alpha1.ComparisonTaskStatus{
			{ID: "a", BaseRate: floatPtr(0.6), CandidateRate: floatPtr(0.2)},
			{ID: "b", BaseRate: floatPtr(0.4), CandidateRate: floatPtr(0.6)},
			{ID: "c"}, // still running
		}}}
		d := ComputeRunDeltas(comp, nil, nil)
		assertDelta(t, "quality", d.Quality, "−10.0 pp", deltaClassBad)
		if !strings.Contains(d.Quality.Title, "Comparison task rates") {
			t.Errorf("expected the title to name the Comparison rates, got %q", d.Quality.Title)
		}
		assertDelta(t, "tokens", d.Tokens, "—", deltaClassMissing)
		assertDelta(t, "cost", d.Cost, "—", deltaClassMissing)
	})

	t.Run("zero baseline tokens show the absolute change", func(t *testing.T) {
		base := deltaRun("base", nil, deltaTask{"a", 0.5, 0, 0})
		cand := deltaRun("cand", nil, deltaTask{"a", 0.5, 12345, 0.02})
		d := ComputeRunDeltas(nil, base, cand)
		assertDelta(t, "tokens", d.Tokens, "+12.3K", deltaClassBad)
		assertDelta(t, "cost", d.Cost, "+$0.02", deltaClassBad)
	})

	t.Run("mean score is used when success rate is unset", func(t *testing.T) {
		base := &v1alpha1.Run{Status: v1alpha1.RunStatus{Tasks: []v1alpha1.TaskStatus{{ID: "a", MeanScore: floatPtr(0.25)}}}}
		cand := &v1alpha1.Run{Status: v1alpha1.RunStatus{Tasks: []v1alpha1.TaskStatus{{ID: "a", MeanScore: floatPtr(0.75)}}}}
		assertDelta(t, "quality", ComputeRunDeltas(nil, base, cand).Quality, "+50.0 pp", deltaClassGood)
	})
}

func TestDeltaFormatting(t *testing.T) {
	counts := map[int64]string{
		0:             "0",
		999:           "999",
		-999:          "999",
		12345:         "12.3K",
		99999:         "100.0K",
		183750:        "0.18M",
		-180000:       "0.18M",
		2500000:       "2.50M",
		1_200_000_000: "1.20B",
	}
	for in, want := range counts {
		if got := compactCount(in); got != want {
			t.Errorf("compactCount(%d) = %q, want %q", in, got, want)
		}
	}
	if got := signedCompactCount(-180000); got != "−0.18M" {
		t.Errorf("signedCompactCount(-180000) = %q", got)
	}

	usd := map[float64]string{
		0.07:     "+$0.07",
		-0.5:     "−$0.50",
		0.0013:   "+$0.0013",
		-0.00004: "$0.00", // rounds to zero at four decimals, so it is unsigned
		0:        "$0.00",
		0.000001: "$0.00",
	}
	for in, want := range usd {
		if got, _ := signedUSD(in); got != want {
			t.Errorf("signedUSD(%v) = %q, want %q", in, got, want)
		}
	}

	if text, sign := signedFixed(-0.04, 1); text != "0.0" || sign != 0 {
		t.Errorf("signedFixed(-0.04, 1) = %q, %d; want unsigned zero", text, sign)
	}
}
