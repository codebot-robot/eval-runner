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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/discovery"
	"github.com/gke-labs/eval-runner/pkg/sandboxclient"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

func setupScheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("failed to add corev1 scheme: %v", err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("failed to add v1alpha1 scheme: %v", err)
	}
	if err := sandboxv1beta1.AddToScheme(s); err != nil {
		t.Fatalf("failed to add sandbox scheme: %v", err)
	}
	return s
}

func TestReconcilePendingToDiscovering(t *testing.T) {
	scheme := setupScheme(t)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    &sandboxclient.FakeClient{},
		SandboxNamespace: "eval-runner-sandbox",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("Expected RequeueAfter to be set")
	}

	// Assert Sandbox was created
	sandboxName := discovery.SandboxNameForRun("test-run")
	var sb sandboxv1beta1.Sandbox
	if err := client.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner-sandbox", Name: sandboxName}, &sb); err != nil {
		t.Fatalf("Failed to find created sandbox: %v", err)
	}
	if sb.Labels[discovery.LabelRunName] != "test-run" {
		t.Errorf("Expected sandbox to have run label, got: %v", sb.Labels)
	}

	// Assert Run updated to Discovering
	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseDiscovering {
		t.Errorf("Expected phase Discovering, got %s", updatedRun.Status.Phase)
	}
	if !controllerutil.ContainsFinalizer(&updatedRun, v1alpha1.FinalizerDiscoverySandbox) {
		t.Errorf("Expected finalizer %s on run, got: %v", v1alpha1.FinalizerDiscoverySandbox, updatedRun.Finalizers)
	}
	if updatedRun.Status.DiscoveryDeadline == nil {
		t.Errorf("Expected DiscoveryDeadline to be set in status")
	}
}

func TestReconcilePodReadySuccess(t *testing.T) {
	scheme := setupScheme(t)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseDiscovering,
		},
	}

	sandboxName := discovery.SandboxNameForRun("test-run")
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.244.0.5",
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	validManifestJSON := `{
		"tasks": [
			{"id": "triage/runbook_lookup", "workdir": "evals", "spec": "runbook.py@lookup"},
			{"id": "triage/summary_quality", "workdir": "evals", "spec": "summary.py@quality"}
		]
	}`

	executor := &sandboxclient.FakeClient{
		ListStdout: validManifestJSON,
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    executor,
		SandboxNamespace: "eval-runner-sandbox",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Did not expect RequeueAfter on success")
	}

	// Assert Run updated to Discovered with Manifest
	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseDiscovered {
		t.Errorf("Expected phase Discovered, got %s", updatedRun.Status.Phase)
	}
	if updatedRun.Status.Manifest == nil || len(updatedRun.Status.Manifest.Tasks) != 2 {
		t.Fatalf("Expected 2 tasks in manifest, got: %+v", updatedRun.Status.Manifest)
	}

	// Assert Sandbox was cleaned up
	var checkSb sandboxv1beta1.Sandbox
	err = client.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner-sandbox", Name: sandboxName}, &checkSb)
	if err == nil || !apierrors.IsNotFound(err) {
		t.Errorf("Expected sandbox to be deleted, got err: %v", err)
	}
}

func TestReconcileBadCommitFailsAndCleansUp(t *testing.T) {
	scheme := setupScheme(t)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "deadbeef00000000000000000000000000000000",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseDiscovering,
		},
	}

	sandboxName := discovery.SandboxNameForRun("test-run")
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.244.0.5",
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	executor := &sandboxclient.FakeClient{
		CheckoutStderr: "fatal: reference is not a tree: deadbeef00000000000000000000000000000000",
		CheckoutExit:   128,
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    executor,
		SandboxNamespace: "eval-runner-sandbox",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	_, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	// Assert Run updated to Failed
	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
	}
	if !strings.Contains(updatedRun.Status.Message, "checkout") {
		t.Errorf("Expected failure message to name the checkout step, got: %s", updatedRun.Status.Message)
	}
	if !strings.Contains(updatedRun.Status.Message, "deadbeef") {
		t.Errorf("Expected failure message to contain stderr tail, got: %s", updatedRun.Status.Message)
	}
	if strings.Contains(updatedRun.Status.Message, "attempts") {
		t.Errorf("Expected permanent failure to not mention attempts, got: %s", updatedRun.Status.Message)
	}

	// Assert Sandbox was cleaned up
	var checkSb sandboxv1beta1.Sandbox
	err = client.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner-sandbox", Name: sandboxName}, &checkSb)
	if err == nil || !apierrors.IsNotFound(err) {
		t.Errorf("Expected sandbox to be deleted on failure, got err: %v", err)
	}
}

