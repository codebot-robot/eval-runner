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
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/gke-labs/eval-runner/pkg/forge/consumer"
	"github.com/gke-labs/eval-runner/pkg/github"
	"github.com/gke-labs/eval-runner/pkg/pooling"
	"github.com/gke-labs/eval-runner/pkg/stats"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *RunReconciler) gitHubAppSecret() string {
	r.cfgMu.RLock()
	defer r.cfgMu.RUnlock()
	return r.GitHubAppSecret
}

func (r *RunReconciler) gitHubTokenSecret() string {
	r.cfgMu.RLock()
	defer r.cfgMu.RUnlock()
	return r.GitHubTokenSecret
}

func (r *RunReconciler) uiBaseURL() string {
	r.cfgMu.RLock()
	defer r.cfgMu.RUnlock()
	return r.UIBaseURL
}

func (r *RunReconciler) headTopUpInterval() time.Duration {
	r.cfgMu.RLock()
	defer r.cfgMu.RUnlock()
	return r.HeadTopUpInterval
}

func (r *RunReconciler) logNoCredsOnce(ctx context.Context) {
	r.noCredsLoggedMu.Lock()
	defer r.noCredsLoggedMu.Unlock()
	if !r.noCredsLogged {
		r.noCredsLogged = true
		logf.FromContext(ctx).Info("GitHub reporting disabled: no GitHub credentials configured")
	}
}

func (r *RunReconciler) getOrCreateAppAuth(secret *corev1.Secret, appIDStr, installIDStr, appSlug string, privKey []byte) (*github.AppTokenProvider, error) {
	cacheKey := fmt.Sprintf("%s/%s/%s/%s/%s", secret.Namespace, secret.Name, secret.ResourceVersion, appIDStr, installIDStr)

	r.cachedAppMu.Lock()
	defer r.cachedAppMu.Unlock()

	if r.cachedAppKey == cacheKey && r.cachedAppAuth != nil {
		return r.cachedAppAuth, nil
	}

	appAuth, err := github.NewAppTokenProvider(github.AppConfig{
		AppID:          appIDStr,
		InstallationID: installIDStr,
		PrivateKeyPEM:  privKey,
		AppSlug:        appSlug,
		BaseURL:        r.GitHubBaseURL,
	})
	if err != nil {
		return nil, err
	}

	r.cachedAppKey = cacheKey
	r.cachedAppAuth = appAuth
	return appAuth, nil
}

func (r *RunReconciler) getGitHubClient(ctx context.Context) (github.Client, error) {
	if r.GitHubClient != nil {
		return r.GitHubClient, nil
	}

	appSec := r.gitHubAppSecret()
	tokSec := r.gitHubTokenSecret()
	secretNames := []string{}
	if appSec != "" {
		secretNames = append(secretNames, appSec)
	}
	if tokSec != "" && tokSec != appSec {
		secretNames = append(secretNames, tokSec)
	}

	if len(secretNames) == 0 {
		r.logNoCredsOnce(ctx)
		return nil, nil
	}

	for _, secretName := range secretNames {
		var secret corev1.Secret
		err := r.Get(ctx, types.NamespacedName{
			Namespace: r.controllerNamespace(),
			Name:      secretName,
		}, &secret)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("reading secret %s: %w", secretName, err)
		}

		if secretName == appSec {
			appIDStr := strings.TrimSpace(string(secret.Data["app-id"]))
			installIDStr := strings.TrimSpace(string(secret.Data["installation-id"]))
			privKey := secret.Data["private-key"]
			appSlug := strings.TrimSpace(string(secret.Data["app-slug"]))

			if appIDStr != "" && installIDStr != "" && len(privKey) > 0 {
				appAuth, err := r.getOrCreateAppAuth(&secret, appIDStr, installIDStr, appSlug, privKey)
				if err != nil {
					return nil, fmt.Errorf("initializing app auth from secret %s: %w", secretName, err)
				}

				creds := GitHubCredentials{
					AppID:          appIDStr,
					InstallationID: installIDStr,
					PrivateKey:     privKey,
					AppSlug:        appSlug,
					AppAuth:        appAuth,
				}
				if r.GitHubClientFactory != nil {
					return r.GitHubClientFactory(creds), nil
				}
				return github.NewHTTPClient(github.HTTPClientConfig{
					AppAuth: appAuth,
					BaseURL: r.GitHubBaseURL,
				}), nil
			}
		}

		tok := strings.TrimSpace(string(secret.Data["token"]))
		if tok != "" {
			creds := GitHubCredentials{Token: tok}
			if r.GitHubClientFactory != nil {
				return r.GitHubClientFactory(creds), nil
			}
			return github.NewHTTPClient(github.HTTPClientConfig{
				Token:   tok,
				BaseURL: r.GitHubBaseURL,
			}), nil
		}
	}

	r.logNoCredsOnce(ctx)
	return nil, nil
}

