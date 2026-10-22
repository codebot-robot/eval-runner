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
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/gke-labs/eval-runner/pkg/forge/consumer"
	"sigs.k8s.io/yaml"
)

// RepoConfig defines a repository and its baseline branches.
type RepoConfig = consumer.RepoConfig

// EventHandler consumes typed forge events synchronously before batch commits.
type EventHandler interface {
	ConsumeEvent(ctx context.Context, ev consumer.Event) error
}

// Hint provides out-of-band updates (such as webhook notifications) to be applied during a sync.
type Hint struct {
	Branch            *forge.Branch
	ChangeRequest     *forge.ChangeRequest
	CloneURL          string
	CandidateCloneURL string
	BaselineCloneURL  string
}

// ParseRepoConfigList parses the YAML list of repositories from the repositories ConfigMap key.
// Invalid entries are rejected with a clear error.
func ParseRepoConfigList(raw string) ([]RepoConfig, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	var list []RepoConfig
	if err := yaml.Unmarshal([]byte(trimmed), &list); err != nil {
		return nil, fmt.Errorf("expected a YAML list of {name, branches}: %w", err)
	}

	if len(list) == 0 {
		return nil, errors.New("expected a YAML list of {name, branches}")
	}

	for i := range list {
		name := strings.TrimSpace(list[i].Name)
		if name == "" {
			return nil, errors.New("invalid repository \"\": repository name cannot be empty")
		}

		ref, cloneURL, ok := parseRepoRefAndCloneURL(name)
		if !ok || !ref.IsValid() {
			return nil, fmt.Errorf("invalid repository %q: invalid host, owner, or repo name", name)
		}

		list[i].Ref = ref
		if list[i].CloneURL == "" {
			list[i].CloneURL = cloneURL
		}
		if len(list[i].Branches) == 0 {
			list[i].Branches = []string{"main"}
		}
	}

	return list, nil
}

func parseRepoRefAndCloneURL(raw string) (forge.RepoRef, string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return forge.RepoRef{}, "", false
	}

	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return forge.RepoRef{}, "", false
		}
		host := u.Host
		path := strings.Trim(u.Path, "/")
		path = strings.TrimSuffix(path, ".git")
		parts := strings.Split(path, "/")
		if len(parts) == 1 && parts[0] != "" {
			return forge.RepoRef{Host: host, Owner: "-", Name: parts[0]}, raw, true
		}
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			return forge.RepoRef{Host: host, Owner: parts[0], Name: parts[1]}, raw, true
		}
		return forge.RepoRef{}, "", false
	}

	parts := strings.Split(raw, "/")
	if len(parts) == 2 {
		ref, ok := forge.RepoRefFromOwnerName(raw)
		return ref, "", ok
	}
	if len(parts) == 3 {
		ref := forge.RepoRef{Host: parts[0], Owner: parts[1], Name: parts[2]}
		return ref, "", ref.IsValid()
	}

	return forge.RepoRef{}, "", false
}
