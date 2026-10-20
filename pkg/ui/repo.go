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

package ui

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultNamespace is the standard fallback namespace for eval-runner.
const DefaultNamespace = "eval-runner"

var (
	hostRegex      = regexp.MustCompile(`^[a-z0-9.-]+\.[a-z]+$`)
	ownerRepoRegex = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

func isValidHost(host string) bool {
	return hostRegex.MatchString(strings.ToLower(strings.TrimSpace(host)))
}

func isValidOwnerOrRepo(val string) bool {
	v := strings.TrimSpace(val)
	if v == "." || v == ".." {
		return false
	}
	return ownerRepoRegex.MatchString(v)
}

// RepoRef identifies a repository by host, owner, and repo name.
type RepoRef struct {
	Host  string
	Owner string
	Name  string
}

// Path returns the URL path prefix "/host/owner/name" for this repository.
func (r RepoRef) Path() string {
	return fmt.Sprintf("/%s/%s/%s", r.Host, r.Owner, r.Name)
}

// String returns "host/owner/name" for this repository.
func (r RepoRef) String() string {
	return fmt.Sprintf("%s/%s/%s", r.Host, r.Owner, r.Name)
}

// Equal performs a case-insensitive equality comparison against another RepoRef.
func (r RepoRef) Equal(other RepoRef) bool {
	return strings.EqualFold(r.Host, other.Host) &&
		strings.EqualFold(r.Owner, other.Owner) &&
		strings.EqualFold(r.Name, other.Name)
}

// IsZero reports whether the RepoRef is empty.
func (r RepoRef) IsZero() bool {
	return r.Host == "" && r.Owner == "" && r.Name == ""
}

// ParseRepoURL parses a repository URL in HTTPS, SSH, or git formats:
// - https://host/owner/name(.git)
// - http://host/owner/name(.git)
// - git@host:owner/name(.git)
// - ssh://git@host/owner/name(.git)
//
// Host is lowercased. Host, owner, and name are validated against the route regexes.
func ParseRepoURL(rawURL string) (RepoRef, bool) {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return RepoRef{}, false
	}

	var host, owner, name string

	// 1. SCP-like syntax: git@host:owner/name(.git) or user@host:owner/name(.git)
	if atIdx := strings.Index(s, "@"); atIdx != -1 && !strings.Contains(s, "://") {
		colonIdx := strings.Index(s, ":")
		if colonIdx > atIdx {
			host = s[atIdx+1 : colonIdx]
			path := s[colonIdx+1:]
			path = strings.TrimPrefix(path, "/")
			path = strings.TrimSuffix(path, "/")
			path = strings.TrimSuffix(path, ".git")
			path = strings.TrimSuffix(path, "/")
			parts := strings.Split(path, "/")
			if len(parts) == 2 {
				owner, name = parts[0], parts[1]
			}
		}
	} else if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return RepoRef{}, false
		}
		host = u.Hostname()
		path := strings.TrimPrefix(u.Path, "/")
		path = strings.TrimSuffix(path, "/")
		path = strings.TrimSuffix(path, ".git")
		path = strings.TrimSuffix(path, "/")
		parts := strings.Split(path, "/")
		if len(parts) == 2 {
			owner, name = parts[0], parts[1]
		}
	}

	host = strings.ToLower(strings.TrimSpace(host))
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)

	if !isValidHost(host) || !isValidOwnerOrRepo(owner) || !isValidOwnerOrRepo(name) {
		return RepoRef{}, false
	}
	return RepoRef{Host: host, Owner: owner, Name: name}, true
}

// RepoRefFromOwnerName creates a RepoRef with host "github.com" from an "owner/name" string.
func RepoRefFromOwnerName(ownerName string) (RepoRef, bool) {
	s := strings.TrimSpace(ownerName)
	s = strings.TrimPrefix(s, "/")
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return RepoRef{}, false
	}
	owner := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	if !isValidOwnerOrRepo(owner) || !isValidOwnerOrRepo(name) {
		return RepoRef{}, false
	}
	return RepoRef{Host: "github.com", Owner: owner, Name: name}, true
}

// repoLink returns a URL path for the repository with the given suffix, appending
// "?namespace=" when ns differs from DefaultNamespace.
func repoLink(ref RepoRef, suffix string, ns string) string {
	var base string
	if ref.Host != "" {
		base = ref.Path() + suffix
	} else {
		base = suffix
		if base == "" {
			base = "/"
		}
	}
	if ns != "" && ns != DefaultNamespace {
		sep := "?"
		if strings.Contains(base, "?") {
			sep = "&"
		}
		base += sep + "namespace=" + url.QueryEscape(ns)
	}
	return base
}

// repoIndexLink returns the repo index link ("/" or "/?namespace=...").
func repoIndexLink(ns string) string {
	return repoLink(RepoRef{}, "", ns)
}

// runRepoRef resolves the RepoRef of a Run via spec.repo.url, then the
// "evalrunner.gke-labs.dev/repo" label.
func runRepoRef(run *v1alpha1.Run) (RepoRef, bool) {
	if run == nil {
		return RepoRef{}, false
	}
	if run.Spec.Repo.URL != "" {
		if ref, ok := ParseRepoURL(run.Spec.Repo.URL); ok {
			return ref, true
		}
		if ref, ok := RepoRefFromOwnerName(run.Spec.Repo.URL); ok {
			return ref, true
		}
	}
	if labelVal, ok := run.Labels["evalrunner.gke-labs.dev/repo"]; ok && labelVal != "" {
		ownerName := strings.ReplaceAll(labelVal, "_", "/")
		if ref, ok := RepoRefFromOwnerName(ownerName); ok {
			return ref, true
		}
	}
	return RepoRef{}, false
}

// comparisonRepoRef resolves the RepoRef of a Comparison via spec.pullRequest.repository,
// falling back to the baseline Run's URL (fork candidates must not re-home a PR).
func comparisonRepoRef(ctx context.Context, comp *v1alpha1.Comparison, c client.Reader) (RepoRef, bool) {
	if comp == nil {
		return RepoRef{}, false
	}
	if comp.Spec.PullRequest != nil && comp.Spec.PullRequest.Repository != "" {
		if ref, ok := ParseRepoURL(comp.Spec.PullRequest.Repository); ok {
			return ref, true
		}
		if ref, ok := RepoRefFromOwnerName(comp.Spec.PullRequest.Repository); ok {
			return ref, true
		}
	}
	if comp.Spec.Baseline.Run != "" && c != nil {
		var baseRun v1alpha1.Run
		if err := c.Get(ctx, client.ObjectKey{Namespace: comp.Namespace, Name: comp.Spec.Baseline.Run}, &baseRun); err != nil {
			return RepoRef{}, false
		}
		return runRepoRef(&baseRun)
	}
	return RepoRef{}, false
}
