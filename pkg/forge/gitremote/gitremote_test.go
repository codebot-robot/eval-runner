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

package gitremote

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

func TestExtractSymbolicHEAD(t *testing.T) {
	testCases := []struct {
		name string
		refs []*plumbing.Reference
		want string
	}{
		{
			name: "symbolic HEAD pointing to refs/heads/main",
			refs: []*plumbing.Reference{
				plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main")),
				plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), plumbing.NewHash("cd67d6e81203b96b7fafa520603200bc168217f3")),
			},
			want: "main",
		},
		{
			name: "symbolic HEAD pointing to custom branch",
			refs: []*plumbing.Reference{
				plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/develop")),
			},
			want: "develop",
		},
		{
			name: "no symbolic HEAD",
			refs: []*plumbing.Reference{
				plumbing.NewHashReference(plumbing.HEAD, plumbing.NewHash("cd67d6e81203b96b7fafa520603200bc168217f3")),
			},
			want: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractSymbolicHEAD(tc.refs); got != tc.want {
				t.Errorf("ExtractSymbolicHEAD() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractBranches_TagsIgnored(t *testing.T) {
	refs := []*plumbing.Reference{
		plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main")),
		plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/zebra"), plumbing.NewHash("1111111111111111111111111111111111111111")),
		plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/apple"), plumbing.NewHash("2222222222222222222222222222222222222222")),
		plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), plumbing.NewHash("3333333333333333333333333333333333333333")),
		plumbing.NewHashReference(plumbing.ReferenceName("refs/tags/v1.0.0"), plumbing.NewHash("4444444444444444444444444444444444444444")),
		plumbing.NewHashReference(plumbing.ReferenceName("refs/pull/42/head"), plumbing.NewHash("5555555555555555555555555555555555555555")),
	}

	branches := ExtractBranches(refs)
	if len(branches) != 3 {
		t.Fatalf("ExtractBranches() returned %d branches, want 3", len(branches))
	}

	// Should be sorted alphabetically: apple, main, zebra
	if branches[0].Name != "apple" || branches[0].SHA != "2222222222222222222222222222222222222222" {
		t.Errorf("branches[0] = %+v", branches[0])
	}
	if branches[1].Name != "main" || branches[1].SHA != "3333333333333333333333333333333333333333" {
		t.Errorf("branches[1] = %+v", branches[1])
	}
	if branches[2].Name != "zebra" || branches[2].SHA != "1111111111111111111111111111111111111111" {
		t.Errorf("branches[2] = %+v", branches[2])
	}
}

func TestExtractChangeRequests(t *testing.T) {
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	refs := []*plumbing.Reference{
		plumbing.NewHashReference(plumbing.ReferenceName("refs/pull/42/head"), plumbing.NewHash("1111111111111111111111111111111111111111")),
		plumbing.NewHashReference(plumbing.ReferenceName("refs/pull/42/merge"), plumbing.NewHash("2222222222222222222222222222222222222222")),
		plumbing.NewHashReference(plumbing.ReferenceName("refs/pull/10/head"), plumbing.NewHash("3333333333333333333333333333333333333333")),
		// Orphan merge without matching head should be ignored
		plumbing.NewHashReference(plumbing.ReferenceName("refs/pull/99/merge"), plumbing.NewHash("9999999999999999999999999999999999999999")),
		plumbing.NewHashReference(plumbing.ReferenceName("refs/tags/v2.0.0"), plumbing.NewHash("4444444444444444444444444444444444444444")),
		plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), plumbing.NewHash("5555555555555555555555555555555555555555")),
	}

	crs := ExtractChangeRequests(refs, ref, "main")
	if len(crs) != 2 {
		t.Fatalf("ExtractChangeRequests() returned %d CRs, want 2 (orphan merge excluded)", len(crs))
	}

	// Sorted by PR number: 10, then 42
	if crs[0].Number != 10 || crs[0].ID != "10" {
		t.Errorf("crs[0] = %+v", crs[0])
	}
	if crs[0].Head.SHA != "3333333333333333333333333333333333333333" || crs[0].Head.Ref != "refs/pull/10/head" {
		t.Errorf("crs[0].Head = %+v", crs[0].Head)
	}
	if crs[0].State != "unknown" {
		t.Errorf("crs[0].State = %q, want 'unknown'", crs[0].State)
	}
	if crs[0].MergeSHA != "" {
		t.Errorf("crs[0].MergeSHA = %q, want empty", crs[0].MergeSHA)
	}

	if crs[1].Number != 42 || crs[1].ID != "42" {
		t.Errorf("crs[1] = %+v", crs[1])
	}
	if crs[1].Head.SHA != "1111111111111111111111111111111111111111" {
		t.Errorf("crs[1].Head = %+v", crs[1].Head)
	}
	if crs[1].State != "unknown" {
		t.Errorf("crs[1].State = %q, want 'unknown'", crs[1].State)
	}
	if crs[1].MergeSHA != "2222222222222222222222222222222222222222" {
		t.Errorf("crs[1].MergeSHA = %q, want 2222...", crs[1].MergeSHA)
	}
}

