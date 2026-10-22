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
	"math/rand"
	"time"
)

// DefaultJitterFactor is the default symmetric jitter factor (±20%).
const DefaultJitterFactor = 0.20

// Jitter returns a duration randomized symmetrically within [d * (1 - factor), d * (1 + factor)].
// If factor <= 0 or d <= 0, d is returned unchanged.
// If factor >= 1.0, factor is capped at 0.99 to prevent negative durations.
func Jitter(d time.Duration, factor float64) time.Duration {
	if d <= 0 || factor <= 0 {
		return d
	}
	if factor >= 1.0 {
		factor = 0.99
	}
	minD := float64(d) * (1.0 - factor)
	maxD := float64(d) * (1.0 + factor)
	delta := maxD - minD
	return time.Duration(minD + rand.Float64()*delta)
}
