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

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/gke-labs/eval-runner/pkg/forge/consumer"
	"github.com/gke-labs/eval-runner/pkg/forge/gitremote"
	"github.com/gke-labs/eval-runner/pkg/forge/store"
	gh "github.com/gke-labs/eval-runner/pkg/github"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakek8s "sigs.k8s.io/controller-runtime/pkg/client/fake"
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

// TestSyncer_503_EmitsNothingAndCountsError verifies that forge errors during
// a sync are returned and counted as errors, and no spurious ChangeClosed is emitted.
func TestSyncer_503_EmitsNothingAndCountsError(t *testing.T) {
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

	err := s.SyncRepo(ctx, ref)
	if err == nil {
		t.Fatalf("expected error on 503 sync, got nil")
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
	mu           sync.Mutex
	calls        []string
	branches     []forge.Branch
	changes      []forge.ChangeRequest
	pullRequests map[int]*forge.ChangeRequest
	commits      map[string]*forge.Commit
	rateLimit    gh.RateLimitInfo
	pendingSyncs map[string]*store.EndpointSync
	clearedSyncs int
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
	f.recordCall("Repository")
	f.stageSyncLocked(ref, "/repos/"+ref.Owner+"/"+ref.Name, `"repo-etag"`)
	f.mu.Unlock()
	return &forge.Repository{Ref: ref, DefaultBranch: "main", CloneURL: "https://github.com/" + ref.Owner + "/" + ref.Name + ".git"}, nil
}

func (f *countingFakeForge) Branches(ctx context.Context, ref forge.RepoRef) ([]forge.Branch, error) {
	f.mu.Lock()
	f.recordCall("Branches")
	f.stageSyncLocked(ref, "/repos/"+ref.Owner+"/"+ref.Name+"/branches?per_page=100", `"branches-etag"`)
	branches := f.branches
	f.mu.Unlock()
	return branches, nil
}

func (f *countingFakeForge) ChangeRequests(ctx context.Context, ref forge.RepoRef) ([]forge.ChangeRequest, error) {
	f.mu.Lock()
	f.recordCall("ChangeRequests")
	f.stageSyncLocked(ref, "/repos/"+ref.Owner+"/"+ref.Name+"/pulls?state=open&per_page=100", `"pulls-etag"`)
	crs := f.changes
	f.mu.Unlock()
	return crs, nil
}

func (f *countingFakeForge) Commit(ctx context.Context, ref forge.RepoRef, sha string) (*forge.Commit, error) {
	f.mu.Lock()
	short := sha
	if len(short) > 7 {
		short = short[:7]
	}
	f.recordCall(fmt.Sprintf("Commit(%s)", short))
	f.stageSyncLocked(ref, "/repos/"+ref.Owner+"/"+ref.Name+"/commits/"+sha, fmt.Sprintf(`"commit-%s"`, short))
	c := f.commits[sha]
	f.mu.Unlock()
	if c != nil {
		return c, nil
	}
	return &forge.Commit{SHA: sha, Title: "commit " + short}, nil
}

func (f *countingFakeForge) PullRequest(ctx context.Context, ref forge.RepoRef, number int) (*forge.ChangeRequest, error) {
	f.mu.Lock()
	f.recordCall(fmt.Sprintf("PullRequest(%d)", number))
	f.stageSyncLocked(ref, fmt.Sprintf("/repos/%s/%s/pulls/%d", ref.Owner, ref.Name, number), fmt.Sprintf(`"pr-%d-etag"`, number))
	defer f.mu.Unlock()
	if f.pullRequests != nil {
		if cr, ok := f.pullRequests[number]; ok && cr != nil {
			crCopy := *cr
			return &crCopy, nil
		}
	}
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
	return f.rateLimit
}

func (f *countingFakeForge) stageSyncLocked(ref forge.RepoRef, endpoint, etag string) {
	if f.pendingSyncs == nil {
		f.pendingSyncs = make(map[string]*store.EndpointSync)
	}
	f.pendingSyncs[endpoint] = &store.EndpointSync{
		ETag: etag,
	}
}

func (f *countingFakeForge) PopPendingEndpointSyncs(ref forge.RepoRef) map[string]*store.EndpointSync {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.pendingSyncs
	f.pendingSyncs = nil
	return s
}

func (f *countingFakeForge) ClearPendingEndpointSyncs(ref forge.RepoRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pendingSyncs = nil
	f.clearedSyncs++
}

// TestSyncer_ZeroCalls_FakeReturnsOpenPRsOnly verifies requirement 1:
// The fake returns open PRs only (PR #10). ls-remote advertises open PR #10 along with
// surviving closed PR heads #20, #30, #40. First reconcile seeds placeholder closed CRs,
// and the tick after it makes ZERO API calls.
func TestSyncer_ZeroCalls_FakeReturnsOpenPRsOnly(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "zero-calls.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	currentRefs := []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "1111111111111111111111111111111111111111"),
		plumbing.NewReferenceFromStrings("refs/pull/10/head", "aaaa101010101010101010101010101010101010"),
		// Surviving closed PR heads retained forever by github.com
		plumbing.NewReferenceFromStrings("refs/pull/20/head", "bbbb202020202020202020202020202020202020"),
		plumbing.NewReferenceFromStrings("refs/pull/30/head", "cccc303030303030303030303030303030303030"),
		plumbing.NewReferenceFromStrings("refs/pull/40/head", "dddd404040404040404040404040404040404040"),
	}

	gr := gitremote.New(gitremote.WithRefLister(func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		return currentRefs, nil
	}))

	// Fake returns OPEN PRs ONLY (PR #10). Closed PRs #20, #30, #40 are NOT returned!
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
		},
		commits: map[string]*forge.Commit{
			"1111111111111111111111111111111111111111": {SHA: "1111111111111111111111111111111111111111"},
			"aaaa101010101010101010101010101010101010": {SHA: "aaaa101010101010101010101010101010101010"},
		},
	}

	reg := forge.NewRegistry(fake)
	reg.Register("github.com", fake)

	s := New(SyncerOptions{
		Store:                 st,
		Registry:              reg,
		GitRemoteForge:        gr,
		Repositories:          []RepoConfig{{Ref: ref, Branches: []string{"main"}}},
		LsRemoteInterval:      60 * time.Second,
		FullReconcileInterval: 1 * time.Hour,
	})

	ctx := t.Context()

	// Tick 1: First full reconcile seeds placeholder closed CRs for #20, #30, #40
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("first sync failed: %v", err)
	}
	callsTick1 := fake.getAndClearCalls()
	if len(callsTick1) == 0 {
		t.Fatalf("expected initial sync to make seeding calls, got 0")
	}

	// Verify that placeholder closed CRs were seeded in store
	allStored, err := st.ListChangeRequests(ref)
	if err != nil {
		t.Fatalf("ListChangeRequests failed: %v", err)
	}
	if len(allStored) != 4 {
		t.Fatalf("expected 4 change requests in store (1 open + 3 seeded closed), got %d: %+v", len(allStored), allStored)
	}

	// Tick 2: Second tick makes ZERO API calls!
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("second sync failed: %v", err)
	}
	callsTick2 := fake.getAndClearCalls()
	if len(callsTick2) != 0 {
		t.Errorf("expected ZERO API calls on tick after seeding, got: %v", callsTick2)
	}

	// Tick 3: Adding tag noise also produces ZERO API calls
	currentRefs = append(currentRefs, plumbing.NewReferenceFromStrings("refs/tags/v1.0.0", "1111111111111111111111111111111111111111"))
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("third sync failed: %v", err)
	}
	callsTick3 := fake.getAndClearCalls()
	if len(callsTick3) != 0 {
		t.Errorf("expected ZERO API calls when tags added, got: %v", callsTick3)
	}
}

