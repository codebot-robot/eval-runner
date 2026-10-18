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
	"strconv"
	"strings"
	"sync"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WorkItem represents an event enqueued for asynchronous background processing.
type WorkItem struct {
	Event            string                  // EventPullRequest or EventPush
	PullRequestEvent *GitHubPullRequestEvent // populated if Event == EventPullRequest
	PushEvent        *GitHubPushEvent        // populated if Event == EventPush
}

// WorkerPool manages bounded background workers that execute Kubernetes API operations.
type WorkerPool struct {
	cfg     Config
	queue   chan WorkItem
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
	running bool
	mu      sync.Mutex
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

// Start launches worker goroutines.
func (wp *WorkerPool) Start(ctx context.Context) {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	if wp.running {
		return
	}
	wp.ctx, wp.cancel = context.WithCancel(ctx)
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

// Stop gracefully shuts down workers, waiting for the queue to drain.
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
		if item.PullRequestEvent != nil {
			return wp.ProcessPullRequest(ctx, item.PullRequestEvent)
		}
	case EventPush:
		if item.PushEvent != nil {
			return wp.ProcessPush(ctx, item.PushEvent)
		}
	default:
		log.Printf("worker: unsupported work item event %q", item.Event)
	}
	return nil
}

// ProcessPullRequest reconciles candidate Run, baseline Run (with cache reuse), and Comparison for a PR.
func (wp *WorkerPool) ProcessPullRequest(ctx context.Context, event *GitHubPullRequestEvent) error {
	if event == nil || wp.cfg.Client == nil {
		return nil
	}

	prNum := event.Number
	if prNum == 0 && event.PullRequest.Number != 0 {
		prNum = event.PullRequest.Number
	}
	if prNum <= 0 {
		return fmt.Errorf("invalid pull request number %d", prNum)
	}

	headSHA := strings.TrimSpace(event.PullRequest.Head.SHA)
	baseSHA := strings.TrimSpace(event.PullRequest.Base.SHA)
	if headSHA == "" || baseSHA == "" {
		return fmt.Errorf("missing head (%s) or base (%s) commit SHA in pull request #%d", headSHA, baseSHA, prNum)
	}

	repoFullName := strings.TrimSpace(event.Repository.FullName)
	if repoFullName == "" {
		repoFullName = fmt.Sprintf("%s/%s", event.Repository.Owner.Login, event.Repository.Name)
	}
	cloneURL := fmt.Sprintf("https://github.com/%s.git", repoFullName)

	headShort := ShortSHA(headSHA)
	baseShort := ShortSHA(baseSHA)
	candRunName := fmt.Sprintf("pr-%d-%s", prNum, headShort)
	defaultBaseRunName := fmt.Sprintf("base-%s", baseShort)
	compName := fmt.Sprintf("pr-%d", prNum)

	ns := wp.cfg.Namespace
	sanitizedRepo := SanitizeLabelValue(repoFullName)

	// 1. Create or update Candidate Run: pr-<number>-<headSHA7>
	candRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      candRunName,
			Namespace: ns,
			Labels: map[string]string{
				LabelPR:      strconv.Itoa(prNum),
				LabelRepo:    sanitizedRepo,
				LabelHeadSHA: headSHA,
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    cloneURL,
				Commit: headSHA,
			},
			Model: wp.cfg.DefaultModel,
			Limit: wp.cfg.DefaultLimit,
		},
	}
	if err := wp.createOrUpdateRun(ctx, candRun); err != nil {
		return fmt.Errorf("failed to create/update candidate run %s/%s: %w", ns, candRunName, err)
	}

	// 2. Reconcile Baseline Run with Baseline Cache (§4.1):
	// Reuse if a Completed Run for that commit and model already exists.
	baselineRunName, err := wp.findCompletedBaselineRun(ctx, ns, baseSHA, wp.cfg.DefaultModel)
	if err != nil {
		log.Printf("warning: error checking baseline cache for commit %s: %v", baseSHA, err)
	}

	if baselineRunName != "" {
		log.Printf("reusing completed baseline run %s/%s for base commit %s (model %s)", ns, baselineRunName, baseSHA, wp.cfg.DefaultModel)
	} else {
		// Baseline not in cache: ensure base-<baseSHA7> exists or create it
		baselineRunName = defaultBaseRunName
		baseRun := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name:      baselineRunName,
				Namespace: ns,
				Labels: map[string]string{
					LabelRepo:    sanitizedRepo,
					LabelHeadSHA: baseSHA,
				},
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					URL:    cloneURL,
					Commit: baseSHA,
				},
				Model: wp.cfg.DefaultModel,
				Limit: wp.cfg.DefaultLimit,
			},
		}
		if err := wp.createOrUpdateRun(ctx, baseRun); err != nil {
			return fmt.Errorf("failed to create/update baseline run %s/%s: %w", ns, baselineRunName, err)
		}
	}

	// 3. Create or update Comparison pr-<number>
	comp := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:      compName,
			Namespace: ns,
			Labels: map[string]string{
				LabelPR:      strconv.Itoa(prNum),
				LabelRepo:    sanitizedRepo,
				LabelHeadSHA: headSHA,
			},
		},
		Spec: v1alpha1.ComparisonSpec{
			Baseline: v1alpha1.ComparisonRunRef{
				Run: baselineRunName,
			},
			Candidate: v1alpha1.ComparisonRunRef{
				Run: candRunName,
			},
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: repoFullName,
				Number:     prNum,
				HeadSHA:    headSHA,
				BaseSHA:    baseSHA,
			},
		},
	}
	if err := wp.createOrUpdateComparison(ctx, comp); err != nil {
		return fmt.Errorf("failed to create/update comparison %s/%s: %w", ns, compName, err)
	}

	log.Printf("successfully reconciled PR #%d: candidate=%s, baseline=%s, comparison=%s", prNum, candRunName, baselineRunName, compName)
	return nil
}

