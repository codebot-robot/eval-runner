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
	"math/rand"
	"testing"
)

func TestWilsonCoverageSimulation(t *testing.T) {
	// Acceptance criterion:
	// A coverage simulation test (seeded): for a few true rates and n,
	// the 95% Wilson interval covers the truth close to 95% of the time.
	const trials = 1500
	const nominalConf = 0.95

	trueRates := []float64{0.10, 0.50, 0.85}
	sampleSizes := []int{15, 40, 100}

	for _, p := range trueRates {
		for _, n := range sampleSizes {
			rng := rand.New(rand.NewSource(1337 + int64(p*1000) + int64(n)))
			covered := 0

			for i := 0; i < trials; i++ {
				successes := 0
				for s := 0; s < n; s++ {
					if rng.Float64() < p {
						successes++
					}
				}

				est := Proportion(successes, n, nominalConf)
				if est.CI.Lower <= p && p <= est.CI.Upper {
					covered++
				}
			}

			empiricalCoverage := float64(covered) / float64(trials)
			t.Logf("true p=%.2f, n=%d: empirical coverage = %.3f", p, n, empiricalCoverage)

			// Wilson coverage on discrete binomial distributions is typically within [0.91, 0.985]
			if empiricalCoverage < 0.91 || empiricalCoverage > 0.985 {
				t.Errorf("Wilson coverage for p=%.2f, n=%d is %.3f, expected close to 0.95 (in [0.91, 0.985])",
					p, n, empiricalCoverage)
			}
		}
	}
}
