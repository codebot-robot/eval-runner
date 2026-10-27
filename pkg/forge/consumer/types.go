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

package consumer

import (
	"strings"

	"github.com/gke-labs/eval-runner/pkg/forge"
)

const (
	// LabelPR tracks the pull request number on created Runs and Comparisons.
	LabelPR = "evalrunner.gke-labs.dev/pr"

	// LabelPRState tracks the pull request state ("open", "merged", "closed") on Comparisons.
	LabelPRState = "evalrunner.gke-labs.dev/pr-state"

	// StateOpen is the PR state label value for open pull requests.
	StateOpen = "open"

	// StateMerged is the PR state label value for merged pull requests.
	StateMerged = "merged"

	// StateClosed is the PR state label value for closed pull requests.
	StateClosed = "closed"

	// LabelTargetRepo tracks the target repository identifier on created Runs and Comparisons.
	LabelTargetRepo = "evalrunner.gke-labs.dev/target-repo"

	// LabelRepo tracks the repository identifier on created Runs and Comparisons.
	LabelRepo = "evalrunner.gke-labs.dev/repo"

	// LabelHeadSHA tracks the git commit SHA on created Runs and Comparisons.
	LabelHeadSHA = "evalrunner.gke-labs.dev/head-sha"

	// LabelBranch tracks the branch name on created baseline Runs.
	LabelBranch = "evalrunner.gke-labs.dev/branch"

	// AnnotationCommitTime tracks the commit timestamp (RFC3339) on created baseline Runs.
	AnnotationCommitTime = "evalrunner.gke-labs.dev/commit-time"

	// DefaultNamespace is the fallback namespace for Runs and Comparisons.
	DefaultNamespace = "eval-runner"

	// DefaultBatchSize is the default initial sample batch size.
	DefaultBatchSize = 8

	// DefaultMaxSamplesPerTask is the default cap for adaptive task samples.
	DefaultMaxSamplesPerTask = 64
)

// EventKind identifies the kind of event emitted by the Syncer or webhook.
type EventKind string

const (
	// EventBranchAdvanced is emitted when a branch head changes SHA or a new branch is discovered.
	EventBranchAdvanced EventKind = "BranchAdvanced"

	// EventChangeOpened is emitted when a new change request is discovered or transitions from closed to open.
	EventChangeOpened EventKind = "ChangeOpened"

	// EventChangeUpdated is emitted when an open change request's head SHA or metadata changes.
	EventChangeUpdated EventKind = "ChangeUpdated"

	// EventChangeClosed is emitted when a change request transitions to closed or merged.
	EventChangeClosed EventKind = "ChangeClosed"
)

// Event represents a typed event processed by Consumer.
type Event interface {
	EventKind() EventKind
	Repository() forge.RepoRef
}

// BranchAdvancedEvent is emitted when a branch advances to a new commit SHA.
type BranchAdvancedEvent struct {
	Repo       forge.RepoRef
	Branch     string
	OldSHA     string
	NewSHA     string
	Commit     *forge.Commit
	CommitTime string
	CloneURL   string
}

func (e BranchAdvancedEvent) EventKind() EventKind      { return EventBranchAdvanced }
func (e BranchAdvancedEvent) Repository() forge.RepoRef { return e.Repo }

// ChangeOpenedEvent is emitted when a change request is opened.
type ChangeOpenedEvent struct {
	Repo              forge.RepoRef
	Change            forge.ChangeRequest
	CandidateCloneURL string
	BaselineCloneURL  string
}

func (e ChangeOpenedEvent) EventKind() EventKind      { return EventChangeOpened }
func (e ChangeOpenedEvent) Repository() forge.RepoRef { return e.Repo }

// ChangeUpdatedEvent is emitted when an open change request is updated.
type ChangeUpdatedEvent struct {
	Repo              forge.RepoRef
	OldHeadSHA        string
	Change            forge.ChangeRequest
	CandidateCloneURL string
	BaselineCloneURL  string
}

func (e ChangeUpdatedEvent) EventKind() EventKind      { return EventChangeUpdated }
func (e ChangeUpdatedEvent) Repository() forge.RepoRef { return e.Repo }

// ChangeClosedEvent is emitted when a change request is closed or merged.
type ChangeClosedEvent struct {
	Repo   forge.RepoRef
	Change forge.ChangeRequest
}

func (e ChangeClosedEvent) EventKind() EventKind      { return EventChangeClosed }
func (e ChangeClosedEvent) Repository() forge.RepoRef { return e.Repo }

// RepoConfig specifies a repository to track and its baseline branches.
type RepoConfig struct {
	Name     string        `json:"name"`
	Ref      forge.RepoRef `json:"ref,omitempty"`
	CloneURL string        `json:"cloneURL,omitempty"`
	Branches []string      `json:"branches,omitempty"`
}

// RepoGetter retrieves stored repository metadata by reference.
type RepoGetter interface {
	GetRepository(repo forge.RepoRef) (*forge.Repository, bool)
}

// ChangeRequestGetter retrieves stored change requests by reference and number or ID.
type ChangeRequestGetter interface {
	GetChangeRequest(repo forge.RepoRef, numberOrID string) (*forge.ChangeRequest, error)
}

// Config holds configuration options for Consumer.
type Config struct {
	Namespace                string
	DefaultModel             string
	DefaultCohort            string
	DefaultBatchSize         int
	DefaultMaxSamplesPerTask int
	DefaultBudgetUSD         *float64
	TrackedRepos             []RepoConfig
	IsMultiRepo              bool
	Store                    RepoGetter
}

// IsMultiRepo reports whether a repository allow-list or configuration permits more than one repository.
// Exactly one exported multi-repository naming rule used by both controller and webhook binaries.
// A wildcard "*" entry permits multiple repositories. Duplicate repository entries are normalized and deduplicated.
func IsMultiRepo(repos []string) bool {
	seen := make(map[string]bool)
	for _, r := range repos {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if r == "*" {
			return true
		}
		seen[strings.ToLower(r)] = true
	}
	return len(seen) > 1
}
