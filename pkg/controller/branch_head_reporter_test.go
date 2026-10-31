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
	"sync"
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

	// Verify rich output: text carries table and footer, images carries sparkline URL
	if !strings.Contains(cr.Text, "| eval | samples | successes | rate (95% CI) |") {
		t.Errorf("expected table header in cr.Text, got:\n%s", cr.Text)
	}
	if !strings.Contains(cr.Text, "[task-1](https://dotevals.com/github.com/gke-labs/eval-runner/runs/base-head222/tasks/task-1/samples)") {
		t.Errorf("expected sample link in cr.Text, got:\n%s", cr.Text)
	}
	if !strings.Contains(cr.Text, "[series](https://dotevals.com/github.com/gke-labs/eval-runner/series/main)") {
		t.Errorf("expected series link in cr.Text, got:\n%s", cr.Text)
	}
	if strings.Contains(cr.Text, "/tmp/") || strings.Contains(cr.Text, "gs://") {
		t.Errorf("forbidden artifact paths in cr.Text:\n%s", cr.Text)
	}
	if strings.Contains(cr.Text, "/runs/eval-runner") || strings.Contains(cr.Text, "/comparisons/eval-runner") {
		t.Errorf("forbidden namespaced routes in cr.Text:\n%s", cr.Text)
	}
	if len(cr.Images) != 1 {
		t.Fatalf("expected 1 image in cr.Images, got %d", len(cr.Images))
	}
	wantSparkline := "https://dotevals.com/github.com/gke-labs/eval-runner/sparkline.svg?branch=main&w=600&h=80"
	if cr.Images[0].ImageURL != wantSparkline {
		t.Errorf("cr.Images[0].ImageURL = %q, want %q", cr.Images[0].ImageURL, wantSparkline)
	}
	if cr.Images[0].Caption != "main, last 20 commits" {
		t.Errorf("cr.Images[0].Caption = %q, want 'main, last 20 commits'", cr.Images[0].Caption)
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
	if !strings.Contains(crTopUp.Text, "| 32 | 20 |") {
		t.Errorf("expected pooled 32 samples and 20 successes in crTopUp.Text, got:\n%s", crTopUp.Text)
	}
	if !strings.Contains(crTopUp.Text, "[series](https://dotevals.com/github.com/gke-labs/eval-runner/series/main)") {
		t.Errorf("expected series link in crTopUp.Text, got:\n%s", crTopUp.Text)
	}
	if strings.Contains(crTopUp.Text, "/tmp/") || strings.Contains(crTopUp.Text, "gs://") {
		t.Errorf("forbidden artifact paths in crTopUp.Text:\n%s", crTopUp.Text)
	}
	if strings.Contains(crTopUp.Text, "/runs/eval-runner") || strings.Contains(crTopUp.Text, "/comparisons/eval-runner") {
		t.Errorf("forbidden namespaced routes in crTopUp.Text:\n%s", crTopUp.Text)
	}
	if len(crTopUp.Images) != 1 || crTopUp.Images[0].ImageURL != wantSparkline {
		t.Errorf("expected sparkline image in crTopUp.Images, got %+v", crTopUp.Images)
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
	if err := cl.Create(t.Context(), runningRun); err != nil {
		t.Fatalf("cl.Create error: %v", err)
	}
	runningRun.Status.Phase = v1alpha1.RunPhaseRunning
	if err := cl.Status().Update(t.Context(), runningRun); err != nil {
		t.Fatalf("cl.Status().Update error: %v", err)
	}
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

func TestBranchHeadReporting_ImagesOmittedWhenNoUIBaseURL(t *testing.T) {
	scheme := setupScheme(t)
	ghServer := github.NewFakeGitHubServer()
	defer ghServer.Close()

	ghClient := ghServer.Client("test-token")
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	headRate := 1.0

	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-noui111",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelTargetRepo: "gke-labs_eval-runner",
				consumer.LabelBranch:     "main",
				consumer.LabelHeadSHA:    "noui1111111",
			},
			Annotations: map[string]string{
				consumer.AnnotationCommitTime: now.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "noui1111111",
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
		GitHubClient:        ghClient,
		UIBaseURL:           "", // Empty UI Base URL
		Now:                 func() time.Time { return now },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "base-noui111"}}
	_, err := reconciler.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	checkRuns := ghServer.CheckRuns()
	if len(checkRuns) != 1 {
		t.Fatalf("expected 1 check run, got %d", len(checkRuns))
	}
	cr := checkRuns[0]
	if len(cr.Images) != 0 {
		t.Errorf("expected Images to be omitted when uiBaseURL is empty, got %+v", cr.Images)
	}
	if strings.Contains(cr.Text, "[series]") {
		t.Errorf("unexpected '[series]' link in cr.Text when uiBaseURL is empty: %s", cr.Text)
	}
	if strings.Contains(cr.Text, "/tmp/") || strings.Contains(cr.Text, "gs://") {
		t.Errorf("forbidden artifact paths in cr.Text: %s", cr.Text)
	}
	if strings.Contains(cr.Text, "/runs/eval-runner") || strings.Contains(cr.Text, "/comparisons/eval-runner") {
		t.Errorf("forbidden namespaced routes in cr.Text: %s", cr.Text)
	}
}

