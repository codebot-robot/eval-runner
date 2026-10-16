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
	"unicode/utf8"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/discovery"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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
	MaxStdoutBytes   int64
	MaxStderrBytes   int64
	MaxMessageBytes  int
}

func (r *RunReconciler) maxStdoutBytes() int64 {
	if r.MaxStdoutBytes > 0 {
		return r.MaxStdoutBytes
	}
	return discovery.MaxStdoutBytes
}

func (r *RunReconciler) maxStderrBytes() int64 {
	if r.MaxStderrBytes > 0 {
		return r.MaxStderrBytes
	}
	return discovery.MaxStderrBytes
}

func (r *RunReconciler) maxMessageBytes() int {
	if r.MaxMessageBytes > 0 {
		return r.MaxMessageBytes
	}
	return discovery.MaxMessageBytes
}

func (r *RunReconciler) maxStderrLines() int {
	return discovery.MaxStderrLines
}

// Reconcile handles the Run state machine:
//  1. Pending -> ensure finalizer, create Sandbox in eval-runner-sandbox, set Discovering.
//  2. Wait for sandbox pod Ready, exec git clone && git checkout && ./.evals/list.
//  3. Parse and validate manifest, write status.manifest, set Discovered.
//     On failure, set Failed with message and tail of stderr.
//  4. Delete sandbox when done, idempotent across restarts.
//  5. On Run deletion, GC discovery sandbox and remove finalizer.
func (r *RunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	sandboxNamespace := r.SandboxNamespace
	if sandboxNamespace == "" {
		sandboxNamespace = discovery.DefaultSandboxNamespace
	}

	sandboxName := discovery.SandboxNameForRun(req.Name)
	sandboxKey := types.NamespacedName{Namespace: sandboxNamespace, Name: sandboxName}

	var run v1alpha1.Run
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		if apierrors.IsNotFound(err) {
			// Run was deleted. Clean up any leftover sandbox matching this Run.
			if err := r.deleteSandboxIfMatching(ctx, sandboxKey, req.Namespace, req.Name); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Deletion lifecycle: Run is marked for deletion. Clean up sandbox and remove finalizer.
	if !run.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&run, v1alpha1.FinalizerDiscoverySandbox) {
			logger.Info("Run is being deleted; cleaning up discovery sandbox", "run", run.Name)
			if err := r.deleteSandboxIfMatching(ctx, sandboxKey, run.Namespace, run.Name); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&run, v1alpha1.FinalizerDiscoverySandbox)
			if err := r.Update(ctx, &run); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Terminal states: already finished, do nothing (clean up any leftover sandbox).
	if run.Status.Phase == v1alpha1.RunPhaseDiscovered || run.Status.Phase == v1alpha1.RunPhaseFailed {
		if err := r.deleteSandboxIfMatching(ctx, sandboxKey, run.Namespace, run.Name); err != nil {
			logger.Error(err, "failed to delete leftover sandbox for terminal run", "sandbox", sandboxName)
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is attached before creating or processing sandbox.
	if !controllerutil.ContainsFinalizer(&run, v1alpha1.FinalizerDiscoverySandbox) {
		controllerutil.AddFinalizer(&run, v1alpha1.FinalizerDiscoverySandbox)
		if err := r.Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
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

		err := r.updateRunStatus(ctx, req.NamespacedName, func(status *v1alpha1.RunStatus, gen int64) {
			status.Phase = v1alpha1.RunPhaseDiscovering
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type:               v1alpha1.ConditionTypeDiscovered,
				Status:             metav1.ConditionFalse,
				Reason:             "Discovering",
				Message:            "Running discovery in sandbox",
				ObservedGeneration: gen,
			})
		})
		if err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Sandbox exists. Ensure phase reflects Discovering if it was Pending or empty
	if run.Status.Phase != v1alpha1.RunPhaseDiscovering {
		err := r.updateRunStatus(ctx, req.NamespacedName, func(status *v1alpha1.RunStatus, gen int64) {
			status.Phase = v1alpha1.RunPhaseDiscovering
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type:               v1alpha1.ConditionTypeDiscovered,
				Status:             metav1.ConditionFalse,
				Reason:             "Discovering",
				Message:            "Running discovery in sandbox",
				ObservedGeneration: gen,
			})
		})
		if err != nil {
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
		tail := extractStderrTail(stderr, r.maxStderrLines(), discovery.MaxStderrTailBytes)
		msg := fmt.Sprintf("Command execution failed: %v", err)
		if tail != "" {
			msg += "\n" + tail
		}
		return r.failRun(ctx, &run, &sandbox, msg)
	}

	stdoutLimit := r.maxStdoutBytes()
	stderrLimit := r.maxStderrBytes()

	// Defense-in-depth: validate stdout and stderr sizes even if executor did not report limit error
	if int64(len(stdout)) > stdoutLimit {
		return r.failRun(ctx, &run, &sandbox, fmt.Sprintf("Command execution failed: %v", &discovery.LimitError{
			Stream: "stdout",
			Limit:  stdoutLimit,
		}))
	}
	if int64(len(stderr)) > stderrLimit {
		tail := extractStderrTail(stderr, r.maxStderrLines(), discovery.MaxStderrTailBytes)
		msg := fmt.Sprintf("Command execution failed: %v", &discovery.LimitError{
			Stream: "stderr",
			Limit:  stderrLimit,
		})
		if tail != "" {
			msg += "\n" + tail
		}
		return r.failRun(ctx, &run, &sandbox, msg)
	}

	// Step 4: Parse & validate manifest
	manifest, err := discovery.ParseManifest([]byte(stdout))
	if err != nil {
		tail := extractStderrTail(stderr, r.maxStderrLines(), discovery.MaxStderrTailBytes)
		msg := fmt.Sprintf("Failed to parse manifest: %v", err)
		if tail != "" {
			msg += "\n" + tail
		}
		return r.failRun(ctx, &run, &sandbox, msg)
	}

	if err := discovery.ValidateManifest(manifest); err != nil {
		return r.failRun(ctx, &run, &sandbox, fmt.Sprintf("Manifest validation failed: %v", err))
	}

	// Step 5: Success -> update Run to Discovered, then cleanup sandbox
	logger.Info("Discovery successful; recording manifest and cleaning up sandbox", "tasks", len(manifest.Tasks))
	err = r.updateRunStatus(ctx, req.NamespacedName, func(status *v1alpha1.RunStatus, gen int64) {
		status.Phase = v1alpha1.RunPhaseDiscovered
		status.Message = ""
		status.Manifest = manifest
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionTypeDiscovered,
			Status:             metav1.ConditionTrue,
			Reason:             "Discovered",
			Message:            fmt.Sprintf("Discovered %d tasks", len(manifest.Tasks)),
			ObservedGeneration: gen,
		})
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.Delete(ctx, &sandbox); err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "failed to delete sandbox", "sandbox", sandbox.Name)
	}

	return ctrl.Result{}, nil
}

