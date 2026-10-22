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

package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gke-labs/eval-runner/pkg/forge"
)

func TestGitHubForge_Kind(t *testing.T) {
	f := New()
	if got := f.Kind(); got != "github.com" {
		t.Errorf("Kind() = %q, want %q", got, "github.com")
	}
}

func TestGitHubForge_Repository(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/gke-labs/eval-runner" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"default_branch":"main","clone_url":"https://github.com/gke-labs/eval-runner.git"}`)
			return
		}
		if r.URL.Path == "/repos/gke-labs/not-found" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	f := New(WithBaseURL(server.URL))
	ctx := t.Context()

	// 1. Success case
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	repo, err := f.Repository(ctx, ref)
	if err != nil {
		t.Fatalf("Repository() failed: %v", err)
	}
	if repo.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want %q", repo.DefaultBranch, "main")
	}
	if repo.CloneURL != "https://github.com/gke-labs/eval-runner.git" {
		t.Errorf("CloneURL = %q, want %q", repo.CloneURL, "https://github.com/gke-labs/eval-runner.git")
	}

	// 2. 404 case
	notFoundRef := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "not-found"}
	_, err = f.Repository(ctx, notFoundRef)
	if err == nil || !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("Repository(not-found) err = %v, want forge.ErrNotFound", err)
	}
}

func TestGitHubForge_Branches_PaginationAnd404(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/gke-labs/not-found/branches" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}

		if r.URL.Path == "/repos/gke-labs/eval-runner/branches" {
			w.Header().Set("Content-Type", "application/json")
			page := r.URL.Query().Get("page")
			if page == "2" {
				fmt.Fprint(w, `[{"name":"feature-2","commit":{"sha":"2222222222222222222222222222222222222222"}}]`)
				return
			}
			// Page 1
			nextLink := fmt.Sprintf(`<%s/repos/gke-labs/eval-runner/branches?page=2&per_page=100>; rel="next"`, serverURL)
			w.Header().Set("Link", nextLink)
			fmt.Fprint(w, `[{"name":"main","commit":{"sha":"1111111111111111111111111111111111111111"}}]`)
			return
		}
		http.NotFound(w, r)
	}))
	serverURL = server.URL
	defer server.Close()

	f := New(WithBaseURL(server.URL))
	ctx := t.Context()

	// 1. Pagination across 2 pages
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	branches, err := f.Branches(ctx, ref)
	if err != nil {
		t.Fatalf("Branches() failed: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("Branches() returned %d branches, want 2", len(branches))
	}
	if branches[0].Name != "main" || branches[0].SHA != "1111111111111111111111111111111111111111" {
		t.Errorf("branches[0] = %+v", branches[0])
	}
	if branches[1].Name != "feature-2" || branches[1].SHA != "2222222222222222222222222222222222222222" {
		t.Errorf("branches[1] = %+v", branches[1])
	}

	// 2. 404 case
	notFoundRef := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "not-found"}
	_, err = f.Branches(ctx, notFoundRef)
	if err == nil || !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("Branches(not-found) err = %v, want forge.ErrNotFound", err)
	}
}

func TestGitHubForge_ChangeRequests_PaginationAnd404(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/gke-labs/not-found/pulls" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}

		if r.URL.Path == "/repos/gke-labs/eval-runner/pulls" {
			w.Header().Set("Content-Type", "application/json")
			page := r.URL.Query().Get("page")
			if page == "2" {
				fmt.Fprint(w, `[
					{
						"id": 102,
						"number": 2,
						"title": "PR two",
						"user": {"login": "dev2"},
						"head": {"ref": "feat-2", "sha": "2222", "repo": {"clone_url": "https://github.com/fork/eval-runner.git"}},
						"base": {"ref": "main", "sha": "0000"},
						"state": "open",
						"updated_at": "2026-10-21T11:00:00Z"
					}
				]`)
				return
			}
			// Page 1
			nextLink := fmt.Sprintf(`<%s/repos/gke-labs/eval-runner/pulls?page=2&per_page=100>; rel="next"`, serverURL)
			w.Header().Set("Link", nextLink)
			fmt.Fprint(w, `[
				{
					"id": 101,
					"number": 1,
					"title": "PR one",
					"user": {"login": "dev1"},
					"head": {"ref": "feat-1", "sha": "1111", "repo": {"clone_url": "https://github.com/gke-labs/eval-runner.git"}},
					"base": {"ref": "main", "sha": "0000"},
					"state": "open",
					"updated_at": "2026-10-21T10:00:00Z"
				}
			]`)
			return
		}
		http.NotFound(w, r)
	}))
	serverURL = server.URL
	defer server.Close()

	f := New(WithBaseURL(server.URL))
	ctx := t.Context()

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	crs, err := f.ChangeRequests(ctx, ref)
	if err != nil {
		t.Fatalf("ChangeRequests() failed: %v", err)
	}
	if len(crs) != 2 {
		t.Fatalf("ChangeRequests() returned %d items, want 2", len(crs))
	}
	if crs[0].Number != 1 || crs[0].ID != "1" || crs[0].Author != "dev1" || crs[0].Head.SHA != "1111" {
		t.Errorf("crs[0] = %+v", crs[0])
	}
	if crs[1].Number != 2 || crs[1].ID != "2" || crs[1].Author != "dev2" || crs[1].Head.RepoRef.Owner != "fork" {
		t.Errorf("crs[1] = %+v", crs[1])
	}

	// 404 case
	notFoundRef := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "not-found"}
	_, err = f.ChangeRequests(ctx, notFoundRef)
	if err == nil || !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("ChangeRequests(not-found) err = %v, want forge.ErrNotFound", err)
	}
}

