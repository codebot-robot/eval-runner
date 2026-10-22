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
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/eval-runner/pkg/forge"
	bolt "go.etcd.io/bbolt"
)

const (
	// bboltMagic is the 4-byte magic number identifying bbolt database files.
	bboltMagic = 0xED0CDAED
)

var (
	// ErrNotFound indicates that an item was not found in the store.
	ErrNotFound = forge.ErrNotFound

	// ErrSchemaMismatch indicates a schema version mismatch occurred.
	ErrSchemaMismatch = errors.New("schema version mismatch")
)

// Store provides a local database of forge data backed by bbolt,
// accompanied by fast in-memory indexes.
type Store struct {
	mu   sync.RWMutex
	db   *bolt.DB
	path string

	// In-memory indexes
	repos map[string]*forge.Repository // repoKey -> repo
	// commits is a global in-memory index keyed by commit SHA across all repositories,
	// enabling O(1) commit lookups by SHA even though on-disk buckets (commits/<repo>)
	// partition commits per repository.
	commits     map[string]*forge.Commit
	branchHeads map[string][]forge.Branch                 // repoKey -> branches sorted newest-first
	openChanges map[string]map[string]forge.ChangeRequest // repoKey -> (crKey -> changeRequest)
}

// Open creates or opens a bbolt store at path with default options.
func Open(path string) (*Store, error) {
	return OpenWithOptions(StoreOptions{
		Path:    path,
		Timeout: 5 * time.Second,
	})
}

// OpenWithOptions creates or opens a bbolt store with the provided options.
func OpenWithOptions(opts StoreOptions) (*Store, error) {
	if opts.Path == "" {
		return nil, errors.New("store path cannot be empty")
	}

	dir := filepath.Dir(opts.Path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	if err := validateAndSanitizeDBFile(opts.Path); err != nil {
		return nil, fmt.Errorf("failed to validate database at %s: %w", opts.Path, err)
	}

	boltOpts := &bolt.Options{
		Timeout:  opts.Timeout,
		NoSync:   opts.NoSync,
		PageSize: opts.PageSize,
	}

	db, err := bolt.Open(opts.Path, 0600, boltOpts)
	if err != nil {
		if moveErr := moveCorruptedAside(opts.Path, err.Error()); moveErr != nil {
			log.Printf("warning: failed to move corrupted database aside: %v", moveErr)
			return nil, fmt.Errorf("failed to move corrupted database aside: %w", moveErr)
		}
		db, err = bolt.Open(opts.Path, 0600, boltOpts)
		if err != nil {
			return nil, fmt.Errorf("failed to open fresh bbolt database at %s: %w", opts.Path, err)
		}
	}

	s := &Store{
		db:          db,
		path:        opts.Path,
		repos:       make(map[string]*forge.Repository),
		commits:     make(map[string]*forge.Commit),
		branchHeads: make(map[string][]forge.Branch),
		openChanges: make(map[string]map[string]forge.ChangeRequest),
	}

	if err := s.initSchemaAndIndexes(); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return nil, fmt.Errorf("init error: %w (db close error: %v)", err, closeErr)
		}
		return nil, err
	}

	return s, nil
}

