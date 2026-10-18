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

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/discovery"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

// Unit test: a 500-task manifest keeps the serialized status small (< 3 KiB).
func TestStatusSize_500TaskManifestKeepsStatusSmall(t *testing.T) {
	const taskCount = 500
	tasks := make([]v1alpha1.ManifestTask, taskCount)
	for i := 0; i < taskCount; i++ {
		tasks[i] = v1alpha1.ManifestTask{
			ID:      fmt.Sprintf("evals/benchmark/task_number_%04d", i),
			Spec:    fmt.Sprintf("benchmark.py@task_%04d", i),
			Workdir: "evals/benchmark",
		}
	}
	manifest := &v1alpha1.Manifest{Tasks: tasks}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal manifest failed: %v", err)
	}

	// Verify old way (manifest in status) would have been huge (~60 KiB)
	if len(manifestBytes) < 30000 {
		t.Errorf("Expected 500-task manifest raw JSON to be large, got %d bytes", len(manifestBytes))
	}

	h := sha256.Sum256(manifestBytes)
	manifestHash := "sha256:" + hex.EncodeToString(h[:])

	const maxCappedIDs = 50
	taskIDs := make([]string, 0, maxCappedIDs)
	for i := 0; i < maxCappedIDs; i++ {
		taskIDs = append(taskIDs, tasks[i].ID)
	}

	runStatus := v1alpha1.RunStatus{
		Phase: v1alpha1.RunPhaseDiscovered,
		Discovery: &v1alpha1.RunDiscoveryStatus{
			ManifestPath:      "gs://my-bucket/runs/abcdef1234567890/manifest.json",
			ManifestHash:      manifestHash,
			TaskCount:         taskCount,
			EligibleTaskCount: taskCount,
			TaskIDs:           taskIDs,
		},
		Message: "Discovered 500 tasks",
	}

	serializedStatus, err := json.Marshal(runStatus)
	if err != nil {
		t.Fatalf("Marshal status failed: %v", err)
	}

	// Serialized status with capped taskIDs must be small (< 3 KiB)
	if len(serializedStatus) > 3072 {
		t.Errorf("Expected serialized status for 500 tasks to be < 3072 bytes, got %d bytes", len(serializedStatus))
	}
	if len(runStatus.Discovery.TaskIDs) != 50 {
		t.Errorf("Expected capped 50 task IDs in status, got %d", len(runStatus.Discovery.TaskIDs))
	}
}

// Unit test: restart recovery via sink loads manifest from URI and populates cache.
func TestRestartRecoveryViaSink(t *testing.T) {
	scheme := setupScheme(t)

	commit := "abc1234"
	sink := artifacts.NewInMemorySink()

	manifest := &v1alpha1.Manifest{
		Tasks: []v1alpha1.ManifestTask{
			{ID: "task-1", Spec: "eval.py@task1", Workdir: "evals"},
			{ID: "task-2", Spec: "eval.py@task2", Workdir: "evals"},
		},
	}
	manifestBytes, _ := json.Marshal(manifest)
	h := sha256.Sum256(manifestBytes)
	manifestHash := "sha256:" + hex.EncodeToString(h[:])

	uri, err := sink.StoreManifest(t.Context(), artifacts.RunMetadata{
		RunName:      "test-run",
		RunNamespace: "eval-runner",
		Commit:       commit,
		TaskCount:    2,
	}, manifestBytes)
	if err != nil {
		t.Fatalf("StoreManifest failed: %v", err)
	}

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: commit,
			},
			Model: "mockllm/model",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseRunning,
			Discovery: &v1alpha1.RunDiscoveryStatus{
				ManifestPath:      uri,
				ManifestHash:      manifestHash,
				TaskCount:         2,
				EligibleTaskCount: 2,
				TaskIDs:           []string{"task-1", "task-2"},
			},
			Tasks: []v1alpha1.TaskStatus{
				{ID: "task-1", Phase: v1alpha1.TaskPhasePending},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(run).
		Build()

	// New reconciler with empty cache (simulating restart)
	reconciler := &RunReconciler{
		Client:           cl,
		Scheme:           scheme,
		SandboxNamespace: "eval-runner-sandbox",
		ArtifactSink:     sink,
	}

	// Verify cache is initially empty
	if reconciler.getCachedManifest(run.Namespace+"/"+run.Name) != nil {
		t.Fatalf("Expected empty cache before load")
	}

	// Load manifest via getOrLoadManifest
	loaded, err := reconciler.getOrLoadManifest(t.Context(), run)
	if err != nil {
		t.Fatalf("getOrLoadManifest failed: %v", err)
	}
	if len(loaded.Tasks) != 2 {
		t.Fatalf("Expected 2 tasks loaded, got %d", len(loaded.Tasks))
	}

	// Verify cache is now populated
	cached := reconciler.getCachedManifest(run.Namespace + "/" + run.Name)
	if cached == nil || len(cached.Tasks) != 2 {
		t.Errorf("Expected manifest in cache after getOrLoadManifest")
	}
}

