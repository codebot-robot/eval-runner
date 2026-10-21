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

var (
	hostRegex        = regexp.MustCompile(`^[a-z0-9.-]+$`)
	repoSegmentRegex = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// IsValidRepoHost checks if a host contains a dot, matches ^[a-z0-9.-]+$, and is not "." or "..".
func IsValidRepoHost(h string) bool {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "." || h == ".." {
		return false
	}
	if !strings.Contains(h, ".") {
		return false
	}
	return hostRegex.MatchString(h)
}

// IsValidRepoOwner checks if an owner matches ^[A-Za-z0-9_.-]+$ or is "-", and is not "." or "..".
func IsValidRepoOwner(o string) bool {
	o = strings.TrimSpace(o)
	if o == "." || o == ".." {
		return false
	}
	if o == "-" {
		return true
	}
	return repoSegmentRegex.MatchString(o)
}

// IsValidRepoName checks if a repository name matches ^[A-Za-z0-9_.-]+$ or is "-", and is not "." or "..".
func IsValidRepoName(n string) bool {
	n = strings.TrimSpace(n)
	if n == "." || n == ".." {
		return false
	}
	if n == "-" {
		return true
	}
	return repoSegmentRegex.MatchString(n)
}

// RepoRef identifies a repository by host, owner, and repository name.
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

// IsValid reports whether all segments of the RepoRef pass validation.
func (r RepoRef) IsValid() bool {
	return IsValidRepoHost(r.Host) && IsValidRepoOwner(r.Owner) && IsValidRepoName(r.Name)
}

// ParseRepoURL parses a repository clone URL or scp-style address into a RepoRef.
// It accepts any URL scheme (https, ssh, git protocol, etc.) as well as scp-style
// addresses (e.g. user@host:owner/name.git). Hosts with ports have their port
// stripped. Trailing slashes and .git extensions are trimmed. Single-segment
// clone URLs receive the reserved placeholder owner "-" (a single hyphen).
func ParseRepoURL(rawURL string) (RepoRef, bool) {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return RepoRef{}, false
	}

	var host, path string

	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return RepoRef{}, false
		}
		host = u.Hostname()
		path = u.Path
	} else if colonIdx := strings.Index(s, ":"); colonIdx != -1 {
		// SCP-style syntax: [user@]host:path
		beforeColon := s[:colonIdx]
		path = s[colonIdx+1:]
		if atIdx := strings.LastIndex(beforeColon, "@"); atIdx != -1 {
			host = beforeColon[atIdx+1:]
		} else {
			host = beforeColon
		}
	} else {
		return RepoRef{}, false
	}

	host = strings.ToLower(strings.TrimSpace(host))

	// Clean path: trim leading/trailing slashes and .git
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if path == "" {
		return RepoRef{}, false
	}

	parts := strings.Split(path, "/")
	var owner, name string
	if len(parts) == 1 {
		owner = "-"
		name = parts[0]
	} else if len(parts) == 2 {
		owner = parts[0]
		name = parts[1]
	} else {
		return RepoRef{}, false
	}

	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)

	ref := RepoRef{Host: host, Owner: owner, Name: name}
	if !ref.IsValid() {
		return RepoRef{}, false
	}
	return ref, true
}

// RepoRefFromOwnerName creates a RepoRef assuming github.com host for an "owner/name" string.
func RepoRefFromOwnerName(ownerName string) (RepoRef, bool) {
	s := strings.TrimSpace(ownerName)
	s = strings.Trim(s, "/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.Trim(s, "/")
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return RepoRef{}, false
	}
	owner := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	ref := RepoRef{
		Host:  "github.com",
		Owner: owner,
		Name:  name,
	}
	if !ref.IsValid() {
		return RepoRef{}, false
	}
	return ref, true
}

// repoLink returns a URL path for the repository with the given suffix, appending
// "?namespace=" when linkNS is non-empty.
func repoLink(ref RepoRef, suffix string, linkNS string) string {
	var base string
	if ref.Host != "" {
		base = ref.Path() + suffix
	} else {
		base = suffix
		if base == "" {
			base = "/"
		}
	}
	if linkNS != "" {
		sep := "?"
		if strings.Contains(base, "?") {
			sep = "&"
		}
		base += sep + "namespace=" + url.QueryEscape(linkNS)
	}
	return base
}

// repoIndexLink returns the repository index link ("/" or "/?namespace=...").
func repoIndexLink(linkNS string) string {
	return repoLink(RepoRef{}, "", linkNS)
}

// runLink returns a URL to a run detail page. If ref.Host is set, it stays under
// the repository prefix (appending linkNS if non-empty); otherwise it uses the
// legacy /runs/{ns}/{name} format.
func runLink(ref RepoRef, runName string, linkNS string, legacyNS ...string) string {
	if ref.Host != "" {
		return repoLink(ref, "/runs/"+runName, linkNS)
	}
	ns := linkNS
	if len(legacyNS) > 0 && legacyNS[0] != "" {
		ns = legacyNS[0]
	}
	return fmt.Sprintf("/runs/%s/%s", ns, runName)
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
func comparisonRepoRef(ctx context.Context, comp *v1alpha1.Comparison, r client.Reader) (RepoRef, bool) {
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
	if comp.Spec.Baseline.Run != "" && r != nil {
		var baseRun v1alpha1.Run
		if err := r.Get(ctx, client.ObjectKey{Namespace: comp.Namespace, Name: comp.Spec.Baseline.Run}, &baseRun); err != nil {
			return RepoRef{}, false
		}
		return runRepoRef(&baseRun)
	}
	return RepoRef{}, false
}
