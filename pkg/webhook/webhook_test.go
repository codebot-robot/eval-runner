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

package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = v1alpha1.AddToScheme(scheme)
	return scheme
}

func computeSignature(payload, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestSignatureVerification(t *testing.T) {
	secret := []byte("super-secret-key-12345")
	payload := []byte(`{"action":"opened","number":42}`)

	validSig := computeSignature(payload, secret)

	tests := []struct {
		name      string
		payload   []byte
		secret    []byte
		sigHeader string
		expected  bool
	}{
		{
			name:      "valid signature",
			payload:   payload,
			secret:    secret,
			sigHeader: validSig,
			expected:  true,
		},
		{
			name:      "tampered signature",
			payload:   payload,
			secret:    secret,
			sigHeader: validSig + "bad",
			expected:  false,
		},
		{
			name:      "tampered body",
			payload:   []byte(`{"action":"opened","number":43}`),
			secret:    secret,
			sigHeader: validSig,
			expected:  false,
		},
		{
			name:      "wrong secret",
			payload:   payload,
			secret:    []byte("different-secret"),
			sigHeader: validSig,
			expected:  false,
		},
		{
			name:      "missing signature header",
			payload:   payload,
			secret:    secret,
			sigHeader: "",
			expected:  false,
		},
		{
			name:      "empty secret",
			payload:   payload,
			secret:    nil,
			sigHeader: validSig,
			expected:  false,
		},
		{
			name:      "invalid header prefix (not sha256=)",
			payload:   payload,
			secret:    secret,
			sigHeader: "sha1=" + hex.EncodeToString(secret),
			expected:  false,
		},
		{
			name:      "malformed hex digest",
			payload:   payload,
			secret:    secret,
			sigHeader: "sha256=not-valid-hex-zzz",
			expected:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := VerifySignature(tc.payload, tc.secret, tc.sigHeader)
			if actual != tc.expected {
				t.Errorf("VerifySignature() = %v, expected %v", actual, tc.expected)
			}
		})
	}
}

func TestSanitizeLabelValue(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"gke-labs/eval-runner", "gke-labs_eval-runner"},
		{"eval-runner", "eval-runner"},
		{"--test..value--", "test..value"},
		{"org/sub/repo", "org_sub_repo"},
		{"", ""},
		{"a" + string(bytes.Repeat([]byte("x"), 100)) + "z", "a" + string(bytes.Repeat([]byte("x"), 61)) + "x"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			actual := SanitizeLabelValue(tc.input)
			if actual != tc.expected {
				t.Errorf("SanitizeLabelValue(%q) = %q, expected %q", tc.input, actual, tc.expected)
			}
		})
	}
}

func TestRepositoryAllowList(t *testing.T) {
	allowList := []string{"gke-labs/eval-runner", "org/repo"}

	if !IsRepositoryAllowed("gke-labs/eval-runner", allowList) {
		t.Errorf("expected gke-labs/eval-runner to be allowed")
	}
	if !IsRepositoryAllowed("GKE-LABS/EVAL-RUNNER", allowList) {
		t.Errorf("expected case-insensitive match to be allowed")
	}
	if !IsRepositoryAllowed("org/repo", allowList) {
		t.Errorf("expected org/repo to be allowed")
	}
	if IsRepositoryAllowed("untrusted/repo", allowList) {
		t.Errorf("expected untrusted/repo to be denied")
	}
	// Empty allow-list allows all
	if !IsRepositoryAllowed("anyone/anyrepo", nil) {
		t.Errorf("expected empty allow-list to allow all")
	}
	if !IsRepositoryAllowed("anyone/anyrepo", []string{}) {
		t.Errorf("expected empty slice allow-list to allow all")
	}
}

