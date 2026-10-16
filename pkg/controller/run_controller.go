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
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/discovery"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

// RunReconciler reconciles a Run object
type RunReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Executor         PodExecutor
	SandboxNamespace string
}

// Reconcile handles the Run state machine:
//  1. Pending -> create Sandbox in eval-runner-sandbox, set Discovering.
//  2. Wait for sandbox pod Ready, exec git clone && git checkout && ./.evals/list.
//  3. Parse and validate manifest, write status.manifest, set Discovered.
//     On failure, set Failed with message and tail of stderr.
//  4. Delete sandbox when done, idempotent across restarts.
func (r *RunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var run v1alpha1.Run
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	sandboxNamespace := r.SandboxNamespace
	if sandboxNamespace == "" {
		sandboxNamespace = discovery.DefaultSandboxNamespace
	}

	sandboxName := discovery.SandboxNameForRun(run.Name)
	sandboxKey := types.NamespacedName{Namespace: sandboxNamespace, Name: sandboxName}

	// Terminal states: already finished, do nothing (clean up any leftover sandbox)
	if run.Status.Phase == v1alpha1.RunPhaseDiscovered || run.Status.Phase == v1alpha1.RunPhaseFailed {
		var leftoverSb sandboxv1beta1.Sandbox
		err := r.Get(ctx, sandboxKey, &leftoverSb)
		if err == nil {
			if err := r.Delete(ctx, &leftoverSb); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to delete leftover sandbox for terminal run", "sandbox", sandboxName)
			}
		} else if !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to check leftover sandbox for terminal run", "sandbox", sandboxName)
		}
		return ctrl.Result{}, nil
	}

	var sandbox sandboxv1beta1.Sandbox

	err := r.Get(ctx, sandboxKey, &sandbox)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	sandboxExists := (err == nil)

	// Step 1: Create sandbox if it does not exist
	if !sandboxExists {
		logger.Info("Creating discovery sandbox", "sandbox", sandboxName, "namespace", sandboxNamespace)
		desiredSandbox := discovery.BuildSandbox(&run, sandboxNamespace)
		if err := r.Create(ctx, desiredSandbox); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, fmt.Errorf("failed to create sandbox: %w", err)
			}
		}

		run.Status.Phase = v1alpha1.RunPhaseDiscovering
		meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionTypeDiscovered,
			Status:             metav1.ConditionFalse,
			Reason:             "Discovering",
			Message:            "Running discovery in sandbox",
			ObservedGeneration: run.Generation,
		})

		if err := r.Status().Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Sandbox exists. Ensure phase reflects Discovering if it was Pending or empty
	if run.Status.Phase != v1alpha1.RunPhaseDiscovering {
		run.Status.Phase = v1alpha1.RunPhaseDiscovering
		meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionTypeDiscovered,
			Status:             metav1.ConditionFalse,
			Reason:             "Discovering",
			Message:            "Running discovery in sandbox",
			ObservedGeneration: run.Generation,
		})
		if err := r.Status().Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Step 2: Check if backing pod is Ready
	var pod corev1.Pod
	podKey := types.NamespacedName{Namespace: sandboxNamespace, Name: sandboxName}
	if err := r.Get(ctx, podKey, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Backing pod not yet created", "pod", podKey)
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	if pod.Status.Phase == corev1.PodFailed {
		return r.failRun(ctx, &run, &sandbox, fmt.Sprintf("Sandbox pod failed: %s", pod.Status.Message))
	}

	if !isPodReady(&pod) {
		logger.Info("Backing pod not yet ready", "pod", podKey, "phase", pod.Status.Phase)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Step 3: Pod is Ready -> execute discovery command
	logger.Info("Sandbox pod is ready; executing discovery", "pod", podKey)
	cmd := discovery.BuildDiscoveryCommand(run.Spec.Repo.URL, run.Spec.Repo.Commit)
	execCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	stdout, stderr, err := r.Executor.Exec(execCtx, sandboxNamespace, pod.Name, discovery.DiscoveryContainerName, cmd)
	if err != nil {
		tail := extractStderrTail(stderr, 10)
		msg := fmt.Sprintf("Command execution failed: %v", err)
		if tail != "" {
			msg += "\n" + tail
		}
		return r.failRun(ctx, &run, &sandbox, msg)
	}

	// Step 4: Parse & validate manifest
	manifest, err := discovery.ParseManifest([]byte(stdout))
	if err != nil {
		tail := extractStderrTail(stderr, 10)
		msg := fmt.Sprintf("Failed to parse manifest: %v", err)
		if tail != "" {
			msg += "\n" + tail
		}
		return r.failRun(ctx, &run, &sandbox, msg)
	}

	if err := discovery.ValidateManifest(manifest); err != nil {
		return r.failRun(ctx, &run, &sandbox, fmt.Sprintf("Manifest validation failed: %v", err))
	}

	// Step 5: Success -> cleanup sandbox and update Run to Discovered
	logger.Info("Discovery successful; cleaning up sandbox and recording manifest", "tasks", len(manifest.Tasks))
	if err := r.Delete(ctx, &sandbox); err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "failed to delete sandbox", "sandbox", sandbox.Name)
	}

	run.Status.Phase = v1alpha1.RunPhaseDiscovered
	run.Status.Message = ""
	run.Status.Manifest = manifest
	meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionTypeDiscovered,
		Status:             metav1.ConditionTrue,
		Reason:             "Discovered",
		Message:            fmt.Sprintf("Discovered %d tasks", len(manifest.Tasks)),
		ObservedGeneration: run.Generation,
	})

	if err := r.Status().Update(ctx, &run); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *RunReconciler) failRun(ctx context.Context, run *v1alpha1.Run, sandbox *sandboxv1beta1.Sandbox, message string) (ctrl.Result, error) {
	if sandbox != nil {
		if err := r.Delete(ctx, sandbox); err != nil && !apierrors.IsNotFound(err) {
			logger := log.FromContext(ctx)
			logger.Error(err, "failed to delete sandbox on failure", "sandbox", sandbox.Name)
		}
	}

	run.Status.Phase = v1alpha1.RunPhaseFailed
	run.Status.Message = message
	meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionTypeDiscovered,
		Status:             metav1.ConditionFalse,
		Reason:             "DiscoveryFailed",
		Message:            message,
		ObservedGeneration: run.Generation,
	})

	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func extractStderrTail(stderr string, maxLines int) string {
	trimmed := strings.TrimSpace(stderr)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// SetupWithManager sets up the controller with the Manager.
func (r *RunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Run{}).
		Watches(
			&sandboxv1beta1.Sandbox{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				runName := obj.GetLabels()[discovery.LabelRunName]
				runNS := obj.GetLabels()[discovery.LabelRunNamespace]
				if runName != "" && runNS != "" {
					return []reconcile.Request{
						{NamespacedName: types.NamespacedName{Namespace: runNS, Name: runName}},
					}
				}
				return nil
			}),
		).
		Complete(r)
}
