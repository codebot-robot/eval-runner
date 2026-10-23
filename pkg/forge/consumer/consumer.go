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

package consumer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/forge"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Consumer processes typed forge events and turns them into Runs and Comparisons.
type Consumer struct {
	client             client.Client
	store              RepoGetter
	cfg                Config
	mu                 sync.Mutex
	defaultModelLogged bool
}

// New constructs a Consumer with the given Kubernetes client and configuration.
func New(cl client.Client, cfg Config) *Consumer {
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
func (c *Consumer) UpdateConfig(cfg Config) {
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

// ConsumeEvent executes the corresponding Kubernetes mutations for an Event synchronously.
func (c *Consumer) ConsumeEvent(ctx context.Context, ev Event) error {
	if ev == nil || c.client == nil {
		return nil
	}

	c.mu.Lock()
	cfg := c.cfg
	modelLogged := c.defaultModelLogged
	if cfg.DefaultModel == "" {
		if !modelLogged {
			log.Printf("consumer: defaultModel is unset; skipping Run and Comparison creation")
			c.defaultModelLogged = true
		}
	}
	c.mu.Unlock()

	switch e := ev.(type) {
	case BranchAdvancedEvent:
		if strings.TrimSpace(cfg.DefaultModel) == "" {
			return nil
		}
		return c.processBranchAdvanced(ctx, cfg, e)

	case ChangeOpenedEvent:
		if err := c.reopenComparisons(ctx, cfg.Namespace, e.Repo, e.Change.Number); err != nil {
			return err
		}
		if strings.TrimSpace(cfg.DefaultModel) == "" {
			return nil
		}
		return c.processChangeOpenedOrUpdated(ctx, cfg, e.Repo, e.Change, e.CandidateCloneURL, e.BaselineCloneURL)

	case ChangeUpdatedEvent:
		if strings.TrimSpace(cfg.DefaultModel) == "" {
			return nil
		}
		return c.processChangeOpenedOrUpdated(ctx, cfg, e.Repo, e.Change, e.CandidateCloneURL, e.BaselineCloneURL)

	case ChangeClosedEvent:
		// ChangeClosed should run cleanup even if defaultModel is unset
		return c.processChangeClosed(ctx, cfg, e.Repo, e.Change)

	default:
		log.Printf("consumer: unsupported event kind %q", ev.EventKind())
		return nil
	}
}

func (c *Consumer) getCloneURL(ref forge.RepoRef, hintURL string) (string, error) {
	if hintURL != "" {
		return hintURL, nil
	}
	if c.store != nil {
		if repo, ok := c.store.GetRepository(ref); ok && repo != nil && repo.CloneURL != "" {
			return repo.CloneURL, nil
		}
	}
	return "", fmt.Errorf("no clone URL available for repository %s (missing from store and event hint)", ref.String())
}

func repoIdentifier(ref forge.RepoRef) string {
	if strings.EqualFold(ref.Host, "github.com") || ref.Host == "" {
		return fmt.Sprintf("%s/%s", ref.Owner, ref.Name)
	}
	if ref.Owner == "-" {
		return fmt.Sprintf("%s/%s", ref.Host, ref.Name)
	}
	return fmt.Sprintf("%s/%s/%s", ref.Host, ref.Owner, ref.Name)
}

func (c *Consumer) isBaselineBranch(cfg Config, repo forge.RepoRef, branch string) bool {
	if branch == "" {
		branch = "main"
	}
	rStr := repoIdentifier(repo)
	for _, r := range cfg.TrackedRepos {
		if strings.EqualFold(r.Name, rStr) || r.Ref.Equal(repo) || strings.EqualFold(r.Name, repo.String()) {
			if len(r.Branches) == 0 {
				return branch == "main"
			}
			for _, b := range r.Branches {
				if b == branch {
					return true
				}
			}
			return false
		}
	}
	return branch == "main"
}

func (c *Consumer) processBranchAdvanced(ctx context.Context, cfg Config, ev BranchAdvancedEvent) error {
	branch := ev.Branch
	if branch == "" {
		branch = "main"
	}
	if !c.isBaselineBranch(cfg, ev.Repo, branch) {
		return nil
	}

	targetRepo := repoIdentifier(ev.Repo)
	baseRunName := BaselineRunName(targetRepo, ev.NewSHA, cfg.IsMultiRepo)
	sanitizedRepo := SanitizeLabelValue(targetRepo)
	sanitizedBranch := SanitizeLabelValue(branch)

	labels := map[string]string{
		LabelRepo:    sanitizedRepo,
		LabelHeadSHA: ev.NewSHA,
		LabelBranch:  sanitizedBranch,
	}

	var annotations map[string]string
	commitTime := ev.CommitTime
	if commitTime == "" && ev.Commit != nil && !ev.Commit.Time.IsZero() {
		commitTime = ev.Commit.Time.Format("2006-01-02T15:04:05Z07:00")
	}
	if commitTime != "" {
		annotations = map[string]string{
			AnnotationCommitTime: commitTime,
		}
	}

	cloneURL, err := c.getCloneURL(ev.Repo, ev.CloneURL)
	if err != nil {
		return fmt.Errorf("failed to resolve clone URL for branch %s on %s: %w", branch, targetRepo, err)
	}

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
		return fmt.Errorf("failed to reconcile push baseline run %s/%s: %w", cfg.Namespace, baseRunName, err)
	}

	log.Printf("consumer: successfully reconciled push baseline run %s/%s for commit %s (branch %s)",
		cfg.Namespace, baseRunName, ev.NewSHA, branch)
	return nil
}

func (c *Consumer) processChangeOpenedOrUpdated(ctx context.Context, cfg Config, repo forge.RepoRef, cr forge.ChangeRequest, candCloneURLHint, baseCloneURLHint string) error {
	prNum := cr.Number
	headSHA := cr.Head.SHA
	baseSHA := cr.Base.SHA

	targetRepo := repoIdentifier(repo)
	candRepo := targetRepo
	if !cr.Head.RepoRef.IsZero() {
		candRepo = repoIdentifier(cr.Head.RepoRef)
	}
	baseRepo := targetRepo

	candCloneURL := cr.Head.CloneURL
	if candCloneURL == "" {
		candCloneURL = candCloneURLHint
	}
	if candCloneURL == "" {
		if !cr.Head.RepoRef.IsZero() {
			candURL, err := c.getCloneURL(cr.Head.RepoRef, "")
			if err != nil {
				return fmt.Errorf("failed to resolve candidate clone URL: %w", err)
			}
			candCloneURL = candURL
		} else {
			candURL, err := c.getCloneURL(repo, "")
			if err != nil {
				return fmt.Errorf("failed to resolve candidate clone URL: %w", err)
			}
			candCloneURL = candURL
		}
	}

	baseCloneURL := baseCloneURLHint
	if baseCloneURL == "" {
		baseURL, err := c.getCloneURL(repo, "")
		if err != nil {
			return fmt.Errorf("failed to resolve baseline clone URL: %w", err)
		}
		baseCloneURL = baseURL
	}

	sanitizedCandRepo := SanitizeLabelValue(candRepo)
	sanitizedBaseRepo := SanitizeLabelValue(baseRepo)
	sanitizedTargetRepo := SanitizeLabelValue(targetRepo)

	// Check if a Comparison already exists for this PR
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

	// 2. Baseline Run with Cache Reuse
	baselineRunName, err := c.FindCompletedBaselineRun(ctx, cfg.Namespace, baseSHA, baseCloneURL, cfg.DefaultModel, baseRepo, cfg.IsMultiRepo)
	if err != nil {
		log.Printf("warning: error checking baseline cache for commit %s: %v", baseSHA, err)
	}

	if baselineRunName != "" {
		log.Printf("consumer: reusing completed baseline run %s/%s for base commit %s (model %s)",
			cfg.Namespace, baselineRunName, baseSHA, cfg.DefaultModel)
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
				LabelPRState: StateOpen,
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

	log.Printf("consumer: successfully reconciled PR #%d: candidate=%s, baseline=%s, comparison=%s",
		prNum, candRunName, baselineRunName, compName)
	return nil
}

func prStateOnClose(cr forge.ChangeRequest) string {
	if cr.Merged || strings.EqualFold(cr.State, StateMerged) {
		return StateMerged
	}
	return StateClosed
}

func (c *Consumer) reopenComparisons(ctx context.Context, namespace string, repo forge.RepoRef, prNum int) error {
	if c.client == nil || prNum <= 0 {
		return nil
	}
	targetRepo := repoIdentifier(repo)
	compList, err := FindComparisonsForPR(ctx, c.client, namespace, targetRepo, prNum)
	if err != nil {
		return fmt.Errorf("failed to list comparisons for PR #%d in %q on reopen: %w", prNum, targetRepo, err)
	}
	for i := range compList {
		comp := &compList[i]
		if comp.Labels == nil || comp.Labels[LabelPRState] != StateOpen {
			if comp.Labels == nil {
				comp.Labels = make(map[string]string)
			}
			comp.Labels[LabelPRState] = StateOpen
			if err := c.client.Update(ctx, comp); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to update comparison %s/%s for PR #%d on reopen: %w", comp.Namespace, comp.Name, prNum, err)
			}
		}
	}
	return nil
}

func (c *Consumer) processChangeClosed(ctx context.Context, cfg Config, repo forge.RepoRef, cr forge.ChangeRequest) error {
	prNum := cr.Number
	prNumStr := strconv.Itoa(prNum)
	targetRepo := repoIdentifier(repo)

	compList, err := FindComparisonsForPR(ctx, c.client, cfg.Namespace, targetRepo, prNum)
	if err != nil {
		return fmt.Errorf("failed to list comparisons for PR #%d in %q: %w", prNum, targetRepo, err)
	}

	targetState := prStateOnClose(cr)
	var updatedComparisons int
	for i := range compList {
		comp := &compList[i]
		if comp.Labels == nil {
			comp.Labels = make(map[string]string)
		}
		if comp.Labels[LabelPRState] != targetState {
			comp.Labels[LabelPRState] = targetState
			if err := c.client.Update(ctx, comp); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to update comparison %s/%s for PR #%d: %w", comp.Namespace, comp.Name, prNum, err)
			}
			updatedComparisons++
		}
	}

	// Gather runs referenced by ALL Comparisons in namespace
	referencedRuns := make(map[string]bool)
	var allComps v1alpha1.ComparisonList
	var compListOpts []client.ListOption
	if cfg.Namespace != "" {
		compListOpts = append(compListOpts, client.InNamespace(cfg.Namespace))
	}
	if err := c.client.List(ctx, &allComps, compListOpts...); err != nil {
		return fmt.Errorf("failed to list comparisons: %w", err)
	}
	for i := range allComps.Items {
		comp := &allComps.Items[i]
		if comp.Spec.Candidate.Run != "" {
			referencedRuns[comp.Spec.Candidate.Run] = true
		}
		if comp.Spec.Baseline.Run != "" {
			referencedRuns[comp.Spec.Baseline.Run] = true
		}
	}

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
		isMatch := (sanitizedTargetRepo != "" && run.Labels[LabelRepo] == sanitizedTargetRepo) ||
			(sanitizedCandRepo != "" && run.Labels[LabelRepo] == sanitizedCandRepo) ||
			(run.Labels[LabelRepo] == "")

		if !isMatch {
			continue
		}

		if referencedRuns[run.Name] {
			log.Printf("consumer: preserving seed run %s/%s: referenced by comparison", run.Namespace, run.Name)
			continue
		}
		var ownedByComp bool
		for _, ref := range run.OwnerReferences {
			if ref.Kind == "Comparison" || ref.Kind == "" {
				ownedByComp = true
				break
			}
		}
		if ownedByComp {
			log.Printf("consumer: preserving seed run %s/%s: carries owner reference to comparison", run.Namespace, run.Name)
			continue
		}

		if err := c.client.Delete(ctx, run); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete seed run %s/%s for PR #%d: %w", run.Namespace, run.Name, prNum, err)
		}
		deletedRuns++
	}

	log.Printf("consumer: closed PR #%d for %s (state=%s): updated %d comparison(s), deleted %d orphaned seed run(s)", prNum, targetRepo, targetState, updatedComparisons, deletedRuns)
	return nil
}

