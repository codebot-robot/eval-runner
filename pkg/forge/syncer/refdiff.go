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
	"sort"
	"strconv"
	"strings"

	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/go-git/go-git/v5/plumbing"
)

// RefDiff contains differences between advertised remote refs and local stored refs.
type RefDiff struct {
	NewBranches     []forge.Branch
	UpdatedBranches []forge.Branch
	DeletedBranches []string
	OpenedPRs       []int
	SyncPRs         []int
	ClosedPRs       []int
	HasChanges      bool
}

// DiffRefs compares advertised remote refs against stored branches and all stored change requests.
// It keys PR head comparison by PR number and head SHA.
// Tags, HEAD, and merge refs are ignored.
func DiffRefs(storedBranches []forge.Branch, storedCRs []forge.ChangeRequest, remoteRefs []*plumbing.Reference) RefDiff {
	remoteBranches := make(map[string]string)
	remotePullHeads := make(map[int]string)

	for _, r := range remoteRefs {
		if r == nil {
			continue
		}
		// Ignore tags and HEAD
		if r.Name().IsTag() || r.Name() == plumbing.HEAD {
			continue
		}

		name := r.Name().String()
		if strings.HasPrefix(name, "refs/pull/") {
			trimmed := strings.TrimPrefix(name, "refs/pull/")
			parts := strings.Split(trimmed, "/")
			if len(parts) == 2 && parts[1] == "head" {
				if num, err := strconv.Atoi(parts[0]); err == nil && num > 0 {
					remotePullHeads[num] = r.Hash().String()
				}
			}
			continue
		}

		if r.Name().IsBranch() {
			remoteBranches[r.Name().Short()] = r.Hash().String()
		}
	}

	diff := RefDiff{}

	// Diff branches
	storedBranchMap := make(map[string]string)
	for _, b := range storedBranches {
		storedBranchMap[b.Name] = b.SHA
	}

	for name, sha := range remoteBranches {
		if storedSHA, exists := storedBranchMap[name]; exists {
			if storedSHA != sha {
				diff.UpdatedBranches = append(diff.UpdatedBranches, forge.Branch{
					Name: name,
					SHA:  sha,
				})
			}
		} else {
			diff.NewBranches = append(diff.NewBranches, forge.Branch{
				Name: name,
				SHA:  sha,
			})
		}
	}

	for name := range storedBranchMap {
		if _, exists := remoteBranches[name]; !exists {
			diff.DeletedBranches = append(diff.DeletedBranches, name)
		}
	}

	// Diff change requests against EVERYTHING stored (keyed by PR number and head SHA)
	storedPRMap := make(map[int]string)
	storedOpenPRs := make(map[int]bool)
	for _, cr := range storedCRs {
		storedPRMap[cr.Number] = cr.Head.SHA
		if strings.EqualFold(cr.State, "open") {
			storedOpenPRs[cr.Number] = true
		}
	}

	for num, sha := range remotePullHeads {
		if storedSHA, exists := storedPRMap[num]; exists {
			if storedSHA != sha {
				diff.SyncPRs = append(diff.SyncPRs, num)
			}
			// If storedSHA == sha, PR number was seen and head SHA matches: NO API call triggered!
		} else {
			// PR number never seen before: opened!
			diff.OpenedPRs = append(diff.OpenedPRs, num)
		}
	}

	for num := range storedOpenPRs {
		if _, exists := remotePullHeads[num]; !exists {
			diff.ClosedPRs = append(diff.ClosedPRs, num)
		}
	}

	diff.HasChanges = len(diff.NewBranches) > 0 ||
		len(diff.UpdatedBranches) > 0 ||
		len(diff.DeletedBranches) > 0 ||
		len(diff.OpenedPRs) > 0 ||
		len(diff.SyncPRs) > 0 ||
		len(diff.ClosedPRs) > 0

	sort.Slice(diff.NewBranches, func(i, j int) bool { return diff.NewBranches[i].Name < diff.NewBranches[j].Name })
	sort.Slice(diff.UpdatedBranches, func(i, j int) bool { return diff.UpdatedBranches[i].Name < diff.UpdatedBranches[j].Name })
	sort.Strings(diff.DeletedBranches)
	sort.Ints(diff.OpenedPRs)
	sort.Ints(diff.SyncPRs)
	sort.Ints(diff.ClosedPRs)

	return diff
}