func TestConfigPrecedence(t *testing.T) {
	limit10 := 10
	limit20 := 20

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "eval-runner-config",
			Namespace: "eval-runner",
		},
		Data: map[string]string{
			ConfigMapKeyGitHubWebhookSecret: "custom-webhook-secret",
			ConfigMapKeyDefaultModel:        "google/gemini-flash",
			ConfigMapKeyDefaultLimit:        "10",
			ConfigMapKeyWebhookRepositories: "gke-labs/eval-runner,foo/bar",
		},
	}

	// 1. ConfigMap applied
	cfg, err := ResolveWebhookConfig(cm, RawWebhookFlags{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GitHubWebhookSecret != "custom-webhook-secret" {
		t.Errorf("expected GitHubWebhookSecret %q, got %q", "custom-webhook-secret", cfg.GitHubWebhookSecret)
	}
	if cfg.DefaultModel != "google/gemini-flash" {
		t.Errorf("expected DefaultModel %q, got %q", "google/gemini-flash", cfg.DefaultModel)
	}
	if cfg.DefaultLimit == nil || *cfg.DefaultLimit != 10 {
		t.Errorf("expected DefaultLimit 10, got %v", cfg.DefaultLimit)
	}
	if len(cfg.WebhookRepositories) != 2 || cfg.WebhookRepositories[0] != "gke-labs/eval-runner" {
		t.Errorf("expected WebhookRepositories %v", cfg.WebhookRepositories)
	}

	// 2. Explicit flag overrides ConfigMap
	flags := RawWebhookFlags{
		DefaultModel: "mockllm/model",
		DefaultLimit: &limit20,
		Explicit: map[string]bool{
			"default-model": true,
			"default-limit": true,
		},
	}
	cfgWithFlags, err := ResolveWebhookConfig(cm, flags, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfgWithFlags.DefaultModel != "mockllm/model" {
		t.Errorf("expected explicit flag to override DefaultModel, got %q", cfgWithFlags.DefaultModel)
	}
	if cfgWithFlags.DefaultLimit == nil || *cfgWithFlags.DefaultLimit != limit20 {
		t.Errorf("expected explicit flag to override DefaultLimit, got %v", cfgWithFlags.DefaultLimit)
	}

	// 3. Malformed limit in ConfigMap returns error
	badCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "eval-runner-config", Namespace: "eval-runner"},
		Data: map[string]string{
			ConfigMapKeyDefaultLimit: "not-a-number",
		},
	}
	_, err = ResolveWebhookConfig(badCM, RawWebhookFlags{}, nil)
	if err == nil {
		t.Errorf("expected error for malformed defaultLimit")
	}
	_ = limit10
}

func TestProcessPullRequest_CreateAndIdempotency(t *testing.T) {
	scheme := newTestScheme()
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	limit := 15
	cfg := Config{
		Namespace:    "eval-runner",
		DefaultModel: "mockllm/model",
		DefaultLimit: &limit,
		Client:       cl,
	}
	worker := NewWorkerPool(cfg)

	event := &GitHubPullRequestEvent{
		Action: ActionOpened,
		Number: 105,
		PullRequest: GitHubPullRequestData{
			Number: 105,
			Head: GitHubCommitRef{
				SHA: "1234567890abcdef1234567890abcdef12345678",
			},
			Base: GitHubCommitRef{
				SHA: "abcdef1234567890abcdef1234567890abcdef12",
			},
		},
		Repository: GitHubRepository{
			FullName: "gke-labs/eval-runner",
			Name:     "eval-runner",
		},
	}

	ctx := t.Context()

	// 1. Process pull_request opened
	if err := worker.ProcessPullRequest(ctx, event); err != nil {
		t.Fatalf("ProcessPullRequest failed: %v", err)
	}

	// Verify Candidate Run
	candRunName := "pr-105-1234567"
	var candRun v1alpha1.Run
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "eval-runner", Name: candRunName}, &candRun); err != nil {
		t.Fatalf("candidate Run %s not found: %v", candRunName, err)
	}
	if candRun.Spec.Repo.Commit != event.PullRequest.Head.SHA {
		t.Errorf("expected commit %s, got %s", event.PullRequest.Head.SHA, candRun.Spec.Repo.Commit)
	}
	if candRun.Spec.Model != "mockllm/model" {
		t.Errorf("expected model mockllm/model, got %s", candRun.Spec.Model)
	}
	if candRun.Spec.Limit == nil || *candRun.Spec.Limit != 15 {
		t.Errorf("expected limit 15, got %v", candRun.Spec.Limit)
	}
	if candRun.Labels[LabelPR] != "105" {
		t.Errorf("expected label pr=105, got %s", candRun.Labels[LabelPR])
	}
	if candRun.Labels[LabelRepo] != "gke-labs_eval-runner" {
		t.Errorf("expected sanitized repo label, got %s", candRun.Labels[LabelRepo])
	}
	if candRun.Labels[LabelHeadSHA] != event.PullRequest.Head.SHA {
		t.Errorf("expected head-sha label, got %s", candRun.Labels[LabelHeadSHA])
	}

	// Verify Baseline Run
	baseRunName := "base-abcdef1"
	var baseRun v1alpha1.Run
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "eval-runner", Name: baseRunName}, &baseRun); err != nil {
		t.Fatalf("baseline Run %s not found: %v", baseRunName, err)
	}
	if baseRun.Spec.Repo.Commit != event.PullRequest.Base.SHA {
		t.Errorf("expected commit %s, got %s", event.PullRequest.Base.SHA, baseRun.Spec.Repo.Commit)
	}

	// Verify Comparison
	compName := "pr-105"
	var comp v1alpha1.Comparison
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "eval-runner", Name: compName}, &comp); err != nil {
		t.Fatalf("Comparison %s not found: %v", compName, err)
	}
	if comp.Spec.Candidate.Run != candRunName {
		t.Errorf("expected Candidate.Run %s, got %s", candRunName, comp.Spec.Candidate.Run)
	}
	if comp.Spec.Baseline.Run != baseRunName {
		t.Errorf("expected Baseline.Run %s, got %s", baseRunName, comp.Spec.Baseline.Run)
	}
	if comp.Spec.PullRequest == nil || comp.Spec.PullRequest.Number != 105 {
		t.Errorf("expected PullRequest.Number 105, got %v", comp.Spec.PullRequest)
	}
	if comp.Spec.PullRequest.HeadSHA != event.PullRequest.Head.SHA {
		t.Errorf("expected HeadSHA %s, got %s", event.PullRequest.Head.SHA, comp.Spec.PullRequest.HeadSHA)
	}
	if comp.Labels[LabelPR] != "105" {
		t.Errorf("expected label pr=105, got %s", comp.Labels[LabelPR])
	}

	// 2. Redelivery of the exact same event: must be idempotent and succeed
	if err := worker.ProcessPullRequest(ctx, event); err != nil {
		t.Fatalf("Redelivery ProcessPullRequest failed: %v", err)
	}

	var runList v1alpha1.RunList
	if err := cl.List(ctx, &runList, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list runs: %v", err)
	}
	if len(runList.Items) != 2 {
		t.Errorf("expected exactly 2 Runs after redelivery, got %d", len(runList.Items))
	}
}

