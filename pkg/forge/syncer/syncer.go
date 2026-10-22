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
	// DefaultLsRemoteInterval is the default polling interval for git ls-remote (60s).
	DefaultLsRemoteInterval = 60 * time.Second
	// DefaultAPIFillInInterval is the default interval for API fill-in (0s / immediate on change).
	DefaultAPIFillInInterval = 0 * time.Second
	// DefaultFullReconcileInterval is the default interval for full reconcile with conditional requests (1h).
	DefaultFullReconcileInterval = 1 * time.Hour
	// DefaultRateFloorPercent is the default rate limit remaining floor (10%).
	DefaultRateFloorPercent = 10
)

// PullRequestGetter is optionally implemented by forges that support fetching individual PRs.
type PullRequestGetter interface {
	PullRequest(ctx context.Context, ref forge.RepoRef, number int) (*forge.ChangeRequest, error)
}

// RateLimitReporter is optionally implemented by forges that report rate limits.
type RateLimitReporter interface {
	RateLimit() gh.RateLimitInfo
}

type repoLoop struct {
	cancel       context.CancelFunc
	trigger      chan struct{}
	intervalChan chan time.Duration
}

// Syncer synchronizes configured repositories from their authoritative forges into
// the local bbolt store and notifies the event consumer.
type Syncer struct {
	mu                    sync.RWMutex
	store                 *store.Store
	registry              *forge.Registry
	consumer              EventHandler
	repos                 []RepoConfig
	gitremote             *gitremote.Forge
	lsRemoteInterval      time.Duration
	apiFillInInterval     time.Duration
	fullReconcileInterval time.Duration
	rateFloorPercent      int

	runCtx              context.Context
	repoLoops           map[string]*repoLoop
	lastFullReconcile   map[string]time.Time
	lastAPIFillIn       map[string]time.Time
	lastRateLimitLogged map[string]int
}

// SyncerOptions configures a Syncer instance.
type SyncerOptions struct {
	Store                 *store.Store
	Registry              *forge.Registry
	Consumer              EventHandler
	Repositories          []RepoConfig
	GitRemote             *gitremote.Forge
	LsRemoteInterval      time.Duration
	APIFillInInterval     time.Duration
	FullReconcileInterval time.Duration
	RateFloorPercent      int
}

// New creates a new Syncer with the given options.
func New(opts SyncerOptions) *Syncer {
	gr := opts.GitRemote
	if gr == nil {
		gr = gitremote.New()
	}
	lsInterval := opts.LsRemoteInterval
	if lsInterval <= 0 {
		lsInterval = DefaultLsRemoteInterval
	}
	apiInterval := opts.APIFillInInterval
	if apiInterval < 0 {
		apiInterval = DefaultAPIFillInInterval
	}
	fullRecInterval := opts.FullReconcileInterval
	if fullRecInterval <= 0 {
		fullRecInterval = DefaultFullReconcileInterval
	}
	rateFloor := opts.RateFloorPercent
	if rateFloor <= 0 {
		rateFloor = DefaultRateFloorPercent
	}

	s := &Syncer{
		store:                 opts.Store,
		registry:              opts.Registry,
		consumer:              opts.Consumer,
		repos:                 opts.Repositories,
		gitremote:             gr,
		lsRemoteInterval:      lsInterval,
		apiFillInInterval:     apiInterval,
		fullReconcileInterval: fullRecInterval,
		rateFloorPercent:      rateFloor,
		repoLoops:             make(map[string]*repoLoop),
		lastFullReconcile:     make(map[string]time.Time),
		lastAPIFillIn:         make(map[string]time.Time),
		lastRateLimitLogged:   make(map[string]int),
	}
	return s
}

// Start starts the background polling loops for tracked repositories.
// It implements manager.Runnable.
func (s *Syncer) Start(ctx context.Context) error {
	s.mu.Lock()
	s.runCtx = ctx
	repos := make([]RepoConfig, len(s.repos))
	copy(repos, s.repos)
	s.mu.Unlock()

	for _, r := range repos {
		if !r.Ref.IsZero() {
			s.startRepoLoop(ctx, r.Ref)
		}
	}
	<-ctx.Done()
	return nil
}