func TestGitRemoteForge_Lifecycle(t *testing.T) {
	ctx := t.Context()

	mockLister := func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		return []*plumbing.Reference{
			plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/develop")),
			plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/develop"), plumbing.NewHash("1234567890abcdef1234567890abcdef12345678")),
			plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), plumbing.NewHash("abcdef1234567890abcdef1234567890abcdef12")),
			plumbing.NewHashReference(plumbing.ReferenceName("refs/pull/7/head"), plumbing.NewHash("feedface1234feedface1234feedface1234feed")),
		}, nil
	}

	f := New(WithRefLister(mockLister))
	if f.Kind() != "gitremote" {
		t.Errorf("Kind() = %q, want %q", f.Kind(), "gitremote")
	}

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	// 1. Repository
	repo, err := f.Repository(ctx, ref)
	if err != nil {
		t.Fatalf("Repository() failed: %v", err)
	}
	if repo.DefaultBranch != "develop" {
		t.Errorf("DefaultBranch = %q, want %q", repo.DefaultBranch, "develop")
	}
	if repo.CloneURL != "https://github.com/gke-labs/eval-runner.git" {
		t.Errorf("CloneURL = %q, want %q", repo.CloneURL, "https://github.com/gke-labs/eval-runner.git")
	}

	// 2. Branches
	branches, err := f.Branches(ctx, ref)
	if err != nil {
		t.Fatalf("Branches() failed: %v", err)
	}
	if len(branches) != 2 || branches[0].Name != "develop" || branches[1].Name != "main" {
		t.Errorf("Branches() = %+v", branches)
	}

	// 3. ChangeRequests for github.com
	crs, err := f.ChangeRequests(ctx, ref)
	if err != nil {
		t.Fatalf("ChangeRequests() failed: %v", err)
	}
	if len(crs) != 1 || crs[0].Number != 7 || crs[0].State != "unknown" {
		t.Errorf("ChangeRequests() = %+v", crs)
	}

	// 4. ChangeRequests for non-github remote returns ErrUnsupported
	nonGHRef := forge.RepoRef{Host: "git-server.local", Owner: "-", Name: "docs-eval"}
	_, err = f.ChangeRequests(ctx, nonGHRef)
	if err == nil || !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("ChangeRequests(non-github) err = %v, want forge.ErrUnsupported", err)
	}

	// 5. Commit returns ErrUnsupported
	_, err = f.Commit(ctx, ref, "1234567890abcdef1234567890abcdef12345678")
	if err == nil || !errors.Is(err, forge.ErrUnsupported) {
		t.Errorf("Commit() err = %v, want forge.ErrUnsupported", err)
	}
}

func TestGitRemoteForge_ErrorsAndCancellation(t *testing.T) {
	ref := forge.RepoRef{Host: "git.example.com", Owner: "owner", Name: "repo"}

	// 1. Repository Not Found maps to forge.ErrNotFound
	notFoundLister := func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		return nil, transport.ErrRepositoryNotFound
	}
	fNotFound := New(WithRefLister(notFoundLister))

	_, err := fNotFound.Repository(t.Context(), ref)
	if err == nil || !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("Repository(not found) err = %v, want forge.ErrNotFound", err)
	}
	_, err = fNotFound.Branches(t.Context(), ref)
	if err == nil || !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("Branches(not found) err = %v, want forge.ErrNotFound", err)
	}

	// 2. Context Cancellation returns raw context error, not ErrNotFound
	canceledLister := func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		return nil, ctx.Err()
	}
	fCanceled := New(WithRefLister(canceledLister))

	cancelCtx, cancel := context.WithCancel(t.Context())
	cancel() // canceled immediately

	_, err = fCanceled.Branches(cancelCtx, ref)
	if err == nil || errors.Is(err, forge.ErrNotFound) {
		t.Errorf("Branches(canceled) err = %v, must not be ErrNotFound", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Branches(canceled) err = %v, want context.Canceled", err)
	}

	// 3. Timeout returns context.DeadlineExceeded
	timeoutLister := func(ctx context.Context, url string) ([]*plumbing.Reference, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
			return nil, errors.New("should have timed out")
		}
	}
	fTimeout := New(WithRefLister(timeoutLister))

	timeoutCtx, timeoutCancel := context.WithTimeout(t.Context(), 1*time.Millisecond)
	defer timeoutCancel()

	_, err = fTimeout.Branches(timeoutCtx, ref)
	if err == nil || errors.Is(err, forge.ErrNotFound) {
		t.Errorf("Branches(timeout) err = %v, must not be ErrNotFound", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Branches(timeout) err = %v, want context.DeadlineExceeded", err)
	}
}
