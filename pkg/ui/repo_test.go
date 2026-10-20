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
)

func TestParseRepoURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantRef RepoRef
	}{
		{
			name:   "github https with .git",
			input:  "https://github.com/gke-labs/eval-runner.git",
			wantOK: true,
			wantRef: RepoRef{
				Host:  "github.com",
				Owner: "gke-labs",
				Name:  "eval-runner",
			},
		},
		{
			name:   "github https without .git",
			input:  "https://github.com/gke-labs/eval-runner",
			wantOK: true,
			wantRef: RepoRef{
				Host:  "github.com",
				Owner: "gke-labs",
				Name:  "eval-runner",
			},
		},
		{
			name:   "github ssh scp-style with .git",
			input:  "git@github.com:gke-labs/eval-runner.git",
			wantOK: true,
			wantRef: RepoRef{
				Host:  "github.com",
				Owner: "gke-labs",
				Name:  "eval-runner",
			},
		},
		{
			name:   "github ssh scp-style without .git",
			input:  "git@github.com:gke-labs/eval-runner",
			wantOK: true,
			wantRef: RepoRef{
				Host:  "github.com",
				Owner: "gke-labs",
				Name:  "eval-runner",
			},
		},
		{
			name:   "github ssh url-style",
			input:  "ssh://git@github.com/gke-labs/eval-runner",
			wantOK: true,
			wantRef: RepoRef{
				Host:  "github.com",
				Owner: "gke-labs",
				Name:  "eval-runner",
			},
		},
		{
			name:   "github ssh url-style with port and .git",
			input:  "ssh://git@github.com:22/gke-labs/eval-runner.git",
			wantOK: true,
			wantRef: RepoRef{
				Host:  "github.com",
				Owner: "gke-labs",
				Name:  "eval-runner",
			},
		},
		{
			name:   "another host https",
			input:  "https://gitlab.example.org/team-alpha/my_service.git",
			wantOK: true,
			wantRef: RepoRef{
				Host:  "gitlab.example.org",
				Owner: "team-alpha",
				Name:  "my_service",
			},
		},
		{
			name:   "another host ssh scp-style",
			input:  "git@bitbucket.org:org-name/repo-123.git",
			wantOK: true,
			wantRef: RepoRef{
				Host:  "bitbucket.org",
				Owner: "org-name",
				Name:  "repo-123",
			},
		},
		{
			name:   "case insensitivity lowercases host",
			input:  "https://GITHUB.COM/Gke-Labs/Eval-Runner.git",
			wantOK: true,
			wantRef: RepoRef{
				Host:  "github.com",
				Owner: "Gke-Labs",
				Name:  "Eval-Runner",
			},
		},
		{
			name:   "trailing slash in https url",
			input:  "https://github.com/gke-labs/eval-runner/",
			wantOK: true,
			wantRef: RepoRef{
				Host:  "github.com",
				Owner: "gke-labs",
				Name:  "eval-runner",
			},
		},
		{
			name:   "trailing slash after .git",
			input:  "https://github.com/gke-labs/eval-runner.git/",
			wantOK: true,
			wantRef: RepoRef{
				Host:  "github.com",
				Owner: "gke-labs",
				Name:  "eval-runner",
			},
		},
		{
			name:   "dot github special repo name",
			input:  "https://github.com/gke-labs/.github.git",
			wantOK: true,
			wantRef: RepoRef{
				Host:  "github.com",
				Owner: "gke-labs",
				Name:  ".github",
			},
		},
		{
			name:   "reject dot dot traversal in owner",
			input:  "https://github.com/../eval-runner",
			wantOK: false,
		},
		{
			name:   "reject single dot in owner",
			input:  "https://github.com/./eval-runner",
			wantOK: false,
		},
		{
			name:   "reject dot dot traversal in repo",
			input:  "https://github.com/gke-labs/..",
			wantOK: false,
		},
		{
			name:   "reject single dot in repo",
			input:  "https://github.com/gke-labs/.",
			wantOK: false,
		},
		{
			name:   "empty string",
			input:  "",
			wantOK: false,
		},
		{
			name:   "garbage text",
			input:  "not a valid url",
			wantOK: false,
		},
		{
			name:   "just scheme",
			input:  "https://",
			wantOK: false,
		},
		{
			name:   "host only",
			input:  "https://github.com",
			wantOK: false,
		},
		{
			name:   "single segment path",
			input:  "https://github.com/eval-runner",
			wantOK: false,
		},
		{
			name:   "three segment path",
			input:  "https://github.com/gke-labs/eval-runner/extra",
			wantOK: false,
		},
		{
			name:   "scp empty path",
			input:  "git@github.com:",
			wantOK: false,
		},
		{
			name:   "invalid host without dot",
			input:  "https://localhost/gke-labs/eval-runner",
			wantOK: false,
		},
		{
			name:   "invalid characters in owner",
			input:  "https://github.com/bad@owner/eval-runner",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, ok := ParseRepoURL(tc.input)
			if ok != tc.wantOK {
				t.Fatalf("ParseRepoURL(%q) ok = %v, want %v", tc.input, ok, tc.wantOK)
			}
			if tc.wantOK && ref != tc.wantRef {
				t.Fatalf("ParseRepoURL(%q) = %+v, want %+v", tc.input, ref, tc.wantRef)
			}
		})
	}
}