func TestReconcileControllerRestartMidReconcile(t *testing.T) {
	scheme := setupScheme(t)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseDiscovering,
		},
	}

	sandboxName := discovery.SandboxNameForRun("test-run")
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    &sandboxclient.FakeClient{},
		SandboxNamespace: "eval-runner-sandbox",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("Expected RequeueAfter when pod is pending")
	}

	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseDiscovering {
		t.Errorf("Expected phase to remain Discovering, got %s", updatedRun.Status.Phase)
	}
}

func TestReconcileTerminalRunIsNoop(t *testing.T) {
	scheme := setupScheme(t)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseDiscovered,
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    &sandboxclient.FakeClient{},
		SandboxNamespace: "eval-runner-sandbox",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Expected no requeue for terminal Run")
	}

	sandboxName := discovery.SandboxNameForRun("test-run")
	var sb sandboxv1beta1.Sandbox
	err = client.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner-sandbox", Name: sandboxName}, &sb)
	if err == nil || !apierrors.IsNotFound(err) {
		t.Errorf("Expected no sandbox created for terminal Run, got err: %v", err)
	}
}

func TestReconcileDeleteRunMidDiscoveryRemovesSandbox(t *testing.T) {
	scheme := setupScheme(t)

	now := metav1.Now()
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-run",
			Namespace:         "eval-runner",
			DeletionTimestamp: &now,
			Finalizers:        []string{v1alpha1.FinalizerDiscoverySandbox},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseDiscovering,
		},
	}

	sandboxName := discovery.SandboxNameForRun("test-run")
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    &sandboxclient.FakeClient{},
		SandboxNamespace: "eval-runner-sandbox",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Expected no requeue on deletion")
	}

	var checkSb sandboxv1beta1.Sandbox
	err = client.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner-sandbox", Name: sandboxName}, &checkSb)
	if err == nil || !apierrors.IsNotFound(err) {
		t.Errorf("Expected sandbox to be deleted on Run deletion, got err: %v", err)
	}

	var updatedRun v1alpha1.Run
	err = client.Get(t.Context(), req.NamespacedName, &updatedRun)
	if err == nil && controllerutil.ContainsFinalizer(&updatedRun, v1alpha1.FinalizerDiscoverySandbox) {
		t.Errorf("Expected finalizer to be removed from run")
	}
}

func TestReconcileDeletedRunNotFoundCleansUpSandbox(t *testing.T) {
	scheme := setupScheme(t)

	runName := "test-run-ghost"
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: "eval-runner",
		},
	}

	sandboxName := discovery.SandboxNameForRun(runName)
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sb).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    &sandboxclient.FakeClient{},
		SandboxNamespace: "eval-runner-sandbox",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: runName}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Expected no requeue for deleted Run")
	}

	var checkSb sandboxv1beta1.Sandbox
	err = client.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner-sandbox", Name: sandboxName}, &checkSb)
	if err == nil || !apierrors.IsNotFound(err) {
		t.Errorf("Expected leftover sandbox to be deleted when Run is not found, got err: %v", err)
	}
}

func TestReconcileTerminalRunWithLeftoverSandboxRemovesSandbox(t *testing.T) {
	scheme := setupScheme(t)

	runName := "test-run-terminal"
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseDiscovered,
		},
	}

	sandboxName := discovery.SandboxNameForRun(runName)
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    &sandboxclient.FakeClient{},
		SandboxNamespace: "eval-runner-sandbox",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: runName}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Expected no requeue for terminal Run")
	}

	var checkSb sandboxv1beta1.Sandbox
	err = client.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner-sandbox", Name: sandboxName}, &checkSb)
	if err == nil || !apierrors.IsNotFound(err) {
		t.Errorf("Expected leftover sandbox to be deleted for terminal Run, got err: %v", err)
	}
}

func TestReconcileStatusUpdateConflictDoesNotRerunDiscovery(t *testing.T) {
	scheme := setupScheme(t)

	runName := "test-run-conflict"
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:       runName,
			Namespace:  "eval-runner",
			Finalizers: []string{v1alpha1.FinalizerDiscoverySandbox},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseDiscovering,
		},
	}

	sandboxName := discovery.SandboxNameForRun(runName)
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.244.0.5",
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	validManifestJSON := `{"tasks":[{"id":"triage/lookup","spec":"runbook.py@lookup"}]}`
	executor := &sandboxclient.FakeClient{
		ListStdout: validManifestJSON,
	}

	firstStatusUpdate := true
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if subResourceName == "status" && firstStatusUpdate {
					firstStatusUpdate = false
					return apierrors.NewConflict(v1alpha1.GroupVersion.WithResource("runs").GroupResource(), runName, errors.New("conflict"))
				}
				return cl.SubResource(subResourceName).Update(ctx, obj, opts...)
			},
		}).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    executor,
		SandboxNamespace: "eval-runner-sandbox",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: runName}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Expected no requeue on success, got %v", res.RequeueAfter)
	}

	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseDiscovered {
		t.Errorf("Expected phase Discovered after conflict retry, got %s", updatedRun.Status.Phase)
	}
}

