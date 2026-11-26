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
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/discovery"
	"github.com/gke-labs/eval-runner/pkg/sandboxclient"
)

func createReadySandboxPod(t *testing.T, kubeClient client.Client, sandboxName, podIP string) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: podIP,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
	if err := kubeClient.Create(t.Context(), pod); err != nil {
		t.Fatalf("create ready sandbox pod: %v", err)
	}
}

func TestReconcileAsyncSandboxTask_StartPollRunningAndCompleted(t *testing.T) {
	scheme := setupScheme(t)
	manifest := &v1alpha1.Manifest{
		Tasks: []v1alpha1.ManifestTask{{
			ID:        "task-async",
			Spec:      "eval.py@async",
			Image:     "custom-task-image:v1",
			Command:   []string{"run-eval", "--fast"},
			Resources: []v1alpha1.ManifestResource{{Type: v1alpha1.ResourceTypeSandbox}},
		}},
	}
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "run-async-happy", Namespace: "eval-runner"},
		Spec:       v1alpha1.RunSpec{Repo: v1alpha1.RepoSpec{URL: "git://repo", Commit: "commit1"}, Model: "mockllm/model"},
		Status:     v1alpha1.RunStatus{Phase: v1alpha1.RunPhaseRunning},
	}

	fakeClient := &sandboxclient.FakeClient{
		ReadFileFn: func(ctx context.Context, path string) ([]byte, error) {
			if path == "/out/results.json" {
				return []byte(`{"schemaVersion": 1, "samples": [{"id": "s1", "passed": true, "score": 1.0}]}`), nil
			}
			return nil, fmt.Errorf("unexpected file %q", path)
		},
	}
	sink := artifacts.NewInMemorySink()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).WithStatusSubresource(run).Build()
	pollInterval := 7 * time.Second
	reconciler := &RunReconciler{
		Client:           kubeClient,
		Scheme:           scheme,
		InstanceID:       "controller-1",
		SandboxNamespace: "eval-runner-sandbox",
		SandboxClient:    fakeClient,
		ArtifactSink:     sink,
		Config:           ClusterConfig{PollProgressInterval: pollInterval},
	}
	reconciler.SetCachedManifest(run.Namespace, run.Name, manifest)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}

	// Turn 1: create sandbox and mark task Running.
	if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("turn 1 error: %v", err)
	}
	var sandboxList sandboxv1beta1.SandboxList
	if err := kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox")); err != nil || len(sandboxList.Items) != 1 {
		t.Fatalf("expected 1 sandbox, got %d (err=%v)", len(sandboxList.Items), err)
	}
	createReadySandboxPod(t, kubeClient, sandboxList.Items[0].Name, "10.244.1.10")

	// Turn 2: ensure /workspace/out, call StartAsync, persist ExecutionID, keep sandbox, requeue.
	result, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("turn 2 error: %v", err)
	}
	if !result.Requeue {
		t.Errorf("turn 2 expected Requeue=true, got %+v", result)
	}
	if len(fakeClient.Calls) != 1 || strings.Join(fakeClient.Calls[0].Command, " ") != "mkdir -p /workspace/out" {
		t.Errorf("expected mkdir -p /workspace/out call, got %+v", fakeClient.Calls)
	}
	if len(fakeClient.StartAsyncCalls) != 1 {
		t.Fatalf("expected 1 StartAsync call, got %d", len(fakeClient.StartAsyncCalls))
	}
	expectedExecutionID := asyncTaskExecutionID(run.Name, v1alpha1.TaskStatus{ID: "task-async", Restarts: 0})
	if fakeClient.StartAsyncCalls[0].ExecutionID != expectedExecutionID {
		t.Errorf("StartAsync executionID = %q, want %q", fakeClient.StartAsyncCalls[0].ExecutionID, expectedExecutionID)
	}

	var updatedRun v1alpha1.Run
	if err := kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("get run after turn 2: %v", err)
	}
	if updatedRun.Status.Tasks[0].Phase != v1alpha1.TaskPhaseRunning {
		t.Fatalf("expected Running after turn 2, got %s", updatedRun.Status.Tasks[0].Phase)
	}
	if updatedRun.Status.Tasks[0].ExecutionID != expectedExecutionID {
		t.Fatalf("ExecutionID = %q, want %q", updatedRun.Status.Tasks[0].ExecutionID, expectedExecutionID)
	}

	// Turn 3: PollAsync returns AsyncTaskRunning -> keep Running, requeue after pollProgressInterval.
	result, err = reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("turn 3 error: %v", err)
	}
	if result.RequeueAfter != pollInterval {
		t.Errorf("turn 3 RequeueAfter = %v, want %v", result.RequeueAfter, pollInterval)
	}
	if len(fakeClient.PollAsyncCalls) != 1 || fakeClient.PollAsyncCalls[0] != expectedExecutionID {
		t.Errorf("unexpected PollAsync calls: %v", fakeClient.PollAsyncCalls)
	}
	if err := kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox")); err != nil || len(sandboxList.Items) != 1 {
		t.Fatalf("expected sandbox preserved while running, got %d (err=%v)", len(sandboxList.Items), err)
	}

	// Turn 4: PollAsync returns AsyncTaskCompleted (exit 0) -> finalize artifact, mark Completed, clear ExecutionID, delete sandbox.
	fakeClient.SetAsyncTaskStatus(expectedExecutionID, sandboxclient.AsyncTaskStatus{
		State:    sandboxclient.AsyncTaskCompleted,
		ExitCode: 0,
	})
	result, err = reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("turn 4 error: %v", err)
	}
	if !result.Requeue {
		t.Errorf("turn 4 expected Requeue=true, got %+v", result)
	}
	if err := kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("get run after turn 4: %v", err)
	}
	task := updatedRun.Status.Tasks[0]
	if task.Phase != v1alpha1.TaskPhaseCompleted {
		t.Errorf("expected Completed, got %s (message=%q)", task.Phase, task.Message)
	}
	if task.ExecutionID != "" {
		t.Errorf("expected ExecutionID cleared, got %q", task.ExecutionID)
	}
	if task.Samples != 1 || task.Successes != 1 || task.LogPath == "" {
		t.Errorf("unexpected task stats/logPath: samples=%d successes=%d logPath=%q", task.Samples, task.Successes, task.LogPath)
	}
	if err := kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox")); err != nil || len(sandboxList.Items) != 0 {
		t.Errorf("expected sandbox deleted on completion, got %d (err=%v)", len(sandboxList.Items), err)
	}
}