// TestSyncer_ExactAPICallSequencePerScenario asserts the exact call sequence for every scenario:
// unchanged tick = []; seeding tick; branch push = [Commit]; new branch; branch deleted;
// PR opened; PR synchronised; closed PR head moved; hourly reconcile.
// Asserts no Repository() and no full /pulls list on fill-in ticks.
func TestSyncer_ExactAPICallSequencePerScenario(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "call-sequence.db")
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
		commits: map[string]*forge.Commit{
			"1111111111111111111111111111111111111111": {SHA: "1111111111111111111111111111111111111111"},
		},
	}

	reg := forge.NewRegistry(fake)
	reg.Register("github.com", fake)

	s := New(SyncerOptions{
		Store:                 st,
		Registry:              reg,
		GitRemoteForge:        gr,
		Repositories:          []RepoConfig{{Ref: ref, Branches: []string{"main"}}},
		LsRemoteInterval:      60 * time.Second,
		FullReconcileInterval: 1 * time.Hour,
	})

	ctx := t.Context()

	// 1. Scenario: Seeding tick
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("seeding sync failed: %v", err)
	}
	wantSeeding := []string{"Repository", "Branches", "ChangeRequests", "Commit(1111111)"}
	if calls := fake.getAndClearCalls(); fmt.Sprintf("%v", calls) != fmt.Sprintf("%v", wantSeeding) {
		t.Errorf("Scenario seeding tick: expected %v, got %v", wantSeeding, calls)
	}

	// 2. Scenario: Unchanged tick = []
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("unchanged sync failed: %v", err)
	}
	if calls := fake.getAndClearCalls(); len(calls) != 0 {
		t.Errorf("Scenario unchanged tick: expected [], got %v", calls)
	}

	// 3. Scenario: Branch push = [Commit]
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "2222222222222222222222222222222222222222"),
	}
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("branch push sync failed: %v", err)
	}
	wantPush := []string{"Commit(2222222)"}
	if calls := fake.getAndClearCalls(); fmt.Sprintf("%v", calls) != fmt.Sprintf("%v", wantPush) {
		t.Errorf("Scenario branch push: expected %v, got %v", wantPush, calls)
	}

	// 4. Scenario: New branch = [Commit]
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "2222222222222222222222222222222222222222"),
		plumbing.NewReferenceFromStrings("refs/heads/topic", "3333333333333333333333333333333333333333"),
	}
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("new branch sync failed: %v", err)
	}
	wantNewBranch := []string{"Commit(3333333)"}
	if calls := fake.getAndClearCalls(); fmt.Sprintf("%v", calls) != fmt.Sprintf("%v", wantNewBranch) {
		t.Errorf("Scenario new branch: expected %v, got %v", wantNewBranch, calls)
	}

	// 5. Scenario: Branch deleted = []
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "2222222222222222222222222222222222222222"),
	}
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("branch deleted sync failed: %v", err)
	}
	if calls := fake.getAndClearCalls(); len(calls) != 0 {
		t.Errorf("Scenario branch deleted: expected [], got %v", calls)
	}

	// 6. Scenario: PR opened = [PullRequest, Commit]
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "2222222222222222222222222222222222222222"),
		plumbing.NewReferenceFromStrings("refs/pull/50/head", "5555555555555555555555555555555555555555"),
	}
	fake.changes = append(fake.changes, forge.ChangeRequest{
		Number: 50,
		State:  "open",
		Head: forge.ChangeRequestHead{
			Ref: "feat-50",
			SHA: "5555555555555555555555555555555555555555",
		},
	})
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("PR opened sync failed: %v", err)
	}
	wantPROpened := []string{"PullRequest(50)", "Commit(5555555)"}
	if calls := fake.getAndClearCalls(); fmt.Sprintf("%v", calls) != fmt.Sprintf("%v", wantPROpened) {
		t.Errorf("Scenario PR opened: expected %v, got %v", wantPROpened, calls)
	}

	// 7. Scenario: PR synchronised = [PullRequest, Commit]
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "2222222222222222222222222222222222222222"),
		plumbing.NewReferenceFromStrings("refs/pull/50/head", "6666666666666666666666666666666666666666"),
	}
	fake.changes[0].Head.SHA = "6666666666666666666666666666666666666666"
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("PR sync sync failed: %v", err)
	}
	wantPRSync := []string{"PullRequest(50)", "Commit(6666666)"}
	if calls := fake.getAndClearCalls(); fmt.Sprintf("%v", calls) != fmt.Sprintf("%v", wantPRSync) {
		t.Errorf("Scenario PR synchronised: expected %v, got %v", wantPRSync, calls)
	}

	// 8. Scenario: Closed PR head moved = [PullRequest, Commit]
	fake.changes[0].State = "closed"
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "2222222222222222222222222222222222222222"),
		plumbing.NewReferenceFromStrings("refs/pull/50/head", "7777777777777777777777777777777777777777"),
	}
	fake.changes[0].Head.SHA = "7777777777777777777777777777777777777777"
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("closed PR head moved sync failed: %v", err)
	}
	wantClosedMoved := []string{"PullRequest(50)", "Commit(7777777)"}
	if calls := fake.getAndClearCalls(); fmt.Sprintf("%v", calls) != fmt.Sprintf("%v", wantClosedMoved) {
		t.Errorf("Scenario closed PR head moved: expected %v, got %v", wantClosedMoved, calls)
	}

	// 9. Scenario: Hourly reconcile = [Repository, Branches, ChangeRequests]
	rKey := strings.ToLower(ref.String())
	s.mu.Lock()
	s.lastFullReconcile[rKey] = time.Now().Add(-2 * time.Hour) // force full reconcile
	s.mu.Unlock()
	fake.branches = []forge.Branch{{Name: "main", SHA: "2222222222222222222222222222222222222222"}}
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("hourly reconcile failed: %v", err)
	}
	wantHourly := []string{"Repository", "Branches", "ChangeRequests"}
	if calls := fake.getAndClearCalls(); fmt.Sprintf("%v", calls) != fmt.Sprintf("%v", wantHourly) {
		t.Errorf("Scenario hourly reconcile: expected %v, got %v", wantHourly, calls)
	}
}

