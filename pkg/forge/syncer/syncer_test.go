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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/gke-labs/eval-runner/pkg/forge/consumer"
	"github.com/gke-labs/eval-runner/pkg/forge/gitremote"
	"github.com/gke-labs/eval-runner/pkg/forge/store"
	gh "github.com/gke-labs/eval-runner/pkg/github"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type recordingConsumer struct {
	mu        sync.Mutex
	events    []consumer.Event
	onConsume func(ev consumer.Event) error
}

func (r *recordingConsumer) ConsumeEvent(ctx context.Context, ev consumer.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.onConsume != nil {
		if err := r.onConsume(ev); err != nil {
			return err
		}
	}
	r.events = append(r.events, ev)
	return nil
}

func (r *recordingConsumer) getAndClearEvents() []consumer.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	evs := r.events
	r.events = nil
	return evs
}

func newTestSyncer(t *testing.T, ff *forge.FakeForge, cons EventHandler) (*Syncer, *store.Store) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "syncer-test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	reg := forge.NewRegistry(ff)

	s := New(SyncerOptions{
		Store:    st,
		Registry: reg,
		Consumer: cons,
	})
	return s, st
}

// TestSyncer_Sequence_OpenUpdateClose tests syncer sequences:
// open -> update -> close; re-sync emits nothing.
func TestSyncer_Sequence_OpenUpdateClose(t *testing.T) {
	ctx := t.Context()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	ff := forge.NewFakeForge("fake")
	ff.SetRepository(ref, &forge.Repository{
		Ref:           ref,
		DefaultBranch: "main",
	})
	ff.SetBranches(ref, []forge.Branch{
		{Name: "main", SHA: "0000000000000000000000000000000000000001"},
	})

	sha1 := "1111111111111111111111111111111111111111"
	sha2 := "2222222222222222222222222222222222222222"
	baseSHA := "0000000000000000000000000000000000000001"

	// 1. Initial State: Open PR #1
	ff.SetChangeRequests(ref, []forge.ChangeRequest{
		{
			ID:     "1",
			Number: 1,
			Title:  "feat: initial pr",
			State:  "open",
			Head:   forge.ChangeRequestHead{Ref: "feat", SHA: sha1},
			Base:   forge.ChangeRequestBase{Ref: "main", SHA: baseSHA},
		},
	})

	cons := &recordingConsumer{}
	s, st := newTestSyncer(t, ff, cons)

	// First sync -> BranchAdvanced + ChangeOpened
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("first SyncRepo failed: %v", err)
	}

	evs := cons.getAndClearEvents()
	var kinds []consumer.EventKind
	for _, e := range evs {
		kinds = append(kinds, e.EventKind())
	}
	if len(kinds) != 2 || kinds[0] != consumer.EventBranchAdvanced || kinds[1] != consumer.EventChangeOpened {
		t.Fatalf("expected [BranchAdvanced, ChangeOpened], got %v", kinds)
	}

	// Re-sync emits nothing
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("re-sync failed: %v", err)
	}
	evs = cons.getAndClearEvents()
	if len(evs) != 0 {
		t.Fatalf("expected re-sync to emit nothing, got %v", evs)
	}

	// 2. Update PR #1: new head SHA2
	ff.SetChangeRequests(ref, []forge.ChangeRequest{
		{
			ID:     "1",
			Number: 1,
			Title:  "feat: initial pr updated",
			State:  "open",
			Head:   forge.ChangeRequestHead{Ref: "feat", SHA: sha2},
			Base:   forge.ChangeRequestBase{Ref: "main", SHA: baseSHA},
		},
	})

	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("update SyncRepo failed: %v", err)
	}
	evs = cons.getAndClearEvents()
	if len(evs) != 1 || evs[0].EventKind() != consumer.EventChangeUpdated {
		t.Fatalf("expected [ChangeUpdated], got %v", evs)
	}

	// Re-sync emits nothing
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("re-sync after update failed: %v", err)
	}
	evs = cons.getAndClearEvents()
	if len(evs) != 0 {
		t.Fatalf("expected re-sync to emit nothing, got %v", evs)
	}

	// 3. Advance main branch
	newMainSHA := "0000000000000000000000000000000000000002"
	ff.SetBranches(ref, []forge.Branch{
		{Name: "main", SHA: newMainSHA},
	})
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("main advance SyncRepo failed: %v", err)
	}
	evs = cons.getAndClearEvents()
	if len(evs) != 1 || evs[0].EventKind() != consumer.EventBranchAdvanced {
		t.Fatalf("expected [BranchAdvanced], got %v", evs)
	}

	// 4. Close PR #1
	ff.SetChangeRequests(ref, []forge.ChangeRequest{
		{
			ID:     "1",
			Number: 1,
			Title:  "feat: initial pr updated",
			State:  "closed",
			Head:   forge.ChangeRequestHead{Ref: "feat", SHA: sha2},
			Base:   forge.ChangeRequestBase{Ref: "main", SHA: baseSHA},
		},
	})
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("close SyncRepo failed: %v", err)
	}
	evs = cons.getAndClearEvents()
	if len(evs) != 1 || evs[0].EventKind() != consumer.EventChangeClosed {
		t.Fatalf("expected [ChangeClosed], got %v", evs)
	}

	// Verify store has 0 open change requests
	openCRs := st.GetOpenChangeRequests(ref)
	if len(openCRs) != 0 {
		t.Fatalf("expected 0 open change requests in store after close, got %+v", openCRs)
	}

	// Re-sync emits nothing
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("re-sync after close failed: %v", err)
	}
	evs = cons.getAndClearEvents()
	if len(evs) != 0 {
		t.Fatalf("expected re-sync after close to emit nothing, got %v", evs)
	}
}