type discoveringRunFixture struct {
	client      client.Client
	reconciler  *RunReconciler
	req         ctrl.Request
	sandboxName string
}

func newDiscoveringRunFixture(t *testing.T, clientInstance sandboxclient.Client) *discoveringRunFixture {
	scheme := setupScheme(t)
	runName := "test-run"
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:       runName,
			Namespace:  "eval-runner",
			Finalizers: []string{v1alpha1.FinalizerDiscoverySandbox},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseDiscovering,
		},
	}

	sandboxName := discovery.SandboxNameForRun(runName)
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.244.0.5",
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           cl,
		Scheme:           scheme,
		SandboxClient:    clientInstance,
		SandboxNamespace: "eval-runner-sandbox",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: runName}}
	return &discoveringRunFixture{
		client:      cl,
		reconciler:  reconciler,
		req:         req,
		sandboxName: sandboxName,
	}
}

func TestReconcileStdoutOverLimitFails(t *testing.T) {
	f := newDiscoveringRunFixture(t, &sandboxclient.FakeClient{
		ListErr: &discovery.LimitError{
			Stream: "stdout",
			Limit:  discovery.MaxStdoutBytes,
		},
	})

	_, err := f.reconciler.Reconcile(t.Context(), f.req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var updatedRun v1alpha1.Run
	if err := f.client.Get(t.Context(), f.req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
	}
	if !strings.Contains(updatedRun.Status.Message, "stdout limit exceeded") {
		t.Errorf("Expected message to contain 'stdout limit exceeded', got: %s", updatedRun.Status.Message)
	}
}

func TestReconcileStderrOverLimitFails(t *testing.T) {
	f := newDiscoveringRunFixture(t, &sandboxclient.FakeClient{
		ListErr: &discovery.LimitError{
			Stream: "stderr",
			Limit:  discovery.MaxStderrBytes,
		},
	})

	_, err := f.reconciler.Reconcile(t.Context(), f.req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var updatedRun v1alpha1.Run
	if err := f.client.Get(t.Context(), f.req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
	}
	if !strings.Contains(updatedRun.Status.Message, "stderr limit exceeded") {
		t.Errorf("Expected message to contain 'stderr limit exceeded', got: %s", updatedRun.Status.Message)
	}
}

func TestReconcileOversizedManifestFails(t *testing.T) {
	var tasks []string
	for i := 0; i < 1001; i++ {
		tasks = append(tasks, fmt.Sprintf(`{"id": "task_%d", "spec": "runbook.py"}`, i))
	}
	oversizedJSON := fmt.Sprintf(`{"tasks": [%s]}`, strings.Join(tasks, ","))

	f := newDiscoveringRunFixture(t, &sandboxclient.FakeClient{
		ListStdout: oversizedJSON,
	})

	_, err := f.reconciler.Reconcile(t.Context(), f.req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var updatedRun v1alpha1.Run
	if err := f.client.Get(t.Context(), f.req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
	}
	if !strings.Contains(updatedRun.Status.Message, "exceeding maximum allowed") {
		t.Errorf("Expected message to contain 'exceeding maximum allowed', got: %s", updatedRun.Status.Message)
	}
}

func TestReconcileStatusMessageBoundedOnFailure(t *testing.T) {
	hugeStderr := strings.Repeat("error line for testing\n", 500)
	f := newDiscoveringRunFixture(t, &sandboxclient.FakeClient{
		ListStderr: hugeStderr,
		ListErr:    errors.New("exit code 1"),
	})

	_, err := f.reconciler.Reconcile(t.Context(), f.req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var updatedRun v1alpha1.Run
	if err := f.client.Get(t.Context(), f.req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
	}
	if len(updatedRun.Status.Message) > discovery.MaxMessageBytes {
		t.Errorf("Expected status message length <= %d, got %d", discovery.MaxMessageBytes, len(updatedRun.Status.Message))
	}
}

func TestReconcileStdoutOverLimitWithoutExecutorError(t *testing.T) {
	hugeStdout := strings.Repeat("x", int(discovery.MaxStdoutBytes)+10)
	f := newDiscoveringRunFixture(t, &sandboxclient.FakeClient{
		ListStdout: hugeStdout,
	})

	_, err := f.reconciler.Reconcile(t.Context(), f.req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var updatedRun v1alpha1.Run
	if err := f.client.Get(t.Context(), f.req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
	}
	if !strings.Contains(updatedRun.Status.Message, "stdout limit exceeded") {
		t.Errorf("Expected message to contain 'stdout limit exceeded', got: %s", updatedRun.Status.Message)
	}
}

func TestReconcileStderrOverLimitWithoutExecutorError(t *testing.T) {
	hugeStderr := strings.Repeat("x", int(discovery.MaxStderrBytes)+10)
	f := newDiscoveringRunFixture(t, &sandboxclient.FakeClient{
		ListStderr: hugeStderr,
		ListStdout: `{"tasks":[{"id":"t1","spec":"s1"}]}`,
	})
	f.reconciler.MaxStderrBytes = discovery.MaxStderrBytes

	_, err := f.reconciler.Reconcile(t.Context(), f.req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var updatedRun v1alpha1.Run
	if err := f.client.Get(t.Context(), f.req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
	}
	if !strings.Contains(updatedRun.Status.Message, "stderr limit exceeded") {
		t.Errorf("Expected message to contain 'stderr limit exceeded', got: %s", updatedRun.Status.Message)
	}
}

func TestReconcileTransientCloneFailureRetriesAndSucceeds(t *testing.T) {
	scheme := setupScheme(t)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run-retry",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseDiscovering,
		},
	}

	sandboxName := discovery.SandboxNameForRun("test-run-retry")
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.244.0.5",
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	validManifestJSON := `{
		"tasks": [
			{"id": "triage/runbook_lookup", "workdir": "evals", "spec": "runbook.py@lookup"},
			{"id": "triage/summary_quality", "workdir": "evals", "spec": "summary.py@quality"}
		]
	}`

	cloneAttempts := 0
	executor := &sandboxclient.FakeClient{
		ExecFn: func(call int, ctx context.Context, cfg sandboxclient.ProcessConfig) (*sandboxclient.ExecuteResult, error) {
			if len(cfg.Command) > 1 && cfg.Command[1] == "clone" {
				cloneAttempts++
				if cloneAttempts <= 2 {
					// Simulate transient connection failure for first 2 attempts
					return &sandboxclient.ExecuteResult{
						ExitCode: 128,
						Stderr:   "fatal: unable to connect to git-server.eval-runner-sandbox.svc.cluster.local: Connection refused",
					}, nil
				}
				return &sandboxclient.ExecuteResult{ExitCode: 0}, nil
			}
			if len(cfg.Command) > 0 && cfg.Command[0] == "./.evals/list" {
				return &sandboxclient.ExecuteResult{ExitCode: 0, Stdout: validManifestJSON}, nil
			}
			return &sandboxclient.ExecuteResult{ExitCode: 0}, nil
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    executor,
		SandboxNamespace: "eval-runner-sandbox",
		Sleep: func(ctx context.Context, d time.Duration) error {
			return nil
		},
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run-retry"}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Did not expect RequeueAfter on success")
	}

	if cloneAttempts != 3 {
		t.Errorf("Expected 3 clone attempts (2 failures + 1 success), got %d", cloneAttempts)
	}

	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseDiscovered {
		t.Errorf("Expected phase Discovered, got %s", updatedRun.Status.Phase)
	}
	if updatedRun.Status.Manifest == nil || len(updatedRun.Status.Manifest.Tasks) != 2 {
		t.Fatalf("Expected 2 tasks in manifest, got: %+v", updatedRun.Status.Manifest)
	}
	// Assert attempts are recorded on success
	if !strings.Contains(updatedRun.Status.Message, "3 attempts") {
		t.Errorf("Expected status.Message to record 3 attempts, got: %s", updatedRun.Status.Message)
	}

	var checkSb sandboxv1beta1.Sandbox
	err = client.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner-sandbox", Name: sandboxName}, &checkSb)
	if err == nil || !apierrors.IsNotFound(err) {
		t.Errorf("Expected sandbox to be deleted, got err: %v", err)
	}
}

func TestReconcileTransientCloneFailureExhaustsRetries(t *testing.T) {
	scheme := setupScheme(t)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run-exhaust",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseDiscovering,
		},
	}

	sandboxName := discovery.SandboxNameForRun("test-run-exhaust")
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.244.0.5",
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	cloneAttempts := 0
	executor := &sandboxclient.FakeClient{
		ExecFn: func(call int, ctx context.Context, cfg sandboxclient.ProcessConfig) (*sandboxclient.ExecuteResult, error) {
			if len(cfg.Command) > 1 && cfg.Command[1] == "clone" {
				cloneAttempts++
				return &sandboxclient.ExecuteResult{
					ExitCode: 128,
					Stderr:   "fatal: unable to connect to git-server:\ngit-server: errno=Connection refused",
				}, nil
			}
			return &sandboxclient.ExecuteResult{ExitCode: 0}, nil
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    executor,
		SandboxNamespace: "eval-runner-sandbox",
		MaxDiscoveryAttempts: 5,
		Sleep: func(ctx context.Context, d time.Duration) error {
			return nil
		},
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run-exhaust"}}
	_, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	if cloneAttempts != 5 {
		t.Errorf("Expected 5 clone attempts, got %d", cloneAttempts)
	}

	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
	}
	if !strings.Contains(updatedRun.Status.Message, "step git clone failed after 5 attempts") {
		t.Errorf("Expected status.message to indicate clone step failed after 5 attempts, got: %s", updatedRun.Status.Message)
	}
	if !strings.Contains(updatedRun.Status.Message, "Connection refused") {
		t.Errorf("Expected status.message to include stderr detail, got: %s", updatedRun.Status.Message)
	}

	// Assert Sandbox was cleaned up
	var checkSb sandboxv1beta1.Sandbox
	err = client.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner-sandbox", Name: sandboxName}, &checkSb)
	if err == nil || !apierrors.IsNotFound(err) {
		t.Errorf("Expected sandbox to be deleted on failure, got err: %v", err)
	}
}

func TestStepBasedFailures(t *testing.T) {
	tests := []struct {
		name              string
		client            *sandboxclient.FakeClient
		expectedMsgSubstr []string
	}{
		{
			name: "checkout failure fails immediately without retry",
			client: &sandboxclient.FakeClient{
				CheckoutExit:   1,
				CheckoutStderr: "error: pathspec 'bad-branch' did not match any file(s) known to git",
			},
			expectedMsgSubstr: []string{"step git checkout failed", "pathspec"},
		},
		{
			name: ".evals/list missing fails immediately without retry",
			client: &sandboxclient.FakeClient{
				ListErr: status.Errorf(codes.NotFound, "command or path not found: ./.evals/list"),
			},
			expectedMsgSubstr: []string{"step ./.evals/list failed", "missing .evals/list"},
		},
		{
			name: ".evals/list non-executable fails immediately without retry",
			client: &sandboxclient.FakeClient{
				ListErr: status.Errorf(codes.PermissionDenied, "permission denied: ./.evals/list"),
			},
			expectedMsgSubstr: []string{"step ./.evals/list failed", "not executable"},
		},
		{
			name: ".evals/list non-zero exit fails immediately without retry",
			client: &sandboxclient.FakeClient{
				ListErr: nil,
				ExecFn: func(call int, ctx context.Context, cfg sandboxclient.ProcessConfig) (*sandboxclient.ExecuteResult, error) {
					if len(cfg.Command) > 0 && cfg.Command[0] == "./.evals/list" {
						return &sandboxclient.ExecuteResult{
							ExitCode: 2,
							Stderr:   "Traceback: invalid arguments",
						}, nil
					}
					return &sandboxclient.ExecuteResult{ExitCode: 0}, nil
				},
			},
			expectedMsgSubstr: []string{"step ./.evals/list failed with exit code 2", "invalid arguments"},
		},
		{
			name: ".evals/list invalid JSON fails immediately without retry",
			client: &sandboxclient.FakeClient{
				ListStdout: "{invalid json content",
				ListStderr: "warning: parsing config",
			},
			expectedMsgSubstr: []string{"step ./.evals/list failed", "invalid JSON"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDiscoveringRunFixture(t, tt.client)
			_, err := f.reconciler.Reconcile(t.Context(), f.req)
			if err != nil {
				t.Fatalf("Reconcile error: %v", err)
			}

			var updatedRun v1alpha1.Run
			if err := f.client.Get(t.Context(), f.req.NamespacedName, &updatedRun); err != nil {
				t.Fatalf("Failed to get run: %v", err)
			}
			if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
				t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
			}
			for _, substr := range tt.expectedMsgSubstr {
				if !strings.Contains(updatedRun.Status.Message, substr) {
					t.Errorf("Expected status message to contain %q, got: %s", substr, updatedRun.Status.Message)
				}
			}
			if strings.Contains(updatedRun.Status.Message, "attempts") {
				t.Errorf("Expected non-clone failure not to mention retry attempts, got: %s", updatedRun.Status.Message)
			}
		})
	}
}

func TestReconcilePodNeverReadyPastDeadline(t *testing.T) {
	scheme := setupScheme(t)

	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	pastDeadline := metav1.NewTime(now.Add(-5 * time.Minute))
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-run",
			Namespace:  "eval-runner",
			Finalizers: []string{v1alpha1.FinalizerDiscoverySandbox},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase:             v1alpha1.RunPhaseDiscovering,
			DiscoveryDeadline: &pastDeadline,
		},
	}

	sandboxName := discovery.SandboxNameForRun("test-run")
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    &sandboxclient.FakeClient{},
		SandboxNamespace: "eval-runner-sandbox",
		Now:              func() time.Time { return now },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Expected no requeue after deadline failure, got %v", res.RequeueAfter)
	}

	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
	}
	if !strings.Contains(updatedRun.Status.Message, "Discovery deadline exceeded") {
		t.Errorf("Expected failure message to mention deadline exceeded, got: %s", updatedRun.Status.Message)
	}
}