func validateAndSanitizeDBFile(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	size := fi.Size()
	if size == 0 {
		return nil
	}

	// Any non-empty bbolt file must have at least meta page 0 and meta page 1.
	// Initial page header + meta data requires at least 64 bytes.
	if size < 64 {
		return moveCorruptedAside(path, fmt.Sprintf("file size (%d bytes) smaller than minimum page header", size))
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			log.Printf("warning: error closing file %s: %v", path, closeErr)
		}
	}()

	p0Header := make([]byte, 64)
	if _, err := io.ReadFull(f, p0Header); err != nil {
		return moveCorruptedAside(path, fmt.Sprintf("unable to read initial meta page 0 header: %v", err))
	}

	magic0 := binary.LittleEndian.Uint32(p0Header[16:20])
	if magic0 != bboltMagic {
		return moveCorruptedAside(path, fmt.Sprintf("invalid magic 0x%x in meta page 0", magic0))
	}

	pageSize0 := binary.LittleEndian.Uint32(p0Header[24:28])
	if pageSize0 < 1024 || pageSize0 > 65536 || (pageSize0&(pageSize0-1)) != 0 {
		return moveCorruptedAside(path, fmt.Sprintf("invalid page size %d in meta page 0", pageSize0))
	}

	minTwoPages := int64(2 * pageSize0)
	if size < minTwoPages {
		return moveCorruptedAside(path, fmt.Sprintf("file size (%d bytes) smaller than 2 meta pages (%d bytes)", size, minTwoPages))
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return moveCorruptedAside(path, fmt.Sprintf("seek error: %v", err))
	}

	twoPages := make([]byte, minTwoPages)
	if _, err := io.ReadFull(f, twoPages); err != nil {
		return moveCorruptedAside(path, fmt.Sprintf("unable to read 2 meta pages (%d bytes): %v", minTwoPages, err))
	}

	page0 := twoPages[:pageSize0]
	magic0 = binary.LittleEndian.Uint32(page0[16:20])
	version0 := binary.LittleEndian.Uint32(page0[20:24])
	pgid0 := binary.LittleEndian.Uint64(page0[56:64])
	txid0 := binary.LittleEndian.Uint64(page0[64:72])

	page1 := twoPages[pageSize0 : 2*pageSize0]
	magic1 := binary.LittleEndian.Uint32(page1[16:20])
	version1 := binary.LittleEndian.Uint32(page1[20:24])
	pageSize1 := binary.LittleEndian.Uint32(page1[24:28])
	pgid1 := binary.LittleEndian.Uint64(page1[56:64])
	txid1 := binary.LittleEndian.Uint64(page1[64:72])

	valid0 := magic0 == bboltMagic && version0 == 2
	valid1 := magic1 == bboltMagic && version1 == 2 && pageSize1 == pageSize0

	if !valid0 && !valid1 {
		return moveCorruptedAside(path, "neither meta page 0 nor meta page 1 is valid")
	}

	var activePgid uint64
	if valid0 && valid1 {
		if txid1 > txid0 {
			activePgid = pgid1
		} else {
			activePgid = pgid0
		}
	} else if valid1 {
		activePgid = pgid1
	} else {
		activePgid = pgid0
	}

	if activePgid < 2 {
		activePgid = 2
	}

	expectedSize := int64(activePgid) * int64(pageSize0)
	if size < expectedSize {
		return moveCorruptedAside(path, fmt.Sprintf("truncated file: size is %d bytes, but meta page pgid (%d) requires at least %d bytes", size, activePgid, expectedSize))
	}

	return nil
}

func moveCorruptedAside(path, reason string) error {
	backupPath := fmt.Sprintf("%s.corrupted.%s", path, time.Now().UTC().Format("20060102T150405Z"))
	log.Printf("warning: forge database at %s is corrupted (%s); moving aside to %s and starting fresh", path, reason, backupPath)
	return os.Rename(path, backupPath)
}

// Close closes the underlying bbolt database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// Path returns the filesystem path of the database.
func (s *Store) Path() string {
	return s.path
}