func TestReconcileAsyncSandboxTask_StartAlreadyExistsIsIdempotent(t *testing.T) {
	scheme := setupScheme(t)
	manifest := &v1alpha1.Manifest{
		Tasks: []v1alpha1.ManifestTask{{
			ID:        "task-idempotent",
			Image:     "custom-task-image:v1",
			Command:   []string{"run-eval"},
			Resources: []v1alpha1.ManifestResource{{Type: v1alpha1.ResourceTypeSandbox}},
		}},
	}
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "run-async-idempotent", Namespace: "eval-runner"},
		Spec:       v1alpha1.RunSpec{Repo: v1alpha1.RepoSpec{URL: "git://repo", Commit: "c1"}, Model: "mockllm/model"},
		Status:     v1alpha1.RunStatus{Phase: v1alpha1.RunPhaseRunning},
	}

	expectedExecutionID := asyncTaskExecutionID(run.Name, v1alpha1.TaskStatus{ID: "task-idempotent", Restarts: 0})
	fakeClient := &sandboxclient.FakeClient{}
	fakeClient.SetAsyncTaskStatus(expectedExecutionID, sandboxclient.AsyncTaskStatus{State: sandboxclient.AsyncTaskRunning})

	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).WithStatusSubresource(run).Build()
	reconciler := &RunReconciler{
		Client:           kubeClient,
		Scheme:           scheme,
		SandboxNamespace: "eval-runner-sandbox",
		SandboxClient:    fakeClient,
	}
	reconciler.SetCachedManifest(run.Namespace, run.Name, manifest)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}

	if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("turn 1 error: %v", err)
	}
	var sandboxList sandboxv1beta1.SandboxList
	_ = kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox"))
	createReadySandboxPod(t, kubeClient, sandboxList.Items[0].Name, "10.244.1.11")

	// StartAsync hits ErrAsyncTaskAlreadyExists on FakeClient -> treated as idempotent success.
	result, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("turn 2 error: %v", err)
	}
	if !result.Requeue {
		t.Errorf("expected Requeue=true, got %+v", result)
	}
	var updatedRun v1alpha1.Run
	_ = kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun)
	if updatedRun.Status.Tasks[0].Phase != v1alpha1.TaskPhaseRunning {
		t.Errorf("expected Running, got %s", updatedRun.Status.Tasks[0].Phase)
	}
	if updatedRun.Status.Tasks[0].ExecutionID != expectedExecutionID {
		t.Errorf("ExecutionID = %q, want %q", updatedRun.Status.Tasks[0].ExecutionID, expectedExecutionID)
	}
}

