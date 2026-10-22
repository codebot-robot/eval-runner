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
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/gke-labs/eval-runner/pkg/forge/store"
)

func newTestSyncer(t *testing.T, ff *forge.FakeForge) (*Syncer, *store.Store) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "syncer-test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	syncOpts := SyncerOptions{
		Store:           st,
		Forge:           ff,
		EventBufferSize: 100,
	}
	s, err := New(syncOpts)
	if err != nil {
		t.Fatalf("failed to create syncer: %v", err)
	}
	return s, st
}

// TestSyncer_ExactEventSequence_OpenUpdateClose tests that the syncer against FakeForge
// produces the exact event sequence for open -> update -> close.
func TestSyncer_ExactEventSequence_OpenUpdateClose(t *testing.T) {
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

	// 1. Initial State: Open PR #1 with head SHA1
	ff.SetChangeRequests(ref, []forge.ChangeRequest{
		{
			ID:     "1",
			Number: 1,
			Title:  "feat: initial pr",
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref: "feat",
				SHA: sha1,
			},
			Base: forge.ChangeRequestBase{
				Ref: "main",
				SHA: baseSHA,
			},
			UpdatedAt: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		},
	})

	s, st := newTestSyncer(t, ff)

	// First sync -> expects ChangeOpened
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("first SyncRepo failed: %v", err)
	}

	var recordedChangeEvents []EventKind
drain1:
	for {
		select {
		case ev := <-s.Events():
			if ev.EventKind() == EventChangeOpened || ev.EventKind() == EventChangeUpdated || ev.EventKind() == EventChangeClosed {
				recordedChangeEvents = append(recordedChangeEvents, ev.EventKind())
			}
		default:
			break drain1
		}
	}

	if len(recordedChangeEvents) != 1 || recordedChangeEvents[0] != EventChangeOpened {
		t.Fatalf("step 1 expected [ChangeOpened], got %v", recordedChangeEvents)
	}

	// Verify store has PR #1 in open changes
	openCRs := st.GetOpenChangeRequests(ref)
	if len(openCRs) != 1 || openCRs[0].Number != 1 {
		t.Fatalf("expected 1 open change request in store, got %+v", openCRs)
	}

	// 2. Update PR #1: new head SHA2
	ff.SetChangeRequests(ref, []forge.ChangeRequest{
		{
			ID:     "1",
			Number: 1,
			Title:  "feat: updated pr",
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref: "feat",
				SHA: sha2,
			},
			Base: forge.ChangeRequestBase{
				Ref: "main",
				SHA: baseSHA,
			},
			UpdatedAt: time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
		},
	})

	// Second sync -> expects ChangeUpdated
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("second SyncRepo failed: %v", err)
	}

drain2:
	for {
		select {
		case ev := <-s.Events():
			if ev.EventKind() == EventChangeOpened || ev.EventKind() == EventChangeUpdated || ev.EventKind() == EventChangeClosed {
				recordedChangeEvents = append(recordedChangeEvents, ev.EventKind())
			}
		default:
			break drain2
		}
	}

	if len(recordedChangeEvents) != 2 || recordedChangeEvents[1] != EventChangeUpdated {
		t.Fatalf("step 2 expected [ChangeOpened, ChangeUpdated], got %v", recordedChangeEvents)
	}

	// 3. Close PR #1: state = closed
	ff.SetChangeRequests(ref, []forge.ChangeRequest{
		{
			ID:     "1",
			Number: 1,
			Title:  "feat: closed pr",
			State:  "closed",
			Head: forge.ChangeRequestHead{
				Ref: "feat",
				SHA: sha2,
			},
			Base: forge.ChangeRequestBase{
				Ref: "main",
				SHA: baseSHA,
			},
			UpdatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		},
	})

	// Third sync -> expects ChangeClosed
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("third SyncRepo failed: %v", err)
	}