func TestProcessPullRequest_Synchronize(t *testing.T) {
	scheme := newTestScheme()
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	cfg := Config{
		Namespace:    "eval-runner",
		DefaultModel: "mockllm/model",
		Client:       cl,
	}
	worker := NewWorkerPool(cfg)
	ctx := t.Context()

	// Initial opened event
	initialEvent := &GitHubPullRequestEvent{
		Action: ActionOpened,
		Number: 105,
		PullRequest: GitHubPullRequestData{
			Number: 105,
			Head:   GitHubCommitRef{SHA: "1111111111111111111111111111111111111111"},
			Base:   GitHubCommitRef{SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
		Repository: GitHubRepository{FullName: "gke-labs/eval-runner"},
	}
	if err := worker.ProcessPullRequest(ctx, initialEvent); err != nil {
		t.Fatalf("initial event failed: %v", err)
	}

	// New synchronize event with new head commit
	syncEvent := &GitHubPullRequestEvent{
		Action: ActionSynchronize,
		Number: 105,
		PullRequest: GitHubPullRequestData{
			Number: 105,
			Head:   GitHubCommitRef{SHA: "2222222222222222222222222222222222222222"},
			Base:   GitHubCommitRef{SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
		Repository: GitHubRepository{FullName: "gke-labs/eval-runner"},
	}
	if err := worker.ProcessPullRequest(ctx, syncEvent); err != nil {
		t.Fatalf("synchronize event failed: %v", err)
	}

	// Verify new candidate Run created
	var newCandRun v1alpha1.Run
	newCandName := "pr-105-2222222"
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "eval-runner", Name: newCandName}, &newCandRun); err != nil {
		t.Fatalf("new candidate Run %s not found: %v", newCandName, err)
	}

	// Verify Comparison updated to point to new candidate Run
	var comp v1alpha1.Comparison
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "eval-runner", Name: "pr-105"}, &comp); err != nil {
		t.Fatalf("Comparison pr-105 not found: %v", err)
	}
	if comp.Spec.Candidate.Run != newCandName {
		t.Errorf("expected Comparison candidate to point to %s, got %s", newCandName, comp.Spec.Candidate.Run)
	}
	if comp.Spec.PullRequest.HeadSHA != "2222222222222222222222222222222222222222" {
		t.Errorf("expected Comparison HeadSHA to update to %s, got %s", "2222222222222222222222222222222222222222", comp.Spec.PullRequest.HeadSHA)
	}
	if comp.Labels[LabelHeadSHA] != "2222222222222222222222222222222222222222" {
		t.Errorf("expected Comparison label %s to update", LabelHeadSHA)
	}
}

