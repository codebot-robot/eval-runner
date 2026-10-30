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

package pooling

import (
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/stats"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFormatTitle_SpecificationExample(t *testing.T) {
	// Head commit: 10 successes out of 16 samples
	est := stats.PooledWilsonFromTotals(10, 16, 0.95)

	// Parent delta: +2 percentage points with CI [-0.09, +0.13]
	lastMove := &stats.DeltaEstimate{
		Delta:      0.02,
		CI:         stats.Interval{Lower: -0.09, Upper: 0.13},
		Confidence: 0.95,
		Method:     "unpaired-welch",
	}

	got := FormatTitle(&est, lastMove)
	want := "63% ± 21 · n=16 · Δ vs parent +2 (95% CI \u22129…+13)"
	if got != want {
		t.Errorf("FormatTitle() = %q, want %q", got, want)
	}

	// Without parent move
	gotNoParent := FormatTitle(&est, nil)
	wantNoParent := "63% ± 21 · n=16"
	if gotNoParent != wantNoParent {
		t.Errorf("FormatTitle() without parent = %q, want %q", gotNoParent, wantNoParent)
	}
}

func TestPoolRunsForCommit_SeedAndTopUp(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	rate1 := 5.0 / 8.0
	rate2 := 5.0 / 8.0

	seedRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name: "base-abc1234",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/head-sha": "abc123456789",
				"evalrunner.gke-labs.dev/branch":   "main",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": now.Format(time.RFC3339),
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-1",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     8,
					Successes:   5,
					SuccessRate: &rate1,
					Tokens: &v1alpha1.TaskTokens{
						Total: 400,
					},
				},
			},
		},
	}

	topupRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name: "base-abc1234-2",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/head-sha": "abc123456789",
				"evalrunner.gke-labs.dev/branch":   "main",
				"evalrunner.gke-labs.dev/topup":    "2",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-1",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     8,
					Successes:   5,
					SuccessRate: &rate2,
					Tokens: &v1alpha1.TaskTokens{
						Total: 400,
					},
				},
			},
		},
	}

	pool := PoolRunsForCommit([]*v1alpha1.Run{seedRun, topupRun})
	if pool.CommitSHA != "abc123456789" {
		t.Errorf("CommitSHA = %q, want abc123456789", pool.CommitSHA)
	}
	if len(pool.Tasks) != 1 {
		t.Fatalf("len(pool.Tasks) = %d, want 1", len(pool.Tasks))
	}
	task := pool.Tasks[0]
	if task.Samples != 16 {
		t.Errorf("task.Samples = %d, want 16", task.Samples)
	}
	if task.Successes != 10 {
		t.Errorf("task.Successes = %d, want 10", task.Successes)
	}
	if task.Tokens == nil || task.Tokens.Total != 800 {
		t.Errorf("task.Tokens.Total = %+v, want 800", task.Tokens)
	}
	if pool.OverallEstimate == nil || pool.OverallEstimate.N != 16 {
		t.Fatalf("OverallEstimate = %+v, want N=16", pool.OverallEstimate)
	}
}

func TestComputeSeriesLastMove(t *testing.T) {
	t1 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	parentRate := 10.0 / 16.0
	parentRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name: "base-parent",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/head-sha": "parent1111",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": t1.Format(time.RFC3339),
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-1",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     16,
					Successes:   10,
					SuccessRate: &parentRate,
				},
			},
		},
	}

	headRate := 12.0 / 16.0
	headRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name: "base-head",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/head-sha": "head2222",
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": t2.Format(time.RFC3339),
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-1",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     16,
					Successes:   12,
					SuccessRate: &headRate,
				},
			},
		},
	}

	delta, str, chip, tooltip := ComputeSeriesLastMove([]*v1alpha1.Run{parentRun, headRun})
	if delta == nil {
		t.Fatalf("expected non-nil delta")
	}
	if str == "—" || chip == "" || tooltip == "" {
		t.Errorf("unexpected empty return values: str=%q chip=%q tooltip=%q", str, chip, tooltip)
	}
}
