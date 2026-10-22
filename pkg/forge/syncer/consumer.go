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

package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/forge"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// LabelPR tracks the pull request number on created Runs and Comparisons.
	LabelPR = "evalrunner.gke-labs.dev/pr"

	// LabelRepo tracks the repository identifier on created Runs and Comparisons.
	LabelRepo = "evalrunner.gke-labs.dev/repo"

	// LabelHeadSHA tracks the git commit SHA on created Runs and Comparisons.
	LabelHeadSHA = "evalrunner.gke-labs.dev/head-sha"

	// LabelBranch tracks the branch name on created baseline Runs.
	LabelBranch = "evalrunner.gke-labs.dev/branch"

	// AnnotationCommitTime tracks the commit timestamp (RFC3339) on created baseline Runs.
	AnnotationCommitTime = "evalrunner.gke-labs.dev/commit-time"

	// DefaultNamespace is the fallback namespace for Runs and Comparisons.
	DefaultNamespace = "eval-runner"

	// DefaultBatchSize is the default initial sample batch size.
	DefaultBatchSize = 8

	// DefaultMaxSamplesPerTask is the default cap for adaptive task samples.
	DefaultMaxSamplesPerTask = 64
)

// RepoGetter retrieves stored repository metadata by reference.
type RepoGetter interface {
	GetRepository(repo forge.RepoRef) (*forge.Repository, bool)
}

// ConsumerConfig holds configuration options for the Consumer.
type ConsumerConfig struct {
	Namespace                string
	DefaultModel             string
	DefaultCohort            string
	DefaultBatchSize         int
	DefaultMaxSamplesPerTask int
	DefaultBudgetUSD         *float64
	TrackedRepos             []RepoConfig
	IsMultiRepo              bool
	Store                    RepoGetter
}

// Consumer processes typed forge events and turns them into Runs and Comparisons.
type Consumer struct {
	client client.Client
	store  RepoGetter
	cfg    ConsumerConfig
	mu     sync.Mutex
}

// NewConsumer constructs a Consumer with the given Kubernetes client and configuration.
func NewConsumer(cl client.Client, cfg ConsumerConfig) *Consumer {
	if cfg.Namespace == "" {
		cfg.Namespace = DefaultNamespace
	}
	if cfg.DefaultBatchSize <= 0 {
		cfg.DefaultBatchSize = DefaultBatchSize
	}
	if cfg.DefaultMaxSamplesPerTask <= 0 {
		cfg.DefaultMaxSamplesPerTask = DefaultMaxSamplesPerTask
	}
	return &Consumer{
		client: cl,
		store:  cfg.Store,
		cfg:    cfg,
	}
}

// UpdateConfig updates the consumer's configuration at runtime.
func (c *Consumer) UpdateConfig(cfg ConsumerConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cfg.Namespace == "" {
		cfg.Namespace = c.cfg.Namespace
	}
	if cfg.DefaultBatchSize <= 0 {
		cfg.DefaultBatchSize = c.cfg.DefaultBatchSize
	}
	if cfg.DefaultMaxSamplesPerTask <= 0 {
		cfg.DefaultMaxSamplesPerTask = c.cfg.DefaultMaxSamplesPerTask
	}
	if cfg.Store != nil {
		c.store = cfg.Store
	}
	c.cfg = cfg
}

// Start launches a background event consumption loop until ctx is cancelled.
func (c *Consumer) Start(ctx context.Context, events <-chan Event) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				if err := c.ConsumeEvent(ctx, ev); err != nil {
					log.Printf("consumer: error handling %s event for %s: %v", ev.EventKind(), ev.Repository().String(), err)
				}
			}
		}
	}()
}