func TestProcessPullRequest_BaselineCacheReuse(t *testing.T) {
	scheme := newTestScheme()

	baseSHA := "basecommit1234567890abcdef1234567890abcdef"
	// Pre-create an already Completed Run for basecommit
	preExistingCompletedRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "base-basecom",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: baseSHA,
			},
			Model: "mockllm/model",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(preExistingCompletedRun).Build()

	cfg := Config{
		Namespace:    "eval-runner",
		DefaultModel: "mockllm/model",
		Client:       cl,
	}
	worker := NewWorkerPool(cfg)
	ctx := t.Context()

	event := &GitHubPullRequestEvent{
		Action: ActionOpened,
		Number: 200,
		PullRequest: GitHubPullRequestData{
			Number: 200,
			Head:   GitHubCommitRef{SHA: "headcommit1234567890abcdef1234567890abcdef"},
			Base:   GitHubCommitRef{SHA: baseSHA},
		},
		Repository: GitHubRepository{FullName: "gke-labs/eval-runner"},
	}

	if err := worker.ProcessPullRequest(ctx, event); err != nil {
		t.Fatalf("ProcessPullRequest failed: %v", err)
	}

	// Verify Comparison points to preExistingCompletedRun
	var comp v1alpha1.Comparison
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "eval-runner", Name: "pr-200"}, &comp); err != nil {
		t.Fatalf("Comparison pr-200 not found: %v", err)
	}
	if comp.Spec.Baseline.Run != "base-basecom" {
		t.Errorf("expected Baseline.Run to reuse 'base-basecom', got %s", comp.Spec.Baseline.Run)
	}
}

func TestProcessPush_DefaultBranch(t *testing.T) {
	scheme := newTestScheme()
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	cfg := Config{
		Namespace:    "eval-runner",
		DefaultModel: "google/gemini-flash",
		Client:       cl,
	}
	worker := NewWorkerPool(cfg)
	ctx := t.Context()

	pushEvent := &GitHubPushEvent{
		Ref:   "refs/heads/main",
		After: "9876543210abcdef9876543210abcdef98765432",
		Repository: GitHubRepository{
			FullName:      "gke-labs/eval-runner",
			DefaultBranch: "main",
		},
	}

	if err := worker.ProcessPush(ctx, pushEvent); err != nil {
		t.Fatalf("ProcessPush failed: %v", err)
	}

	var baseRun v1alpha1.Run
	baseRunName := "base-9876543"
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "eval-runner", Name: baseRunName}, &baseRun); err != nil {
		t.Fatalf("base Run %s not found: %v", baseRunName, err)
	}
	if baseRun.Spec.Repo.Commit != pushEvent.After {
		t.Errorf("expected commit %s, got %s", pushEvent.After, baseRun.Spec.Repo.Commit)
	}
	if baseRun.Spec.Model != "google/gemini-flash" {
		t.Errorf("expected model google/gemini-flash, got %s", baseRun.Spec.Model)
	}

	// Redelivery should be idempotent
	if err := worker.ProcessPush(ctx, pushEvent); err != nil {
		t.Fatalf("ProcessPush redelivery failed: %v", err)
	}
}

