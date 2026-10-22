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
	"fmt"
	"time"

	"github.com/gke-labs/eval-runner/pkg/forge"
)

const (
	// CurrentSchemaVersion is the version identifier for stored records.
	CurrentSchemaVersion = 1

	// BucketRepos is the bbolt bucket name storing repository metadata.
	BucketRepos = "repos"

	// BucketMeta is the bbolt bucket name storing store-wide metadata.
	BucketMeta = "meta"

	// MetaKeySchemaVersion is the key in BucketMeta storing the database schema version.
	MetaKeySchemaVersion = "schemaVersion"
)

// BucketBranches returns the bucket name for a repository's branches.
func BucketBranches(repo string) string {
	return fmt.Sprintf("branches/%s", repo)
}

// BucketCommits returns the bucket name for a repository's commits.
func BucketCommits(repo string) string {
	return fmt.Sprintf("commits/%s", repo)
}

// BucketChanges returns the bucket name for a repository's change requests.
func BucketChanges(repo string) string {
	return fmt.Sprintf("changes/%s", repo)
}

// BucketSync returns the bucket name for a repository's sync metadata.
func BucketSync(repo string) string {
	return fmt.Sprintf("sync/%s", repo)
}

// VersionHeader is used to inspect the schema version of stored JSON records.
type VersionHeader struct {
	SchemaVersion int `json:"schemaVersion"`
}

// StoredRepo represents a repository record stored in bbolt.
type StoredRepo struct {
	SchemaVersion int               `json:"schemaVersion"`
	Repository    *forge.Repository `json:"repository"`
}

// StoredBranch represents a branch record stored in bbolt.
type StoredBranch struct {
	SchemaVersion int          `json:"schemaVersion"`
	Branch        forge.Branch `json:"branch"`
}

// StoredCommit represents a commit record stored in bbolt.
type StoredCommit struct {
	SchemaVersion int           `json:"schemaVersion"`
	Commit        *forge.Commit `json:"commit"`
}

// StoredChangeRequest represents a change request record stored in bbolt.
type StoredChangeRequest struct {
	SchemaVersion int                 `json:"schemaVersion"`
	ChangeRequest forge.ChangeRequest `json:"changeRequest"`
}

// SyncMeta represents sync state, cursors, and timestamps for a repository.
type SyncMeta struct {
	LastPolled time.Time         `json:"lastPolled,omitempty"`
	Cursor     string            `json:"cursor,omitempty"`
	ETag       string            `json:"etag,omitempty"`
	Data       map[string]string `json:"data,omitempty"`
}

// StoredSync represents a sync record stored in bbolt.
type StoredSync struct {
	SchemaVersion int       `json:"schemaVersion"`
	SyncMeta      *SyncMeta `json:"syncMeta"`
}

// SyncBatch contains updates for a repository to be applied atomically in a single write.
type SyncBatch struct {
	Repo           *forge.Repository
	Branches       []forge.Branch
	FullBranchSync bool
	Changes        []forge.ChangeRequest
	Commits        []*forge.Commit
	Sync           *SyncMeta
}

// StoreOptions contains configuration options for opening a Store.
type StoreOptions struct {
	Path    string
	Timeout time.Duration
	NoSync  bool
}
