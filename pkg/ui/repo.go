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
	"strings"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/forge"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RepoRef identifies a repository by host, owner, and repository name.
// Moved to pkg/forge and aliased here for backwards compatibility.
type RepoRef = forge.RepoRef

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
		frag := ""
		if idx := strings.Index(base, "#"); idx != -1 {
			frag = base[idx:]
			base = base[:idx]
		}
		sep := "?"
		if strings.Contains(base, "?") {
			sep = "&"
		}
		base = base + sep + "namespace=" + url.QueryEscape(linkNS) + frag
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
		if ref, ok := forge.ParseRepoURL(run.Spec.Repo.URL); ok {
			return ref, true
		}
		if ref, ok := forge.RepoRefFromOwnerName(run.Spec.Repo.URL); ok {
			return ref, true
		}
	}
	if labelVal, ok := run.Labels["evalrunner.gke-labs.dev/repo"]; ok && labelVal != "" {
		ownerName := strings.ReplaceAll(labelVal, "_", "/")
		if ref, ok := forge.RepoRefFromOwnerName(ownerName); ok {
			return ref, true
		}
	}
	return RepoRef{}, false
}

// pullRequestRepoRef parses a repository string from a PullRequestSpec.
// It tries RepoRefFromOwnerName first (for "owner/repo" format), falling back to ParseRepoURL.
func pullRequestRepoRef(repo string) (RepoRef, bool) {
	if ref, ok := forge.RepoRefFromOwnerName(repo); ok {
		return ref, true
	}
	return forge.ParseRepoURL(repo)
}

// comparisonRepoRef resolves the RepoRef of a Comparison via spec.pullRequest.repository,
// falling back to the baseline Run's URL (fork candidates must not re-home a PR).
func comparisonRepoRef(ctx context.Context, comp *v1alpha1.Comparison, r client.Reader) (RepoRef, bool) {
	if comp == nil {
		return RepoRef{}, false
	}
	if comp.Spec.PullRequest != nil && comp.Spec.PullRequest.Repository != "" {
		if ref, ok := pullRequestRepoRef(comp.Spec.PullRequest.Repository); ok {
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
