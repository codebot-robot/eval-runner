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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

// FakeCheckRun records check run details in the fake server.
type FakeCheckRun struct {
	ID         int64
	Name       string
	HeadSHA    string
	Status     string
	Conclusion string
	Title      string
	Summary    string
}

// FakeGitHubServer is an in-memory httptest fake simulating the GitHub REST API.
type FakeGitHubServer struct {
	mu             sync.Mutex
	server         *httptest.Server
	comments       map[int64]*IssueComment
	issueComments  map[string][]int64 // "owner/repo#number" -> comment IDs
	checkRuns      map[int64]*FakeCheckRun
	nextCommentID  int64
	nextCheckRunID int64

	// Injected errors: if non-empty, the next request(s) return this status and message
	injectedErrors []injectedError
	requestLog     []string
}

type injectedError struct {
	method     string
	pathPrefix string
	statusCode int
	body       string
	headers    map[string]string
}

// NewFakeGitHubServer creates and starts a new FakeGitHubServer.
func NewFakeGitHubServer() *FakeGitHubServer {
	f := &FakeGitHubServer{
		comments:       make(map[int64]*IssueComment),
		issueComments:  make(map[string][]int64),
		checkRuns:      make(map[int64]*FakeCheckRun),
		nextCommentID:  1000,
		nextCheckRunID: 5000,
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handleHTTP))
	return f
}

// URL returns the base URL of the fake server.
func (f *FakeGitHubServer) URL() string {
	return f.server.URL
}

// Close shuts down the fake server.
func (f *FakeGitHubServer) Close() {
	f.server.Close()
}

// Client returns an HTTPClient configured to use this fake server.
func (f *FakeGitHubServer) Client(token string) *HTTPClient {
	return NewHTTPClient(HTTPClientConfig{
		Token:      token,
		BaseURL:    f.server.URL,
		HTTPClient: f.server.Client(),
	})
}

// InjectError configures the next HTTP request to return the given status code and body.
func (f *FakeGitHubServer) InjectError(statusCode int, body string, headers ...map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var h map[string]string
	if len(headers) > 0 {
		h = headers[0]
	}
	f.injectedErrors = append(f.injectedErrors, injectedError{
		statusCode: statusCode,
		body:       body,
		headers:    h,
	})
}

// InjectPathError configures a status code and body to be returned for a request matching method and pathPrefix.
func (f *FakeGitHubServer) InjectPathError(method, pathPrefix string, statusCode int, body string, headers ...map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var h map[string]string
	if len(headers) > 0 {
		h = headers[0]
	}
	f.injectedErrors = append(f.injectedErrors, injectedError{
		method:     method,
		pathPrefix: pathPrefix,
		statusCode: statusCode,
		body:       body,
		headers:    h,
	})
}

// Comments returns all stored comments.
func (f *FakeGitHubServer) Comments() []*IssueComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	var res []*IssueComment
	for _, c := range f.comments {
		res = append(res, c)
	}
	return res
}

// CheckRuns returns all stored check runs.
func (f *FakeGitHubServer) CheckRuns() []*FakeCheckRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	var res []*FakeCheckRun
	for _, cr := range f.checkRuns {
		res = append(res, cr)
	}
	return res
}

// RequestLog returns logged request method and paths.
func (f *FakeGitHubServer) RequestLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := make([]string, len(f.requestLog))
	copy(copied, f.requestLog)
	return copied
}