func TestBranchHeadReporting_Backfill(t *testing.T) {
	scheme := setupScheme(t)
	ghServer := github.NewFakeGitHubServer()
	defer ghServer.Close()

	ghClient := ghServer.Client("test-token")
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	parentTime := now.Add(-1 * time.Hour)

	parentRate := 10.0 / 16.0
	// 1. Older commit's completed seed Run (no checkRunID)
	parentRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-parent888",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelTargetRepo: "gke-labs_eval-runner",
				consumer.LabelBranch:     "main",
				consumer.LabelHeadSHA:    "parent8888888",
			},
			Annotations: map[string]string{
				consumer.AnnotationCommitTime: parentTime.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "parent8888888",
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

	headRate := 12.0 / 16.0
	// 2. Newest head commit's completed seed Run (no checkRunID)
	seedRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-head999",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelTargetRepo: "gke-labs_eval-runner",
				consumer.LabelBranch:     "main",
				consumer.LabelHeadSHA:    "head9999999",
			},
			Annotations: map[string]string{
				consumer.AnnotationCommitTime: now.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "head9999999",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-1",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     16,
					Successes:   12,
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

	// Run startup backfill pass
	count, err := reconciler.BackfillBranchHeadCheckRuns(t.Context())
	if err != nil {
		t.Fatalf("BackfillBranchHeadCheckRuns error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected backfill count 1, got %d", count)
	}

	// Exactly 1 check run posted to fake GitHub
	checkRuns := ghServer.CheckRuns()
	if len(checkRuns) != 1 {
		t.Fatalf("expected exactly 1 check run, got %d", len(checkRuns))
	}

	// Bounded to the newest head per branch: posted for head9999999, NOT parent8888888
	cr := checkRuns[0]
	if cr.HeadSHA != "head9999999" {
		t.Errorf("cr.HeadSHA = %q, want head9999999 (newest head commit only)", cr.HeadSHA)
	}
	if cr.Status != "completed" {
		t.Errorf("cr.Status = %q, want completed", cr.Status)
	}
	if !strings.Contains(cr.Text, "| eval | samples | successes | rate (95% CI) |") {
		t.Errorf("expected table header in cr.Text, got:\n%s", cr.Text)
	}
	if len(cr.Images) != 1 {
		t.Fatalf("expected 1 sparkline image, got %d", len(cr.Images))
	}

	// Verify seed Run's status has reporting information updated
	var updatedSeed v1alpha1.Run
	seedKey := types.NamespacedName{Namespace: "eval-runner", Name: "base-head999"}
	if err := cl.Get(t.Context(), seedKey, &updatedSeed); err != nil {
		t.Fatalf("failed to get updated seed run: %v", err)
	}
	if updatedSeed.Status.Reporting == nil || updatedSeed.Status.Reporting.CheckRunID == 0 {
		t.Fatalf("expected non-zero CheckRunID on seed run status, got %+v", updatedSeed.Status.Reporting)
	}

	// Older parent Run must NOT have reporting status
	var updatedParent v1alpha1.Run
	parentKey := types.NamespacedName{Namespace: "eval-runner", Name: "base-parent888"}
	if err := cl.Get(t.Context(), parentKey, &updatedParent); err != nil {
		t.Fatalf("failed to get updated parent run: %v", err)
	}
	if updatedParent.Status.Reporting != nil && updatedParent.Status.Reporting.CheckRunID != 0 {
		t.Errorf("unexpected checkRunID on older parent run: %+v", updatedParent.Status.Reporting)
	}

	// Second backfill pass must be a no-op (idempotent; 0 backfilled)
	count2, err := reconciler.BackfillBranchHeadCheckRuns(t.Context())
	if err != nil {
		t.Fatalf("second BackfillBranchHeadCheckRuns error: %v", err)
	}
	if count2 != 0 {
		t.Errorf("expected count 0 on second backfill, got %d", count2)
	}
	if len(ghServer.CheckRuns()) != 1 {
		t.Errorf("expected still exactly 1 check run, got %d", len(ghServer.CheckRuns()))
	}
}