// TestSyncer_ConsumeBeforeCommit pins the consume-before-commit order:
// the test reads the store from inside the consumer and asserts commit has not happened yet.
// If the consumer fails, commit is skipped and the error is returned.
func TestSyncer_ConsumeBeforeCommit(t *testing.T) {
	ctx := t.Context()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	ff := forge.NewFakeForge("fake")
	ff.SetBranches(ref, []forge.Branch{
		{Name: "main", SHA: "0000000000000000000000000000000000000001"},
	})
	ff.SetChangeRequests(ref, []forge.ChangeRequest{})

	var insideConsumerStoreChecked bool
	failConsumer := true

	var stRef *store.Store
	cons := &recordingConsumer{
		onConsume: func(ev consumer.Event) error {
			// Read the store from inside the consumer: commit must NOT have happened yet!
			branches, err := stRef.ListBranches(ref)
			if err != nil {
				t.Fatalf("error listing branches from store inside consumer: %v", err)
			}
			if len(branches) > 0 {
				t.Fatalf("CRITICAL: store batch was already committed before consumer finished! (branches: %v)", branches)
			}
			insideConsumerStoreChecked = true

			if failConsumer {
				return errors.New("transient consumer apiserver error")
			}
			return nil
		},
	}

	s, st := newTestSyncer(t, ff, cons)
	stRef = st

	// 1. Sync while consumer fails: must skip commit and return error
	err := s.SyncRepo(ctx, ref)
	if err == nil {
		t.Fatalf("expected SyncRepo to fail when consumer fails, but got nil")
	}
	if !insideConsumerStoreChecked {
		t.Fatalf("consumer onConsume was never called")
	}

	// Verify store still has NOT committed the branch
	branchesAfterFail, _ := st.ListBranches(ref)
	if len(branchesAfterFail) != 0 {
		t.Fatalf("expected store to be empty after consumer failure, got %v", branchesAfterFail)
	}

	// 2. Next sync with healthy consumer: regenerates the event, succeeds and commits
	failConsumer = false
	insideConsumerStoreChecked = false
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("SyncRepo failed with healthy consumer: %v", err)
	}
	if !insideConsumerStoreChecked {
		t.Fatalf("consumer was not called on retry")
	}

	// Verify store has now committed the branch
	branchesAfterSuccess, _ := st.ListBranches(ref)
	if len(branchesAfterSuccess) != 1 || branchesAfterSuccess[0].Name != "main" {
		t.Fatalf("expected store to have committed main branch after success, got %v", branchesAfterSuccess)
	}
}