func TestReconcileAsyncSandboxTask_NonZeroExitBoundedStderr(t *testing.T) {
	scheme := setupScheme(t)
	manifest := &v1alpha1.Manifest{
		Tasks: []v1alpha1.ManifestTask{{
			ID:        "task-nonzero",
			Image:     "custom-task-image:v1",
			Command:   []string{"run-eval"},
			Resources: []v1alpha1.ManifestResource{{Type: v1alpha1.ResourceTypeSandbox}},
		}},
	}
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "run-async-nonzero", Namespace: "eval-runner"},
		Spec:       v1alpha1.RunSpec{Repo: v1alpha1.RepoSpec{URL: "git://repo", Commit: "c1"}, Model: "mockllm/model"},
		Status:     v1alpha1.RunStatus{Phase: v1alpha1.RunPhaseRunning},
	}

	var stderrBuilder strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&stderrBuilder, "stderr line %d\n", i)
	}
	fakeClient := &sandboxclient.FakeClient{
		PollAsyncFn: func(ctx context.Context, executionID string) (*sandboxclient.AsyncTaskStatus, error) {
			return &sandboxclient.AsyncTaskStatus{
				State:    sandboxclient.AsyncTaskCompleted,
				ExitCode: 7,
				Stderr:   stderrBuilder.String(),
			}, nil
		},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).WithStatusSubresource(run).Build()
	reconciler := &RunReconciler{
		Client:           kubeClient,
		Scheme:           scheme,
		SandboxNamespace: "eval-runner-sandbox",
		SandboxClient:    fakeClient,
	}
	reconciler.SetCachedManifest(run.Namespace, run.Name, manifest)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}

	_, _ = reconciler.Reconcile(t.Context(), req)
	var sandboxList sandboxv1beta1.SandboxList
	_ = kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox"))
	createReadySandboxPod(t, kubeClient, sandboxList.Items[0].Name, "10.244.1.12")

	// Start then poll non-zero exit.
	if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("turn 2 error: %v", err)
	}
	if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("turn 3 error: %v", err)
	}

	var updatedRun v1alpha1.Run
	_ = kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun)
	task := updatedRun.Status.Tasks[0]
	if task.Phase != v1alpha1.TaskPhaseFailed {
		t.Fatalf("expected Failed, got %s", task.Phase)
	}
	if task.ExecutionID != "" {
		t.Errorf("expected ExecutionID cleared, got %q", task.ExecutionID)
	}
	if !strings.Contains(task.Message, "exit code 7") || !strings.Contains(task.Message, "stderr line 50") {
		t.Errorf("expected exit code and tail stderr, got %q", task.Message)
	}
	if strings.Contains(task.Message, "stderr line 1\n") {
		t.Errorf("expected stderr to be bounded to tail lines, got %q", task.Message)
	}
	_ = kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox"))
	if len(sandboxList.Items) != 0 {
		t.Errorf("expected sandbox deleted after non-zero exit, found %d", len(sandboxList.Items))
	}
}