func TestHandler_HTTPEndpointsAndSignature(t *testing.T) {
	secretKey := []byte("my-test-secret-key")
	scheme := newTestScheme()

	secretObj := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-webhook",
			Namespace: "eval-runner",
		},
		Data: map[string][]byte{
			"secret": secretKey,
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secretObj).Build()

	cfg := Config{
		Namespace:           "eval-runner",
		GitHubWebhookSecret: "github-webhook",
		DefaultModel:        "mockllm/model",
		Client:              cl,
	}
	server := NewServer(cfg)
	server.Start(t.Context())
	defer server.Stop()

	// 1. Healthz probe
	reqHealth := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rrHealth := httptest.NewRecorder()
	server.ServeHTTP(rrHealth, reqHealth)
	if rrHealth.Code != http.StatusOK {
		t.Errorf("expected healthz 200, got %d", rrHealth.Code)
	}

	// 2. Readyz probe
	reqReady := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rrReady := httptest.NewRecorder()
	server.ServeHTTP(rrReady, reqReady)
	if rrReady.Code != http.StatusOK {
		t.Errorf("expected readyz 200, got %d", rrReady.Code)
	}

	// 3. Unsigned request -> 401
	body := []byte(`{"action":"opened","number":101}`)
	reqUnsigned := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	reqUnsigned.Header.Set(HeaderGitHubEvent, EventPullRequest)
	rrUnsigned := httptest.NewRecorder()
	server.ServeHTTP(rrUnsigned, reqUnsigned)
	if rrUnsigned.Code != http.StatusUnauthorized {
		t.Errorf("expected unsigned request 401, got %d", rrUnsigned.Code)
	}

	// 4. Invalid signature -> 401
	reqBadSig := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	reqBadSig.Header.Set(HeaderGitHubEvent, EventPullRequest)
	reqBadSig.Header.Set(HeaderHubSignature256, "sha256=1111222233334444")
	rrBadSig := httptest.NewRecorder()
	server.ServeHTTP(rrBadSig, reqBadSig)
	if rrBadSig.Code != http.StatusUnauthorized {
		t.Errorf("expected bad signature 401, got %d", rrBadSig.Code)
	}

	// 5. Valid signature with recorded pull_request event payload
	recordedPRPayload := []byte(`{
		"action": "opened",
		"number": 305,
		"pull_request": {
			"number": 305,
			"head": {
				"sha": "aaaaaaa111111111111111111111111111111111"
			},
			"base": {
				"sha": "bbbbbbb222222222222222222222222222222222"
			}
		},
		"repository": {
			"full_name": "gke-labs/eval-runner",
			"name": "eval-runner",
			"default_branch": "main"
		}
	}`)
	validSig := computeSignature(recordedPRPayload, secretKey)

	reqValid := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(recordedPRPayload))
	reqValid.Header.Set(HeaderGitHubEvent, EventPullRequest)
	reqValid.Header.Set(HeaderHubSignature256, validSig)
	rrValid := httptest.NewRecorder()

	server.ServeHTTP(rrValid, reqValid)
	if rrValid.Code != http.StatusOK {
		t.Fatalf("expected valid signed PR request 200, got %d: %s", rrValid.Code, rrValid.Body.String())
	}

	// Wait briefly for worker queue to process the item
	time.Sleep(100 * time.Millisecond)

	var candRun v1alpha1.Run
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner", Name: "pr-305-aaaaaaa"}, &candRun); err != nil {
		t.Fatalf("expected candidate Run pr-305-aaaaaaa to be created: %v", err)
	}
	var baseRun v1alpha1.Run
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner", Name: "base-bbbbbbb"}, &baseRun); err != nil {
		t.Fatalf("expected baseline Run base-bbbbbbb to be created: %v", err)
	}
	var comp v1alpha1.Comparison
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "eval-runner", Name: "pr-305"}, &comp); err != nil {
		t.Fatalf("expected Comparison pr-305 to be created: %v", err)
	}
}

func TestHandler_AllowListIgnored(t *testing.T) {
	secretKey := []byte("secret")
	scheme := newTestScheme()
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	cfg := Config{
		Namespace:           "eval-runner",
		StaticWebhookSecret: secretKey,
		WebhookRepositories: []string{"gke-labs/eval-runner"},
		Client:              cl,
	}
	server := NewServer(cfg)
	server.Start(t.Context())
	defer server.Stop()

	// PR for a repo not in the allow-list
	payload := []byte(`{
		"action": "opened",
		"number": 1,
		"pull_request": {
			"number": 1,
			"head": {"sha": "1111111111111111111111111111111111111111"},
			"base": {"sha": "2222222222222222222222222222222222222222"}
		},
		"repository": {
			"full_name": "other-org/other-repo"
		}
	}`)
	sig := computeSignature(payload, secretKey)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set(HeaderGitHubEvent, EventPullRequest)
	req.Header.Set(HeaderHubSignature256, sig)
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for ignored repository, got %d", rr.Code)
	}

	time.Sleep(50 * time.Millisecond)

	var runList v1alpha1.RunList
	_ = cl.List(t.Context(), &runList, client.InNamespace("eval-runner"))
	if len(runList.Items) != 0 {
		t.Errorf("expected 0 runs created for disallowed repo, got %d", len(runList.Items))
	}
}
