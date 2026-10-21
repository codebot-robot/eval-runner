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

import "math"

// DifferenceUnpaired computes the statistical difference delta = a.Point - b.Point
// and its two-sided confidence interval for independent (unpaired) samples.
//
// When both methods are "wilson" (binary proportions), it computes Newcombe's
// hybrid-Wilson score interval (Newcombe Method 10).
// When either method is "t" (continuous scores), it computes Welch's t-interval
// from (mean, sd, n).
//
// If confidence is not in (0, 1), it defaults to DefaultConfidence (0.95).
// The resulting bounds are clamped to [-1.0, 1.0].
func DifferenceUnpaired(a, b Estimate, confidence float64) DeltaEstimate {
	if confidence <= 0 || confidence >= 1 {
		confidence = DefaultConfidence
	}

	delta := a.Point - b.Point

	if a.Method == "t" || b.Method == "t" {
		return differenceWelch(a, b, delta, confidence)
	}
	return differenceNewcombe(a, b, delta, confidence)
}

func differenceNewcombe(a, b Estimate, delta, confidence float64) DeltaEstimate {
	if a.N <= 0 || b.N <= 0 {
		return DeltaEstimate{
			Delta:      delta,
			CI:         Interval{Lower: -1.0, Upper: 1.0},
			Confidence: confidence,
			Method:     "newcombe",
		}
	}

	l1, u1 := a.CI.Lower, a.CI.Upper
	if a.Confidence != confidence || a.CI == (Interval{}) {
		k1 := int(math.Round(a.Point * float64(a.N)))
		est1 := Proportion(k1, a.N, confidence)
		l1, u1 = est1.CI.Lower, est1.CI.Upper
	}

	l2, u2 := b.CI.Lower, b.CI.Upper
	if b.Confidence != confidence || b.CI == (Interval{}) {
		k2 := int(math.Round(b.Point * float64(b.N)))
		est2 := Proportion(k2, b.N, confidence)
		l2, u2 = est2.CI.Lower, est2.CI.Upper
	}

	p1 := a.Point
	p2 := b.Point

	dLower := math.Sqrt(math.Pow(p1-l1, 2) + math.Pow(u2-p2, 2))
	dUpper := math.Sqrt(math.Pow(u1-p1, 2) + math.Pow(p2-l2, 2))

	lower := math.Max(-1.0, delta-dLower)
	upper := math.Min(1.0, delta+dUpper)

	return DeltaEstimate{
		Delta:      delta,
		CI:         Interval{Lower: lower, Upper: upper},
		Confidence: confidence,
		Method:     "newcombe",
	}
}

func differenceWelch(a, b Estimate, delta, confidence float64) DeltaEstimate {
	// Sample size <= 1 on either side cannot estimate variance (df undefined or < 1).
	if a.N <= 1 || b.N <= 1 {
		return DeltaEstimate{
			Delta:      delta,
			CI:         Interval{Lower: -1.0, Upper: 1.0},
			Confidence: confidence,
			Method:     "welch",
		}
	}

	var sd1 float64
	if a.SD != nil {
		sd1 = *a.SD
	} else {
		// Fallback for mixed wilson/t comparisons or legacy status without successRateSD:
		// approximate sample standard deviation using Bernoulli variance with Bessel's correction n/(n-1).
		p := math.Max(0.0, math.Min(1.0, a.Point))
		sd1 = math.Sqrt(p * (1.0 - p) * float64(a.N) / float64(a.N-1))
	}

	var sd2 float64
	if b.SD != nil {
		sd2 = *b.SD
	} else {
		// Fallback for mixed wilson/t comparisons or legacy status without successRateSD:
		// approximate sample standard deviation using Bernoulli variance with Bessel's correction n/(n-1).
		p := math.Max(0.0, math.Min(1.0, b.Point))
		sd2 = math.Sqrt(p * (1.0 - p) * float64(b.N) / float64(b.N-1))
	}

	v1 := (sd1 * sd1) / float64(a.N)
	v2 := (sd2 * sd2) / float64(b.N)
	se := math.Sqrt(v1 + v2)

	if se == 0 {
		clamped := math.Max(-1.0, math.Min(1.0, delta))
		return DeltaEstimate{
			Delta:      clamped,
			CI:         Interval{Lower: clamped, Upper: clamped},
			Confidence: confidence,
			Method:     "welch",
		}
	}

	denom := (v1*v1)/float64(a.N-1) + (v2*v2)/float64(b.N-1)
	if denom == 0 {
		clamped := math.Max(-1.0, math.Min(1.0, delta))
		return DeltaEstimate{
			Delta:      clamped,
			CI:         Interval{Lower: clamped, Upper: clamped},
			Confidence: confidence,
			Method:     "welch",
		}
	}

	df := math.Pow(v1+v2, 2) / denom
	tCrit := StudentTQuantile((1.0+confidence)/2.0, df)
	margin := tCrit * se

	lower := math.Max(-1.0, delta-margin)
	upper := math.Min(1.0, delta+margin)

	return DeltaEstimate{
		Delta:      delta,
		CI:         Interval{Lower: lower, Upper: upper},
		Confidence: confidence,
		Method:     "welch",
	}
}
