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
	"github.com/gke-labs/eval-runner/pkg/forge/consumer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RepoRef identifies a repository by host, owner, and repository name.
// Moved to pkg/forge and aliased here for backwards compatibility.
type RepoRef = forge.RepoRef

// RepoLink returns a URL path for the repository with the given suffix, appending
// "?namespace=" when linkNS is non-empty.
func RepoLink(ref RepoRef, suffix string, linkNS string) string {
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

func repoLink(ref RepoRef, suffix string, linkNS string) string {
	return RepoLink(ref, suffix, linkNS)
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

// RunRepoRef resolves the RepoRef of a Run via spec.repo.url, then the
// "evalrunner.gke-labs.dev/repo" label.
func RunRepoRef(run *v1alpha1.Run) (RepoRef, bool) {
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

func runRepoRef(run *v1alpha1.Run) (RepoRef, bool) {
	return RunRepoRef(run)
}

// targetRepoMatches checks if a sanitized target-repo label value matches ref.
func targetRepoMatches(labelVal string, ref RepoRef) bool {
	if labelVal == "" {
		return false
	}
	if strings.EqualFold(labelVal, consumer.SanitizeLabelValue(ref.Owner+"/"+ref.Name)) {
		return true
	}
	if ref.Host != "" && !strings.EqualFold(ref.Host, "github.com") {
		if strings.EqualFold(labelVal, consumer.SanitizeLabelValue(ref.Host+"/"+ref.Owner+"/"+ref.Name)) {
			return true
		}
	}
	ownerName := strings.ReplaceAll(labelVal, "_", "/")
	if tRef, ok := forge.RepoRefFromOwnerName(ownerName); ok && tRef.Equal(ref) {
		return true
	}
	return false
}

// TargetRepoRef resolves the RepoRef from labels via the "evalrunner.gke-labs.dev/target-repo" label.
func TargetRepoRef(labels map[string]string) (RepoRef, bool) {
	if labels == nil {
		return RepoRef{}, false
	}
	if labelVal, ok := labels[consumer.LabelTargetRepo]; ok && labelVal != "" {
		ownerName := strings.ReplaceAll(labelVal, "_", "/")
		if ref, ok := forge.RepoRefFromOwnerName(ownerName); ok {
			return ref, true
		}
	}
	return RepoRef{}, false
}

func targetRepoRef(run *v1alpha1.Run) (RepoRef, bool) {
	if run == nil {
		return RepoRef{}, false
	}
	return TargetRepoRef(run.Labels)
}

// runBelongsToRepo returns true if the Run belongs to the given repository ref.
// A Run belongs to ref if its "evalrunner.gke-labs.dev/target-repo" label matches ref,
// or runRepoRef matches ref, or (fallback for objects created before this change)
// a Comparison in the namespace references the Run as candidate or baseline and its
// spec.pullRequest.repository matches ref.
func runBelongsToRepo(run *v1alpha1.Run, ref RepoRef, comps ...v1alpha1.Comparison) bool {
	if run == nil || ref.IsZero() {
		return false
	}

	// 1. Target repo label match
	if run.Labels != nil {
		if labelVal, ok := run.Labels[consumer.LabelTargetRepo]; ok && labelVal != "" {
			if targetRepoMatches(labelVal, ref) {
				return true
			}
		}
	}

	// 2. Source repo match
	if runRef, ok := runRepoRef(run); ok && runRef.Equal(ref) {
		return true
	}

	// 3. Fallback: Comparison references the Run as candidate or baseline
	for i := range comps {
		comp := &comps[i]
		if comp.Spec.PullRequest == nil || comp.Spec.PullRequest.Repository == "" {
			continue
		}
		isReferenced := (comp.Spec.Candidate.Run != "" && comp.Spec.Candidate.Run == run.Name) ||
			(comp.Spec.Baseline.Run != "" && comp.Spec.Baseline.Run == run.Name)
		if !isReferenced && run.Labels != nil {
			if parentComp, ok := run.Labels["evalrunner.gke-labs.dev/comparison"]; ok && parentComp != "" && parentComp == comp.Name {
				isReferenced = true
			}
		}
		if isReferenced {
			if prRef, ok := pullRequestRepoRef(comp.Spec.PullRequest.Repository); ok && prRef.Equal(ref) {
				return true
			}
		}
	}

	return false
}

// PullRequestRepoRef parses a repository string from a PullRequestSpec.
// It tries RepoRefFromOwnerName first (for "owner/repo" format), falling back to ParseRepoURL.
func PullRequestRepoRef(repo string) (RepoRef, bool) {
	if ref, ok := forge.RepoRefFromOwnerName(repo); ok {
		return ref, true
	}
	return forge.ParseRepoURL(repo)
}

func pullRequestRepoRef(repo string) (RepoRef, bool) {
	return PullRequestRepoRef(repo)
}

// ComparisonRepoRef resolves the RepoRef of a Comparison from its pull-request spec,
// target-repo labels, or referenced candidate/baseline runs.
func ComparisonRepoRef(comp *v1alpha1.Comparison, runs ...*v1alpha1.Run) (RepoRef, bool) {
	if comp == nil {
		return RepoRef{}, false
	}
	if comp.Spec.PullRequest != nil && comp.Spec.PullRequest.Repository != "" {
		if ref, ok := PullRequestRepoRef(comp.Spec.PullRequest.Repository); ok {
			return ref, true
		}
	}
	if comp.Labels != nil {
		if ref, ok := TargetRepoRef(comp.Labels); ok {
			return ref, true
		}
	}
	for _, run := range runs {
		if run == nil {
			continue
		}
		if ref, ok := TargetRepoRef(run.Labels); ok {
			return ref, true
		}
		if ref, ok := RunRepoRef(run); ok {
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
