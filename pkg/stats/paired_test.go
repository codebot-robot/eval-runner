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
	"fmt"
	"math"
	"testing"
)

func TestPairedIdenticalArrays(t *testing.T) {
	// Acceptance criterion:
	// Identical arrays give delta 0 with CI containing 0 and unchanged under a margin.
	n := 20
	cand := make([]SampleScore, n)
	base := make([]SampleScore, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("sample-%d", i)
		score := float64(i%2) * 0.8
		cand[i] = SampleScore{ID: id, Score: score}
		base[i] = SampleScore{ID: id, Score: score}
	}

	est := ComparePaired(cand, base, 0.95)
	if est.MeanDelta != 0.0 {
		t.Errorf("MeanDelta = %v, want 0.0", est.MeanDelta)
	}
	if est.CI.Lower > 0 || est.CI.Upper < 0 {
		t.Errorf("CI [%v, %v] must contain 0", est.CI.Lower, est.CI.Upper)
	}
	if est.PImprovement != 0.5 {
		t.Errorf("PImprovement for identical samples = %v, want 0.5", est.PImprovement)
	}
	if est.DroppedCandidate != 0 || est.DroppedBaseline != 0 {
		t.Errorf("Expected 0 dropped samples, got cand=%d base=%d", est.DroppedCandidate, est.DroppedBaseline)
	}

	dec := Decide(est, 0.05, n, 100, 1.0, 10.0)
	if dec.Label != TaskLabelUnchanged {
		t.Errorf("Decide label = %v, want %v", dec.Label, TaskLabelUnchanged)
	}
	if !dec.Stop {
		t.Errorf("Decide Stop = %v, want true", dec.Stop)
	}
}

func TestPairedConstructedImprovement(t *testing.T) {
	// Acceptance criterion: a constructed improvement is detected.
	n := 30
	cand := make([]SampleScore, n)
	base := make([]SampleScore, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("sample-%d", i)
		cand[i] = SampleScore{ID: id, Score: 1.0}
		base[i] = SampleScore{ID: id, Score: 0.0}
	}

	est := ComparePaired(cand, base, 0.95)
	if est.MeanDelta != 1.0 {
		t.Errorf("MeanDelta = %v, want 1.0", est.MeanDelta)
	}
	if est.CI.Lower != 1.0 || est.CI.Upper != 1.0 {
		t.Errorf("CI = [%v, %v], want [1.0, 1.0]", est.CI.Lower, est.CI.Upper)
	}
	if est.PImprovement != 1.0 {
		t.Errorf("PImprovement = %v, want 1.0", est.PImprovement)
	}

	dec := Decide(est, 0.05, n, 100, 1.0, 10.0)
	if dec.Label != TaskLabelImproved {
		t.Errorf("Decide label = %v, want %v", dec.Label, TaskLabelImproved)
	}
	if !dec.Stop {
		t.Errorf("Decide Stop = %v, want true", dec.Stop)
	}
}

func TestPairedConstructedRegression(t *testing.T) {
	n := 30
	cand := make([]SampleScore, n)
	base := make([]SampleScore, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("sample-%d", i)
		cand[i] = SampleScore{ID: id, Score: 0.0}
		base[i] = SampleScore{ID: id, Score: 1.0}
	}

	est := ComparePaired(cand, base, 0.95)
	if est.MeanDelta != -1.0 {
		t.Errorf("MeanDelta = %v, want -1.0", est.MeanDelta)
	}
	if est.CI.Upper != -1.0 {
		t.Errorf("CI upper = %v, want -1.0", est.CI.Upper)
	}
	if est.PImprovement != 0.0 {
		t.Errorf("PImprovement = %v, want 0.0", est.PImprovement)
	}

	dec := Decide(est, 0.05, n, 100, 1.0, 10.0)
	if dec.Label != TaskLabelRegressed {
		t.Errorf("Decide label = %v, want %v", dec.Label, TaskLabelRegressed)
	}
	if !dec.Stop {
		t.Errorf("Decide Stop = %v, want true", dec.Stop)
	}
}

func TestPairedUnpairedIDsDropped(t *testing.T) {
	// Acceptance criterion: unpaired ids are counted as dropped, not silently ignored.
	cand := []SampleScore{
		{ID: "common-1", Score: 1.0},
		{ID: "common-2", Score: 0.8},
		{ID: "pr-only-1", Score: 0.9},
		{ID: "pr-only-2", Score: 0.5},
	}
	base := []SampleScore{
		{ID: "common-1", Score: 0.5},
		{ID: "common-2", Score: 0.8},
		{ID: "base-only-1", Score: 0.4},
	}

	est := ComparePaired(cand, base, 0.95)
	if est.NumPairs != 2 {
		t.Errorf("NumPairs = %d, want 2", est.NumPairs)
	}
	if est.DroppedCandidate != 2 {
		t.Errorf("DroppedCandidate = %d, want 2", est.DroppedCandidate)
	}
	if est.DroppedBaseline != 1 {
		t.Errorf("DroppedBaseline = %d, want 1", est.DroppedBaseline)
	}
}

