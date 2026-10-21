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
	"testing"
)

func TestDifferenceUnpaired_NewcombePublishedValues(t *testing.T) {
	// Reference from Robert G. Newcombe (1998): "Interval estimation for the difference
	// between independent proportions: comparison of eleven methods", Statistics in Medicine 17:
	// Sample 1: 56/70 (p1 = 0.80)
	// Sample 2: 48/80 (p2 = 0.60)
	// delta = +0.20, 95% Newcombe hybrid-Wilson interval: [0.0524, 0.3339]
	a := Proportion(56, 70, 0.95)
	b := Proportion(48, 80, 0.95)

	delta := DifferenceUnpaired(a, b, 0.95)

	if delta.Method != "newcombe" {
		t.Fatalf("expected method 'newcombe', got %q", delta.Method)
	}
	if math.Abs(delta.Delta-0.20) > 1e-6 {
		t.Errorf("expected delta 0.20, got %f", delta.Delta)
	}

	expectedLower := 0.052431
	expectedUpper := 0.333873
	if math.Abs(delta.CI.Lower-expectedLower) > 0.001 {
		t.Errorf("expected lower ~%f, got %f", expectedLower, delta.CI.Lower)
	}
	if math.Abs(delta.CI.Upper-expectedUpper) > 0.001 {
		t.Errorf("expected upper ~%f, got %f", expectedUpper, delta.CI.Upper)
	}
}

func TestDifferenceUnpaired_WelchScipyReference(t *testing.T) {
	// Reference computed with scipy.stats.ttest_ind(s1, s2, equal_var=False).confidence_interval(0.95)
	// Sample 1: [0.8, 0.7, 0.9, 0.8, 0.85] (n=5, mean=0.81, sd=0.07416198)
	// Sample 2: [0.5, 0.6, 0.55, 0.65] (n=4, mean=0.575, sd=0.06454972)
	// delta = 0.81 - 0.575 = 0.235
	// Welch df = 6.905761
	// 95% Welch t interval: [0.125266, 0.344734]
	s1 := []float64{0.8, 0.7, 0.9, 0.8, 0.85}
	s2 := []float64{0.5, 0.6, 0.55, 0.65}

	a := Mean(s1, 0.95)
	b := Mean(s2, 0.95)

	if a.SD == nil || b.SD == nil {
		t.Fatalf("expected non-nil SD on Mean estimates: a.SD=%v, b.SD=%v", a.SD, b.SD)
	}

	delta := DifferenceUnpaired(a, b, 0.95)

	if delta.Method != "welch" {
		t.Fatalf("expected method 'welch', got %q", delta.Method)
	}
	if math.Abs(delta.Delta-0.235) > 1e-6 {
		t.Errorf("expected delta 0.235, got %f", delta.Delta)
	}

	expectedLower := 0.125266
	expectedUpper := 0.344734
	if math.Abs(delta.CI.Lower-expectedLower) > 0.001 {
		t.Errorf("expected lower ~%f, got %f", expectedLower, delta.CI.Lower)
	}
	if math.Abs(delta.CI.Upper-expectedUpper) > 0.001 {
		t.Errorf("expected upper ~%f, got %f", expectedUpper, delta.CI.Upper)
	}
}

func TestDifferenceUnpaired_DegenerateN1(t *testing.T) {
	// When either or both samples have n <= 1, variance is undefined or cannot estimate df.
	// Welch must return the full support interval [-1.0, 1.0].
	aSingle := Mean([]float64{0.8}, 0.95) // n=1
	bMulti := Mean([]float64{0.5, 0.6, 0.7}, 0.95)

	delta := DifferenceUnpaired(aSingle, bMulti, 0.95)
	if delta.Method != "welch" {
		t.Errorf("expected method 'welch', got %s", delta.Method)
	}
	if delta.CI.Lower != -1.0 || delta.CI.Upper != 1.0 {
		t.Errorf("expected [-1.0, 1.0] for n=1 Welch, got [%f, %f]", delta.CI.Lower, delta.CI.Upper)
	}

	deltaRev := DifferenceUnpaired(bMulti, aSingle, 0.95)
	if deltaRev.CI.Lower != -1.0 || deltaRev.CI.Upper != 1.0 {
		t.Errorf("expected [-1.0, 1.0] for n=1 Welch reverse, got [%f, %f]", deltaRev.CI.Lower, deltaRev.CI.Upper)
	}

	bSingle := Mean([]float64{0.4}, 0.95)
	deltaBoth := DifferenceUnpaired(aSingle, bSingle, 0.95)
	if deltaBoth.CI.Lower != -1.0 || deltaBoth.CI.Upper != 1.0 {
		t.Errorf("expected [-1.0, 1.0] when both have n=1, got [%f, %f]", deltaBoth.CI.Lower, deltaBoth.CI.Upper)
	}
}

func TestDifferenceUnpaired_DegenerateN0(t *testing.T) {
	a0 := Estimate{Point: 0.0, CI: Interval{Lower: 0.0, Upper: 1.0}, Confidence: 0.95, N: 0, Method: "wilson"}
	b := Proportion(10, 20, 0.95)

	delta := DifferenceUnpaired(a0, b, 0.95)
	if delta.Method != "newcombe" {
		t.Errorf("expected method 'newcombe', got %s", delta.Method)
	}
	if delta.CI.Lower != -1.0 || delta.CI.Upper != 1.0 {
		t.Errorf("expected [-1.0, 1.0] for n=0 Newcombe, got [%f, %f]", delta.CI.Lower, delta.CI.Upper)
	}
}

func TestDifferenceUnpaired_MixedWilsonAndT(t *testing.T) {
	// When one estimate is "wilson" and the other is "t", Welch must be used.
	aWilson := Proportion(18, 20, 0.95)               // method "wilson"
	bT := Mean([]float64{0.5, 0.6, 0.55, 0.65}, 0.95) // method "t"

	delta := DifferenceUnpaired(aWilson, bT, 0.95)
	if delta.Method != "welch" {
		t.Errorf("expected method 'welch' when one estimate is 't', got %s", delta.Method)
	}
	if delta.Delta <= 0 {
		t.Errorf("expected positive delta, got %f", delta.Delta)
	}

	deltaRev := DifferenceUnpaired(bT, aWilson, 0.95)
	if deltaRev.Method != "welch" {
		t.Errorf("expected method 'welch' when one estimate is 't' (reversed), got %s", deltaRev.Method)
	}
	if deltaRev.Delta >= 0 {
		t.Errorf("expected negative delta, got %f", deltaRev.Delta)
	}
}

func TestDifferenceUnpaired_ZeroVariance(t *testing.T) {
	// Identical scores in both groups -> standard error = 0
	a := Mean([]float64{0.8, 0.8, 0.8}, 0.95)
	b := Mean([]float64{0.6, 0.6, 0.6}, 0.95)

	delta := DifferenceUnpaired(a, b, 0.95)
	if delta.Method != "welch" {
		t.Errorf("expected method 'welch', got %s", delta.Method)
	}
	if math.Abs(delta.Delta-0.2) > 1e-6 {
		t.Errorf("expected delta 0.2, got %f", delta.Delta)
	}
	if math.Abs(delta.CI.Lower-0.2) > 1e-6 || math.Abs(delta.CI.Upper-0.2) > 1e-6 {
		t.Errorf("expected [0.2, 0.2] for zero-variance Welch, got [%f, %f]", delta.CI.Lower, delta.CI.Upper)
	}
}
