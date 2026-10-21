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

package forge

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// UpsertCommentCall records an invocation of UpsertComment on FakeForge.
type UpsertCommentCall struct {
	Ref  RepoRef
	ID   ChangeRequestID
	Key  string
	Body string
}

// SetStatusCall records an invocation of SetStatus on FakeForge.
type SetStatusCall struct {
	Ref    RepoRef
	SHA    string
	Status Status
}

// FakeForge is an in-memory implementation of Forge and Reporter for unit tests.
type FakeForge struct {
	mu             sync.RWMutex
	KindName       string
	Repositories   map[string]*Repository
	BranchMap      map[string][]Branch
	ChangeReqs     map[string][]ChangeRequest
	CommitMap      map[string]*Commit
	Comments       map[string]string // key: repoKey/id/markerKey -> body
	Statuses       map[string]Status // key: repoKey/sha/context -> status
	UpsertCalls    []UpsertCommentCall
	SetStatusCalls []SetStatusCall
	nextCommentID  int64
	nextCheckRunID int64

	// Custom error injection for tests
	RepoErr       error
	BranchesErr   error
	ChangeReqsErr error
	CommitErr     error
	UpsertErr     error
	SetStatusErr  error
}

// NewFakeForge creates a new FakeForge with the specified kind.
func NewFakeForge(kind string) *FakeForge {
	if kind == "" {
		kind = "fake"
	}
	return &FakeForge{
		KindName:       kind,
		Repositories:   make(map[string]*Repository),
		BranchMap:      make(map[string][]Branch),
		ChangeReqs:     make(map[string][]ChangeRequest),
		CommitMap:      make(map[string]*Commit),
		Comments:       make(map[string]string),
		Statuses:       make(map[string]Status),
		nextCommentID:  100,
		nextCheckRunID: 200,
	}
}

func repoKey(ref RepoRef) string {
	return strings.ToLower(ref.String())
}

// Kind returns the configured forge identifier.
func (f *FakeForge) Kind() string {
	if f.KindName == "" {
		return "fake"
	}
	return f.KindName
}

// SetRepository adds or updates repository metadata for a RepoRef.
func (f *FakeForge) SetRepository(ref RepoRef, repo *Repository) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Repositories[repoKey(ref)] = repo
}

// Repository returns metadata for ref, or ErrNotFound if not found.
func (f *FakeForge) Repository(ctx context.Context, ref RepoRef) (*Repository, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.RepoErr != nil {
		return nil, f.RepoErr
	}
	if repo, ok := f.Repositories[repoKey(ref)]; ok {
		return repo, nil
	}
	return nil, ErrNotFound
}

// SetBranches configures the branches for a RepoRef.
func (f *FakeForge) SetBranches(ref RepoRef, branches []Branch) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.BranchMap[repoKey(ref)] = branches
}

// Branches returns branch heads for ref.
func (f *FakeForge) Branches(ctx context.Context, ref RepoRef) ([]Branch, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.BranchesErr != nil {
		return nil, f.BranchesErr
	}
	if branches, ok := f.BranchMap[repoKey(ref)]; ok {
		return branches, nil
	}
	return nil, ErrNotFound
}

// SetChangeRequests configures change requests for a RepoRef.
func (f *FakeForge) SetChangeRequests(ref RepoRef, crs []ChangeRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ChangeReqs[repoKey(ref)] = crs
}

// ChangeRequests returns change requests for ref.
func (f *FakeForge) ChangeRequests(ctx context.Context, ref RepoRef) ([]ChangeRequest, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.ChangeReqsErr != nil {
		return nil, f.ChangeReqsErr
	}
	if crs, ok := f.ChangeReqs[repoKey(ref)]; ok {
		return crs, nil
	}
	return nil, ErrNotFound
}

// SetCommit configures a commit for a RepoRef and SHA.
func (f *FakeForge) SetCommit(ref RepoRef, commit *Commit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CommitMap[fmt.Sprintf("%s:%s", repoKey(ref), commit.SHA)] = commit
	// Also index by SHA alone for convenience
	f.CommitMap[commit.SHA] = commit
}

// Commit returns commit metadata for ref and sha.
func (f *FakeForge) Commit(ctx context.Context, ref RepoRef, sha string) (*Commit, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.CommitErr != nil {
		return nil, f.CommitErr
	}
	if commit, ok := f.CommitMap[fmt.Sprintf("%s:%s", repoKey(ref), sha)]; ok {
		return commit, nil
	}
	if commit, ok := f.CommitMap[sha]; ok {
		return commit, nil
	}
	return nil, ErrNotFound
}

// UpsertComment stores or updates a comment for the given ref, change request ID, and key.
func (f *FakeForge) UpsertComment(ctx context.Context, ref RepoRef, id ChangeRequestID, key string, body string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.UpsertErr != nil {
		return 0, f.UpsertErr
	}
	f.UpsertCalls = append(f.UpsertCalls, UpsertCommentCall{
		Ref:  ref,
		ID:   id,
		Key:  key,
		Body: body,
	})
	f.Comments[fmt.Sprintf("%s/%s/%s", repoKey(ref), id, key)] = body
	f.nextCommentID++
	return f.nextCommentID, nil
}

// SetStatus records a status check on a commit SHA.
func (f *FakeForge) SetStatus(ctx context.Context, ref RepoRef, sha string, status Status) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SetStatusErr != nil {
		return 0, f.SetStatusErr
	}
	f.SetStatusCalls = append(f.SetStatusCalls, SetStatusCall{
		Ref:    ref,
		SHA:    sha,
		Status: status,
	})
	f.Statuses[fmt.Sprintf("%s/%s/%s", repoKey(ref), sha, status.Context)] = status
	if status.CheckRunID != 0 {
		return status.CheckRunID, nil
	}
	f.nextCheckRunID++
	return f.nextCheckRunID, nil
}

// GetComment retrieves a comment body previously recorded by UpsertComment.
func (f *FakeForge) GetComment(ref RepoRef, id ChangeRequestID, key string) (string, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	body, ok := f.Comments[fmt.Sprintf("%s/%s/%s", repoKey(ref), id, key)]
	return body, ok
}

// GetStatus retrieves a status previously recorded by SetStatus.
func (f *FakeForge) GetStatus(ref RepoRef, sha, context string) (Status, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	st, ok := f.Statuses[fmt.Sprintf("%s/%s/%s", repoKey(ref), sha, context)]
	return st, ok
}

// GetUpsertCalls returns a copy of recorded UpsertComment invocations.
func (f *FakeForge) GetUpsertCalls() []UpsertCommentCall {
	f.mu.RLock()
	defer f.mu.RUnlock()
	calls := make([]UpsertCommentCall, len(f.UpsertCalls))
	copy(calls, f.UpsertCalls)
	return calls
}

// GetSetStatusCalls returns a copy of recorded SetStatus invocations.
func (f *FakeForge) GetSetStatusCalls() []SetStatusCall {
	f.mu.RLock()
	defer f.mu.RUnlock()
	calls := make([]SetStatusCall, len(f.SetStatusCalls))
	copy(calls, f.SetStatusCalls)
	return calls
}
