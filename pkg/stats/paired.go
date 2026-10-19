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
	"math/rand"
	"sort"
)

const (
	// DefaultSeed is the default random seed used for deterministic bootstrap sampling.
	DefaultSeed = 42

	// DefaultBootstrapReplicates is the number of bootstrap resamples for percentile CI.
	DefaultBootstrapReplicates = 2000

	// DefaultBayesianBootstrapDraws is the number of Dirichlet weight draws for P(improvement).
	DefaultBayesianBootstrapDraws = 5000

	// tieEpsilon is the numerical tolerance used for identifying tied differences and mean equivalence.
	tieEpsilon = 1e-9
)

// ComparePaired performs a paired comparison between candidate (PR) scores and baseline scores
// on matching sample IDs. Unpaired samples are tracked and reported as dropped.
func ComparePaired(candidate, baseline []SampleScore, confidence float64) PairedEstimate {
	return ComparePairedWithSeed(candidate, baseline, confidence, DefaultSeed)
}

// ComparePairedWithSeed performs a paired comparison using an explicit random seed for determinism.
func ComparePairedWithSeed(candidate, baseline []SampleScore, confidence float64, seed int64) PairedEstimate {
	if confidence <= 0 || confidence >= 1 {
		confidence = DefaultConfidence
	}

	// Index baseline samples by sample ID using a queue for potential duplicates.
	baseQueue := make(map[string][]float64)
	for _, b := range baseline {
		baseQueue[b.ID] = append(baseQueue[b.ID], b.Score)
	}

	var diffs []float64
	var candScores []float64
	var baseScores []float64
	droppedCandidate := 0

	for _, c := range candidate {
		scores, ok := baseQueue[c.ID]
		if ok && len(scores) > 0 {
			baseScore := scores[0]
			baseQueue[c.ID] = scores[1:]
			candScores = append(candScores, c.Score)
			baseScores = append(baseScores, baseScore)
			diffs = append(diffs, c.Score-baseScore)
		} else {
			droppedCandidate++
		}
	}

	droppedBaseline := 0
	for _, scores := range baseQueue {
		droppedBaseline += len(scores)
	}

	numPairs := len(diffs)
	if numPairs == 0 {
		return PairedEstimate{
			MeanDelta:          0.0,
			CI:                 Interval{Lower: 0.0, Upper: 0.0},
			BootstrapCI:        Interval{Lower: 0.0, Upper: 0.0},
			NormalCI:           Interval{Lower: 0.0, Upper: 0.0},
			PImprovement:       0.5,
			NormalPImprovement: 0.5,
			BaseMean:           0.0,
			CandidateMean:      0.0,
			Confidence:         confidence,
			NumPairs:           0,
			Wins:               0,
			Losses:             0,
			Ties:               0,
			DroppedCandidate:   droppedCandidate,
			DroppedBaseline:    droppedBaseline,
		}
	}

	var sumCand, sumBase, sumDiff float64
	var wins, losses, ties int
	for i := 0; i < numPairs; i++ {
		sumCand += candScores[i]
		sumBase += baseScores[i]
		sumDiff += diffs[i]
		if diffs[i] > tieEpsilon {
			wins++
		} else if diffs[i] < -tieEpsilon {
			losses++
		} else {
			ties++
		}
	}
	candMean := sumCand / float64(numPairs)
	baseMean := sumBase / float64(numPairs)
	meanDelta := sumDiff / float64(numPairs)

	bootCI := BootstrapPercentileCI(diffs, confidence, DefaultBootstrapReplicates, seed)
	normCI := NormalApproximationCI(diffs, confidence)
	pImp := BayesianBootstrapPImprovement(diffs, DefaultBayesianBootstrapDraws, seed+1)
	normPImp := NormalApproximationPImprovement(diffs)

	return PairedEstimate{
		MeanDelta:          meanDelta,
		CI:                 bootCI, // Bootstrap percentile is the default
		BootstrapCI:        bootCI,
		NormalCI:           normCI,
		PImprovement:       pImp,
		NormalPImprovement: normPImp,
		BaseMean:           baseMean,
		CandidateMean:      candMean,
		Confidence:         confidence,
		NumPairs:           numPairs,
		Wins:               wins,
		Losses:             losses,
		Ties:               ties,
		DroppedCandidate:   droppedCandidate,
		DroppedBaseline:    droppedBaseline,
	}
}