func TestReconcilePodSucceededFailsAndCleansUp(t *testing.T) {
	scheme := setupScheme(t)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-run",
			Namespace:  "eval-runner",
			Finalizers: []string{v1alpha1.FinalizerDiscoverySandbox},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseDiscovering,
		},
	}

	sandboxName := discovery.SandboxNameForRun("test-run")
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase:   corev1.PodSucceeded,
			Message: "Container terminated with exit code 0 prematurely",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    &sandboxclient.FakeClient{},
		SandboxNamespace: "eval-runner-sandbox",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Expected no requeue on PodSucceeded failure, got %v", res.RequeueAfter)
	}

	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
	}
	if !strings.Contains(updatedRun.Status.Message, "succeeded unexpectedly") {
		t.Errorf("Expected failure message to mention 'succeeded unexpectedly', got: %s", updatedRun.Status.Message)
	}
	if !strings.Contains(updatedRun.Status.Message, "Container terminated with exit code 0 prematurely") {
		t.Errorf("Expected failure message to include pod status message, got: %s", updatedRun.Status.Message)
	}
}

func TestReconcilePodImagePullBackOffSurfacesWaitingReason(t *testing.T) {
	scheme := setupScheme(t)

	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	pastDeadline := metav1.NewTime(now.Add(-1 * time.Minute))
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-run",
			Namespace:  "eval-runner",
			Finalizers: []string{v1alpha1.FinalizerDiscoverySandbox},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase:             v1alpha1.RunPhaseDiscovering,
			DiscoveryDeadline: &pastDeadline,
		},
	}

	sandboxName := discovery.SandboxNameForRun("test-run")
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "agent",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ImagePullBackOff",
							Message: "Back-off pulling image bad-image:v1",
						},
					},
				},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    &sandboxclient.FakeClient{},
		SandboxNamespace: "eval-runner-sandbox",
		Now:              func() time.Time { return now },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	_, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
	}
	if !strings.Contains(updatedRun.Status.Message, "ImagePullBackOff") {
		t.Errorf("Expected message to contain ImagePullBackOff, got: %s", updatedRun.Status.Message)
	}
	if !strings.Contains(updatedRun.Status.Message, "Back-off pulling image bad-image:v1") {
		t.Errorf("Expected message to contain container message, got: %s", updatedRun.Status.Message)
	}
}

