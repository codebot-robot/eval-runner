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
	"github.com/gke-labs/eval-runner/pkg/forge/store"
)

const (
	// DefaultEventBufferSize is the default buffer size for the events channel.
	DefaultEventBufferSize = 500
)

// EventHandler receives and processes typed forge events.
type EventHandler interface {
	ConsumeEvent(ctx context.Context, ev Event) error
}

// Syncer synchronizes configured repositories from a Forge into a local Store
// and emits typed events on changes.
type Syncer struct {
	store    *store.Store
	forge    forge.Forge
	registry *forge.Registry
	consumer EventHandler
	events   chan Event

	mu           sync.RWMutex
	repositories []RepoConfig
}

// SyncerOptions provides initialization options for Syncer.
type SyncerOptions struct {
	Store           *store.Store
	Forge           forge.Forge
	Registry        *forge.Registry
	Consumer        EventHandler
	Repositories    []RepoConfig
	EventBufferSize int
}

// New creates a new Syncer.
func New(opts SyncerOptions) (*Syncer, error) {
	if opts.Store == nil {
		return nil, errors.New("syncer requires a non-nil Store")
	}
	bufSize := opts.EventBufferSize
	if bufSize <= 0 {
		bufSize = DefaultEventBufferSize
	}

	return &Syncer{
		store:        opts.Store,
		forge:        opts.Forge,
		registry:     opts.Registry,
		consumer:     opts.Consumer,
		events:       make(chan Event, bufSize),
		repositories: opts.Repositories,
	}, nil
}

// SetConsumer sets the consumer that handles events synchronously before store commit.
func (s *Syncer) SetConsumer(c EventHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumer = c
}

// Events returns the channel of typed events emitted by the syncer.
func (s *Syncer) Events() <-chan Event {
	return s.events
}

// UpdateRepositories updates the list of configured repositories at runtime.
func (s *Syncer) UpdateRepositories(repos []RepoConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repositories = repos
}

// Repositories returns a copy of configured repositories.
func (s *Syncer) Repositories() []RepoConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RepoConfig, len(s.repositories))
	copy(out, s.repositories)
	return out
}

// SyncAll syncs all configured repositories against the forge.
func (s *Syncer) SyncAll(ctx context.Context) error {
	repos := s.Repositories()
	var errs []error
	for _, r := range repos {
		if r.Ref.IsZero() {
			ref, ok := parseRepoRefString(r.Name)
			if ok {
				r.Ref = ref
			}
		}
		if !r.Ref.IsZero() {
			if err := s.SyncRepo(ctx, r.Ref); err != nil {
				errs = append(errs, fmt.Errorf("sync %s: %w", r.Ref.String(), err))
			}
		}
	}
	return errors.Join(errs...)
}

// SyncRepo synchronizes a single repository from the forge into the store.
func (s *Syncer) SyncRepo(ctx context.Context, ref forge.RepoRef) error {
	return s.SyncRepoWithHint(ctx, ref, nil)
}