// TestSyncer_Hinted503_EmitsNothingAndCountsError verifies that forge errors during
// a hinted sync are returned and counted as errors, and no spurious ChangeClosed is emitted.
func TestSyncer_Hinted503_EmitsNothingAndCountsError(t *testing.T) {
	ctx := t.Context()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	ff := forge.NewFakeForge("fake")
	ff.SetBranches(ref, []forge.Branch{
		{Name: "main", SHA: "0000000000000000000000000000000000000001"},
	})

	// Pre-populate open PR #1..#5
	var initialCRs []forge.ChangeRequest
	for i := 1; i <= 5; i++ {
		initialCRs = append(initialCRs, forge.ChangeRequest{
			ID:     forge.ChangeRequestID(fmt.Sprintf("%d", i)),
			Number: i,
			Title:  "open pr",
			State:  "open",
			Head:   forge.ChangeRequestHead{Ref: "branch", SHA: "head-sha"},
			Base:   forge.ChangeRequestBase{Ref: "main", SHA: "0000000000000000000000000000000000000001"},
		})
	}
	ff.SetChangeRequests(ref, initialCRs)

	cons := &recordingConsumer{}
	s, st := newTestSyncer(t, ff, cons)

	// Initial sync to store PR #1..#5
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	cons.getAndClearEvents()
	if len(st.GetOpenChangeRequests(ref)) != 5 {
		t.Fatalf("expected 5 open change requests in store")
	}

	// Now forge starts returning 503 error on ChangeRequests
	err503 := errors.New("503 Service Unavailable")
	ff.ChangeReqsErr = err503

	initialErrors := testutil.ToFloat64(SyncsTotal.WithLabelValues(ref.String(), "error"))

	hintPR := forge.ChangeRequest{
		ID:     "6",
		Number: 6,
		Title:  "hint pr",
		State:  "open",
		Head:   forge.ChangeRequestHead{Ref: "hint", SHA: "hint-sha"},
		Base:   forge.ChangeRequestBase{Ref: "main", SHA: "0000000000000000000000000000000000000001"},
	}
	hint := &Hint{
		ChangeRequest: &hintPR,
	}

	err := s.SyncRepoWithHint(ctx, ref, hint)
	if err == nil {
		t.Fatalf("expected error on hinted 503 sync, got nil")
	}

	// Error counter must have incremented
	currentErrors := testutil.ToFloat64(SyncsTotal.WithLabelValues(ref.String(), "error"))
	if currentErrors <= initialErrors {
		t.Errorf("expected error counter to increment on 503, went from %f to %f", initialErrors, currentErrors)
	}

	// Assert NO ChangeClosed events emitted for PR #1..#5
	evs := cons.getAndClearEvents()
	for _, ev := range evs {
		if ev.EventKind() == consumer.EventChangeClosed {
			t.Fatalf("spurious ChangeClosed event emitted during 503 error: %v", ev)
		}
	}

	// Open changes in store must remain unchanged
	if len(st.GetOpenChangeRequests(ref)) != 5 {
		t.Errorf("expected store to retain 5 open change requests, got %d", len(st.GetOpenChangeRequests(ref)))
	}
}

// TestSyncer_ChangeRequests_ErrNotFound_ReturnsErrorAndPreservesStore verifies that
// forge.ErrNotFound from ChangeRequests is treated as an error (e.g. GitHub 404 on lost token access),
// increments error counter, never emits ChangeClosed, and preserves stored change requests.
func TestSyncer_ChangeRequests_ErrNotFound_ReturnsErrorAndPreservesStore(t *testing.T) {
	ctx := t.Context()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	ff := forge.NewFakeForge("fake")
	ff.SetBranches(ref, []forge.Branch{
		{Name: "main", SHA: "0000000000000000000000000000000000000001"},
	})

	initialCRs := []forge.ChangeRequest{
		{ID: "1", Number: 1, Title: "open pr 1", State: "open", Head: forge.ChangeRequestHead{SHA: "h1"}, Base: forge.ChangeRequestBase{SHA: "0001"}},
		{ID: "2", Number: 2, Title: "open pr 2", State: "open", Head: forge.ChangeRequestHead{SHA: "h2"}, Base: forge.ChangeRequestBase{SHA: "0001"}},
	}
	ff.SetChangeRequests(ref, initialCRs)

	cons := &recordingConsumer{}
	s, st := newTestSyncer(t, ff, cons)

	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	cons.getAndClearEvents()
	if len(st.GetOpenChangeRequests(ref)) != 2 {
		t.Fatalf("expected 2 open change requests in store")
	}

	// ChangeRequests now fails with ErrNotFound (e.g. 404 from lost permissions)
	ff.ChangeReqsErr = forge.ErrNotFound

	initialErrors := testutil.ToFloat64(SyncsTotal.WithLabelValues(ref.String(), "error"))

	err := s.SyncRepo(ctx, ref)
	if err == nil {
		t.Fatalf("expected error when ChangeRequests returns ErrNotFound, got nil")
	}

	currentErrors := testutil.ToFloat64(SyncsTotal.WithLabelValues(ref.String(), "error"))
	if currentErrors <= initialErrors {
		t.Errorf("expected error counter to increment on ErrNotFound, went from %f to %f", initialErrors, currentErrors)
	}

	evs := cons.getAndClearEvents()
	for _, ev := range evs {
		if ev.EventKind() == consumer.EventChangeClosed {
			t.Fatalf("spurious ChangeClosed emitted on ErrNotFound: %v", ev)
		}
	}

	if len(st.GetOpenChangeRequests(ref)) != 2 {
		t.Errorf("expected store to preserve 2 open change requests, got %d", len(st.GetOpenChangeRequests(ref)))
	}
}

