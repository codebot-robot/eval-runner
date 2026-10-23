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
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/gke-labs/eval-runner/pkg/forge/consumer"
	"github.com/gke-labs/eval-runner/pkg/forge/gitremote"
	"github.com/gke-labs/eval-runner/pkg/forge/store"
	gh "github.com/gke-labs/eval-runner/pkg/github"
	"github.com/go-git/go-git/v5/plumbing"
)

const (
	// DefaultLsRemoteInterval is the default cadence for ls-remote ref checking (60s).
	DefaultLsRemoteInterval = 60 * time.Second
	// DefaultFullReconcileInterval is the default interval for full reconcile with conditional requests (1h).
	DefaultFullReconcileInterval = 1 * time.Hour
	// DefaultRateFloorPercent is the default rate limit remaining floor (10%).
	DefaultRateFloorPercent = 10
)

// PullRequestGetter is optionally implemented by forges that support fetching individual PRs.
type PullRequestGetter interface {
	PullRequest(ctx context.Context, ref forge.RepoRef, number int) (*forge.ChangeRequest, error)
}

// RateLimitProvider is optionally implemented by forges with rate-limiting clients.
type RateLimitProvider interface {
	RateLimit() gh.RateLimitInfo
}

// Syncer synchronizes configured repositories from their authoritative forges into
// the local bbolt store and notifies the event consumer.
type Syncer struct {
	mu                    sync.RWMutex
	store                 *store.Store
	registry              *forge.Registry
	gitRemoteForge        forge.Forge
	consumer              EventHandler
	repos                 []RepoConfig
	lsRemoteInterval      time.Duration
	fullReconcileInterval time.Duration
	rateFloorPercent      int
	jitterFactor          float64
	logger                func(format string, args ...any)

	// Runtime loop lifecycle
	runCtx            context.Context
	runCancel         context.CancelFunc
	repoLoops         map[string]*repoLoop
	lastFullReconcile map[string]time.Time
	hasEverSynced     map[string]bool
	suppressed        map[string]bool
}

type repoLoop struct {
	ref     forge.RepoRef
	cancel  context.CancelFunc
	rearmCh chan time.Duration
	done    chan struct{}
}

type syncerConfigSnapshot struct {
	lsRemoteInterval      time.Duration
	fullReconcileInterval time.Duration
	rateFloorPercent      int
	jitterFactor          float64
}

// SyncerOptions configures a Syncer instance.
type SyncerOptions struct {
	Store                 *store.Store
	Registry              *forge.Registry
	GitRemoteForge        forge.Forge
	Consumer              EventHandler
	Repositories          []RepoConfig
	LsRemoteInterval      time.Duration
	FullReconcileInterval time.Duration
	RateLimitFloorPercent int
	JitterFactor          float64
	Logger                func(format string, args ...any)
}

// New creates a new Syncer with the given options.
func New(opts SyncerOptions) *Syncer {
	lsRemoteInt := opts.LsRemoteInterval
	if lsRemoteInt <= 0 {
		lsRemoteInt = DefaultLsRemoteInterval
	}
	fullRecInt := opts.FullReconcileInterval
	if fullRecInt <= 0 {
		fullRecInt = DefaultFullReconcileInterval
	}
	floorPct := opts.RateLimitFloorPercent
	if floorPct <= 0 {
		floorPct = DefaultRateFloorPercent
	}
	jitter := opts.JitterFactor
	if jitter <= 0 {
		jitter = DefaultJitterFactor
	}

	return &Syncer{
		store:                 opts.Store,
		registry:              opts.Registry,
		gitRemoteForge:        opts.GitRemoteForge,
		consumer:              opts.Consumer,
		repos:                 opts.Repositories,
		lsRemoteInterval:      lsRemoteInt,
		fullReconcileInterval: fullRecInt,
		rateFloorPercent:      floorPct,
		jitterFactor:          jitter,
		logger:                opts.Logger,
		repoLoops:             make(map[string]*repoLoop),
		lastFullReconcile:     make(map[string]time.Time),
		hasEverSynced:         make(map[string]bool),
		suppressed:            make(map[string]bool),
	}
}

// SetConsumer configures the event handler for the syncer.
func (s *Syncer) SetConsumer(c EventHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumer = c
}

func (s *Syncer) getConsumer() EventHandler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.consumer
}