// TestSyncer_ValidatorsCommittedWithBatch_ConsumerFailureDropsValidators verifies requirement 2:
// Validators (ETag, Last-Modified) travel with the batch.
// A tick whose consumer fails persists no validator, and the next tick refetches fully.
func TestSyncer_ValidatorsCommittedWithBatch_ConsumerFailureDropsValidators(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "validators-batch.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	currentRefs := []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "1111111111111111111111111111111111111111"),
		plumbing.NewReferenceFromStrings("refs/pull/7/head", "7777777777777777777777777777777777777777"),
	}
	gr := gitremote.New(gitremote.WithRefLister(func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		return currentRefs, nil
	}))

	fake := &countingFakeForge{
		branches: []forge.Branch{{Name: "main", SHA: "1111111111111111111111111111111111111111"}},
		changes: []forge.ChangeRequest{{
			Number: 7,
			State:  "open",
			Head:   forge.ChangeRequestHead{Ref: "feat-7", SHA: "7777777777777777777777777777777777777777"},
		}},
	}
	reg := forge.NewRegistry(fake)
	reg.Register("github.com", fake)

	// Consumer fails on ChangeOpened event
	failConsumer := true
	cons := &recordingConsumer{
		onConsume: func(ev consumer.Event) error {
			if failConsumer && ev.EventKind() == consumer.EventChangeOpened {
				return errors.New("simulated transient consumer error")
			}
			return nil
		},
	}

	s := New(SyncerOptions{
		Store:                 st,
		Registry:              reg,
		GitRemoteForge:        gr,
		Consumer:              cons,
		Repositories:          []RepoConfig{{Ref: ref, Branches: []string{"main"}}},
		LsRemoteInterval:      60 * time.Second,
		FullReconcileInterval: 1 * time.Hour,
	})

	ctx := t.Context()

	// Tick 1: Consumer fails!
	err = s.SyncRepo(ctx, ref)
	if err == nil {
		t.Fatalf("expected tick 1 to fail when consumer fails, got nil")
	}

	// Assert NO validator persisted in store
	epPulls, err := st.GetEndpointSync(ref, "/repos/gke-labs/eval-runner/pulls?state=open&per_page=100")
	if !errors.Is(err, store.ErrNotFound) || epPulls != nil {
		t.Errorf("expected no validator persisted after consumer failure, got: %+v (err: %v)", epPulls, err)
	}
	if fake.clearedSyncs != 1 {
		t.Errorf("expected pending validators to be cleared on forge, clearedSyncs=%d", fake.clearedSyncs)
	}

	// Tick 2: Consumer succeeds!
	failConsumer = false
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("second sync after consumer fix failed: %v", err)
	}

	// Assert validator IS now persisted in store
	epPullsAfter, err := st.GetEndpointSync(ref, "/repos/gke-labs/eval-runner/pulls?state=open&per_page=100")
	if err != nil || epPullsAfter == nil || epPullsAfter.ETag != `"pulls-etag"` {
		t.Fatalf("expected validator to be persisted after successful consumer, got: %+v (err: %v)", epPullsAfter, err)
	}
}

