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

func TestNormalQuantile(t *testing.T) {
	tests := []struct {
		p        float64
		expected float64
		tol      float64
	}{
		{p: 0.5, expected: 0.0, tol: 1e-12},
		{p: 0.8413447460685429, expected: 1.0, tol: 1e-6},
		{p: 0.975, expected: 1.959963984540054, tol: 1e-6},
		{p: 0.995, expected: 2.575829303548900, tol: 1e-6},
		{p: 0.025, expected: -1.959963984540054, tol: 1e-6},
	}

	for _, tc := range tests {
		got := NormalQuantile(tc.p)
		if math.Abs(got-tc.expected) > tc.tol {
			t.Errorf("NormalQuantile(%v) = %v, expected %v", tc.p, got, tc.expected)
		}
	}

	// Boundary cases
	if !math.IsInf(NormalQuantile(0), -1) {
		t.Errorf("NormalQuantile(0) should be -Inf")
	}
	if !math.IsInf(NormalQuantile(1), 1) {
		t.Errorf("NormalQuantile(1) should be +Inf")
	}
}

func TestNormalCDF(t *testing.T) {
	for _, p := range []float64{0.01, 0.1, 0.25, 0.5, 0.75, 0.9, 0.99} {
		z := NormalQuantile(p)
		gotP := NormalCDF(z)
		if math.Abs(gotP-p) > 1e-9 {
			t.Errorf("NormalCDF(NormalQuantile(%v)) = %v, want %v", p, gotP, p)
		}
	}
}

func TestStudentTQuantile(t *testing.T) {
	// Known two-tailed 95% critical values (p = 0.975)
	known975 := []struct {
		df       float64
		expected float64
	}{
		{df: 1, expected: 12.70620},
		{df: 2, expected: 4.30265},
		{df: 5, expected: 2.57058},
		{df: 9, expected: 2.26216},
		{df: 29, expected: 2.04523},
		{df: 99, expected: 1.98422},
	}

	for _, tc := range known975 {
		got := StudentTQuantile(0.975, tc.df)
		if math.Abs(got-tc.expected) > 0.001 {
			t.Errorf("StudentTQuantile(0.975, df=%v) = %v, want ~%v", tc.df, got, tc.expected)
		}

		// Verify inverse: StudentTCDF(StudentTQuantile(p)) == p
		cdf := StudentTCDF(got, tc.df)
		if math.Abs(cdf-0.975) > 1e-6 {
			t.Errorf("StudentTCDF(%v, df=%v) = %v, want 0.975", got, tc.df, cdf)
		}
	}

	// Symmetry: t(p) == -t(1-p)
	tLow := StudentTQuantile(0.025, 10)
	tHigh := StudentTQuantile(0.975, 10)
	if math.Abs(tLow+tHigh) > 1e-9 {
		t.Errorf("StudentTQuantile symmetry broken: %v vs %v", tLow, tHigh)
	}
}
