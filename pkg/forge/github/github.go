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
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/eval-runner/pkg/forge"
	gh "github.com/gke-labs/eval-runner/pkg/github"
)

var (
	_ forge.Forge    = (*Forge)(nil)
	_ forge.Reporter = (*Forge)(nil)

	// ErrCheckRunsUnavailable indicates that check runs cannot be created with token authentication.
	ErrCheckRunsUnavailable = errors.New("check runs unavailable with token auth; comment posted")
)

// Forge implements forge.Forge and forge.Reporter for github.com.
type Forge struct {
	client *gh.HTTPClient
}

// SetClient sets or updates the underlying HTTPClient (e.g. after resolving credentials).
func (f *Forge) SetClient(client *gh.HTTPClient) {
	f.client = client
}

// Option configures a GitHub Forge.
type Option func(*options)

type options struct {
	client     *gh.HTTPClient
	token      string
	appAuth    *gh.AppTokenProvider
	baseURL    string
	httpClient *http.Client
}

// WithClient configures an existing gh.HTTPClient.
func WithClient(client *gh.HTTPClient) Option {
	return func(o *options) {
		o.client = client
	}
}

// WithToken sets an API token for GitHub authentication.
func WithToken(token string) Option {
	return func(o *options) {
		o.token = token
	}
}

// WithAppAuth sets GitHub App token authentication.
func WithAppAuth(appAuth *gh.AppTokenProvider) Option {
	return func(o *options) {
		o.appAuth = appAuth
	}
}

// WithBaseURL overrides the GitHub API base URL (useful for enterprise or tests).
func WithBaseURL(baseURL string) Option {
	return func(o *options) {
		o.baseURL = baseURL
	}
}

// WithHTTPClient sets a custom underlying http.Client.
func WithHTTPClient(client *http.Client) Option {
	return func(o *options) {
		o.httpClient = client
	}
}

// New creates a new GitHub Forge instance.
func New(opts ...Option) *Forge {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	if o.client != nil {
		return &Forge{client: o.client}
	}

	client := gh.NewHTTPClient(gh.HTTPClientConfig{
		Token:      o.token,
		AppAuth:    o.appAuth,
		BaseURL:    o.baseURL,
		HTTPClient: o.httpClient,
	})
	return &Forge{client: client}
}

// Kind returns "github.com".
func (f *Forge) Kind() string {
	return "github.com"
}

type repoResponse struct {
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
}

// Repository returns metadata for a repository reference.
func (f *Forge) Repository(ctx context.Context, ref forge.RepoRef) (*forge.Repository, error) {
	path := fmt.Sprintf("/repos/%s/%s", ref.Owner, ref.Name)
	var resp repoResponse
	if err := f.client.DoRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, mapError(err)
	}

	return &forge.Repository{
		Ref:           ref,
		DefaultBranch: resp.DefaultBranch,
		CloneURL:      resp.CloneURL,
	}, nil
}

type branchResponse struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// Branches lists branch heads (name -> commit sha) with pagination.
func (f *Forge) Branches(ctx context.Context, ref forge.RepoRef) ([]forge.Branch, error) {
	path := fmt.Sprintf("/repos/%s/%s/branches?per_page=100", ref.Owner, ref.Name)
	var allBranches []forge.Branch
	const maxPages = 100

	for page := 0; page < maxPages && path != ""; page++ {
		var pageBranches []branchResponse
		headers, err := f.client.DoRequestHeader(ctx, http.MethodGet, path, nil, &pageBranches)
		if err != nil {
			return nil, mapError(err)
		}
		for _, b := range pageBranches {
			allBranches = append(allBranches, forge.Branch{
				Name: b.Name,
				SHA:  b.Commit.SHA,
			})
		}

		next := gh.ParseNextLink(headers.Get("Link"))
		if next != "" {
			if strings.HasPrefix(next, "http://") || strings.HasPrefix(next, "https://") {
				baseURL := f.client.BaseURL()
				if !strings.HasPrefix(next, baseURL) {
					return nil, fmt.Errorf("refusing to follow link header outside base URL %q: %s", baseURL, next)
				}
			}
		}
		path = next
	}

	return allBranches, nil
}