// ConsumeEvent turns a typed forge event into Runs and Comparisons.
// Replaying the same event multiple times is strictly idempotent.
func (c *Consumer) ConsumeEvent(ctx context.Context, ev Event) error {
	if ev == nil || c.client == nil {
		return nil
	}

	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()

	switch event := ev.(type) {
	case BranchAdvancedEvent:
		if cfg.DefaultModel == "" {
			log.Printf("consumer: defaultModel not configured; skipping Run creation for %s", ev.Repository().String())
			return nil
		}
		return c.processBranchAdvanced(ctx, cfg, event)
	case ChangeOpenedEvent:
		if cfg.DefaultModel == "" {
			log.Printf("consumer: defaultModel not configured; skipping Run and Comparison creation for %s", ev.Repository().String())
			return nil
		}
		return c.processChangeOpenedOrUpdated(ctx, cfg, event.Repo, event.Change)
	case ChangeUpdatedEvent:
		if cfg.DefaultModel == "" {
			log.Printf("consumer: defaultModel not configured; skipping Run and Comparison creation for %s", ev.Repository().String())
			return nil
		}
		return c.processChangeOpenedOrUpdated(ctx, cfg, event.Repo, event.Change)
	case ChangeClosedEvent:
		return c.processChangeClosed(ctx, cfg, event.Repo, event.Change)
	default:
		log.Printf("consumer: unrecognized event kind %q", ev.EventKind())
		return nil
	}
}

func (c *Consumer) getCloneURL(ref forge.RepoRef) string {
	if c.store != nil {
		if r, ok := c.store.GetRepository(ref); ok && r != nil && r.CloneURL != "" {
			return r.CloneURL
		}
	}
	return BuildCloneURLForRef(ref)
}

func repoIdentifier(ref forge.RepoRef) string {
	if ref.Host == "" || strings.EqualFold(ref.Host, "github.com") {
		if ref.Owner != "" && ref.Name != "" {
			return fmt.Sprintf("%s/%s", ref.Owner, ref.Name)
		}
	}
	return ref.String()
}

// processBranchAdvanced reconciles a warm baseline run on pushes to baseline branches.
func (c *Consumer) processBranchAdvanced(ctx context.Context, cfg ConsumerConfig, ev BranchAdvancedEvent) error {
	if !c.isBaselineBranch(cfg, ev.Repo, ev.Branch) {
		return nil
	}

	targetRepo := repoIdentifier(ev.Repo)
	baseRunName := BaselineRunName(targetRepo, ev.NewSHA, cfg.IsMultiRepo)
	sanitizedRepo := SanitizeLabelValue(targetRepo)
	sanitizedBranch := SanitizeLabelValue(ev.Branch)

	labels := map[string]string{
		LabelRepo:    sanitizedRepo,
		LabelHeadSHA: ev.NewSHA,
	}
	if sanitizedBranch != "" {
		labels[LabelBranch] = sanitizedBranch
	}

	var annotations map[string]string
	if ev.Commit != nil && !ev.Commit.Time.IsZero() {
		annotations = map[string]string{
			AnnotationCommitTime: ev.Commit.Time.UTC().Format(time.RFC3339),
		}
	}

	cloneURL := c.getCloneURL(ev.Repo)
	baseRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:        baseRunName,
			Namespace:   cfg.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    cloneURL,
				Commit: ev.NewSHA,
			},
			Model:  cfg.DefaultModel,
			Cohort: cfg.DefaultCohort,
			SampleRange: &v1alpha1.SampleRange{
				Start: 1,
				End:   cfg.DefaultBatchSize,
			},
		},
	}

	if err := c.CreateOrUpdateRun(ctx, baseRun); err != nil {
		return fmt.Errorf("failed to create/update warm baseline run %s/%s: %w", cfg.Namespace, baseRunName, err)
	}

	log.Printf("consumer: warm baseline run %s/%s reconciled for branch %s", cfg.Namespace, baseRunName, ev.Branch)
	return nil
}