// TestSyncer_RateLimitBudgetFloor_BackoffAndOneLogPerStateTransition verifies requirement 4:
// Below configurable floor (default 10%), API calls are suppressed until reset while ls-remote continues.
// Exactly one log line is emitted per state transition.
func TestSyncer_RateLimitBudgetFloor_BackoffAndOneLogPerStateTransition(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "rate-limit-floor.db")
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
		branches: []forge.Branch{{Name: "main", SHA: "1111111111111111111111111111111111111111"}},
		rateLimit: gh.RateLimitInfo{
			Limit:     100,
			Remaining: 50, // normal state initially
			Reset:     time.Now().Add(1 * time.Hour),
		},
	}

	reg := forge.NewRegistry(fake)
	reg.Register("github.com", fake)

	var logMu sync.Mutex
	var logs []string
	logger := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	s := New(SyncerOptions{
		Store:                 st,
		Registry:              reg,
		GitRemoteForge:        gr,
		Repositories:          []RepoConfig{{Ref: ref}},
		RateLimitFloorPercent: 10, // floor = 10% of 100 = 10
		LsRemoteInterval:      60 * time.Second,
		FullReconcileInterval: 1 * time.Hour,
		Logger:                logger,
	})

	ctx := t.Context()

	// 1. Initial sync to populate store
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	_ = fake.getAndClearCalls()
	logMu.Lock()
	if len(logs) != 0 {
		t.Fatalf("expected 0 state transition logs in normal state, got %v", logs)
	}
	logMu.Unlock()

	// 2. Ref changes: branch advances to 2222...
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "2222222222222222222222222222222222222222"),
	}

	// Drop rate limit below floor: 8 <= 10
	fake.mu.Lock()
	fake.rateLimit = gh.RateLimitInfo{
		Limit:     100,
		Remaining: 8,
		Reset:     time.Now().Add(30 * time.Minute),
	}
	fake.mu.Unlock()

	// Tick 2: API calls must be suppressed, ls-remote continues, exactly 1 log line emitted
	lsCallsBefore := lsRemoteCalls
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("sync during suppression returned error: %v", err)
	}

	if lsRemoteCalls != lsCallsBefore+1 {
		t.Errorf("expected ls-remote to still run during suppression, got %d (was %d)", lsRemoteCalls, lsCallsBefore)
	}
	if calls := fake.getAndClearCalls(); len(calls) != 0 {
		t.Errorf("expected API calls to be suppressed, got %v", calls)
	}

	logMu.Lock()
	if len(logs) != 1 || !strings.Contains(logs[0], "budget floor reached") {
		t.Fatalf("expected exactly 1 log line for floor reached, got: %v", logs)
	}
	logMu.Unlock()

	// Tick 3: Second tick while still below floor -> API calls still suppressed, NO duplicate log!
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "3333333333333333333333333333333333333333"),
	}
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("sync 3 returned error: %v", err)
	}
	if calls := fake.getAndClearCalls(); len(calls) != 0 {
		t.Errorf("expected API calls to be suppressed on tick 3, got %v", calls)
	}
	logMu.Lock()
	if len(logs) != 1 {
		t.Fatalf("expected still exactly 1 log line (no duplicate logs while suppressed), got %d: %v", len(logs), logs)
	}
	logMu.Unlock()

	// Tick 4: Rate limit recovers (remaining 80 > floor 10) -> transition to normal: exactly 1 log line
	fake.mu.Lock()
	fake.rateLimit = gh.RateLimitInfo{
		Limit:     100,
		Remaining: 80,
		Reset:     time.Now().Add(1 * time.Hour),
	}
	fake.mu.Unlock()

	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("sync 4 returned error: %v", err)
	}
	if calls := fake.getAndClearCalls(); len(calls) == 0 {
		t.Errorf("expected API calls to resume after rate limit recovery, got 0")
	}

	logMu.Lock()
	if len(logs) != 2 || !strings.Contains(logs[1], "budget recovered") {
		t.Fatalf("expected exactly 2 state transition logs total, got: %v", logs)
	}
	if !strings.Contains(logs[1], "80 remaining > 10 floor of 100") {
		t.Errorf("expected recovery log to contain '80 remaining > 10 floor of 100', got: %q", logs[1])
	}
	logMu.Unlock()
}

// TestSyncer_RateLimitBudgetRecovery_WindowResetPassed verifies that when rate limit suppression
// ends because the reset window has passed, the recovery log reports "window reset passed at <reset>"
// rather than printing a stale pre-reset remaining count.
func TestSyncer_RateLimitBudgetRecovery_WindowResetPassed(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "rate-limit-reset.db")
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

	resetTime := time.Date(2026, 10, 23, 3, 24, 0, 0, time.UTC)
	fake := &countingFakeForge{
		branches: []forge.Branch{{Name: "main", SHA: "1111111111111111111111111111111111111111"}},
		rateLimit: gh.RateLimitInfo{
			Limit:     60,
			Remaining: 50,
			Reset:     time.Now().Add(1 * time.Hour),
		},
	}

	reg := forge.NewRegistry(fake)
	reg.Register("github.com", fake)

	var logMu sync.Mutex
	var logs []string
	logger := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	s := New(SyncerOptions{
		Store:                 st,
		Registry:              reg,
		GitRemoteForge:        gr,
		Repositories:          []RepoConfig{{Ref: ref}},
		RateLimitFloorPercent: 10, // floor = 10% of 60 = 6
		LsRemoteInterval:      60 * time.Second,
		FullReconcileInterval: 1 * time.Hour,
		Logger:                logger,
	})

	ctx := t.Context()

	// 1. Initial sync to populate store
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	_ = fake.getAndClearCalls()

	// 2. Ref changes: branch advances to 2222...
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "2222222222222222222222222222222222222222"),
	}

	// Drop rate limit below floor: 5 <= 6, reset in the future
	futureReset := time.Now().Add(30 * time.Minute)
	fake.mu.Lock()
	fake.rateLimit = gh.RateLimitInfo{
		Limit:     60,
		Remaining: 5,
		Reset:     futureReset,
	}
	fake.mu.Unlock()

	// Tick 2: API calls must be suppressed, 1 floor reached log emitted
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("sync during suppression returned error: %v", err)
	}
	if calls := fake.getAndClearCalls(); len(calls) != 0 {
		t.Errorf("expected API calls to be suppressed, got %v", calls)
	}

	logMu.Lock()
	if len(logs) != 1 || !strings.Contains(logs[0], "budget floor reached") {
		t.Fatalf("expected 1 log line for floor reached, got: %v", logs)
	}
	logMu.Unlock()

	// Tick 3: Window reset passes (reset time now in the past), but remaining is still 5 (stale pre-reset count <= 6)
	fake.mu.Lock()
	fake.rateLimit = gh.RateLimitInfo{
		Limit:     60,
		Remaining: 5,
		Reset:     resetTime,
	}
	fake.mu.Unlock()

	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("sync after reset passed returned error: %v", err)
	}
	if calls := fake.getAndClearCalls(); len(calls) == 0 {
		t.Errorf("expected API calls to resume after window reset passed, got 0")
	}

	logMu.Lock()
	if len(logs) != 2 || !strings.Contains(logs[1], "budget recovered") {
		t.Fatalf("expected exactly 2 state transition logs total, got: %v", logs)
	}
	expectedMsg := fmt.Sprintf("forge rate limit budget recovered for %s: window reset passed at %s; resuming API calls", ref.String(), resetTime.Format(time.RFC3339))
	if logs[1] != expectedMsg {
		t.Errorf("expected recovery log %q, got %q", expectedMsg, logs[1])
	}
	if strings.Contains(logs[1], "remaining >") {
		t.Errorf("recovery log should not mention 'remaining >', got: %q", logs[1])
	}
	logMu.Unlock()
}