// SyncRepoWithHint synchronizes a single repository, optionally incorporating a hint.
func (s *Syncer) SyncRepoWithHint(ctx context.Context, ref forge.RepoRef, hint *Hint) error {
	f, err := s.resolveForge(ref)
	if err != nil && hint == nil {
		SyncsTotal.WithLabelValues(ref.String(), "error").Inc()
		return err
	}

	// 1. Read existing state from store
	prevBranches, bErr := s.store.ListBranches(ref)
	if bErr != nil {
		log.Printf("warning: unable to list previous branches for %s: %v", ref.String(), bErr)
	}
	prevBranchMap := make(map[string]string)
	for _, b := range prevBranches {
		prevBranchMap[b.Name] = b.SHA
	}

	prevChanges, crErr := s.store.ListChangeRequests(ref)
	if crErr != nil {
		log.Printf("warning: unable to list previous change requests for %s: %v", ref.String(), crErr)
	}
	prevChangeMap := make(map[string]forge.ChangeRequest)
	for _, cr := range prevChanges {
		prevChangeMap[changeKey(cr)] = cr
	}

	// 2. Fetch new state from forge (or hint fallback)
	var repoMeta *forge.Repository
	var branches []forge.Branch
	var crs []forge.ChangeRequest
	var fullCRFetch bool

	if f != nil {
		if rMeta, rErr := f.Repository(ctx, ref); rErr != nil {
			log.Printf("warning: unable to fetch repository metadata for %s: %v", ref.String(), rErr)
		} else {
			repoMeta = rMeta
		}

		bList, bErr := f.Branches(ctx, ref)
		if bErr != nil && hint == nil {
			SyncsTotal.WithLabelValues(ref.String(), "error").Inc()
			return fmt.Errorf("failed to fetch branches for %s: %w", ref.String(), bErr)
		}
		if bErr == nil {
			branches = bList
		}

		crList, cErr := f.ChangeRequests(ctx, ref)
		if cErr != nil && !errors.Is(cErr, forge.ErrUnsupported) && hint == nil {
			SyncsTotal.WithLabelValues(ref.String(), "error").Inc()
			return fmt.Errorf("failed to fetch change requests for %s: %w", ref.String(), cErr)
		}
		if cErr == nil && crList != nil {
			crs = crList
			fullCRFetch = true
		}
	}

	// Incorporate hint if present, merging into stored state rather than an empty list
	if hint != nil {
		if repoMeta == nil {
			cloneURL := hint.CloneURL
			if cloneURL == "" {
				cloneURL = BuildCloneURL(ref.Owner, ref.Name)
			}
			repoMeta = &forge.Repository{
				Ref:           ref,
				DefaultBranch: "main",
				CloneURL:      cloneURL,
			}
		}
		if len(branches) == 0 {
			branches = append([]forge.Branch(nil), prevBranches...)
		}
		if len(crs) == 0 {
			crs = append([]forge.ChangeRequest(nil), prevChanges...)
		}

		if hint.Branch != "" && hint.CommitSHA != "" {
			var found bool
			for i := range branches {
				if branches[i].Name == hint.Branch {
					branches[i].SHA = hint.CommitSHA
					found = true
					break
				}
			}
			if !found {
				branches = append(branches, forge.Branch{
					Name: hint.Branch,
					SHA:  hint.CommitSHA,
				})
			}
		}
		if hint.ChangeRequest != nil {
			k := changeKey(*hint.ChangeRequest)
			var found bool
			for i := range crs {
				if changeKey(crs[i]) == k {
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
			Ref:           ref,
			DefaultBranch: "main",
			CloneURL:      BuildCloneURLForRef(ref),
		}
	}
	if saveErr := s.store.SaveRepository(repoMeta); saveErr != nil {
		log.Printf("warning: unable to save repository metadata for %s: %v", ref.String(), saveErr)
	}

	// 3. Collect missing commits
	neededSHAs := make(map[string]bool)
	for _, b := range branches {
		if b.SHA != "" {
			neededSHAs[b.SHA] = true
		}
	}
	for _, cr := range crs {
		if cr.Head.SHA != "" {
			neededSHAs[cr.Head.SHA] = true
		}
		if cr.Base.SHA != "" {
			neededSHAs[cr.Base.SHA] = true
		}
		if cr.MergeSHA != "" {
			neededSHAs[cr.MergeSHA] = true
		}
	}

	var commitsToSave []*forge.Commit
	for sha := range neededSHAs {
		if c, ok := s.store.GetCommit(sha); ok && c != nil {
			continue
		}
		var commit *forge.Commit
		if f != nil {
			if c, cErr := f.Commit(ctx, ref, sha); cErr != nil {
				log.Printf("warning: unable to fetch commit %s for %s: %v", sha, ref.String(), cErr)
			} else {
				commit = c
			}
		}
		if commit == nil {
			commitTime := time.Now().UTC()
			if hint != nil && hint.CommitTime != "" {
				if t, parseErr := time.Parse(time.RFC3339, hint.CommitTime); parseErr != nil {
					log.Printf("warning: invalid commit time %q in hint: %v", hint.CommitTime, parseErr)
				} else {
					commitTime = t.UTC()
				}
			}
			commit = &forge.Commit{
				SHA:   sha,
				Time:  commitTime,
				Title: fmt.Sprintf("Commit %s", shortSHA(sha)),
			}
		}
		commitsToSave = append(commitsToSave, commit)
	}

	// 4. Compute typed events
	var eventsToEmit []Event

	// a) Branches
	for _, b := range branches {
		oldSHA, existed := prevBranchMap[b.Name]
		if !existed || oldSHA != b.SHA {
			commit, _ := s.store.GetCommit(b.SHA)
			if commit == nil {
				for _, c := range commitsToSave {
					if c.SHA == b.SHA {
						commit = c
						break
					}
				}
			}
			eventsToEmit = append(eventsToEmit, BranchAdvancedEvent{
				Repo:   ref,
				Branch: b.Name,
				OldSHA: oldSHA,
				NewSHA: b.SHA,
				Commit: commit,
			})
		}
	}

	// b) Change requests
	seenCRs := make(map[string]bool)
	for _, cr := range crs {
		k := changeKey(cr)
		seenCRs[k] = true
		prev, existed := prevChangeMap[k]
		if !existed {
			if isOpenChange(cr) {
				eventsToEmit = append(eventsToEmit, ChangeOpenedEvent{
					Repo:   ref,
					Change: cr,
				})
			}
		} else {
			wasOpen := isOpenChange(prev)
			isOpen := isOpenChange(cr)
			if wasOpen && !isOpen {
				eventsToEmit = append(eventsToEmit, ChangeClosedEvent{
					Repo:   ref,
					Change: cr,
				})
			} else if !wasOpen && isOpen {
				eventsToEmit = append(eventsToEmit, ChangeOpenedEvent{
					Repo:   ref,
					Change: cr,
				})
			} else if isOpen {
				if prev.Head.SHA != cr.Head.SHA || prev.Base.SHA != cr.Base.SHA {
					eventsToEmit = append(eventsToEmit, ChangeUpdatedEvent{
						Repo:       ref,
						OldHeadSHA: prev.Head.SHA,
						Change:     cr,
					})
				}
			}
		}
	}

	// c) Closed change requests that disappeared from forge
	// Only run when ChangeRequests was successfully fetched in this sync.
	if fullCRFetch {
		for prevK, prevCR := range prevChangeMap {
			if !seenCRs[prevK] && isOpenChange(prevCR) {
				closed := prevCR
				closed.State = "closed"
				eventsToEmit = append(eventsToEmit, ChangeClosedEvent{
					Repo:   ref,
					Change: closed,
				})
			}
		}
	}

	// 5. Consume events synchronously before committing the batch to the store.
	s.mu.RLock()
	consumer := s.consumer
	s.mu.RUnlock()

	for _, ev := range eventsToEmit {
		if consumer != nil {
			if err := consumer.ConsumeEvent(ctx, ev); err != nil {
				log.Printf("syncer: error consuming event %s for %s: %v", ev.EventKind(), ref.String(), err)
			}
		}
		s.emit(ev)
		EventsTotal.WithLabelValues(ref.String(), string(ev.EventKind())).Inc()
	}

	// 6. Batch write into store after events have been consumed.
	batch := &store.SyncBatch{
		Repo:           repoMeta,
		Branches:       branches,
		FullBranchSync: fullCRFetch,
		Changes:        crs,
		Commits:        commitsToSave,
		Sync: &store.SyncMeta{
			LastPolled: time.Now().UTC(),
		},
	}
	if err := s.store.SaveSyncBatch(ctx, ref, batch); err != nil {
		SyncsTotal.WithLabelValues(ref.String(), "error").Inc()
		return fmt.Errorf("failed to save sync batch: %w", err)
	}

	SyncsTotal.WithLabelValues(ref.String(), "success").Inc()
	if sz, szErr := s.store.Size(); szErr != nil {
		log.Printf("warning: unable to read store size for %s: %v", s.store.Path(), szErr)
	} else {
		StoreSizeBytes.WithLabelValues(s.store.Path()).Set(float64(sz))
	}

	return nil
}

func (s *Syncer) emit(ev Event) {
	select {
	case s.events <- ev:
	default:
		log.Printf("warning: syncer event queue full, dropping event %s for %s", ev.EventKind(), ev.Repository().String())
	}
}

func (s *Syncer) resolveForge(ref forge.RepoRef) (forge.Forge, error) {
	if s.forge != nil {
		return s.forge, nil
	}
	if s.registry != nil {
		f := s.registry.ForRepo(ref)
		if f != nil {
			return f, nil
		}
	}
	return nil, errors.New("no forge or registry configured")
}

func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

func changeKey(cr forge.ChangeRequest) string {
	if cr.Number > 0 {
		return strconv.Itoa(cr.Number)
	}
	return string(cr.ID)
}

func isOpenChange(cr forge.ChangeRequest) bool {
	st := strings.ToLower(strings.TrimSpace(cr.State))
	return st != "closed" && st != "merged"
}
