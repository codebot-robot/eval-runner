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
	"testing"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestParseRepoURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantRef RepoRef
		wantOK  bool
	}{
		// https scheme
		{
			name:    "https with .git",
			input:   "https://github.com/gke-labs/eval-runner.git",
			wantRef: RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"},
			wantOK:  true,
		},
		{
			name:    "https without .git",
			input:   "https://github.com/gke-labs/eval-runner",
			wantRef: RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"},
			wantOK:  true,
		},
		{
			name:    "http scheme",
			input:   "http://gitlab.com/group/project.git",
			wantRef: RepoRef{Host: "gitlab.com", Owner: "group", Name: "project"},
			wantOK:  true,
		},
		// ssh scheme
		{
			name:    "ssh scheme with user and .git",
			input:   "ssh://git@github.com/owner/repo.git",
			wantRef: RepoRef{Host: "github.com", Owner: "owner", Name: "repo"},
			wantOK:  true,
		},
		{
			name:    "ssh scheme with port",
			input:   "ssh://git@github.com:22/owner/repo",
			wantRef: RepoRef{Host: "github.com", Owner: "owner", Name: "repo"},
			wantOK:  true,
		},
		// git protocol
		{
			name:    "plain git protocol",
			input:   "git://git.example.com/owner/repo.git",
			wantRef: RepoRef{Host: "git.example.com", Owner: "owner", Name: "repo"},
			wantOK:  true,
		},
		{
			name:    "plain git protocol with port",
			input:   "git://git.example.com:9418/owner/repo.git",
			wantRef: RepoRef{Host: "git.example.com", Owner: "owner", Name: "repo"},
			wantOK:  true,
		},
		// scp-style
		{
			name:    "scp-style with user and .git",
			input:   "git@github.com:owner/repo.git",
			wantRef: RepoRef{Host: "github.com", Owner: "owner", Name: "repo"},
			wantOK:  true,
		},
		{
			name:    "scp-style without .git",
			input:   "user@host.example.com:owner/repo",
			wantRef: RepoRef{Host: "host.example.com", Owner: "owner", Name: "repo"},
			wantOK:  true,
		},
		{
			name:    "scp-style single-segment clone url",
			input:   "git@git-server.local:docs-eval.git",
			wantRef: RepoRef{Host: "git-server.local", Owner: "-", Name: "docs-eval"},
			wantOK:  true,
		},
		// other hosts
		{
			name:    "in-cluster git server hostname with dot",
			input:   "git://git-server.eval-runner-sandbox.svc/docs-eval.git",
			wantRef: RepoRef{Host: "git-server.eval-runner-sandbox.svc", Owner: "-", Name: "docs-eval"},
			wantOK:  true,
		},
		{
			name:    "other domain with hyphens and numbers",
			input:   "https://code-server-01.dev.corp.net/team1/evals.git",
			wantRef: RepoRef{Host: "code-server-01.dev.corp.net", Owner: "team1", Name: "evals"},
			wantOK:  true,
		},
		// port handling
		{
			name:    "https with port",
			input:   "https://github.com:8443/owner/repo.git",
			wantRef: RepoRef{Host: "github.com", Owner: "owner", Name: "repo"},
			wantOK:  true,
		},
		{
			name:    "http with custom port",
			input:   "http://git.corp.net:8080/my-owner/my-repo",
			wantRef: RepoRef{Host: "git.corp.net", Owner: "my-owner", Name: "my-repo"},
			wantOK:  true,
		},
		// trailing slash handling
		{
			name:    "trailing slash after repo",
			input:   "https://github.com/owner/repo/",
			wantRef: RepoRef{Host: "github.com", Owner: "owner", Name: "repo"},
			wantOK:  true,
		},
		{
			name:    "trailing slash after .git",
			input:   "https://github.com/owner/repo.git/",
			wantRef: RepoRef{Host: "github.com", Owner: "owner", Name: "repo"},
			wantOK:  true,
		},
		// single-segment clone URLs
		{
			name:    "single-segment with git scheme",
			input:   "git://git-server.eval-runner-sandbox.svc/docs-eval.git",
			wantRef: RepoRef{Host: "git-server.eval-runner-sandbox.svc", Owner: "-", Name: "docs-eval"},
			wantOK:  true,
		},
		{
			name:    "single-segment https without .git",
			input:   "https://git.internal.net/docs-eval",
			wantRef: RepoRef{Host: "git.internal.net", Owner: "-", Name: "docs-eval"},
			wantOK:  true,
		},
		// dot and double-dot rejected
		{
			name:   "dot as owner",
			input:  "https://github.com/./repo",
			wantOK: false,
		},
		{
			name:   "double-dot as owner",
			input:  "https://github.com/../repo",
			wantOK: false,
		},
		{
			name:   "dot as name",
			input:  "https://github.com/owner/.",
			wantOK: false,
		},
		{
			name:   "double-dot as name",
			input:  "https://github.com/owner/..",
			wantOK: false,
		},
		{
			name:   "dot as host",
			input:  "https://./owner/repo",
			wantOK: false,
		},
		{
			name:   "double-dot as host",
			input:  "https://../owner/repo",
			wantOK: false,
		},
		{
			name:   "scp dot owner",
			input:  "git@github.com:./repo.git",
			wantOK: false,
		},
		{
			name:   "scp double-dot owner",
			input:  "git@github.com:../repo.git",
			wantOK: false,
		},
		// host without dot rejected
		{
			name:   "localhost without dot",
			input:  "https://localhost/owner/repo",
			wantOK: false,
		},
		{
			name:   "internal host without dot",
			input:  "git://git-server/docs-eval.git",
			wantOK: false,
		},
		// invalid characters
		{
			name:   "invalid character in host",
			input:  "https://git_hub.com/owner/repo",
			wantOK: false,
		},
		{
			name:   "empty string",
			input:  "",
			wantOK: false,
		},
		{
			name:   "whitespace only",
			input:  "   ",
			wantOK: false,
		},
		{
			name:   "too many segments",
			input:  "https://github.com/a/b/c",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotRef, gotOK := ParseRepoURL(tc.input)
			if gotOK != tc.wantOK {
				t.Fatalf("ParseRepoURL(%q) gotOK=%v, want %v (ref=%+v)", tc.input, gotOK, tc.wantOK, gotRef)
			}
			if tc.wantOK && gotRef != tc.wantRef {
				t.Errorf("ParseRepoURL(%q) = %+v, want %+v", tc.input, gotRef, tc.wantRef)
			}
		})
	}
}