func cleanRepo(repoOrURL string) string {
	s := strings.TrimSpace(repoOrURL)
	s = strings.TrimPrefix(s, "https://github.com/")
	s = strings.TrimPrefix(s, "http://github.com/")
	s = strings.TrimPrefix(s, "git@github.com:")
	s = strings.TrimPrefix(s, "git://github.com/")
	s = strings.TrimSuffix(s, ".git")
	return strings.Trim(s, "/")
}

func resolveRunRepoRef(run *v1alpha1.Run) forge.RepoRef {
	if run == nil {
		return forge.RepoRef{}
	}
	if run.Labels != nil {
		if labelVal, ok := run.Labels[consumer.LabelTargetRepo]; ok && labelVal != "" {
			parts := strings.Split(labelVal, "_")
			if len(parts) >= 2 {
				return forge.RepoRef{
					Host:  "github.com",
					Owner: parts[0],
					Name:  strings.Join(parts[1:], "_"),
				}
			}
		}
		if labelVal, ok := run.Labels[consumer.LabelRepo]; ok && labelVal != "" {
			parts := strings.Split(labelVal, "_")
			if len(parts) >= 2 {
				return forge.RepoRef{
					Host:  "github.com",
					Owner: parts[0],
					Name:  strings.Join(parts[1:], "_"),
				}
			}
		}
	}
	if run.Spec.Repo.URL != "" {
		cleaned := cleanRepo(run.Spec.Repo.URL)
		parts := strings.Split(cleaned, "/")
		if len(parts) >= 2 {
			return forge.RepoRef{
				Host:  "github.com",
				Owner: parts[0],
				Name:  strings.Join(parts[1:], "/"),
			}
		}
	}
	return forge.RepoRef{}
}

func isTrackedBranchRun(run *v1alpha1.Run) bool {
	return run != nil &&
		run.Labels != nil &&
		run.Labels[consumer.LabelBranch] != ""
}

func (r *RunReconciler) reportBranchHeadInProgress(ctx context.Context, run *v1alpha1.Run) error {
	if !isTrackedBranchRun(run) {
		return nil
	}
	// Only seed run creates in_progress check run
	if run.Labels != nil && run.Labels[consumer.LabelTopUp] != "" {
		return nil
	}
	if run.Status.Reporting != nil && run.Status.Reporting.CheckRunID != 0 {
		return nil
	}

	ghClient, err := r.getGitHubClient(ctx)
	if err != nil || ghClient == nil {
		return nil
	}

	repoRef := resolveRunRepoRef(run)
	if repoRef.Owner == "" || repoRef.Name == "" {
		return nil
	}
	branch := run.Labels[consumer.LabelBranch]
	commitSHA := pooling.RunCommitSHA(run)
	if commitSHA == "" {
		return nil
	}

	state := github.CheckRunStateForBranchHead(nil, nil, r.uiBaseURL(), repoRef, branch, run.Status.Phase, false, 0)
	rep := github.NewReporter(ghClient, github.ReporterOptions{UIBaseURL: r.uiBaseURL()})
	checkRunID, hash, err := rep.ReportBranchHead(ctx, repoRef, commitSHA, state, 0)
	if err != nil {
		return fmt.Errorf("reporting branch head in_progress: %w", err)
	}

	runKey := types.NamespacedName{Namespace: run.Namespace, Name: run.Name}
	return r.updateRunStatus(ctx, runKey, func(status *v1alpha1.RunStatus, gen int64) {
		status.Reporting = &v1alpha1.RunReportingStatus{
			CheckRunID:     checkRunID,
			LastPostedHash: hash,
		}
	})
}