// CompareDifferences computes paired estimates directly from a slice of paired differences (candidate - base).
func CompareDifferences(diffs []float64, confidence float64, seed int64) PairedEstimate {
	if confidence <= 0 || confidence >= 1 {
		confidence = DefaultConfidence
	}

	numPairs := len(diffs)
	if numPairs == 0 {
		return PairedEstimate{
			MeanDelta:          0.0,
			CI:                 Interval{Lower: 0.0, Upper: 0.0},
			BootstrapCI:        Interval{Lower: 0.0, Upper: 0.0},
			NormalCI:           Interval{Lower: 0.0, Upper: 0.0},
			PImprovement:       0.5,
			NormalPImprovement: 0.5,
			Confidence:         confidence,
			NumPairs:           0,
			Wins:               0,
			Losses:             0,
			Ties:               0,
		}
	}

	var sumDiff float64
	var wins, losses, ties int
	for _, d := range diffs {
		sumDiff += d
		if d > tieEpsilon {
			wins++
		} else if d < -tieEpsilon {
			losses++
		} else {
			ties++
		}
	}
	meanDelta := sumDiff / float64(numPairs)

	bootCI := BootstrapPercentileCI(diffs, confidence, DefaultBootstrapReplicates, seed)
	normCI := NormalApproximationCI(diffs, confidence)
	pImp := BayesianBootstrapPImprovement(diffs, DefaultBayesianBootstrapDraws, seed+1)
	normPImp := NormalApproximationPImprovement(diffs)

	return PairedEstimate{
		MeanDelta:          meanDelta,
		CI:                 bootCI,
		BootstrapCI:        bootCI,
		NormalCI:           normCI,
		PImprovement:       pImp,
		NormalPImprovement: normPImp,
		Confidence:         confidence,
		NumPairs:           numPairs,
		Wins:               wins,
		Losses:             losses,
		Ties:               ties,
	}
}

// BootstrapPercentileCI computes a non-parametric bootstrap percentile confidence interval
// for the mean of the provided differences using a fixed random seed.
//
// Why Bootstrap Percentile is the default:
// Paired differences in eval benchmarks frequently take discrete values in [-1, +1]
// (e.g. -1 for regression, 0 for tie, +1 for win) or are skewed partial credits.
// The percentile bootstrap does not assume normality, respects asymmetry in the empirical
// distribution of differences, and naturally produces bounds confined to the empirical data range.
//
// Note on degenerate intervals:
// When all differences are identical or when n=1, all resampled means are identical, yielding
// a zero-width interval [d, d]. The stopping rule in Decide explicitly guards against this by
// requiring at least MinPairsToDecide paired samples before confidence-interval-based stopping
// rules can fire.
func BootstrapPercentileCI(diffs []float64, confidence float64, replicates int, seed int64) Interval {
	n := len(diffs)
	if n == 0 {
		return Interval{Lower: 0.0, Upper: 0.0}
	}
	if n == 1 {
		return Interval{Lower: diffs[0], Upper: diffs[0]}
	}

	allSame := true
	for i := 1; i < n; i++ {
		if diffs[i] != diffs[0] {
			allSame = false
			break
		}
	}
	if allSame {
		return Interval{Lower: diffs[0], Upper: diffs[0]}
	}

	if replicates <= 0 {
		replicates = DefaultBootstrapReplicates
	}

	rng := rand.New(rand.NewSource(seed))
	means := make([]float64, replicates)
	nF := float64(n)

	for b := 0; b < replicates; b++ {
		var bSum float64
		for i := 0; i < n; i++ {
			idx := rng.Intn(n)
			bSum += diffs[idx]
		}
		means[b] = bSum / nF
	}

	sort.Float64s(means)

	alpha := 1.0 - confidence
	loIndex := (alpha / 2.0) * float64(replicates-1)
	hiIndex := (1.0 - alpha/2.0) * float64(replicates-1)

	interpolate := func(idx float64) float64 {
		i0 := int(math.Floor(idx))
		i1 := int(math.Ceil(idx))
		if i0 < 0 {
			i0 = 0
		}
		if i1 >= replicates {
			i1 = replicates - 1
		}
		if i0 == i1 {
			return means[i0]
		}
		frac := idx - float64(i0)
		return means[i0]*(1.0-frac) + means[i1]*frac
	}

	lower := interpolate(loIndex)
	upper := interpolate(hiIndex)

	return Interval{Lower: lower, Upper: upper}
}

