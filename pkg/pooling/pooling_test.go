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
	"math"
	"testing"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExtractTaskCounts(t *testing.T) {
	rate := 0.75
	task1 := v1alpha1.TaskStatus{
		ID:          "task1",
		Phase:       v1alpha1.TaskPhaseCompleted,
		Samples:     8,
		Successes:   6,
		SuccessRate: &rate,
	}
	tc, ok := ExtractTaskCounts(task1)
	if !ok {
		t.Fatalf("expected ok=true for completed task")
	}
	if tc.Samples != 8 || tc.Successes != 6 {
		t.Errorf("got samples=%d, succ=%d; want 8, 6", tc.Samples, tc.Successes)
	}

	// Fallback when Successes is 0 but SuccessRate is set
	task2 := v1alpha1.TaskStatus{
		ID:          "task2",
		Phase:       v1alpha1.TaskPhaseCompleted,
		Samples:     8,
		Successes:   0,
		SuccessRate: &rate,
	}
	tc2, ok2 := ExtractTaskCounts(task2)
	if !ok2 {
		t.Fatalf("expected ok=true for completed task with rate fallback")
	}
	if tc2.Samples != 8 || tc2.Successes != 6 {
		t.Errorf("got samples=%d, succ=%d; want 8, 6 (from rate fallback)", tc2.Samples, tc2.Successes)
	}

	// Not completed or 0 samples
	taskRunning := v1alpha1.TaskStatus{
		ID:      "task3",
		Phase:   v1alpha1.TaskPhaseRunning,
		Samples: 4,
	}
	if _, ok := ExtractTaskCounts(taskRunning); ok {
		t.Errorf("expected ok=false for running task")
	}

	taskZeroSamples := v1alpha1.TaskStatus{
		ID:      "task4",
		Phase:   v1alpha1.TaskPhaseCompleted,
		Samples: 0,
	}
	if _, ok := ExtractTaskCounts(taskZeroSamples); ok {
		t.Errorf("expected ok=false for 0 samples")
	}
}

func TestPoolCommitRuns(t *testing.T) {
	cost1 := 0.12
	cost2 := 0.15

	run1 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name: "base-aaaaaaa",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/head-sha": "aaaaaaa",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{Commit: "aaaaaaa"},
		},
		Status: v1alpha1.RunStatus{
			Phase:   v1alpha1.RunPhaseCompleted,
			CostUSD: &cost1,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "taskA", Phase: v1alpha1.TaskPhaseCompleted, Samples: 8, Successes: 7},
				{ID: "taskB", Phase: v1alpha1.TaskPhaseCompleted, Samples: 8, Successes: 4},
			},
		},
	}

	run2 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name: "base-aaaaaaa-2",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/head-sha": "aaaaaaa",
				"evalrunner.gke-labs.dev/topup":    "2",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{Commit: "aaaaaaa"},
		},
		Status: v1alpha1.RunStatus{
			Phase:   v1alpha1.RunPhaseCompleted,
			CostUSD: &cost2,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "taskA", Phase: v1alpha1.TaskPhaseCompleted, Samples: 8, Successes: 8},
				{ID: "taskB", Phase: v1alpha1.TaskPhaseCompleted, Samples: 8, Successes: 5},
			},
		},
	}

	runOtherCommit := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name: "base-bbbbbbb",
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/head-sha": "bbbbbbb",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{Commit: "bbbbbbb"},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "taskA", Phase: v1alpha1.TaskPhaseCompleted, Samples: 8, Successes: 1},
			},
		},
	}

	pool := PoolCommitRuns([]*v1alpha1.Run{run1, run2, runOtherCommit}, "aaaaaaa")

	if len(pool.Runs) != 2 {
		t.Fatalf("expected 2 runs in pool for aaaaaaa, got %d", len(pool.Runs))
	}
	if pool.TotalSamples != 32 {
		t.Errorf("expected 32 total samples, got %d", pool.TotalSamples)
	}
	if pool.TotalSuccesses != 24 { // 7 + 4 + 8 + 5 = 24
		t.Errorf("expected 24 total successes, got %d", pool.TotalSuccesses)
	}
	if pool.SamplesPerTask["taskA"] != 16 {
		t.Errorf("expected 16 samples for taskA, got %d", pool.SamplesPerTask["taskA"])
	}
	if pool.SamplesPerTask["taskB"] != 16 {
		t.Errorf("expected 16 samples for taskB, got %d", pool.SamplesPerTask["taskB"])
	}
	if !pool.HasCost || math.Abs(pool.TotalCostUSD-0.27) > 1e-6 {
		t.Errorf("expected cost $0.27, got %v (hasCost=%v)", pool.TotalCostUSD, pool.HasCost)
	}

	est := pool.OverallEstimate()
	if est.N != 32 {
		t.Errorf("expected pooled estimate N=32, got %d", est.N)
	}
	expectedPoint := 24.0 / 32.0 // 0.75
	if math.Abs(est.Point-expectedPoint) > 1e-6 {
		t.Errorf("expected point %.4f, got %.4f", expectedPoint, est.Point)
	}

	hw := pool.OverallCIHalfWidth()
	expectedHW := (est.CI.Upper - est.CI.Lower) / 2.0
	if math.Abs(hw-expectedHW) > 1e-6 {
		t.Errorf("expected half-width %.4f, got %.4f", expectedHW, hw)
	}
}