// FindCompletedBaselineRun checks the baseline cache for a completed Run matching commit, url, model, and repo.
func (c *Consumer) FindCompletedBaselineRun(ctx context.Context, namespace, commit, expectedURL, model string, repo string, isMultiRepo bool) (string, error) {
	if c.client == nil || commit == "" {
		return "", nil
	}

	defaultName := BaselineRunName(repo, commit, isMultiRepo)
	var directRun v1alpha1.Run
	err := c.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: defaultName}, &directRun)
	if err == nil {
		if directRun.Status.Phase == v1alpha1.RunPhaseCompleted &&
			directRun.Spec.Model == model &&
			strings.EqualFold(directRun.Spec.Repo.Commit, commit) &&
			directRun.Spec.Repo.URL == expectedURL {
			return directRun.Name, nil
		}
	} else if !apierrors.IsNotFound(err) {
		return "", err
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

// CreateOrUpdateRun creates a Run or idempotently updates its labels and annotations.
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

// CreateOrUpdateComparison creates a Comparison or idempotently updates its spec and labels.
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
	sha7 := ShortSHA(headSHA)
	if isMultiRepo && repo != "" {
		return fmt.Sprintf("pr-%s-%d-%s", RepoHash(repo), prNum, sha7)
	}
	return fmt.Sprintf("pr-%d-%s", prNum, sha7)
}