// Unit test: hash drift keeps the phase and reports.
func TestManifestHashDriftKeepsPhaseAndReports(t *testing.T) {
	scheme := setupScheme(t)

	commit := "abc1234"
	sink := artifacts.NewInMemorySink()

	// Store manifest A
	manifestA := &v1alpha1.Manifest{
		Tasks: []v1alpha1.ManifestTask{
			{ID: "task-drift", Spec: "eval.py@drift"},
		},
	}
	manifestABytes, _ := json.Marshal(manifestA)
	uri, err := sink.StoreManifest(t.Context(), artifacts.RunMetadata{
		RunName:      "test-run-drift",
		RunNamespace: "eval-runner",
		Commit:       commit,
		TaskCount:    1,
	}, manifestABytes)
	if err != nil {
		t.Fatalf("StoreManifest failed: %v", err)
	}

	// Run recorded a DIFFERENT hash (simulating drift)
	expectedHash := "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run-drift",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: commit,
			},
			Model: "mockllm/model",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseRunning,
			Discovery: &v1alpha1.RunDiscoveryStatus{
				ManifestPath:      uri,
				ManifestHash:      expectedHash,
				TaskCount:         1,
				EligibleTaskCount: 1,
				TaskIDs:           []string{"task-drift"},
			},
			Tasks: []v1alpha1.TaskStatus{
				{ID: "task-drift", Phase: v1alpha1.TaskPhasePending},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           cl,
		Scheme:           scheme,
		SandboxNamespace: "eval-runner-sandbox",
		ArtifactSink:     sink,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run-drift"}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if res.Requeue {
		t.Errorf("Did not expect immediate requeue on hash drift")
	}

	var updatedRun v1alpha1.Run
	if err := cl.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get updated run: %v", err)
	}

	// Phase MUST be kept as Running!
	if updatedRun.Status.Phase != v1alpha1.RunPhaseRunning {
		t.Errorf("Expected phase to remain Running on hash drift, got %s", updatedRun.Status.Phase)
	}

	// Message reports hash drift
	if !strings.Contains(updatedRun.Status.Message, "hash drift") {
		t.Errorf("Expected status.Message to report hash drift, got: %s", updatedRun.Status.Message)
	}
}