func TestRepoRefFromOwnerName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantRef RepoRef
		wantOK  bool
	}{
		{
			name:    "standard owner/repo",
			input:   "gke-labs/eval-runner",
			wantRef: RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"},
			wantOK:  true,
		},
		{
			name:    "with .git suffix",
			input:   "gke-labs/eval-runner.git",
			wantRef: RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"},
			wantOK:  true,
		},
		{
			name:    "with leading and trailing slashes",
			input:   "/gke-labs/eval-runner/",
			wantRef: RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"},
			wantOK:  true,
		},
		{
			name:    "with hyphen owner",
			input:   "-/docs-eval",
			wantRef: RepoRef{Host: "github.com", Owner: "-", Name: "docs-eval"},
			wantOK:  true,
		},
		{
			name:   "dot as owner",
			input:  "./repo",
			wantOK: false,
		},
		{
			name:   "double-dot as owner",
			input:  "../repo",
			wantOK: false,
		},
		{
			name:   "dot as name",
			input:  "owner/.",
			wantOK: false,
		},
		{
			name:   "double-dot as name",
			input:  "owner/..",
			wantOK: false,
		},
		{
			name:   "single segment",
			input:  "eval-runner",
			wantOK: false,
		},
		{
			name:   "three segments",
			input:  "a/b/c",
			wantOK: false,
		},
		{
			name:   "empty",
			input:  "",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotRef, gotOK := RepoRefFromOwnerName(tc.input)
			if gotOK != tc.wantOK {
				t.Fatalf("RepoRefFromOwnerName(%q) gotOK=%v, want %v (ref=%+v)", tc.input, gotOK, tc.wantOK, gotRef)
			}
			if tc.wantOK && gotRef != tc.wantRef {
				t.Errorf("RepoRefFromOwnerName(%q) = %+v, want %+v", tc.input, gotRef, tc.wantRef)
			}
		})
	}
}