// Size returns the size in bytes of the database file on disk.
func (s *Store) Size() (int64, error) {
	fi, err := os.Stat(s.path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func repoKey(ref forge.RepoRef) string {
	return strings.ToLower(ref.String())
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

// initSchemaAndIndexes checks the database schema version. If missing or mismatched,
// it drops all existing buckets and triggers a clean rebuild.
func (s *Store) initSchemaAndIndexes() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var needsRebuild bool
	err := s.db.Update(func(tx *bolt.Tx) error {
		metaBkt := tx.Bucket([]byte(BucketMeta))
		if metaBkt == nil {
			var err error
			metaBkt, err = tx.CreateBucketIfNotExists([]byte(BucketMeta))
			if err != nil {
				return err
			}
			versionBytes, err := json.Marshal(VersionHeader{SchemaVersion: CurrentSchemaVersion})
			if err != nil {
				return err
			}
			return metaBkt.Put([]byte(MetaKeySchemaVersion), versionBytes)
		}

		vBytes := metaBkt.Get([]byte(MetaKeySchemaVersion))
		if vBytes == nil {
			needsRebuild = true
			return nil
		}
		var vh VersionHeader
		if err := json.Unmarshal(vBytes, &vh); err != nil || vh.SchemaVersion != CurrentSchemaVersion {
			needsRebuild = true
			return nil
		}
		return nil
	})
	if err != nil {
		return err
	}

	if needsRebuild {
		if err := s.clearBucketsLocked(); err != nil {
			return fmt.Errorf("failed to clear buckets on schema mismatch: %w", err)
		}
		if err := s.db.Update(func(tx *bolt.Tx) error {
			metaBkt, err := tx.CreateBucketIfNotExists([]byte(BucketMeta))
			if err != nil {
				return err
			}
			versionBytes, err := json.Marshal(VersionHeader{SchemaVersion: CurrentSchemaVersion})
			if err != nil {
				return err
			}
			return metaBkt.Put([]byte(MetaKeySchemaVersion), versionBytes)
		}); err != nil {
			return err
		}
	}

	return s.rebuildIndexesLocked()
}

// Clear drops all buckets and re-initializes the database with an empty index and current schema.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.clearBucketsLocked(); err != nil {
		return err
	}

	if err := s.db.Update(func(tx *bolt.Tx) error {
		metaBkt, err := tx.CreateBucketIfNotExists([]byte(BucketMeta))
		if err != nil {
			return err
		}
		versionBytes, err := json.Marshal(VersionHeader{SchemaVersion: CurrentSchemaVersion})
		if err != nil {
			return err
		}
		return metaBkt.Put([]byte(MetaKeySchemaVersion), versionBytes)
	}); err != nil {
		return err
	}

	s.repos = make(map[string]*forge.Repository)
	s.commits = make(map[string]*forge.Commit)
	s.branchHeads = make(map[string][]forge.Branch)
	s.openChanges = make(map[string]map[string]forge.ChangeRequest)
	return nil
}

func (s *Store) clearBucketsLocked() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var bucketNames [][]byte
		c := tx.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if v == nil { // bucket
				bucketNames = append(bucketNames, k)
			}
		}
		for _, bName := range bucketNames {
			if err := tx.DeleteBucket(bName); err != nil {
				return err
			}
		}
		return nil
	})
}

// RebuildIndexes scans the bbolt buckets and populates all in-memory indexes.
// If an individual record schema mismatch is encountered, it clears all data and starts clean.
func (s *Store) RebuildIndexes() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebuildIndexesLocked()
}

