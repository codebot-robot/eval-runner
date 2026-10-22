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

package forge

import (
	"testing"
)

func TestParseRepoURL(t *testing.T) {
	testCases := []struct {
		name    string
		input   string
		wantRef RepoRef
		wantOK  bool
	}{
		{
			name:    "standard https github url with .git",
			input:   "https://github.com/gke-labs/eval-runner.git",
			wantRef: RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"},
			wantOK:  true,
		},
		{
			name:    "https github url without .git",
			input:   "https://github.com/gke-labs/eval-runner",
			wantRef: RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"},
			wantOK:  true,
		},
		{
			name:    "gitlab url",
			input:   "https://gitlab.com/group/project.git",
			wantRef: RepoRef{Host: "gitlab.com", Owner: "group", Name: "project"},
			wantOK:  true,
		},
		{
			name:    "ssh url with ssh scheme",
			input:   "ssh://git@github.com/owner/repo.git",
			wantRef: RepoRef{Host: "github.com", Owner: "owner", Name: "repo"},
			wantOK:  true,
		},
		{
			name:    "scp-style syntax",
			input:   "git@github.com:owner/repo.git",
			wantRef: RepoRef{Host: "github.com", Owner: "owner", Name: "repo"},
			wantOK:  true,
		},
		{
			name:    "custom port in https is stripped",
			input:   "https://git.example.com:8443/owner/repo.git",
			wantRef: RepoRef{Host: "git.example.com", Owner: "owner", Name: "repo"},
			wantOK:  true,
		},
		{
			name:    "git protocol with port",
			input:   "git://git-server.local:9418/docs-eval.git",
			wantRef: RepoRef{Host: "git-server.local", Owner: "-", Name: "docs-eval"},
			wantOK:  true,
		},
		{
			name:    "single segment repo gets hyphen owner",
			input:   "http://git-server.eval-runner-sandbox.svc/docs-eval.git",
			wantRef: RepoRef{Host: "git-server.eval-runner-sandbox.svc", Owner: "-", Name: "docs-eval"},
			wantOK:  true,
		},
		{
			name:   "empty input",
			input:  "",
			wantOK: false,
		},
		{
			name:   "too many path segments",
			input:  "https://github.com/a/b/c.git",
			wantOK: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotRef, gotOK := ParseRepoURL(tc.input)
			if gotOK != tc.wantOK {
				t.Fatalf("ParseRepoURL(%q) gotOK=%v, want %v", tc.input, gotOK, tc.wantOK)
			}
			if gotOK && !gotRef.Equal(tc.wantRef) {
				t.Errorf("ParseRepoURL(%q) = %+v, want %+v", tc.input, gotRef, tc.wantRef)
			}
		})
	}
}

func TestRepoRefFromOwnerName(t *testing.T) {
	testCases := []struct {
		name    string
		input   string
		wantRef RepoRef
		wantOK  bool
	}{
		{
			name:    "valid owner/name",
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
			name:   "single segment invalid",
			input:  "single-segment",
			wantOK: false,
		},
		{
			name:   "three segments invalid",
			input:  "a/b/c",
			wantOK: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotRef, gotOK := RepoRefFromOwnerName(tc.input)
			if gotOK != tc.wantOK {
				t.Fatalf("RepoRefFromOwnerName(%q) gotOK=%v, want %v", tc.input, gotOK, tc.wantOK)
			}
			if gotOK && !gotRef.Equal(tc.wantRef) {
				t.Errorf("RepoRefFromOwnerName(%q) = %+v, want %+v", tc.input, gotRef, tc.wantRef)
			}
		})
	}
}

func TestRepoRefMethods(t *testing.T) {
	ref := RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	if got := ref.Path(); got != "/github.com/gke-labs/eval-runner" {
		t.Errorf("Path() = %q, want %q", got, "/github.com/gke-labs/eval-runner")
	}
	if got := ref.String(); got != "github.com/gke-labs/eval-runner" {
		t.Errorf("String() = %q, want %q", got, "github.com/gke-labs/eval-runner")
	}
	if !ref.Equal(RepoRef{Host: "GitHub.Com", Owner: "GKE-LABS", Name: "Eval-Runner"}) {
		t.Errorf("Equal() failed case-insensitive match")
	}
	if ref.IsZero() {
		t.Errorf("IsZero() returned true for non-empty ref")
	}
	if !ref.IsValid() {
		t.Errorf("IsValid() returned false for valid ref")
	}

	empty := RepoRef{}
	if !empty.IsZero() {
		t.Errorf("IsZero() returned false for empty ref")
	}
	if empty.IsValid() {
		t.Errorf("IsValid() returned true for empty ref")
	}
}
