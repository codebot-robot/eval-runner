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
	"testing"
)

func TestDecideStoppingRuleTableDriven(t *testing.T) {
	tests := []struct {
		name          string
		est           PairedEstimate
		margin        float64
		samplesRun    int
		sampleCap     int
		spent         float64
		budget        float64
		expectedLabel TaskLabel
		expectedStop  bool
	}{
		{
			name: "improved - CI strictly above zero",
			est: PairedEstimate{
				NumPairs:  96,
				MeanDelta: 0.23,
				CI:        Interval{Lower: 0.15, Upper: 0.31},
			},
			margin:        0.05,
			samplesRun:    96,
			sampleCap:     100,
			spent:         4.10,
			budget:        10.0,
			expectedLabel: TaskLabelImproved,
			expectedStop:  true,
		},
		{
			name: "regressed - CI strictly below zero",
			est: PairedEstimate{
				NumPairs:  40,
				MeanDelta: -0.18,
				CI:        Interval{Lower: -0.28, Upper: -0.08},
			},
			margin:        0.05,
			samplesRun:    40,
			sampleCap:     100,
			spent:         2.00,
			budget:        10.0,
			expectedLabel: TaskLabelRegressed,
			expectedStop:  true,
		},
		{
			name: "unchanged - CI entirely within equivalence margin [-0.05, +0.05]",
			est: PairedEstimate{
				NumPairs:  48,
				MeanDelta: 0.01,
				CI:        Interval{Lower: -0.04, Upper: 0.04},
			},
			margin:        0.05,
			samplesRun:    48,
			sampleCap:     100,
			spent:         2.50,
			budget:        10.0,
			expectedLabel: TaskLabelUnchanged,
			expectedStop:  true,
		},
		{
			name: "inconclusive - sample cap reached",
			est: PairedEstimate{
				NumPairs:  50,
				MeanDelta: 0.03,
				CI:        Interval{Lower: -0.08, Upper: 0.14},
			},
			margin:        0.05,
			samplesRun:    50,
			sampleCap:     50,
			spent:         2.50,
			budget:        10.0,
			expectedLabel: TaskLabelInconclusive,
			expectedStop:  true,
		},
		{
			name: "inconclusive - budget reached",
			est: PairedEstimate{
				NumPairs:  40,
				MeanDelta: 0.04,
				CI:        Interval{Lower: -0.06, Upper: 0.14},
			},
			margin:        0.05,
			samplesRun:    40,
			sampleCap:     100,
			spent:         10.00,
			budget:        10.0,
			expectedLabel: TaskLabelInconclusive,
			expectedStop:  true,
		},
		{
			name: "continue - spans zero and outside margin, budget and cap remain",
			est: PairedEstimate{
				NumPairs:  16,
				MeanDelta: 0.12,
				CI:        Interval{Lower: -0.06, Upper: 0.48},
			},
			margin:        0.05,
			samplesRun:    16,
			sampleCap:     100,
			spent:         1.00,
			budget:        10.0,
			expectedLabel: TaskLabelContinue,
			expectedStop:  false,
		},
		{
			name: "continue - n < MinPairsToDecide even with zero-width interval",
			est: PairedEstimate{
				NumPairs:  3,
				MeanDelta: 1.0,
				CI:        Interval{Lower: 1.0, Upper: 1.0},
			},
			margin:        0.05,
			samplesRun:    3,
			sampleCap:     100,
			spent:         0.10,
			budget:        10.0,
			expectedLabel: TaskLabelContinue,
			expectedStop:  false,
		},
		{
			name: "inconclusive - n < MinPairsToDecide but sample cap reached",
			est: PairedEstimate{
				NumPairs:  3,
				MeanDelta: 1.0,
				CI:        Interval{Lower: 1.0, Upper: 1.0},
			},
			margin:        0.05,
			samplesRun:    3,
			sampleCap:     3,
			spent:         0.10,
			budget:        10.0,
			expectedLabel: TaskLabelInconclusive,
			expectedStop:  true,
		},
		{
			name: "continue - zero samples run",
			est: PairedEstimate{
				NumPairs: 0,
			},
			margin:        0.05,
			samplesRun:    0,
			sampleCap:     100,
			spent:         0.0,
			budget:        10.0,
			expectedLabel: TaskLabelContinue,
			expectedStop:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dec := Decide(tc.est, tc.margin, tc.samplesRun, tc.sampleCap, tc.spent, tc.budget)
			if dec.Label != tc.expectedLabel {
				t.Errorf("Decide Label = %v, want %v", dec.Label, tc.expectedLabel)
			}
			if dec.Stop != tc.expectedStop {
				t.Errorf("Decide Stop = %v, want %v", dec.Stop, tc.expectedStop)
			}
			if !tc.expectedStop {
				if dec.NextBatchSize <= 0 {
					t.Errorf("Continue decision should suggest NextBatchSize > 0, got %d", dec.NextBatchSize)
				}
				if dec.EstimatedSamples <= 0 {
					t.Errorf("Continue decision should estimate samples needed > 0, got %d", dec.EstimatedSamples)
				}
			}
		})
	}
}