func (s *Store) rebuildIndexesLocked() error {
	newRepos := make(map[string]*forge.Repository)
	newCommits := make(map[string]*forge.Commit)
	newBranches := make(map[string][]forge.Branch)
	newOpenChanges := make(map[string]map[string]forge.ChangeRequest)

	var schemaMismatch bool

	err := s.db.View(func(tx *bolt.Tx) error {
		// 1. Repos
		if b := tx.Bucket([]byte(BucketRepos)); b != nil {
			c := b.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				var rec StoredRepo
				if err := json.Unmarshal(v, &rec); err != nil || rec.SchemaVersion != CurrentSchemaVersion {
					schemaMismatch = true
					return nil
				}
				if rec.Repository != nil {
					newRepos[repoKey(rec.Repository.Ref)] = rec.Repository
				}
			}
		}

		// 2. Iterate all buckets
		c := tx.Cursor()
		for bName, v := c.First(); bName != nil; bName, v = c.Next() {
			if v != nil {
				continue // not a bucket
			}
			bStr := string(bName)
			bkt := tx.Bucket(bName)
			if bkt == nil {
				continue
			}

			switch {
			case strings.HasPrefix(bStr, "commits/"):
				cur := bkt.Cursor()
				for k, val := cur.First(); k != nil; k, val = cur.Next() {
					var rec StoredCommit
					if err := json.Unmarshal(val, &rec); err != nil || rec.SchemaVersion != CurrentSchemaVersion {
						schemaMismatch = true
						return nil
					}
					if rec.Commit != nil {
						newCommits[rec.Commit.SHA] = rec.Commit
					}
				}

			case strings.HasPrefix(bStr, "branches/"):
				rKey := strings.ToLower(strings.TrimPrefix(bStr, "branches/"))
				cur := bkt.Cursor()
				for k, val := cur.First(); k != nil; k, val = cur.Next() {
					var rec StoredBranch
					if err := json.Unmarshal(val, &rec); err != nil || rec.SchemaVersion != CurrentSchemaVersion {
						schemaMismatch = true
						return nil
					}
					newBranches[rKey] = append(newBranches[rKey], rec.Branch)
				}

			case strings.HasPrefix(bStr, "changes/"):
				rKey := strings.ToLower(strings.TrimPrefix(bStr, "changes/"))
				cur := bkt.Cursor()
				for k, val := cur.First(); k != nil; k, val = cur.Next() {
					var rec StoredChangeRequest
					if err := json.Unmarshal(val, &rec); err != nil || rec.SchemaVersion != CurrentSchemaVersion {
						schemaMismatch = true
						return nil
					}
					if isOpenChange(rec.ChangeRequest) {
						if newOpenChanges[rKey] == nil {
							newOpenChanges[rKey] = make(map[string]forge.ChangeRequest)
						}
						newOpenChanges[rKey][changeKey(rec.ChangeRequest)] = rec.ChangeRequest
					}
				}
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	if schemaMismatch {
		if err := s.clearBucketsLocked(); err != nil {
			return err
		}
		if err := s.db.Update(func(tx *bolt.Tx) error {
			metaBkt, err := tx.CreateBucketIfNotExists([]byte(BucketMeta))
			if err != nil {
				return err
			}
			versionBytes, err := json.Marshal(VersionHeader{SchemaVersion: CurrentSchemaVersion})
			if err != nil {
				return err
			}
			return metaBkt.Put([]byte(MetaKeySchemaVersion), versionBytes)
		}); err != nil {
			return err
		}
		s.repos = make(map[string]*forge.Repository)
		s.commits = make(map[string]*forge.Commit)
		s.branchHeads = make(map[string][]forge.Branch)
		s.openChanges = make(map[string]map[string]forge.ChangeRequest)
		return nil
	}

	// Sort branches newest-first using commit timestamps
	for rKey, branches := range newBranches {
		sortBranchesNewestFirst(branches, newCommits)
		newBranches[rKey] = branches
	}

	s.repos = newRepos
	s.commits = newCommits
	s.branchHeads = newBranches
	s.openChanges = newOpenChanges
	return nil
}

func sortBranchesNewestFirst(branches []forge.Branch, commits map[string]*forge.Commit) {
	sort.SliceStable(branches, func(i, j int) bool {
		cI := commits[branches[i].SHA]
		cJ := commits[branches[j].SHA]
		if cI != nil && cJ != nil {
			if !cI.Time.Equal(cJ.Time) {
				return cI.Time.After(cJ.Time)
			}
		} else if cI != nil {
			return true
		} else if cJ != nil {
			return false
		}
		return branches[i].Name < branches[j].Name
	})
}

// SaveSyncBatch atomically applies repository updates to disk in a single bbolt transaction
// and updates the in-memory indexes.
func (s *Store) SaveSyncBatch(ctx context.Context, repo forge.RepoRef, batch *SyncBatch) error {
	if batch == nil {
		return nil
	}
	rKey := repoKey(repo)

	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.db.Update(func(tx *bolt.Tx) error {
		// 1. Repo
		if batch.Repo != nil {
			bkt, err := tx.CreateBucketIfNotExists([]byte(BucketRepos))
			if err != nil {
				return err
			}
			data, err := json.Marshal(StoredRepo{
				SchemaVersion: CurrentSchemaVersion,
				Repository:    batch.Repo,
			})
			if err != nil {
				return err
			}
			if err := bkt.Put([]byte(rKey), data); err != nil {
				return err
			}
		}

		// 2. Commits
		if len(batch.Commits) > 0 {
			bkt, err := tx.CreateBucketIfNotExists([]byte(BucketCommits(rKey)))
			if err != nil {
				return err
			}
			for _, c := range batch.Commits {
				if c == nil || c.SHA == "" {
					continue
				}
				data, err := json.Marshal(StoredCommit{
					SchemaVersion: CurrentSchemaVersion,
					Commit:        c,
				})
				if err != nil {
					return err
				}
				if err := bkt.Put([]byte(c.SHA), data); err != nil {
					return err
				}
			}
		}

		// 3. Branches
		if batch.Branches != nil {
			bkt, err := tx.CreateBucketIfNotExists([]byte(BucketBranches(rKey)))
			if err != nil {
				return err
			}
			newBranchNames := make(map[string]bool)
			for _, b := range batch.Branches {
				if b.Name == "" {
					continue
				}
				newBranchNames[b.Name] = true
				data, err := json.Marshal(StoredBranch{
					SchemaVersion: CurrentSchemaVersion,
					Branch:        b,
				})
				if err != nil {
					return err
				}
				if err := bkt.Put([]byte(b.Name), data); err != nil {
					return err
				}
			}
			if batch.FullBranchSync {
				// Delete branches no longer present in full sync
				var toDelete [][]byte
				c := bkt.Cursor()
				for k, _ := c.First(); k != nil; k, _ = c.Next() {
					if !newBranchNames[string(k)] {
						toDelete = append(toDelete, k)
					}
				}
				for _, k := range toDelete {
					if err := bkt.Delete(k); err != nil {
						return err
					}
				}
			}
		}

		// 4. ChangeRequests
		if len(batch.Changes) > 0 {
			bkt, err := tx.CreateBucketIfNotExists([]byte(BucketChanges(rKey)))
			if err != nil {
				return err
			}
			for _, cr := range batch.Changes {
				k := changeKey(cr)
				if k == "" {
					continue
				}
				data, err := json.Marshal(StoredChangeRequest{
					SchemaVersion: CurrentSchemaVersion,
					ChangeRequest: cr,
				})
				if err != nil {
					return err
				}
				if err := bkt.Put([]byte(k), data); err != nil {
					return err
				}
			}
		}

		// 5. Sync metadata
		if batch.Sync != nil {
			bkt, err := tx.CreateBucketIfNotExists([]byte(BucketSync(rKey)))
			if err != nil {
				return err
			}
			data, err := json.Marshal(StoredSync{
				SchemaVersion: CurrentSchemaVersion,
				SyncMeta:      batch.Sync,
			})
			if err != nil {
				return err
			}
			if err := bkt.Put([]byte("sync"), data); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	// Update in-memory indexes
	if batch.Repo != nil {
		s.repos[rKey] = batch.Repo
	}
	for _, c := range batch.Commits {
		if c != nil && c.SHA != "" {
			s.commits[c.SHA] = c
		}
	}
	if batch.Branches != nil {
		if batch.FullBranchSync {
			updated := make([]forge.Branch, len(batch.Branches))
			copy(updated, batch.Branches)
			sortBranchesNewestFirst(updated, s.commits)
			s.branchHeads[rKey] = updated
		} else {
			curMap := make(map[string]forge.Branch)
			for _, b := range s.branchHeads[rKey] {
				curMap[b.Name] = b
			}
			for _, b := range batch.Branches {
				curMap[b.Name] = b
			}
			var updated []forge.Branch
			for _, b := range curMap {
				updated = append(updated, b)
			}
			sortBranchesNewestFirst(updated, s.commits)
			s.branchHeads[rKey] = updated
		}
	}
	if len(batch.Changes) > 0 {
		if s.openChanges[rKey] == nil {
			s.openChanges[rKey] = make(map[string]forge.ChangeRequest)
		}
		for _, cr := range batch.Changes {
			k := changeKey(cr)
			if isOpenChange(cr) {
				s.openChanges[rKey][k] = cr
			} else {
				delete(s.openChanges[rKey], k)
			}
		}
	}

	return nil
}

// GetOpenChangeRequests returns all open change requests for a repository from the in-memory index.
func (s *Store) GetOpenChangeRequests(repo forge.RepoRef) []forge.ChangeRequest {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rKey := repoKey(repo)
	m := s.openChanges[rKey]
	if len(m) == 0 {
		return nil
	}

	out := make([]forge.ChangeRequest, 0, len(m))
	for _, cr := range m {
		out = append(out, cr)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Number < out[j].Number
	})
	return out
}

// GetCommit returns a commit by SHA from the in-memory index.
func (s *Store) GetCommit(sha string) (*forge.Commit, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	c, ok := s.commits[sha]
	return c, ok
}

// GetBranchHeads returns branch heads for a repository, sorted newest-first by commit time.
func (s *Store) GetBranchHeads(repo forge.RepoRef) []forge.Branch {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rKey := repoKey(repo)
	branches := s.branchHeads[rKey]
	if len(branches) == 0 {
		return nil
	}
	out := make([]forge.Branch, len(branches))
	copy(out, branches)
	return out
}

// GetRepository returns stored repository metadata from the in-memory index.
func (s *Store) GetRepository(repo forge.RepoRef) (*forge.Repository, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rKey := repoKey(repo)
	r, ok := s.repos[rKey]
	return r, ok
}

// SaveRepository saves repository metadata to disk and updates the in-memory index.
func (s *Store) SaveRepository(repo *forge.Repository) error {
	if repo == nil {
		return errors.New("repository cannot be nil")
	}
	rKey := repoKey(repo.Ref)

	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists([]byte(BucketRepos))
		if err != nil {
			return err
		}
		data, err := json.Marshal(StoredRepo{
			SchemaVersion: CurrentSchemaVersion,
			Repository:    repo,
		})
		if err != nil {
			return err
		}
		return bkt.Put([]byte(rKey), data)
	})
	if err != nil {
		return err
	}
	s.repos[rKey] = repo
	return nil
}

// SaveBranch saves a single branch to disk and updates the in-memory index.
func (s *Store) SaveBranch(repo forge.RepoRef, branch forge.Branch) error {
	rKey := repoKey(repo)

	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists([]byte(BucketBranches(rKey)))
		if err != nil {
			return err
		}
		data, err := json.Marshal(StoredBranch{
			SchemaVersion: CurrentSchemaVersion,
			Branch:        branch,
		})
		if err != nil {
			return err
		}
		return bkt.Put([]byte(branch.Name), data)
	})
	if err != nil {
		return err
	}

	curMap := make(map[string]forge.Branch)
	for _, b := range s.branchHeads[rKey] {
		curMap[b.Name] = b
	}
	curMap[branch.Name] = branch
	var updated []forge.Branch
	for _, b := range curMap {
		updated = append(updated, b)
	}
	sortBranchesNewestFirst(updated, s.commits)
	s.branchHeads[rKey] = updated

	return nil
}

// SaveCommit saves a single commit to disk and updates the in-memory index.
func (s *Store) SaveCommit(repo forge.RepoRef, commit *forge.Commit) error {
	if commit == nil || commit.SHA == "" {
		return errors.New("invalid commit")
	}
	rKey := repoKey(repo)

	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists([]byte(BucketCommits(rKey)))
		if err != nil {
			return err
		}
		data, err := json.Marshal(StoredCommit{
			SchemaVersion: CurrentSchemaVersion,
			Commit:        commit,
		})
		if err != nil {
			return err
		}
		return bkt.Put([]byte(commit.SHA), data)
	})
	if err != nil {
		return err
	}
	s.commits[commit.SHA] = commit
	return nil
}

