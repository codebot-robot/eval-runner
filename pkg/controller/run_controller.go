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
	"regexp"
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

const (
	// DefaultMaxDiscoveryAttempts is the default maximum number of discovery attempts for transient errors.
	DefaultMaxDiscoveryAttempts = 5

	// DefaultDiscoveryDeadline is the default overall deadline for discovery to complete.
	DefaultDiscoveryDeadline = 10 * time.Minute
)

// DefaultDiscoveryRetryBackoff defines the default backoff intervals between discovery retry attempts.
var DefaultDiscoveryRetryBackoff = []time.Duration{
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	40 * time.Second,
}

var (
	transientPatterns = []*regexp.Regexp{
		// Connection refused / reset
		regexp.MustCompile(`(?i)connection refused`),
		regexp.MustCompile(`(?i)connection reset`),
		// DNS resolution
		regexp.MustCompile(`(?i)could not resolve host`),
		regexp.MustCompile(`(?i)name does not resolve`),
		regexp.MustCompile(`(?i)name or service not known`),
		regexp.MustCompile(`(?i)temporary failure in name resolution`),
		regexp.MustCompile(`(?i)nodename nor servname provided`),
		regexp.MustCompile(`(?i)unable to look up`),
		// Timeouts
		regexp.MustCompile(`(?i)connection timed out`),
		regexp.MustCompile(`(?i)operation timed out`),
		regexp.MustCompile(`(?i)timed out`),
		regexp.MustCompile(`(?i)\bi/o timeout\b`),
		regexp.MustCompile(`(?i)tls handshake timeout`),
		// HTTP 5xx
		regexp.MustCompile(`(?i)the requested url returned error: 5\d\d`),
		regexp.MustCompile(`(?i)http[ /]5\d\d`),
		regexp.MustCompile(`(?i)status( code)?:? 5\d\d`),
		regexp.MustCompile(`(?i)rpc failed; curl \d+ the requested url returned error: 5`),
		// Remote hung up / network routing
		regexp.MustCompile(`(?i)remote end hung up unexpectedly`),
		regexp.MustCompile(`(?i)early eof`),
		regexp.MustCompile(`(?i)network is unreachable`),
		regexp.MustCompile(`(?i)no route to host`),
		regexp.MustCompile(`(?i)host is down`),
		regexp.MustCompile(`(?i)empty reply from server`),
	}

	permanentPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)reference is not a tree`),
		regexp.MustCompile(`(?i)did not match any file\(s\) known to git`),
		regexp.MustCompile(`(?i)ambiguous argument`),
		regexp.MustCompile(`(?i)\.evals/list`),
		regexp.MustCompile(`(?i)repository not found`),
		regexp.MustCompile(`(?i)repository '.*' does not exist`),
		regexp.MustCompile(`(?i)authentication failed`),
		regexp.MustCompile(`(?i)permission denied \(publickey\)`),
		regexp.MustCompile(`(?i)the requested url returned error: 40[134]`),
	}
)

// IsPermanentDiscoveryError reports whether a discovery execution error or stderr
// represents a permanent failure (e.g. invalid commit or missing .evals/list) that
// should not be retried.
func IsPermanentDiscoveryError(err error, stderr string) bool {
	combined := stderr
	if err != nil {
		combined += "\n" + err.Error()
	}
	for _, p := range permanentPatterns {
		if p.MatchString(combined) {
			return true
		}
	}
	return false
}

// IsTransientDiscoveryError reports whether a discovery execution error or stderr
// is a transient failure (e.g. connection refused, DNS lookup failure, timeout, HTTP 5xx)
// suitable for retry with backoff.
func IsTransientDiscoveryError(err error, stderr string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if IsPermanentDiscoveryError(err, stderr) {
		return false
	}
	combined := stderr + "\n" + err.Error()
	for _, p := range transientPatterns {
		if p.MatchString(combined) {
			return true
		}
	}
	return false
}

func backoffForAttempt(backoffs []time.Duration, attempt int) time.Duration {
	if len(backoffs) == 0 {
		return 5 * time.Second
	}
	idx := attempt - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(backoffs) {
		idx = len(backoffs) - 1
	}
	return backoffs[idx]
}

func formatAttemptCount(n int) string {
	if n == 1 {
		return "1 attempt"
	}
	return fmt.Sprintf("%d attempts", n)
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// RunReconciler reconciles a Run object
type RunReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Executor         PodExecutor
	SandboxNamespace string
	MaxStdoutBytes   int64
	MaxStderrBytes   int64
	MaxMessageBytes  int

	// DiscoveryDeadline is the overall deadline duration for discovery to complete.
	// Defaults to DefaultDiscoveryDeadline (10m) if <= 0.
	DiscoveryDeadline time.Duration

	// Now returns the current time. In tests, this can be overridden.
	Now func() time.Time

	// MaxDiscoveryAttempts is the maximum number of discovery attempts for transient errors.
	// Defaults to DefaultMaxDiscoveryAttempts (5) if <= 0.
	MaxDiscoveryAttempts int

	// DiscoveryRetryBackoff defines the backoff schedule between transient attempts.
	// Defaults to DefaultDiscoveryRetryBackoff if empty.
	DiscoveryRetryBackoff []time.Duration

	// Sleep pauses execution between retries. In tests, this can be overridden to return immediately.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (r *RunReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *RunReconciler) discoveryDeadline() time.Duration {
	if r.DiscoveryDeadline > 0 {
		return r.DiscoveryDeadline
	}
	return DefaultDiscoveryDeadline
}

func (r *RunReconciler) requeueDelay(deadline *metav1.Time) time.Duration {
	defaultDelay := 2 * time.Second
	if deadline == nil {
		return defaultDelay
	}
	remaining := deadline.Sub(r.now())
	if remaining <= 0 {
		return 0
	}
	if remaining < defaultDelay {
		return remaining
	}
	return defaultDelay
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

		deadline := metav1.NewTime(r.now().Add(r.discoveryDeadline()))
		err := r.updateRunStatus(ctx, req.NamespacedName, func(status *v1alpha1.RunStatus, gen int64) {
			status.Phase = v1alpha1.RunPhaseDiscovering
			if status.DiscoveryDeadline == nil {
				status.DiscoveryDeadline = &deadline
			}
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
		run.Status.Phase = v1alpha1.RunPhaseDiscovering
		run.Status.DiscoveryDeadline = &deadline

		return ctrl.Result{RequeueAfter: r.requeueDelay(run.Status.DiscoveryDeadline)}, nil
	}

	// Sandbox exists. Ensure phase reflects Discovering and deadline is recorded (back-filled for in-flight Runs)
	if run.Status.Phase != v1alpha1.RunPhaseDiscovering || run.Status.DiscoveryDeadline == nil {
		var setDeadline *metav1.Time
		err := r.updateRunStatus(ctx, req.NamespacedName, func(status *v1alpha1.RunStatus, gen int64) {
			status.Phase = v1alpha1.RunPhaseDiscovering
			if status.DiscoveryDeadline == nil {
				dl := metav1.NewTime(r.now().Add(r.discoveryDeadline()))
				status.DiscoveryDeadline = &dl
			}
			setDeadline = status.DiscoveryDeadline
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
		run.Status.Phase = v1alpha1.RunPhaseDiscovering
		run.Status.DiscoveryDeadline = setDeadline
	}

	// Step 2: Check backing pod and conditions
	podName := sandbox.Name
	if name, ok := sandbox.Annotations[sandboxv1beta1.SandboxPodNameAnnotation]; ok && name != "" {
		podName = name
	}

	var pod corev1.Pod
	podKey := types.NamespacedName{Namespace: sandboxNamespace, Name: podName}
	podErr := r.Get(ctx, podKey, &pod)
	if podErr != nil && !apierrors.IsNotFound(podErr) {
		return ctrl.Result{}, podErr
	}
	podExists := (podErr == nil)

	// Check if overall discovery deadline has been exceeded.
	// Runs before the pod-exists check so that Runs where the pod was never created or is missing still fail on deadline expiry.
	if run.Status.DiscoveryDeadline != nil && !r.now().Before(run.Status.DiscoveryDeadline.Time) {
		var podPtr *corev1.Pod
		if podExists {
			podPtr = &pod
		}
		msg := formatPodDeadlineMessage(podPtr)
		logger.Info("Discovery deadline exceeded; failing Run", "run", run.Name, "message", msg)
		return r.failRun(ctx, &run, &sandbox, msg)
	}

	if !podExists {
		logger.Info("Backing pod not yet created", "pod", podKey)
		return ctrl.Result{RequeueAfter: r.requeueDelay(run.Status.DiscoveryDeadline)}, nil
	}

	if pod.Status.Phase == corev1.PodFailed {
		msg := "Sandbox pod failed"
		if pod.Status.Message != "" {
			msg = fmt.Sprintf("%s: %s", msg, pod.Status.Message)
		}
		return r.failRun(ctx, &run, &sandbox, msg)
	}

	if pod.Status.Phase == corev1.PodSucceeded {
		msg := "Sandbox pod succeeded unexpectedly before discovery completed"
		if pod.Status.Message != "" {
			msg = fmt.Sprintf("%s: %s", msg, pod.Status.Message)
		}
		return r.failRun(ctx, &run, &sandbox, msg)
	}

	finishedCond := meta.FindStatusCondition(sandbox.Status.Conditions, string(sandboxv1beta1.SandboxConditionFinished))
	if finishedCond != nil && finishedCond.Status == metav1.ConditionTrue {
		if finishedCond.Reason == sandboxv1beta1.SandboxReasonPodFailed {
			msg := "Sandbox pod failed"
			if finishedCond.Message != "" {
				msg = fmt.Sprintf("%s: %s", msg, finishedCond.Message)
			}
			return r.failRun(ctx, &run, &sandbox, msg)
		}
		if finishedCond.Reason == sandboxv1beta1.SandboxReasonPodSucceeded {
			msg := "Sandbox pod succeeded unexpectedly before discovery completed"
			if finishedCond.Message != "" {
				msg = fmt.Sprintf("%s: %s", msg, finishedCond.Message)
			}
			return r.failRun(ctx, &run, &sandbox, msg)
		}
	}

	sandboxReady := meta.IsStatusConditionTrue(sandbox.Status.Conditions, string(sandboxv1beta1.SandboxConditionReady))
	if !sandboxReady && !isPodReady(&pod) {
		logger.Info("Backing pod not yet ready", "pod", podKey, "phase", pod.Status.Phase)
		return ctrl.Result{RequeueAfter: r.requeueDelay(run.Status.DiscoveryDeadline)}, nil
	}

	// Step 3: Pod is Ready -> execute discovery command
	logger.Info("Sandbox pod is ready; executing discovery", "pod", podKey)
	cmd := discovery.BuildDiscoveryCommand(run.Spec.Repo.URL, run.Spec.Repo.Commit)
	execTimeout := 5 * time.Minute
	if run.Status.DiscoveryDeadline != nil {
		remaining := run.Status.DiscoveryDeadline.Sub(r.now())
		if remaining <= 0 {
			return r.failRun(ctx, &run, &sandbox, formatPodDeadlineMessage(&pod))
		}
		if remaining < execTimeout {
			execTimeout = remaining
		}
	}
	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	maxAttempts := r.MaxDiscoveryAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxDiscoveryAttempts
	}

	backoffs := r.DiscoveryRetryBackoff
	if len(backoffs) == 0 {
		backoffs = DefaultDiscoveryRetryBackoff
	}

	sleepFn := r.Sleep
	if sleepFn == nil {
		sleepFn = defaultSleep
	}

	var stdout, stderr string
	var execErr error
	var attempts int

	for attempts = 1; attempts <= maxAttempts; attempts++ {
		if run.Status.DiscoveryDeadline != nil && !r.now().Before(run.Status.DiscoveryDeadline.Time) {
			break
		}

		stdout, stderr, execErr = r.Executor.Exec(execCtx, sandboxNamespace, pod.Name, discovery.DiscoveryContainerName, cmd)
		if execErr == nil {
			break
		}

		if !IsTransientDiscoveryError(execErr, stderr) || attempts >= maxAttempts || execCtx.Err() != nil {
			break
		}

		if run.Status.DiscoveryDeadline != nil && !r.now().Before(run.Status.DiscoveryDeadline.Time) {
			break
		}

		backoffDuration := backoffForAttempt(backoffs, attempts)
		if run.Status.DiscoveryDeadline != nil {
			rem := run.Status.DiscoveryDeadline.Sub(r.now())
			if rem <= 0 {
				break
			}
			if rem < backoffDuration {
				backoffDuration = rem
			}
		}

		logger.Info("Transient error during discovery execution; retrying",
			"attempt", attempts,
			"maxAttempts", maxAttempts,
			"error", execErr,
			"backoff", backoffDuration,
		)

		attemptMsg := fmt.Sprintf("Running discovery in sandbox (attempt %d of %d failed; retrying)", attempts, maxAttempts)
		retryMsg := fmt.Sprintf("Discovery attempt %d failed; retrying", attempts)
		if updateErr := r.updateRunStatus(execCtx, req.NamespacedName, func(status *v1alpha1.RunStatus, gen int64) {
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type:               v1alpha1.ConditionTypeDiscovered,
				Status:             metav1.ConditionFalse,
				Reason:             "Discovering",
				Message:            attemptMsg,
				ObservedGeneration: gen,
			})
			status.Message = retryMsg
		}); updateErr != nil {
			logger.Error(updateErr, "failed to update run status during retry", "attempt", attempts)
		}

		if err := sleepFn(execCtx, backoffDuration); err != nil {
			logger.Info("Context done while waiting to retry discovery", "error", err)
			break
		}
	}

	if execErr != nil {
		tail := extractStderrTail(stderr, r.maxStderrLines(), discovery.MaxStderrTailBytes)
		isDeadlineExceeded := (run.Status.DiscoveryDeadline != nil && !r.now().Before(run.Status.DiscoveryDeadline.Time)) ||
			errors.Is(execCtx.Err(), context.DeadlineExceeded)
		if isDeadlineExceeded {
			msg := formatPodDeadlineMessage(&pod)
			if tail != "" {
				msg += "\n" + tail
			}
			return r.failRun(ctx, &run, &sandbox, msg)
		}
		var msg string
		if attempts > 1 || (IsTransientDiscoveryError(execErr, stderr) && attempts >= maxAttempts) {
			msg = fmt.Sprintf("Command execution failed after %s: %v", formatAttemptCount(attempts), execErr)
		} else {
			msg = fmt.Sprintf("Command execution failed: %v", execErr)
		}
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
	if pod == nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func formatPodDeadlineMessage(pod *corev1.Pod) string {
	if pod == nil {
		return "Discovery deadline exceeded: sandbox pod not found"
	}

	var details []string
	allStatuses := append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
	for _, cs := range allStatuses {
		if cs.State.Waiting != nil {
			reason := cs.State.Waiting.Reason
			msg := cs.State.Waiting.Message
			if reason != "" && msg != "" {
				details = append(details, fmt.Sprintf("container %s waiting: %s: %s", cs.Name, reason, msg))
			} else if reason != "" {
				details = append(details, fmt.Sprintf("container %s waiting: %s", cs.Name, reason))
			} else if msg != "" {
				details = append(details, fmt.Sprintf("container %s waiting: %s", cs.Name, msg))
			}
		}
	}

	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
			if cond.Reason != "" && cond.Message != "" {
				details = append(details, fmt.Sprintf("PodScheduled=%s: %s", cond.Reason, cond.Message))
			} else if cond.Reason != "" {
				details = append(details, fmt.Sprintf("PodScheduled=%s", cond.Reason))
			} else if cond.Message != "" {
				details = append(details, fmt.Sprintf("PodScheduled: %s", cond.Message))
			}
		}
	}

	if len(details) == 0 {
		for _, cond := range pod.Status.Conditions {
			if cond.Status == corev1.ConditionFalse && cond.Type != corev1.PodScheduled && cond.Message != "" {
				details = append(details, fmt.Sprintf("%s=%s: %s", cond.Type, cond.Reason, cond.Message))
			}
		}
	}

	msg := fmt.Sprintf("Discovery deadline exceeded (pod phase: %s)", pod.Status.Phase)
	if len(details) > 0 {
		msg = fmt.Sprintf("%s: %s", msg, strings.Join(details, "; "))
	}
	return msg
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
		Watches(
			&corev1.Pod{},
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