drain3:
	for {
		select {
		case ev := <-s.Events():
			if ev.EventKind() == EventChangeOpened || ev.EventKind() == EventChangeUpdated || ev.EventKind() == EventChangeClosed {
				recordedChangeEvents = append(recordedChangeEvents, ev.EventKind())
			}
		default:
			break drain3
		}
	}

	// Verify the exact event sequence: ChangeOpened -> ChangeUpdated -> ChangeClosed
	expectedSequence := []EventKind{
		EventChangeOpened,
		EventChangeUpdated,
		EventChangeClosed,
	}

	if len(recordedChangeEvents) != len(expectedSequence) {
		t.Fatalf("expected exact sequence length %d, got %v", len(expectedSequence), recordedChangeEvents)
	}
	for i := range expectedSequence {
		if recordedChangeEvents[i] != expectedSequence[i] {
			t.Errorf("at index %d: expected %s, got %s", i, expectedSequence[i], recordedChangeEvents[i])
		}
	}

	// Verify open changes in store is now empty
	if openAfter := st.GetOpenChangeRequests(ref); len(openAfter) != 0 {
		t.Errorf("expected 0 open change requests after close, got %d", len(openAfter))
	}
}

// TestSyncer_BranchAdvanced verifies that branch head advancement emits BranchAdvancedEvent.
func TestSyncer_BranchAdvanced(t *testing.T) {
	ctx := t.Context()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	ff := forge.NewFakeForge("fake")
	ff.SetRepository(ref, &forge.Repository{
		Ref:           ref,
		DefaultBranch: "main",
	})
	ff.SetBranches(ref, []forge.Branch{
		{Name: "main", SHA: "1111111111111111111111111111111111111111"},
	})
	ff.SetChangeRequests(ref, nil)

	s, st := newTestSyncer(t, ff)

	// First sync
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("SyncRepo failed: %v", err)
	}
	ev1 := <-s.Events()
	ba1, ok := ev1.(BranchAdvancedEvent)
	if !ok || ba1.Branch != "main" || ba1.NewSHA != "1111111111111111111111111111111111111111" {
		t.Fatalf("expected BranchAdvancedEvent for initial branch, got %+v", ev1)
	}

	// Advance branch
	ff.SetBranches(ref, []forge.Branch{
		{Name: "main", SHA: "2222222222222222222222222222222222222222"},
	})

	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("SyncRepo failed: %v", err)
	}
	ev2 := <-s.Events()
	ba2, ok := ev2.(BranchAdvancedEvent)
	if !ok || ba2.Branch != "main" || ba2.OldSHA != "1111111111111111111111111111111111111111" || ba2.NewSHA != "2222222222222222222222222222222222222222" {
		t.Fatalf("expected BranchAdvancedEvent for advanced branch, got %+v", ev2)
	}

	// Verify store branch heads
	bHeads := st.GetBranchHeads(ref)
	if len(bHeads) != 1 || bHeads[0].SHA != "2222222222222222222222222222222222222222" {
		t.Errorf("unexpected branch heads in store: %+v", bHeads)
	}
}

// TestParseRepoConfigList verifies parsing of repositories from YAML and rejection of invalid names.
func TestParseRepoConfigList(t *testing.T) {
	t.Run("valid_yaml", func(t *testing.T) {
		raw := `
- name: github.com/gke-labs/eval-runner
  branches: [main, release-1]
- name: github.com/org/repo2
`
		cfg, err := ParseRepoConfigList(raw)
		if err != nil {
			t.Fatalf("ParseRepoConfigList YAML failed: %v", err)
		}
		if len(cfg) != 2 {
			t.Fatalf("expected 2 configs, got %d", len(cfg))
		}
		if cfg[0].Ref.Name != "eval-runner" || len(cfg[0].Branches) != 2 {
			t.Errorf("unexpected cfg[0]: %+v", cfg[0])
		}
		if cfg[1].Ref.Name != "repo2" || len(cfg[1].Branches) != 1 || cfg[1].Branches[0] != "main" {
			t.Errorf("unexpected cfg[1]: %+v", cfg[1])
		}
	})

	t.Run("invalid_entry_rejected", func(t *testing.T) {
		raw := `
- name: nonsense
`
		_, err := ParseRepoConfigList(raw)
		if err == nil {
			t.Fatalf("expected error parsing invalid repository name 'nonsense', got nil")
		}
	})
}