// processChangeOpenedOrUpdated reconciles candidate Run, baseline Run (with cache reuse),
// and Comparison for an open or updated change request.
func (c *Consumer) processChangeOpenedOrUpdated(ctx context.Context, cfg ConsumerConfig, repo forge.RepoRef, cr forge.ChangeRequest) error {
	prNum := cr.Number
	if prNum <= 0 {
		return nil
	}
	headSHA := cr.Head.SHA
	baseSHA := cr.Base.SHA
	if headSHA == "" || baseSHA == "" {
		return nil
	}

	targetRepo := repoIdentifier(repo)
	candRepo := targetRepo
	candCloneURL := c.getCloneURL(repo)
	if !cr.Head.RepoRef.IsZero() {
		candRepo = repoIdentifier(cr.Head.RepoRef)
		candCloneURL = c.getCloneURL(cr.Head.RepoRef)
	}
	baseRepo := targetRepo
	baseCloneURL := c.getCloneURL(repo)

	sanitizedCandRepo := SanitizeLabelValue(candRepo)
	sanitizedBaseRepo := SanitizeLabelValue(baseRepo)
	sanitizedTargetRepo := SanitizeLabelValue(targetRepo)

	// Check if a Comparison already exists for this PR and target repository
	existingComp, err := FindComparisonForPR(ctx, c.client, cfg.Namespace, targetRepo, prNum)
	if err != nil {
		return fmt.Errorf("failed looking up comparison for PR #%d: %w", prNum, err)
	}

	var compName string
	if existingComp != nil {
		compName = existingComp.Name
	} else {
		compName = ComparisonName(targetRepo, prNum, cfg.IsMultiRepo)
	}

	candRunName := CandidateRunName(targetRepo, prNum, headSHA, cfg.IsMultiRepo)
	if existingComp != nil && existingComp.Spec.PullRequest != nil &&
		strings.EqualFold(existingComp.Spec.PullRequest.HeadSHA, headSHA) &&
		existingComp.Spec.Candidate.Run != "" {
		candRunName = existingComp.Spec.Candidate.Run
	}

	defaultBaseRunName := BaselineRunName(baseRepo, baseSHA, cfg.IsMultiRepo)
	if existingComp != nil && existingComp.Spec.PullRequest != nil &&
		strings.EqualFold(existingComp.Spec.PullRequest.BaseSHA, baseSHA) &&
		existingComp.Spec.Baseline.Run != "" {
		defaultBaseRunName = existingComp.Spec.Baseline.Run
	}

	// 1. Candidate Run
	candRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      candRunName,
			Namespace: cfg.Namespace,
			Labels: map[string]string{
				LabelPR:      strconv.Itoa(prNum),
				LabelRepo:    sanitizedCandRepo,
				LabelHeadSHA: headSHA,
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    candCloneURL,
				Commit: headSHA,
			},
			Model: cfg.DefaultModel,
			SampleRange: &v1alpha1.SampleRange{
				Start: 1,
				End:   cfg.DefaultBatchSize,
			},
		},
	}
	if err := c.CreateOrUpdateRun(ctx, candRun); err != nil {
		return fmt.Errorf("failed to create/update candidate run %s/%s: %w", cfg.Namespace, candRunName, err)
	}

	// 2. Baseline Run (with cache reuse)
	baselineRunName, err := c.FindCompletedBaselineRun(ctx, cfg.Namespace, baseSHA, baseCloneURL, cfg.DefaultModel, baseRepo, cfg.IsMultiRepo)
	if err != nil {
		log.Printf("warning: error checking baseline cache for commit %s: %v", baseSHA, err)
	}

	if baselineRunName != "" {
		log.Printf("consumer: reusing completed baseline run %s/%s for base commit %s (model %s)", cfg.Namespace, baselineRunName, baseSHA, cfg.DefaultModel)
	} else {
		baselineRunName = defaultBaseRunName
		baseRun := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name:      baselineRunName,
				Namespace: cfg.Namespace,
				Labels: map[string]string{
					LabelRepo:    sanitizedBaseRepo,
					LabelHeadSHA: baseSHA,
				},
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					URL:    baseCloneURL,
					Commit: baseSHA,
				},
				Model: cfg.DefaultModel,
				SampleRange: &v1alpha1.SampleRange{
					Start: 1,
					End:   cfg.DefaultBatchSize,
				},
			},
		}
		if err := c.CreateOrUpdateRun(ctx, baseRun); err != nil {
			return fmt.Errorf("failed to create/update baseline run %s/%s: %w", cfg.Namespace, baselineRunName, err)
		}
	}

	// 3. Comparison
	comp := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:      compName,
			Namespace: cfg.Namespace,
			Labels: map[string]string{
				LabelPR:      strconv.Itoa(prNum),
				LabelRepo:    sanitizedTargetRepo,
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
			Sampling: &v1alpha1.ComparisonSamplingSpec{
				BatchSize:         cfg.DefaultBatchSize,
				MaxSamplesPerTask: cfg.DefaultMaxSamplesPerTask,
			},
			BudgetUSD: cfg.DefaultBudgetUSD,
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: targetRepo,
				Number:     prNum,
				HeadSHA:    headSHA,
				BaseSHA:    baseSHA,
			},
		},
	}
	if err := c.CreateOrUpdateComparison(ctx, comp); err != nil {
		return fmt.Errorf("failed to create/update comparison %s/%s: %w", cfg.Namespace, compName, err)
	}

	log.Printf("consumer: successfully reconciled PR #%d for %s: cand=%s base=%s comp=%s", prNum, targetRepo, candRunName, baselineRunName, compName)
	return nil
}

