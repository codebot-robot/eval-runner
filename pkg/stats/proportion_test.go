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

func TestProportionWilsonKnownValues(t *testing.T) {
	// Acceptance criterion: Wilson 95% for 7/10 is approximately [0.397, 0.892]
	est7_10 := Proportion(7, 10, 0.95)
	if math.Abs(est7_10.Point-0.70) > 1e-9 {
		t.Errorf("Point estimate for 7/10 = %v, want 0.70", est7_10.Point)
	}
	if math.Abs(est7_10.CI.Lower-0.397) > 0.005 {
		t.Errorf("Wilson 95%% CI lower for 7/10 = %v, want ~0.397", est7_10.CI.Lower)
	}
	if math.Abs(est7_10.CI.Upper-0.892) > 0.005 {
		t.Errorf("Wilson 95%% CI upper for 7/10 = %v, want ~0.892", est7_10.CI.Upper)
	}

	// 0/10: sane bounded interval [0.0, ~0.278]
	est0_10 := Proportion(0, 10, 0.95)
	if est0_10.Point != 0.0 {
		t.Errorf("Point estimate for 0/10 = %v, want 0.0", est0_10.Point)
	}
	if est0_10.CI.Lower != 0.0 {
		t.Errorf("Wilson lower for 0/10 = %v, want 0.0", est0_10.CI.Lower)
	}
	if est0_10.CI.Upper <= 0 || est0_10.CI.Upper > 0.35 {
		t.Errorf("Wilson upper for 0/10 = %v, want sane bound ~0.278", est0_10.CI.Upper)
	}
	if math.Abs(est0_10.CI.Upper-0.278) > 0.005 {
		t.Errorf("Wilson upper for 0/10 = %v, want ~0.278", est0_10.CI.Upper)
	}

	// 10/10: sane bounded interval [~0.722, 1.0]
	est10_10 := Proportion(10, 10, 0.95)
	if est10_10.Point != 1.0 {
		t.Errorf("Point estimate for 10/10 = %v, want 1.0", est10_10.Point)
	}
	if est10_10.CI.Upper != 1.0 {
		t.Errorf("Wilson upper for 10/10 = %v, want 1.0", est10_10.CI.Upper)
	}
	if est10_10.CI.Lower < 0.65 || est10_10.CI.Lower >= 1.0 {
		t.Errorf("Wilson lower for 10/10 = %v, want sane bound ~0.722", est10_10.CI.Lower)
	}
	if math.Abs(est10_10.CI.Lower-0.722) > 0.005 {
		t.Errorf("Wilson lower for 10/10 = %v, want ~0.722", est10_10.CI.Lower)
	}

	// Edge cases: n=0, negative successes, clamped successes
	est0 := Proportion(0, 0, 0.95)
	if est0.CI.Lower != 0.0 || est0.CI.Upper != 1.0 {
		t.Errorf("Proportion(0, 0) should return [0, 1], got %v", est0.CI)
	}

	estNeg := Proportion(-2, 10, 0.95)
	if estNeg.Point != 0.0 || estNeg.CI.Lower != 0.0 {
		t.Errorf("Negative successes should clamp to 0, got %v", estNeg)
	}

	estOver := Proportion(15, 10, 0.95)
	if estOver.Point != 1.0 || estOver.CI.Upper != 1.0 {
		t.Errorf("Overflow successes should clamp to n, got %v", estOver)
	}
}

func TestMeanStudentT(t *testing.T) {
	// Empty scores
	emptyEst := Mean([]float64{}, 0.95)
	if emptyEst.N != 0 || emptyEst.CI.Lower != 0.0 || emptyEst.CI.Upper != 1.0 {
		t.Errorf("Mean of empty slice should be [0, 1], got %v", emptyEst)
	}

	// Single score: n=1 caveat
	singleEst := Mean([]float64{0.75}, 0.95)
	if singleEst.N != 1 || singleEst.Point != 0.75 || singleEst.CI.Lower != 0.0 || singleEst.CI.Upper != 1.0 {
		t.Errorf("Mean of single score should be point=0.75 and CI=[0, 1], got %v", singleEst)
	}

	// Identical scores: variance = 0
	sameEst := Mean([]float64{0.8, 0.8, 0.8, 0.8}, 0.95)
	if sameEst.Point != 0.8 || sameEst.CI.Lower != 0.8 || sameEst.CI.Upper != 0.8 {
		t.Errorf("Identical scores should have zero-width CI around mean, got %v", sameEst)
	}

	// Sample with non-binary scores
	scores := []float64{0.2, 0.4, 0.6, 0.8} // mean=0.5, var=0.066667, s=0.2582, se=0.1291, df=3, t*=3.1824
	est := Mean(scores, 0.95)
	if math.Abs(est.Point-0.5) > 1e-9 {
		t.Errorf("Mean point = %v, want 0.5", est.Point)
	}
	expectedMargin := 3.182446 * (0.2581988897 / 2.0)
	expectedLower := 0.5 - expectedMargin
	expectedUpper := 0.5 + expectedMargin
	if math.Abs(est.CI.Lower-expectedLower) > 0.01 {
		t.Errorf("Mean lower CI = %v, want ~%v", est.CI.Lower, expectedLower)
	}
	if math.Abs(est.CI.Upper-expectedUpper) > 0.01 {
		t.Errorf("Mean upper CI = %v, want ~%v", est.CI.Upper, expectedUpper)
	}
}