func TestGitHubForge_Commit_SuccessAnd404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/gke-labs/eval-runner/commits/c0ffee" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{
				"sha": "c0ffee",
				"parents": [{"sha": "parent1"}, {"sha": "parent2"}],
				"commit": {
					"author": {"date": "2026-10-21T09:00:00Z"},
					"message": "fix: resolve timeout\n\nDetailed commit body description."
				}
			}`)
			return
		}
		if r.URL.Path == "/repos/gke-labs/eval-runner/commits/notfound" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	f := New(WithBaseURL(server.URL))
	ctx := t.Context()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	commit, err := f.Commit(ctx, ref, "c0ffee")
	if err != nil {
		t.Fatalf("Commit() failed: %v", err)
	}
	if commit.SHA != "c0ffee" {
		t.Errorf("SHA = %q, want %q", commit.SHA, "c0ffee")
	}
	if len(commit.Parents) != 2 || commit.Parents[0] != "parent1" || commit.Parents[1] != "parent2" {
		t.Errorf("Parents = %+v", commit.Parents)
	}
	if commit.Title != "fix: resolve timeout" {
		t.Errorf("Title = %q, want %q", commit.Title, "fix: resolve timeout")
	}

	// 404 case
	_, err = f.Commit(ctx, ref, "notfound")
	if err == nil || !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("Commit(notfound) err = %v, want forge.ErrNotFound", err)
	}
}

func TestGitHubForge_Reporter_UpsertCommentAndSetStatus(t *testing.T) {
	var mu sync.Mutex
	comments := make(map[int64]string)
	statuses := make(map[string]map[string]any)
	nextCommentID := int64(100)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		auth := r.Header.Get("Authorization")
		if auth != "Bearer valid-token" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}

		if r.URL.Path == "/user" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"login":"eval-runner-bot"}`)
			return
		}

		// List or create comments: /repos/gke-labs/eval-runner/issues/42/comments
		if r.URL.Path == "/repos/gke-labs/eval-runner/issues/42/comments" {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				var list []map[string]any
				for id, body := range comments {
					list = append(list, map[string]any{
						"id":   id,
						"body": body,
						"user": map[string]any{"login": "eval-runner-bot"},
					})
				}
				_ = json.NewEncoder(w).Encode(list)
				return
			}
			if r.Method == http.MethodPost {
				var req map[string]string
				_ = json.NewDecoder(r.Body).Decode(&req)
				id := nextCommentID
				nextCommentID++
				comments[id] = req["body"]
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "body": req["body"]})
				return
			}
		}

		// Update comment: /repos/gke-labs/eval-runner/issues/comments/{id}
		if strings.HasPrefix(r.URL.Path, "/repos/gke-labs/eval-runner/issues/comments/") && r.Method == http.MethodPatch {
			var id int64
			fmt.Sscanf(r.URL.Path, "/repos/gke-labs/eval-runner/issues/comments/%d", &id)
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			comments[id] = req["body"]
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "body": req["body"]})
			return
		}

		// Create check run: /repos/gke-labs/eval-runner/check-runs
		if r.URL.Path == "/repos/gke-labs/eval-runner/check-runs" && r.Method == http.MethodPost {
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			statuses["c0ffee"] = req
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": int64(301)})
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	ctx := t.Context()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	// 1. Unauthenticated forge must return ErrUnauthorized
	unauthForge := New(WithBaseURL(server.URL))
	if _, err := unauthForge.UpsertComment(ctx, ref, "42", "key1", "body1"); !errors.Is(err, forge.ErrUnauthorized) {
		t.Errorf("unauthenticated UpsertComment err = %v, want forge.ErrUnauthorized", err)
	}
	if _, err := unauthForge.SetStatus(ctx, ref, "c0ffee", forge.Status{State: "success"}); !errors.Is(err, forge.ErrUnauthorized) {
		t.Errorf("unauthenticated SetStatus err = %v, want forge.ErrUnauthorized", err)
	}

	// 2. Authenticated forge: Create comment
	authForge := New(WithBaseURL(server.URL), WithToken("valid-token"))
	key := "<!-- marker -->"
	body := "Initial evaluation result"
	commentID, err := authForge.UpsertComment(ctx, ref, "42", key, body)
	if err != nil {
		t.Fatalf("UpsertComment(create) failed: %v", err)
	}
	if commentID != 100 {
		t.Errorf("UpsertComment(create) commentID = %d, want 100", commentID)
	}

	mu.Lock()
	if len(comments) != 1 || !strings.Contains(comments[100], "Initial evaluation result") {
		t.Errorf("comments after create = %+v", comments)
	}
	mu.Unlock()

	// 3. Authenticated forge: Update existing comment (upsert with same key)
	updatedBody := "Updated evaluation result"
	updatedID, err := authForge.UpsertComment(ctx, ref, "42", key, updatedBody)
	if err != nil {
		t.Fatalf("UpsertComment(update) failed: %v", err)
	}
	if updatedID != 100 {
		t.Errorf("UpsertComment(update) commentID = %d, want 100", updatedID)
	}

	mu.Lock()
	if len(comments) != 1 || !strings.Contains(comments[100], "Updated evaluation result") {
		t.Errorf("comments after update = %+v", comments)
	}
	mu.Unlock()

	// 4. Authenticated forge: SetStatus
	status := forge.Status{
		State:       "success",
		Context:     "eval-runner/ci",
		Description: "Passed all tests",
		TargetURL:   "https://eval-runner.example.com/comparisons/1",
	}
	statusID, err := authForge.SetStatus(ctx, ref, "c0ffee", status)
	if err != nil {
		t.Fatalf("SetStatus failed: %v", err)
	}
	if statusID != 301 {
		t.Errorf("SetStatus checkRunID = %d, want 301", statusID)
	}

	mu.Lock()
	if statuses["c0ffee"] == nil || statuses["c0ffee"]["conclusion"] != "success" {
		t.Errorf("statuses = %+v", statuses)
	}
	mu.Unlock()
}