// TestSyncer_RepoLoopLifecycle_TimerRearmAndCancel verifies requirement 5:
// runtime-reloadable intervals re-arm running timers; loops cancelled on removal, never duplicated on re-add.
func TestSyncer_RepoLoopLifecycle_TimerRearmAndCancel(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "syncer-lifecycle.db")
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
		GitRemoteForge:        gr,
		Repositories:          []RepoConfig{{Ref: ref}},
		LsRemoteInterval:      1 * time.Hour, // long initial interval
		FullReconcileInterval: 1 * time.Hour,
	})

	ctx, cancel := context.WithCancel(t.Context())
	startDone := make(chan struct{})
	go func() {
		_ = s.Start(ctx)
		close(startDone)
	}()
	defer func() {
		cancel()
		<-startDone
	}()

	// Wait for initial poll
	time.Sleep(50 * time.Millisecond)
	pollMu.Lock()
	initialPolls := pollCount
	pollMu.Unlock()
	if initialPolls == 0 {
		t.Fatalf("expected initial poll, got 0")
	}

	// 1. Re-arm running timer with short interval (1h -> 50ms)
	s.UpdateConfig([]RepoConfig{{Ref: ref}}, 50*time.Millisecond, 1*time.Hour, 10)
	time.Sleep(200 * time.Millisecond)

	pollMu.Lock()
	pollsAfterRearm := pollCount
	pollMu.Unlock()
	if pollsAfterRearm <= initialPolls {
		t.Errorf("expected timer to re-arm immediately upon UpdateConfig: %d <= %d", pollsAfterRearm, initialPolls)
	}

	// 2. Remove repository -> loop cancelled!
	s.UpdateConfig(nil, 50*time.Millisecond, 1*time.Hour, 10)
	time.Sleep(50 * time.Millisecond)

	pollMu.Lock()
	pollsAtRemoval := pollCount
	pollMu.Unlock()

	time.Sleep(150 * time.Millisecond)
	pollMu.Lock()
	pollsAfterRemoval := pollCount
	pollMu.Unlock()
	if pollsAfterRemoval != pollsAtRemoval {
		t.Errorf("expected 0 polls after repo removal, but poll count advanced: %d -> %d", pollsAtRemoval, pollsAfterRemoval)
	}

	// 3. Re-add repository -> single loop running, no duplicate
	s.UpdateConfig([]RepoConfig{{Ref: ref}}, 50*time.Millisecond, 1*time.Hour, 10)
	time.Sleep(100 * time.Millisecond)

	s.mu.RLock()
	loopCount := len(s.repoLoops)
	s.mu.RUnlock()
	if loopCount != 1 {
		t.Errorf("expected exactly 1 loop running after re-add, got %d", loopCount)
	}
}