// BaselineRunName returns the deterministic baseline Run resource name for a commit.
func BaselineRunName(repo string, commitSHA string, isMultiRepo bool) string {
	sha7 := ShortSHA(commitSHA)
	if isMultiRepo && repo != "" {
		return fmt.Sprintf("base-%s-%s", RepoHash(repo), sha7)
	}
	return fmt.Sprintf("base-%s", sha7)
}

// SanitizeLabelValue normalizes a string into a valid Kubernetes label value.
func SanitizeLabelValue(val string) string {
	val = strings.TrimSpace(val)
	if val == "" {
		return ""
	}

	var sb strings.Builder
	for _, r := range val {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}
	res := sb.String()
	res = strings.Trim(res, "-_.")
	if len(res) > 63 {
		res = res[:63]
		res = strings.Trim(res, "-_.")
	}
	return res
}

// FindComparisonsForPR finds all Comparisons for a pull request number and repository.
func FindComparisonsForPR(ctx context.Context, cl client.Reader, namespace, repo string, prNum int) ([]v1alpha1.Comparison, error) {
	if cl == nil || prNum <= 0 {
		return nil, nil
	}

	prNumStr := strconv.Itoa(prNum)
	var compList v1alpha1.ComparisonList
	var listOpts []client.ListOption
	if namespace != "" {
		listOpts = append(listOpts, client.InNamespace(namespace))
	}
	listOpts = append(listOpts, client.MatchingLabels{LabelPR: prNumStr})

	if err := cl.List(ctx, &compList, listOpts...); err != nil {
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

	// Fallback by name for backwards compatibility (e.g. unlabelled test fixtures or existing cluster objects)
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

// FindComparisonForPR finds a single Comparison matching repo and PR number.
func FindComparisonForPR(ctx context.Context, cl client.Reader, namespace, repo string, prNum int) (*v1alpha1.Comparison, error) {
	matches, err := FindComparisonsForPR(ctx, cl, namespace, repo, prNum)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return &matches[0], nil
}