func TestReconcilePodUnschedulableSurfacesPodScheduledFalse(t *testing.T) {
	scheme := setupScheme(t)

	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	pastDeadline := metav1.NewTime(now.Add(-1 * time.Minute))
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-run",
			Namespace:  "eval-runner",
			Finalizers: []string{v1alpha1.FinalizerDiscoverySandbox},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase:             v1alpha1.RunPhaseDiscovering,
			DiscoveryDeadline: &pastDeadline,
		},
	}

	sandboxName := discovery.SandboxNameForRun("test-run")
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{
				{
					Type:    corev1.PodScheduled,
					Status:  corev1.ConditionFalse,
					Reason:  "Unschedulable",
					Message: "0/1 nodes are available: 1 Insufficient cpu.",
				},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    &sandboxclient.FakeClient{},
		SandboxNamespace: "eval-runner-sandbox",
		Now:              func() time.Time { return now },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	_, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
	}
	if !strings.Contains(updatedRun.Status.Message, "Unschedulable") {
		t.Errorf("Expected message to contain Unschedulable, got: %s", updatedRun.Status.Message)
	}
	if !strings.Contains(updatedRun.Status.Message, "Insufficient cpu") {
		t.Errorf("Expected message to contain Insufficient cpu, got: %s", updatedRun.Status.Message)
	}
}

