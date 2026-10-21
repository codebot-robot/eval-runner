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
)

// DefaultConfidence is the standard two-sided confidence level (95%).
const DefaultConfidence = 0.95

// Proportion computes the sample success rate and the Wilson score confidence interval.
// If confidence is not in (0, 1), it defaults to DefaultConfidence (0.95).
//
// Why Wilson score interval over Wald:
// In agent evaluations, small sample sizes (e.g. n=10..96) and extreme success rates
// (near 0.0 or 1.0) are very common. The textbook Wald interval (p ± z * sqrt(p(1-p)/n))
// has notoriously poor coverage properties for small n, often collapses to a degenerate
// zero-width interval [0, 0] or [1, 1] when successes are 0 or n, and can produce bounds
// strictly outside [0, 1].
//
// In contrast, the Wilson score interval inverts the score test under the binomial distribution.
// It naturally respects the [0, 1] parameter boundary, provides non-degenerate intervals for
// 0/n and n/n, and maintains actual coverage close to nominal confidence even for small n.
func Proportion(successes, n int, confidence float64) Estimate {
	if confidence <= 0 || confidence >= 1 {
		confidence = DefaultConfidence
	}

	if n <= 0 {
		return Estimate{
			Point:      0.0,
			CI:         Interval{Lower: 0.0, Upper: 1.0},
			Confidence: confidence,
			N:          0,
			Method:     "wilson",
		}
	}

	if successes < 0 {
		successes = 0
	} else if successes > n {
		successes = n
	}

	nF := float64(n)
	p := float64(successes) / nF

	z := NormalQuantile((1.0 + confidence) / 2.0)
	z2 := z * z

	denom := 1.0 + z2/nF
	center := (p + z2/(2.0*nF)) / denom
	margin := (z / denom) * math.Sqrt(p*(1.0-p)/nF+z2/(4.0*nF*nF))

	lower := math.Max(0.0, center-margin)
	upper := math.Min(1.0, center+margin)

	return Estimate{
		Point:      p,
		CI:         Interval{Lower: lower, Upper: upper},
		Confidence: confidence,
		N:          n,
		Method:     "wilson",
	}
}

// Mean computes the sample mean and Student's t-distribution confidence interval
// for non-binary scores (such as Inspect partial credit scores in [0, 1]).
// If confidence is not in (0, 1), it defaults to DefaultConfidence (0.95).
//
// Caveats for tiny n:
//  1. With n=1, sample variance is mathematically undefined (df=0), so the confidence
//     interval spans the entire support [0.0, 1.0].
//  2. For very small n (e.g. n=2, 3), the t-distribution has heavy tails (t* ≈ 12.71 at n=2),
//     which yields very wide intervals.
//  3. Since scores are bounded in [0, 1], the underlying distribution is not strictly normal;
//     the resulting t-interval bounds are therefore clamped to [0.0, 1.0].
func Mean(scores []float64, confidence float64) Estimate {
	if confidence <= 0 || confidence >= 1 {
		confidence = DefaultConfidence
	}

	n := len(scores)
	if n == 0 {
		return Estimate{
			Point:      0.0,
			CI:         Interval{Lower: 0.0, Upper: 1.0},
			Confidence: confidence,
			N:          0,
			Method:     "t",
		}
	}

	if n == 1 {
		pt := math.Max(0.0, math.Min(1.0, scores[0]))
		return Estimate{
			Point:      pt,
			CI:         Interval{Lower: 0.0, Upper: 1.0},
			Confidence: confidence,
			N:          1,
			Method:     "t",
		}
	}

	var sum float64
	for _, s := range scores {
		sum += s
	}
	mean := sum / float64(n)

	var sumSq float64
	for _, s := range scores {
		d := s - mean
		sumSq += d * d
	}
	variance := sumSq / float64(n-1)
	sdVal := math.Sqrt(variance)
	se := math.Sqrt(variance / float64(n))

	if se == 0 {
		clamped := math.Max(0.0, math.Min(1.0, mean))
		return Estimate{
			Point:      clamped,
			CI:         Interval{Lower: clamped, Upper: clamped},
			Confidence: confidence,
			N:          n,
			Method:     "t",
			SD:         &sdVal,
		}
	}

	tCrit := StudentTQuantile((1.0+confidence)/2.0, float64(n-1))
	margin := tCrit * se

	lower := math.Max(0.0, mean-margin)
	upper := math.Min(1.0, mean+margin)

	return Estimate{
		Point:      mean,
		CI:         Interval{Lower: lower, Upper: upper},
		Confidence: confidence,
		N:          n,
		Method:     "t",
		SD:         &sdVal,
	}
}
