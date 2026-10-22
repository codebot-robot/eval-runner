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
)

// Syncer synchronizes configured repositories from their authoritative forges into
// the local bbolt store and notifies the event consumer.
type Syncer struct {
	mu       sync.RWMutex
	store    *store.Store
	registry *forge.Registry
	consumer EventHandler
	repos    []RepoConfig
}

// SyncerOptions configures a Syncer instance.
type SyncerOptions struct {
	Store        *store.Store
	Registry     *forge.Registry
	Consumer     EventHandler
	Repositories []RepoConfig
}

// New creates a new Syncer with the given options.
func New(opts SyncerOptions) *Syncer {
	s := &Syncer{
		store:    opts.Store,
		registry: opts.Registry,
		consumer: opts.Consumer,
		repos:    opts.Repositories,
	}
	return s
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

// UpdateRepositories updates the list of tracked repositories at runtime.
func (s *Syncer) UpdateRepositories(repos []RepoConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos = repos
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
		}
	}
	repoCfg := s.getRepoConfig(ref)
	if repoCfg.CloneURL != "" {
		if gr, ok := f.(*gitremote.Forge); ok {
			gr.RegisterURL(ref, repoCfg.CloneURL)
		}
	}

	// 2. Fetch repository metadata from forge
	var repoMeta *forge.Repository
	if r, rErr := f.Repository(ctx, ref); rErr == nil && r != nil {
		repoMeta = r
	} else if rErr != nil && !errors.Is(rErr, forge.ErrUnsupported) && !errors.Is(rErr, forge.ErrNotFound) {
		recordError(ref, "repository")
		return fmt.Errorf("failed to fetch repository %s: %w", ref.String(), rErr)
	}

	// 3. Load previous state from store
	var prevBranches []forge.Branch
	var prevChanges []forge.ChangeRequest
	if s.store != nil {
		storedBranches, bErr := s.store.ListBranches(ref)
		if bErr != nil {
			recordError(ref, "store_read")
			return fmt.Errorf("failed to list branches from store for %s: %w", ref.String(), bErr)
		}
		prevBranches = storedBranches
		prevChanges = s.store.GetOpenChangeRequests(ref)
	}

	// 4. Fetch branches from forge
	var branches []forge.Branch
	var branchesFetchSucceeded bool
	bList, bErr := f.Branches(ctx, ref)
	if bErr != nil {
		recordError(ref, "branches")
		return fmt.Errorf("failed to fetch branches for %s: %w", ref.String(), bErr)
	}
	branches = bList
	branchesFetchSucceeded = true

	// 5. Fetch change requests from forge
	var crs []forge.ChangeRequest
	var crsFetchSucceeded bool
	crList, cErr := f.ChangeRequests(ctx, ref)
	if cErr != nil && !errors.Is(cErr, forge.ErrUnsupported) {
		recordError(ref, "change_requests")
		return fmt.Errorf("failed to fetch change requests for %s: %w", ref.String(), cErr)
	}
	if cErr == nil {
		crs = crList
		crsFetchSucceeded = true
	} else if errors.Is(cErr, forge.ErrUnsupported) && hint != nil {
		// When ChangeRequests is unsupported, keep stored changes
		crs = append([]forge.ChangeRequest(nil), prevChanges...)
	}

	// 6. Merge hint if present (never merge into an empty list)
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
