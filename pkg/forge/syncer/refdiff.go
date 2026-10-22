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

// RefDiff contains the detected differences between stored state and advertised remote refs.
type RefDiff struct {
	HasChanges      bool
	NewBranches     []forge.Branch
	UpdatedBranches []forge.Branch
	DeletedBranches []string
	OpenedPRs       []int
	SyncPRs         []int
	ClosedPRs       []int
	AllBranches     []forge.Branch
	RemotePRHeads   map[int]string // PR number -> head SHA
}

// DiffRefs compares stored branches and change requests against advertised remote references.
// Only new/changed refs/heads/* and refs/pull/*/head trigger changes.
// Tag noise (refs/tags/*), HEAD, and pull merge refs (refs/pull/*/merge) are ignored.
func DiffRefs(storedBranches []forge.Branch, storedCRs []forge.ChangeRequest, remoteRefs []*plumbing.Reference) RefDiff {
	remoteBranchMap := make(map[string]string)
	remotePRHeadMap := make(map[int]string)
	var allBranches []forge.Branch

	for _, r := range remoteRefs {
		if r == nil {
			continue
		}
		refName := r.Name().String()

		// 1. Tag noise ignored
		if r.Name().IsTag() || strings.HasPrefix(refName, "refs/tags/") {
			continue
		}
		// 2. Symbolic HEAD ignored
		if r.Name() == plumbing.HEAD {
			continue
		}

		// 3. refs/heads/*
		if r.Name().IsBranch() || strings.HasPrefix(refName, "refs/heads/") {
			bName := strings.TrimPrefix(refName, "refs/heads/")
			sha := r.Hash().String()
			remoteBranchMap[bName] = sha
			allBranches = append(allBranches, forge.Branch{
				Name: bName,
				SHA:  sha,
			})
			continue
		}

		// 4. refs/pull/*/head
		if strings.HasPrefix(refName, "refs/pull/") && strings.HasSuffix(refName, "/head") {
			trimmed := strings.TrimPrefix(refName, "refs/pull/")
			trimmed = strings.TrimSuffix(trimmed, "/head")
			num, err := strconv.Atoi(trimmed)
			if err == nil && num > 0 {
				remotePRHeadMap[num] = r.Hash().String()
			}
		}
	}

	sort.Slice(allBranches, func(i, j int) bool {
		return allBranches[i].Name < allBranches[j].Name
	})

	storedBranchMap := make(map[string]string)
	for _, b := range storedBranches {
		storedBranchMap[b.Name] = b.SHA
	}

	storedPRHeadMap := make(map[int]string)
	storedOpenPRMap := make(map[int]bool)
	for _, cr := range storedCRs {
		if cr.Number > 0 {
			storedPRHeadMap[cr.Number] = cr.Head.SHA
			if isCROpen(cr) {
				storedOpenPRMap[cr.Number] = true
			}
		}
	}

	diff := RefDiff{
		AllBranches:   allBranches,
		RemotePRHeads: remotePRHeadMap,
	}

	// Diff branches
	for name, newSHA := range remoteBranchMap {
		oldSHA, exists := storedBranchMap[name]
		if !exists {
			diff.NewBranches = append(diff.NewBranches, forge.Branch{Name: name, SHA: newSHA})
			diff.HasChanges = true
		} else if oldSHA != newSHA {
			diff.UpdatedBranches = append(diff.UpdatedBranches, forge.Branch{Name: name, SHA: newSHA})
			diff.HasChanges = true
		}
	}
	for name := range storedBranchMap {
		if _, exists := remoteBranchMap[name]; !exists {
			diff.DeletedBranches = append(diff.DeletedBranches, name)
			diff.HasChanges = true
		}
	}

	// Diff pull requests:
	// A refs/pull/N/head entry is an API trigger only when N has never been seen or its head SHA changed.
	// Closed/merged PRs retain their refs/pull/N/head indefinitely on github.com, so we diff
	// against all stored PRs regardless of state to avoid false triggers.
	for num, newSHA := range remotePRHeadMap {
		oldSHA, exists := storedPRHeadMap[num]
		if !exists {
			diff.OpenedPRs = append(diff.OpenedPRs, num)
			diff.HasChanges = true
		} else if oldSHA != newSHA {
			diff.SyncPRs = append(diff.SyncPRs, num)
			diff.HasChanges = true
		}
	}
	for num := range storedOpenPRMap {
		if _, exists := remotePRHeadMap[num]; !exists {
			diff.ClosedPRs = append(diff.ClosedPRs, num)
			diff.HasChanges = true
		}
	}

	sort.Slice(diff.NewBranches, func(i, j int) bool { return diff.NewBranches[i].Name < diff.NewBranches[j].Name })
	sort.Slice(diff.UpdatedBranches, func(i, j int) bool { return diff.UpdatedBranches[i].Name < diff.UpdatedBranches[j].Name })
	sort.Strings(diff.DeletedBranches)
	sort.Ints(diff.OpenedPRs)
	sort.Ints(diff.SyncPRs)
	sort.Ints(diff.ClosedPRs)

	return diff
}