// processChangeClosed cleans up Comparisons and seed Runs when a change request closes.
func (c *Consumer) processChangeClosed(ctx context.Context, cfg ConsumerConfig, repo forge.RepoRef, cr forge.ChangeRequest) error {
	prNum := cr.Number
	if prNum <= 0 {
		return nil
	}
	prNumStr := strconv.Itoa(prNum)
	targetRepo := repoIdentifier(repo)

	compList, err := FindComparisonsForPR(ctx, c.client, cfg.Namespace, targetRepo, prNum)
	if err != nil {
		return fmt.Errorf("failed to list comparisons for PR #%d in %q: %w", prNum, targetRepo, err)
	}

	candRunNames := make(map[string]bool)
	deletedComparisonNames := make(map[string]bool)
	var deletedComparisons int
	for i := range compList {
		comp := &compList[i]
		if comp.Spec.Candidate.Run != "" {
			candRunNames[comp.Spec.Candidate.Run] = true
		}

		if err := c.client.Delete(ctx, comp, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete comparison %s/%s for PR #%d: %w", comp.Namespace, comp.Name, prNum, err)
		}
		deletedComparisonNames[comp.Name] = true
		deletedComparisons++
	}

	// Find any baseline Runs referenced by surviving Comparisons in the namespace so they are not deleted.
	survivingBaselineRuns := make(map[string]bool)
	var allComps v1alpha1.ComparisonList
	var compListOpts []client.ListOption
	if cfg.Namespace != "" {
		compListOpts = append(compListOpts, client.InNamespace(cfg.Namespace))
	}
	if err := c.client.List(ctx, &allComps, compListOpts...); err != nil {
		return fmt.Errorf("failed to list surviving comparisons: %w", err)
	}
	for i := range allComps.Items {
		cItem := &allComps.Items[i]
		if !deletedComparisonNames[cItem.Name] && cItem.Spec.Baseline.Run != "" {
			survivingBaselineRuns[cItem.Spec.Baseline.Run] = true
		}
	}

	// Delete candidate seed runs for this PR
	runListOpts := []client.ListOption{client.MatchingLabels{LabelPR: prNumStr}}
	if cfg.Namespace != "" {
		runListOpts = append(runListOpts, client.InNamespace(cfg.Namespace))
	}
	var runList v1alpha1.RunList
	if err := c.client.List(ctx, &runList, runListOpts...); err != nil {
		return fmt.Errorf("failed to list runs for PR #%d: %w", prNum, err)
	}

	sanitizedTargetRepo := SanitizeLabelValue(targetRepo)
	var sanitizedCandRepo string
	if !cr.Head.RepoRef.IsZero() {
		sanitizedCandRepo = SanitizeLabelValue(repoIdentifier(cr.Head.RepoRef))
	}

	var deletedRuns int
	for i := range runList.Items {
		run := &runList.Items[i]
		isMatch := candRunNames[run.Name] ||
			(sanitizedTargetRepo != "" && run.Labels[LabelRepo] == sanitizedTargetRepo) ||
			(sanitizedCandRepo != "" && run.Labels[LabelRepo] == sanitizedCandRepo)

		if !isMatch {
			continue
		}

		if survivingBaselineRuns[run.Name] {
			log.Printf("consumer: preserving seed run %s/%s: referenced as baseline by surviving comparison", run.Namespace, run.Name)
			continue
		}
		var ownedBySurvivingComp bool
		for _, ref := range run.OwnerReferences {
			if (ref.Kind == "Comparison" || ref.Kind == "") && !deletedComparisonNames[ref.Name] {
				ownedBySurvivingComp = true
				break
			}
		}
		if ownedBySurvivingComp {
			log.Printf("consumer: preserving seed run %s/%s: carries owner reference to surviving comparison", run.Namespace, run.Name)
			continue
		}

		if err := c.client.Delete(ctx, run); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete seed run %s/%s for PR #%d: %w", run.Namespace, run.Name, prNum, err)
		}
		deletedRuns++
	}

	log.Printf("consumer: closed PR #%d for %s: deleted %d comparison(s) and %d seed run(s)", prNum, targetRepo, deletedComparisons, deletedRuns)
	return nil
}