type recordingConsumer struct {
	events []Event
}

func (r *recordingConsumer) ConsumeEvent(ctx context.Context, ev Event) error {
	r.events = append(r.events, ev)
	return nil
}

// TestSyncer_ConsumeBeforeCommit verifies that the syncer calls the consumer synchronously before committing.
func TestSyncer_ConsumeBeforeCommit(t *testing.T) {
	ctx := t.Context()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	ff := forge.NewFakeForge("fake")
	ff.SetRepository(ref, &forge.Repository{
		Ref:           ref,
		DefaultBranch: "main",
	})
	ff.SetBranches(ref, []forge.Branch{
		{Name: "main", SHA: "1111111111111111111111111111111111111111"},
	})
	ff.SetChangeRequests(ref, nil)

	s, st := newTestSyncer(t, ff)
	rc := &recordingConsumer{}
	s.SetConsumer(rc)

	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("SyncRepo failed: %v", err)
	}

	if len(rc.events) != 1 || rc.events[0].EventKind() != EventBranchAdvanced {
		t.Errorf("expected 1 BranchAdvancedEvent consumed synchronously, got %v", rc.events)
	}

	// Verify store committed the branch
	branches, err := st.ListBranches(ref)
	if err != nil || len(branches) != 1 {
		t.Errorf("expected 1 branch in store, got %v (err: %v)", branches, err)
	}
}

// TestSyncer_ForgeError_DoesNotCloseCRs verifies that when ChangeRequests returns an error,
// previously open change requests in the store are preserved and not closed.
func TestSyncer_ForgeError_DoesNotCloseCRs(t *testing.T) {
	ctx := t.Context()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	ff := forge.NewFakeForge("fake")
	ff.SetRepository(ref, &forge.Repository{Ref: ref, DefaultBranch: "main"})
	ff.SetBranches(ref, []forge.Branch{{Name: "main", SHA: "0000000000000000000000000000000000000001"}})
	ff.SetChangeRequests(ref, []forge.ChangeRequest{
		{
			ID:     "10",
			Number: 10,
			Title:  "feat: open pr",
			State:  "open",
			Head:   forge.ChangeRequestHead{SHA: "1111111111111111111111111111111111111111"},
			Base:   forge.ChangeRequestBase{SHA: "0000000000000000000000000000000000000001"},
		},
	})

	s, st := newTestSyncer(t, ff)
	rc := &recordingConsumer{}
	s.SetConsumer(rc)

	// Initial sync succeeds and opens PR #10
	if err := s.SyncRepo(ctx, ref); err != nil {
		t.Fatalf("initial SyncRepo failed: %v", err)
	}
	openCRs := st.GetOpenChangeRequests(ref)
	if len(openCRs) != 1 || openCRs[0].Number != 10 {
		t.Fatalf("expected PR #10 open in store, got %v", openCRs)
	}

	// Now forge returns 503 error on ChangeRequests
	rc.events = nil
	ff.ChangeReqsErr = errors.New("503 service unavailable")

	err := s.SyncRepo(ctx, ref)
	if err == nil {
		t.Fatalf("expected error from SyncRepo when ChangeRequests fails, got nil")
	}

	// Verify NO ChangeClosedEvent was emitted
	for _, ev := range rc.events {
		if ev.EventKind() == EventChangeClosed {
			t.Errorf("spurious ChangeClosedEvent emitted during failed sync: %+v", ev)
		}
	}

	// Verify PR #10 is STILL open in the store
	openAfter := st.GetOpenChangeRequests(ref)
	if len(openAfter) != 1 || openAfter[0].Number != 10 {
		t.Errorf("expected PR #10 to remain open in store after failed sync, got %v", openAfter)
	}
}