func TestBranchHeadReporting_BackfillFailedSeed(t *testing.T) {
	scheme := setupScheme(t)
	ghServer := github.NewFakeGitHubServer()
	defer ghServer.Close()

	ghClient := ghServer.Client("test-token")
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	failRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-failhead",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelTargetRepo: "gke-labs_eval-runner",
				consumer.LabelBranch:     "main",
				consumer.LabelHeadSHA:    "failhead111",
			},
			Annotations: map[string]string{
				consumer.AnnotationCommitTime: now.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "failhead111",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseFailed,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:      "task-1",
					Phase:   v1alpha1.TaskPhaseFailed,
					Message: "syntax error in eval script",
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

	count, err := reconciler.BackfillBranchHeadCheckRuns(t.Context())
	if err != nil {
		t.Fatalf("BackfillBranchHeadCheckRuns error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}

	checkRuns := ghServer.CheckRuns()
	if len(checkRuns) != 1 {
		t.Fatalf("expected 1 check run, got %d", len(checkRuns))
	}
	cr := checkRuns[0]
	if cr.HeadSHA != "failhead111" {
		t.Errorf("cr.HeadSHA = %q, want failhead111", cr.HeadSHA)
	}
	if cr.Conclusion != "failure" {
		t.Errorf("cr.Conclusion = %q, want failure", cr.Conclusion)
	}

	var updated v1alpha1.Run
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner", Name: "base-failhead"}, &updated); err != nil {
		t.Fatalf("failed to get updated run: %v", err)
	}
	if updated.Status.Reporting == nil || updated.Status.Reporting.CheckRunID == 0 {
		t.Fatalf("expected non-zero CheckRunID on failed seed run status, got %+v", updated.Status.Reporting)
	}
}

func TestBranchHeadReporting_BackfillConcurrentWithReconcile(t *testing.T) {
	scheme := setupScheme(t)
	ghServer := github.NewFakeGitHubServer()
	defer ghServer.Close()

	ghClient := ghServer.Client("test-token")
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	headRate := 1.0

	seedRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-racehead",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelTargetRepo: "gke-labs_eval-runner",
				consumer.LabelBranch:     "main",
				consumer.LabelHeadSHA:    "racehead111",
			},
			Annotations: map[string]string{
				consumer.AnnotationCommitTime: now.Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: "racehead111",
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
		WithObjects(seedRun).
		WithStatusSubresource(seedRun).
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

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "eval-runner", Name: "base-racehead"}}

	var wg sync.WaitGroup
	startCh := make(chan struct{})
	errCh := make(chan error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-startCh
		_, err := reconciler.Reconcile(t.Context(), req)
		if err != nil {
			errCh <- err
		}
	}()
	go func() {
		defer wg.Done()
		<-startCh
		_, err := reconciler.BackfillBranchHeadCheckRuns(t.Context())
		if err != nil {
			errCh <- err
		}
	}()

	close(startCh)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent reporting error: %v", err)
	}

	checkRuns := ghServer.CheckRuns()
	if len(checkRuns) != 1 {
		t.Fatalf("expected exactly 1 check run from concurrent reconcile and backfill, got %d", len(checkRuns))
	}
	cr := checkRuns[0]
	if cr.HeadSHA != "racehead111" {
		t.Errorf("cr.HeadSHA = %q, want racehead111", cr.HeadSHA)
	}

	var updated v1alpha1.Run
	if err := cl.Get(t.Context(), req.NamespacedName, &updated); err != nil {
		t.Fatalf("failed to get updated run: %v", err)
	}
	if updated.Status.Reporting == nil || updated.Status.Reporting.CheckRunID == 0 {
		t.Fatalf("expected non-zero CheckRunID on seed run status, got %+v", updated.Status.Reporting)
	}
}