// SetLogger configures a custom logger for state transitions.
func (s *Syncer) SetLogger(l func(format string, args ...any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = l
}

func (s *Syncer) log(format string, args ...any) {
	s.mu.RLock()
	logger := s.logger
	s.mu.RUnlock()
	if logger != nil {
		logger(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

// Repositories returns the current list of tracked repositories.
func (s *Syncer) Repositories() []RepoConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RepoConfig, len(s.repos))
	copy(out, s.repos)
	return out
}

func (s *Syncer) snapshotConfig() syncerConfigSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return syncerConfigSnapshot{
		lsRemoteInterval:      s.lsRemoteInterval,
		fullReconcileInterval: s.fullReconcileInterval,
		rateFloorPercent:      s.rateFloorPercent,
		jitterFactor:          s.jitterFactor,
	}
}

// UpdateConfig updates tracked repositories, intervals, and rate floor at runtime.
// Re-arms running per-repository timers immediately, cancels removed loops (joining them cleanly),
// and updates the HTTP client's floor.
func (s *Syncer) UpdateConfig(repos []RepoConfig, lsRemote, fullRec time.Duration, floorPct int) {
	s.mu.Lock()

	if lsRemote > 0 {
		s.lsRemoteInterval = lsRemote
	}
	if fullRec > 0 {
		s.fullReconcileInterval = fullRec
	}
	if floorPct > 0 {
		s.rateFloorPercent = floorPct
		if s.registry != nil {
			dummyRef := forge.RepoRef{Host: "github.com", Owner: "-", Name: "-"}
			if ghf, ok := s.registry.ForRepo(dummyRef).(interface{ Client() *gh.HTTPClient }); ok && ghf.Client() != nil {
				ghf.Client().SetRateFloorPercent(floorPct)
			}
		}
	}
	s.repos = repos

	newRepoMap := make(map[string]RepoConfig)
	for _, r := range repos {
		if !r.Ref.IsZero() {
			newRepoMap[strings.ToLower(r.Ref.String())] = r
		}
	}

	// 1. Cancel removed repository loops
	var stoppedLoops []*repoLoop
	for rKey, rl := range s.repoLoops {
		if _, stillExists := newRepoMap[rKey]; !stillExists {
			rl.cancel()
			stoppedLoops = append(stoppedLoops, rl)
			delete(s.repoLoops, rKey)
		}
	}

	// 2. Re-arm running existing loops
	for rKey := range newRepoMap {
		if rl, exists := s.repoLoops[rKey]; exists {
			select {
			case rl.rearmCh <- s.lsRemoteInterval:
			default:
			}
		}
	}
	s.mu.Unlock()

	// Wait for stopped loops to finish (outside lock) to prevent goroutine leaks (N1)
	for _, rl := range stoppedLoops {
		<-rl.done
	}

	// 3. Start new loops for newly added repositories
	s.mu.Lock()
	for rKey, r := range newRepoMap {
		if s.repoLoops[rKey] == nil && s.runCtx != nil && s.runCtx.Err() == nil {
			s.startRepoLoopLocked(r.Ref)
		}
	}
	s.mu.Unlock()
}

// UpdateRepositories updates the list of tracked repositories at runtime.
func (s *Syncer) UpdateRepositories(repos []RepoConfig) {
	s.UpdateConfig(repos, 0, 0, 0)
}

// UpdateIntervals updates cadence intervals at runtime and re-arms running timers.
func (s *Syncer) UpdateIntervals(lsRemote, fullRec time.Duration) {
	s.UpdateConfig(s.Repositories(), lsRemote, fullRec, 0)
}

// SetRateFloorPercent updates the rate limit floor percentage at runtime.
func (s *Syncer) SetRateFloorPercent(pct int) {
	s.UpdateConfig(s.Repositories(), 0, 0, pct)
}

// Start runs the syncer background polling loops until ctx is cancelled.
func (s *Syncer) Start(ctx context.Context) error {
	s.mu.Lock()
	s.runCtx, s.runCancel = context.WithCancel(ctx)
	repos := make([]RepoConfig, len(s.repos))
	copy(repos, s.repos)

	for _, r := range repos {
		if !r.Ref.IsZero() {
			s.startRepoLoopLocked(r.Ref)
		}
	}
	s.mu.Unlock()

	<-ctx.Done()

	s.mu.Lock()
	var stoppedLoops []*repoLoop
	for _, rl := range s.repoLoops {
		rl.cancel()
		stoppedLoops = append(stoppedLoops, rl)
	}
	s.repoLoops = make(map[string]*repoLoop)
	s.mu.Unlock()

	for _, rl := range stoppedLoops {
		<-rl.done
	}

	return nil
}

func (s *Syncer) startRepoLoopLocked(ref forge.RepoRef) {
	rKey := strings.ToLower(ref.String())
	if _, exists := s.repoLoops[rKey]; exists {
		return
	}

	loopCtx, cancel := context.WithCancel(s.runCtx)
	rl := &repoLoop{
		ref:     ref,
		cancel:  cancel,
		rearmCh: make(chan time.Duration, 1),
		done:    make(chan struct{}),
	}
	s.repoLoops[rKey] = rl

	go s.runRepoLoop(loopCtx, rl)
}

func (s *Syncer) runRepoLoop(ctx context.Context, rl *repoLoop) {
	defer close(rl.done)

	// Initial sync run immediately
	if err := s.SyncRepo(ctx, rl.ref); err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log("initial sync failed for %s: %v", rl.ref.String(), err)
		}
	}

	snap := s.snapshotConfig()
	d := Jitter(snap.lsRemoteInterval, snap.jitterFactor)
	timer := time.NewTimer(d)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case newInterval := <-rl.rearmCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(Jitter(newInterval, snap.jitterFactor))
		case <-timer.C:
			if err := s.SyncRepo(ctx, rl.ref); err != nil {
				if !errors.Is(err, context.Canceled) {
					s.log("poll tick failed for %s: %v", rl.ref.String(), err)
				}
			}
			curSnap := s.snapshotConfig()
			timer.Reset(Jitter(curSnap.lsRemoteInterval, curSnap.jitterFactor))
		}
	}
}