// TestSyncer_DisappearedChangeRequests verifies that absent change requests emit ChangeClosed
// only when ChangeRequests succeeded.
func TestSyncer_DisappearedChangeRequests(t *testing.T) {
	ctx := t.Context()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	ff := forge.NewFakeForge("fake")
	ff.SetBranches(ref, []forge.Branch{{Name: "main", SHA: "0001"}})
	ff.SetChangeRequests(ref, []forge.ChangeRequest{
		{ID: "1", Number: 1, State: "open", Head: forge.ChangeRequestHead{SHA: "h1"}, Base: forge.ChangeRequestBase{SHA: "0001"}},
		{ID: "2", Number: 2, State: "open", Head: forge.ChangeRequestHead{SHA: "h2"}, Base: forge.ChangeRequestBase{SHA: "0001"}},
	})

	cons := &recordingConsumer{}
	s, st := newTestSyncer(t, ff, cons)

	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	cons.getAndClearEvents()

	// PR #2 disappears from forge list (only PR #1 returned)
	ff.SetChangeRequests(ref, []forge.ChangeRequest{
		{ID: "1", Number: 1, State: "open", Head: forge.ChangeRequestHead{SHA: "h1"}, Base: forge.ChangeRequestBase{SHA: "0001"}},
	})

	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("second sync failed: %v", err)
	}
	evs := cons.getAndClearEvents()
	if len(evs) != 1 || evs[0].EventKind() != consumer.EventChangeClosed {
		t.Fatalf("expected [ChangeClosed] for disappeared PR #2, got %v", evs)
	}
	closedEv := evs[0].(consumer.ChangeClosedEvent)
	if closedEv.Change.Number != 2 {
		t.Errorf("expected ChangeClosed for PR #2, got #%d", closedEv.Change.Number)
	}

	// PR #1 also disappears (empty list from forge is a real empty list)
	ff.SetChangeRequests(ref, []forge.ChangeRequest{})
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("third sync failed: %v", err)
	}
	evs = cons.getAndClearEvents()
	if len(evs) != 1 || evs[0].EventKind() != consumer.EventChangeClosed {
		t.Fatalf("expected [ChangeClosed] for disappeared PR #1, got %v", evs)
	}

	if len(st.GetOpenChangeRequests(ref)) != 0 {
		t.Errorf("expected 0 open change requests in store")
	}
}