func TestRepoRef_Methods(t *testing.T) {
	ref := RepoRef{Host: "GitHub.com", Owner: "Gke-Labs", Name: "Eval-Runner"}

	// Path
	wantPath := "/GitHub.com/Gke-Labs/Eval-Runner"
	if ref.Path() != wantPath {
		t.Errorf("Path() = %q, want %q", ref.Path(), wantPath)
	}

	// Equal (case-insensitive)
	other := RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	if !ref.Equal(other) {
		t.Errorf("expected ref %+v to Equal %+v", ref, other)
	}
	diff := RepoRef{Host: "gitlab.com", Owner: "gke-labs", Name: "eval-runner"}
	if ref.Equal(diff) {
		t.Errorf("expected ref %+v to NOT Equal %+v", ref, diff)
	}

	// Single segment placeholder
	single := RepoRef{Host: "git-server.local", Owner: "-", Name: "docs-eval"}
	if single.Path() != "/git-server.local/-/docs-eval" {
		t.Errorf("Path() = %q, want %q", single.Path(), "/git-server.local/-/docs-eval")
	}
}

func TestRepoLink(t *testing.T) {
	ref := RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	if got := repoLink(ref, "/runs", ""); got != "/github.com/gke-labs/eval-runner/runs" {
		t.Errorf("repoLink() empty linkNS = %q, want %q", got, "/github.com/gke-labs/eval-runner/runs")
	}

	if got := repoLink(ref, "/runs", "custom-ns"); got != "/github.com/gke-labs/eval-runner/runs?namespace=custom-ns" {
		t.Errorf("repoLink() custom ns = %q, want %q", got, "/github.com/gke-labs/eval-runner/runs?namespace=custom-ns")
	}

	if got := repoLink(ref, "/runs?page=2", "custom-ns"); got != "/github.com/gke-labs/eval-runner/runs?page=2&namespace=custom-ns" {
		t.Errorf("repoLink() with existing query = %q, want %q", got, "/github.com/gke-labs/eval-runner/runs?page=2&namespace=custom-ns")
	}

	// runLink
	if got := runLink(ref, "my-run", ""); got != "/github.com/gke-labs/eval-runner/runs/my-run" {
		t.Errorf("runLink() with ref and empty linkNS = %q, want %q", got, "/github.com/gke-labs/eval-runner/runs/my-run")
	}
	if got := runLink(ref, "my-run", "custom-ns"); got != "/github.com/gke-labs/eval-runner/runs/my-run?namespace=custom-ns" {
		t.Errorf("runLink() with ref and custom-ns = %q, want %q", got, "/github.com/gke-labs/eval-runner/runs/my-run?namespace=custom-ns")
	}
	if got := runLink(RepoRef{}, "my-run", "", "eval-runner"); got != "/runs/eval-runner/my-run" {
		t.Errorf("runLink() without ref = %q, want %q", got, "/runs/eval-runner/my-run")
	}
}

func TestRunRepoRef(t *testing.T) {
	// From spec.repo.url
	runURL := &v1alpha1.Run{
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL: "https://github.com/gke-labs/eval-runner.git",
			},
		},
	}
	ref, ok := runRepoRef(runURL)
	if !ok || ref != (RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}) {
		t.Errorf("runRepoRef from URL = %+v, %v", ref, ok)
	}

	// From label
	runLabel := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/repo": "gke-labs_eval-runner",
			},
		},
	}
	ref, ok = runRepoRef(runLabel)
	if !ok || ref != (RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}) {
		t.Errorf("runRepoRef from label = %+v, %v", ref, ok)
	}
}

func TestComparisonRepoRef(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)

	ctx := t.Context()

	// From spec.pullRequest.repository
	compPR := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{Namespace: "eval-runner", Name: "comp-pr"},
		Spec: v1alpha1.ComparisonSpec{
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "gke-labs/eval-runner",
			},
		},
	}
	ref, ok := comparisonRepoRef(ctx, compPR, nil)
	if !ok || ref != (RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}) {
		t.Errorf("comparisonRepoRef from PR = %+v, %v", ref, ok)
	}

	// Fallback to baseline run
	baseRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{Namespace: "eval-runner", Name: "base-run"},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL: "git://git-server.local/docs-eval.git",
			},
		},
	}
	compBase := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{Namespace: "eval-runner", Name: "comp-base"},
		Spec: v1alpha1.ComparisonSpec{
			Baseline: v1alpha1.ComparisonRunRef{
				Run: "base-run",
			},
		},
	}
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseRun).Build()
	ref, ok = comparisonRepoRef(ctx, compBase, fc)
	if !ok || ref != (RepoRef{Host: "git-server.local", Owner: "-", Name: "docs-eval"}) {
		t.Errorf("comparisonRepoRef from baseline = %+v, %v", ref, ok)
	}
}