// ProcessPush reconciles base-<sha7> on pushes to the default branch to warm the baseline cache.
func (wp *WorkerPool) ProcessPush(ctx context.Context, event *GitHubPushEvent) error {
	if event == nil || wp.cfg.Client == nil {
		return nil
	}

	commitSHA := strings.TrimSpace(event.After)
	if commitSHA == "" && event.HeadCommit != nil {
		commitSHA = strings.TrimSpace(event.HeadCommit.ID)
	}
	if commitSHA == "" || commitSHA == "0000000000000000000000000000000000000000" {
		log.Printf("push event with empty/null commit SHA, ignoring")
		return nil
	}

	repoFullName := strings.TrimSpace(event.Repository.FullName)
	if repoFullName == "" {
		repoFullName = fmt.Sprintf("%s/%s", event.Repository.Owner.Login, event.Repository.Name)
	}
	cloneURL := fmt.Sprintf("https://github.com/%s.git", repoFullName)

	ns := wp.cfg.Namespace
	baseRunName := fmt.Sprintf("base-%s", ShortSHA(commitSHA))
	sanitizedRepo := SanitizeLabelValue(repoFullName)

	baseRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      baseRunName,
			Namespace: ns,
			Labels: map[string]string{
				LabelRepo:    sanitizedRepo,
				LabelHeadSHA: commitSHA,
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    cloneURL,
				Commit: commitSHA,
			},
			Model: wp.cfg.DefaultModel,
			Limit: wp.cfg.DefaultLimit,
		},
	}

	if err := wp.createOrUpdateRun(ctx, baseRun); err != nil {
		return fmt.Errorf("failed to create/update warm baseline run %s/%s: %w", ns, baseRunName, err)
	}

	log.Printf("successfully reconciled push event: warm baseline run %s/%s created/updated", ns, baseRunName)
	return nil
}

