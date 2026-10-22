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
	"testing"

	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/go-git/go-git/v5/plumbing"
)

func TestRefDiffing(t *testing.T) {
	storedBranches := []forge.Branch{
		{Name: "main", SHA: "aaaa111111111111111111111111111111111111"},
		{Name: "release-v1", SHA: "bbbb222222222222222222222222222222222222"},
	}

	storedCRs := []forge.ChangeRequest{
		{
			Number: 10,
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref: "feature-10",
				SHA: "cccc333333333333333333333333333333333333",
			},
		},
		{
			Number: 20,
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref: "feature-20",
				SHA: "dddd444444444444444444444444444444444444",
			},
		},
		{
			Number: 30,
			State:  "closed",
			Head: forge.ChangeRequestHead{
				Ref: "feature-30",
				SHA: "3030303030303030303030303030303030303030",
			},
		},
	}

	t.Run("no change -> hasChanges is false", func(t *testing.T) {
		refs := []*plumbing.Reference{
			plumbing.NewReferenceFromStrings("refs/heads/main", "aaaa111111111111111111111111111111111111"),
			plumbing.NewReferenceFromStrings("refs/heads/release-v1", "bbbb222222222222222222222222222222222222"),
			plumbing.NewReferenceFromStrings("refs/pull/10/head", "cccc333333333333333333333333333333333333"),
			plumbing.NewReferenceFromStrings("refs/pull/20/head", "dddd444444444444444444444444444444444444"),
			plumbing.NewReferenceFromStrings("refs/pull/30/head", "3030303030303030303030303030303030303030"),
		}

		diff := DiffRefs(storedBranches, storedCRs, refs)
		if diff.HasChanges {
			t.Errorf("expected HasChanges=false, got true (newBranches: %v, updated: %v, openedPRs: %v, syncPRs: %v)",
				diff.NewBranches, diff.UpdatedBranches, diff.OpenedPRs, diff.SyncPRs)
		}
	})

	t.Run("closed PR with surviving head ref ignored -> hasChanges is false", func(t *testing.T) {
		refs := []*plumbing.Reference{
			plumbing.NewReferenceFromStrings("refs/heads/main", "aaaa111111111111111111111111111111111111"),
			plumbing.NewReferenceFromStrings("refs/heads/release-v1", "bbbb222222222222222222222222222222222222"),
			plumbing.NewReferenceFromStrings("refs/pull/10/head", "cccc333333333333333333333333333333333333"),
			plumbing.NewReferenceFromStrings("refs/pull/20/head", "dddd444444444444444444444444444444444444"),
			// Closed PR 30 still advertised by git ls-remote (github.com retains head refs)
			plumbing.NewReferenceFromStrings("refs/pull/30/head", "3030303030303030303030303030303030303030"),
		}

		diff := DiffRefs(storedBranches, storedCRs, refs)
		if diff.HasChanges {
			t.Errorf("expected HasChanges=false for closed PR with surviving head ref, got true (openedPRs: %v, syncPRs: %v)",
				diff.OpenedPRs, diff.SyncPRs)
		}
	})

	t.Run("tag noise ignored", func(t *testing.T) {
		refs := []*plumbing.Reference{
			plumbing.NewReferenceFromStrings("refs/heads/main", "aaaa111111111111111111111111111111111111"),
			plumbing.NewReferenceFromStrings("refs/heads/release-v1", "bbbb222222222222222222222222222222222222"),
			plumbing.NewReferenceFromStrings("refs/pull/10/head", "cccc333333333333333333333333333333333333"),
			plumbing.NewReferenceFromStrings("refs/pull/20/head", "dddd444444444444444444444444444444444444"),
			// Added tags and merge refs
			plumbing.NewReferenceFromStrings("refs/tags/v1.0.0", "aaaa111111111111111111111111111111111111"),
			plumbing.NewReferenceFromStrings("refs/tags/v2.0.0-rc1", "eeee555555555555555555555555555555555555"),
			plumbing.NewReferenceFromStrings("refs/pull/10/merge", "ffff666666666666666666666666666666666666"),
			plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main")),
		}

		diff := DiffRefs(storedBranches, storedCRs, refs)
		if diff.HasChanges {
			t.Errorf("expected tag noise and HEAD to be ignored (HasChanges=false), got true")
		}
	})

	t.Run("new branch detected", func(t *testing.T) {
		refs := []*plumbing.Reference{
			plumbing.NewReferenceFromStrings("refs/heads/main", "aaaa111111111111111111111111111111111111"),
			plumbing.NewReferenceFromStrings("refs/heads/release-v1", "bbbb222222222222222222222222222222222222"),
			plumbing.NewReferenceFromStrings("refs/heads/feat-new", "1111222233334444555566667777888899990000"),
			plumbing.NewReferenceFromStrings("refs/pull/10/head", "cccc333333333333333333333333333333333333"),
			plumbing.NewReferenceFromStrings("refs/pull/20/head", "dddd444444444444444444444444444444444444"),
		}

		diff := DiffRefs(storedBranches, storedCRs, refs)
		if !diff.HasChanges {
			t.Fatal("expected HasChanges=true for new branch")
		}
		if len(diff.NewBranches) != 1 || diff.NewBranches[0].Name != "feat-new" {
			t.Errorf("expected NewBranches to have 'feat-new', got %+v", diff.NewBranches)
		}
	})

	t.Run("force push on branch detected", func(t *testing.T) {
		refs := []*plumbing.Reference{
			// main SHA changed (force-push or fast-forward)
			plumbing.NewReferenceFromStrings("refs/heads/main", "9999999999999999999999999999999999999999"),
			plumbing.NewReferenceFromStrings("refs/heads/release-v1", "bbbb222222222222222222222222222222222222"),
			plumbing.NewReferenceFromStrings("refs/pull/10/head", "cccc333333333333333333333333333333333333"),
			plumbing.NewReferenceFromStrings("refs/pull/20/head", "dddd444444444444444444444444444444444444"),
		}

		diff := DiffRefs(storedBranches, storedCRs, refs)
		if !diff.HasChanges {
			t.Fatal("expected HasChanges=true for updated branch")
		}
		if len(diff.UpdatedBranches) != 1 || diff.UpdatedBranches[0].Name != "main" {
			t.Errorf("expected UpdatedBranches to have 'main', got %+v", diff.UpdatedBranches)
		}
	})

	t.Run("pr opened detected", func(t *testing.T) {
		refs := []*plumbing.Reference{
			plumbing.NewReferenceFromStrings("refs/heads/main", "aaaa111111111111111111111111111111111111"),
			plumbing.NewReferenceFromStrings("refs/heads/release-v1", "bbbb222222222222222222222222222222222222"),
			plumbing.NewReferenceFromStrings("refs/pull/10/head", "cccc333333333333333333333333333333333333"),
			plumbing.NewReferenceFromStrings("refs/pull/20/head", "dddd444444444444444444444444444444444444"),
			// New PR #40
			plumbing.NewReferenceFromStrings("refs/pull/40/head", "4444444444444444444444444444444444444444"),
		}

		diff := DiffRefs(storedBranches, storedCRs, refs)
		if !diff.HasChanges {
			t.Fatal("expected HasChanges=true for opened PR")
		}
		if len(diff.OpenedPRs) != 1 || diff.OpenedPRs[0] != 40 {
			t.Errorf("expected OpenedPRs to contain 40, got %+v", diff.OpenedPRs)
		}
	})

	t.Run("pr synchronised detected", func(t *testing.T) {
		refs := []*plumbing.Reference{
			plumbing.NewReferenceFromStrings("refs/heads/main", "aaaa111111111111111111111111111111111111"),
			plumbing.NewReferenceFromStrings("refs/heads/release-v1", "bbbb222222222222222222222222222222222222"),
			// PR 10 updated commit SHA
			plumbing.NewReferenceFromStrings("refs/pull/10/head", "1010101010101010101010101010101010101010"),
			plumbing.NewReferenceFromStrings("refs/pull/20/head", "dddd444444444444444444444444444444444444"),
		}

		diff := DiffRefs(storedBranches, storedCRs, refs)
		if !diff.HasChanges {
			t.Fatal("expected HasChanges=true for synchronized PR")
		}
		if len(diff.SyncPRs) != 1 || diff.SyncPRs[0] != 10 {
			t.Errorf("expected SyncPRs to contain 10, got %+v", diff.SyncPRs)
		}
	})

	t.Run("pr closed detected", func(t *testing.T) {
		refs := []*plumbing.Reference{
			plumbing.NewReferenceFromStrings("refs/heads/main", "aaaa111111111111111111111111111111111111"),
			plumbing.NewReferenceFromStrings("refs/heads/release-v1", "bbbb222222222222222222222222222222222222"),
			// PR 20 removed from remote refs
			plumbing.NewReferenceFromStrings("refs/pull/10/head", "cccc333333333333333333333333333333333333"),
		}

		diff := DiffRefs(storedBranches, storedCRs, refs)
		if !diff.HasChanges {
			t.Fatal("expected HasChanges=true for closed PR")
		}
		if len(diff.ClosedPRs) != 1 || diff.ClosedPRs[0] != 20 {
			t.Errorf("expected ClosedPRs to contain 20, got %+v", diff.ClosedPRs)
		}
	})
}