func TestReconcileDeadlineSurvivesControllerRestart(t *testing.T) {
	scheme := setupScheme(t)

	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	initialDeadline := metav1.NewTime(now.Add(10 * time.Minute))

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-run",
			Namespace:  "eval-runner",
			Finalizers: []string{v1alpha1.FinalizerDiscoverySandbox},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase:             v1alpha1.RunPhaseDiscovering,
			DiscoveryDeadline: &initialDeadline,
		},
	}

	sandboxName := discovery.SandboxNameForRun("test-run")
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconcilerRestarted := &RunReconciler{
		Client:            client,
		Scheme:            scheme,
		SandboxClient:     &sandboxclient.FakeClient{},
		SandboxNamespace:  "eval-runner-sandbox",
		DiscoveryDeadline: 20 * time.Minute,
		Now:               func() time.Time { return now.Add(2 * time.Minute) },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	_, err := reconcilerRestarted.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var runAfterReconcile v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &runAfterReconcile); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if !runAfterReconcile.Status.DiscoveryDeadline.Time.Equal(initialDeadline.Time) {
		t.Errorf("Expected DiscoveryDeadline to remain %v, got %v", initialDeadline.Time, runAfterReconcile.Status.DiscoveryDeadline.Time)
	}

	reconcilerPastDeadline := &RunReconciler{
		Client:            client,
		Scheme:            scheme,
		SandboxClient:     &sandboxclient.FakeClient{},
		SandboxNamespace:  "eval-runner-sandbox",
		DiscoveryDeadline: 20 * time.Minute,
		Now:               func() time.Time { return now.Add(11 * time.Minute) },
	}
	_, err = reconcilerPastDeadline.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var runAfterExpiry v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &runAfterExpiry); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if runAfterExpiry.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected run to fail once original deadline passed, got %s", runAfterExpiry.Status.Phase)
	}
	if !strings.Contains(runAfterExpiry.Status.Message, "Discovery deadline exceeded") {
		t.Errorf("Expected failure message to mention deadline exceeded, got: %s", runAfterExpiry.Status.Message)
	}
}