// findCompletedBaselineRun checks if any Run in the namespace targeting commit and model has completed.
func (wp *WorkerPool) findCompletedBaselineRun(ctx context.Context, namespace, commit, model string) (string, error) {
	// First check direct candidate base-<sha7>
	directName := fmt.Sprintf("base-%s", ShortSHA(commit))
	var directRun v1alpha1.Run
	if err := wp.cfg.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: directName}, &directRun); err != nil {
		if !apierrors.IsNotFound(err) {
			return "", err
		}
	} else if directRun.Status.Phase == v1alpha1.RunPhaseCompleted && directRun.Spec.Model == model {
		return directRun.Name, nil
	}

	// Next search all runs in the namespace
	var runList v1alpha1.RunList
	if err := wp.cfg.Client.List(ctx, &runList, client.InNamespace(namespace)); err != nil {
		return "", err
	}

	for _, r := range runList.Items {
		if r.Status.Phase != v1alpha1.RunPhaseCompleted {
			continue
		}
		if r.Spec.Model != model {
			continue
		}
		// Match exact commit or prefix match
		c := strings.ToLower(strings.TrimSpace(r.Spec.Repo.Commit))
		target := strings.ToLower(strings.TrimSpace(commit))
		if c == target || strings.HasPrefix(c, target) || strings.HasPrefix(target, c) {
			return r.Name, nil
		}
	}
	return "", nil
}

func (wp *WorkerPool) createOrUpdateRun(ctx context.Context, run *v1alpha1.Run) error {
	var existing v1alpha1.Run
	key := types.NamespacedName{
		Namespace: run.Namespace,
		Name:      run.Name,
	}

	if err := wp.cfg.Client.Get(ctx, key, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			return wp.cfg.Client.Create(ctx, run)
		}
		return err
	}

	// Run already exists. Update spec and labels idempotently.
	needsUpdate := false

	if existing.Spec.Repo.URL != run.Spec.Repo.URL ||
		existing.Spec.Repo.Commit != run.Spec.Repo.Commit ||
		existing.Spec.Model != run.Spec.Model {
		existing.Spec.Repo = run.Spec.Repo
		existing.Spec.Model = run.Spec.Model
		needsUpdate = true
	}

	if (existing.Spec.Limit == nil && run.Spec.Limit != nil) ||
		(existing.Spec.Limit != nil && run.Spec.Limit == nil) ||
		(existing.Spec.Limit != nil && run.Spec.Limit != nil && *existing.Spec.Limit != *run.Spec.Limit) {
		existing.Spec.Limit = run.Spec.Limit
		needsUpdate = true
	}

	if existing.Labels == nil {
		existing.Labels = make(map[string]string)
	}
	for k, v := range run.Labels {
		if existing.Labels[k] != v {
			existing.Labels[k] = v
			needsUpdate = true
		}
	}

	if needsUpdate {
		return wp.cfg.Client.Update(ctx, &existing)
	}
	return nil
}

func (wp *WorkerPool) createOrUpdateComparison(ctx context.Context, comp *v1alpha1.Comparison) error {
	var existing v1alpha1.Comparison
	key := types.NamespacedName{
		Namespace: comp.Namespace,
		Name:      comp.Name,
	}

	if err := wp.cfg.Client.Get(ctx, key, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			return wp.cfg.Client.Create(ctx, comp)
		}
		return err
	}

	// Comparison exists. Update baseline/candidate refs, pullRequest spec, and labels (for synchronize re-pointing).
	needsUpdate := false

	if existing.Spec.Candidate.Run != comp.Spec.Candidate.Run {
		existing.Spec.Candidate.Run = comp.Spec.Candidate.Run
		needsUpdate = true
	}
	if existing.Spec.Baseline.Run != comp.Spec.Baseline.Run {
		existing.Spec.Baseline.Run = comp.Spec.Baseline.Run
		needsUpdate = true
	}

	if comp.Spec.PullRequest != nil {
		if existing.Spec.PullRequest == nil ||
			existing.Spec.PullRequest.Repository != comp.Spec.PullRequest.Repository ||
			existing.Spec.PullRequest.Number != comp.Spec.PullRequest.Number ||
			existing.Spec.PullRequest.HeadSHA != comp.Spec.PullRequest.HeadSHA ||
			existing.Spec.PullRequest.BaseSHA != comp.Spec.PullRequest.BaseSHA {
			existing.Spec.PullRequest = comp.Spec.PullRequest.DeepCopy()
			needsUpdate = true
		}
	}

	if existing.Labels == nil {
		existing.Labels = make(map[string]string)
	}
	for k, v := range comp.Labels {
		if existing.Labels[k] != v {
			existing.Labels[k] = v
			needsUpdate = true
		}
	}

	if needsUpdate {
		return wp.cfg.Client.Update(ctx, &existing)
	}
	return nil
}