func (r *RunReconciler) deleteSandboxIfMatching(ctx context.Context, sandboxKey types.NamespacedName, runNamespace, runName string) error {
	logger := log.FromContext(ctx)
	var sb sandboxv1beta1.Sandbox
	if err := r.Get(ctx, sandboxKey, &sb); err != nil {
		return client.IgnoreNotFound(err)
	}
	if sb.Labels[discovery.LabelRunName] == runName && sb.Labels[discovery.LabelRunNamespace] == runNamespace {
		if err := r.Delete(ctx, &sb); err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to delete sandbox", "sandbox", sandboxKey.Name)
			return err
		}
		logger.Info("Deleted sandbox for run", "sandbox", sandboxKey.Name, "run", runName)
	}
	return nil
}

func (r *RunReconciler) updateRunStatus(ctx context.Context, key types.NamespacedName, mutate func(*v1alpha1.RunStatus, int64)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.Run
		if err := r.Get(ctx, key, &latest); err != nil {
			return err
		}
		mutate(&latest.Status, latest.Generation)
		return r.Status().Update(ctx, &latest)
	})
}

func (r *RunReconciler) failRun(ctx context.Context, run *v1alpha1.Run, sandbox *sandboxv1beta1.Sandbox, message string) (ctrl.Result, error) {
	boundedMessage := truncateMessage(message, r.maxMessageBytes())
	key := types.NamespacedName{Namespace: run.Namespace, Name: run.Name}
	err := r.updateRunStatus(ctx, key, func(status *v1alpha1.RunStatus, gen int64) {
		status.Phase = v1alpha1.RunPhaseFailed
		status.Message = boundedMessage
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionTypeDiscovered,
			Status:             metav1.ConditionFalse,
			Reason:             "DiscoveryFailed",
			Message:            boundedMessage,
			ObservedGeneration: gen,
		})
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	if sandbox != nil {
		if err := r.Delete(ctx, sandbox); err != nil && !apierrors.IsNotFound(err) {
			logger := log.FromContext(ctx)
			logger.Error(err, "failed to delete sandbox on failure", "sandbox", sandbox.Name)
		}
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

func extractStderrTail(stderr string, maxLines int, maxBytes int) string {
	trimmed := strings.TrimSpace(stderr)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	res := strings.Join(lines, "\n")
	if maxBytes > 0 && len(res) > maxBytes {
		res = res[len(res)-maxBytes:]
		if idx := strings.Index(res, "\n"); idx != -1 && idx < len(res)-1 {
			res = res[idx+1:]
		}
		for len(res) > 0 && !utf8.RuneStart(res[0]) {
			res = res[1:]
		}
	}
	return strings.TrimSpace(res)
}

func truncateMessage(msg string, maxBytes int) string {
	if maxBytes <= 0 || len(msg) <= maxBytes {
		return msg
	}
	const suffix = "\n... [truncated]"
	if maxBytes <= len(suffix) {
		return msg[:maxBytes]
	}
	cutoff := maxBytes - len(suffix)
	for cutoff > 0 && !utf8.RuneStart(msg[cutoff]) {
		cutoff--
	}
	return msg[:cutoff] + suffix
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