type pullResponse struct {
	ID     int64  `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			CloneURL string `json:"clone_url"`
			FullName string `json:"full_name"`
			Name     string `json:"name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ChangeRequests lists open change requests with head/base refs and shas.
func (f *Forge) ChangeRequests(ctx context.Context, ref forge.RepoRef) ([]forge.ChangeRequest, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&per_page=100", ref.Owner, ref.Name)
	var allCRs []forge.ChangeRequest
	const maxPages = 100

	for page := 0; page < maxPages && path != ""; page++ {
		var pagePRs []pullResponse
		headers, err := f.client.DoRequestHeader(ctx, http.MethodGet, path, nil, &pagePRs)
		if err != nil {
			return nil, mapError(err)
		}

		for _, pr := range pagePRs {
			headRepoRef := ref
			if pr.Head.Repo != nil {
				if parsed, ok := forge.ParseRepoURL(pr.Head.Repo.CloneURL); ok {
					headRepoRef = parsed
				} else if pr.Head.Repo.FullName != "" {
					if parsed, ok := forge.RepoRefFromOwnerName(pr.Head.Repo.FullName); ok {
						headRepoRef = parsed
					}
				} else if pr.Head.Repo.Owner.Login != "" && pr.Head.Repo.Name != "" {
					headRepoRef = forge.RepoRef{
						Host:  ref.Host,
						Owner: pr.Head.Repo.Owner.Login,
						Name:  pr.Head.Repo.Name,
					}
				}
			}

			allCRs = append(allCRs, forge.ChangeRequest{
				ID:     forge.ChangeRequestID(strconv.Itoa(pr.Number)),
				Number: pr.Number,
				Title:  pr.Title,
				Author: pr.User.Login,
				Head: forge.ChangeRequestHead{
					Ref:     pr.Head.Ref,
					SHA:     pr.Head.SHA,
					RepoRef: headRepoRef,
				},
				Base: forge.ChangeRequestBase{
					Ref: pr.Base.Ref,
					SHA: pr.Base.SHA,
				},
				State:     pr.State,
				UpdatedAt: pr.UpdatedAt,
			})
		}

		next := gh.ParseNextLink(headers.Get("Link"))
		if next != "" {
			if strings.HasPrefix(next, "http://") || strings.HasPrefix(next, "https://") {
				baseURL := f.client.BaseURL()
				if !strings.HasPrefix(next, baseURL) {
					return nil, fmt.Errorf("refusing to follow link header outside base URL %q: %s", baseURL, next)
				}
			}
		}
		path = next
	}

	return allCRs, nil
}

type commitResponse struct {
	SHA     string `json:"sha"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
	Commit struct {
		Author struct {
			Date time.Time `json:"date"`
		} `json:"author"`
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
		Message string `json:"message"`
	} `json:"commit"`
}

// Commit returns commit metadata (sha, parents, author time, message title).
func (f *Forge) Commit(ctx context.Context, ref forge.RepoRef, sha string) (*forge.Commit, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s", ref.Owner, ref.Name, sha)
	var resp commitResponse
	if err := f.client.DoRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, mapError(err)
	}

	parents := make([]string, len(resp.Parents))
	for i, p := range resp.Parents {
		parents[i] = p.SHA
	}

	t := resp.Commit.Author.Date
	if t.IsZero() {
		t = resp.Commit.Committer.Date
	}

	title := resp.Commit.Message
	if idx := strings.Index(title, "\n"); idx != -1 {
		title = title[:idx]
	}

	return &forge.Commit{
		SHA:     resp.SHA,
		Parents: parents,
		Time:    t,
		Title:   strings.TrimSpace(title),
	}, nil
}

// UpsertComment posts or updates a PR comment matching key.
func (f *Forge) UpsertComment(ctx context.Context, ref forge.RepoRef, id forge.ChangeRequestID, key string, body string) (int64, error) {
	if !f.client.HasCredentials() {
		return 0, forge.ErrUnauthorized
	}

	prNum, err := strconv.Atoi(strings.TrimPrefix(string(id), "#"))
	if err != nil {
		return 0, fmt.Errorf("invalid change request id %q: %w", id, err)
	}

	// Plain comment without marker when key is empty
	if key == "" {
		commentID, err := f.client.CreateIssueComment(ctx, ref.Owner, ref.Name, prNum, body)
		return commentID, mapError(err)
	}

	marker := key
	if !strings.HasPrefix(marker, "<!--") {
		marker = fmt.Sprintf("<!-- %s -->", marker)
	}

	content := body
	if !strings.Contains(content, marker) {
		content = marker + "\n\n" + content
	}

	currentUser, err := f.client.CurrentUser(ctx)
	if err != nil {
		return 0, mapError(err)
	}

	comments, err := f.client.ListIssueComments(ctx, ref.Owner, ref.Name, prNum)
	if err != nil {
		return 0, mapError(err)
	}

	commentID := int64(0)
	for _, c := range comments {
		if strings.Contains(c.Body, marker) {
			if currentUser == "" || strings.EqualFold(c.User.Login, currentUser) {
				commentID = c.ID
				break
			}
		}
	}

	if commentID != 0 {
		if err := f.client.UpdateIssueComment(ctx, ref.Owner, ref.Name, commentID, content); err != nil {
			return 0, mapError(err)
		}
		return commentID, nil
	}

	newID, err := f.client.CreateIssueComment(ctx, ref.Owner, ref.Name, prNum, content)
	return newID, mapError(err)
}

// SetStatus reports a status check (check run) on a commit SHA.
func (f *Forge) SetStatus(ctx context.Context, ref forge.RepoRef, sha string, status forge.Status) (int64, error) {
	if !f.client.HasCredentials() {
		return 0, forge.ErrUnauthorized
	}

	checkState := gh.CheckRunState{
		Name:       status.Context,
		Conclusion: status.State,
		Title:      status.Description,
		Summary:    status.Description,
	}
	if checkState.Name == "" {
		checkState.Name = gh.CheckRunName
	}
	if status.State == "in_progress" || status.State == "pending" {
		checkState.Status = "in_progress"
		checkState.Conclusion = ""
	} else {
		checkState.Status = "completed"
	}

	if status.CheckRunID != 0 {
		err := f.client.UpdateCheckRun(ctx, ref.Owner, ref.Name, status.CheckRunID, checkState)
		if err == nil {
			return status.CheckRunID, nil
		}
		var pe *gh.PermanentError
		if errors.As(err, &pe) {
			if pe.StatusCode == http.StatusForbidden {
				return 0, ErrCheckRunsUnavailable
			}
			if pe.StatusCode != http.StatusNotFound {
				return 0, mapError(err)
			}
		} else {
			return 0, mapError(err)
		}
	}

	newID, err := f.client.CreateCheckRun(ctx, ref.Owner, ref.Name, sha, checkState)
	if err != nil {
		var pe *gh.PermanentError
		if errors.As(err, &pe) && pe.StatusCode == http.StatusForbidden {
			return 0, ErrCheckRunsUnavailable
		}
		return 0, mapError(err)
	}
	return newID, nil
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	var pe *gh.PermanentError
	if errors.As(err, &pe) {
		if pe.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %w", forge.ErrNotFound, pe)
		}
		if pe.StatusCode == http.StatusUnauthorized || pe.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w: %w", forge.ErrUnauthorized, pe)
		}
	}
	return err
}
