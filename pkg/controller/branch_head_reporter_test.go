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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/forge/consumer"
	"github.com/gke-labs/eval-runner/pkg/github"
	"github.com/gke-labs/eval-runner/pkg/sandboxclient"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBranchHeadReporting_Lifecycle(t *testing.T) {
	scheme := setupScheme(t)
	ghServer := github.NewFakeGitHubServer()
	defer ghServer.Close()

	ghClient := ghServer.Client("test-token")

	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	parentTime := now.Add(-1 * time.Hour)

	parentRate := 10.0 / 16.0
	parentRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-parent111",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelTargetRepo: "gke-labs_eval-runner",
				consumer.LabelBranch:     "main",
				consumer.LabelHeadSHA:    "parent1111111",
			},
			Annotations: map[string]string{
				consumer.AnnotationCommitTime: parentTime.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "parent1111111",
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

	headRate := 10.0 / 16.0
	seedRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-head222",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelTargetRepo: "gke-labs_eval-runner",
				consumer.LabelBranch:     "main",
				consumer.LabelHeadSHA:    "head2222222",
			},
			Annotations: map[string]string{
				consumer.AnnotationCommitTime: now.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "head2222222",
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
					SuccessRate: &headRate,
				},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parentRun, seedRun).
		WithStatusSubresource(parentRun, seedRun).
		Build()

	reconciler := &RunReconciler{
		Client:              cl,
		Scheme:              scheme,
		SandboxClient:       &sandboxclient.FakeClient{},
		SandboxNamespace:    "eval-runner-sandbox",
		ControllerNamespace: "eval-runner",
		GitHubClient:        ghClient,
		UIBaseURL:           "https://dotevals.com",
		Now:                 func() time.Time { return now },
	}

	// 1. Seed Run completes -> check run created on the commit with title format
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "base-head222"}}
	_, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	checkRuns := ghServer.CheckRuns()
	if len(checkRuns) != 1 {
		t.Fatalf("expected 1 check run created, got %d", len(checkRuns))
	}
	cr := checkRuns[0]
	if cr.Name != github.CheckRunName {
		t.Errorf("cr.Name = %q, want %q", cr.Name, github.CheckRunName)
	}
	if cr.HeadSHA != "head2222222" {
		t.Errorf("cr.HeadSHA = %q, want head2222222", cr.HeadSHA)
	}
	if cr.Status != "completed" {
		t.Errorf("cr.Status = %q, want completed", cr.Status)
	}
	if cr.Conclusion != "success" {
		t.Errorf("cr.Conclusion = %q, want success", cr.Conclusion)
	}
	// Title format: 63% ± 21 · n=16 · Δ vs parent ...
	if !strings.HasPrefix(cr.Title, "63% ± 21 · n=16 · Δ vs parent") {
		t.Errorf("cr.Title = %q, expected prefix '63%% ± 21 · n=16 · Δ vs parent'", cr.Title)
	}
	if !strings.Contains(cr.Title, "(95% CI") {
		t.Errorf("cr.Title = %q, expected to contain '(95%% CI'", cr.Title)
	}
	wantURL := "https://dotevals.com/github.com/gke-labs/eval-runner/series/main"
	if cr.DetailsURL != wantURL {
		t.Errorf("cr.DetailsURL = %q, want %q", cr.DetailsURL, wantURL)
	}

	// Verify seed Run's status has reporting information
	var updatedSeed v1alpha1.Run
	if err := cl.Get(t.Context(), req.NamespacedName, &updatedSeed); err != nil {
		t.Fatalf("failed to get updated seed run: %v", err)
	}
	if updatedSeed.Status.Reporting == nil || updatedSeed.Status.Reporting.CheckRunID == 0 {
		t.Fatalf("expected non-zero CheckRunID on seed run status, got %+v", updatedSeed.Status.Reporting)
	}
	if updatedSeed.Status.Reporting.LastPostedHash == "" {
		t.Errorf("expected LastPostedHash to be set on seed run status")
	}
	initialCheckRunID := updatedSeed.Status.Reporting.CheckRunID

	// 2. A top-up Run completes -> same check run updated (no second check run)
	topUpRate := 10.0 / 16.0
	topUpRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-head222-2",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelTargetRepo: "gke-labs_eval-runner",
				consumer.LabelBranch:     "main",
				consumer.LabelHeadSHA:    "head2222222",
				consumer.LabelTopUp:      "2",
			},
			Annotations: map[string]string{
				consumer.AnnotationCommitTime: now.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "head2222222",
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
					SuccessRate: &topUpRate,
				},
			},
		},
	}
	if err := cl.Create(t.Context(), topUpRun); err != nil {
		t.Fatalf("failed to create top-up run: %v", err)
	}

	topUpReq := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "base-head222-2"}}
	_, err = reconciler.Reconcile(t.Context(), topUpReq)
	if err != nil {
		t.Fatalf("Reconcile top-up error: %v", err)
	}

	checkRunsAfterTopUp := ghServer.CheckRuns()
	if len(checkRunsAfterTopUp) != 1 {
		t.Fatalf("expected still exactly 1 check run, got %d", len(checkRunsAfterTopUp))
	}
	crTopUp := checkRunsAfterTopUp[0]
	if crTopUp.ID != initialCheckRunID {
		t.Errorf("crTopUp.ID = %d, want initial check run ID %d", crTopUp.ID, initialCheckRunID)
	}
	if !strings.Contains(crTopUp.Title, "n=32") {
		t.Errorf("crTopUp.Title = %q, expected n=32 after pooling 2 runs", crTopUp.Title)
	}

	// 3. Controller restart -> no duplicate check run
	freshReconciler := &RunReconciler{
		Client:              cl,
		Scheme:              scheme,
		SandboxClient:       &sandboxclient.FakeClient{},
		SandboxNamespace:    "eval-runner-sandbox",
		ControllerNamespace: "eval-runner",
		GitHubClient:        ghClient,
		UIBaseURL:           "https://dotevals.com",
		Now:                 func() time.Time { return now },
	}
	_, err = freshReconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile after restart error: %v", err)
	}

	checkRunsAfterRestart := ghServer.CheckRuns()
	if len(checkRunsAfterRestart) != 1 {
		t.Fatalf("expected still exactly 1 check run after restart, got %d", len(checkRunsAfterRestart))
	}
}