// TestSyncer_ClosedPRHeadMoved_EmitsNoEvent verifies that when a surviving pull head
// for a stored-closed PR moves, the fill-in tick updates the stored SHA but emits
// NO ChangeUpdatedEvent. If the PR is subsequently re-opened, ChangeOpenedEvent is emitted.
func TestSyncer_ClosedPRHeadMoved_EmitsNoEvent(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "closed-pr-head-moved.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	currentRefs := []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "1111111111111111111111111111111111111111"),
		plumbing.NewReferenceFromStrings("refs/pull/20/head", "2222222222222222222222222222222222222222"),
	}
	gr := gitremote.New(gitremote.WithRefLister(func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		return currentRefs, nil
	}))

	fake := &countingFakeForge{
		branches: []forge.Branch{{Name: "main", SHA: "1111111111111111111111111111111111111111"}},
		// PR #20 is closed on GitHub
		changes: []forge.ChangeRequest{{
			Number: 20,
			State:  "closed",
			Head:   forge.ChangeRequestHead{Ref: "feat-20", SHA: "2222222222222222222222222222222222222222"},
		}},
	}
	reg := forge.NewRegistry(fake)
	reg.Register("github.com", fake)

	cons := &recordingConsumer{}
	s := New(SyncerOptions{
		Store:                 st,
		Registry:              reg,
		GitRemoteForge:        gr,
		Consumer:              cons,
		Repositories:          []RepoConfig{{Ref: ref, Branches: []string{"main"}}},
		LsRemoteInterval:      60 * time.Second,
		FullReconcileInterval: 1 * time.Hour,
	})

	ctx := t.Context()

	// Initial sync: seeds PR #20 as closed
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	cons.getAndClearEvents()
	_ = fake.getAndClearCalls()

	// 1. Closed PR head moves from 2222... to 8888...
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "1111111111111111111111111111111111111111"),
		plumbing.NewReferenceFromStrings("refs/pull/20/head", "8888888888888888888888888888888888888888"),
	}
	fake.changes[0].Head.SHA = "8888888888888888888888888888888888888888"

	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("sync after closed PR head moved failed: %v", err)
	}

	// Assert NO events were emitted!
	evs := cons.getAndClearEvents()
	if len(evs) != 0 {
		t.Fatalf("expected 0 events when closed PR head moves, got %d: %v", len(evs), evs)
	}

	// Assert exact call sequence: 2 calls (PullRequest and Commit) and 0 events
	wantCalls := []string{"PullRequest(20)", "Commit(8888888)"}
	if calls := fake.getAndClearCalls(); fmt.Sprintf("%v", calls) != fmt.Sprintf("%v", wantCalls) {
		t.Errorf("expected calls %v, got %v", wantCalls, calls)
	}

	// Assert stored CR has updated head SHA
	crStored, err := st.GetChangeRequest(ref, "20")
	if err != nil {
		t.Fatalf("failed to get CR from store: %v", err)
	}
	if crStored.Head.SHA != "8888888888888888888888888888888888888888" {
		t.Errorf("expected stored SHA to be updated to 8888..., got %s", crStored.Head.SHA)
	}

	// 2. Closed PR is re-opened on GitHub (state changes to open) and head moves to 9999...
	currentRefs = []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "1111111111111111111111111111111111111111"),
		plumbing.NewReferenceFromStrings("refs/pull/20/head", "9999999999999999999999999999999999999999"),
	}
	fake.changes[0].State = "open"
	fake.changes[0].Head.SHA = "9999999999999999999999999999999999999999"

	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("sync after PR reopened failed: %v", err)
	}

	// Assert ChangeOpenedEvent was emitted on re-open
	evsReopen := cons.getAndClearEvents()
	if len(evsReopen) != 1 || evsReopen[0].EventKind() != consumer.EventChangeOpened {
		t.Fatalf("expected 1 ChangeOpenedEvent when closed PR is re-opened, got %d: %v", len(evsReopen), evsReopen)
	}
}

// TestSyncer_ForkChangeRequest_CandidateCloneURL verifies that when a change request is from a fork
// (with Head.CloneURL set), the syncer emits events whose CandidateCloneURL is the fork's clone URL,
// while BaselineCloneURL is the base repository's clone URL.
func TestSyncer_ForkChangeRequest_CandidateCloneURL(t *testing.T) {
	ctx := t.Context()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	forkRef := forge.RepoRef{Host: "github.com", Owner: "codebot-robot", Name: "eval-runner"}
	baseURL := "https://github.com/gke-labs/eval-runner.git"
	forkURL := "https://github.com/codebot-robot/eval-runner.git"

	ff := forge.NewFakeForge("fake")
	ff.SetRepository(ref, &forge.Repository{
		Ref:           ref,
		DefaultBranch: "main",
		CloneURL:      baseURL,
	})
	ff.SetBranches(ref, []forge.Branch{
		{Name: "main", SHA: "0000000000000000000000000000000000000001"},
	})

	forkSHA1 := "1111111111111111111111111111111111111111"
	forkSHA2 := "2222222222222222222222222222222222222222"
	baseSHA := "0000000000000000000000000000000000000001"

	// 1. Initial State: Open Fork PR #363
	ff.SetChangeRequests(ref, []forge.ChangeRequest{
		{
			ID:     "363",
			Number: 363,
			Title:  "feat: fork PR",
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref:      "my-branch",
				SHA:      forkSHA1,
				RepoRef:  forkRef,
				CloneURL: forkURL,
			},
			Base: forge.ChangeRequestBase{Ref: "main", SHA: baseSHA},
		},
	})

	cons := &recordingConsumer{}
	s, _ := newTestSyncer(t, ff, cons)

	// First sync -> BranchAdvanced + ChangeOpened
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("first SyncRepo failed: %v", err)
	}

	evs := cons.getAndClearEvents()
	var openEv *consumer.ChangeOpenedEvent
	for _, e := range evs {
		if co, ok := e.(consumer.ChangeOpenedEvent); ok && co.Change.Number == 363 {
			coCopy := co
			openEv = &coCopy
			break
		}
	}
	if openEv == nil {
		t.Fatalf("expected ChangeOpenedEvent for PR #363, got %v", evs)
	}
	if openEv.CandidateCloneURL != forkURL {
		t.Errorf("ChangeOpenedEvent CandidateCloneURL = %q, want fork URL %q", openEv.CandidateCloneURL, forkURL)
	}
	if openEv.BaselineCloneURL != baseURL {
		t.Errorf("ChangeOpenedEvent BaselineCloneURL = %q, want base URL %q", openEv.BaselineCloneURL, baseURL)
	}

	// 2. Update PR #363: new head SHA2 on fork
	ff.SetChangeRequests(ref, []forge.ChangeRequest{
		{
			ID:     "363",
			Number: 363,
			Title:  "feat: fork PR updated",
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref:      "my-branch",
				SHA:      forkSHA2,
				RepoRef:  forkRef,
				CloneURL: forkURL,
			},
			Base: forge.ChangeRequestBase{Ref: "main", SHA: baseSHA},
		},
	})

	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("update SyncRepo failed: %v", err)
	}

	evs = cons.getAndClearEvents()
	var updateEv *consumer.ChangeUpdatedEvent
	for _, e := range evs {
		if cu, ok := e.(consumer.ChangeUpdatedEvent); ok && cu.Change.Number == 363 {
			cuCopy := cu
			updateEv = &cuCopy
			break
		}
	}
	if updateEv == nil {
		t.Fatalf("expected ChangeUpdatedEvent for PR #363, got %v", evs)
	}
	if updateEv.CandidateCloneURL != forkURL {
		t.Errorf("ChangeUpdatedEvent CandidateCloneURL = %q, want fork URL %q", updateEv.CandidateCloneURL, forkURL)
	}
	if updateEv.BaselineCloneURL != baseURL {
		t.Errorf("ChangeUpdatedEvent BaselineCloneURL = %q, want base URL %q", updateEv.BaselineCloneURL, baseURL)
	}
}