func (c *Consumer) isBaselineBranch(cfg ConsumerConfig, repo forge.RepoRef, branch string) bool {
	rStr := repoIdentifier(repo)
	for _, r := range cfg.TrackedRepos {
		if strings.EqualFold(r.Name, rStr) || r.Ref.Equal(repo) || strings.EqualFold(r.Name, repo.String()) {
			if len(r.Branches) == 0 {
				return branch == "" || branch == "main"
			}
			for _, b := range r.Branches {
				if b == branch || (branch == "" && b == "main") {
					return true
				}
			}
			return false
		}
	}
	// Untracked repository: default baseline branch is "main"
	return branch == "" || branch == "main"
}

func (c *Consumer) FindCompletedBaselineRun(ctx context.Context, namespace, commit, expectedURL, model string, repo string, isMultiRepo bool) (string, error) {
	var candidateNames []string
	if repo != "" {
		candidateNames = append(candidateNames, BaselineRunName(repo, commit, isMultiRepo))
	}
	legacyName := fmt.Sprintf("base-%s", ShortSHA(commit))
	if len(candidateNames) == 0 || candidateNames[0] != legacyName {
		candidateNames = append(candidateNames, legacyName)
	}

	for _, directName := range candidateNames {
		var directRun v1alpha1.Run
		if err := c.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: directName}, &directRun); err != nil {
			if !apierrors.IsNotFound(err) {
				return "", err
			}
		} else if directRun.Status.Phase == v1alpha1.RunPhaseCompleted &&
			directRun.Spec.Model == model &&
			strings.EqualFold(directRun.Spec.Repo.Commit, commit) &&
			directRun.Spec.Repo.URL == expectedURL {
			return directRun.Name, nil
		}
	}

	var runList v1alpha1.RunList
	if err := c.client.List(ctx, &runList, client.InNamespace(namespace), client.MatchingLabels{LabelHeadSHA: commit}); err != nil {
		return "", err
	}

	for _, r := range runList.Items {
		if r.Status.Phase == v1alpha1.RunPhaseCompleted &&
			r.Spec.Model == model &&
			strings.EqualFold(r.Spec.Repo.Commit, commit) &&
			r.Spec.Repo.URL == expectedURL {
			return r.Name, nil
		}
	}
	return "", nil
}

