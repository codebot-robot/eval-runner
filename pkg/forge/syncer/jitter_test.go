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

func TestJitter_SymmetricBounds(t *testing.T) {
	base := 60 * time.Second
	factor := 0.20 // ±20% -> [48s, 72s]
	minExpected := 48 * time.Second
	maxExpected := 72 * time.Second

	var minSeen, maxSeen time.Duration
	minSeen = 100 * time.Hour

	for i := 0; i < 1000; i++ {
		j := Jitter(base, factor)
		if j < minExpected || j > maxExpected {
			t.Fatalf("jitter %v out of expected range [%v, %v]", j, minExpected, maxExpected)
		}
		if j < minSeen {
			minSeen = j
		}
		if j > maxSeen {
			maxSeen = j
		}
	}

	// Over 1000 trials, minSeen should be close to 48s and maxSeen close to 72s
	if minSeen > 52*time.Second {
		t.Errorf("expected minSeen close to 48s, got %v", minSeen)
	}
	if maxSeen < 68*time.Second {
		t.Errorf("expected maxSeen close to 72s, got %v", maxSeen)
	}
}

func TestJitter_EdgeCases(t *testing.T) {
	// Zero duration unchanged
	if got := Jitter(0, 0.20); got != 0 {
		t.Errorf("expected 0, got %v", got)
	}
	// Negative duration unchanged
	if got := Jitter(-10*time.Second, 0.20); got != -10*time.Second {
		t.Errorf("expected -10s, got %v", got)
	}
	// Zero or negative factor unchanged
	if got := Jitter(60*time.Second, 0); got != 60*time.Second {
		t.Errorf("expected 60s, got %v", got)
	}
	if got := Jitter(60*time.Second, -0.5); got != 60*time.Second {
		t.Errorf("expected 60s, got %v", got)
	}
	// Large factor capped at 0.99
	j := Jitter(60*time.Second, 2.0)
	if j <= 0 {
		t.Errorf("expected positive duration even with large factor, got %v", j)
	}
}