func (s *Syncer) startRepoLoop(ctx context.Context, ref forge.RepoRef) {
	rKey := strings.ToLower(ref.String())
	s.mu.Lock()
	if s.repoLoops == nil {
		s.repoLoops = make(map[string]*repoLoop)
	}
	if _, exists := s.repoLoops[rKey]; exists {
		// Already running; do not start a duplicate goroutine.
		s.mu.Unlock()
		return
	}

	loopCtx, cancel := context.WithCancel(ctx)
	loop := &repoLoop{
		cancel:       cancel,
		trigger:      make(chan struct{}, 1),
		intervalChan: make(chan time.Duration, 1),
	}
	s.repoLoops[rKey] = loop
	interval := s.lsRemoteInterval
	s.mu.Unlock()

	go func() {
		defer cancel()

		// Run initial poll
		if err := s.SyncRepo(loopCtx, ref); err != nil {
			log.Printf("forge syncer: initial poll for %s completed with error: %v", ref.String(), err)
		}

		currentInterval := interval
		for {
			jittered := Jitter(currentInterval, DefaultJitterFactor)
			timer := time.NewTimer(jittered)

			select {
			case <-loopCtx.Done():
				timer.Stop()
				return
			case <-loop.trigger:
				timer.Stop()
			case newInterval := <-loop.intervalChan:
				timer.Stop()
				if newInterval > 0 {
					currentInterval = newInterval
				}
				continue
			case <-timer.C:
			}

			if err := s.SyncRepo(loopCtx, ref); err != nil {
				log.Printf("forge syncer: poll for %s failed: %v", ref.String(), err)
			}
		}
	}()
}

// ScheduleImmediatePoll triggers an immediate poll for the specified repository.
func (s *Syncer) ScheduleImmediatePoll(ref forge.RepoRef) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rKey := strings.ToLower(ref.String())
	if loop, ok := s.repoLoops[rKey]; ok {
		select {
		case loop.trigger <- struct{}{}:
		default:
		}
	}
}

// TriggerSync is an alias for ScheduleImmediatePoll.
func (s *Syncer) TriggerSync(ref forge.RepoRef) {
	s.ScheduleImmediatePoll(ref)
}

// UpdateIntervals updates cadence intervals at runtime and immediately re-arms running loops.
func (s *Syncer) UpdateIntervals(lsRemote, apiFillIn, fullReconcile time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lsRemote > 0 {
		s.lsRemoteInterval = lsRemote
	}
	if apiFillIn >= 0 {
		s.apiFillInInterval = apiFillIn
	}
	if fullReconcile > 0 {
		s.fullReconcileInterval = fullReconcile
	}
	for _, loop := range s.repoLoops {
		select {
		case loop.intervalChan <- lsRemote:
		default:
		}
	}
}

// SetRateFloorPercent updates the rate limit floor percentage at runtime.
func (s *Syncer) SetRateFloorPercent(pct int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pct <= 0 {
		pct = DefaultRateFloorPercent
	}
	s.rateFloorPercent = pct
}

// SetConsumer configures the event handler for the syncer.
func (s *Syncer) SetConsumer(c EventHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumer = c
}

// Repositories returns the current list of tracked repositories.
func (s *Syncer) Repositories() []RepoConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RepoConfig, len(s.repos))
	copy(out, s.repos)
	return out
}

// UpdateRepositories updates the list of tracked repositories at runtime,
// canceling loops for removed repositories and starting loops for newly added ones.
func (s *Syncer) UpdateRepositories(repos []RepoConfig) {
	s.mu.Lock()
	s.repos = repos
	ctx := s.runCtx

	// Cancel and remove loops for dropped repositories
	newRepoSet := make(map[string]bool)
	for _, r := range repos {
		if !r.Ref.IsZero() {
			newRepoSet[strings.ToLower(r.Ref.String())] = true
		}
	}
	for rKey, loop := range s.repoLoops {
		if !newRepoSet[rKey] {
			loop.cancel()
			delete(s.repoLoops, rKey)
		}
	}
	s.mu.Unlock()

	// Start loops for newly added repositories
	if ctx != nil {
		for _, r := range repos {
			if !r.Ref.IsZero() {
				s.startRepoLoop(ctx, r.Ref)
			}
		}
	}
}