func TestReconcileAsyncSandboxTask_NotFoundAndPollErrors(t *testing.T) {
	scheme := setupScheme(t)

	testCases := []struct {
		name                string
		initialFailures     int
		pollFn              func(ctx context.Context, executionID string) (*sandboxclient.AsyncTaskStatus, error)
		wantPhase           v1alpha1.TaskPhase
		wantRestarts        int
		wantFailureAttempts int
		wantSubstring       string
	}{
		{
			name:            "AsyncTaskNotFound retries within bound",
			initialFailures: 0,
			pollFn: func(ctx context.Context, executionID string) (*sandboxclient.AsyncTaskStatus, error) {
				return &sandboxclient.AsyncTaskStatus{State: sandboxclient.AsyncTaskNotFound}, nil
			},
			wantPhase:           v1alpha1.TaskPhasePending,
			wantRestarts:        1,
			wantFailureAttempts: 1,
			wantSubstring:       "not found in sandbox",
		},
		{
			name:            "AsyncTaskNotFound fails terminally at max attempts",
			initialFailures: maxTaskAttempts - 1,
			pollFn: func(ctx context.Context, executionID string) (*sandboxclient.AsyncTaskStatus, error) {
				return &sandboxclient.AsyncTaskStatus{State: sandboxclient.AsyncTaskNotFound}, nil
			},
			wantPhase:           v1alpha1.TaskPhaseFailed,
			wantRestarts:        0,
			wantFailureAttempts: maxTaskAttempts,
			wantSubstring:       "failed after 3 attempts",
		},
		{
			name:            "Transient poll error retries within bound",
			initialFailures: 0,
			pollFn: func(ctx context.Context, executionID string) (*sandboxclient.AsyncTaskStatus, error) {
				return nil, status.Error(codes.Unavailable, "sandboxd disconnected")
			},
			wantPhase:           v1alpha1.TaskPhasePending,
			wantRestarts:        1,
			wantFailureAttempts: 1,
			wantSubstring:       "sandboxd disconnected",
		},
		{
			name:            "Unauthenticated poll error fails immediately without infra retry",
			initialFailures: 0,
			pollFn: func(ctx context.Context, executionID string) (*sandboxclient.AsyncTaskStatus, error) {
				return nil, status.Error(codes.Unauthenticated, "invalid token")
			},
			wantPhase:           v1alpha1.TaskPhaseFailed,
			wantRestarts:        0,
			wantFailureAttempts: 0,
			wantSubstring:       "unauthenticated",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			manifest := &v1alpha1.Manifest{
				Tasks: []v1alpha1.ManifestTask{{
					ID:        "task-poll-err",
					Image:     "custom-task-image:v1",
					Command:   []string{"run-eval"},
					Resources: []v1alpha1.ManifestResource{{Type: v1alpha1.ResourceTypeSandbox}},
				}},
			}
			startedAt := metav1.NewTime(time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC))
			run := &v1alpha1.Run{
				ObjectMeta: metav1.ObjectMeta{Name: "run-async-poll-err", Namespace: "eval-runner"},
				Spec:       v1alpha1.RunSpec{Repo: v1alpha1.RepoSpec{URL: "git://repo", Commit: "c1"}, Model: "mockllm/model"},
				Status: v1alpha1.RunStatus{
					Phase: v1alpha1.RunPhaseRunning,
					Tasks: []v1alpha1.TaskStatus{{
						ID:              "task-poll-err",
						Phase:           v1alpha1.TaskPhaseRunning,
						StartedAt:       &startedAt,
						ExecutionID:     "exec-attempt-0",
						FailureAttempts: testCase.initialFailures,
					}},
				},
			}
			sandboxName := discovery.TaskSandboxNameForRun(run.Name, "task-poll-err")
			sandbox := &sandboxv1beta1.Sandbox{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sandboxName,
					Namespace: "eval-runner-sandbox",
					Labels: map[string]string{
						discovery.LabelRunName:      run.Name,
						discovery.LabelRunNamespace: run.Namespace,
					},
					Annotations: map[string]string{
						discovery.AnnotationTaskID:             "task-poll-err",
						discovery.AnnotationControllerInstance: "controller-1",
					},
				},
			}
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, sandbox).WithStatusSubresource(run).Build()
			createReadySandboxPod(t, kubeClient, sandboxName, "10.244.1.13")

			fakeClient := &sandboxclient.FakeClient{PollAsyncFn: testCase.pollFn}
			reconciler := &RunReconciler{
				Client:           kubeClient,
				Scheme:           scheme,
				InstanceID:       "controller-1",
				SandboxNamespace: "eval-runner-sandbox",
				SandboxClient:    fakeClient,
				Now:              func() time.Time { return startedAt.Add(5 * time.Second) },
			}
			reconciler.SetCachedManifest(run.Namespace, run.Name, manifest)
			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}

			if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
				t.Fatalf("reconcile error: %v", err)
			}

			var updatedRun v1alpha1.Run
			_ = kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun)
			task := updatedRun.Status.Tasks[0]
			if task.Phase != testCase.wantPhase {
				t.Errorf("Phase = %s, want %s (message=%q, restartReason=%q)", task.Phase, testCase.wantPhase, task.Message, task.RestartReason)
			}
			if task.ExecutionID != "" {
				t.Errorf("expected ExecutionID cleared, got %q", task.ExecutionID)
			}
			if task.Restarts != testCase.wantRestarts {
				t.Errorf("Restarts = %d, want %d", task.Restarts, testCase.wantRestarts)
			}
			if task.FailureAttempts != testCase.wantFailureAttempts {
				t.Errorf("FailureAttempts = %d, want %d", task.FailureAttempts, testCase.wantFailureAttempts)
			}
			gotText := task.Message + " " + task.RestartReason
			if !strings.Contains(gotText, testCase.wantSubstring) {
				t.Errorf("expected message/restartReason to contain %q, got %q", testCase.wantSubstring, gotText)
			}
			var sandboxList sandboxv1beta1.SandboxList
			_ = kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox"))
			if len(sandboxList.Items) != 0 {
				t.Errorf("expected sandbox deleted, found %d", len(sandboxList.Items))
			}
		})
	}
}