func TestReconcileRetryLoopCutShortByDeadline(t *testing.T) {
	scheme := setupScheme(t)

	startTime := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	currentTime := startTime
	deadline := metav1.NewTime(startTime.Add(15 * time.Second))

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-run",
			Namespace:  "eval-runner",
			Finalizers: []string{v1alpha1.FinalizerDiscoverySandbox},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase:             v1alpha1.RunPhaseDiscovering,
			DiscoveryDeadline: &deadline,
		},
	}

	sandboxName := discovery.SandboxNameForRun("test-run")
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.244.0.5",
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	cloneAttempts := 0
	executor := &sandboxclient.FakeClient{
		ExecFn: func(call int, ctx context.Context, cfg sandboxclient.ProcessConfig) (*sandboxclient.ExecuteResult, error) {
			if len(cfg.Command) > 1 && cfg.Command[1] == "clone" {
				cloneAttempts++
				return &sandboxclient.ExecuteResult{
					ExitCode: 128,
					Stderr:   "fatal: unable to connect to git-server: Connection refused",
				}, nil
			}
			return &sandboxclient.ExecuteResult{ExitCode: 0}, nil
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    executor,
		SandboxNamespace: "eval-runner-sandbox",
		MaxDiscoveryAttempts: 5,
		DiscoveryRetryBackoff: []time.Duration{
			10 * time.Second,
			10 * time.Second,
		},
		Now: func() time.Time { return currentTime },
		Sleep: func(ctx context.Context, d time.Duration) error {
			// Advance simulated clock by 20s, which pushes time past the 15s deadline
			currentTime = currentTime.Add(20 * time.Second)
			return nil
		},
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Expected no requeue on failure, got %v", res.RequeueAfter)
	}

	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", updatedRun.Status.Phase)
	}
	if !strings.Contains(updatedRun.Status.Message, "Discovery deadline exceeded") {
		t.Errorf("Expected message to contain 'Discovery deadline exceeded', got: %s", updatedRun.Status.Message)
	}
	if !strings.Contains(updatedRun.Status.Message, "Connection refused") {
		t.Errorf("Expected message to contain stderr tail, got: %s", updatedRun.Status.Message)
	}

	// Verify retry loop was cut short: should only have made 1 attempt instead of 5
	if cloneAttempts != 1 {
		t.Errorf("Expected exactly 1 clone attempt before deadline cut the loop short, got %d", cloneAttempts)
	}

	// Assert Sandbox was cleaned up
	var checkSb sandboxv1beta1.Sandbox
	err = client.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner-sandbox", Name: sandboxName}, &checkSb)
	if err == nil || !apierrors.IsNotFound(err) {
		t.Errorf("Expected sandbox to be deleted on failure, got err: %v", err)
	}
}

