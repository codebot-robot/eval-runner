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

package main

import (
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/cache"
)

func TestNewManagerOptions(t *testing.T) {
	tests := []struct {
		name        string
		leaderElect bool
	}{
		{
			name:        "leader election enabled",
			leaderElect: true,
		},
		{
			name:        "leader election disabled",
			leaderElect: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := newManagerOptions(scheme, cache.Options{}, tc.leaderElect, "eval-runner", ":8081")

			if opts.LeaderElection != tc.leaderElect {
				t.Errorf("LeaderElection = %v, want %v", opts.LeaderElection, tc.leaderElect)
			}
			if opts.LeaderElectionID != defaultLeaderElectionID {
				t.Errorf("LeaderElectionID = %q, want %q", opts.LeaderElectionID, defaultLeaderElectionID)
			}
			if opts.LeaderElectionNamespace != "eval-runner" {
				t.Errorf("LeaderElectionNamespace = %q, want %q", opts.LeaderElectionNamespace, "eval-runner")
			}
			if !opts.LeaderElectionReleaseOnCancel {
				t.Errorf("LeaderElectionReleaseOnCancel = false, want true")
			}
			if opts.LeaseDuration == nil || *opts.LeaseDuration != 60*time.Second {
				t.Errorf("LeaseDuration = %v, want %v", opts.LeaseDuration, 60*time.Second)
			}
			if opts.RenewDeadline == nil || *opts.RenewDeadline != 40*time.Second {
				t.Errorf("RenewDeadline = %v, want %v", opts.RenewDeadline, 40*time.Second)
			}
			if opts.RetryPeriod == nil || *opts.RetryPeriod != 8*time.Second {
				t.Errorf("RetryPeriod = %v, want %v", opts.RetryPeriod, 8*time.Second)
			}
			if opts.HealthProbeBindAddress != ":8081" {
				t.Errorf("HealthProbeBindAddress = %q, want %q", opts.HealthProbeBindAddress, ":8081")
			}
		})
	}
}