// NormalApproximationCI computes the classical normal approximation confidence interval
// for the mean of the paired differences: mean ± z * (s / sqrt(n)).
func NormalApproximationCI(diffs []float64, confidence float64) Interval {
	n := len(diffs)
	if n == 0 {
		return Interval{Lower: 0.0, Upper: 0.0}
	}
	if n == 1 {
		return Interval{Lower: diffs[0], Upper: diffs[0]}
	}

	var sum float64
	for _, d := range diffs {
		sum += d
	}
	mean := sum / float64(n)

	var sumSq float64
	for _, d := range diffs {
		diff := d - mean
		sumSq += diff * diff
	}
	variance := sumSq / float64(n-1)
	se := math.Sqrt(variance / float64(n))

	if se == 0 {
		return Interval{Lower: mean, Upper: mean}
	}

	z := NormalQuantile((1.0 + confidence) / 2.0)
	margin := z * se

	return Interval{
		Lower: mean - margin,
		Upper: mean + margin,
	}
}

// NormalApproximationPImprovement computes the probability of improvement under the
// classical normal approximation: P(Δ > 0) + 0.5 · P(Δ = 0), splitting ties evenly.
func NormalApproximationPImprovement(diffs []float64) float64 {
	n := len(diffs)
	if n == 0 {
		return 0.5
	}

	allZero := true
	for _, d := range diffs {
		if math.Abs(d) > tieEpsilon {
			allZero = false
			break
		}
	}
	if allZero {
		return 0.5
	}

	var sum float64
	for _, d := range diffs {
		sum += d
	}
	mean := sum / float64(n)

	var sumSq float64
	for _, d := range diffs {
		diff := d - mean
		sumSq += diff * diff
	}
	variance := sumSq / float64(n-1)
	se := math.Sqrt(variance / float64(n))

	if se == 0 {
		if mean > tieEpsilon {
			return 1.0
		} else if mean < -tieEpsilon {
			return 0.0
		}
		return 0.5
	}

	// For a continuous normal distribution N(mean, se^2), P(Δ = 0) = 0,
	// and P(Δ > 0) = 1 - Φ(-mean/se) = Φ(mean / se).
	z := mean / se
	return NormalCDF(z)
}