// TestParseRepoConfigList verifies YAML list parsing, clone URL preservation, and invalid entry rejection.
func TestParseRepoConfigList(t *testing.T) {
	t.Run("ValidYAML", func(t *testing.T) {
		yamlInput := `
- name: gke-labs/eval-runner
  branches: [main, dev]
- name: git://git-server.eval-runner-sandbox.svc/docs-eval.git
  branches: [main]
`
		repos, err := ParseRepoConfigList(yamlInput)
		if err != nil {
			t.Fatalf("ParseRepoConfigList failed: %v", err)
		}
		if len(repos) != 2 {
			t.Fatalf("expected 2 repos, got %d", len(repos))
		}
		if repos[0].Ref.Owner != "gke-labs" || repos[0].Ref.Name != "eval-runner" {
			t.Errorf("unexpected ref: %+v", repos[0].Ref)
		}
		if repos[1].CloneURL != "git://git-server.eval-runner-sandbox.svc/docs-eval.git" {
			t.Errorf("expected clone URL preserved, got %q", repos[1].CloneURL)
		}
		if repos[1].Ref.Name != "docs-eval" {
			t.Errorf("unexpected ref for URL repo: %+v", repos[1].Ref)
		}
	})

	t.Run("CommaList_RejectedWithClearError", func(t *testing.T) {
		_, err := ParseRepoConfigList("gke-labs/eval-runner,other/repo")
		if err == nil {
			t.Fatalf("expected error for comma list, got nil")
		}
		if !strings.Contains(err.Error(), "expected a YAML list of {name, branches}") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("InvalidEntry_Nonsense_Rejected", func(t *testing.T) {
		yamlInput := `
- name: nonsense
`
		_, err := ParseRepoConfigList(yamlInput)
		if err == nil {
			t.Fatalf("expected error for nonsense repo name, got nil")
		}
		if !strings.Contains(err.Error(), "invalid repository \"nonsense\": invalid host, owner, or repo name") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("EmptyString_ReturnsNil", func(t *testing.T) {
		repos, err := ParseRepoConfigList("   \n  ")
		if err != nil || repos != nil {
			t.Errorf("expected nil, nil for empty string, got %v, %v", repos, err)
		}
	})
}

type countingFakeForge struct {
	mu          sync.Mutex
	calls       []string
	repoCalls   int
	branchCalls int
	changeCalls int
	commitCalls int
	prCalls     int

	branches      []forge.Branch
	changes       []forge.ChangeRequest
	commits       map[string]*forge.Commit
	rateLimitInfo gh.RateLimitInfo
}

func (f *countingFakeForge) recordCall(name string) {
	f.calls = append(f.calls, name)
}

func (f *countingFakeForge) getAndClearCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := make([]string, len(f.calls))
	copy(res, f.calls)
	f.calls = nil
	return res
}

func (f *countingFakeForge) Kind() string { return "github.com" }

func (f *countingFakeForge) Repository(ctx context.Context, ref forge.RepoRef) (*forge.Repository, error) {
	f.mu.Lock()
	f.repoCalls++
	f.recordCall("Repository")
	f.mu.Unlock()
	return &forge.Repository{Ref: ref, DefaultBranch: "main", CloneURL: "https://github.com/gke-labs/eval-runner.git"}, nil
}

func (f *countingFakeForge) Branches(ctx context.Context, ref forge.RepoRef) ([]forge.Branch, error) {
	f.mu.Lock()
	f.branchCalls++
	f.recordCall("Branches")
	defer f.mu.Unlock()
	return f.branches, nil
}

func (f *countingFakeForge) ChangeRequests(ctx context.Context, ref forge.RepoRef) ([]forge.ChangeRequest, error) {
	f.mu.Lock()
	f.changeCalls++
	f.recordCall("ChangeRequests")
	defer f.mu.Unlock()
	return f.changes, nil
}

func (f *countingFakeForge) Commit(ctx context.Context, ref forge.RepoRef, sha string) (*forge.Commit, error) {
	f.mu.Lock()
	f.commitCalls++
	short := sha
	if len(short) > 7 {
		short = short[:7]
	}
	f.recordCall(fmt.Sprintf("Commit(%s)", short))
	defer f.mu.Unlock()
	if f.commits != nil {
		if c, ok := f.commits[sha]; ok {
			return c, nil
		}
	}
	return &forge.Commit{SHA: sha, Title: "commit " + sha}, nil
}

func (f *countingFakeForge) PullRequest(ctx context.Context, ref forge.RepoRef, number int) (*forge.ChangeRequest, error) {
	f.mu.Lock()
	f.prCalls++
	f.recordCall(fmt.Sprintf("PullRequest(%d)", number))
	defer f.mu.Unlock()
	for _, cr := range f.changes {
		if cr.Number == number {
			return &cr, nil
		}
	}
	return nil, forge.ErrNotFound
}

func (f *countingFakeForge) RateLimit() gh.RateLimitInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rateLimitInfo
}

func (f *countingFakeForge) totalAPICalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repoCalls + f.branchCalls + f.changeCalls + f.commitCalls + f.prCalls
}

func TestSyncer_NoChangeZeroAPICalls(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "syncer-zero-calls-test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	currentRefs := []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "1111111111111111111111111111111111111111"),
		plumbing.NewReferenceFromStrings("refs/pull/10/head", "aaaa101010101010101010101010101010101010"),
		// Closed PR with surviving refs/pull/N/head (github.com retains head refs forever)
		plumbing.NewReferenceFromStrings("refs/pull/20/head", "bbbb202020202020202020202020202020202020"),
	}

	gr := gitremote.New(gitremote.WithRefLister(func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		return currentRefs, nil
	}))

	fake := &countingFakeForge{
		branches: []forge.Branch{
			{Name: "main", SHA: "1111111111111111111111111111111111111111"},
		},
		changes: []forge.ChangeRequest{
			{
				Number: 10,
				State:  "open",
				Head: forge.ChangeRequestHead{
					Ref: "feat-10",
					SHA: "aaaa101010101010101010101010101010101010",
				},
			},
			{
				Number: 20,
				State:  "closed",
				Head: forge.ChangeRequestHead{
					Ref: "feat-20",
					SHA: "bbbb202020202020202020202020202020202020",
				},
			},
		},
		commits: map[string]*forge.Commit{
			"1111111111111111111111111111111111111111": {
				SHA:   "1111111111111111111111111111111111111111",
				Title: "initial commit",
			},
			"aaaa101010101010101010101010101010101010": {
				SHA:   "aaaa101010101010101010101010101010101010",
				Title: "pr 10 commit",
			},
		},
	}

	reg := forge.NewRegistry(fake)
	reg.Register("github.com", fake)

	consumer := &recordingConsumer{}

	s := New(SyncerOptions{
		Store:                 st,
		Registry:              reg,
		Consumer:              consumer,
		Repositories:          []RepoConfig{{Ref: ref, Branches: []string{"main"}}},
		GitRemote:             gr,
		LsRemoteInterval:      60 * time.Second,
		FullReconcileInterval: 1 * time.Hour,
	})

	ctx := t.Context()

	// Tick 1: Initial sync populates the store from empty state
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("first sync failed: %v", err)
	}

	callsAfterFirstSync := fake.getAndClearCalls()
	if len(callsAfterFirstSync) == 0 {
		t.Fatalf("expected initial sync to make API calls to populate store, got 0")
	}

	// Tick 2: Second sync with identical refs from ls-remote (including surviving closed PR head ref)
	// INVARIANT: "no change → zero API calls"
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("second sync failed: %v", err)
	}

	callsAfterSecondSync := fake.getAndClearCalls()
	if len(callsAfterSecondSync) != 0 {
		t.Errorf("expected ZERO API calls when refs do not change, but got: %v", callsAfterSecondSync)
	}

	// Tick 3: Third sync with only tag noise added to ls-remote
	currentRefs = append(currentRefs, plumbing.NewReferenceFromStrings("refs/tags/v1.0.0", "1111111111111111111111111111111111111111"))
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("third sync failed: %v", err)
	}

	callsAfterThirdSync := fake.getAndClearCalls()
	if len(callsAfterThirdSync) != 0 {
		t.Errorf("expected ZERO API calls when only tag noise is added, but got: %v", callsAfterThirdSync)
	}
}