// Unit test: transient vs not-found Load error handling.
func TestTransientVsNotFoundLoad(t *testing.T) {
	scheme := setupScheme(t)
	commit := "abc1234"

	// 1. Transient Load error (timeout): requeues with message, keeps task sandboxes
	t.Run("TransientLoadError_KeepsSandboxesAndRequeues", func(t *testing.T) {
		transientSink := &failingSink{
			err:          context.DeadlineExceeded, // transient error
			commitPrefix: fmt.Sprintf("runs/%s/", commit),
		}

		run := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-run-transient",
				Namespace: "eval-runner",
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					URL:    "git://git-server/repo.git",
					Commit: commit,
				},
				Model: "mockllm/model",
			},
			Status: v1alpha1.RunStatus{
				Phase: v1alpha1.RunPhaseRunning,
				Discovery: &v1alpha1.RunDiscoveryStatus{
					ManifestPath: fmt.Sprintf("runs/%s/manifest.json", commit),
					ManifestHash: "sha256:abcdef",
					TaskCount:    1,
					TaskIDs:      []string{"task-1"},
				},
				Tasks: []v1alpha1.TaskStatus{
					{ID: "task-1", Phase: v1alpha1.TaskPhaseRunning},
				},
			},
		}

		// Existing task sandbox
		taskSBName := discovery.TaskSandboxNameForRun("test-run-transient", "task-1")
		taskSB := &sandboxv1beta1.Sandbox{
			ObjectMeta: metav1.ObjectMeta{
				Name:      taskSBName,
				Namespace: "eval-runner-sandbox",
				Labels: map[string]string{
					discovery.LabelRunName:      "test-run-transient",
					discovery.LabelRunNamespace: "eval-runner",
					discovery.LabelRole:         discovery.RoleSample,
				},
				Annotations: map[string]string{
					discovery.AnnotationTaskID: "task-1",
				},
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(run, taskSB).
			WithStatusSubresource(run).
			Build()

		reconciler := &RunReconciler{
			Client:           cl,
			Scheme:           scheme,
			SandboxNamespace: "eval-runner-sandbox",
			ArtifactSink:     transientSink,
		}

		req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run-transient"}}
		res, err := reconciler.Reconcile(t.Context(), req)
		if err != nil {
			t.Fatalf("Unexpected Reconcile error: %v", err)
		}
		if res.RequeueAfter == 0 {
			t.Errorf("Expected RequeueAfter > 0 on transient load error, got %v", res.RequeueAfter)
		}

		// Verify task sandbox was KEPT
		var checkSB sandboxv1beta1.Sandbox
		err = cl.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner-sandbox", Name: taskSBName}, &checkSB)
		if err != nil {
			t.Errorf("Expected task sandbox to be kept on transient error, got: %v", err)
		}

		// Verify Run phase remains Running
		var updatedRun v1alpha1.Run
		_ = cl.Get(t.Context(), req.NamespacedName, &updatedRun)
		if updatedRun.Status.Phase != v1alpha1.RunPhaseRunning {
			t.Errorf("Expected phase Running, got %s", updatedRun.Status.Phase)
		}
	})

	// 2. Not-found Load error: resets to Pending to re-discover
	t.Run("NotFoundLoadError_ResetsToPendingAndRediscover", func(t *testing.T) {
		notFoundSink := &failingSink{
			err:          artifacts.ErrNotFound,
			commitPrefix: fmt.Sprintf("runs/%s/", commit),
		}

		run := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-run-notfound",
				Namespace: "eval-runner",
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					URL:    "git://git-server/repo.git",
					Commit: commit,
				},
				Model: "mockllm/model",
			},
			Status: v1alpha1.RunStatus{
				Phase: v1alpha1.RunPhaseRunning,
				Discovery: &v1alpha1.RunDiscoveryStatus{
					ManifestPath: fmt.Sprintf("runs/%s/manifest.json", commit),
					ManifestHash: "sha256:abcdef",
					TaskCount:    1,
					TaskIDs:      []string{"task-1"},
				},
				Tasks: []v1alpha1.TaskStatus{
					{ID: "task-1", Phase: v1alpha1.TaskPhasePending},
				},
			},
		}

		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(run).
			WithStatusSubresource(run).
			Build()

		reconciler := &RunReconciler{
			Client:           cl,
			Scheme:           scheme,
			SandboxNamespace: "eval-runner-sandbox",
			ArtifactSink:     notFoundSink,
		}

		req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run-notfound"}}
		res, err := reconciler.Reconcile(t.Context(), req)
		if err != nil {
			t.Fatalf("Unexpected Reconcile error: %v", err)
		}
		if !res.Requeue {
			t.Errorf("Expected Requeue: true on reset to Pending")
		}

		var updatedRun v1alpha1.Run
		_ = cl.Get(t.Context(), req.NamespacedName, &updatedRun)
		if updatedRun.Status.Phase != v1alpha1.RunPhasePending {
			t.Errorf("Expected phase Pending on not-found manifest, got %s", updatedRun.Status.Phase)
		}
		if updatedRun.Status.DiscoveryDeadline != nil {
			t.Errorf("Expected DiscoveryDeadline to be cleared (nil) on reset, got %v", updatedRun.Status.DiscoveryDeadline)
		}
		if updatedRun.Status.Discovery != nil {
			t.Errorf("Expected Discovery to be cleared (nil) on reset")
		}
	})
}