func (r *RunReconciler) branchLock(repoRef forge.RepoRef, branch string) *sync.Mutex {
	r.branchReportMu.Lock()
	defer r.branchReportMu.Unlock()
	if r.branchReportLocks == nil {
		r.branchReportLocks = make(map[string]*sync.Mutex)
	}
	key := fmt.Sprintf("%s/%s/%s@%s", repoRef.Host, repoRef.Owner, repoRef.Name, branch)
	mu, ok := r.branchReportLocks[key]
	if !ok {
		mu = &sync.Mutex{}
		r.branchReportLocks[key] = mu
	}
	return mu
}

func (r *RunReconciler) reportBranchHeadTerminal(ctx context.Context, run *v1alpha1.Run) error {
	if !isTrackedBranchRun(run) {
		return nil
	}

	ghClient, err := r.getGitHubClient(ctx)
	if err != nil || ghClient == nil {
		return nil
	}

	repoRef := resolveRunRepoRef(run)
	if repoRef.Owner == "" || repoRef.Name == "" {
		return nil
	}
	branch := run.Labels[consumer.LabelBranch]
	if branch == "" {
		return nil
	}

	mu := r.branchLock(repoRef, branch)
	mu.Lock()
	defer mu.Unlock()

	currentCommitSHA := pooling.RunCommitSHA(run)
	if currentCommitSHA == "" {
		return nil
	}

	var runList v1alpha1.RunList
	if err := r.List(ctx, &runList, client.InNamespace(run.Namespace), client.MatchingLabels{consumer.LabelBranch: branch}); err != nil {
		return fmt.Errorf("listing runs for branch head reporting: %w", err)
	}

	var matchedRuns []*v1alpha1.Run
	for i := range runList.Items {
		item := &runList.Items[i]
		itemRef := resolveRunRepoRef(item)
		if itemRef.Owner != repoRef.Owner || itemRef.Name != repoRef.Name {
			continue
		}
		matchedRuns = append(matchedRuns, item)
	}

	runsByCommit := make(map[string][]*v1alpha1.Run)
	type commitInfo struct {
		sha  string
		time time.Time
	}
	var commits []commitInfo
	seenCommits := make(map[string]bool)

	for _, item := range matchedRuns {
		sha := pooling.RunCommitSHA(item)
		if sha == "" {
			continue
		}
		runsByCommit[sha] = append(runsByCommit[sha], item)
		if !seenCommits[sha] {
			seenCommits[sha] = true
			ct, _ := pooling.RunCommitTime(item)
			commits = append(commits, commitInfo{sha: sha, time: ct})
		}
	}

	if len(commits) == 0 {
		return nil
	}

	sort.Slice(commits, func(i, j int) bool {
		if !commits[i].time.Equal(commits[j].time) {
			return commits[i].time.Before(commits[j].time)
		}
		return commits[i].sha < commits[j].sha
	})

	headCommitSHA := commits[len(commits)-1].sha
	if currentCommitSHA != headCommitSHA {
		// Only the current branch-head commit reports check runs
		return nil
	}

	headRuns := runsByCommit[headCommitSHA]
	var seedRun *v1alpha1.Run
	for _, hr := range headRuns {
		if hr.Labels == nil || hr.Labels[consumer.LabelTopUp] == "" {
			seedRun = hr
			break
		}
	}
	if seedRun == nil {
		seedRun = headRuns[0]
	}

	headPool := pooling.PoolRunsForCommit(headRuns)

	var lastMove *stats.DeltaEstimate
	if len(commits) >= 2 {
		parentCommitSHA := commits[len(commits)-2].sha
		parentRuns := runsByCommit[parentCommitSHA]
		parentPool := pooling.PoolRunsForCommit(parentRuns)
		lastMove = pooling.ComputeCommitDelta(parentPool, headPool)
	}

	topUpsEnabled := r.headTopUpInterval() > 0
	var nextTopUpIn time.Duration
	if topUpsEnabled {
		lastRunTime := headPool.CommitTime
		for _, hr := range headRuns {
			if hr.CreationTimestamp.After(lastRunTime) {
				lastRunTime = hr.CreationTimestamp.Time
			}
		}
		nextTime := lastRunTime.Add(r.headTopUpInterval())
		now := r.now()
		if nextTime.After(now) {
			nextTopUpIn = nextTime.Sub(now)
		}
	}

	state := github.CheckRunStateForBranchHead(
		headPool,
		lastMove,
		r.uiBaseURL(),
		repoRef,
		branch,
		run.Status.Phase,
		topUpsEnabled,
		nextTopUpIn,
	)

	// Ensure we inspect the freshest status of seedRun
	var freshSeed v1alpha1.Run
	seedKey := types.NamespacedName{Namespace: seedRun.Namespace, Name: seedRun.Name}
	if err := r.Get(ctx, seedKey, &freshSeed); err != nil {
		return fmt.Errorf("getting fresh seed run status: %w", err)
	}
	seedRun = &freshSeed

	checkRunID := int64(0)
	if seedRun.Status.Reporting != nil {
		checkRunID = seedRun.Status.Reporting.CheckRunID
	}
	if checkRunID == 0 && run.Status.Reporting != nil {
		checkRunID = run.Status.Reporting.CheckRunID
	}

	hash := github.HashCheckRunState(state)
	if checkRunID != 0 && seedRun.Status.Reporting != nil && seedRun.Status.Reporting.LastPostedHash == hash {
		// Content unchanged, skip update
		return nil
	}

	rep := github.NewReporter(ghClient, github.ReporterOptions{UIBaseURL: r.uiBaseURL()})
	newCheckRunID, postedHash, err := rep.ReportBranchHead(ctx, repoRef, headCommitSHA, state, checkRunID)
	if err != nil {
		return fmt.Errorf("reporting branch head check run: %w", err)
	}

	if err := r.updateRunStatus(ctx, seedKey, func(status *v1alpha1.RunStatus, gen int64) {
		status.Reporting = &v1alpha1.RunReportingStatus{
			CheckRunID:     newCheckRunID,
			LastPostedHash: postedHash,
		}
	}); err != nil {
		return fmt.Errorf("updating seed run reporting status: %w", err)
	}

	if run.Name != seedRun.Name {
		runKey := types.NamespacedName{Namespace: run.Namespace, Name: run.Name}
		if err := r.updateRunStatus(ctx, runKey, func(status *v1alpha1.RunStatus, gen int64) {
			status.Reporting = &v1alpha1.RunReportingStatus{
				CheckRunID:     newCheckRunID,
				LastPostedHash: postedHash,
			}
		}); err != nil {
			return fmt.Errorf("updating run reporting status: %w", err)
		}
	}

	return nil
}