func (f *FakeGitHubServer) handleHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requestLog = append(f.requestLog, fmt.Sprintf("%s %s", r.Method, r.URL.Path))

	for i, errResp := range f.injectedErrors {
		if (errResp.method == "" || r.Method == errResp.method) &&
			(errResp.pathPrefix == "" || strings.HasPrefix(r.URL.Path, errResp.pathPrefix)) {
			f.injectedErrors = append(f.injectedErrors[:i], f.injectedErrors[i+1:]...)
			for k, v := range errResp.headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(errResp.statusCode)
			if _, err := w.Write([]byte(errResp.body)); err != nil {
				_ = err
			}
			f.mu.Unlock()
			return
		}
	}
	f.mu.Unlock()

	path := r.URL.Path

	// 1. List or create issue comments: /repos/{owner}/{repo}/issues/{number}/comments
	if strings.HasPrefix(path, "/repos/") && strings.Contains(path, "/issues/") && strings.HasSuffix(path, "/comments") {
		f.handleIssueComments(w, r)
		return
	}

	// 2. Update issue comment: /repos/{owner}/{repo}/issues/comments/{comment_id}
	if strings.HasPrefix(path, "/repos/") && strings.Contains(path, "/issues/comments/") {
		f.handleUpdateComment(w, r)
		return
	}

	// 3. Create check run: /repos/{owner}/{repo}/check-runs
	if strings.HasPrefix(path, "/repos/") && strings.HasSuffix(path, "/check-runs") && r.Method == http.MethodPost {
		f.handleCreateCheckRun(w, r)
		return
	}

	// 4. Update check run: /repos/{owner}/{repo}/check-runs/{check_run_id}
	if strings.HasPrefix(path, "/repos/") && strings.Contains(path, "/check-runs/") && r.Method == http.MethodPatch {
		f.handleUpdateCheckRun(w, r)
		return
	}

	http.NotFound(w, r)
}

func (f *FakeGitHubServer) handleIssueComments(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// ["repos", owner, repo, "issues", number, "comments"]
	if len(parts) != 6 {
		http.NotFound(w, r)
		return
	}
	owner, repo, numStr := parts[1], parts[2], parts[4]
	issueKey := fmt.Sprintf("%s/%s#%s", owner, repo, numStr)

	switch r.Method {
	case http.MethodGet:
		ids := f.issueComments[issueKey]
		var result []*IssueComment
		for _, id := range ids {
			if c, ok := f.comments[id]; ok {
				result = append(result, c)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(result); err != nil {
			_ = err
		}

	case http.MethodPost:
		var payload commentPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.nextCommentID++
		c := &IssueComment{
			ID:   f.nextCommentID,
			Body: payload.Body,
		}
		f.comments[c.ID] = c
		f.issueComments[issueKey] = append(f.issueComments[issueKey], c.ID)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(commentResponse{ID: c.ID}); err != nil {
			_ = err
		}

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (f *FakeGitHubServer) handleUpdateComment(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// ["repos", owner, repo, "issues", "comments", comment_id]
	if len(parts) != 6 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[5], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	c, ok := f.comments[id]
	if !ok {
		http.NotFound(w, r)
		return
	}

	var payload commentPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.Body = payload.Body

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(c); err != nil {
		_ = err
	}
}

func (f *FakeGitHubServer) handleCreateCheckRun(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var payload checkRunCreatePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	f.nextCheckRunID++
	cr := &FakeCheckRun{
		ID:         f.nextCheckRunID,
		Name:       payload.Name,
		HeadSHA:    payload.HeadSHA,
		Status:     payload.Status,
		Conclusion: payload.Conclusion,
	}
	if payload.Output != nil {
		cr.Title = payload.Output.Title
		cr.Summary = payload.Output.Summary
	}
	f.checkRuns[cr.ID] = cr

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(checkRunResponse{ID: cr.ID}); err != nil {
		_ = err
	}
}

func (f *FakeGitHubServer) handleUpdateCheckRun(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// ["repos", owner, repo, "check-runs", check_run_id]
	if len(parts) != 5 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	cr, ok := f.checkRuns[id]
	if !ok {
		http.NotFound(w, r)
		return
	}

	var payload checkRunUpdatePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cr.Name = payload.Name
	cr.Status = payload.Status
	cr.Conclusion = payload.Conclusion
	if payload.Output != nil {
		cr.Title = payload.Output.Title
		cr.Summary = payload.Output.Summary
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(checkRunResponse{ID: cr.ID}); err != nil {
		_ = err
	}
}
