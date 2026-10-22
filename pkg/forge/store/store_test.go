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

package store

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/pkg/forge"
	bolt "go.etcd.io/bbolt"
)

func newTestStore(t testing.TB) *Store {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-forge.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

// TestRoundTripPerBucket verifies storage and retrieval for every bucket kind:
// repos, branches/<repo>, commits/<repo>, changes/<repo>, sync/<repo>.
func TestRoundTripPerBucket(t *testing.T) {
	s := newTestStore(t)
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	t.Run("repos", func(t *testing.T) {
		repo := &forge.Repository{
			Ref:           ref,
			DefaultBranch: "main",
			CloneURL:      "https://github.com/gke-labs/eval-runner.git",
		}
		if err := s.SaveRepository(repo); err != nil {
			t.Fatalf("SaveRepository failed: %v", err)
		}

		got, ok := s.GetRepository(ref)
		if !ok || got == nil {
			t.Fatalf("expected repository in in-memory index")
		}
		if got.DefaultBranch != "main" || got.CloneURL != repo.CloneURL {
			t.Errorf("unexpected repo metadata: %+v", got)
		}
	})

	t.Run("branches", func(t *testing.T) {
		bMain := forge.Branch{Name: "main", SHA: "1111111111111111111111111111111111111111"}
		bDev := forge.Branch{Name: "dev", SHA: "2222222222222222222222222222222222222222"}

		if err := s.SaveBranch(ref, bMain); err != nil {
			t.Fatalf("SaveBranch failed: %v", err)
		}
		if err := s.SaveBranch(ref, bDev); err != nil {
			t.Fatalf("SaveBranch failed: %v", err)
		}

		gotBranch, err := s.GetBranch(ref, "main")
		if err != nil {
			t.Fatalf("GetBranch failed: %v", err)
		}
		if gotBranch.SHA != bMain.SHA {
			t.Errorf("expected branch SHA %s, got %s", bMain.SHA, gotBranch.SHA)
		}

		branches, err := s.ListBranches(ref)
		if err != nil {
			t.Fatalf("ListBranches failed: %v", err)
		}
		if len(branches) != 2 {
			t.Errorf("expected 2 branches, got %d", len(branches))
		}
	})

	t.Run("commits", func(t *testing.T) {
		commitTime := time.Date(2026, time.October, 22, 10, 0, 0, 0, time.UTC)
		commit := &forge.Commit{
			SHA:     "1111111111111111111111111111111111111111",
			Parents: []string{"0000000000000000000000000000000000000000"},
			Time:    commitTime,
			Title:   "feat: initial commit",
		}
		if err := s.SaveCommit(ref, commit); err != nil {
			t.Fatalf("SaveCommit failed: %v", err)
		}

		gotCommit, ok := s.GetCommit(commit.SHA)
		if !ok || gotCommit == nil {
			t.Fatalf("GetCommit failed to find commit in index")
		}
		if gotCommit.Title != commit.Title || !gotCommit.Time.Equal(commitTime) {
			t.Errorf("unexpected commit: %+v", gotCommit)
		}
	})

	t.Run("changes", func(t *testing.T) {
		cr := forge.ChangeRequest{
			ID:     "305",
			Number: 305,
			Title:  "feat: something cool",
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref: "cool-feature",
				SHA: "3333333333333333333333333333333333333333",
			},
			Base: forge.ChangeRequestBase{
				Ref: "main",
				SHA: "1111111111111111111111111111111111111111",
			},
		}

		if err := s.SaveChangeRequest(ref, cr); err != nil {
			t.Fatalf("SaveChangeRequest failed: %v", err)
		}

		gotCR, err := s.GetChangeRequest(ref, "305")
		if err != nil {
			t.Fatalf("GetChangeRequest failed: %v", err)
		}
		if gotCR.Title != cr.Title {
			t.Errorf("expected title %q, got %q", cr.Title, gotCR.Title)
		}

		openCRs := s.GetOpenChangeRequests(ref)
		if len(openCRs) != 1 {
			t.Errorf("expected 1 open change request in index, got %d", len(openCRs))
		}

		// Update to closed: index should no longer contain it as open
		cr.State = "closed"
		if err := s.SaveChangeRequest(ref, cr); err != nil {
			t.Fatalf("SaveChangeRequest closed failed: %v", err)
		}
		openCRsAfter := s.GetOpenChangeRequests(ref)
		if len(openCRsAfter) != 0 {
			t.Errorf("expected 0 open change requests in index after close, got %d", len(openCRsAfter))
		}
	})

	t.Run("sync", func(t *testing.T) {
		syncMeta := &SyncMeta{
			LastPolled: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC),
			Cursor:     "cursor-12345",
			ETag:       "etag-67890",
		}
		if err := s.SaveSync(ref, syncMeta); err != nil {
			t.Fatalf("SaveSync failed: %v", err)
		}

		gotSync, err := s.GetSync(ref)
		if err != nil {
			t.Fatalf("GetSync failed: %v", err)
		}
		if gotSync.Cursor != "cursor-12345" || gotSync.ETag != "etag-67890" {
			t.Errorf("unexpected sync metadata: %+v", gotSync)
		}
	})
}