func TestRepoRefMethods(t *testing.T) {
	ref := RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	if got := ref.Path(); got != "/github.com/gke-labs/eval-runner" {
		t.Errorf("Path() = %q, want %q", got, "/github.com/gke-labs/eval-runner")
	}

	sameRef := RepoRef{Host: "GITHUB.com", Owner: "GKE-LABS", Name: "Eval-Runner"}
	if !ref.Equal(sameRef) {
		t.Errorf("Equal() should be case-insensitive")
	}

	diffRef := RepoRef{Host: "gitlab.com", Owner: "gke-labs", Name: "eval-runner"}
	if ref.Equal(diffRef) {
		t.Errorf("Equal() should return false for different host")
	}

	fromOwner, ok := RepoRefFromOwnerName("gke-labs/eval-runner")
	if !ok || !ref.Equal(fromOwner) {
		t.Errorf("RepoRefFromOwnerName() = %+v, %v, want %+v, true", fromOwner, ok, ref)
	}

	if _, ok := RepoRefFromOwnerName("invalid"); ok {
		t.Errorf("RepoRefFromOwnerName(\"invalid\") should return ok=false")
	}
}

func TestRepoLink(t *testing.T) {
	ref := RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	if got := repoLink(ref, "", "eval-runner"); got != "/github.com/gke-labs/eval-runner" {
		t.Errorf("repoLink(ref, \"\", \"eval-runner\") = %q, want \"/github.com/gke-labs/eval-runner\"", got)
	}

	if got := repoLink(ref, "/runs", "eval-runner"); got != "/github.com/gke-labs/eval-runner/runs" {
		t.Errorf("repoLink(ref, \"/runs\", \"eval-runner\") = %q, want \"/github.com/gke-labs/eval-runner/runs\"", got)
	}

	if got := repoLink(ref, "/runs", "custom-ns"); got != "/github.com/gke-labs/eval-runner/runs?namespace=custom-ns" {
		t.Errorf("repoLink(ref, \"/runs\", \"custom-ns\") = %q, want \"/github.com/gke-labs/eval-runner/runs?namespace=custom-ns\"", got)
	}

	if got := repoLink(RepoRef{}, "", "eval-runner"); got != "/" {
		t.Errorf("repoLink(empty, \"\", \"eval-runner\") = %q, want \"/\"", got)
	}

	if got := repoLink(RepoRef{}, "", "custom-ns"); got != "/?namespace=custom-ns" {
		t.Errorf("repoLink(empty, \"\", \"custom-ns\") = %q, want \"/?namespace=custom-ns\"", got)
	}
}
