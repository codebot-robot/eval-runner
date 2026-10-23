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
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	metricsOnce sync.Once

	// SyncsTotal counts the number of repository sync attempts.
	SyncsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "evalrunner_forge_syncs_total",
			Help: "Total number of repository sync attempts against a forge.",
		},
		[]string{"repo", "status"},
	)

	// EventsTotal counts the number of typed forge events emitted.
	EventsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "evalrunner_forge_events_total",
			Help: "Total number of typed events emitted by the forge syncer.",
		},
		[]string{"repo", "kind"},
	)

	// ErrorsTotal counts the number of errors encountered during forge operations.
	ErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "evalrunner_forge_errors_total",
			Help: "Total number of errors encountered during forge operations.",
		},
		[]string{"repo", "phase"},
	)

	// StoreSizeBytes tracks the current size in bytes of the forge database file on disk.
	StoreSizeBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "evalrunner_forge_store_bytes",
			Help: "Current size in bytes of the forge bbolt database file.",
		},
		[]string{"path"},
	)

	// RateLimitRemaining tracks the remaining rate limit quota for a forge repository.
	RateLimitRemaining = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "evalrunner_forge_ratelimit_remaining",
			Help: "Current remaining rate limit quota for a forge.",
		},
		[]string{"repo"},
	)

	// RateLimitLimit tracks the total rate limit budget for a forge repository.
	RateLimitLimit = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "evalrunner_forge_ratelimit_limit",
			Help: "Total rate limit budget for a forge.",
		},
		[]string{"repo"},
	)

	// RateLimitResetTimestamp tracks the rate limit reset timestamp for a forge repository.
	RateLimitResetTimestamp = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "evalrunner_forge_ratelimit_reset_seconds",
			Help: "Rate limit reset Unix timestamp for a forge.",
		},
		[]string{"repo"},
	)
)

func initMetrics() {
	metricsOnce.Do(func() {
		ctrlmetrics.Registry.MustRegister(
			SyncsTotal,
			EventsTotal,
			ErrorsTotal,
			StoreSizeBytes,
			RateLimitRemaining,
			RateLimitLimit,
			RateLimitResetTimestamp,
		)
	})
}

func init() {
	initMetrics()
}