func (s *Syncer) getRepoConfig(ref forge.RepoRef) RepoConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.repos {
		if r.Ref.Equal(ref) || strings.EqualFold(r.Ref.String(), ref.String()) {
			return r
		}
	}
	return RepoConfig{Ref: ref}
}

func (s *Syncer) resolveForge(ref forge.RepoRef) (forge.Forge, error) {
	if s.registry == nil {
		return nil, errors.New("no forge registry configured")
	}
	f := s.registry.ForRepo(ref)
	if f == nil {
		return nil, fmt.Errorf("no forge available for repository %s", ref.String())
	}
	return f, nil
}

// SyncAll syncs all configured repositories against their forges.
func (s *Syncer) SyncAll(ctx context.Context) error {
	repos := s.Repositories()
	var errs []error
	for _, r := range repos {
		if !r.Ref.IsZero() {
			if err := s.SyncRepo(ctx, r.Ref); err != nil {
				errs = append(errs, fmt.Errorf("sync %s: %w", r.Ref.String(), err))
			}
		}
	}
	return errors.Join(errs...)
}

func recordError(ref forge.RepoRef, phase string) {
	SyncsTotal.WithLabelValues(ref.String(), "error").Inc()
	ErrorsTotal.WithLabelValues(ref.String(), phase).Inc()
}

func (s *Syncer) checkRateLimit(ref forge.RepoRef, f forge.Forge, snap syncerConfigSnapshot) bool {
	if crfs, ok := f.(interface{ Client() *gh.HTTPClient }); ok && crfs.Client() != nil {
		crfs.Client().SetRateFloorPercent(snap.rateFloorPercent)
	}

	rlp, ok := f.(RateLimitProvider)
	if !ok {
		return true
	}
	rl := rlp.RateLimit()
	if rl.Limit <= 0 {
		return true
	}

	RateLimitRemaining.WithLabelValues(ref.String()).Set(float64(rl.Remaining))
	RateLimitLimit.WithLabelValues(ref.String()).Set(float64(rl.Limit))
	RateLimitResetTimestamp.WithLabelValues(ref.String()).Set(float64(rl.Reset.Unix()))

	floor := rl.Limit * snap.rateFloorPercent / 100
	if floor <= 0 {
		floor = 1
	}

	rKey := strings.ToLower(ref.String())
	now := time.Now()
	isBelow := rl.Remaining <= floor && now.Before(rl.Reset)

	s.mu.Lock()
	wasSuppressed := s.suppressed[rKey]
	if isBelow && !wasSuppressed {
		s.suppressed[rKey] = true
		s.mu.Unlock()
		s.log("forge rate limit budget floor reached for %s: %d remaining <= %d floor of %d; suppressing API calls until reset at %s", ref.String(), rl.Remaining, floor, rl.Limit, rl.Reset.Format(time.RFC3339))
		return false
	} else if !isBelow && wasSuppressed {
		s.suppressed[rKey] = false
		s.mu.Unlock()
		if rl.Remaining > floor {
			s.log("forge rate limit budget recovered for %s: %d remaining > %d floor of %d; resuming API calls", ref.String(), rl.Remaining, floor, rl.Limit)
		} else {
			s.log("forge rate limit budget recovered for %s: window reset passed at %s; resuming API calls", ref.String(), rl.Reset.Format(time.RFC3339))
		}
		return true
	}
	s.mu.Unlock()

	return !isBelow
}