// BayesianBootstrapPImprovement estimates the posterior probability of improvement
// as P(Δ > 0) + 0.5 · P(Δ = 0) (ties split evenly) using Rubin's Bayesian Bootstrap
// (Dirichlet(1, ..., 1) resampling weights).
//
// To prevent zero posterior probability when no wins or no losses have been observed
// among ties (e.g. 0W/1L/19T where non-parametric bootstrap mass sits entirely on non-positive
// outcomes), the posterior is regularised with a uniform prior consisting of two symmetric
// pseudo-observations at +1.0 and -1.0. For binary differences, this corresponds analytically
// to P(Beta(W+1, L+1) > 0.5):
//   - 0 wins, 1 loss, 19 ties -> P(Beta(1, 2) > 0.5) = 0.25
//   - 1 win, 0 losses, 19 ties -> P(Beta(2, 1) > 0.5) = 0.75
//   - 3 wins, 2 losses, 15 ties -> P(Beta(4, 3) > 0.5) = 0.65625
//
// Why Bayesian Bootstrap over Beta-Binomial:
// The Bayesian Bootstrap operates directly on continuous differences, seamlessly supporting
// Inspect scorers that return partial credit in [0, 1] without loss of information through
// discretizing into win/loss/tie buckets.
func BayesianBootstrapPImprovement(diffs []float64, draws int, seed int64) float64 {
	n := len(diffs)
	if n == 0 {
		return 0.5
	}

	allZero := true
	for _, d := range diffs {
		if math.Abs(d) > tieEpsilon {
			allZero = false
			break
		}
	}

	if allZero {
		return 0.5
	}

	if draws <= 0 {
		draws = DefaultBayesianBootstrapDraws
	}

	// Regularise posterior with symmetric uniform prior (+1.0, -1.0)
	augmented := make([]float64, n+2)
	copy(augmented, diffs)
	augmented[n] = 1.0
	augmented[n+1] = -1.0
	m := n + 2

	rng := rand.New(rand.NewSource(seed))
	var wins float64

	expWeights := make([]float64, m)

	for b := 0; b < draws; b++ {
		var sumExp float64
		for i := 0; i < m; i++ {
			// Generate standard exponential variable: -ln(U)
			u := rng.Float64()
			if u < 1e-15 {
				u = 1e-15
			}
			e := -math.Log(u)
			expWeights[i] = e
			sumExp += e
		}

		var weightedMean float64
		for i := 0; i < m; i++ {
			weightedMean += (expWeights[i] / sumExp) * augmented[i]
		}

		if weightedMean > tieEpsilon {
			wins++
		} else if math.Abs(weightedMean) <= tieEpsilon {
			wins += 0.5
		}
	}

	return wins / float64(draws)
}

// ComputePairedCISeries evaluates paired delta estimates and confidence intervals
// for prefix slices of pairs at each specified n in nValues using a fixed random seed.
// Only valid sample sizes (2 <= n <= len(pairs)) are evaluated.
// Duplicate or non-increasing n values are ignored to ensure monotone progression.
// If confidence <= 0 or >= 1, DefaultConfidence (0.95) is used.
// If margin <= 0, DefaultEquivalenceMargin (0.05) is used.
// If sampleCap > 0, it is passed to Decide so boundaries reaching or exceeding the cap
// can resolve to inconclusive if unresolved.
func ComputePairedCISeries(pairs []PairedSampleScore, nValues []int, confidence float64, seed int64, margin float64, sampleCap int) []PairedCISeriesPoint {
	if len(pairs) < 2 {
		return nil
	}
	if confidence <= 0 || confidence >= 1 {
		confidence = DefaultConfidence
	}
	if margin <= 0 {
		margin = DefaultEquivalenceMargin
	}

	var series []PairedCISeriesPoint
	lastN := 0

	for _, n := range nValues {
		if n < 2 || n > len(pairs) || n <= lastN {
			continue
		}
		sub := pairs[:n]
		cand := make([]SampleScore, n)
		base := make([]SampleScore, n)
		for i := 0; i < n; i++ {
			cand[i] = SampleScore{ID: sub[i].ID, Score: sub[i].CandidateScore}
			base[i] = SampleScore{ID: sub[i].ID, Score: sub[i].BaseScore}
		}

		est := ComparePairedWithSeed(cand, base, confidence, seed)
		ciWidth := est.CI.Upper - est.CI.Lower
		dec := Decide(est, margin, n, sampleCap, 0, 0)

		series = append(series, PairedCISeriesPoint{
			N:        n,
			Estimate: est,
			CIWidth:  ciWidth,
			Decision: dec,
		})
		lastN = n
	}

	return series
}