// BackfillBranchHeadCheckRuns runs a single backfill pass over all tracked repository x branch targets.
// For the newest head commit on each branch only, if the seed Run is terminal and status.reporting.checkRunID
// is unset, it posts a check run to GitHub and updates the Run status.
// Bounding to the newest head per branch ensures a controller restart does not post check runs for historical commits.
// Returns the count of backfilled check runs.
func (r *RunReconciler) BackfillBranchHeadCheckRuns(ctx context.Context) (int, error) {
	ghClient, err := r.getGitHubClient(ctx)
	if err != nil {
		return 0, fmt.Errorf("getting github client for backfill: %w", err)
	}
	if ghClient == nil {
		return 0, nil
	}

	var runList v1alpha1.RunList
	if err := r.List(ctx, &runList, client.InNamespace(r.controllerNamespace())); err != nil {
		return 0, fmt.Errorf("listing runs for branch head backfill: %w", err)
	}

	type repoBranchKey struct {
		host   string
		owner  string
		repo   string
		branch string
	}
	runsByTarget := make(map[repoBranchKey][]*v1alpha1.Run)
	for i := range runList.Items {
		run := &runList.Items[i]
		if !isTrackedBranchRun(run) {
			continue
		}
		repoRef := resolveRunRepoRef(run)
		if repoRef.Owner == "" || repoRef.Name == "" {
			continue
		}
		host := repoRef.Host
		if host == "" {
			host = "github.com"
		}
		branch := run.Labels[consumer.LabelBranch]
		key := repoBranchKey{
			host:   host,
			owner:  repoRef.Owner,
			repo:   repoRef.Name,
			branch: branch,
		}
		runsByTarget[key] = append(runsByTarget[key], run)
	}

	var keys []repoBranchKey
	for k := range runsByTarget {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].host != keys[j].host {
			return keys[i].host < keys[j].host
		}
		if keys[i].owner != keys[j].owner {
			return keys[i].owner < keys[j].owner
		}
		if keys[i].repo != keys[j].repo {
			return keys[i].repo < keys[j].repo
		}
		return keys[i].branch < keys[j].branch
	})

	count := 0
	for _, key := range keys {
		targetRuns := runsByTarget[key]
		runsByCommit := make(map[string][]*v1alpha1.Run)
		type commitInfo struct {
			sha  string
			time time.Time
		}
		var commits []commitInfo
		seenCommits := make(map[string]bool)

		for _, item := range targetRuns {
			sha := pooling.RunCommitSHA(item)
			if sha == "" {
				continue
			}
			runsByCommit[sha] = append(runsByCommit[sha], item)
			if !seenCommits[sha] {
				seenCommits[sha] = true
				ct, _ := pooling.RunCommitTime(item)
				commits = append(commits, commitInfo{sha: sha, time: ct})
			}
		}
		if len(commits) == 0 {
			continue
		}

		sort.Slice(commits, func(i, j int) bool {
			if !commits[i].time.Equal(commits[j].time) {
				return commits[i].time.Before(commits[j].time)
			}
			return commits[i].sha < commits[j].sha
		})

		headCommitSHA := commits[len(commits)-1].sha
		headRuns := runsByCommit[headCommitSHA]
		var seedRun *v1alpha1.Run
		for _, hr := range headRuns {
			if hr.Labels == nil || hr.Labels[consumer.LabelTopUp] == "" {
				seedRun = hr
				break
			}
		}
		if seedRun == nil {
			seedRun = headRuns[0]
		}

		if seedRun.Status.Phase != v1alpha1.RunPhaseCompleted && seedRun.Status.Phase != v1alpha1.RunPhaseFailed {
			continue
		}

		var freshSeed v1alpha1.Run
		seedKey := types.NamespacedName{Namespace: seedRun.Namespace, Name: seedRun.Name}
		if err := r.Get(ctx, seedKey, &freshSeed); err != nil {
			logf.FromContext(ctx).Error(err, "failed getting fresh seed run for backfill", "seed", seedKey)
			continue
		}
		seedRun = &freshSeed

		if seedRun.Status.Reporting != nil && seedRun.Status.Reporting.CheckRunID != 0 {
			continue
		}

		alreadyReported := false
		for _, hr := range headRuns {
			if hr.Status.Reporting != nil && hr.Status.Reporting.CheckRunID != 0 {
				alreadyReported = true
				break
			}
		}
		if alreadyReported {
			continue
		}

		if err := r.reportBranchHeadTerminal(ctx, seedRun); err != nil {
			logf.FromContext(ctx).Error(err, "failed backfilling branch-head check run",
				"repo", fmt.Sprintf("%s/%s", key.owner, key.repo),
				"branch", key.branch,
				"commit", headCommitSHA,
			)
			continue
		}

		var postSeed v1alpha1.Run
		if err := r.Get(ctx, seedKey, &postSeed); err != nil {
			logf.FromContext(ctx).Error(err, "failed getting post-reporting seed run for backfill", "seed", seedKey)
			continue
		}
		if postSeed.Status.Reporting != nil && postSeed.Status.Reporting.CheckRunID != 0 &&
			(freshSeed.Status.Reporting == nil || postSeed.Status.Reporting.CheckRunID != freshSeed.Status.Reporting.CheckRunID) {
			count++
		}
	}

	logf.FromContext(ctx).Info("backfilled branch-head check runs", "count", count)
	return count, nil
}

// BranchHeadBackfillRunnable executes a single backfill pass after leader election.
type BranchHeadBackfillRunnable struct {
	Reconciler *RunReconciler
}

func (b *BranchHeadBackfillRunnable) Start(ctx context.Context) error {
	count, err := b.Reconciler.BackfillBranchHeadCheckRuns(ctx)
	if err != nil && ctx.Err() == nil {
		logf.FromContext(ctx).Error(err, "branch-head check run backfill pass failed")
	} else if err == nil {
		logf.FromContext(ctx).Info("branch-head check run backfill pass completed", "count", count)
	}
	return nil
}

func (b *BranchHeadBackfillRunnable) NeedLeaderElection() bool {
	return true
}
