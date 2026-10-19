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
	if est.NormalPImprovement != 0.5 {
		t.Errorf("NormalPImprovement for identical samples = %v, want 0.5", est.NormalPImprovement)
	}
	if est.Wins != 0 || est.Losses != 0 || est.Ties != n {
		t.Errorf("Expected wins/losses/ties = 0/0/%d, got %d/%d/%d", n, est.Wins, est.Losses, est.Ties)
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

func TestTieSplittingAndNormalFallback(t *testing.T) {
	// Acceptance criterion: all-tied -> P = 0.5 (both bootstrap and normal fallback)
	allZeros := []float64{0.0, 0.0, 0.0, 0.0}
	pBootZero := BayesianBootstrapPImprovement(allZeros, 1000, 42)
	if pBootZero != 0.5 {
		t.Errorf("BayesianBootstrapPImprovement for all-zero diffs = %v, want 0.5", pBootZero)
	}

	pNormZero := NormalApproximationPImprovement(allZeros)
	if pNormZero != 0.5 {
		t.Errorf("NormalApproximationPImprovement for all-zero diffs = %v, want 0.5", pNormZero)
	}

	estZero := CompareDifferences(allZeros, 0.95, 42)
	if estZero.PImprovement != 0.5 {
		t.Errorf("CompareDifferences.PImprovement for all-zero = %v, want 0.5", estZero.PImprovement)
	}
	if estZero.NormalPImprovement != 0.5 {
		t.Errorf("CompareDifferences.NormalPImprovement for all-zero = %v, want 0.5", estZero.NormalPImprovement)
	}
	if estZero.Wins != 0 || estZero.Losses != 0 || estZero.Ties != 4 {
		t.Errorf("CompareDifferences wins/losses/ties = %d/%d/%d, want 0/0/4",
			estZero.Wins, estZero.Losses, estZero.Ties)
	}

	// Acceptance criterion: mixed with ties -> P matches the split formula on a hand-computed example
	// Hand-computed Example 1: Symmetric differences with ties: {+1.0, 0.0, 0.0, -1.0}
	// Wins=1, Losses=1, Ties=2.
	// Empirical split formula: (Wins + 0.5 * Ties) / N = (1 + 0.5 * 2) / 4 = 0.50.
	// Normal approximation: mean = 0, se > 0 => z = 0 => NormalCDF(0) = 0.50.
	symDiffs := []float64{1.0, 0.0, 0.0, -1.0}
	pNormSym := NormalApproximationPImprovement(symDiffs)
	if pNormSym != 0.50 {
		t.Errorf("NormalApproximationPImprovement(symmetric) = %v, want 0.50", pNormSym)
	}

	estSym := CompareDifferences(symDiffs, 0.95, 42)
	if estSym.Wins != 1 || estSym.Losses != 1 || estSym.Ties != 2 {
		t.Errorf("CompareDifferences wins/losses/ties = %d/%d/%d, want 1/1/2",
			estSym.Wins, estSym.Losses, estSym.Ties)
	}
	if math.Abs(estSym.PImprovement-0.50) > 0.02 {
		t.Errorf("BayesianBootstrapPImprovement(symmetric) = %v, want ~0.50", estSym.PImprovement)
	}

	// Hand-computed Example 2: Asymmetric differences with ties: {+1.0, 0.0, 0.0, +1.0}
	// Wins=2, Losses=0, Ties=2.
	// Empirical split formula: (Wins + 0.5 * Ties) / N = (2 + 0.5 * 2) / 4 = 0.75.
	// Normal approximation analytical computation:
	//   mean = (1 + 0 + 0 + 1) / 4 = 0.5
	//   variance = sum((x - 0.5)^2) / 3 = (0.25 * 4) / 3 = 1/3
	//   se = sqrt((1/3) / 4) = 1 / sqrt(12)
	//   z = 0.5 / (1 / sqrt(12)) = 0.5 * sqrt(12) = sqrt(3) ≈ 1.7320508075688772
	//   expected P = NormalCDF(sqrt(3)) ≈ 0.95836764
	asymDiffs := []float64{1.0, 0.0, 0.0, 1.0}
	expectedZ := math.Sqrt(3.0)
	expectedPNorm := NormalCDF(expectedZ)

	pNormAsym := NormalApproximationPImprovement(asymDiffs)
	if math.Abs(pNormAsym-expectedPNorm) > 1e-6 {
		t.Errorf("NormalApproximationPImprovement({1,0,0,1}) = %v, want NormalCDF(sqrt(3)) = %v",
			pNormAsym, expectedPNorm)
	}

	estAsym := CompareDifferences(asymDiffs, 0.95, 42)
	if estAsym.Wins != 2 || estAsym.Losses != 0 || estAsym.Ties != 2 {
		t.Errorf("CompareDifferences wins/losses/ties = %d/%d/%d, want 2/0/2",
			estAsym.Wins, estAsym.Losses, estAsym.Ties)
	}
	if math.Abs(estAsym.NormalPImprovement-expectedPNorm) > 1e-6 {
		t.Errorf("estAsym.NormalPImprovement = %v, want %v", estAsym.NormalPImprovement, expectedPNorm)
	}

	// Hand-computed Example 3: Uniform prior regularisation on 0W/1L/19T
	// In the real GKE comparison docs/readme_comprehension_blind (20 pairs: 0 wins, 1 loss, 19 ties):
	// Analytically, Beta(W+1, L+1) = Beta(1, 2), P(Beta(1, 2) > 0.5) = 0.25.
	diffs0W1L := make([]float64, 20)
	diffs0W1L[0] = -1.0
	pBoot0W1L := BayesianBootstrapPImprovement(diffs0W1L, DefaultBayesianBootstrapDraws, 42)
	if math.Abs(pBoot0W1L-0.25) > 0.02 {
		t.Errorf("BayesianBootstrapPImprovement(0W/1L/19T) = %v, want ~0.25 (Beta(1, 2))", pBoot0W1L)
	}
	est0W1L := CompareDifferences(diffs0W1L, 0.95, 42)
	if est0W1L.Wins != 0 || est0W1L.Losses != 1 || est0W1L.Ties != 19 {
		t.Errorf("CompareDifferences wins/losses/ties = %d/%d/%d, want 0/1/19",
			est0W1L.Wins, est0W1L.Losses, est0W1L.Ties)
	}
	if math.Abs(est0W1L.PImprovement-0.25) > 0.02 {
		t.Errorf("est0W1L.PImprovement = %v, want ~0.25", est0W1L.PImprovement)
	}

	// Hand-computed Example 4: Uniform prior regularisation on 1W/0L/19T
	// Analytically, Beta(W+1, L+1) = Beta(2, 1), P(Beta(2, 1) > 0.5) = 0.75.
	diffs1W0L := make([]float64, 20)
	diffs1W0L[0] = 1.0
	pBoot1W0L := BayesianBootstrapPImprovement(diffs1W0L, DefaultBayesianBootstrapDraws, 42)
	if math.Abs(pBoot1W0L-0.75) > 0.02 {
		t.Errorf("BayesianBootstrapPImprovement(1W/0L/19T) = %v, want ~0.75 (Beta(2, 1))", pBoot1W0L)
	}

	// Hand-computed Example 5: Uniform prior regularisation on 3W/2L/15T
	// Analytically, Beta(W+1, L+1) = Beta(4, 3), P(Beta(4, 3) > 0.5) = 21/32 = 0.65625.
	diffs3W2L := []float64{1.0, 1.0, 1.0, -1.0, -1.0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	pBoot3W2L := BayesianBootstrapPImprovement(diffs3W2L, DefaultBayesianBootstrapDraws, 42)
	if math.Abs(pBoot3W2L-0.65625) > 0.02 {
		t.Errorf("BayesianBootstrapPImprovement(3W/2L/15T) = %v, want ~0.656 (Beta(4, 3))", pBoot3W2L)
	}
}

func TestComputePairedCISeries_Deterministic(t *testing.T) {
	pairs := make([]PairedSampleScore, 12)
	for i := 0; i < 12; i++ {
		base := 0.5
		cand := 0.5 + float64(i%3)*0.2
		pairs[i] = PairedSampleScore{
			ID:             fmt.Sprintf("sample-%d", i),
			BaseScore:      base,
			CandidateScore: cand,
			Delta:          cand - base,
		}
	}

	nValues := []int{4, 8, 12}
	run1 := ComputePairedCISeries(pairs, nValues, 0.95, 42, 0.05)
	run2 := ComputePairedCISeries(pairs, nValues, 0.95, 42, 0.05)

	if len(run1) != 3 {
		t.Fatalf("expected 3 points, got %d", len(run1))
	}
	if len(run2) != 3 {
		t.Fatalf("expected 3 points in run2, got %d", len(run2))
	}

	for i := range run1 {
		p1, p2 := run1[i], run2[i]
		if p1.N != p2.N {
			t.Errorf("point %d: N mismatch (%d vs %d)", i, p1.N, p2.N)
		}
		if p1.CIWidth != p2.CIWidth {
			t.Errorf("point %d: CIWidth mismatch (%v vs %v)", i, p1.CIWidth, p2.CIWidth)
		}
		if p1.Estimate.MeanDelta != p2.Estimate.MeanDelta {
			t.Errorf("point %d: MeanDelta mismatch (%v vs %v)", i, p1.Estimate.MeanDelta, p2.Estimate.MeanDelta)
		}
		if p1.Estimate.CI != p2.Estimate.CI {
			t.Errorf("point %d: CI mismatch (%v vs %v)", i, p1.Estimate.CI, p2.Estimate.CI)
		}
		if p1.Decision.Label != p2.Decision.Label {
			t.Errorf("point %d: Decision.Label mismatch (%v vs %v)", i, p1.Decision.Label, p2.Decision.Label)
		}
	}
}

func TestComputePairedCISeries_SeriesAtGivenN(t *testing.T) {
	pairs := make([]PairedSampleScore, 12)
	for i := 0; i < 12; i++ {
		cand := 0.8
		base := 0.6
		pairs[i] = PairedSampleScore{
			ID:             fmt.Sprintf("sample-%d", i),
			BaseScore:      base,
			CandidateScore: cand,
			Delta:          cand - base,
		}
	}

	nValues := []int{4, 8, 12}
	series := ComputePairedCISeries(pairs, nValues, 0.95, 42, 0.05)

	if len(series) != 3 {
		t.Fatalf("expected 3 points, got %d", len(series))
	}

	expectedNs := []int{4, 8, 12}
	for i, pt := range series {
		if pt.N != expectedNs[i] {
			t.Errorf("point %d: expected N=%d, got %d", i, expectedNs[i], pt.N)
		}
		expectedWidth := pt.Estimate.CI.Upper - pt.Estimate.CI.Lower
		if math.Abs(pt.CIWidth-expectedWidth) > 1e-9 {
			t.Errorf("point %d: expected CIWidth %v, got %v", i, expectedWidth, pt.CIWidth)
		}
		if i > 0 && pt.N <= series[i-1].N {
			t.Errorf("expected strictly monotone n, but pt[%d].N=%d <= pt[%d].N=%d", i, pt.N, i-1, series[i-1].N)
		}
	}
}

func TestComputePairedCISeries_EdgeCases(t *testing.T) {
	// Pairs < 2 returns nil
	if res := ComputePairedCISeries(nil, []int{4}, 0.95, 42, 0.05); res != nil {
		t.Errorf("expected nil for nil pairs, got %v", res)
	}
	onePair := []PairedSampleScore{{ID: "1", BaseScore: 0.5, CandidateScore: 0.8, Delta: 0.3}}
	if res := ComputePairedCISeries(onePair, []int{1}, 0.95, 42, 0.05); res != nil {
		t.Errorf("expected nil for single pair, got %v", res)
	}

	// nValues with n < 2, n > len(pairs), duplicates or non-monotone
	pairs := []PairedSampleScore{
		{ID: "1", BaseScore: 0.5, CandidateScore: 0.8, Delta: 0.3},
		{ID: "2", BaseScore: 0.5, CandidateScore: 0.8, Delta: 0.3},
		{ID: "3", BaseScore: 0.5, CandidateScore: 0.8, Delta: 0.3},
		{ID: "4", BaseScore: 0.5, CandidateScore: 0.8, Delta: 0.3},
	}
	// n=1 (<2), n=3 (valid), n=2 (non-monotone), n=4 (valid), n=10 (>len)
	res := ComputePairedCISeries(pairs, []int{1, 3, 2, 4, 10}, 0.95, 42, 0.05)
	if len(res) != 2 {
		t.Fatalf("expected 2 valid points (n=3 and n=4), got %d", len(res))
	}
	if res[0].N != 3 || res[1].N != 4 {
		t.Errorf("expected points at N=3 and N=4, got N=%d and N=%d", res[0].N, res[1].N)
	}
}