// TestSyncer_Consumer_EmptyStore_PRClosedInForge_GetsClosed verifies:
// Syncer+consumer test that starts with an empty store, a Comparison for PR N,
// and a forge reporting N not open -> N gets closed.
// Also verifies closed-head seeding only calls PullRequest for PRs with live Comparisons.
func TestSyncer_Consumer_EmptyStore_PRClosedInForge_GetsClosed(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "empty-store.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	targetRepo := "github.com/gke-labs/eval-runner"
	sanitizedRepo := consumer.SanitizeLabelValue(targetRepo)

	// Pre-create Comparison for PR #42 in the cluster (e.g. from earlier pod)
	comp42 := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-42",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelPR:   "42",
				consumer.LabelRepo: sanitizedRepo,
			},
		},
		Spec: v1alpha1.ComparisonSpec{
			Candidate: v1alpha1.ComparisonRunRef{Run: "pr-42-candidate"},
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: targetRepo,
				Number:     42,
			},
		},
	}
	candRun42 := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-42-candidate",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelPR:   "42",
				consumer.LabelRepo: sanitizedRepo,
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add v1alpha1 scheme: %v", err)
	}
	cl := fakek8s.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(comp42, candRun42).
		Build()

	cons := consumer.New(cl, consumer.Config{
		Namespace:    "eval-runner",
		DefaultModel: "mockllm/model",
		Store:        st,
	})

	// Remote refs advertise main head, pull/42/head, and pull/99/head (another closed PR without live comparison)
	remoteRefs := []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "1111111111111111111111111111111111111111"),
		plumbing.NewReferenceFromStrings("refs/pull/42/head", "4444444444444444444444444444444444444444"),
		plumbing.NewReferenceFromStrings("refs/pull/99/head", "9999999999999999999999999999999999999999"),
	}
	gr := gitremote.New(gitremote.WithRefLister(func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		return remoteRefs, nil
	}))

	// Forge reports PR 42 and 99 NOT open (fake.changes is empty)
	fake := &countingFakeForge{
		branches: []forge.Branch{
			{Name: "main", SHA: "1111111111111111111111111111111111111111"},
		},
		changes: []forge.ChangeRequest{}, // no open PRs
		pullRequests: map[int]*forge.ChangeRequest{
			42: {
				ID:     "42",
				Number: 42,
				Title:  "feat: merged PR 42",
				State:  "closed",
				Merged: true,
				Head: forge.ChangeRequestHead{
					Ref: "feat-42",
					SHA: "4444444444444444444444444444444444444444",
				},
				Base: forge.ChangeRequestBase{
					Ref: "main",
					SHA: "1111111111111111111111111111111111111111",
				},
			},
		},
	}

	reg := forge.NewRegistry(fake)
	reg.Register("github.com", fake)

	s := New(SyncerOptions{
		Store:                 st,
		Registry:              reg,
		GitRemoteForge:        gr,
		Consumer:              cons,
		Repositories:          []RepoConfig{{Ref: ref}},
		LsRemoteInterval:      1 * time.Minute,
		FullReconcileInterval: 1 * time.Hour,
	})

	ctx := t.Context()
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("SyncRepo failed: %v", err)
	}

	// 1. Verify PR 42 Comparison was retained and labeled merged
	var checkComp v1alpha1.Comparison
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "eval-runner", Name: "pr-42"}, &checkComp); err != nil {
		t.Errorf("expected comparison pr-42 to be retained on PR close, got err: %v", err)
	} else if checkComp.Labels[consumer.LabelPRState] != consumer.StateMerged {
		t.Errorf("expected pr-42 label %s=%s, got %q", consumer.LabelPRState, consumer.StateMerged, checkComp.Labels[consumer.LabelPRState])
	}

	// 2. Verify PullRequest(42) was called to set merged correctly
	// but PullRequest(99) was NOT called because it had no live Comparison
	calls := fake.getAndClearCalls()
	var calledPR42, calledPR99 bool
	for _, call := range calls {
		if call == "PullRequest(42)" {
			calledPR42 = true
		}
		if call == "PullRequest(99)" {
			calledPR99 = true
		}
	}
	if !calledPR42 {
		t.Errorf("expected PullRequest(42) to be called for live Comparison, calls were: %v", calls)
	}
	if calledPR99 {
		t.Errorf("expected PullRequest(99) NOT to be called (placeholder only), calls were: %v", calls)
	}

	// 3. Verify store contents
	cr42, err := st.GetChangeRequest(ref, "42")
	if err != nil || cr42 == nil {
		t.Fatalf("expected PR 42 to be in store, err: %v", err)
	}
	if cr42.State != "closed" || !cr42.Merged {
		t.Errorf("expected PR 42 to be closed and merged, got state=%q merged=%v", cr42.State, cr42.Merged)
	}

	cr99, err := st.GetChangeRequest(ref, "99")
	if err != nil || cr99 == nil {
		t.Fatalf("expected PR 99 placeholder in store, err: %v", err)
	}
	if cr99.State != "closed" || cr99.Merged {
		t.Errorf("expected PR 99 to be closed placeholder (merged=false), got state=%q merged=%v", cr99.State, cr99.Merged)
	}
}

