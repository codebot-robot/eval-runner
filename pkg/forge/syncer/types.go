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
	"fmt"
	"strings"

	"github.com/gke-labs/eval-runner/pkg/forge"
	"sigs.k8s.io/yaml"
)

// EventKind identifies the kind of event emitted by the Syncer.
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

// Event represents a typed event emitted when forge data changes.
type Event interface {
	EventKind() EventKind
	Repository() forge.RepoRef
}

// BranchAdvancedEvent is emitted when a branch advances to a new commit SHA.
type BranchAdvancedEvent struct {
	Repo   forge.RepoRef
	Branch string
	OldSHA string
	NewSHA string
	Commit *forge.Commit
}

func (e BranchAdvancedEvent) EventKind() EventKind      { return EventBranchAdvanced }
func (e BranchAdvancedEvent) Repository() forge.RepoRef { return e.Repo }

// ChangeOpenedEvent is emitted when a change request is opened.
type ChangeOpenedEvent struct {
	Repo   forge.RepoRef
	Change forge.ChangeRequest
}

func (e ChangeOpenedEvent) EventKind() EventKind      { return EventChangeOpened }
func (e ChangeOpenedEvent) Repository() forge.RepoRef { return e.Repo }

// ChangeUpdatedEvent is emitted when an open change request is updated.
type ChangeUpdatedEvent struct {
	Repo       forge.RepoRef
	OldHeadSHA string
	Change     forge.ChangeRequest
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
	Branches []string      `json:"branches,omitempty"`
}

// Hint provides an event hint to the syncer, such as from an incoming webhook.
type Hint struct {
	Repo          forge.RepoRef
	Branch        string
	CommitSHA     string
	CommitTime    string
	CloneURL      string
	ChangeRequest *forge.ChangeRequest
	Action        string // "opened", "synchronize", "reopened", "closed"
}

// ParseRepoConfigList parses repository configuration from a YAML list.
func ParseRepoConfigList(raw string) ([]RepoConfig, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	var structured []RepoConfig
	if err := yaml.Unmarshal([]byte(trimmed), &structured); err != nil {
		return nil, fmt.Errorf("invalid repository list YAML: %w", err)
	}

	if len(structured) == 0 {
		return nil, fmt.Errorf("empty repository list")
	}

	for i := range structured {
		item := &structured[i]
		if strings.TrimSpace(item.Name) == "" {
			return nil, fmt.Errorf("repository entry at index %d has empty name", i)
		}
		ref, ok := parseRepoRefString(item.Name)
		if !ok || !ref.IsValid() {
			return nil, fmt.Errorf("invalid repository %q: invalid host, owner, or repo name", item.Name)
		}
		item.Ref = ref
		if len(item.Branches) == 0 {
			item.Branches = []string{"main"}
		}
	}

	return structured, nil
}

func parseRepoRefString(raw string) (forge.RepoRef, bool) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") || strings.Contains(raw, "@") {
		return forge.ParseRepoURL(raw)
	}

	// If no scheme, check if host is included: e.g. "github.com/owner/repo"
	parts := strings.Split(raw, "/")
	if len(parts) == 3 && forge.IsValidRepoHost(parts[0]) {
		ref := forge.RepoRef{
			Host:  strings.ToLower(parts[0]),
			Owner: parts[1],
			Name:  parts[2],
		}
		if ref.IsValid() {
			return ref, true
		}
	}

	// Fallback to owner/repo assuming github.com
	if len(parts) == 2 {
		return forge.RepoRefFromOwnerName(raw)
	}

	return forge.RepoRef{}, false
}