func TestPairedCINarrowingShape(t *testing.T) {
	// Acceptance criterion:
	// Must reproduce the shape of the budget mockup in docs/concept/ui/budget.html:
	// CI narrowing as n grows.
	//
	// In the concept:
	// n=4: [-0.42, +0.78] (width 1.20)
	// n=16: [-0.06, +0.48] (width 0.54)
	// n=24: [0.00, +0.43] (width 0.43)
	// n=96: [+0.15, +0.31] (width 0.16)
	// We simulate a fixed paired sequence with delta ~ +0.23 and test that sample sizes
	// 8, 16, 32, 64, 128 produce progressively narrower confidence intervals.

	// Fixed sequence generated with seed 42 (base ~0.61, candidate ~0.84)
	popBase := []float64{
		1, 1, 0, 1, 1, 1, 1, 0, 1, 1, 0, 0, 0, 1, 1, 0, 1, 0, 0, 1,
		1, 1, 1, 0, 0, 1, 1, 0, 1, 0, 1, 1, 1, 0, 1, 1, 0, 1, 1, 0,
		1, 1, 1, 1, 1, 1, 0, 1, 0, 1, 0, 1, 1, 1, 0, 1, 1, 1, 1, 0,
		1, 1, 1, 0, 1, 0, 1, 1, 1, 1, 0, 1, 1, 0, 1, 1, 1, 0, 1, 1,
		0, 1, 1, 1, 1, 1, 0, 1, 1, 1, 1, 0, 0, 1, 1, 1, 1, 1, 0, 1,
		1, 1, 0, 1, 1, 1, 1, 0, 1, 1, 0, 0, 0, 1, 1, 0, 1, 0, 0, 1,
		1, 1, 1, 0, 0, 1, 1, 0,
	}
	popCand := []float64{
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 1, 1, 1, 1, 1, 0, 1, 1,
		1, 1, 1, 1, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 1, 1, 1, 1, 1, 0, 1, 1,
		1, 1, 1, 1, 0, 1, 1, 1,
	}

	sampleSizes := []int{8, 16, 32, 64, 128}
	var lastWidth float64 = 2.0

	for _, n := range sampleSizes {
		cand := make([]SampleScore, n)
		base := make([]SampleScore, n)
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("id-%d", i)
			cand[i] = SampleScore{ID: id, Score: popCand[i]}
			base[i] = SampleScore{ID: id, Score: popBase[i]}
		}

		est := ComparePairedWithSeed(cand, base, 0.95, 42)
		width := est.CI.Upper - est.CI.Lower
		t.Logf("n=%d: mean=%.2f, CI=[%.2f, %.2f], width=%.2f, P(improvement)=%.3f",
			n, est.MeanDelta, est.CI.Lower, est.CI.Upper, width, est.PImprovement)

		if width >= lastWidth {
			t.Errorf("CI width did not narrow from n=%d (current width=%.2f, last=%.2f)", n, width, lastWidth)
		}
		lastWidth = width

		if n >= 64 {
			// At larger n, CI should be clear of zero: improved
			if est.CI.Lower <= 0 {
				t.Errorf("At n=%d, CI lower bound (%v) should exclude zero", n, est.CI.Lower)
			}
		}
	}
}

func TestNormalAndBootstrapComparison(t *testing.T) {
	// Verify that both NormalCI and BootstrapCI are populated and reasonably close
	diffs := []float64{0.2, 0.1, 0.3, -0.1, 0.0, 0.4, 0.2, 0.1, 0.3, 0.2}
	est := CompareDifferences(diffs, 0.95, 42)

	if est.BootstrapCI.Lower == 0 && est.BootstrapCI.Upper == 0 {
		t.Errorf("BootstrapCI should not be zero")
	}
	if est.NormalCI.Lower == 0 && est.NormalCI.Upper == 0 {
		t.Errorf("NormalCI should not be zero")
	}

	diffLower := math.Abs(est.BootstrapCI.Lower - est.NormalCI.Lower)
	diffUpper := math.Abs(est.BootstrapCI.Upper - est.NormalCI.Upper)
	if diffLower > 0.15 || diffUpper > 0.15 {
		t.Errorf("Bootstrap and Normal CI diverged too much: boot=%v, norm=%v", est.BootstrapCI, est.NormalCI)
	}
}