// runTruncationTest tests that a database file truncated to half its size
// does not panic on Open, moves the corrupted file aside with a timestamp,
// logs a warning, and opens cleanly empty.
func runTruncationTest(t *testing.T, pageSize int) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "truncate-test.db")
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	// 1. Populate a database with pageSize
	s, err := OpenWithOptions(StoreOptions{
		Path:     dbPath,
		PageSize: pageSize,
		NoSync:   true,
	})
	if err != nil {
		t.Fatalf("failed to open initial store: %v", err)
	}

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	commits := make([]*forge.Commit, 200)
	for i := 0; i < 200; i++ {
		commits[i] = &forge.Commit{
			SHA:     fmt.Sprintf("%040x", i),
			Parents: []string{fmt.Sprintf("%040x", i-1)},
			Time:    baseTime.Add(time.Duration(i) * time.Minute),
			Title:   fmt.Sprintf("Commit %d", i),
		}
	}
	if err := s.SaveSyncBatch(t.Context(), ref, &SyncBatch{Commits: commits}); err != nil {
		t.Fatalf("SaveSyncBatch failed: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	fi, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("failed to stat db file: %v", err)
	}
	originalSize := fi.Size()
	if originalSize <= int64(2*pageSize) {
		t.Fatalf("originalSize %d too small for page size %d", originalSize, pageSize)
	}

	// 2. Truncate the file to half its size
	if err := os.Truncate(dbPath, originalSize/2); err != nil {
		t.Fatalf("failed to truncate db file: %v", err)
	}

	// 3. Open the truncated file: must not panic, must move aside, and must open cleanly empty
	s2, err := OpenWithOptions(StoreOptions{
		Path:     dbPath,
		PageSize: pageSize,
	})
	if err != nil {
		t.Fatalf("Open failed on truncated database (pageSize=%d): %v", pageSize, err)
	}
	defer s2.Close()

	// Verify the database is empty
	if _, ok := s2.GetCommit(fmt.Sprintf("%040x", 0)); ok {
		t.Errorf("expected commit to be absent in clean rebuilt database")
	}

	// Verify a .corrupted.<timestamp> file was created in tmpDir
	files, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read tmpDir: %v", err)
	}
	var foundCorrupted bool
	for _, f := range files {
		if strings.Contains(f.Name(), ".corrupted.") {
			foundCorrupted = true
			break
		}
	}
	if !foundCorrupted {
		t.Errorf("expected corrupted file to be moved aside with .corrupted. suffix (pageSize=%d)", pageSize)
	}
}

// TestTruncatedDB_MovesAsideAndReopensClean_4096 verifies truncation handling with 4 KiB pages.
func TestTruncatedDB_MovesAsideAndReopensClean_4096(t *testing.T) {
	runTruncationTest(t, 4096)
}

// TestTruncatedDB_MovesAsideAndReopensClean_NativePageSize verifies truncation handling with native pages.
func TestTruncatedDB_MovesAsideAndReopensClean_NativePageSize(t *testing.T) {
	runTruncationTest(t, os.Getpagesize())
}

// TestGarbageDB_MovesAsideAndReopensClean verifies that random garbage bytes
// do not panic, move the file aside with a timestamp, and open a clean empty store.
func TestGarbageDB_MovesAsideAndReopensClean(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "garbage-test.db")

	garbage := make([]byte, 20480)
	if _, err := rand.Read(garbage); err != nil {
		t.Fatalf("rand.Read failed: %v", err)
	}
	if err := os.WriteFile(dbPath, garbage, 0600); err != nil {
		t.Fatalf("failed to write garbage file: %v", err)
	}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed on garbage database: %v", err)
	}
	defer s.Close()

	// Verify database is working and empty
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	if _, ok := s.GetRepository(ref); ok {
		t.Errorf("expected repository to be absent in fresh database")
	}

	// Verify .corrupted. file exists
	files, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read tmpDir: %v", err)
	}
	var foundCorrupted bool
	for _, f := range files {
		if strings.Contains(f.Name(), ".corrupted.") {
			foundCorrupted = true
			break
		}
	}
	if !foundCorrupted {
		t.Errorf("expected garbage file to be moved aside with .corrupted. suffix")
	}
}

