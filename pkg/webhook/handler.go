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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// MaxPayloadBytes limits the maximum accepted request body size (10 MiB).
	MaxPayloadBytes = 10 * 1024 * 1024
)

// Server implements http.Handler for receiving GitHub webhooks.
type Server struct {
	cfg    Config
	mux    *http.ServeMux
	worker *WorkerPool

	mu              sync.RWMutex
	cachedSecret    []byte
	lastSecretFetch time.Time
}

// NewServer creates a new webhook Server and initializes endpoints.
func NewServer(cfg Config) *Server {
	s := &Server{
		cfg:    cfg,
		mux:    http.NewServeMux(),
		worker: NewWorkerPool(cfg),
	}
	s.routes()
	return s
}

// Worker returns the underlying WorkerPool.
func (s *Server) Worker() *WorkerPool {
	return s.worker
}

// Start launches the worker pool.
func (s *Server) Start() {
	s.worker.Start()
}

// Stop stops the worker pool and waits for queued items to complete.
func (s *Server) Stop() {
	s.worker.Stop()
}

// ServeHTTP delegates to internal mux.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/readyz", s.handleReadyz)
	s.mux.HandleFunc("/", s.handleWebhook)
	s.mux.HandleFunc("/webhook", s.handleWebhook)
}

func writeResponse(w http.ResponseWriter, status int, body string) {
	w.WriteHeader(status)
	if _, err := w.Write([]byte(body)); err != nil {
		_ = err
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	writeResponse(w, http.StatusOK, "ok\n")
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	writeResponse(w, http.StatusOK, "ok\n")
}

// getWebhookSecret loads the webhook secret either from static config or from Secret in Kubernetes.
func (s *Server) getWebhookSecret(ctx context.Context) ([]byte, error) {
	if len(s.cfg.StaticWebhookSecret) > 0 {
		return s.cfg.StaticWebhookSecret, nil
	}

	s.mu.RLock()
	if len(s.cachedSecret) > 0 && time.Since(s.lastSecretFetch) < 30*time.Second {
		defer s.mu.RUnlock()
		return s.cachedSecret, nil
	}
	s.mu.RUnlock()

	if s.cfg.Client == nil {
		return nil, fmt.Errorf("kubernetes client not configured and no static secret provided")
	}

	secretName := s.cfg.GitHubWebhookSecret
	if secretName == "" {
		secretName = DefaultGitHubWebhookSecret
	}

	var secret corev1.Secret
	key := types.NamespacedName{
		Namespace: s.cfg.Namespace,
		Name:      secretName,
	}

	if err := s.cfg.Client.Get(ctx, key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("webhook secret %s/%s not found", s.cfg.Namespace, secretName)
		}
		return nil, fmt.Errorf("failed to get webhook secret %s/%s: %w", s.cfg.Namespace, secretName, err)
	}

	secBytes := secret.Data["secret"]
	if len(secBytes) == 0 {
		return nil, fmt.Errorf("key 'secret' not found or empty in Secret %s/%s", s.cfg.Namespace, secretName)
	}

	s.mu.Lock()
	s.cachedSecret = secBytes
	s.lastSecretFetch = time.Now()
	s.mu.Unlock()

	return secBytes, nil
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// Health probes and other non-root paths should not trigger webhook on root path
	if r.URL.Path != "/" && r.URL.Path != "/webhook" {
		http.NotFound(w, r)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. Read request body capped at MaxPayloadBytes
	r.Body = http.MaxBytesReader(w, r.Body, MaxPayloadBytes)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read payload: %v", err), http.StatusBadRequest)
		return
	}

	// 2. Load webhook secret
	secret, err := s.getWebhookSecret(r.Context())
	if err != nil {
		log.Printf("webhook secret error: %v", err)
		http.Error(w, "Unauthorized: webhook secret unavailable", http.StatusUnauthorized)
		return
	}

	// 3. Verify X-Hub-Signature-256 signature
	sigHeader := r.Header.Get(HeaderHubSignature256)
	if sigHeader == "" || !VerifySignature(payload, secret, sigHeader) {
		http.Error(w, "Unauthorized: invalid signature", http.StatusUnauthorized)
		return
	}

	eventType := r.Header.Get(HeaderGitHubEvent)
	deliveryID := r.Header.Get(HeaderGitHubDelivery)

	switch eventType {
	case EventPing:
		w.Header().Set("Content-Type", "application/json")
		writeResponse(w, http.StatusOK, `{"message":"pong"}`+"\n")
		return

	case EventPullRequest:
		var event GitHubPullRequestEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			http.Error(w, fmt.Sprintf("Malformed pull_request payload: %v", err), http.StatusBadRequest)
			return
		}

		prNum := event.Number
		if prNum == 0 && event.PullRequest.Number != 0 {
			prNum = event.PullRequest.Number
		}
		if prNum <= 0 {
			http.Error(w, fmt.Sprintf("Invalid pull request number: %d", prNum), http.StatusBadRequest)
			return
		}

		// Parse and validate target base repository (hosting the PR)
		targetOwner, targetName, err := ParseOwnerAndName(&event.Repository)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid repository in payload: %v", err), http.StatusBadRequest)
			return
		}
		targetRepoFullName := fmt.Sprintf("%s/%s", targetOwner, targetName)

		// Check allow-list against target repository hosting the PR
		if !IsRepositoryAllowed(targetRepoFullName, s.cfg.WebhookRepositories) {
			log.Printf("ignoring event for repository %s: not in allow-list", targetRepoFullName)
			writeResponse(w, http.StatusOK, "ignored: repository not in allow-list\n")
			return
		}

		// Filter actions: opened, synchronize, reopened
		action := event.Action
		if action != ActionOpened && action != ActionSynchronize && action != ActionReopened {
			log.Printf("ignoring pull_request action %q for %s #%d", action, targetRepoFullName, prNum)
			writeResponse(w, http.StatusOK, "ignored: action not actionable\n")
			return
		}

		// Validate head and base commit SHAs (Blocking 2)
		headSHA := strings.ToLower(strings.TrimSpace(event.PullRequest.Head.SHA))
		baseSHA := strings.ToLower(strings.TrimSpace(event.PullRequest.Base.SHA))
		if err := ValidateCommitSHA(headSHA); err != nil {
			http.Error(w, fmt.Sprintf("Invalid head commit SHA: %v", err), http.StatusBadRequest)
			return
		}
		if err := ValidateCommitSHA(baseSHA); err != nil {
			http.Error(w, fmt.Sprintf("Invalid base commit SHA: %v", err), http.StatusBadRequest)
			return
		}

		// Candidate repo from pull_request.head.repo (for forks) or fallback to event.Repository (Blocking 1)
		candRepoObj := event.PullRequest.Head.Repo
		if candRepoObj == nil {
			candRepoObj = &event.Repository
		}
		candOwner, candName, err := ParseOwnerAndName(candRepoObj)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid head repository in payload: %v", err), http.StatusBadRequest)
			return
		}

		// Baseline repo from pull_request.base.repo or fallback to event.Repository
		baseRepoObj := event.PullRequest.Base.Repo
		if baseRepoObj == nil {
			baseRepoObj = &event.Repository
		}
		baseOwner, baseName, err := ParseOwnerAndName(baseRepoObj)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid base repository in payload: %v", err), http.StatusBadRequest)
			return
		}

		candCloneURL := BuildCloneURL(candOwner, candName)
		baseCloneURL := BuildCloneURL(baseOwner, baseName)
		candRepoFullName := fmt.Sprintf("%s/%s", candOwner, candName)
		baseRepoFullName := fmt.Sprintf("%s/%s", baseOwner, baseName)

		item := WorkItem{
			Event: EventPullRequest,
			PullRequest: &PullRequestWorkItem{
				PRNumber:          prNum,
				HeadSHA:           headSHA,
				BaseSHA:           baseSHA,
				CandidateCloneURL: candCloneURL,
				CandidateRepo:     candRepoFullName,
				BaselineCloneURL:  baseCloneURL,
				BaselineRepo:      baseRepoFullName,
				TargetRepo:        targetRepoFullName,
			},
		}
		if !s.worker.Enqueue(item) {
			log.Printf("warning: worker queue full, dropping pull_request event delivery %s", deliveryID)
			http.Error(w, "worker queue full", http.StatusServiceUnavailable)
			return
		}

		writeResponse(w, http.StatusOK, "ok: enqueued pull_request\n")

	case EventPush:
		var event GitHubPushEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			http.Error(w, fmt.Sprintf("Malformed push payload: %v", err), http.StatusBadRequest)
			return
		}

		repoOwner, repoName, err := ParseOwnerAndName(&event.Repository)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid repository in payload: %v", err), http.StatusBadRequest)
			return
		}
		repoFullName := fmt.Sprintf("%s/%s", repoOwner, repoName)

		// Check allow-list
		if !IsRepositoryAllowed(repoFullName, s.cfg.WebhookRepositories) {
			log.Printf("ignoring event for repository %s: not in allow-list", repoFullName)
			writeResponse(w, http.StatusOK, "ignored: repository not in allow-list\n")
			return
		}

		// Filter: push must be to default branch
		defaultBranch := event.Repository.DefaultBranch
		if defaultBranch == "" {
			defaultBranch = "main"
		}
		expectedRef := fmt.Sprintf("refs/heads/%s", defaultBranch)
		if event.Ref != expectedRef && event.Ref != defaultBranch {
			log.Printf("ignoring push to non-default branch %q (default: %q)", event.Ref, expectedRef)
			writeResponse(w, http.StatusOK, "ignored: push to non-default branch\n")
			return
		}

		// Ignore deleted branches
		if event.Deleted || event.After == "0000000000000000000000000000000000000000" {
			writeResponse(w, http.StatusOK, "ignored: branch deleted\n")
			return
		}

		commitSHA := strings.ToLower(strings.TrimSpace(event.After))
		if commitSHA == "" && event.HeadCommit != nil {
			commitSHA = strings.ToLower(strings.TrimSpace(event.HeadCommit.ID))
		}
		if err := ValidateCommitSHA(commitSHA); err != nil {
			http.Error(w, fmt.Sprintf("Invalid commit SHA in push payload: %v", err), http.StatusBadRequest)
			return
		}

		cloneURL := BuildCloneURL(repoOwner, repoName)

		item := WorkItem{
			Event: EventPush,
			Push: &PushWorkItem{
				CommitSHA: commitSHA,
				CloneURL:  cloneURL,
				Repo:      repoFullName,
			},
		}
		if !s.worker.Enqueue(item) {
			log.Printf("warning: worker queue full, dropping push event delivery %s", deliveryID)
			http.Error(w, "worker queue full", http.StatusServiceUnavailable)
			return
		}

		writeResponse(w, http.StatusOK, "ok: enqueued push\n")

	default:
		log.Printf("ignoring unhandled event type %q", eventType)
		writeResponse(w, http.StatusOK, "ignored: event type not handled\n")
	}
}
