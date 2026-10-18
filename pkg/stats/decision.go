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
)

// DefaultEquivalenceMargin is the default equivalence margin (±0.05).
const DefaultEquivalenceMargin = 0.05

// MinPairsToDecide is the minimum number of paired samples required before
// confidence-interval-based stopping rules (improved, regressed, unchanged) are evaluated.
// Below this threshold, sampling continues (unless sample cap or budget is reached)
// to prevent degenerate zero-width intervals (e.g. 1 win or 2 ties) from prematurely
// stopping execution on insufficient data.
const MinPairsToDecide = 8

// Decide evaluates whether sampling for a task should stop or continue according
// to the stopping rule defined in docs/concept/functionality.md §4.2:
//
// 1. If at least MinPairsToDecide paired samples have been run:
//   - CI on Δ excludes zero -> improved (CI strictly > 0) or regressed (CI strictly < 0).
//   - CI on Δ is entirely within ±margin -> unchanged.
//
// 2. Cap or budget reached -> inconclusive.
// 3. Otherwise -> continue, with suggested next batch size and estimated samples to resolve.
func Decide(est PairedEstimate, margin float64, samplesRun, sampleCap int, spent, budget float64) Decision {
	if margin < 0 {
		margin = DefaultEquivalenceMargin
	}

	// Rule 1 & Rule 2: CI-based stopping rules require a minimum number of paired samples
	// to avoid premature decisions on degenerate or tiny-sample intervals.
	if est.NumPairs >= MinPairsToDecide {
		if est.CI.Lower > 0 {
			return Decision{
				Label:  TaskLabelImproved,
				Stop:   true,
				Reason: fmt.Sprintf("CI [%+.2f, %+.2f] excludes zero: improved", est.CI.Lower, est.CI.Upper),
			}
		}
		if est.CI.Upper < 0 {
			return Decision{
				Label:  TaskLabelRegressed,
				Stop:   true,
				Reason: fmt.Sprintf("CI [%+.2f, %+.2f] excludes zero: regressed", est.CI.Lower, est.CI.Upper),
			}
		}

		if est.CI.Lower >= -margin && est.CI.Upper <= margin {
			return Decision{
				Label:  TaskLabelUnchanged,
				Stop:   true,
				Reason: fmt.Sprintf("CI [%+.2f, %+.2f] is entirely within equivalence margin ±%.2f: unchanged", est.CI.Lower, est.CI.Upper, margin),
			}
		}
	}

	// Calculate estimated additional samples needed for both inconclusive and continue decisions
	estMore := estimateNeededSamples(est, margin, samplesRun)

	// Rule 3: Cap or budget reached
	capReached := sampleCap > 0 && samplesRun >= sampleCap
	budgetReached := budget > 0 && spent >= budget

	if capReached || budgetReached {
		var reason string
		if capReached && budgetReached {
			reason = fmt.Sprintf("sample cap (%d) and budget ($%.2f) reached without resolving", sampleCap, budget)
		} else if capReached {
			reason = fmt.Sprintf("sample cap (%d) reached without resolving", sampleCap)
		} else {
			reason = fmt.Sprintf("budget ($%.2f) reached without resolving (spent $%.2f)", budget, spent)
		}

		return Decision{
			Label:            TaskLabelInconclusive,
			Stop:             true,
			Reason:           reason,
			EstimatedSamples: estMore,
		}
	}

	// Rule 4: Continue sampling
	nextBatch := suggestBatchSize(estMore, samplesRun, sampleCap)

	var reason string
	if est.NumPairs < MinPairsToDecide {
		reason = fmt.Sprintf("fewer than %d paired samples run (%d): continue", MinPairsToDecide, est.NumPairs)
	} else {
		reason = fmt.Sprintf("CI [%+.2f, %+.2f] spans zero and exceeds margin ±%.2f: continue", est.CI.Lower, est.CI.Upper, margin)
	}

	return Decision{
		Label:            TaskLabelContinue,
		Stop:             false,
		Reason:           reason,
		NextBatchSize:    nextBatch,
		EstimatedSamples: estMore,
	}
}

// estimateNeededSamples estimates how many additional samples are required to narrow
// the confidence interval to either exclude zero or fit entirely within ±margin.
func estimateNeededSamples(est PairedEstimate, margin float64, samplesRun int) int {
	if samplesRun < MinPairsToDecide {
		return MinPairsToDecide - samplesRun
	}

	width := est.CI.Upper - est.CI.Lower
	halfWidth := width / 2.0

	// Target half-width: to exclude zero, half-width must be < |meanDelta|;
	// to fit in margin, half-width must be <= margin.
	targetHalfWidth := margin
	absDelta := math.Abs(est.MeanDelta)
	if absDelta > targetHalfWidth {
		targetHalfWidth = absDelta
	}

	if halfWidth <= 0 || targetHalfWidth <= 0 {
		return 8
	}

	ratio := halfWidth / targetHalfWidth
	nTotal := int(math.Ceil(float64(samplesRun) * ratio * ratio))
	nMore := nTotal - samplesRun
	if nMore < 4 {
		nMore = 4
	}
	return nMore
}

// suggestBatchSize determines a sensible next batch size based on remaining needed samples.
func suggestBatchSize(estMore, samplesRun, sampleCap int) int {
	var batch int
	switch {
	case estMore <= 8:
		batch = 8
	case estMore <= 16:
		batch = 16
	case estMore <= 32:
		batch = 24
	default:
		batch = 32
	}

	if sampleCap > 0 {
		remaining := sampleCap - samplesRun
		if remaining < batch {
			batch = remaining
		}
		if batch < 1 {
			batch = 1
		}
	}

	return batch
}
