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
			Title:  "feat: forge support",
			Author: "dev-bot",
			Head: forge.ChangeRequestHead{
				Ref: "feat-branch",
				SHA: "aaaaaaa111111111111111111111111111111111",
			},
			Base: forge.ChangeRequestBase{
				Ref: "main",
				SHA: "bbbbbbb222222222222222222222222222222222",
			},
			State:     "open",
			UpdatedAt: time.Now().UTC(),
		}
		if err := s.SaveChangeRequest(ref, cr); err != nil {
			t.Fatalf("SaveChangeRequest failed: %v", err)
		}

		gotCR, err := s.GetChangeRequest(ref, "305")
		if err != nil {
			t.Fatalf("GetChangeRequest failed: %v", err)
		}
		if gotCR.Title != cr.Title || gotCR.Head.SHA != cr.Head.SHA {
			t.Errorf("unexpected change request: %+v", gotCR)
		}

		openCRs := s.GetOpenChangeRequests(ref)
		if len(openCRs) != 1 || openCRs[0].Number != 305 {
			t.Errorf("unexpected open change requests: %+v", openCRs)
		}
	})

	t.Run("sync", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		syncMeta := &SyncMeta{
			LastPolled: now,
			Cursor:     "cursor-12345",
			ETag:       "etag-abcdef",
			Data: map[string]string{
				"branches_etag": "b-123",
			},
		}
		if err := s.SaveSync(ref, syncMeta); err != nil {
			t.Fatalf("SaveSync failed: %v", err)
		}

		gotSync, err := s.GetSync(ref)
		if err != nil {
			t.Fatalf("GetSync failed: %v", err)
		}
		if gotSync.Cursor != "cursor-12345" || gotSync.ETag != "etag-abcdef" || !gotSync.LastPolled.Equal(now) {
			t.Errorf("unexpected sync metadata: %+v", gotSync)
		}
	})
}

// TestSchemaVersionMismatchTriggersRebuild verifies that if a database file
// contains an outdated schema version, opening or rebuilding drops old data
// and re-initializes cleanly.
func TestSchemaVersionMismatchTriggersRebuild(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "mismatch.db")

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	// 1. Write data with outdated schemaVersion 0
	boltDB, err := bolt.Open(dbPath, 0600, nil)
	if err != nil {
		t.Fatalf("failed to open bolt: %v", err)
	}
	err = boltDB.Update(func(tx *bolt.Tx) error {
		metaBkt, err := tx.CreateBucketIfNotExists([]byte(BucketMeta))
		if err != nil {
			return err
		}
		// Write outdated schema version 0
		vBytes, _ := json.Marshal(VersionHeader{SchemaVersion: 0})
		if err := metaBkt.Put([]byte(MetaKeySchemaVersion), vBytes); err != nil {
			return err
		}

		// Write old repo data
		repoBkt, err := tx.CreateBucketIfNotExists([]byte(BucketRepos))
		if err != nil {
			return err
		}
		oldRepoData, _ := json.Marshal(StoredRepo{
			SchemaVersion: 0,
			Repository: &forge.Repository{
				Ref:           ref,
				DefaultBranch: "main",
			},
		})
		return repoBkt.Put([]byte(repoKey(ref)), oldRepoData)
	})
	if err != nil {
		t.Fatalf("failed to seed outdated DB: %v", err)
	}
	_ = boltDB.Close()

	// 2. Open store with current code -> must trigger rebuild
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed on mismatched schema: %v", err)
	}
	defer s.Close()

	// 3. Verify old data was dropped and store is re-initialized with CurrentSchemaVersion
	_, ok := s.GetRepository(ref)
	if ok {
		t.Errorf("expected old repository data to be wiped after schema mismatch rebuild")
	}

	// 4. Verify meta bucket now has CurrentSchemaVersion
	err = s.db.View(func(tx *bolt.Tx) error {
		metaBkt := tx.Bucket([]byte(BucketMeta))
		if metaBkt == nil {
			return fmt.Errorf("meta bucket missing")
		}
		vBytes := metaBkt.Get([]byte(MetaKeySchemaVersion))
		var vh VersionHeader
		if err := json.Unmarshal(vBytes, &vh); err != nil {
			return err
		}
		if vh.SchemaVersion != CurrentSchemaVersion {
			return fmt.Errorf("expected schemaVersion %d, got %d", CurrentSchemaVersion, vh.SchemaVersion)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("meta verification failed: %v", err)
	}
}

// TestRecordSchemaVersionMismatchTriggersRebuild verifies that if individual records
// have a mismatched schema version during RebuildIndexes, the store clears and resets cleanly.
func TestRecordSchemaVersionMismatchTriggersRebuild(t *testing.T) {
	s := newTestStore(t)
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	// Inject a commit with invalid schema version
	err := s.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists([]byte(BucketCommits(repoKey(ref))))
		if err != nil {
			return err
		}
		badCommitData, _ := json.Marshal(StoredCommit{
			SchemaVersion: 999, // Mismatched!
			Commit: &forge.Commit{
				SHA:   "badsha12345",
				Title: "bad commit",
			},
		})
		return bkt.Put([]byte("badsha12345"), badCommitData)
	})
	if err != nil {
		t.Fatalf("failed to inject bad commit: %v", err)
	}

	// Rebuild indexes -> should detect schema mismatch and reset cleanly
	if err := s.RebuildIndexes(); err != nil {
		t.Fatalf("RebuildIndexes failed: %v", err)
	}

	if _, ok := s.GetCommit("badsha12345"); ok {
		t.Errorf("expected mismatched commit to be wiped")
	}
}

// TestTruncatedDB_MovesAsideAndReopensClean verifies that a truncated bbolt database file
// does not trigger a runtime panic or fault, but instead is moved aside and reopened clean.
func TestTruncatedDB_MovesAsideAndReopensClean(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "truncate-test.db")
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	// 1. Create a valid store with commits
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open initial store: %v", err)
	}
	for i := 0; i < 50; i++ {
		if err := s.SaveCommit(ref, &forge.Commit{
			SHA:   fmt.Sprintf("%040x", i),
			Title: fmt.Sprintf("Commit %d", i),
		}); err != nil {
			t.Fatalf("failed to save commit: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	fi, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	originalSize := fi.Size()
	if originalSize < 8192 {
		t.Fatalf("expected db file to be at least 8192 bytes, got %d", originalSize)
	}

	// 2. Truncate the file to half its size
	if err := os.Truncate(dbPath, originalSize/2); err != nil {
		t.Fatalf("failed to truncate db file: %v", err)
	}

	// 3. Open the truncated file: must not panic, must move aside, and must open cleanly empty
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed on truncated database: %v", err)
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
		t.Errorf("expected corrupted file to be moved aside with .corrupted. suffix")
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

// seed10kCommits creates a bbolt database populated with 10,000 commits.
func seed10kCommits(t testing.TB, dbPath string, ref forge.RepoRef) {
	s, err := OpenWithOptions(StoreOptions{
		Path:   dbPath,
		NoSync: true, // fast bulk load for fixture
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

	// Write in chunks of 1000
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

// TestIndexRebuild10kCommits verifies that 10k commits can be rebuilt from disk.
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

	// Verify all 10k commits are indexed
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
