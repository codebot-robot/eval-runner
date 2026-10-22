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
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/gke-labs/eval-runner/pkg/forge/consumer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WorkItem represents a pre-validated event enqueued for asynchronous background processing.
type WorkItem struct {
	Event       string               // EventPullRequest or EventPush
	PullRequest *PullRequestWorkItem // populated if Event == EventPullRequest
	Push        *PushWorkItem        // populated if Event == EventPush
}

// WorkerPool manages bounded background workers that execute Kubernetes API operations.
type WorkerPool struct {
	cfg      Config
	consumer *consumer.Consumer
	queue    chan WorkItem
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
	running  bool
	mu       sync.Mutex
}

// NewWorkerPool constructs a WorkerPool with the configured queue size and workers.
func NewWorkerPool(cfg Config) *WorkerPool {
	qSize := cfg.QueueSize
	if qSize <= 0 {
		qSize = DefaultQueueSize
	}
	return &WorkerPool{
		cfg:   cfg,
		queue: make(chan WorkItem, qSize),
	}
}

func (wp *WorkerPool) getConsumer() *consumer.Consumer {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	if wp.consumer != nil {
		return wp.consumer
	}
	if wp.cfg.Consumer != nil {
		wp.consumer = wp.cfg.Consumer
		return wp.consumer
	}
	var tracked []consumer.RepoConfig
	for _, repo := range wp.cfg.WebhookRepositories {
		ref, ok := parseRepoRef(repo)
		if ok {
			tracked = append(tracked, consumer.RepoConfig{
				Name:     ref.String(),
				Ref:      ref,
				Branches: []string{"main"},
			})
		}
	}
	wp.consumer = consumer.New(wp.cfg.Client, consumer.Config{
		Namespace:                wp.cfg.Namespace,
		DefaultModel:             wp.cfg.DefaultModel,
		DefaultCohort:            wp.cfg.DefaultCohort,
		DefaultBatchSize:         wp.cfg.DefaultBatchSize,
		DefaultMaxSamplesPerTask: wp.cfg.DefaultMaxSamplesPerTask,
		DefaultBudgetUSD:         wp.cfg.DefaultBudgetUSD,
		TrackedRepos:             tracked,
		IsMultiRepo:              wp.cfg.IsMultiRepo(),
		Store:                    nil,
	})
	return wp.consumer
}

// Start launches worker goroutines with their own background context.
func (wp *WorkerPool) Start() {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	if wp.running {
		return
	}
	wp.ctx, wp.cancel = context.WithCancel(context.Background())
	wp.running = true

	workerCount := wp.cfg.Workers
	if workerCount <= 0 {
		workerCount = DefaultWorkers
	}

	for i := 0; i < workerCount; i++ {
		wp.wg.Add(1)
		go wp.workerLoop(i)
	}
}

// Stop gracefully shuts down workers, draining the queue before cancelling context.
func (wp *WorkerPool) Stop() {
	wp.mu.Lock()
	if !wp.running {
		wp.mu.Unlock()
		return
	}
	close(wp.queue)
	wp.running = false
	wp.mu.Unlock()

	wp.wg.Wait()
	if wp.cancel != nil {
		wp.cancel()
	}
}

// Enqueue attempts to add a work item to the bounded worker queue without blocking.
// Returns false if the queue is full.
func (wp *WorkerPool) Enqueue(item WorkItem) bool {
	select {
	case wp.queue <- item:
		return true
	default:
		return false
	}
}

func (wp *WorkerPool) workerLoop(id int) {
	defer wp.wg.Done()
	for item := range wp.queue {
		if err := wp.ProcessItem(wp.ctx, item); err != nil {
			log.Printf("[worker %d] error processing item: %v", id, err)
		}
	}
}

// ProcessItem executes the corresponding Kubernetes mutations for a WorkItem.
func (wp *WorkerPool) ProcessItem(ctx context.Context, item WorkItem) error {
	switch item.Event {
	case EventPullRequest:
		if item.PullRequest != nil {
			if item.PullRequest.Action == ActionClosed {
				return wp.ProcessPullRequestClosed(ctx, item.PullRequest)
			}
			return wp.ProcessPullRequest(ctx, item.PullRequest)
		}
	case EventPush:
		if item.Push != nil {
			return wp.ProcessPush(ctx, item.Push)
		}
	default:
		log.Printf("worker: unsupported work item event %q", item.Event)
	}
	return nil
}