func TestSyncer_ExactAPICallSequencePerScenario(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "syncer-api-sequence-test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	currentRefs := []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "1111111111111111111111111111111111111111"),
	}

	gr := gitremote.New(gitremote.WithRefLister(func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		return currentRefs, nil
	}))

	fake := &countingFakeForge{
		branches: []forge.Branch{
			{Name: "main", SHA: "1111111111111111111111111111111111111111"},
		},
		changes: []forge.ChangeRequest{},
		commits: map[string]*forge.Commit{
			"1111111111111111111111111111111111111111": {
				SHA:   "1111111111111111111111111111111111111111",
				Title: "initial commit",
			},
		},
	}

	reg := forge.NewRegistry(fake)
	reg.Register("github.com", fake)

	s := New(SyncerOptions{
		Store:                 st,
		Registry:              reg,
		Repositories:          []RepoConfig{{Ref: ref, Branches: []string{"main"}}},
		GitRemote:             gr,
		LsRemoteInterval:      60 * time.Second,
		FullReconcileInterval: 1 * time.Hour,
	})

	ctx := t.Context()

	// 1. Initial cold-start sync (populates repo and initial state)
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	_ = fake.getAndClearCalls()

	// Scenario A: Unchanged tick -> EXACTLY 0 API calls
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("unchanged sync failed: %v", err)
	}
	if calls := fake.getAndClearCalls(); len(calls) != 0 {
		t.Errorf("Scenario A (unchanged): expected [] calls, got %v", calls)
	}

	// Scenario B: Branch push (main advances to sha2) -> EXACTLY 1 API call: Commit(sha2)
	// (Repository, Branches, and ChangeRequests are skipped on fill-in ticks)
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "2222222222222222222222222222222222222222"),
	}
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("branch push sync failed: %v", err)
	}
	wantB := []string{"Commit(2222222)"}
	if calls := fake.getAndClearCalls(); fmt.Sprintf("%v", calls) != fmt.Sprintf("%v", wantB) {
		t.Errorf("Scenario B (branch push): expected %v, got %v", wantB, calls)
	}

	// Scenario C: PR opened (new PR #30) -> EXACTLY 2 API calls: PullRequest(30) + Commit(sha3)
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "2222222222222222222222222222222222222222"),
		plumbing.NewReferenceFromStrings("refs/pull/30/head", "3333333333333333333333333333333333333333"),
	}
	fake.changes = append(fake.changes, forge.ChangeRequest{
		Number: 30,
		State:  "open",
		Head: forge.ChangeRequestHead{
			Ref: "feat-30",
			SHA: "3333333333333333333333333333333333333333",
		},
	})
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("pr opened sync failed: %v", err)
	}
	wantC := []string{"PullRequest(30)", "Commit(3333333)"}
	if calls := fake.getAndClearCalls(); fmt.Sprintf("%v", calls) != fmt.Sprintf("%v", wantC) {
		t.Errorf("Scenario C (PR opened): expected %v, got %v", wantC, calls)
	}

	// Scenario D: PR synchronized (PR #30 head moves to sha4) -> EXACTLY 2 API calls: PullRequest(30) + Commit(sha4)
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "2222222222222222222222222222222222222222"),
		plumbing.NewReferenceFromStrings("refs/pull/30/head", "4444444444444444444444444444444444444444"),
	}
	fake.changes[0].Head.SHA = "4444444444444444444444444444444444444444"
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("pr sync sync failed: %v", err)
	}
	wantD := []string{"PullRequest(30)", "Commit(4444444)"}
	if calls := fake.getAndClearCalls(); fmt.Sprintf("%v", calls) != fmt.Sprintf("%v", wantD) {
		t.Errorf("Scenario D (PR synchronized): expected %v, got %v", wantD, calls)
	}

	// Scenario E: Hourly full reconcile -> EXACTLY 3 conditional calls: Repository + Branches + ChangeRequests
	rKey := strings.ToLower(ref.String())
	s.mu.Lock()
	s.lastFullReconcile[rKey] = time.Now().Add(-2 * time.Hour) // force full reconcile
	s.mu.Unlock()

	fake.branches = []forge.Branch{{Name: "main", SHA: "2222222222222222222222222222222222222222"}}
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("full reconcile sync failed: %v", err)
	}
	wantE := []string{"Repository", "Branches", "ChangeRequests"}
	if calls := fake.getAndClearCalls(); fmt.Sprintf("%v", calls) != fmt.Sprintf("%v", wantE) {
		t.Errorf("Scenario E (hourly full reconcile): expected %v, got %v", wantE, calls)
	}
}

