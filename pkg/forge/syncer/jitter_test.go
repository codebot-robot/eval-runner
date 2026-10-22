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

package syncer

import (
	"testing"
	"time"
)

func TestJitterBounds(t *testing.T) {
	base := 60 * time.Second
	factor := 0.20 // ±20%: [48s, 72s]
	minExpected := 48 * time.Second
	maxExpected := 72 * time.Second

	var observedMin time.Duration = 100 * time.Hour
	var observedMax time.Duration = 0

	const iterations = 1000
	for i := 0; i < iterations; i++ {
		j := Jitter(base, factor)
		if j < minExpected {
			t.Fatalf("iteration %d: jitter duration %v was below minimum %v", i, j, minExpected)
		}
		if j > maxExpected {
			t.Fatalf("iteration %d: jitter duration %v was above maximum %v", i, j, maxExpected)
		}
		if j < observedMin {
			observedMin = j
		}
		if j > observedMax {
			observedMax = j
		}
	}

	// Verify dispersion (not returning a fixed constant)
	if observedMax-observedMin < 10*time.Second {
		t.Errorf("expected jitter variance across %d iterations, got span %v (min: %v, max: %v)", iterations, observedMax-observedMin, observedMin, observedMax)
	}

	// Edge cases
	if got := Jitter(0, 0.2); got != 0 {
		t.Errorf("Jitter(0, 0.2) = %v, want 0", got)
	}
	if got := Jitter(base, 0); got != base {
		t.Errorf("Jitter(base, 0) = %v, want %v", got, base)
	}
	if got := Jitter(base, -0.5); got != base {
		t.Errorf("Jitter(base, -0.5) = %v, want %v", got, base)
	}
	if got := Jitter(base, 1.5); got <= 0 {
		t.Errorf("Jitter(base, 1.5) = %v, want positive duration", got)
	}
}