// TestSyncer_Consumer_HourlyReconcile_ClosesOlderPodComparison verifies:
// A second test that the hourly full reconcile also closes a PR whose Comparison was created by an older pod.
func TestSyncer_Consumer_HourlyReconcile_ClosesOlderPodComparison(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "hourly-reconcile.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	targetRepo := "github.com/gke-labs/eval-runner"
	sanitizedRepo := consumer.SanitizeLabelValue(targetRepo)

	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add v1alpha1 scheme: %v", err)
	}
	cl := fakek8s.NewClientBuilder().
		WithScheme(scheme).
		Build()

	cons := consumer.New(cl, consumer.Config{
		Namespace:    "eval-runner",
		DefaultModel: "mockllm/model",
		Store:        st,
	})

	remoteRefs := []*plumbing.Reference{
		plumbing.NewReferenceFromStrings("refs/heads/main", "1111111111111111111111111111111111111111"),
		plumbing.NewReferenceFromStrings("refs/pull/50/head", "5050505050505050505050505050505050505050"),
	}
	gr := gitremote.New(gitremote.WithRefLister(func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		return remoteRefs, nil
	}))

	fake := &countingFakeForge{
		branches: []forge.Branch{
			{Name: "main", SHA: "1111111111111111111111111111111111111111"},
		},
		changes: []forge.ChangeRequest{
			{
				ID:     "50",
				Number: 50,
				Title:  "feat: open PR 50",
				State:  "open",
				Head: forge.ChangeRequestHead{
					Ref: "feat-50",
					SHA: "5050505050505050505050505050505050505050",
				},
				Base: forge.ChangeRequestBase{Ref: "main", SHA: "1111111111111111111111111111111111111111"},
			},
		},
		pullRequests: map[int]*forge.ChangeRequest{
			50: {
				ID:     "50",
				Number: 50,
				Title:  "feat: open PR 50",
				State:  "open",
				Head: forge.ChangeRequestHead{
					Ref: "feat-50",
					SHA: "5050505050505050505050505050505050505050",
				},
				Base: forge.ChangeRequestBase{Ref: "main", SHA: "1111111111111111111111111111111111111111"},
			},
		},
	}

	reg := forge.NewRegistry(fake)
	reg.Register("github.com", fake)

	s := New(SyncerOptions{
		Store:                 st,
		Registry:              reg,
		GitRemoteForge:        gr,
		Consumer:              cons,
		Repositories:          []RepoConfig{{Ref: ref}},
		LsRemoteInterval:      1 * time.Minute,
		FullReconcileInterval: 1 * time.Hour,
	})

	ctx := t.Context()

	// 1. First sync: PR 50 is open, comparison is created by consumer
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("first SyncRepo failed: %v", err)
	}

	var comp50 v1alpha1.Comparison
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "eval-runner", Name: "pr-50"}, &comp50); err != nil {
		t.Fatalf("expected comparison pr-50 to be created on first sync: %v", err)
	}

	// 2. PR 50 merges on the forge
	fake.mu.Lock()
	fake.changes = nil // no open PRs
	fake.pullRequests[50] = &forge.ChangeRequest{
		ID:     "50",
		Number: 50,
		Title:  "feat: open PR 50",
		State:  "closed",
		Merged: true,
		Head: forge.ChangeRequestHead{
			Ref: "feat-50",
			SHA: "5050505050505050505050505050505050505050",
		},
		Base: forge.ChangeRequestBase{Ref: "main", SHA: "1111111111111111111111111111111111111111"},
	}
	fake.mu.Unlock()

	// Pre-create another Comparison created by an older pod for PR 55
	comp55 := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pr-55",
			Namespace: "eval-runner",
			Labels: map[string]string{
				consumer.LabelPR:   "55",
				consumer.LabelRepo: sanitizedRepo,
			},
		},
		Spec: v1alpha1.ComparisonSpec{
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: targetRepo,
				Number:     55,
			},
		},
	}
	if err := cl.Create(ctx, comp55); err != nil {
		t.Fatalf("failed to create comp55: %v", err)
	}
	remoteRefs = append(remoteRefs, plumbing.NewReferenceFromStrings("refs/pull/55/head", "5555555555555555555555555555555555555555"))
	fake.mu.Lock()
	fake.pullRequests[55] = &forge.ChangeRequest{
		ID:     "55",
		Number: 55,
		Title:  "feat: closed PR 55 from older pod",
		State:  "closed",
		Merged: true,
		Head: forge.ChangeRequestHead{
			Ref: "feat-55",
			SHA: "5555555555555555555555555555555555555555",
		},
		Base: forge.ChangeRequestBase{Ref: "main", SHA: "1111111111111111111111111111111111111111"},
	}
	fake.mu.Unlock()

	// Simulate 1 hour passing so next tick triggers a full reconcile
	s.mu.Lock()
	s.lastFullReconcile[strings.ToLower(ref.String())] = time.Now().Add(-2 * time.Hour)
	s.mu.Unlock()

	// 3. Hourly full reconcile runs
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("second (hourly full reconcile) SyncRepo failed: %v", err)
	}

	// Verify PR 50 comparison is retained and labeled closed
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "eval-runner", Name: "pr-50"}, &comp50); err != nil {
		t.Errorf("expected comparison pr-50 to be retained on hourly reconcile, got err: %v", err)
	} else if comp50.Labels[consumer.LabelPRState] != consumer.StateClosed {
		t.Errorf("expected pr-50 label %s=%s, got %q", consumer.LabelPRState, consumer.StateClosed, comp50.Labels[consumer.LabelPRState])
	}
	var checkComp55 v1alpha1.Comparison
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "eval-runner", Name: "pr-55"}, &checkComp55); err != nil {
		t.Errorf("expected comparison pr-55 to be retained on hourly reconcile, got err: %v", err)
	} else if checkComp55.Labels[consumer.LabelPRState] != consumer.StateMerged {
		t.Errorf("expected pr-55 label %s=%s, got %q", consumer.LabelPRState, consumer.StateMerged, checkComp55.Labels[consumer.LabelPRState])
	}
}