// TestSchemaVersionMismatch_Rebuilds verifies that a schema version mismatch in meta
// clears stale data and reinitializes with the current schema.
func TestSchemaVersionMismatch_Rebuilds(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "schema-test.db")
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	if err := s.SaveRepository(&forge.Repository{Ref: ref, DefaultBranch: "main"}); err != nil {
		t.Fatalf("SaveRepository failed: %v", err)
	}

	// Manually corrupt the schema version in meta
	err = s.db.Update(func(tx *bolt.Tx) error {
		metaBkt := tx.Bucket([]byte(BucketMeta))
		badVerBytes, _ := json.Marshal(VersionHeader{SchemaVersion: 999})
		return metaBkt.Put([]byte(MetaKeySchemaVersion), badVerBytes)
	})
	if err != nil {
		t.Fatalf("failed to corrupt schema version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	// Reopen store: must detect schema mismatch and rebuild cleanly
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopening store after schema mismatch failed: %v", err)
	}
	defer s2.Close()

	// Stale repository should have been cleared
	if _, ok := s2.GetRepository(ref); ok {
		t.Errorf("expected repository to be cleared on schema version mismatch")
	}
}

// TestNewestFirstBranchHeads verifies that branches are sorted newest-first by commit time.
func TestNewestFirstBranchHeads(t *testing.T) {
	s := newTestStore(t)
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	tOld := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tMid := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	tNew := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	batch := &SyncBatch{
		Commits: []*forge.Commit{
			{SHA: "sha-old", Time: tOld, Title: "old"},
			{SHA: "sha-mid", Time: tMid, Title: "mid"},
			{SHA: "sha-new", Time: tNew, Title: "new"},
		},
		Branches: []forge.Branch{
			{Name: "branch-old", SHA: "sha-old"},
			{Name: "branch-new", SHA: "sha-new"},
			{Name: "branch-mid", SHA: "sha-mid"},
		},
	}

	if err := s.SaveSyncBatch(t.Context(), ref, batch); err != nil {
		t.Fatalf("SaveSyncBatch failed: %v", err)
	}

	branchHeads := s.GetBranchHeads(ref)
	if len(branchHeads) != 3 {
		t.Fatalf("expected 3 branch heads, got %d", len(branchHeads))
	}

	if branchHeads[0].Name != "branch-new" || branchHeads[1].Name != "branch-mid" || branchHeads[2].Name != "branch-old" {
		t.Errorf("unexpected branch order: %+v", branchHeads)
	}
}

func seed10kCommits(t testing.TB, dbPath string, ref forge.RepoRef) {
	s, err := OpenWithOptions(StoreOptions{
		Path:   dbPath,
		NoSync: true,
	})
	if err != nil {
		t.Fatalf("failed to open store for seeding: %v", err)
	}
	defer s.Close()

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	commits := make([]*forge.Commit, 10000)
	for i := 0; i < 10000; i++ {
		commits[i] = &forge.Commit{
			SHA:     fmt.Sprintf("%040x", i),
			Parents: []string{fmt.Sprintf("%040x", i-1)},
			Time:    baseTime.Add(time.Duration(i) * time.Minute),
			Title:   fmt.Sprintf("Commit number %d", i),
		}
	}

	for i := 0; i < 10000; i += 1000 {
		end := i + 1000
		chunk := commits[i:end]
		batch := &SyncBatch{
			Commits: chunk,
		}
		if err := s.SaveSyncBatch(t.Context(), ref, batch); err != nil {
			t.Fatalf("failed to save chunk: %v", err)
		}
	}
}

// TestIndexRebuild10kCommits verifies that 10k commits can be rebuilt from disk well under 1 second.
func TestIndexRebuild10kCommits(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "10k-fixture.db")
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	seed10kCommits(t, dbPath, ref)

	start := time.Now()
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open seeded store: %v", err)
	}
	defer s.Close()

	elapsed := time.Since(start)
	t.Logf("Rebuild time for 10,000 commits: %v", elapsed)

	if elapsed > 1*time.Second {
		t.Fatalf("index rebuild took %v, must be well under 1 second", elapsed)
	}

	c, ok := s.GetCommit(fmt.Sprintf("%040x", 9999))
	if !ok || c == nil {
		t.Errorf("expected last commit to be indexed")
	}
}

// BenchmarkIndexRebuild10kCommits benchmarks rebuilding in-memory indexes from a 10k-commit fixture.
func BenchmarkIndexRebuild10kCommits(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "benchmark-10k.db")
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	seed10kCommits(b, dbPath, ref)

	s, err := Open(dbPath)
	if err != nil {
		b.Fatalf("failed to open store: %v", err)
	}
	defer s.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.RebuildIndexes(); err != nil {
			b.Fatalf("RebuildIndexes failed: %v", err)
		}
	}
}
