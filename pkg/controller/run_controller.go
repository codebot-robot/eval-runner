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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/discovery"
	"github.com/gke-labs/eval-runner/pkg/sandboxclient"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	// DefaultMaxDiscoveryAttempts is the default maximum number of discovery attempts for clone retries.
	DefaultMaxDiscoveryAttempts = 5

	// DefaultDiscoveryDeadline is the default overall deadline for discovery to complete.
	DefaultDiscoveryDeadline = 10 * time.Minute
)

// DefaultDiscoveryRetryBackoff defines the default backoff intervals between clone retry attempts.
var DefaultDiscoveryRetryBackoff = []time.Duration{
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	40 * time.Second,
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

func isNotFoundError(err error) bool {
	return status.Code(err) == codes.NotFound
}

func isPermissionError(err error) bool {
	return status.Code(err) == codes.PermissionDenied
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
	APIReader     client.Reader
	Scheme        *runtime.Scheme
	ClientFactory sandboxclient.Factory
	// SandboxClient can be set directly to override ClientFactory (e.g. for unit testing).
	SandboxClient    sandboxclient.Client
	SandboxNamespace string
	MaxStdoutBytes   int64
	MaxStderrBytes   int64
	MaxMessageBytes  int

	// DiscoveryDeadline is the overall deadline duration for discovery to complete.
	// Defaults to DefaultDiscoveryDeadline (10m) if <= 0.
	DiscoveryDeadline time.Duration

	// Now returns the current time. In tests, this can be overridden.
	Now func() time.Time

	// MaxDiscoveryAttempts is the maximum number of attempts for the git clone step.
	// Defaults to DefaultMaxDiscoveryAttempts (5) if <= 0.
	MaxDiscoveryAttempts int

	// DiscoveryRetryBackoff defines the backoff schedule between clone retry attempts.
	// Defaults to DefaultDiscoveryRetryBackoff if empty.
	DiscoveryRetryBackoff []time.Duration

	// Sleep pauses execution between retries. In tests, this can be overridden to return immediately.
	Sleep func(ctx context.Context, d time.Duration) error

	mu     sync.RWMutex
	tokens map[string]string // key: namespace/sandboxName -> bearer token
}

func (r *RunReconciler) getAuthToken(key string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.tokens == nil {
		return "", false
	}
	t, ok := r.tokens[key]
	return t, ok
}

func (r *RunReconciler) setAuthToken(key, token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tokens == nil {
		r.tokens = make(map[string]string)
	}
	r.tokens[key] = token
}

func (r *RunReconciler) deleteAuthToken(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tokens != nil {
		delete(r.tokens, key)
	}
}

func generateAuthToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate auth token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (r *RunReconciler) ensureAuthToken(ctx context.Context, run *v1alpha1.Run, sandboxKey types.NamespacedName) (string, error) {
	if token, ok := r.getAuthToken(sandboxKey.String()); ok && token != "" {
		return token, nil
	}

	secretNamespace := run.Namespace
	if secretNamespace == "" {
		secretNamespace = discovery.DefaultControllerNamespace
	}
	secretName := discovery.AuthSecretNameForRun(run.Name)
	secretKey := types.NamespacedName{Namespace: secretNamespace, Name: secretName}

	reader := client.Reader(r.Client)
	if r.APIReader != nil {
		reader = r.APIReader
	}

	var secret corev1.Secret
	if err := reader.Get(ctx, secretKey, &secret); err == nil {
		if tokenBytes, ok := secret.Data["token"]; ok && len(tokenBytes) > 0 {
			token := string(tokenBytes)
			r.setAuthToken(sandboxKey.String(), token)
			return token, nil
		}
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("failed to get auth secret: %w", err)
	}

	token, err := generateAuthToken()
	if err != nil {
		return "", err
	}

	secret = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: secretNamespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"token": []byte(token),
		},
	}
	if r.Scheme != nil {
		if err := controllerutil.SetControllerReference(run, &secret, r.Scheme); err != nil {
			return "", fmt.Errorf("failed to set controller reference on auth secret: %w", err)
		}
	}

	if err := r.Create(ctx, &secret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return "", fmt.Errorf("failed to create auth secret: %w", err)
		}
		if getErr := reader.Get(ctx, secretKey, &secret); getErr != nil {
			return "", fmt.Errorf("failed to get existing auth secret: %w", getErr)
		}
		if tokenBytes, ok := secret.Data["token"]; ok && len(tokenBytes) > 0 {
			token = string(tokenBytes)
		}
	}

	r.setAuthToken(sandboxKey.String(), token)
	return token, nil
}

