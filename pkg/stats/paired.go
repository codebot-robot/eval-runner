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
			MeanDelta:        0.0,
			CI:               Interval{Lower: 0.0, Upper: 0.0},
			BootstrapCI:      Interval{Lower: 0.0, Upper: 0.0},
			NormalCI:         Interval{Lower: 0.0, Upper: 0.0},
			PImprovement:     0.5,
			BaseMean:         0.0,
			CandidateMean:    0.0,
			Confidence:       confidence,
			NumPairs:         0,
			DroppedCandidate: droppedCandidate,
			DroppedBaseline:  droppedBaseline,
		}
	}

	var sumCand, sumBase, sumDiff float64
	for i := 0; i < numPairs; i++ {
		sumCand += candScores[i]
		sumBase += baseScores[i]
		sumDiff += diffs[i]
	}
	candMean := sumCand / float64(numPairs)
	baseMean := sumBase / float64(numPairs)
	meanDelta := sumDiff / float64(numPairs)

	bootCI := BootstrapPercentileCI(diffs, confidence, DefaultBootstrapReplicates, seed)
	normCI := NormalApproximationCI(diffs, confidence)
	pImp := BayesianBootstrapPImprovement(diffs, DefaultBayesianBootstrapDraws, seed+1)

	return PairedEstimate{
		MeanDelta:        meanDelta,
		CI:               bootCI, // Bootstrap percentile is the default
		BootstrapCI:      bootCI,
		NormalCI:         normCI,
		PImprovement:     pImp,
		BaseMean:         baseMean,
		CandidateMean:    candMean,
		Confidence:       confidence,
		NumPairs:         numPairs,
		DroppedCandidate: droppedCandidate,
		DroppedBaseline:  droppedBaseline,
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
			MeanDelta:    0.0,
			CI:           Interval{Lower: 0.0, Upper: 0.0},
			BootstrapCI:  Interval{Lower: 0.0, Upper: 0.0},
			NormalCI:     Interval{Lower: 0.0, Upper: 0.0},
			PImprovement: 0.5,
			Confidence:   confidence,
			NumPairs:     0,
		}
	}

	var sumDiff float64
	for _, d := range diffs {
		sumDiff += d
	}
	meanDelta := sumDiff / float64(numPairs)

	bootCI := BootstrapPercentileCI(diffs, confidence, DefaultBootstrapReplicates, seed)
	normCI := NormalApproximationCI(diffs, confidence)
	pImp := BayesianBootstrapPImprovement(diffs, DefaultBayesianBootstrapDraws, seed+1)

	return PairedEstimate{
		MeanDelta:    meanDelta,
		CI:           bootCI,
		BootstrapCI:  bootCI,
		NormalCI:     normCI,
		PImprovement: pImp,
		Confidence:   confidence,
		NumPairs:     numPairs,
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

// BayesianBootstrapPImprovement estimates the posterior probability that the true delta > 0
// using Rubin's Bayesian Bootstrap (Dirichlet(1, ..., 1) resampling weights).
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
	allPositive := true
	allNegative := true
	for _, d := range diffs {
		if d != 0 {
			allZero = false
		}
		if d <= 0 {
			allPositive = false
		}
		if d >= 0 {
			allNegative = false
		}
	}

	if allZero {
		return 0.5
	}
	if allPositive {
		return 1.0
	}
	if allNegative {
		return 0.0
	}

	if draws <= 0 {
		draws = DefaultBayesianBootstrapDraws
	}

	rng := rand.New(rand.NewSource(seed))
	var wins float64

	expWeights := make([]float64, n)

	for b := 0; b < draws; b++ {
		var sumExp float64
		for i := 0; i < n; i++ {
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
		for i := 0; i < n; i++ {
			weightedMean += (expWeights[i] / sumExp) * diffs[i]
		}

		if weightedMean > 0 {
			wins++
		} else if weightedMean == 0 {
			wins += 0.5
		}
	}

	return wins / float64(draws)
}