func TestBranchHeadReporting_FailedSeed(t *testing.T) {
	scheme := setupScheme(t)
	ghServer := github.NewFakeGitHubServer()
	defer ghServer.Close()

	ghClient := ghServer.Client("test-token")
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	failRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-fail333",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelTargetRepo: "gke-labs_eval-runner",
				consumer.LabelBranch:     "main",
				consumer.LabelHeadSHA:    "fail3333333",
			},
			Annotations: map[string]string{
				consumer.AnnotationCommitTime: now.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "fail3333333",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase:   v1alpha1.RunPhaseFailed,
			Message: "all 2 tasks failed",
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:      "task-1",
					Phase:   v1alpha1.TaskPhaseFailed,
					Message: "syntax error",
				},
				{
					ID:      "task-2",
					Phase:   v1alpha1.TaskPhaseFailed,
					Message: "import error",
				},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(failRun).
		WithStatusSubresource(failRun).
		Build()

	reconciler := &RunReconciler{
		Client:              cl,
		Scheme:              scheme,
		SandboxClient:       &sandboxclient.FakeClient{},
		SandboxNamespace:    "eval-runner-sandbox",
		ControllerNamespace: "eval-runner",
		GitHubClient:        ghClient,
		UIBaseURL:           "https://dotevals.com",
		Now:                 func() time.Time { return now },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "base-fail333"}}
	_, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile failed seed error: %v", err)
	}

	checkRuns := ghServer.CheckRuns()
	if len(checkRuns) != 1 {
		t.Fatalf("expected 1 check run, got %d", len(checkRuns))
	}
	cr := checkRuns[0]
	if cr.Conclusion != "failure" {
		t.Errorf("cr.Conclusion = %q, want failure", cr.Conclusion)
	}
	if cr.Title != "evals failed: all 2 tasks failed" {
		t.Errorf("cr.Title = %q, want 'evals failed: all 2 tasks failed'", cr.Title)
	}
}

func generateTestRSAKey(t *testing.T) []byte {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate test RSA key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	})
}