// Unit test: deadline reset on re-discovery ensures late restarts re-discover instead of failing.
func TestDeadlineResetOnRediscovery(t *testing.T) {
	scheme := setupScheme(t)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	commit := "abc1234"

	// First discovery was 15 minutes ago, so old deadline expired 5 minutes ago!
	pastDeadline := metav1.NewTime(now.Add(-5 * time.Minute))

	sink := artifacts.NewInMemorySink() // non-durable sink, lost on restart

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run-late-restart",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: commit,
			},
			Model: "mockllm/model",
		},
		Status: v1alpha1.RunStatus{
			Phase:             v1alpha1.RunPhaseRunning,
			DiscoveryDeadline: &pastDeadline,
			Discovery: &v1alpha1.RunDiscoveryStatus{
				ManifestPath: fmt.Sprintf("mem://runs/%s/manifest.json", commit),
				ManifestHash: "sha256:123456",
				TaskCount:    1,
				TaskIDs:      []string{"task-1"},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           cl,
		Scheme:           scheme,
		SandboxNamespace: "eval-runner-sandbox",
		ArtifactSink:     sink,
		Now:              func() time.Time { return now },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run-late-restart"}}

	// Turn 1: Reconciler attempts to load manifest from empty in-memory sink -> triggers re-discovery
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Turn 1 error: %v", err)
	}
	if !res.Requeue {
		t.Errorf("Expected Requeue: true on reset to Pending")
	}

	var updatedRun v1alpha1.Run
	_ = cl.Get(t.Context(), req.NamespacedName, &updatedRun)
	if updatedRun.Status.Phase != v1alpha1.RunPhasePending {
		t.Fatalf("Expected phase Pending, got %s", updatedRun.Status.Phase)
	}
	// Verify old expired deadline was CLEARED!
	if updatedRun.Status.DiscoveryDeadline != nil {
		t.Fatalf("Expected DiscoveryDeadline to be cleared on re-discovery, got: %v", updatedRun.Status.DiscoveryDeadline)
	}

	// Turn 2: Next reconciliation begins discovery from scratch.
	// Because DiscoveryDeadline was cleared, it sets a FRESH deadline from now, NOT the expired one.
	_, err = reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Turn 2 error: %v", err)
	}

	_ = cl.Get(t.Context(), req.NamespacedName, &updatedRun)
	if updatedRun.Status.Phase != v1alpha1.RunPhaseDiscovering {
		t.Fatalf("Expected phase Discovering, got %s (did it fail due to old deadline?)", updatedRun.Status.Phase)
	}
	if updatedRun.Status.DiscoveryDeadline == nil {
		t.Fatalf("Expected fresh DiscoveryDeadline to be set")
	}
	// Fresh deadline must be in the FUTURE (now + 10m)
	if !updatedRun.Status.DiscoveryDeadline.After(now) {
		t.Errorf("Expected fresh deadline in future (%v), got past deadline %v", now.Add(10*time.Minute), updatedRun.Status.DiscoveryDeadline.Time)
	}
}

type failingSink struct {
	err          error
	commitPrefix string
}

func (f *failingSink) StoreArtifact(ctx context.Context, meta artifacts.RunMetadata, filename string, data []byte) (string, error) {
	return "", f.err
}

func (f *failingSink) StoreComparison(ctx context.Context, comparisonName string, data []byte) (string, error) {
	return "", f.err
}

func (f *failingSink) StoreManifest(ctx context.Context, meta artifacts.RunMetadata, data []byte) (string, error) {
	return "", f.err
}

func (f *failingSink) Load(ctx context.Context, path string) ([]byte, error) {
	return nil, f.err
}

func (f *failingSink) ArtifactPrefix(commit string) string {
	if f.commitPrefix != "" {
		return f.commitPrefix
	}
	return fmt.Sprintf("runs/%s/", commit)
}

func (f *failingSink) IsDurable() bool {
	return true
}