// ProcessPullRequestClosed cleans up Comparisons and seed Runs when a pull request closes.
func (wp *WorkerPool) ProcessPullRequestClosed(ctx context.Context, item *PullRequestWorkItem) error {
	if item == nil || wp.cfg.Client == nil {
		return nil
	}
	ref, _ := parseRepoRef(item.TargetRepo)
	var candRef forge.RepoRef
	if item.CandidateRepo != "" {
		candRef, _ = parseRepoRef(item.CandidateRepo)
	}
	ev := consumer.ChangeClosedEvent{
		Repo: ref,
		Change: forge.ChangeRequest{
			Number: item.PRNumber,
			State:  "closed",
			Head: forge.ChangeRequestHead{
				RepoRef: candRef,
			},
		},
	}
	return wp.getConsumer().ConsumeEvent(ctx, ev)
}

// ProcessPullRequest reconciles candidate Run, baseline Run, and Comparison for a PR.
func (wp *WorkerPool) ProcessPullRequest(ctx context.Context, item *PullRequestWorkItem) error {
	if item == nil || wp.cfg.Client == nil {
		return nil
	}
	ref, _ := parseRepoRef(item.TargetRepo)
	var candRef forge.RepoRef
	if item.CandidateRepo != "" {
		candRef, _ = parseRepoRef(item.CandidateRepo)
	}
	ev := consumer.ChangeOpenedEvent{
		Repo: ref,
		Change: forge.ChangeRequest{
			Number: item.PRNumber,
			Title:  fmt.Sprintf("PR #%d", item.PRNumber),
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref:     item.CandidateRepo,
				SHA:     item.HeadSHA,
				RepoRef: candRef,
			},
			Base: forge.ChangeRequestBase{
				Ref: item.BaselineRepo,
				SHA: item.BaseSHA,
			},
		},
		CandidateCloneURL: item.CandidateCloneURL,
		BaselineCloneURL:  item.BaselineCloneURL,
	}
	return wp.getConsumer().ConsumeEvent(ctx, ev)
}

// ProcessPush reconciles a warm baseline Run on push events to default branches.
func (wp *WorkerPool) ProcessPush(ctx context.Context, item *PushWorkItem) error {
	if item == nil || wp.cfg.Client == nil {
		return nil
	}
	ref, _ := parseRepoRef(item.Repo)
	var t time.Time
	if item.CommitTime != "" {
		if parsed, err := time.Parse(time.RFC3339, item.CommitTime); err == nil {
			t = parsed
		} else {
			log.Printf("warning: invalid commit time %q in push item: %v", item.CommitTime, err)
		}
	}
	ev := consumer.BranchAdvancedEvent{
		Repo:       ref,
		Branch:     item.Branch,
		NewSHA:     item.CommitSHA,
		CommitTime: item.CommitTime,
		Commit: &forge.Commit{
			SHA:   item.CommitSHA,
			Time:  t,
			Title: fmt.Sprintf("Commit %s", ShortSHA(item.CommitSHA)),
		},
		CloneURL: item.CloneURL,
	}
	return wp.getConsumer().ConsumeEvent(ctx, ev)
}

// FindCompletedBaselineRun looks up an existing completed baseline Run in the cluster.
func (wp *WorkerPool) FindCompletedBaselineRun(ctx context.Context, namespace, commit, expectedURL, model string, repo ...string) (string, error) {
	repoStr := ""
	if len(repo) > 0 {
		repoStr = repo[0]
	}
	return wp.getConsumer().FindCompletedBaselineRun(ctx, namespace, commit, expectedURL, model, repoStr, wp.cfg.IsMultiRepo())
}

// FindComparisonsForPR returns all Comparisons matching the given PR number and repository.
func FindComparisonsForPR(ctx context.Context, cl client.Reader, namespace, repo string, prNum int) ([]v1alpha1.Comparison, error) {
	return consumer.FindComparisonsForPR(ctx, cl, namespace, repo, prNum)
}

// FindComparisonForPR returns the primary Comparison matching the given PR number and repository.
func FindComparisonForPR(ctx context.Context, cl client.Reader, namespace, repo string, prNum int) (*v1alpha1.Comparison, error) {
	return consumer.FindComparisonForPR(ctx, cl, namespace, repo, prNum)
}

func parseRepoRef(repoStr string) (forge.RepoRef, bool) {
	repoStr = strings.TrimSpace(repoStr)
	if strings.Contains(repoStr, "://") || strings.Contains(repoStr, "@") {
		return forge.ParseRepoURL(repoStr)
	}
	parts := strings.Split(repoStr, "/")
	if len(parts) == 3 && forge.IsValidRepoHost(parts[0]) {
		ref := forge.RepoRef{
			Host:  strings.ToLower(parts[0]),
			Owner: parts[1],
			Name:  parts[2],
		}
		if ref.IsValid() {
			return ref, true
		}
	}
	if len(parts) == 2 {
		return forge.RepoRefFromOwnerName(repoStr)
	}
	return forge.RepoRef{}, false
}