// SaveChangeRequest saves a single change request to disk and updates the in-memory index.
func (s *Store) SaveChangeRequest(repo forge.RepoRef, cr forge.ChangeRequest) error {
	rKey := repoKey(repo)
	k := changeKey(cr)

	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists([]byte(BucketChanges(rKey)))
		if err != nil {
			return err
		}
		data, err := json.Marshal(StoredChangeRequest{
			SchemaVersion: CurrentSchemaVersion,
			ChangeRequest: cr,
		})
		if err != nil {
			return err
		}
		return bkt.Put([]byte(k), data)
	})
	if err != nil {
		return err
	}

	if s.openChanges[rKey] == nil {
		s.openChanges[rKey] = make(map[string]forge.ChangeRequest)
	}
	if isOpenChange(cr) {
		s.openChanges[rKey][k] = cr
	} else {
		delete(s.openChanges[rKey], k)
	}

	return nil
}

// SaveSync saves sync metadata for a repository to disk.
func (s *Store) SaveSync(repo forge.RepoRef, syncMeta *SyncMeta) error {
	rKey := repoKey(repo)

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists([]byte(BucketSync(rKey)))
		if err != nil {
			return err
		}
		data, err := json.Marshal(StoredSync{
			SchemaVersion: CurrentSchemaVersion,
			SyncMeta:      syncMeta,
		})
		if err != nil {
			return err
		}
		return bkt.Put([]byte("sync"), data)
	})
}