func TestBranchHeadReporting_GitHubAppSecretConfigured(t *testing.T) {
	scheme := setupScheme(t)
	ghServer := github.NewFakeGitHubServer()
	defer ghServer.Close()

	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	appSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-app",
			Namespace: "eval-runner",
		},
		Data: map[string][]byte{
			"app-id":          []byte("12345"),
			"installation-id": []byte("67890"),
			"private-key":     generateTestRSAKey(t),
			"app-slug":        []byte("eval-runner-app"),
		},
	}

	headRate := 1.0
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-app444",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelTargetRepo: "gke-labs_eval-runner",
				consumer.LabelBranch:     "main",
				consumer.LabelHeadSHA:    "app4444444",
			},
			Annotations: map[string]string{
				consumer.AnnotationCommitTime: now.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "app4444444",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-1",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     10,
					Successes:   10,
					SuccessRate: &headRate,
				},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(appSecret, run).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:              cl,
		Scheme:              scheme,
		SandboxClient:       &sandboxclient.FakeClient{},
		SandboxNamespace:    "eval-runner-sandbox",
		ControllerNamespace: "eval-runner",
		GitHubAppSecret:     "github-app",
		GitHubBaseURL:       ghServer.URL(),
		UIBaseURL:           "https://dotevals.com",
		Now:                 func() time.Time { return now },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "base-app444"}}
	_, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error with GitHub App credentials: %v", err)
	}

	checkRuns := ghServer.CheckRuns()
	if len(checkRuns) != 1 {
		t.Fatalf("expected 1 check run created via GitHub App credentials, got %d", len(checkRuns))
	}
}

func TestBranchHeadReporting_SilentlyDisabledWithoutCredentials(t *testing.T) {
	scheme := setupScheme(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	headRate := 1.0
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-nocreds",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelTargetRepo: "gke-labs_eval-runner",
				consumer.LabelBranch:     "main",
				consumer.LabelHeadSHA:    "nocreds111",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-1",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     10,
					Successes:   10,
					SuccessRate: &headRate,
				},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(run).
		Build()

	reconciler := &RunReconciler{
		Client:              cl,
		Scheme:              scheme,
		SandboxClient:       &sandboxclient.FakeClient{},
		SandboxNamespace:    "eval-runner-sandbox",
		ControllerNamespace: "eval-runner",
		GitHubAppSecret:     "",
		GitHubTokenSecret:   "",
		GitHubClient:        nil,
		Now:                 func() time.Time { return now },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "base-nocreds"}}
	_, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("expected no error when credentials missing, got %v", err)
	}
	if !reconciler.noCredsLogged {
		t.Errorf("expected noCredsLogged to be true")
	}

	// Reconcile second time, still no error
	_, err = reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("expected no error on second reconcile, got %v", err)
	}
}

func TestBranchHeadReporting_InProgressOnSeedStart(t *testing.T) {
	scheme := setupScheme(t)
	ghServer := github.NewFakeGitHubServer()
	defer ghServer.Close()

	ghClient := ghServer.Client("test-token")
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	// Case 1: Pending -> "queued"
	pendingRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-queued555",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelTargetRepo: "gke-labs_eval-runner",
				consumer.LabelBranch:     "main",
				consumer.LabelHeadSHA:    "queued5555555",
			},
			Annotations: map[string]string{
				consumer.AnnotationCommitTime: now.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "queued5555555",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhasePending,
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pendingRun).
		WithStatusSubresource(pendingRun).
		Build()

	reconciler := &RunReconciler{
		Client:              cl,
		Scheme:              scheme,
		SandboxClient:       &sandboxclient.FakeClient{},
		SandboxNamespace:    "eval-runner-sandbox",
		ControllerNamespace: "eval-runner",
		GitHubClient:        ghClient,
		UIBaseURL:           "https://dotevals.com",
		Now:                 func() time.Time { return now },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "base-queued555"}}
	_, _ = reconciler.Reconcile(t.Context(), req)

	checkRuns := ghServer.CheckRuns()
	if len(checkRuns) != 1 {
		t.Fatalf("expected 1 in_progress check run, got %d", len(checkRuns))
	}
	cr := checkRuns[0]
	if cr.Status != "in_progress" {
		t.Errorf("cr.Status = %q, want in_progress", cr.Status)
	}
	if cr.Title != "queued" {
		t.Errorf("cr.Title = %q, want queued", cr.Title)
	}

	// Case 2: Running -> "running"
	runningRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-running555",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelTargetRepo: "gke-labs_eval-runner",
				consumer.LabelBranch:     "main",
				consumer.LabelHeadSHA:    "running5555555",
			},
			Annotations: map[string]string{
				consumer.AnnotationCommitTime: now.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "running5555555",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseRunning,
		},
	}
	_ = cl.Create(t.Context(), runningRun)
	req2 := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "base-running555"}}
	_, _ = reconciler.Reconcile(t.Context(), req2)

	checkRuns2 := ghServer.CheckRuns()
	if len(checkRuns2) != 2 {
		t.Fatalf("expected 2 in_progress check runs, got %d", len(checkRuns2))
	}
	if checkRuns2[1].Title != "running" {
		t.Errorf("checkRuns2[1].Title = %q, want running", checkRuns2[1].Title)
	}
}