func TestReconcileAsyncSandboxTask_TimeoutFailsWithoutPolling(t *testing.T) {
	scheme := setupScheme(t)
	timeoutSeconds := 10
	manifest := &v1alpha1.Manifest{
		Tasks: []v1alpha1.ManifestTask{{
			ID:             "task-timeout",
			Image:          "custom-task-image:v1",
			Command:        []string{"sleep", "60"},
			Resources:      []v1alpha1.ManifestResource{{Type: v1alpha1.ResourceTypeSandbox}},
			TimeoutSeconds: &timeoutSeconds,
		}},
	}
	currentTime := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "run-async-timeout", Namespace: "eval-runner"},
		Spec:       v1alpha1.RunSpec{Repo: v1alpha1.RepoSpec{URL: "git://repo", Commit: "c1"}, Model: "mockllm/model"},
		Status:     v1alpha1.RunStatus{Phase: v1alpha1.RunPhaseRunning},
	}

	fakeClient := &sandboxclient.FakeClient{}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).WithStatusSubresource(run).Build()
	reconciler := &RunReconciler{
		Client:           kubeClient,
		Scheme:           scheme,
		SandboxNamespace: "eval-runner-sandbox",
		SandboxClient:    fakeClient,
		Now:              func() time.Time { return currentTime },
	}
	reconciler.SetCachedManifest(run.Namespace, run.Name, manifest)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}

	// Turn 1: create sandbox.
	if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("turn 1 error: %v", err)
	}
	var sandboxList sandboxv1beta1.SandboxList
	_ = kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox"))
	createReadySandboxPod(t, kubeClient, sandboxList.Items[0].Name, "10.244.1.14")

	// Turn 2: StartAsync.
	if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("turn 2 error: %v", err)
	}

	// Turn 3: Before timeout -> polls running.
	currentTime = currentTime.Add(5 * time.Second)
	result, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("turn 3 error: %v", err)
	}
	if result.RequeueAfter != reconciler.pollProgressInterval() {
		t.Errorf("expected RequeueAfter %v, got %v", reconciler.pollProgressInterval(), result.RequeueAfter)
	}
	if len(fakeClient.PollAsyncCalls) != 1 {
		t.Fatalf("expected 1 PollAsync call before timeout, got %d", len(fakeClient.PollAsyncCalls))
	}

	// Turn 4: Past timeout -> fails without calling PollAsync again, clears ExecutionID, deletes sandbox.
	currentTime = currentTime.Add(5 * time.Second)
	if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("turn 4 error: %v", err)
	}
	if len(fakeClient.PollAsyncCalls) != 1 {
		t.Errorf("expected no extra PollAsync call after timeout, got %d", len(fakeClient.PollAsyncCalls))
	}
	var updatedRun v1alpha1.Run
	_ = kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun)
	task := updatedRun.Status.Tasks[0]
	if task.Phase != v1alpha1.TaskPhaseFailed {
		t.Fatalf("expected Failed, got %s", task.Phase)
	}
	if task.ExecutionID != "" {
		t.Errorf("expected ExecutionID cleared on timeout, got %q", task.ExecutionID)
	}
	if task.Message != "step task command failed: sandbox did not report completion" {
		t.Errorf("unexpected timeout message %q", task.Message)
	}
	_ = kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox"))
	if len(sandboxList.Items) != 0 {
		t.Errorf("expected sandbox deleted on timeout, found %d", len(sandboxList.Items))
	}
}