func (c *Consumer) CreateOrUpdateRun(ctx context.Context, run *v1alpha1.Run) error {
	var existing v1alpha1.Run
	key := types.NamespacedName{
		Namespace: run.Namespace,
		Name:      run.Name,
	}

	if err := c.client.Get(ctx, key, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			if err := c.client.Create(ctx, run); err != nil {
				if apierrors.IsAlreadyExists(err) {
					if getErr := c.client.Get(ctx, key, &existing); getErr != nil {
						return getErr
					}
				} else {
					return err
				}
			} else {
				return nil
			}
		} else {
			return err
		}
	}

	needsUpdate := false
	if existing.Labels == nil {
		existing.Labels = make(map[string]string)
	}
	for k, v := range run.Labels {
		if existing.Labels[k] != v {
			existing.Labels[k] = v
			needsUpdate = true
		}
	}
	if len(run.Annotations) > 0 {
		if existing.Annotations == nil {
			existing.Annotations = make(map[string]string)
		}
		for k, v := range run.Annotations {
			if existing.Annotations[k] != v {
				existing.Annotations[k] = v
				needsUpdate = true
			}
		}
	}

	if needsUpdate {
		return c.client.Update(ctx, &existing)
	}
	return nil
}

func (c *Consumer) CreateOrUpdateComparison(ctx context.Context, comp *v1alpha1.Comparison) error {
	var existing v1alpha1.Comparison
	key := types.NamespacedName{
		Namespace: comp.Namespace,
		Name:      comp.Name,
	}

	if err := c.client.Get(ctx, key, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			if err := c.client.Create(ctx, comp); err != nil {
				if apierrors.IsAlreadyExists(err) {
					if getErr := c.client.Get(ctx, key, &existing); getErr != nil {
						return getErr
					}
				} else {
					return err
				}
			} else {
				return nil
			}
		} else {
			return err
		}
	}

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

	if comp.Spec.Sampling != nil {
		if existing.Spec.Sampling == nil ||
			existing.Spec.Sampling.BatchSize != comp.Spec.Sampling.BatchSize ||
			existing.Spec.Sampling.MaxSamplesPerTask != comp.Spec.Sampling.MaxSamplesPerTask {
			existing.Spec.Sampling = comp.Spec.Sampling.DeepCopy()
			needsUpdate = true
		}
	}

	if comp.Spec.BudgetUSD != nil {
		if existing.Spec.BudgetUSD == nil || *existing.Spec.BudgetUSD != *comp.Spec.BudgetUSD {
			existing.Spec.BudgetUSD = comp.Spec.BudgetUSD
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
		return c.client.Update(ctx, &existing)
	}
	return nil
}

// Helper naming functions

// BuildCloneURL returns the HTTPS git clone URL for a given owner and repository name on github.com.
func BuildCloneURL(owner, name string) string {
	return fmt.Sprintf("https://github.com/%s/%s.git", owner, name)
}

// BuildCloneURLForRef returns the git clone URL for a RepoRef.
func BuildCloneURLForRef(ref forge.RepoRef) string {
	scheme := "https"
	if strings.Contains(ref.Host, "git-server") {
		scheme = "git"
	}
	host := ref.Host
	if host == "" {
		host = "github.com"
	}
	if ref.Owner == "-" {
		return fmt.Sprintf("%s://%s/%s.git", scheme, host, ref.Name)
	}
	return fmt.Sprintf("%s://%s/%s/%s.git", scheme, host, ref.Owner, ref.Name)
}

// ShortSHA returns the first 7 characters of a git commit SHA in lowercase.
func ShortSHA(sha string) string {
	s := strings.ToLower(strings.TrimSpace(sha))
	if len(s) <= 7 {
		return s
	}
	return s[:7]
}

// RepoHash returns a deterministic 7-character lowercase hexadecimal SHA-256 hash of a repository name.
func RepoHash(repo string) string {
	norm := strings.ToLower(strings.TrimSpace(repo))
	h := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(h[:])[:7]
}

// ComparisonName returns the deterministic Comparison resource name for a PR.
func ComparisonName(repo string, prNum int, isMultiRepo bool) string {
	if isMultiRepo && repo != "" {
		return fmt.Sprintf("pr-%s-%d", RepoHash(repo), prNum)
	}
	return fmt.Sprintf("pr-%d", prNum)
}

