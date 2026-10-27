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
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
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

func TestGetPodWithRetry_SuccessFirstAttempt(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "eval-runner-controller-abc",
			Namespace: "eval-runner",
		},
	}
	fakeClient := k8sfake.NewSimpleClientset(pod)

	var logBuf bytes.Buffer
	logger := zap.New(zap.WriteTo(&logBuf), zap.UseDevMode(true))

	testBackoff := wait.Backoff{
		Duration: time.Millisecond,
		Factor:   1.0,
		Steps:    5,
	}

	gotPod, err := getPodWithRetry(context.Background(), fakeClient, "eval-runner", "eval-runner-controller-abc", testBackoff, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPod == nil || gotPod.Name != "eval-runner-controller-abc" {
		t.Fatalf("gotPod = %v, want pod with name eval-runner-controller-abc", gotPod)
	}
	if logBuf.Len() > 0 {
		t.Errorf("expected no logs on immediate success, got: %s", logBuf.String())
	}
}

func TestGetPodWithRetry_RetryThenSuccess(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "eval-runner-controller-abc",
			Namespace: "eval-runner",
		},
	}
	fakeClient := k8sfake.NewSimpleClientset(pod)

	attempts := 0
	fakeClient.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		attempts++
		if attempts <= 3 {
			return true, nil, fmt.Errorf("dial tcp 10.1.0.1:443: connect: connection refused")
		}
		return false, nil, nil
	})

	var logBuf bytes.Buffer
	logger := zap.New(zap.WriteTo(&logBuf), zap.UseDevMode(true))

	testBackoff := wait.Backoff{
		Duration: time.Millisecond,
		Factor:   1.0,
		Steps:    5,
	}

	gotPod, err := getPodWithRetry(context.Background(), fakeClient, "eval-runner", "eval-runner-controller-abc", testBackoff, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPod == nil || gotPod.Name != "eval-runner-controller-abc" {
		t.Fatalf("gotPod = %v, want pod with name eval-runner-controller-abc", gotPod)
	}
	if attempts != 4 {
		t.Errorf("attempts = %d, want 4", attempts)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "unable to get controller pod; retrying") {
		t.Errorf("expected retry log in output, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "connection refused") {
		t.Errorf("expected connection refused in log output, got: %s", logOutput)
	}
	retryCount := strings.Count(logOutput, "unable to get controller pod; retrying")
	if retryCount != 3 {
		t.Errorf("expected 3 retry logs, got %d in: %s", retryCount, logOutput)
	}
}

func TestGetPodWithRetry_ExhaustedRetries(t *testing.T) {
	fakeClient := k8sfake.NewSimpleClientset()

	attempts := 0
	expectedErr := fmt.Errorf("dial tcp 10.1.0.1:443: connect: connection refused")
	fakeClient.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		attempts++
		return true, nil, expectedErr
	})

	var logBuf bytes.Buffer
	logger := zap.New(zap.WriteTo(&logBuf), zap.UseDevMode(true))

	testBackoff := wait.Backoff{
		Duration: time.Millisecond,
		Factor:   1.0,
		Steps:    3,
	}

	gotPod, err := getPodWithRetry(context.Background(), fakeClient, "eval-runner", "eval-runner-controller-abc", testBackoff, logger)
	if err == nil {
		t.Fatalf("expected error after exhausted retries, got nil and pod %v", gotPod)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected connection refused error, got: %v", err)
	}
	if gotPod != nil {
		t.Errorf("gotPod = %v, want nil", gotPod)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	retryCount := strings.Count(logBuf.String(), "unable to get controller pod; retrying")
	if retryCount != 3 {
		t.Errorf("expected 3 retry logs, got %d", retryCount)
	}
}

func TestGetPodWithRetry_ContextCanceled(t *testing.T) {
	fakeClient := k8sfake.NewSimpleClientset()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before execution

	var logBuf bytes.Buffer
	logger := zap.New(zap.WriteTo(&logBuf), zap.UseDevMode(true))

	testBackoff := wait.Backoff{
		Duration: time.Millisecond,
		Factor:   1.0,
		Steps:    3,
	}

	gotPod, err := getPodWithRetry(ctx, fakeClient, "eval-runner", "eval-runner-controller-abc", testBackoff, logger)
	if err == nil {
		t.Fatalf("expected error on canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got: %v", err)
	}
	if gotPod != nil {
		t.Errorf("gotPod = %v, want nil", gotPod)
	}
}

func TestGetPodWithRetry_NilClient(t *testing.T) {
	testBackoff := wait.Backoff{
		Duration: time.Millisecond,
		Factor:   1.0,
		Steps:    1,
	}
	var logBuf bytes.Buffer
	logger := zap.New(zap.WriteTo(&logBuf), zap.UseDevMode(true))

	gotPod, err := getPodWithRetry(context.Background(), nil, "eval-runner", "eval-runner-controller-abc", testBackoff, logger)
	if err == nil {
		t.Fatalf("expected error for nil client, got nil")
	}
	if gotPod != nil {
		t.Errorf("gotPod = %v, want nil", gotPod)
	}
}

func TestDefaultStartupBackoff(t *testing.T) {
	if defaultStartupBackoff.Steps != 8 {
		t.Errorf("defaultStartupBackoff.Steps = %d, want 8", defaultStartupBackoff.Steps)
	}
	if defaultStartupBackoff.Duration != 1*time.Second {
		t.Errorf("defaultStartupBackoff.Duration = %v, want 1s", defaultStartupBackoff.Duration)
	}
	if defaultStartupBackoff.Factor != 2.0 {
		t.Errorf("defaultStartupBackoff.Factor = %v, want 2.0", defaultStartupBackoff.Factor)
	}
	if defaultStartupBackoff.Jitter != 0.1 {
		t.Errorf("defaultStartupBackoff.Jitter = %v, want 0.1", defaultStartupBackoff.Jitter)
	}
	if defaultStartupRetryTimeout != 2*time.Minute {
		t.Errorf("defaultStartupRetryTimeout = %v, want 2m", defaultStartupRetryTimeout)
	}
}