func (s *Syncer) getRepoConfig(ref forge.RepoRef) RepoConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.repos {
		if r.Ref.Equal(ref) {
			return r
		}
		if strings.EqualFold(r.Ref.String(), ref.String()) {
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

// SyncRepo syncs a single repository against its forge without hints.
func (s *Syncer) SyncRepo(ctx context.Context, ref forge.RepoRef) error {
	return s.SyncRepoWithHint(ctx, ref, nil)
}

func recordError(ref forge.RepoRef, phase string) {
	SyncsTotal.WithLabelValues(ref.String(), "error").Inc()
	ErrorsTotal.WithLabelValues(ref.String(), phase).Inc()
}

func (s *Syncer) logRateLimitBackoff(ref forge.RepoRef, remaining, floor int, reset time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rKey := strings.ToLower(ref.String())
	if s.lastRateLimitLogged == nil {
		s.lastRateLimitLogged = make(map[string]int)
	}
	if s.lastRateLimitLogged[rKey] != remaining {
		s.lastRateLimitLogged[rKey] = remaining
		log.Printf("forge syncer: rate limit budget floor reached for %s (%d remaining <= floor %d); backing off API calls until reset at %s", ref.String(), remaining, floor, reset.Format(time.RFC3339))
	}
}

// SyncRepoWithHint syncs a repository against its forge, merging an optional hint into the state.
func (s *Syncer) SyncRepoWithHint(ctx context.Context, ref forge.RepoRef, hint *Hint) error {
	if !ref.IsValid() {
		return fmt.Errorf("invalid repository reference: %v", ref)
	}

	// 1. Resolve forge
	f, err := s.resolveForge(ref)
	if err != nil {
		recordError(ref, "resolve")
		return fmt.Errorf("resolve forge for %s: %w", ref.String(), err)
	}

	// Register known clone URL if gitremote
	if s.store != nil {
		if repo, ok := s.store.GetRepository(ref); ok && repo != nil && repo.CloneURL != "" {
			if gr, ok := f.(*gitremote.Forge); ok {
				gr.RegisterURL(ref, repo.CloneURL)
			}
			if s.gitremote != nil {
				s.gitremote.RegisterURL(ref, repo.CloneURL)
			}
		}
	}
	repoCfg := s.getRepoConfig(ref)
	if repoCfg.CloneURL != "" {
		if gr, ok := f.(*gitremote.Forge); ok {
			gr.RegisterURL(ref, repoCfg.CloneURL)
		}
		if s.gitremote != nil {
			s.gitremote.RegisterURL(ref, repoCfg.CloneURL)
		}
	}

	// 2. Load previous state from store
	var prevBranches []forge.Branch
	var prevChanges []forge.ChangeRequest
	var allStoredChanges []forge.ChangeRequest
	if s.store != nil {
		storedBranches, bErr := s.store.ListBranches(ref)
		if bErr != nil {
			recordError(ref, "store_read")
			return fmt.Errorf("failed to list branches from store for %s: %w", ref.String(), bErr)
		}
		prevBranches = storedBranches
		prevChanges = s.store.GetOpenChangeRequests(ref)
		allStored, cErr := s.store.ListChangeRequests(ref)
		if cErr != nil {
			recordError(ref, "store_read")
			return fmt.Errorf("failed to list change requests from store for %s: %w", ref.String(), cErr)
		}
		allStoredChanges = allStored
	}

	// 3. Change detection via git ls-remote first
	rKey := strings.ToLower(ref.String())
	s.mu.RLock()
	lastRec := s.lastFullReconcile[rKey]
	fullRecInterval := s.fullReconcileInterval
	s.mu.RUnlock()

	var diff RefDiff
	var remoteRefs []*plumbing.Reference
	var lsErr error
	if s.gitremote != nil && (f.Kind() == "github.com" || f.Kind() == "gitremote") {
		remoteRefs, lsErr = s.gitremote.ListRefs(ctx, ref)
		if lsErr != nil && f.Kind() == "gitremote" {
			recordError(ref, "ls_remote")
			return fmt.Errorf("failed to list remote refs for %s: %w", ref.String(), lsErr)
		}
	}

	useLSRemote := (lsErr == nil && remoteRefs != nil)
	isFullRec := !useLSRemote || time.Since(lastRec) >= fullRecInterval || len(prevBranches) == 0

	if useLSRemote {
		diff = DiffRefs(prevBranches, allStoredChanges, remoteRefs)
		if !diff.HasChanges && !isFullRec && hint == nil {
			// No change in refs -> no API call at all!
			SyncsTotal.WithLabelValues(ref.String(), "success").Inc()
			return nil
		}
	}

	// 4. API fill-in throttle check
	if !isFullRec && diff.HasChanges && s.apiFillInInterval > 0 && hint == nil {
		s.mu.RLock()
		lastFillIn := s.lastAPIFillIn[rKey]
		s.mu.RUnlock()
		if time.Since(lastFillIn) < s.apiFillInInterval {
			// Throttle API fill-in until interval passes
			return nil
		}
	}

	// 5. Rate-limit budget check and client floor sync
	if crfs, ok := f.(interface{ Client() *gh.HTTPClient }); ok && crfs.Client() != nil {
		crfs.Client().SetRateFloorPercent(s.rateFloorPercent)
	}
	if rlr, ok := f.(RateLimitReporter); ok {
		rl := rlr.RateLimit()
		if rl.Limit > 0 {
			RateLimitRemaining.WithLabelValues(ref.Host).Set(float64(rl.Remaining))
			RateLimitLimit.WithLabelValues(ref.Host).Set(float64(rl.Limit))
			RateLimitReset.WithLabelValues(ref.Host).Set(float64(rl.Reset.Unix()))

			floor := rl.Limit * s.rateFloorPercent / 100
			if floor <= 0 {
				floor = 1
			}
			if rl.Remaining <= floor && time.Now().Before(rl.Reset) {
				s.logRateLimitBackoff(ref, rl.Remaining, floor, rl.Reset)
				// Back off API calls until reset, keep ls-remote polling
				return nil
			}
		}
	}

	// 6. Fetch repository metadata from store or forge
	var repoMeta *forge.Repository
	if s.store != nil {
		if r, ok := s.store.GetRepository(ref); ok && r != nil {
			repoMeta = r
		}
	}
	if repoMeta == nil || isFullRec {
		if r, rErr := f.Repository(ctx, ref); rErr == nil && r != nil {
			repoMeta = r
		} else if rErr != nil && !errors.Is(rErr, forge.ErrUnsupported) && !errors.Is(rErr, forge.ErrNotFound) {
			recordError(ref, "repository")
			return fmt.Errorf("failed to fetch repository %s: %w", ref.String(), rErr)
		}
	}

	// 7. Fetch branches from forge or ls-remote
	var branches []forge.Branch
	var branchesFetchSucceeded bool
	if useLSRemote && len(diff.AllBranches) > 0 && !isFullRec {
		branches = diff.AllBranches
		branchesFetchSucceeded = true
	} else {
		bList, bErr := f.Branches(ctx, ref)
		if bErr != nil {
			recordError(ref, "branches")
			return fmt.Errorf("failed to fetch branches for %s: %w", ref.String(), bErr)
		}
		branches = bList
		branchesFetchSucceeded = true
	}

	// 8. Fetch change requests from forge
	var crs []forge.ChangeRequest
	var crsFetchSucceeded bool
	if isFullRec {
		crList, cErr := f.ChangeRequests(ctx, ref)
		if cErr != nil && !errors.Is(cErr, forge.ErrUnsupported) {
			recordError(ref, "change_requests")
			return fmt.Errorf("failed to fetch change requests for %s: %w", ref.String(), cErr)
		}
		s.mu.Lock()
		s.lastFullReconcile[rKey] = time.Now()
		s.mu.Unlock()

		if cErr == nil {
			crs = crList
			crsFetchSucceeded = true
		} else if errors.Is(cErr, forge.ErrUnsupported) {
			crs = append([]forge.ChangeRequest(nil), prevChanges...)
		}
	} else if len(diff.OpenedPRs)+len(diff.SyncPRs) == 0 {
		// Fill-in tick with NO PR head changes (e.g. branch push or deletion):
		// Do not call ChangeRequests() or PullRequest()! Keep existing change requests.
		crs = append([]forge.ChangeRequest(nil), prevChanges...)
		crsFetchSucceeded = false
	} else if prg, ok := f.(PullRequestGetter); ok && useLSRemote {
		// API fill-in ONLY for changed PR refs
		crs = append([]forge.ChangeRequest(nil), prevChanges...)
		for _, num := range append(diff.OpenedPRs, diff.SyncPRs...) {
			cr, err := prg.PullRequest(ctx, ref, num)
			if err != nil {
				recordError(ref, "change_request_single")
				continue
			}
			if cr != nil {
				found := false
				for idx := range crs {
					if crs[idx].Number == num {
						crs[idx] = *cr
						found = true
						break
					}
				}
				if !found {
					crs = append(crs, *cr)
				}
			}
		}
		crsFetchSucceeded = false
	} else {
		crList, cErr := f.ChangeRequests(ctx, ref)
		if cErr != nil && !errors.Is(cErr, forge.ErrUnsupported) {
			recordError(ref, "change_requests")
			return fmt.Errorf("failed to fetch change requests for %s: %w", ref.String(), cErr)
		}
		if cErr == nil {
			crs = crList
			crsFetchSucceeded = true
		} else if errors.Is(cErr, forge.ErrUnsupported) && hint != nil {
			crs = append([]forge.ChangeRequest(nil), prevChanges...)
		}
	}

	s.mu.Lock()
	if s.lastAPIFillIn == nil {
		s.lastAPIFillIn = make(map[string]time.Time)
	}
	s.lastAPIFillIn[rKey] = time.Now()
	s.mu.Unlock()

	// 9. Merge hint if present (never merge into an empty list)
	if hint != nil {
		if hint.Branch != nil {
			var found bool
			for i, b := range branches {
				if b.Name == hint.Branch.Name {
					branches[i] = *hint.Branch
					found = true
					break
				}
			}
			if !found {
				branches = append(branches, *hint.Branch)
			}
		}
		if hint.ChangeRequest != nil {
			var found bool
			for i, cr := range crs {
				if (cr.Number > 0 && cr.Number == hint.ChangeRequest.Number) ||
					(cr.ID != "" && cr.ID == hint.ChangeRequest.ID) {
					crs[i] = *hint.ChangeRequest
					found = true
					break
				}
			}
			if !found {
				crs = append(crs, *hint.ChangeRequest)
			}
		}
	}

	if repoMeta == nil {
		repoMeta = &forge.Repository{
			Ref: ref,
		}
	}
	if repoCfg.CloneURL != "" {
		repoMeta.CloneURL = repoCfg.CloneURL
	} else if hint != nil && hint.CloneURL != "" {
		repoMeta.CloneURL = hint.CloneURL
	} else if repoMeta.CloneURL == "" && s.store != nil {
		if prevRepo, ok := s.store.GetRepository(ref); ok && prevRepo != nil && prevRepo.CloneURL != "" {
			repoMeta.CloneURL = prevRepo.CloneURL
		}
	}

	// 7. Diff branches
	prevBranchMap := make(map[string]string)
	for _, b := range prevBranches {
		prevBranchMap[b.Name] = b.SHA
	}

	var events []consumer.Event
	var commitsToFetch []string

	for _, b := range branches {
		oldSHA, exists := prevBranchMap[b.Name]
		if !exists || oldSHA != b.SHA {
			ev := consumer.BranchAdvancedEvent{
				Repo:     ref,
				Branch:   b.Name,
				OldSHA:   oldSHA,
				NewSHA:   b.SHA,
				CloneURL: repoMeta.CloneURL,
			}
			if hint != nil && hint.Branch != nil && hint.Branch.Name == b.Name && hint.CloneURL != "" {
				ev.CloneURL = hint.CloneURL
			}
			events = append(events, ev)
			commitsToFetch = append(commitsToFetch, b.SHA)
		}
	}

	// 8. Diff change requests
	prevCRMap := make(map[string]forge.ChangeRequest)
	for _, cr := range prevChanges {
		k := crKey(cr)
		prevCRMap[k] = cr
	}

	currentCRMap := make(map[string]forge.ChangeRequest)
	for _, cr := range crs {
		k := crKey(cr)
		currentCRMap[k] = cr

		prevCR, exists := prevCRMap[k]
		candCloneURL := repoMeta.CloneURL
		baseCloneURL := repoMeta.CloneURL
		if hint != nil {
			if hint.CandidateCloneURL != "" {
				candCloneURL = hint.CandidateCloneURL
			}
			if hint.BaselineCloneURL != "" {
				baseCloneURL = hint.BaselineCloneURL
			}
		}

		if !exists {
			if isCROpen(cr) {
				events = append(events, consumer.ChangeOpenedEvent{
					Repo:              ref,
					Change:            cr,
					CandidateCloneURL: candCloneURL,
					BaselineCloneURL:  baseCloneURL,
				})
				if cr.Head.SHA != "" {
					commitsToFetch = append(commitsToFetch, cr.Head.SHA)
				}
			}
		} else {
			if isCROpen(cr) {
				// Only head or base SHA changes emit ChangeUpdated (ignore title-only changes)
				if prevCR.Head.SHA != cr.Head.SHA || prevCR.Base.SHA != cr.Base.SHA {
					events = append(events, consumer.ChangeUpdatedEvent{
						Repo:              ref,
						OldHeadSHA:        prevCR.Head.SHA,
						Change:            cr,
						CandidateCloneURL: candCloneURL,
						BaselineCloneURL:  baseCloneURL,
					})
					if cr.Head.SHA != "" {
						commitsToFetch = append(commitsToFetch, cr.Head.SHA)
					}
				}
			} else if !isCROpen(cr) && isCROpen(prevCR) {
				events = append(events, consumer.ChangeClosedEvent{
					Repo:   ref,
					Change: cr,
				})
			}
		}
	}

	// Closed change requests that disappeared from forge
	// A ChangeClosed event is emitted ONLY when ChangeRequests succeeded for that repository in this sync
	// and the change is absent from the full list.
	if crsFetchSucceeded {
		for k, prevCR := range prevCRMap {
			if _, exists := currentCRMap[k]; !exists {
				if isCROpen(prevCR) {
					closedCR := prevCR
					closedCR.State = "closed"
					events = append(events, consumer.ChangeClosedEvent{
						Repo:   ref,
						Change: closedCR,
					})
					crs = append(crs, closedCR)
				}
			}
		}
	}

	// 9. Fetch commits for unknown SHAs
	var fetchedCommits []*forge.Commit
	for _, sha := range commitsToFetch {
		if sha == "" {
			continue
		}
		if s.store != nil {
			if _, ok := s.store.GetCommit(sha); ok {
				continue
			}
		}
		c, err := f.Commit(ctx, ref, sha)
		if err != nil {
			if !errors.Is(err, forge.ErrUnsupported) && !errors.Is(err, forge.ErrNotFound) {
				log.Printf("warning: error fetching commit %s for %s: %v", sha, ref.String(), err)
			}
			continue
		}
		if c != nil {
			fetchedCommits = append(fetchedCommits, c)
		}
	}

	// Attach commit info to BranchAdvancedEvents
	for i := range events {
		if ba, ok := events[i].(consumer.BranchAdvancedEvent); ok {
			for _, fc := range fetchedCommits {
				if fc.SHA == ba.NewSHA {
					ba.Commit = fc
					events[i] = ba
					break
				}
			}
			if ba.Commit == nil && s.store != nil {
				if sc, ok := s.store.GetCommit(ba.NewSHA); ok {
					ba.Commit = sc
					events[i] = ba
				}
			}
		}
	}

	// 10. CONSUME-BEFORE-COMMIT:
	// For each event, the consumer is called synchronously.
	// A consumer error skips the commit, is counted and returned so the next sync regenerates the event.
	s.mu.RLock()
	cons := s.consumer
	s.mu.RUnlock()

	if cons != nil {
		for _, ev := range events {
			EventsTotal.WithLabelValues(ref.String(), string(ev.EventKind())).Inc()
			if err := cons.ConsumeEvent(ctx, ev); err != nil {
				recordError(ref, "consumer")
				return fmt.Errorf("consumer failed on event %s for %s: %w", ev.EventKind(), ref.String(), err)
			}
		}
	}

	// 11. COMMIT TO STORE (only after all events were successfully consumed)
	if s.store != nil {
		batch := &store.SyncBatch{
			Repo:           repoMeta,
			Branches:       branches,
			FullBranchSync: branchesFetchSucceeded,
			Changes:        crs,
			Commits:        fetchedCommits,
			Sync: &store.SyncMeta{
				LastPolled: time.Now().UTC(),
			},
		}
		if epCollector, ok := f.(interface {
			PopPendingEndpointSyncs(ref forge.RepoRef) map[string]*store.EndpointSync
		}); ok {
			batch.EndpointSyncs = epCollector.PopPendingEndpointSyncs(ref)
		}
		if err := s.store.SaveSyncBatch(ctx, ref, batch); err != nil {
			recordError(ref, "store_write")
			return fmt.Errorf("failed to save sync batch for %s: %w", ref.String(), err)
		}
		sz, szErr := s.store.Size()
		if szErr != nil {
			recordError(ref, "store_size")
			return fmt.Errorf("failed to determine store size: %w", szErr)
		}
		StoreSizeBytes.WithLabelValues(s.store.Path()).Set(float64(sz))
	}

	SyncsTotal.WithLabelValues(ref.String(), "success").Inc()
	return nil
}

func crKey(cr forge.ChangeRequest) string {
	if cr.Number > 0 {
		return strconv.Itoa(cr.Number)
	}
	return string(cr.ID)
}

func isCROpen(cr forge.ChangeRequest) bool {
	st := strings.ToLower(strings.TrimSpace(cr.State))
	return st != "closed" && st != "merged"
}