func (s *Syncer) fetchCommitIfUnknown(ctx context.Context, f forge.Forge, ref forge.RepoRef, sha string) (*forge.Commit, error) {
	if sha == "" {
		return nil, nil
	}
	if s.store != nil {
		if c, ok := s.store.GetCommit(sha); ok && c != nil {
			return c, nil
		}
	}
	commit, err := f.Commit(ctx, ref, sha)
	if err != nil {
		if errors.Is(err, forge.ErrUnsupported) || errors.Is(err, forge.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return commit, nil
}

// SyncRepo syncs a repository against its authoritative forge.
func (s *Syncer) SyncRepo(ctx context.Context, ref forge.RepoRef) error {
	if !ref.IsValid() {
		return fmt.Errorf("invalid repository reference: %v", ref)
	}

	// 1. Resolve forge
	f, err := s.resolveForge(ref)
	if err != nil {
		recordError(ref, "resolve")
		return fmt.Errorf("resolve forge for %s: %w", ref.String(), err)
	}

	snap := s.snapshotConfig()
	repoCfg := s.getRepoConfig(ref)

	// 2. Query remote ref advertisement via git ls-remote if gitRemoteForge configured or github/gitremote
	var remoteRefs []*plumbing.Reference
	var cloneURL string
	var lsErr error

	if s.gitRemoteForge != nil && (f.Kind() == "github.com" || f.Kind() == "gitremote") {
		if repoCfg.CloneURL != "" {
			if grf, ok := s.gitRemoteForge.(*gitremote.Forge); ok {
				grf.RegisterURL(ref, repoCfg.CloneURL)
			}
		}
		if grf, ok := s.gitRemoteForge.(*gitremote.Forge); ok {
			remoteRefs, cloneURL, lsErr = grf.ListRefs(ctx, ref)
		}
		if lsErr != nil {
			recordError(ref, "ls_remote")
			return fmt.Errorf("ls-remote failed for %s: %w", ref.String(), lsErr)
		}
	}

	if cloneURL == "" {
		cloneURL = repoCfg.CloneURL
	}
	if s.store != nil && cloneURL != "" {
		if _, ok := s.store.GetRepository(ref); !ok {
			if saveErr := s.store.SaveRepository(&forge.Repository{
				Ref:      ref,
				CloneURL: cloneURL,
			}); saveErr != nil {
				recordError(ref, "store_write")
				return fmt.Errorf("failed to save repository for %s: %w", ref.String(), saveErr)
			}
		}
	}

	// 3. gitremote-only repositories do one ls-remote per tick
	if f.Kind() == "gitremote" {
		return s.syncGitRemoteOnly(ctx, ref, cloneURL, remoteRefs)
	}

	useLSRemote := (lsErr == nil && len(remoteRefs) > 0)

	// 4. Check if full reconcile is due
	s.mu.RLock()
	rKey := strings.ToLower(ref.String())
	hasSynced := s.hasEverSynced[rKey]
	lastFull := s.lastFullReconcile[rKey]
	s.mu.RUnlock()

	isFullReconcile := !hasSynced || time.Since(lastFull) >= snap.fullReconcileInterval || !useLSRemote

	var storedBranches []forge.Branch
	var storedCRs []forge.ChangeRequest
	if s.store != nil {
		var bErr, crErr error
		storedBranches, bErr = s.store.ListBranches(ref)
		if bErr != nil {
			recordError(ref, "store_read")
			return fmt.Errorf("failed to list stored branches for %s: %w", ref.String(), bErr)
		}
		storedCRs, crErr = s.store.ListChangeRequests(ref)
		if crErr != nil {
			recordError(ref, "store_read")
			return fmt.Errorf("failed to list stored change requests for %s: %w", ref.String(), crErr)
		}
	}

	if !isFullReconcile && useLSRemote {
		// Fill-in tick: Diff against all stored state
		diff := DiffRefs(storedBranches, storedCRs, remoteRefs)
		if !diff.HasChanges {
			// Zero API calls when unchanged!
			SyncsTotal.WithLabelValues(ref.String(), "success").Inc()
			return nil
		}

		// Rate budget floor check
		if !s.checkRateLimit(ref, f, snap) {
			return nil
		}

		// Fill in only changed refs: NO Repository() and NO full /pulls list!
		return s.syncFillIn(ctx, f, ref, cloneURL, diff, storedBranches, storedCRs)
	}

	// Full reconcile tick (seeding tick or periodic reconcile)
	if !s.checkRateLimit(ref, f, snap) {
		return nil
	}

	return s.syncFullReconcile(ctx, f, ref, cloneURL, remoteRefs, storedBranches, storedCRs)
}

func (s *Syncer) syncGitRemoteOnly(ctx context.Context, ref forge.RepoRef, cloneURL string, remoteRefs []*plumbing.Reference) error {
	branches := gitremote.ExtractBranches(remoteRefs)
	defaultBranch := gitremote.ExtractSymbolicHEAD(remoteRefs)

	var storedBranches []forge.Branch
	if s.store != nil {
		var bErr error
		storedBranches, bErr = s.store.ListBranches(ref)
		if bErr != nil {
			recordError(ref, "store_read")
			return fmt.Errorf("failed to list stored branches for %s: %w", ref.String(), bErr)
		}
	}

	diff := DiffRefs(storedBranches, nil, remoteRefs)
	var events []consumer.Event
	storedBranchMap := make(map[string]string)
	for _, b := range storedBranches {
		storedBranchMap[b.Name] = b.SHA
	}

	for _, b := range diff.NewBranches {
		events = append(events, consumer.BranchAdvancedEvent{
			Repo:     ref,
			Branch:   b.Name,
			OldSHA:   storedBranchMap[b.Name],
			NewSHA:   b.SHA,
			CloneURL: cloneURL,
		})
	}
	for _, b := range diff.UpdatedBranches {
		events = append(events, consumer.BranchAdvancedEvent{
			Repo:     ref,
			Branch:   b.Name,
			OldSHA:   storedBranchMap[b.Name],
			NewSHA:   b.SHA,
			CloneURL: cloneURL,
		})
	}

	// Consume events before commit
	c := s.getConsumer()
	for _, ev := range events {
		if c != nil {
			if err := c.ConsumeEvent(ctx, ev); err != nil {
				recordError(ref, "consumer")
				return fmt.Errorf("consumer failed on event for %s: %w", ref.String(), err)
			}
		}
	}

	if s.store != nil {
		batch := &store.SyncBatch{
			Repo: &forge.Repository{
				Ref:           ref,
				DefaultBranch: defaultBranch,
				CloneURL:      cloneURL,
			},
			Branches:       branches,
			FullBranchSync: true,
			Sync: &store.SyncMeta{
				LastPolled: time.Now().UTC(),
			},
		}
		if err := s.store.SaveSyncBatch(ctx, ref, batch); err != nil {
			recordError(ref, "store_write")
			return fmt.Errorf("failed to save sync batch for %s: %w", ref.String(), err)
		}
	}

	SyncsTotal.WithLabelValues(ref.String(), "success").Inc()
	return nil
}

func (s *Syncer) syncFillIn(
	ctx context.Context,
	f forge.Forge,
	ref forge.RepoRef,
	cloneURL string,
	diff RefDiff,
	storedBranches []forge.Branch,
	storedCRs []forge.ChangeRequest,
) error {
	var events []consumer.Event
	var newCommits []*forge.Commit

	if cloneURL == "" && s.store != nil {
		if repo, ok := s.store.GetRepository(ref); ok && repo != nil {
			cloneURL = repo.CloneURL
		}
	}

	storedBranchMap := make(map[string]string)
	for _, b := range storedBranches {
		storedBranchMap[b.Name] = b.SHA
	}

	// 1. Process branch changes
	for _, b := range append(diff.NewBranches, diff.UpdatedBranches...) {
		commit, err := s.fetchCommitIfUnknown(ctx, f, ref, b.SHA)
		if err != nil {
			recordError(ref, "fetch_commit")
			s.clearPendingSyncs(f, ref)
			return fmt.Errorf("fetch commit %s for %s: %w", b.SHA, ref.String(), err)
		}
		if commit != nil {
			newCommits = append(newCommits, commit)
		}
		events = append(events, consumer.BranchAdvancedEvent{
			Repo:     ref,
			Branch:   b.Name,
			OldSHA:   storedBranchMap[b.Name],
			NewSHA:   b.SHA,
			Commit:   commit,
			CloneURL: cloneURL,
		})
	}

	// 2. Process PR changes via single PullRequest fetch
	prGetter, _ := f.(PullRequestGetter)
	var updatedCRs []forge.ChangeRequest

	for _, num := range diff.OpenedPRs {
		var cr *forge.ChangeRequest
		var err error
		if prGetter != nil {
			cr, err = prGetter.PullRequest(ctx, ref, num)
		}
		if err != nil {
			recordError(ref, "fetch_pr")
			s.clearPendingSyncs(f, ref)
			return fmt.Errorf("fetch PR #%d for %s: %w", num, ref.String(), err)
		}
		if cr != nil {
			updatedCRs = append(updatedCRs, *cr)
			if cr.Head.SHA != "" {
				c, cErr := s.fetchCommitIfUnknown(ctx, f, ref, cr.Head.SHA)
				if cErr == nil && c != nil {
					newCommits = append(newCommits, c)
				}
			}
			if isCROpen(*cr) {
				events = append(events, consumer.ChangeOpenedEvent{
					Repo:              ref,
					Change:            *cr,
					CandidateCloneURL: candidateCloneURL(*cr, cloneURL),
					BaselineCloneURL:  cloneURL,
				})
			}
		}
	}

	for _, num := range diff.SyncPRs {
		var cr *forge.ChangeRequest
		var err error
		if prGetter != nil {
			cr, err = prGetter.PullRequest(ctx, ref, num)
		}
		if err != nil {
			recordError(ref, "fetch_pr")
			s.clearPendingSyncs(f, ref)
			return fmt.Errorf("fetch PR #%d for %s: %w", num, ref.String(), err)
		}
		if cr != nil {
			updatedCRs = append(updatedCRs, *cr)
			if cr.Head.SHA != "" {
				c, cErr := s.fetchCommitIfUnknown(ctx, f, ref, cr.Head.SHA)
				if cErr == nil && c != nil {
					newCommits = append(newCommits, c)
				}
			}

			// B3: Closed-PR head-moved case emits NOTHING!
			var prevCR *forge.ChangeRequest
			for _, p := range storedCRs {
				if p.Number == num {
					pCopy := p
					prevCR = &pCopy
					break
				}
			}

			prevOpen := prevCR != nil && isCROpen(*prevCR)
			currOpen := isCROpen(*cr)

			if !prevOpen && !currOpen {
				// Closed PR head moved: stored SHA updated in updatedCRs, emit nothing!
			} else if !prevOpen && currOpen {
				// Previously closed PR re-opened on GitHub: emit ChangeOpenedEvent!
				events = append(events, consumer.ChangeOpenedEvent{
					Repo:              ref,
					Change:            *cr,
					CandidateCloneURL: candidateCloneURL(*cr, cloneURL),
					BaselineCloneURL:  cloneURL,
				})
			} else if prevOpen && !currOpen {
				// Open PR closed: emit ChangeClosedEvent!
				events = append(events, consumer.ChangeClosedEvent{
					Repo:   ref,
					Change: *cr,
				})
			} else {
				// Open PR synchronized: emit ChangeUpdatedEvent!
				oldHeadSHA := ""
				if prevCR != nil {
					oldHeadSHA = prevCR.Head.SHA
				}
				events = append(events, consumer.ChangeUpdatedEvent{
					Repo:              ref,
					OldHeadSHA:        oldHeadSHA,
					Change:            *cr,
					CandidateCloneURL: candidateCloneURL(*cr, cloneURL),
					BaselineCloneURL:  cloneURL,
				})
			}
		}
	}

	for _, num := range diff.ClosedPRs {
		for _, cr := range storedCRs {
			if cr.Number == num {
				closedCR := cr
				closedCR.State = "closed"
				updatedCRs = append(updatedCRs, closedCR)
				events = append(events, consumer.ChangeClosedEvent{
					Repo:   ref,
					Change: closedCR,
				})
				break
			}
		}
	}

	// 3. Synchronous consume-before-commit: abort on consumer failure
	c := s.getConsumer()
	for _, ev := range events {
		if c != nil {
			if err := c.ConsumeEvent(ctx, ev); err != nil {
				recordError(ref, "consumer")
				// On consumer error, discard pending validators so next tick refetches fully
				s.clearPendingSyncs(f, ref)
				return fmt.Errorf("consumer failed on event for %s: %w", ref.String(), err)
			}
		}
	}

	// 4. Commit to store with validators traveling in the batch
	updatedBranches := applyBranchDiff(storedBranches, diff)
	batch := &store.SyncBatch{
		Branches: updatedBranches,
		Changes:  updatedCRs,
		Commits:  newCommits,
		Sync: &store.SyncMeta{
			LastPolled: time.Now().UTC(),
		},
	}
	if epCollector, ok := f.(interface {
		PopPendingEndpointSyncs(ref forge.RepoRef) map[string]*store.EndpointSync
	}); ok {
		batch.EndpointSyncs = epCollector.PopPendingEndpointSyncs(ref)
	}

	if s.store != nil {
		if err := s.store.SaveSyncBatch(ctx, ref, batch); err != nil {
			recordError(ref, "store_write")
			return fmt.Errorf("save sync batch for %s: %w", ref.String(), err)
		}
	}

	SyncsTotal.WithLabelValues(ref.String(), "success").Inc()
	return nil
}

func (s *Syncer) syncFullReconcile(
	ctx context.Context,
	f forge.Forge,
	ref forge.RepoRef,
	cloneURL string,
	remoteRefs []*plumbing.Reference,
	storedBranches []forge.Branch,
	storedCRs []forge.ChangeRequest,
) error {
	var repoMeta *forge.Repository
	if r, rErr := f.Repository(ctx, ref); rErr == nil && r != nil {
		repoMeta = r
	} else if rErr != nil && !errors.Is(rErr, forge.ErrUnsupported) && !errors.Is(rErr, forge.ErrNotFound) {
		recordError(ref, "repository")
		s.clearPendingSyncs(f, ref)
		return fmt.Errorf("get repository for %s: %w", ref.String(), rErr)
	}
	if repoMeta == nil && s.store != nil {
		repoMeta, _ = s.store.GetRepository(ref)
	}
	if repoMeta == nil {
		repoMeta = &forge.Repository{
			Ref:           ref,
			DefaultBranch: "main",
			CloneURL:      cloneURL,
		}
	}
	if cloneURL == "" && repoMeta.CloneURL != "" {
		cloneURL = repoMeta.CloneURL
	}
	if cloneURL != "" && repoMeta.CloneURL == "" {
		repoMeta.CloneURL = cloneURL
	}

	branches, bErr := f.Branches(ctx, ref)
	if bErr != nil {
		recordError(ref, "branches")
		s.clearPendingSyncs(f, ref)
		return fmt.Errorf("get branches for %s: %w", ref.String(), bErr)
	}

	openCRs, crErr := f.ChangeRequests(ctx, ref)
	if crErr != nil {
		recordError(ref, "change_requests")
		s.clearPendingSyncs(f, ref)
		return fmt.Errorf("get change requests for %s: %w", ref.String(), crErr)
	}

	// Closed-head seeding:
	// Seed placeholder closed CRs for every advertised pull head not returned as open.
	openCRNums := make(map[int]bool)
	for _, cr := range openCRs {
		openCRNums[cr.Number] = true
	}
	storedCRNums := make(map[int]bool)
	for _, cr := range storedCRs {
		storedCRNums[cr.Number] = true
	}

	defaultBranch := "main"
	if repoMeta != nil && repoMeta.DefaultBranch != "" {
		defaultBranch = repoMeta.DefaultBranch
	}

	var seededCRs []forge.ChangeRequest
	for _, r := range remoteRefs {
		if r == nil {
			continue
		}
		name := r.Name().String()
		if strings.HasPrefix(name, "refs/pull/") {
			trimmed := strings.TrimPrefix(name, "refs/pull/")
			parts := strings.Split(trimmed, "/")
			if len(parts) == 2 && parts[1] == "head" {
				if num, err := strconv.Atoi(parts[0]); err == nil && num > 0 {
					if !openCRNums[num] && !storedCRNums[num] {
						placeholder := forge.ChangeRequest{
							ID:     forge.ChangeRequestID(strconv.Itoa(num)),
							Number: num,
							Title:  fmt.Sprintf("Pull request #%d", num),
							State:  "closed",
							Head: forge.ChangeRequestHead{
								Ref:     fmt.Sprintf("refs/pull/%d/head", num),
								SHA:     r.Hash().String(),
								RepoRef: ref,
							},
							Base: forge.ChangeRequestBase{
								Ref: defaultBranch,
							},
						}
						seededCRs = append(seededCRs, placeholder)
					}
				}
			}
		}
	}

	allCRs := append(openCRs, seededCRs...)

	var commits []*forge.Commit
	for _, b := range branches {
		c, err := s.fetchCommitIfUnknown(ctx, f, ref, b.SHA)
		if err == nil && c != nil {
			commits = append(commits, c)
		}
	}
	for _, cr := range openCRs {
		if cr.Head.SHA != "" {
			c, err := s.fetchCommitIfUnknown(ctx, f, ref, cr.Head.SHA)
			if err == nil && c != nil {
				commits = append(commits, c)
			}
		}
	}

	// Generate events
	var events []consumer.Event
	storedBranchMap := make(map[string]string)
	for _, b := range storedBranches {
		storedBranchMap[b.Name] = b.SHA
	}
	for _, b := range branches {
		if prevSHA, exists := storedBranchMap[b.Name]; !exists || prevSHA != b.SHA {
			events = append(events, consumer.BranchAdvancedEvent{
				Repo:     ref,
				Branch:   b.Name,
				OldSHA:   storedBranchMap[b.Name],
				NewSHA:   b.SHA,
				Commit:   s.findCommit(commits, b.SHA),
				CloneURL: cloneURL,
			})
		}
	}

	storedOpenCRMap := make(map[int]forge.ChangeRequest)
	for _, cr := range storedCRs {
		if isCROpen(cr) {
			storedOpenCRMap[cr.Number] = cr
		}
	}
	seenInResponse := make(map[int]bool)
	for _, cr := range openCRs {
		seenInResponse[cr.Number] = true
		prevCR, hadPrev := storedOpenCRMap[cr.Number]
		if isCROpen(cr) {
			if !hadPrev {
				events = append(events, consumer.ChangeOpenedEvent{
					Repo:              ref,
					Change:            cr,
					CandidateCloneURL: candidateCloneURL(cr, cloneURL),
					BaselineCloneURL:  cloneURL,
				})
			} else if prevCR.Head.SHA != cr.Head.SHA {
				events = append(events, consumer.ChangeUpdatedEvent{
					Repo:              ref,
					OldHeadSHA:        prevCR.Head.SHA,
					Change:            cr,
					CandidateCloneURL: candidateCloneURL(cr, cloneURL),
					BaselineCloneURL:  cloneURL,
				})
			}
		} else {
			if hadPrev {
				events = append(events, consumer.ChangeClosedEvent{
					Repo:   ref,
					Change: cr,
				})
			}
		}
	}

	for num, prevCR := range storedOpenCRMap {
		if !seenInResponse[num] {
			closedCR := prevCR
			closedCR.State = "closed"
			events = append(events, consumer.ChangeClosedEvent{
				Repo:   ref,
				Change: closedCR,
			})
			allCRs = append(allCRs, closedCR)
		}
	}

	// Synchronous consume-before-commit: abort on consumer failure
	c := s.getConsumer()
	for _, ev := range events {
		if c != nil {
			if err := c.ConsumeEvent(ctx, ev); err != nil {
				recordError(ref, "consumer")
				s.clearPendingSyncs(f, ref)
				return fmt.Errorf("consumer failed on event for %s: %w", ref.String(), err)
			}
		}
	}

	batch := &store.SyncBatch{
		Repo:           repoMeta,
		Branches:       branches,
		FullBranchSync: true,
		Changes:        allCRs,
		Commits:        commits,
		Sync: &store.SyncMeta{
			LastPolled: time.Now().UTC(),
		},
	}
	if epCollector, ok := f.(interface {
		PopPendingEndpointSyncs(ref forge.RepoRef) map[string]*store.EndpointSync
	}); ok {
		batch.EndpointSyncs = epCollector.PopPendingEndpointSyncs(ref)
	}

	if s.store != nil {
		if err := s.store.SaveSyncBatch(ctx, ref, batch); err != nil {
			recordError(ref, "store_write")
			return fmt.Errorf("save sync batch for %s: %w", ref.String(), err)
		}
	}

	s.mu.Lock()
	rKey := strings.ToLower(ref.String())
	s.lastFullReconcile[rKey] = time.Now()
	s.hasEverSynced[rKey] = true
	s.mu.Unlock()

	SyncsTotal.WithLabelValues(ref.String(), "success").Inc()
	return nil
}

func (s *Syncer) findCommit(commits []*forge.Commit, sha string) *forge.Commit {
	for _, c := range commits {
		if c.SHA == sha {
			return c
		}
	}
	if s.store != nil {
		if c, ok := s.store.GetCommit(sha); ok {
			return c
		}
	}
	return nil
}

func (s *Syncer) clearPendingSyncs(f forge.Forge, ref forge.RepoRef) {
	if clearable, ok := f.(interface{ ClearPendingEndpointSyncs(forge.RepoRef) }); ok {
		clearable.ClearPendingEndpointSyncs(ref)
	}
}

func candidateCloneURL(cr forge.ChangeRequest, defaultCloneURL string) string {
	if cr.Head.CloneURL != "" {
		return cr.Head.CloneURL
	}
	return defaultCloneURL
}

func isCROpen(cr forge.ChangeRequest) bool {
	return strings.EqualFold(cr.State, "open")
}

func applyBranchDiff(stored []forge.Branch, diff RefDiff) []forge.Branch {
	m := make(map[string]string)
	for _, b := range stored {
		m[b.Name] = b.SHA
	}
	for _, b := range diff.NewBranches {
		m[b.Name] = b.SHA
	}
	for _, b := range diff.UpdatedBranches {
		m[b.Name] = b.SHA
	}
	for _, name := range diff.DeletedBranches {
		delete(m, name)
	}
	var out []forge.Branch
	for name, sha := range m {
		out = append(out, forge.Branch{Name: name, SHA: sha})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