func TestReconcileCustomDiscoveryDeadlineConfigured(t *testing.T) {
	scheme := setupScheme(t)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-run",
			Namespace:  "eval-runner",
			Finalizers: []string{v1alpha1.FinalizerDiscoverySandbox},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(run).
		Build()

	customDeadline := 25 * time.Minute
	fixedNow := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	reconciler := &RunReconciler{
		Client:            client,
		Scheme:            scheme,
		SandboxClient:     &sandboxclient.FakeClient{},
		SandboxNamespace:  "eval-runner-sandbox",
		DiscoveryDeadline: customDeadline,
		Now:               func() time.Time { return fixedNow },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	_, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.DiscoveryDeadline == nil {
		t.Fatalf("Expected DiscoveryDeadline to be set")
	}

	expectedDeadline := fixedNow.Add(customDeadline)
	if !updatedRun.Status.DiscoveryDeadline.Time.Equal(expectedDeadline) {
		t.Errorf("DiscoveryDeadline %v does not equal expected %v", updatedRun.Status.DiscoveryDeadline.Time, expectedDeadline)
	}
}

func TestReconcileSandboxReadyConditionUsed(t *testing.T) {
	scheme := setupScheme(t)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-run",
			Namespace:  "eval-runner",
			Finalizers: []string{v1alpha1.FinalizerDiscoverySandbox},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseDiscovering,
		},
	}

	sandboxName := discovery.SandboxNameForRun("test-run")
	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")
	sb.Status.Conditions = []metav1.Condition{
		{
			Type:   string(sandboxv1beta1.SandboxConditionReady),
			Status: metav1.ConditionTrue,
			Reason: sandboxv1beta1.SandboxReasonDependenciesReady,
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.244.0.5",
		},
	}

	validManifestJSON := `{"tasks":[{"id":"triage/lookup","spec":"runbook.py@lookup"}]}`
	executor := &sandboxclient.FakeClient{ListStdout: validManifestJSON}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		SandboxClient:    executor,
		SandboxNamespace: "eval-runner-sandbox",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Did not expect RequeueAfter on success")
	}

	var updatedRun v1alpha1.Run
	if err := client.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
		t.Fatalf("Failed to get run: %v", err)
	}
	if updatedRun.Status.Phase != v1alpha1.RunPhaseDiscovered {
		t.Errorf("Expected phase Discovered, got %s", updatedRun.Status.Phase)
	}
}

func TestReconcilePodAdoptedFromAnnotation(t *testing.T) {
	scheme := setupScheme(t)

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-run",
			Namespace:  "eval-runner",
			Finalizers: []string{v1alpha1.FinalizerDiscoverySandbox},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseDiscovering,
		},
	}

	sb := discovery.BuildSandbox(run, "eval-runner-sandbox")
	sb.Annotations = map[string]string{
		sandboxv1beta1.SandboxPodNameAnnotation: "custom-adopted-pod",
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "custom-adopted-pod",
			Namespace: "eval-runner-sandbox",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.244.0.42",
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	var dialedPodIP string
	validManifestJSON := `{"tasks":[{"id":"triage/lookup","spec":"runbook.py@lookup"}]}`
	factory := sandboxclient.FactoryFunc(func(podIP string) (sandboxclient.Client, error) {
		dialedPodIP = podIP
		return &sandboxclient.FakeClient{ListStdout: validManifestJSON}, nil
	})

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, sb, pod).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:           client,
		Scheme:           scheme,
		ClientFactory:    factory,
		SandboxNamespace: "eval-runner-sandbox",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "test-run"}}
	res, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("Did not expect RequeueAfter on success")
	}

	if dialedPodIP != "10.244.0.42" {
		t.Errorf("Expected ClientFactory.NewClient to be called with pod IP 10.244.0.42, got %q", dialedPodIP)
	}
}

func TestRequeueDelay(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	r := &RunReconciler{
		Now: func() time.Time { return now },
	}

	// 1. Nil deadline -> 2s
	if got := r.requeueDelay(nil); got != 2*time.Second {
		t.Errorf("requeueDelay(nil) = %v, want 2s", got)
	}

	// 2. Future deadline > 2s -> 2s
	dl1 := metav1.NewTime(now.Add(10 * time.Second))
	if got := r.requeueDelay(&dl1); got != 2*time.Second {
		t.Errorf("requeueDelay(10s) = %v, want 2s", got)
	}

	// 3. Future deadline < 2s -> remaining
	dl2 := metav1.NewTime(now.Add(1200 * time.Millisecond))
	if got := r.requeueDelay(&dl2); got != 1200*time.Millisecond {
		t.Errorf("requeueDelay(1.2s) = %v, want 1.2s", got)
	}

	// 4. Past deadline -> 0
	dl3 := metav1.NewTime(now.Add(-5 * time.Second))
	if got := r.requeueDelay(&dl3); got != 0 {
		t.Errorf("requeueDelay(-5s) = %v, want 0", got)
	}
}
