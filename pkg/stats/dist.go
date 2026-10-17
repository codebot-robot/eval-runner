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

// NormalQuantile returns the quantile (inverse cumulative distribution function)
// of the standard normal distribution N(0, 1) for a given probability p in (0, 1).
func NormalQuantile(p float64) float64 {
	if p <= 0 {
		return math.Inf(-1)
	}
	if p >= 1 {
		return math.Inf(1)
	}
	// Phi(z) = 0.5 * (1 + erf(z / sqrt(2))) = p
	// erf(z / sqrt(2)) = 2p - 1
	// z = sqrt(2) * erfinv(2p - 1)
	return math.Sqrt2 * math.Erfinv(2*p-1)
}

// NormalCDF returns the cumulative distribution function of the standard normal distribution N(0, 1).
func NormalCDF(x float64) float64 {
	return 0.5 * (1.0 + math.Erf(x/math.Sqrt2))
}

// incompleteBetaCF computes the continued fraction for the regularized incomplete beta function
// using Lentz's method.
func incompleteBetaCF(a, b, x float64) float64 {
	const maxIterations = 200
	const epsilon = 1e-15
	const tiny = 1e-30

	qab := a + b
	qap := a + 1.0
	qam := a - 1.0

	c := 1.0
	d := 1.0 - qab*x/qap
	if math.Abs(d) < tiny {
		d = tiny
	}
	d = 1.0 / d
	h := d

	for m := 1; m <= maxIterations; m++ {
		mFloat := float64(m)
		m2 := 2.0 * mFloat

		// Even step
		aa := mFloat * (b - mFloat) * x / ((qam + m2) * (a + m2))
		d = 1.0 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1.0 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1.0 / d
		h *= d * c

		// Odd step
		aa = -(a + mFloat) * (qab + mFloat) * x / ((a + m2) * (qap + m2))
		d = 1.0 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1.0 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1.0 / d
		delH := d * c
		h *= delH

		if math.Abs(delH-1.0) <= epsilon {
			break
		}
	}

	return h
}

// IncompleteBeta returns the regularized incomplete beta function I_x(a, b).
func IncompleteBeta(a, b, x float64) float64 {
	if x <= 0 {
		return 0.0
	}
	if x >= 1 {
		return 1.0
	}

	// Factor before continued fraction: x^a * (1-x)^b / (a * Beta(a, b))
	lgammaAB, _ := math.Lgamma(a + b)
	lgammaA, _ := math.Lgamma(a)
	lgammaB, _ := math.Lgamma(b)

	bt := math.Exp(lgammaAB - lgammaA - lgammaB + a*math.Log(x) + b*math.Log(1.0-x))

	// Use symmetry relation if x > (a + 1) / (a + b + 2) for faster convergence
	if x < (a+1.0)/(a+b+2.0) {
		return bt * incompleteBetaCF(a, b, x) / a
	}
	return 1.0 - bt*incompleteBetaCF(b, a, 1.0-x)/b
}

// StudentTCDF returns the cumulative distribution function of Student's t-distribution
// with df degrees of freedom at value t.
func StudentTCDF(t, df float64) float64 {
	if df <= 0 {
		return math.NaN()
	}
	if math.IsInf(t, 1) {
		return 1.0
	}
	if math.IsInf(t, -1) {
		return 0.0
	}
	if t == 0 {
		return 0.5
	}

	x := df / (df + t*t)
	prob := 0.5 * IncompleteBeta(df/2.0, 0.5, x)
	if t > 0 {
		return 1.0 - prob
	}
	return prob
}

// StudentTPDF returns the probability density function of Student's t-distribution.
func StudentTPDF(t, df float64) float64 {
	if df <= 0 {
		return math.NaN()
	}
	lg1, _ := math.Lgamma((df + 1.0) / 2.0)
	lg2, _ := math.Lgamma(df / 2.0)
	coef := math.Exp(lg1-lg2) / math.Sqrt(math.Pi*df)
	return coef * math.Pow(1.0+t*t/df, -(df+1.0)/2.0)
}

// StudentTQuantile returns the quantile (inverse CDF) of Student's t-distribution
// with df degrees of freedom for probability p in (0, 1).
func StudentTQuantile(p, df float64) float64 {
	if df <= 0 || p <= 0 || p >= 1 {
		if p <= 0 {
			return math.Inf(-1)
		}
		if p >= 1 {
			return math.Inf(1)
		}
		return math.NaN()
	}
	if p == 0.5 {
		return 0.0
	}
	if p < 0.5 {
		return -StudentTQuantile(1.0-p, df)
	}

	// Exact closed forms for df = 1 and df = 2
	if df == 1.0 {
		return math.Tan(math.Pi * (p - 0.5))
	}
	if df == 2.0 {
		alpha := 2.0 * (1.0 - p)
		return math.Sqrt(2.0/(alpha*(2.0-alpha)) - 2.0)
	}

	// Initial estimate from Cornish-Fisher expansion
	z := NormalQuantile(p)
	z2 := z * z
	z3 := z2 * z
	z5 := z3 * z2

	t := z + (z3+z)/(4.0*df) + (5.0*z5+16.0*z3+3.0*z)/(96.0*df*df)

	// Refine using Newton-Raphson iterations
	for i := 0; i < 30; i++ {
		cdf := StudentTCDF(t, df)
		err := cdf - p
		if math.Abs(err) < 1e-13 {
			break
		}
		pdf := StudentTPDF(t, df)
		if pdf <= 0 || math.IsNaN(pdf) {
			break
		}
		step := err / pdf
		t -= step
	}

	return t
}