func (r *RunReconciler) cleanupAuthToken(ctx context.Context, runNamespace, runName string, sandboxKey types.NamespacedName) {
	r.deleteAuthToken(sandboxKey.String())
	if runNamespace == "" {
		runNamespace = discovery.DefaultControllerNamespace
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      discovery.AuthSecretNameForRun(runName),
			Namespace: runNamespace,
		},
	}
	if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		logger := log.FromContext(ctx)
		logger.V(1).Info("Failed to delete auth secret", "secret", secret.Name, "error", err)
	}
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

func (r *RunReconciler) maxDiscoveryAttempts() int {
	if r.MaxDiscoveryAttempts > 0 {
		return r.MaxDiscoveryAttempts
	}
	return DefaultMaxDiscoveryAttempts
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
//  2. Wait for sandbox pod Ready, drive step-based discovery via sandboxd (gRPC ProcessService).
//     - Step 1: git clone -- <url> repo (retries up to N times on failure)
//     - Step 2: git checkout <commit> (fails immediately on error)
//     - Step 3: ./.evals/list (fails immediately on error, typed outcomes)
//  3. Parse and validate manifest, write status.manifest, set Discovered.
//     On failure, set Failed with message naming the failed step.
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
		authToken, tokenErr := r.ensureAuthToken(ctx, &run, sandboxKey)
		if tokenErr != nil {
			return ctrl.Result{}, fmt.Errorf("failed to ensure auth token: %w", tokenErr)
		}
		tokenDigest := discovery.AuthTokenDigest(authToken)

		logger.Info("Creating discovery sandbox", "sandbox", sandboxName, "namespace", sandboxNamespace)
		desiredSandbox := discovery.BuildSandbox(&run, sandboxNamespace, tokenDigest)
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

	// Obtain sandbox client (via direct instance or factory from pod IP)
	var sbClient sandboxclient.Client
	if r.SandboxClient != nil {
		sbClient = r.SandboxClient
	} else {
		if pod.Status.PodIP == "" {
			logger.Info("Pod IP not yet assigned", "pod", podKey)
			return ctrl.Result{RequeueAfter: r.requeueDelay(run.Status.DiscoveryDeadline)}, nil
		}
		factory := r.ClientFactory
		if factory == nil {
			factory = sandboxclient.DefaultFactory(
				sandboxclient.WithStdoutLimit(r.maxStdoutBytes()),
				sandboxclient.WithStderrLimit(r.maxStderrBytes()),
			)
		}
		var opts []sandboxclient.Option
		authToken, tokenErr := r.ensureAuthToken(ctx, &run, sandboxKey)
		if tokenErr != nil {
			logger.V(1).Info("Could not retrieve auth token for sandbox", "error", tokenErr)
		} else if authToken != "" {
			opts = append(opts, sandboxclient.WithAuthToken(authToken))
		}
		var clientErr error
		sbClient, clientErr = factory.NewClient(pod.Status.PodIP, opts...)
		if clientErr != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create sandbox client for %s: %w", pod.Status.PodIP, clientErr)
		}
		defer func() {
			if closeErr := sbClient.Close(); closeErr != nil {
				logger.Error(closeErr, "failed to close sandbox client")
			}
		}()
	}

	// Set execution deadline context
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

	maxAttempts := r.maxDiscoveryAttempts()
	backoffs := r.DiscoveryRetryBackoff
	if len(backoffs) == 0 {
		backoffs = DefaultDiscoveryRetryBackoff
	}

	sleepFn := r.Sleep
	if sleepFn == nil {
		sleepFn = defaultSleep
	}

	// -------------------------------------------------------------------------
	// Step 1: git clone -- <url> repo (retry up to N times on failure)
	// -------------------------------------------------------------------------
	logger.Info("Sandbox pod is ready; starting step 1: git clone", "pod", podKey)
	var cloneRes *sandboxclient.ExecuteResult
	var cloneErr error
	var cloneAttempts int

	for cloneAttempts = 1; cloneAttempts <= maxAttempts; cloneAttempts++ {
		if run.Status.DiscoveryDeadline != nil && !r.now().Before(run.Status.DiscoveryDeadline.Time) {
			break
		}

		// Clean up any previous partial clone before attempting
		if _, cleanErr := sbClient.Execute(execCtx, sandboxclient.ProcessConfig{
			Command: []string{"rm", "-rf", "repo"},
		}); cleanErr != nil {
			logger.V(1).Info("Failed to clean repo directory before clone attempt", "error", cleanErr)
		}

		cloneRes, cloneErr = sbClient.Execute(execCtx, sandboxclient.ProcessConfig{
			Command: []string{"git", "clone", "--", run.Spec.Repo.URL, "repo"},
		})

		if cloneErr == nil && cloneRes != nil && cloneRes.ExitCode == 0 {
			break
		}

		if cloneAttempts >= maxAttempts || execCtx.Err() != nil {
			break
		}

		if run.Status.DiscoveryDeadline != nil && !r.now().Before(run.Status.DiscoveryDeadline.Time) {
			break
		}

		backoffDuration := backoffForAttempt(backoffs, cloneAttempts)
		if run.Status.DiscoveryDeadline != nil {
			rem := run.Status.DiscoveryDeadline.Sub(r.now())
			if rem <= 0 {
				break
			}
			if rem < backoffDuration {
				backoffDuration = rem
			}
		}

		logger.Info("Clone step failed; retrying with backoff",
			"attempt", cloneAttempts,
			"maxAttempts", maxAttempts,
			"error", cloneErr,
			"backoff", backoffDuration,
		)

		attemptMsg := fmt.Sprintf("Running discovery in sandbox (step git clone attempt %d of %d failed; retrying)", cloneAttempts, maxAttempts)
		retryMsg := fmt.Sprintf("step git clone attempt %d of %d failed; retrying", cloneAttempts, maxAttempts)
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
			logger.Error(updateErr, "failed to update run status during retry", "attempt", cloneAttempts)
		}

		if err := sleepFn(execCtx, backoffDuration); err != nil {
			logger.Info("Context done while waiting to retry clone", "error", err)
			break
		}
	}

	// Evaluate git clone outcome
	if cloneErr != nil || cloneRes == nil || cloneRes.ExitCode != 0 {
		var cloneStderr string
		var exitCode int
		if cloneRes != nil {
			cloneStderr = cloneRes.Stderr
			exitCode = cloneRes.ExitCode
		}
		tail := extractStderrTail(cloneStderr, r.maxStderrLines(), discovery.MaxStderrTailBytes)
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
		if tail != "" {
			msg = fmt.Sprintf("step git clone failed after %s: exit code %d: %s", formatAttemptCount(cloneAttempts), exitCode, tail)
		} else if cloneErr != nil {
			msg = fmt.Sprintf("step git clone failed after %s: %v", formatAttemptCount(cloneAttempts), cloneErr)
		} else {
			msg = fmt.Sprintf("step git clone failed after %s: exit code %d", formatAttemptCount(cloneAttempts), exitCode)
		}
		return r.failRun(ctx, &run, &sandbox, msg)
	}

	// -------------------------------------------------------------------------
	// Step 2: git checkout <commit> (no retry: commit not found is permanent)
	// -------------------------------------------------------------------------
	logger.Info("Step 1 succeeded; starting step 2: git checkout", "commit", run.Spec.Repo.Commit)
	checkoutRes, checkoutErr := sbClient.Execute(execCtx, sandboxclient.ProcessConfig{
		Command: []string{"git", "checkout", run.Spec.Repo.Commit},
		Cwd:     "repo",
	})
	if checkoutErr != nil || checkoutRes == nil || checkoutRes.ExitCode != 0 {
		var checkoutStderr string
		var exitCode int
		if checkoutRes != nil {
			checkoutStderr = checkoutRes.Stderr
			exitCode = checkoutRes.ExitCode
		}
		tail := extractStderrTail(checkoutStderr, r.maxStderrLines(), discovery.MaxStderrTailBytes)
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
		if tail != "" {
			msg = fmt.Sprintf("step git checkout failed: exit code %d: %s", exitCode, tail)
		} else if checkoutErr != nil {
			msg = fmt.Sprintf("step git checkout failed: %v", checkoutErr)
		} else {
			msg = fmt.Sprintf("step git checkout failed: exit code %d", exitCode)
		}
		return r.failRun(ctx, &run, &sandbox, msg)
	}

	// -------------------------------------------------------------------------
	// Step 3: ./.evals/list (no retry: missing/non-executable/non-zero/invalid JSON)
	// -------------------------------------------------------------------------
	logger.Info("Step 2 succeeded; starting step 3: ./.evals/list")
	listRes, listErr := sbClient.Execute(execCtx, sandboxclient.ProcessConfig{
		Command: []string{"./.evals/list"},
		Cwd:     "repo",
	})

	if listErr != nil {
		var limitErr *discovery.LimitError
		if errors.As(listErr, &limitErr) {
			var tail string
			if listRes != nil {
				tail = extractStderrTail(listRes.Stderr, r.maxStderrLines(), discovery.MaxStderrTailBytes)
			}
			msg := fmt.Sprintf("step ./.evals/list failed: %v", limitErr)
			if tail != "" {
				msg += "\n" + tail
			}
			return r.failRun(ctx, &run, &sandbox, msg)
		}

		if isNotFoundError(listErr) {
			return r.failRun(ctx, &run, &sandbox, fmt.Sprintf("step ./.evals/list failed: missing .evals/list: %v", listErr))
		}
		if isPermissionError(listErr) {
			return r.failRun(ctx, &run, &sandbox, fmt.Sprintf("step ./.evals/list failed: .evals/list is not executable: %v", listErr))
		}
		return r.failRun(ctx, &run, &sandbox, fmt.Sprintf("step ./.evals/list failed: %v", listErr))
	}

	if listRes == nil {
		return r.failRun(ctx, &run, &sandbox, "step ./.evals/list failed: nil response")
	}

	if listRes.ExitCode != 0 {
		tail := extractStderrTail(listRes.Stderr, r.maxStderrLines(), discovery.MaxStderrTailBytes)
		var msg string
		if tail != "" {
			msg = fmt.Sprintf("step ./.evals/list failed with exit code %d: %s", listRes.ExitCode, tail)
		} else {
			msg = fmt.Sprintf("step ./.evals/list failed with exit code %d", listRes.ExitCode)
		}
		return r.failRun(ctx, &run, &sandbox, msg)
	}

	// Defense-in-depth: validate stdout and stderr sizes even if executor did not report limit error
	stdoutLimit := r.maxStdoutBytes()
	stderrLimit := r.maxStderrBytes()

	if int64(len(listRes.Stdout)) > stdoutLimit {
		return r.failRun(ctx, &run, &sandbox, fmt.Sprintf("step ./.evals/list failed: %v", &discovery.LimitError{
			Stream: "stdout",
			Limit:  stdoutLimit,
		}))
	}
	if int64(len(listRes.Stderr)) > stderrLimit {
		tail := extractStderrTail(listRes.Stderr, r.maxStderrLines(), discovery.MaxStderrTailBytes)
		msg := fmt.Sprintf("step ./.evals/list failed: %v", &discovery.LimitError{
			Stream: "stderr",
			Limit:  stderrLimit,
		})
		if tail != "" {
			msg += "\n" + tail
		}
		return r.failRun(ctx, &run, &sandbox, msg)
	}

	// Step 4: Parse & validate manifest
	manifest, err := discovery.ParseManifest([]byte(listRes.Stdout))
	if err != nil {
		tail := extractStderrTail(listRes.Stderr, r.maxStderrLines(), discovery.MaxStderrTailBytes)
		msg := fmt.Sprintf("step ./.evals/list failed: invalid JSON: %v", err)
		if tail != "" {
			msg += "\n" + tail
		}
		return r.failRun(ctx, &run, &sandbox, msg)
	}

	if err := discovery.ValidateManifest(manifest); err != nil {
		return r.failRun(ctx, &run, &sandbox, fmt.Sprintf("Manifest validation failed: %v", err))
	}

	// Step 5: Success -> update Run to Discovered, then cleanup sandbox
	successMsg := ""
	condMsg := fmt.Sprintf("Discovered %d tasks", len(manifest.Tasks))
	if cloneAttempts > 1 {
		successMsg = fmt.Sprintf("Discovered after %s", formatAttemptCount(cloneAttempts))
		condMsg = fmt.Sprintf("Discovered %d tasks after %s", len(manifest.Tasks), formatAttemptCount(cloneAttempts))
	}

	logger.Info("Discovery successful; recording manifest and cleaning up sandbox",
		"tasks", len(manifest.Tasks),
		"cloneAttempts", cloneAttempts,
	)
	err = r.updateRunStatus(ctx, req.NamespacedName, func(status *v1alpha1.RunStatus, gen int64) {
		status.Phase = v1alpha1.RunPhaseDiscovered
		status.Message = successMsg
		status.Manifest = manifest
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionTypeDiscovered,
			Status:             metav1.ConditionTrue,
			Reason:             "Discovered",
			Message:            condMsg,
			ObservedGeneration: gen,
		})
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.Delete(ctx, &sandbox); err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "failed to delete sandbox", "sandbox", sandbox.Name)
	}
	r.cleanupAuthToken(ctx, run.Namespace, run.Name, sandboxKey)

	return ctrl.Result{}, nil
}

func (r *RunReconciler) deleteSandboxIfMatching(ctx context.Context, sandboxKey types.NamespacedName, runNamespace, runName string) error {
	logger := log.FromContext(ctx)
	r.cleanupAuthToken(ctx, runNamespace, runName, sandboxKey)
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
		sandboxKey := types.NamespacedName{Namespace: sandbox.Namespace, Name: sandbox.Name}
		r.cleanupAuthToken(ctx, run.Namespace, run.Name, sandboxKey)
	} else {
		sbNamespace := r.SandboxNamespace
		if sbNamespace == "" {
			sbNamespace = discovery.DefaultSandboxNamespace
		}
		sandboxKey := types.NamespacedName{Namespace: sbNamespace, Name: discovery.SandboxNameForRun(run.Name)}
		r.cleanupAuthToken(ctx, run.Namespace, run.Name, sandboxKey)
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
