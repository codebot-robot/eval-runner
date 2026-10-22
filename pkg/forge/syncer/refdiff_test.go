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
	"reflect"
	"testing"

	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/go-git/go-git/v5/plumbing"
)

func ref(name, hash string) *plumbing.Reference {
	return plumbing.NewReferenceFromStrings(name, hash)
}

func symbolicRef(name, target string) *plumbing.Reference {
	return plumbing.NewSymbolicReference(plumbing.ReferenceName(name), plumbing.ReferenceName(target))
}

func TestDiffRefs_Table(t *testing.T) {
	repoRef := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	const (
		shaMain     = "1111111111111111111111111111111111111111"
		shaFeat     = "2222222222222222222222222222222222222222"
		shaFeatPush = "2222222222222222222222222222222222229999"
		shaOld      = "3333333333333333333333333333333333333333"
		shaNew      = "4444444444444444444444444444444444444444"
		shaPR1      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		shaPR1Sync  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		shaPR2      = "cccccccccccccccccccccccccccccccccccccccc"
		shaPR10     = "dddddddddddddddddddddddddddddddddddddddd"
		shaPR99     = "9999999999999999999999999999999999999999"
		shaTag1     = "5555555555555555555555555555555555555555"
		shaTag2     = "6666666666666666666666666666666666666666"
		shaMerge    = "7777777777777777777777777777777777777777"
	)

	tests := []struct {
		name           string
		storedBranches []forge.Branch
		storedCRs      []forge.ChangeRequest
		remoteRefs     []*plumbing.Reference
		want           RefDiff
	}{
		{
			name: "unchanged state: branches and open PR match",
			storedBranches: []forge.Branch{
				{Name: "main", SHA: shaMain},
			},
			storedCRs: []forge.ChangeRequest{
				{
					Number: 1,
					State:  "open",
					Head:   forge.ChangeRequestHead{SHA: shaPR1, RepoRef: repoRef},
				},
			},
			remoteRefs: []*plumbing.Reference{
				ref("refs/heads/main", shaMain),
				ref("refs/pull/1/head", shaPR1),
			},
			want: RefDiff{
				HasChanges: false,
			},
		},
		{
			name: "closed PR with surviving head ref produces zero changes",
			storedBranches: []forge.Branch{
				{Name: "main", SHA: shaMain},
			},
			storedCRs: []forge.ChangeRequest{
				{
					Number: 99,
					State:  "closed", // surviving head for closed PR
					Head:   forge.ChangeRequestHead{SHA: shaPR99, RepoRef: repoRef},
				},
			},
			remoteRefs: []*plumbing.Reference{
				ref("refs/heads/main", shaMain),
				ref("refs/pull/99/head", shaPR99), // GitHub retains head forever
			},
			want: RefDiff{
				HasChanges: false,
			},
		},
		{
			name: "force-push on branch detected in UpdatedBranches",
			storedBranches: []forge.Branch{
				{Name: "main", SHA: shaMain},
				{Name: "feature", SHA: shaFeat},
			},
			remoteRefs: []*plumbing.Reference{
				ref("refs/heads/main", shaMain),
				ref("refs/heads/feature", shaFeatPush), // force-pushed
			},
			want: RefDiff{
				UpdatedBranches: []forge.Branch{
					{Name: "feature", SHA: shaFeatPush},
				},
				HasChanges: true,
			},
		},
		{
			name: "deletion of branch detected in DeletedBranches",
			storedBranches: []forge.Branch{
				{Name: "main", SHA: shaMain},
				{Name: "old-feature", SHA: shaOld},
			},
			remoteRefs: []*plumbing.Reference{
				ref("refs/heads/main", shaMain),
			},
			want: RefDiff{
				DeletedBranches: []string{"old-feature"},
				HasChanges:      true,
			},
		},
		{
			name: "new branch detected in NewBranches",
			storedBranches: []forge.Branch{
				{Name: "main", SHA: shaMain},
			},
			remoteRefs: []*plumbing.Reference{
				ref("refs/heads/main", shaMain),
				ref("refs/heads/new-topic", shaNew),
			},
			want: RefDiff{
				NewBranches: []forge.Branch{
					{Name: "new-topic", SHA: shaNew},
				},
				HasChanges: true,
			},
		},
		{
			name: "tags, HEAD, and merge refs are ignored",
			storedBranches: []forge.Branch{
				{Name: "main", SHA: shaMain},
			},
			storedCRs: []forge.ChangeRequest{
				{
					Number: 1,
					State:  "open",
					Head:   forge.ChangeRequestHead{SHA: shaPR1, RepoRef: repoRef},
				},
			},
			remoteRefs: []*plumbing.Reference{
				symbolicRef("HEAD", "refs/heads/main"),
				ref("refs/tags/v1.0.0", shaTag1),
				ref("refs/tags/release-2026", shaTag2),
				ref("refs/pull/1/merge", shaMerge),
				ref("refs/heads/main", shaMain),
				ref("refs/pull/1/head", shaPR1),
			},
			want: RefDiff{
				HasChanges: false,
			},
		},
		{
			name: "PR opened and synchronised",
			storedBranches: []forge.Branch{
				{Name: "main", SHA: shaMain},
			},
			storedCRs: []forge.ChangeRequest{
				{
					Number: 1,
					State:  "open",
					Head:   forge.ChangeRequestHead{SHA: shaPR1, RepoRef: repoRef},
				},
			},
			remoteRefs: []*plumbing.Reference{
				ref("refs/heads/main", shaMain),
				ref("refs/pull/1/head", shaPR1Sync), // synchronised
				ref("refs/pull/2/head", shaPR2),     // newly opened
			},
			want: RefDiff{
				OpenedPRs:  []int{2},
				SyncPRs:    []int{1},
				HasChanges: true,
			},
		},
		{
			name: "open PR closed when head removed from remote",
			storedBranches: []forge.Branch{
				{Name: "main", SHA: shaMain},
			},
			storedCRs: []forge.ChangeRequest{
				{
					Number: 10,
					State:  "open",
					Head:   forge.ChangeRequestHead{SHA: shaPR10, RepoRef: repoRef},
				},
			},
			remoteRefs: []*plumbing.Reference{
				ref("refs/heads/main", shaMain),
			},
			want: RefDiff{
				ClosedPRs:  []int{10},
				HasChanges: true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DiffRefs(tc.storedBranches, tc.storedCRs, tc.remoteRefs)

			// Normalize empty vs nil slices for deep equal
			if len(got.NewBranches) == 0 {
				got.NewBranches = nil
			}
			if len(got.UpdatedBranches) == 0 {
				got.UpdatedBranches = nil
			}
			if len(got.DeletedBranches) == 0 {
				got.DeletedBranches = nil
			}
			if len(got.OpenedPRs) == 0 {
				got.OpenedPRs = nil
			}
			if len(got.SyncPRs) == 0 {
				got.SyncPRs = nil
			}
			if len(got.ClosedPRs) == 0 {
				got.ClosedPRs = nil
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("DiffRefs mismatch:\nGot:  %+v\nWant: %+v", got, tc.want)
			}
		})
	}
}