// GetBranch retrieves a branch from disk by name.
func (s *Store) GetBranch(repo forge.RepoRef, name string) (*forge.Branch, error) {
	rKey := repoKey(repo)

	s.mu.RLock()
	defer s.mu.RUnlock()

	var branch *forge.Branch
	err := s.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket([]byte(BucketBranches(rKey)))
		if bkt == nil {
			return ErrNotFound
		}
		val := bkt.Get([]byte(name))
		if val == nil {
			return ErrNotFound
		}
		var rec StoredBranch
		if err := json.Unmarshal(val, &rec); err != nil {
			return err
		}
		branch = &rec.Branch
		return nil
	})
	return branch, err
}

// ListBranches retrieves all stored branches for a repository from disk.
func (s *Store) ListBranches(repo forge.RepoRef) ([]forge.Branch, error) {
	rKey := repoKey(repo)

	s.mu.RLock()
	defer s.mu.RUnlock()

	var branches []forge.Branch
	err := s.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket([]byte(BucketBranches(rKey)))
		if bkt == nil {
			return nil
		}
		c := bkt.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec StoredBranch
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			branches = append(branches, rec.Branch)
		}
		return nil
	})
	return branches, err
}

// GetChangeRequest retrieves a change request by number or ID from disk.
func (s *Store) GetChangeRequest(repo forge.RepoRef, numberOrID string) (*forge.ChangeRequest, error) {
	rKey := repoKey(repo)

	s.mu.RLock()
	defer s.mu.RUnlock()

	var cr *forge.ChangeRequest
	err := s.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket([]byte(BucketChanges(rKey)))
		if bkt == nil {
			return ErrNotFound
		}
		val := bkt.Get([]byte(numberOrID))
		if val == nil {
			return ErrNotFound
		}
		var rec StoredChangeRequest
		if err := json.Unmarshal(val, &rec); err != nil {
			return err
		}
		cr = &rec.ChangeRequest
		return nil
	})
	return cr, err
}

// ListChangeRequests retrieves all stored change requests for a repository from disk.
func (s *Store) ListChangeRequests(repo forge.RepoRef) ([]forge.ChangeRequest, error) {
	rKey := repoKey(repo)

	s.mu.RLock()
	defer s.mu.RUnlock()

	var crs []forge.ChangeRequest
	err := s.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket([]byte(BucketChanges(rKey)))
		if bkt == nil {
			return nil
		}
		c := bkt.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec StoredChangeRequest
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			crs = append(crs, rec.ChangeRequest)
		}
		return nil
	})
	return crs, err
}

// GetSync retrieves stored sync metadata for a repository.
func (s *Store) GetSync(repo forge.RepoRef) (*SyncMeta, error) {
	rKey := repoKey(repo)

	s.mu.RLock()
	defer s.mu.RUnlock()

	var sm *SyncMeta
	err := s.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket([]byte(BucketSync(rKey)))
		if bkt == nil {
			return ErrNotFound
		}
		val := bkt.Get([]byte("sync"))
		if val == nil {
			return ErrNotFound
		}
		var rec StoredSync
		if err := json.Unmarshal(val, &rec); err != nil {
			return err
		}
		sm = rec.SyncMeta
		return nil
	})
	return sm, err
}