func TestSyncer_FailedConsumerDropsETag(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "syncer-failed-consumer-etag.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	currentRefs := []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "1111111111111111111111111111111111111111"),
		plumbing.NewReferenceFromStrings("refs/pull/9/head", "9999999999999999999999999999999999999999"),
	}
	gr := gitremote.New(gitremote.WithRefLister(func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		return currentRefs, nil
	}))

	fake := &countingFakeForge{
		branches: []forge.Branch{{Name: "main", SHA: "1111111111111111111111111111111111111111"}},
		changes: []forge.ChangeRequest{{
			Number: 9,
			State:  "open",
			Head:   forge.ChangeRequestHead{Ref: "feat-9", SHA: "9999999999999999999999999999999999999999"},
		}},
	}
	reg := forge.NewRegistry(fake)
	reg.Register("github.com", fake)

	// Consumer fails on ChangeOpened event!
	consumer := &recordingConsumer{
		onConsume: func(ev consumer.Event) error {
			if ev.EventKind() == consumer.EventChangeOpened {
				return errors.New("simulated consumer failure")
			}
			return nil
		},
	}

	s := New(SyncerOptions{
		Store:                 st,
		Registry:              reg,
		Consumer:              consumer,
		Repositories:          []RepoConfig{{Ref: ref, Branches: []string{"main"}}},
		GitRemote:             gr,
		LsRemoteInterval:      60 * time.Second,
		FullReconcileInterval: 1 * time.Hour,
	})

	ctx := t.Context()

	// Tick 1: Fails during consume-before-commit!
	err = s.SyncRepo(ctx, ref)
	if err == nil {
		t.Fatalf("expected sync to fail due to consumer failure, got nil")
	}

	// Verify that nothing was committed to store: no branches, no changes, no ETags!
	if branches, _ := st.ListBranches(ref); len(branches) != 0 {
		t.Errorf("expected 0 branches committed after failed consume, got %d", len(branches))
	}
	if openCRs := st.GetOpenChangeRequests(ref); len(openCRs) != 0 {
		t.Errorf("expected 0 open change requests after failed consume, got %d", len(openCRs))
	}
	if _, err := st.GetEndpointSync(ref, "/repos/gke-labs/eval-runner/pulls?state=open&per_page=100"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected endpoint sync ETag to be dropped after failed consume, got %v", err)
	}

	// Tick 2: Consumer succeeds!
	consumer.onConsume = nil
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("second sync after consumer fix failed: %v", err)
	}

	// Verify data is now committed!
	if branches, _ := st.ListBranches(ref); len(branches) != 1 {
		t.Errorf("expected 1 branch committed after successful consume, got %d", len(branches))
	}
	if openCRs := st.GetOpenChangeRequests(ref); len(openCRs) != 1 {
		t.Errorf("expected 1 open change request committed after successful consume, got %d", len(openCRs))
	}
}