func TestGitHubForge_Reporter_SetStatus_InProgressAndCompleted(t *testing.T) {
	var mu sync.Mutex
	var lastPayload map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer valid-token" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/user" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"login":"eval-runner-bot"}`)
			return
		}
		if r.URL.Path == "/repos/gke-labs/eval-runner/check-runs" && r.Method == http.MethodPost {
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			lastPayload = req
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": int64(501)})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	ctx := t.Context()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	f := New(WithBaseURL(server.URL), WithToken("valid-token"))

	tests := []struct {
		name           string
		input          forge.Status
		wantStatus     string
		wantConclusion any
	}{
		{
			name: "explicit in_progress without conclusion",
			input: forge.Status{
				Status:      "in_progress",
				Context:     "eval-runner / evals",
				Description: "running · 1 of 4 samples",
			},
			wantStatus:     "in_progress",
			wantConclusion: nil,
		},
		{
			name: "explicit completed with conclusion",
			input: forge.Status{
				Status:      "completed",
				Conclusion:  "success",
				Context:     "eval-runner / evals",
				Description: "IMPROVED",
			},
			wantStatus:     "completed",
			wantConclusion: "success",
		},
		{
			name: "legacy in_progress via State",
			input: forge.Status{
				State:       "in_progress",
				Context:     "eval-runner / evals",
				Description: "running",
			},
			wantStatus:     "in_progress",
			wantConclusion: nil,
		},
		{
			name: "legacy completed via State",
			input: forge.Status{
				State:       "failure",
				Context:     "eval-runner / evals",
				Description: "REGRESSED",
			},
			wantStatus:     "completed",
			wantConclusion: "failure",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			lastPayload = nil
			mu.Unlock()

			id, err := f.SetStatus(ctx, ref, "abcdef1234", tc.input)
			if err != nil {
				t.Fatalf("SetStatus failed: %v", err)
			}
			if id != 501 {
				t.Errorf("SetStatus checkRunID = %d, want 501", id)
			}

			mu.Lock()
			defer mu.Unlock()
			if lastPayload == nil {
				t.Fatal("expected check-runs payload to be recorded")
			}
			if lastPayload["status"] != tc.wantStatus {
				t.Errorf("payload status = %v, want %v", lastPayload["status"], tc.wantStatus)
			}
			if lastPayload["conclusion"] != tc.wantConclusion {
				t.Errorf("payload conclusion = %v, want %v", lastPayload["conclusion"], tc.wantConclusion)
			}
		})
	}
}