// CandidateRunName returns the deterministic candidate Run resource name for a PR and head commit.
func CandidateRunName(repo string, prNum int, headSHA string, isMultiRepo bool) string {
	if isMultiRepo && repo != "" {
		return fmt.Sprintf("pr-%s-%d-%s", RepoHash(repo), prNum, ShortSHA(headSHA))
	}
	return fmt.Sprintf("pr-%d-%s", prNum, ShortSHA(headSHA))
}

// BaselineRunName returns the deterministic baseline Run resource name for a repository and commit SHA.
func BaselineRunName(repo string, commitSHA string, isMultiRepo bool) string {
	if isMultiRepo && repo != "" {
		return fmt.Sprintf("base-%s-%s", RepoHash(repo), ShortSHA(commitSHA))
	}
	return fmt.Sprintf("base-%s", ShortSHA(commitSHA))
}

// SanitizeLabelValue converts an arbitrary string into a valid Kubernetes label value.
func SanitizeLabelValue(val string) string {
	val = strings.TrimSpace(val)
	if val == "" {
		return ""
	}

	var sb strings.Builder
	for _, r := range val {
		switch {
		case r == '/':
			sb.WriteRune('_')
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}

	s := sb.String()
	s = strings.TrimFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	if len(s) > 63 {
		s = s[:63]
		s = strings.TrimFunc(s, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
	}
	return s
}

// FindComparisonsForPR returns all Comparisons matching the given PR number and repository.
func FindComparisonsForPR(ctx context.Context, cl client.Reader, namespace, repo string, prNum int) ([]v1alpha1.Comparison, error) {
	if cl == nil || prNum <= 0 {
		return nil, nil
	}

	prNumStr := strconv.Itoa(prNum)
	opts := []client.ListOption{
		client.MatchingLabels{LabelPR: prNumStr},
	}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}

	var compList v1alpha1.ComparisonList
	if err := cl.List(ctx, &compList, opts...); err != nil {
		return nil, fmt.Errorf("failed to list comparisons for PR #%d: %w", prNum, err)
	}

	sanitizedRepo := SanitizeLabelValue(repo)
	var matches []v1alpha1.Comparison
	for _, comp := range compList.Items {
		if comp.Spec.PullRequest != nil && comp.Spec.PullRequest.Number != prNum {
			continue
		}
		if repo != "" {
			isMatch := (comp.Labels != nil && comp.Labels[LabelRepo] == sanitizedRepo) ||
				(comp.Spec.PullRequest != nil && strings.EqualFold(comp.Spec.PullRequest.Repository, repo))
			if !isMatch {
				continue
			}
		}
		matches = append(matches, comp)
	}
	if len(matches) > 0 {
		return matches, nil
	}

	// Fallback by name
	candidateNames := []string{
		fmt.Sprintf("pr-%d", prNum),
	}
	if repo != "" {
		hashedName := fmt.Sprintf("pr-%s-%d", RepoHash(repo), prNum)
		if hashedName != candidateNames[0] {
			candidateNames = append(candidateNames, hashedName)
		}
	}

	for _, name := range candidateNames {
		var comp v1alpha1.Comparison
		key := types.NamespacedName{Namespace: namespace, Name: name}
		if err := cl.Get(ctx, key, &comp); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("failed to get comparison %s/%s: %w", namespace, name, err)
			}
			continue
		}
		if comp.Spec.PullRequest != nil && comp.Spec.PullRequest.Number != prNum {
			continue
		}
		if repo != "" {
			isMatch := (comp.Labels != nil && comp.Labels[LabelRepo] == sanitizedRepo) ||
				(comp.Spec.PullRequest != nil && strings.EqualFold(comp.Spec.PullRequest.Repository, repo))
			if !isMatch {
				continue
			}
		}
		matches = append(matches, comp)
	}

	return matches, nil
}

// FindComparisonForPR returns the primary Comparison matching the given PR number and repository.
func FindComparisonForPR(ctx context.Context, cl client.Reader, namespace, repo string, prNum int) (*v1alpha1.Comparison, error) {
	comps, err := FindComparisonsForPR(ctx, cl, namespace, repo, prNum)
	if err != nil || len(comps) == 0 {
		return nil, err
	}
	return &comps[0], nil
}