func TestReconcileAsyncSandboxTask_ControllerRestartRecovery(t *testing.T) {
	scheme := setupScheme(t)
	manifest := &v1alpha1.Manifest{
		Tasks: []v1alpha1.ManifestTask{{
			ID:        "task-restart-recover",
			Image:     "custom-task-image:v1",
			Command:   []string{"run-eval"},
			Resources: []v1alpha1.ManifestResource{{Type: v1alpha1.ResourceTypeSandbox}},
		}},
	}
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "run-async-restart", Namespace: "eval-runner"},
		Spec:       v1alpha1.RunSpec{Repo: v1alpha1.RepoSpec{URL: "git://repo", Commit: "commit1"}, Model: "mockllm/model"},
		Status:     v1alpha1.RunStatus{Phase: v1alpha1.RunPhaseRunning},
	}

	fakeClient := &sandboxclient.FakeClient{
		ReadFileFn: func(ctx context.Context, path string) ([]byte, error) {
			if path == "/out/results.json" {
				return []byte(`{"schemaVersion": 1, "samples": [{"id": "s1", "passed": true, "score": 1.0}]}`), nil
			}
			return nil, fmt.Errorf("unexpected file %q", path)
		},
	}
	sink := artifacts.NewInMemorySink()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).WithStatusSubresource(run).Build()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}

	// Controller instance 1 creates sandbox and calls StartAsync.
	controller1 := &RunReconciler{
		Client:           kubeClient,
		Scheme:           scheme,
		InstanceID:       "controller-1",
		SandboxNamespace: "eval-runner-sandbox",
		SandboxClient:    fakeClient,
		ArtifactSink:     sink,
	}
	controller1.SetCachedManifest(run.Namespace, run.Name, manifest)

	if _, err := controller1.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("controller1 turn 1 error: %v", err)
	}
	var sandboxList sandboxv1beta1.SandboxList
	_ = kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox"))
	createReadySandboxPod(t, kubeClient, sandboxList.Items[0].Name, "10.244.1.15")

	if _, err := controller1.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("controller1 turn 2 error: %v", err)
	}
	if len(fakeClient.StartAsyncCalls) != 1 {
		t.Fatalf("expected 1 StartAsync call from controller1, got %d", len(fakeClient.StartAsyncCalls))
	}
	executionID := fakeClient.StartAsyncCalls[0].ExecutionID

	// Simulate controller restart: fresh RunReconciler with a new InstanceID.
	pollInterval := 4 * time.Second
	controller2 := &RunReconciler{
		Client:           kubeClient,
		Scheme:           scheme,
		InstanceID:       "controller-2",
		SandboxNamespace: "eval-runner-sandbox",
		SandboxClient:    fakeClient,
		ArtifactSink:     sink,
		Config:           ClusterConfig{PollProgressInterval: pollInterval},
	}
	controller2.SetCachedManifest(run.Namespace, run.Name, manifest)

	// Turn 3 on controller2: healthy sandbox with ExecutionID resumes polling instead of restarting.
	result, err := controller2.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("controller2 turn 3 error: %v", err)
	}
	if result.RequeueAfter != pollInterval {
		t.Errorf("controller2 turn 3 RequeueAfter = %v, want %v", result.RequeueAfter, pollInterval)
	}
	if len(fakeClient.StartAsyncCalls) != 1 {
		t.Errorf("expected no new StartAsync calls after restart, got %d", len(fakeClient.StartAsyncCalls))
	}
	if len(fakeClient.PollAsyncCalls) != 1 || fakeClient.PollAsyncCalls[0] != executionID {
		t.Errorf("expected PollAsync(%q) after restart, got %v", executionID, fakeClient.PollAsyncCalls)
	}

	var midRun v1alpha1.Run
	_ = kubeClient.Get(t.Context(), req.NamespacedName, &midRun)
	if midRun.Status.Tasks[0].Phase != v1alpha1.TaskPhaseRunning || midRun.Status.Tasks[0].Restarts != 0 {
		t.Errorf("expected task Running with 0 restarts after controller restart, got phase=%s restarts=%d",
			midRun.Status.Tasks[0].Phase, midRun.Status.Tasks[0].Restarts)
	}

	// Turn 4 on controller2: async command completes -> marks Completed without restarting.
	fakeClient.SetAsyncTaskStatus(executionID, sandboxclient.AsyncTaskStatus{
		State:    sandboxclient.AsyncTaskCompleted,
		ExitCode: 0,
	})
	if _, err := controller2.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("controller2 turn 4 error: %v", err)
	}

	var finalRun v1alpha1.Run
	_ = kubeClient.Get(t.Context(), req.NamespacedName, &finalRun)
	task := finalRun.Status.Tasks[0]
	if task.Phase != v1alpha1.TaskPhaseCompleted {
		t.Errorf("expected Completed after restart recovery, got %s (msg=%q)", task.Phase, task.Message)
	}
	if task.Restarts != 0 {
		t.Errorf("expected 0 restarts after restart recovery, got %d", task.Restarts)
	}
	if task.ExecutionID != "" {
		t.Errorf("expected ExecutionID cleared on completion, got %q", task.ExecutionID)
	}
	_ = kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox"))
	if len(sandboxList.Items) != 0 {
		t.Errorf("expected sandbox deleted after completion, found %d", len(sandboxList.Items))
	}
}