func TestSyncer_RepoLoopLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "syncer-lifecycle-test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	var pollCount int
	var pollMu sync.Mutex
	gr := gitremote.New(gitremote.WithRefLister(func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		pollMu.Lock()
		pollCount++
		pollMu.Unlock()
		return []*plumbing.Reference{
			plumbing.NewReferenceFromStrings("refs/heads/main", "1111111111111111111111111111111111111111"),
		}, nil
	}))

	fake := &countingFakeForge{
		branches: []forge.Branch{{Name: "main", SHA: "1111111111111111111111111111111111111111"}},
	}
	reg := forge.NewRegistry(fake)
	reg.Register("github.com", fake)

	s := New(SyncerOptions{
		Store:                 st,
		Registry:              reg,
		Repositories:          []RepoConfig{{Ref: ref}},
		GitRemote:             gr,
		LsRemoteInterval:      1 * time.Hour, // long initial interval
		FullReconcileInterval: 1 * time.Hour,
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// 1. Start syncer in background
	go func() {
		_ = s.Start(ctx)
	}()

	// Wait for initial poll
	time.Sleep(50 * time.Millisecond)
	pollMu.Lock()
	initialPolls := pollCount
	pollMu.Unlock()
	if initialPolls == 0 {
		t.Fatalf("expected initial poll, got 0")
	}

	// 2. Re-arm timer with short interval (1h -> 50ms)
	s.UpdateIntervals(50*time.Millisecond, 0, 1*time.Hour)
	time.Sleep(200 * time.Millisecond)

	pollMu.Lock()
	pollsAfterRearm := pollCount
	pollMu.Unlock()
	if pollsAfterRearm <= initialPolls {
		t.Errorf("expected timer to re-arm immediately upon UpdateIntervals, but poll count did not advance: %d <= %d", pollsAfterRearm, initialPolls)
	}

	// 3. Remove repository -> loop canceled!
	s.UpdateRepositories(nil)
	time.Sleep(50 * time.Millisecond)

	pollMu.Lock()
	pollsAtRemoval := pollCount
	pollMu.Unlock()

	// Wait 150ms and assert no further polls occur
	time.Sleep(150 * time.Millisecond)
	pollMu.Lock()
	pollsAfterRemoval := pollCount
	pollMu.Unlock()
	if pollsAfterRemoval != pollsAtRemoval {
		t.Errorf("expected 0 polls after repo removal, but poll count advanced from %d to %d", pollsAtRemoval, pollsAfterRemoval)
	}

	// 4. Re-add repository -> single loop running (no duplicate!)
	s.UpdateRepositories([]RepoConfig{{Ref: ref}})
	time.Sleep(150 * time.Millisecond)

	s.mu.RLock()
	loopCount := len(s.repoLoops)
	s.mu.RUnlock()
	if loopCount != 1 {
		t.Errorf("expected exactly 1 loop running after re-add, got %d", loopCount)
	}
}

func TestSyncer_RateLimitBudgetFloor_Backoff(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "syncer-ratelimit-floor-test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	currentRefs := []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "1111111111111111111111111111111111111111"),
	}

	lsRemoteCalls := 0
	gr := gitremote.New(gitremote.WithRefLister(func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		lsRemoteCalls++
		return currentRefs, nil
	}))

	fake := &countingFakeForge{
		branches: []forge.Branch{
			{Name: "main", SHA: "1111111111111111111111111111111111111111"},
		},
		rateLimitInfo: gh.RateLimitInfo{
			Limit:     60,
			Remaining: 59, // normal limit initially
			Reset:     time.Now().Add(1 * time.Hour),
		},
	}

	reg := forge.NewRegistry(fake)
	reg.Register("github.com", fake)

	s := New(SyncerOptions{
		Store:                 st,
		Registry:              reg,
		Repositories:          []RepoConfig{{Ref: ref}},
		GitRemote:             gr,
		RateFloorPercent:      10, // 10% floor = 6
		LsRemoteInterval:      60 * time.Second,
		FullReconcileInterval: 1 * time.Hour,
	})

	ctx := t.Context()

	// 1. Initial sync to populate store
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	apiCallsBefore := fake.totalAPICalls()
	lsCallsBefore := lsRemoteCalls

	// 2. Simulate ref change: main branch advances to sha2
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "2222222222222222222222222222222222222222"),
	}
	fake.branches = []forge.Branch{
		{Name: "main", SHA: "2222222222222222222222222222222222222222"},
	}

	// 3. Drop rate limit below floor: Remaining 5 <= floor 6
	fake.mu.Lock()
	fake.rateLimitInfo = gh.RateLimitInfo{
		Limit:     60,
		Remaining: 5,
		Reset:     time.Now().Add(30 * time.Minute),
	}
	fake.mu.Unlock()

	// 4. Poll: ls-remote is executed, but API calls are backed off
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("poll during backoff returned error: %v", err)
	}

	if lsRemoteCalls != lsCallsBefore+1 {
		t.Errorf("expected ls-remote to still be called during backoff, got %d calls (was %d)", lsRemoteCalls, lsCallsBefore)
	}
	if fake.totalAPICalls() != apiCallsBefore {
		t.Errorf("expected API calls to back off when remaining is below floor, but got %d new API calls", fake.totalAPICalls()-apiCallsBefore)
	}

	// 5. Simulate rate limit window reset (Reset in the past)
	fake.mu.Lock()
	fake.rateLimitInfo = gh.RateLimitInfo{
		Limit:     60,
		Remaining: 60,
		Reset:     time.Now().Add(-1 * time.Second),
	}
	fake.mu.Unlock()

	// 6. Poll now proceeds with API fill-in!
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("poll after reset failed: %v", err)
	}

	if fake.totalAPICalls() <= apiCallsBefore {
		t.Errorf("expected API calls to resume after reset, got total %d (before: %d)", fake.totalAPICalls(), apiCallsBefore)
	}
}
