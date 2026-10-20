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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/stats"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	DefaultPageLimit = 100
	MaxPageLimit     = 500
)

func isValidNamespace(ns string) bool {
	if ns == "" {
		return false
	}
	return len(validation.IsDNS1123Label(ns)) == 0
}

func isValidResourceName(name string) bool {
	if name == "" {
		return false
	}
	return len(validation.IsDNS1123Subdomain(name)) == 0
}

func isValidRepoHash(h string) bool {
	if len(h) < 1 || len(h) > 128 {
		return false
	}
	for _, ch := range h {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.') {
			return false
		}
	}
	return true
}

func parsePagination(r *http.Request) (offset, limit int, err error) {
	q := r.URL.Query()
	offset = 0
	limit = DefaultPageLimit

	if oStr := q.Get("offset"); oStr != "" {
		o, err := strconv.Atoi(oStr)
		if err != nil || o < 0 {
			return 0, 0, fmt.Errorf("invalid offset %q: must be non-negative integer", oStr)
		}
		offset = o
	}

	if lStr := q.Get("limit"); lStr != "" {
		l, err := strconv.Atoi(lStr)
		if err != nil || l <= 0 {
			return 0, 0, fmt.Errorf("invalid limit %q: must be positive integer", lStr)
		}
		if l > MaxPageLimit {
			l = MaxPageLimit
		}
		limit = l
	}

	return offset, limit, nil
}

func buildPageURL(baseURL string, query url.Values, newOffset int, limit int) string {
	q := make(url.Values)
	for k, v := range query {
		q[k] = v
	}
	if newOffset > 0 {
		q.Set("offset", strconv.Itoa(newOffset))
	} else {
		q.Del("offset")
	}
	if limit != DefaultPageLimit {
		q.Set("limit", strconv.Itoa(limit))
	} else {
		q.Del("limit")
	}
	encoded := q.Encode()
	if encoded == "" {
		return baseURL
	}
	return baseURL + "?" + encoded
}

// Config configures the web UI server.
type Config struct {
	Client           client.Reader
	DefaultNamespace string
	ArtifactSink     artifacts.ArtifactSink
}

// Server serves read-only web views of Runs and Comparisons.
type Server struct {
	cfg       Config
	mux       *http.ServeMux
	cache     *logLRUCache
	compCache *comparisonLRUCache
}

// NewServer initializes a new Server with route registration.
func NewServer(cfg Config) *Server {
	if cfg.DefaultNamespace == "" {
		cfg.DefaultNamespace = "eval-runner"
	}

	s := &Server{
		cfg:       cfg,
		mux:       http.NewServeMux(),
		cache:     newLogLRUCache(64*1024*1024, 1*time.Hour),
		compCache: newComparisonLRUCache(32*1024*1024, 1*time.Hour),
	}

	s.routes()
	return s
}

// ServeHTTP delegates to the internal mux, intercepting legacy series detail requests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/series/") {
		s.handleSeriesDetail(w, r)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /{$}", s.handleRepoIndex)

	// Legacy routes
	s.mux.HandleFunc("GET /runs", s.handleRunsList)
	s.mux.HandleFunc("GET /runs/{$}", s.handleRunsList)
	s.mux.HandleFunc("GET /runs/{namespace}/{name}", s.handleRunDetail)
	s.mux.HandleFunc("GET /runs/{namespace}/{name}/tasks", s.handleRunTasks)
	s.mux.HandleFunc("GET /runs/{namespace}/{name}/tasks/{rest...}", s.handleRunTasks)
	s.mux.HandleFunc("GET /comparisons", s.handleComparisonsList)
	s.mux.HandleFunc("GET /comparisons/{$}", s.handleComparisonsList)
	s.mux.HandleFunc("GET /comparisons/{namespace}/{name}", s.handleComparisonDetail)
	s.mux.HandleFunc("GET /pulls", s.handlePullsList)
	s.mux.HandleFunc("GET /pulls/{$}", s.handlePullsList)
	s.mux.HandleFunc("GET /pulls/{repohash}/{number}", s.handlePullDetail)
	s.mux.HandleFunc("GET /series", s.handleSeriesList)

	// Repository-scoped routes
	s.mux.HandleFunc("GET /{host}/{owner}/{repo}/{$}", s.handleRepoOverview)
	s.mux.HandleFunc("GET /{host}/{owner}/{repo}", s.handleRepoOverview)
	s.mux.HandleFunc("GET /{host}/{owner}/{repo}/runs", s.handleRepoRunsList)
	s.mux.HandleFunc("GET /{host}/{owner}/{repo}/runs/{name}", s.handleRepoRunDetail)
	s.mux.HandleFunc("GET /{host}/{owner}/{repo}/runs/{name}/tasks", s.handleRepoRunTasks)
	s.mux.HandleFunc("GET /{host}/{owner}/{repo}/runs/{name}/tasks/{rest...}", s.handleRepoRunTasks)
	s.mux.HandleFunc("GET /{host}/{owner}/{repo}/comparisons", s.handleRepoComparisonsList)
	s.mux.HandleFunc("GET /{host}/{owner}/{repo}/comparisons/{name}", s.handleRepoComparisonDetail)
	s.mux.HandleFunc("GET /{host}/{owner}/{repo}/pulls", s.handleRepoPullsList)
	s.mux.HandleFunc("GET /{host}/{owner}/{repo}/pulls/{number}", s.handleRepoPullDetail)
	s.mux.HandleFunc("GET /{host}/{owner}/{repo}/series", s.handleRepoSeriesList)
	s.mux.HandleFunc("GET /{host}/{owner}/{repo}/series/{branch...}", s.handleRepoSeriesDetail)
}

func parseMetricFilter(r *http.Request) MetricFilter {
	switch m := MetricFilter(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("metric")))); m {
	case MetricFilterRate, MetricFilterLatency, MetricFilterTokens, MetricFilterCost:
		return m
	}
	return MetricFilterAll
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("ok\n")); err != nil {
		return
	}
}

// httpError writes a fixed public message and logs the detail, which must never reach the body.
func httpError(w http.ResponseWriter, status int, public string, detail string, err error) {
	if err != nil {
		if detail != "" {
			log.Printf("%s: %v", detail, err)
		} else {
			log.Printf("%v", err)
		}
	}
	http.Error(w, public, status)
}

func (s *Server) parseRouteRepo(r *http.Request) (RepoRef, bool) {
	host := r.PathValue("host")
	owner := r.PathValue("owner")
	name := r.PathValue("repo")
	if !isValidHost(host) || !isValidOwnerOrRepo(owner) || !isValidOwnerOrRepo(name) {
		return RepoRef{}, false
	}
	return RepoRef{
		Host:  strings.ToLower(strings.TrimSpace(host)),
		Owner: strings.TrimSpace(owner),
		Name:  strings.TrimSpace(name),
	}, true
}

func (s *Server) resolveComparisonRepo(ctx context.Context, comp *v1alpha1.Comparison, runsByName map[string]*v1alpha1.Run) (RepoRef, bool) {
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
	if comp.Spec.Baseline.Run != "" {
		if runsByName != nil {
			if baseRun, ok := runsByName[comp.Spec.Baseline.Run]; ok {
				return runRepoRef(baseRun)
			}
		}
		return comparisonRepoRef(ctx, comp, s.cfg.Client)
	}
	return RepoRef{}, false
}

func (s *Server) handleRepoIndex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}

	var runList v1alpha1.RunList
	if err := s.cfg.Client.List(ctx, &runList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to namespace denied", fmt.Sprintf("access to namespace %q denied", ns), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to list runs", fmt.Sprintf("failed to list runs in namespace %s", ns), err)
		return
	}

	var compList v1alpha1.ComparisonList
	if err := s.cfg.Client.List(ctx, &compList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to namespace denied", fmt.Sprintf("access to namespace %q denied", ns), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to list comparisons", fmt.Sprintf("failed to list comparisons in namespace %s", ns), err)
		return
	}

	runsByName := make(map[string]*v1alpha1.Run, len(runList.Items))
	for i := range runList.Items {
		runsByName[runList.Items[i].Name] = &runList.Items[i]
	}

	type repoCounts struct {
		ref   RepoRef
		runs  int
		comps int
	}
	countsMap := make(map[string]*repoCounts)

	for i := range runList.Items {
		if ref, ok := runRepoRef(&runList.Items[i]); ok {
			key := strings.ToLower(ref.String())
			entry, exists := countsMap[key]
			if !exists {
				entry = &repoCounts{ref: ref}
				countsMap[key] = entry
			}
			entry.runs++
		}
	}

	for i := range compList.Items {
		if ref, ok := s.resolveComparisonRepo(ctx, &compList.Items[i], runsByName); ok {
			key := strings.ToLower(ref.String())
			entry, exists := countsMap[key]
			if !exists {
				entry = &repoCounts{ref: ref}
				countsMap[key] = entry
			}
			entry.comps++
		}
	}

	repos := make([]*repoCounts, 0, len(countsMap))
	for _, entry := range countsMap {
		repos = append(repos, entry)
	}

	sort.Slice(repos, func(i, j int) bool {
		rI, rJ := repos[i].ref, repos[j].ref
		if !strings.EqualFold(rI.Host, rJ.Host) {
			return strings.ToLower(rI.Host) < strings.ToLower(rJ.Host)
		}
		if !strings.EqualFold(rI.Owner, rJ.Owner) {
			return strings.ToLower(rI.Owner) < strings.ToLower(rJ.Owner)
		}
		return strings.ToLower(rI.Name) < strings.ToLower(rJ.Name)
	})

	items := make([]RepoListItem, 0, len(repos))
	for _, rEntry := range repos {
		items = append(items, RepoListItem{
			Host:             rEntry.ref.Host,
			Owner:            rEntry.ref.Owner,
			Name:             rEntry.ref.Name,
			Repository:       rEntry.ref.String(),
			RunsCount:        rEntry.runs,
			ComparisonsCount: rEntry.comps,
			Link:             repoLink(rEntry.ref, "", ns),
		})
	}

	view := RepoIndexView{
		Namespace:    ns,
		Repositories: items,
		TotalCount:   len(items),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplRepoIndex.Execute(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func (s *Server) namespaceFor(r *http.Request) (string, error) {
	ns := r.URL.Query().Get("namespace")
	if ns != "" {
		if !isValidNamespace(ns) {
			return "", fmt.Errorf("invalid namespace %q: must be a valid DNS-1123 label", ns)
		}
		return ns, nil
	}
	return s.cfg.DefaultNamespace, nil
}

func (s *Server) handleRunsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}

	offset, limit, err := parsePagination(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid pagination params", "invalid pagination query params", err)
		return
	}

	var runList v1alpha1.RunList
	if err := s.cfg.Client.List(ctx, &runList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to namespace denied", fmt.Sprintf("access to namespace %q denied", ns), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to list runs", fmt.Sprintf("failed to list runs in namespace %s", ns), err)
		return
	}

	// Sort runs by CreationTimestamp descending (newest first), with name tiebreaker
	sort.Slice(runList.Items, func(i, j int) bool {
		if !runList.Items[i].CreationTimestamp.Equal(&runList.Items[j].CreationTimestamp) {
			return runList.Items[i].CreationTimestamp.After(runList.Items[j].CreationTimestamp.Time)
		}
		return runList.Items[i].Name < runList.Items[j].Name
	})

	totalCount := len(runList.Items)
	start := offset
	if start > totalCount {
		start = totalCount
	}
	end := start + limit
	if end > totalCount {
		end = totalCount
	}

	pageRuns := runList.Items[start:end]
	items := make([]RunListItem, 0, len(pageRuns))
	for _, run := range pageRuns {
		tasks := "—"
		samples := 0
		rates := ""
		if run.Status.Phase == v1alpha1.RunPhaseRunning {
			runningTasks := 0
			for _, t := range run.Status.Tasks {
				if t.Phase == v1alpha1.TaskPhaseRunning {
					runningTasks++
				}
			}
			totalTasks := len(run.Status.Tasks)
			if totalTasks == 0 && run.Status.Discovery != nil {
				totalTasks = run.Status.Discovery.TaskCount
			}
			tasks = fmt.Sprintf("%d/%d tasks running", runningTasks, totalTasks)
			if run.Status.Summary != nil {
				samples = run.Status.Summary.TotalSamples
				rates = run.Status.Summary.Rates
			}
		} else if run.Status.Summary != nil {
			if run.Status.Summary.Tasks != "" {
				tasks = run.Status.Summary.Tasks
			}
			samples = run.Status.Summary.TotalSamples
			rates = run.Status.Summary.Rates
		} else if run.Status.Discovery != nil && run.Status.Discovery.TaskCount > 0 {
			tasks = fmt.Sprintf("0/%d", run.Status.Discovery.TaskCount)
		}

		var sampleRangeStr string
		if run.Spec.SampleRange != nil {
			sampleRangeStr = fmt.Sprintf("samples %d-%d", run.Spec.SampleRange.Start, run.Spec.SampleRange.End)
		} else {
			for _, t := range run.Status.Tasks {
				if t.SampleRange != nil && run.Spec.Limit == nil {
					sampleRangeStr = fmt.Sprintf("samples %d-%d", t.SampleRange.Start, t.SampleRange.End)
					break
				}
			}
		}

		items = append(items, RunListItem{
			Name:        run.Name,
			Namespace:   run.Namespace,
			Phase:       run.Status.Phase,
			PhaseChip:   phaseChipClass(run.Status.Phase),
			Tasks:       tasks,
			Samples:     samples,
			SampleRange: sampleRangeStr,
			Rates:       rates,
			Commit:      ShortCommit(run.Spec.Repo.Commit),
			Age:         FormatAge(run.CreationTimestamp),
			Link:        fmt.Sprintf("/runs/%s/%s", run.Namespace, run.Name),
		})
	}

	hasMore := end < totalCount
	hasPrev := start > 0

	var nextLink, prevLink string
	if hasMore {
		nextLink = buildPageURL("/runs", r.URL.Query(), end, limit)
	}
	if hasPrev {
		prevOffset := start - limit
		if prevOffset < 0 {
			prevOffset = 0
		}
		prevLink = buildPageURL("/runs", r.URL.Query(), prevOffset, limit)
	}

	showingRange := ""
	if totalCount > 0 {
		showingRange = fmt.Sprintf("Showing %d–%d of %d", start+1, end, totalCount)
	}

	view := RunsListView{
		ActiveTab:    NavTabRuns,
		Namespace:    ns,
		Runs:         items,
		TotalCount:   totalCount,
		Offset:       start,
		Limit:        limit,
		HasMore:      hasMore,
		NextLink:     nextLink,
		HasPrev:      hasPrev,
		PrevLink:     prevLink,
		ShowingRange: showingRange,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplRunsList.Execute(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func (s *Server) handleRepoOverview(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.parseRouteRepo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "not found", "invalid repo route params", nil)
		return
	}
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}
	s.renderRepoRuns(w, r, ns, ref, NavTabOverview)
}

func (s *Server) handleRepoRunsList(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.parseRouteRepo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "not found", "invalid repo route params", nil)
		return
	}
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}
	s.renderRepoRuns(w, r, ns, ref, NavTabRuns)
}

func (s *Server) renderRepoRuns(w http.ResponseWriter, r *http.Request, ns string, ref RepoRef, activeTab NavTab) {
	ctx := r.Context()
	offset, limit, err := parsePagination(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid pagination params", "invalid pagination query params", err)
		return
	}

	var runList v1alpha1.RunList
	if err := s.cfg.Client.List(ctx, &runList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to namespace denied", fmt.Sprintf("access to namespace %q denied", ns), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to list runs", fmt.Sprintf("failed to list runs in namespace %s", ns), err)
		return
	}

	var filteredRuns []v1alpha1.Run
	for _, run := range runList.Items {
		rRef, ok := runRepoRef(&run)
		if ok && rRef.Equal(ref) {
			filteredRuns = append(filteredRuns, run)
		}
	}

	sort.Slice(filteredRuns, func(i, j int) bool {
		if !filteredRuns[i].CreationTimestamp.Equal(&filteredRuns[j].CreationTimestamp) {
			return filteredRuns[i].CreationTimestamp.After(filteredRuns[j].CreationTimestamp.Time)
		}
		return filteredRuns[i].Name < filteredRuns[j].Name
	})

	totalCount := len(filteredRuns)
	start := offset
	if start > totalCount {
		start = totalCount
	}
	end := start + limit
	if end > totalCount {
		end = totalCount
	}

	pageRuns := filteredRuns[start:end]
	items := make([]RunListItem, 0, len(pageRuns))
	for _, run := range pageRuns {
		tasks := "—"
		samples := 0
		rates := ""
		if run.Status.Phase == v1alpha1.RunPhaseRunning {
			runningTasks := 0
			for _, t := range run.Status.Tasks {
				if t.Phase == v1alpha1.TaskPhaseRunning {
					runningTasks++
				}
			}
			totalTasks := len(run.Status.Tasks)
			if totalTasks == 0 && run.Status.Discovery != nil {
				totalTasks = run.Status.Discovery.TaskCount
			}
			tasks = fmt.Sprintf("%d/%d tasks running", runningTasks, totalTasks)
			if run.Status.Summary != nil {
				samples = run.Status.Summary.TotalSamples
				rates = run.Status.Summary.Rates
			}
		} else if run.Status.Summary != nil {
			if run.Status.Summary.Tasks != "" {
				tasks = run.Status.Summary.Tasks
			}
			samples = run.Status.Summary.TotalSamples
			rates = run.Status.Summary.Rates
		} else if run.Status.Discovery != nil && run.Status.Discovery.TaskCount > 0 {
			tasks = fmt.Sprintf("0/%d", run.Status.Discovery.TaskCount)
		}

		var sampleRangeStr string
		if run.Spec.SampleRange != nil {
			sampleRangeStr = fmt.Sprintf("samples %d-%d", run.Spec.SampleRange.Start, run.Spec.SampleRange.End)
		} else {
			for _, t := range run.Status.Tasks {
				if t.SampleRange != nil && run.Spec.Limit == nil {
					sampleRangeStr = fmt.Sprintf("samples %d-%d", t.SampleRange.Start, t.SampleRange.End)
					break
				}
			}
		}

		items = append(items, RunListItem{
			Name:        run.Name,
			Namespace:   run.Namespace,
			Phase:       run.Status.Phase,
			PhaseChip:   phaseChipClass(run.Status.Phase),
			Tasks:       tasks,
			Samples:     samples,
			SampleRange: sampleRangeStr,
			Rates:       rates,
			Commit:      ShortCommit(run.Spec.Repo.Commit),
			Age:         FormatAge(run.CreationTimestamp),
			Link:        repoLink(ref, "/runs/"+run.Name, ns),
		})
	}

	hasMore := end < totalCount
	hasPrev := start > 0

	basePath := ref.Path()
	if activeTab == NavTabRuns {
		basePath = ref.Path() + "/runs"
	}

	var nextLink, prevLink string
	if hasMore {
		nextLink = buildPageURL(basePath, r.URL.Query(), end, limit)
	}
	if hasPrev {
		prevOffset := start - limit
		if prevOffset < 0 {
			prevOffset = 0
		}
		prevLink = buildPageURL(basePath, r.URL.Query(), prevOffset, limit)
	}

	showingRange := ""
	if totalCount > 0 {
		showingRange = fmt.Sprintf("Showing %d–%d of %d", start+1, end, totalCount)
	}

	view := RunsListView{
		ActiveTab:    activeTab,
		Repo:         ref,
		Namespace:    ns,
		Runs:         items,
		TotalCount:   totalCount,
		Offset:       start,
		Limit:        limit,
		HasMore:      hasMore,
		NextLink:     nextLink,
		HasPrev:      hasPrev,
		PrevLink:     prevLink,
		ShowingRange: showingRange,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplRunsList.Execute(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns := r.PathValue("namespace")
	name := r.PathValue("name")
	if !isValidNamespace(ns) || !isValidResourceName(name) {
		httpError(w, http.StatusBadRequest, "invalid route params", fmt.Sprintf("invalid route params: namespace=%q name=%q", ns, name), nil)
		return
	}

	var run v1alpha1.Run
	if err := s.cfg.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			httpError(w, http.StatusNotFound, "run not found", "", nil)
			return
		}
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to run denied", fmt.Sprintf("access to run %s/%s denied", ns, name), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to get run", fmt.Sprintf("failed to get run %s/%s", ns, name), err)
		return
	}

	if ref, ok := runRepoRef(&run); ok {
		target := repoLink(ref, "/runs/"+name, ns)
		http.Redirect(w, r, target, http.StatusFound)
		return
	}

	s.renderRunDetail(w, r, &run, ns, RepoRef{})
}

func (s *Server) handleRepoRunDetail(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.parseRouteRepo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "not found", "invalid repo route params", nil)
		return
	}
	name := r.PathValue("name")
	if !isValidResourceName(name) {
		httpError(w, http.StatusBadRequest, "invalid route params", fmt.Sprintf("invalid run name: %q", name), nil)
		return
	}
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}
	ctx := r.Context()
	var run v1alpha1.Run
	if err := s.cfg.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			httpError(w, http.StatusNotFound, "run not found", "", nil)
			return
		}
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to run denied", fmt.Sprintf("access to run %s/%s denied", ns, name), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to get run", fmt.Sprintf("failed to get run %s/%s", ns, name), err)
		return
	}

	runRepo, ok := runRepoRef(&run)
	if !ok || !runRepo.Equal(ref) {
		httpError(w, http.StatusNotFound, "run not found", fmt.Sprintf("run repo %v does not match route repo %v", runRepo, ref), nil)
		return
	}

	s.renderRunDetail(w, r, &run, ns, ref)
}

func (s *Server) renderRunDetail(w http.ResponseWriter, r *http.Request, run *v1alpha1.Run, ns string, ref RepoRef) {
	name := run.Name
	metricFilter := parseMetricFilter(r)
	// Prepare task rows
	taskRows := make([]RunTaskRow, 0, len(run.Status.Tasks))
	var totalCostUSD float64
	var hasCost bool
	var totalTokens int
	var totalReasoningTokens int
	var hasTokens bool
	var sumP50, sumP95 float64
	var countLatency int
	for _, task := range run.Status.Tasks {
		leftPx, widthPx, dotPx, hasCI := CalculateRunBarGeometry(task.SuccessRate, task.MeanScore, task.SuccessRateCI, 240.0)

		rateStr := "—"
		if task.SuccessRate != nil {
			rateStr = fmt.Sprintf("%.2f", *task.SuccessRate)
		} else if task.MeanScore != nil {
			rateStr = fmt.Sprintf("%.2f", *task.MeanScore)
		}

		ciLabel := "—"
		method := ""
		if task.SuccessRateCI != nil {
			ciLabel = fmt.Sprintf("[%.2f, %.2f]", task.SuccessRateCI.Lower, task.SuccessRateCI.Upper)
			method = task.SuccessRateCI.Method
		}

		var taskSampleRange string
		if run.Spec.SampleRange != nil {
			taskSampleRange = fmt.Sprintf("samples %d-%d", run.Spec.SampleRange.Start, run.Spec.SampleRange.End)
		} else if task.SampleRange != nil && run.Spec.Limit == nil {
			taskSampleRange = fmt.Sprintf("samples %d-%d", task.SampleRange.Start, task.SampleRange.End)
		}

		var samplesLink string
		artifactsUnavailable := s.cfg.ArtifactSink == nil && task.LogPath != ""
		if task.LogPath != "" && s.cfg.ArtifactSink != nil {
			if ref.Host != "" {
				samplesLink = repoLink(ref, fmt.Sprintf("/runs/%s/tasks/%s/samples", name, url.PathEscape(task.ID)), ns)
			} else {
				samplesLink = BuildTaskSamplesListLink(ns, name, task.ID)
			}
		}

		samplesDone := task.SamplesDone
		samplesPlanned := task.SamplesPlanned
		if samplesPlanned == 0 {
			if task.SampleRange != nil {
				samplesPlanned = task.SampleRange.End - task.SampleRange.Start + 1
			} else if run.Spec.SampleRange != nil {
				samplesPlanned = run.Spec.SampleRange.End - run.Spec.SampleRange.Start + 1
			} else if run.Spec.Limit != nil {
				samplesPlanned = *run.Spec.Limit
			} else if task.PossibleSamples > 0 {
				samplesPlanned = task.PossibleSamples
			}
		}
		if task.PossibleSamples > 0 && task.PossibleSamples < samplesPlanned {
			samplesPlanned = task.PossibleSamples
		}

		var progressText string
		var progressPct float64
		if task.Phase == v1alpha1.TaskPhaseRunning {
			if samplesPlanned > 0 {
				progressText = fmt.Sprintf("%d of %d samples", samplesDone, samplesPlanned)
				progressPct = float64(samplesDone) / float64(samplesPlanned) * 100.0
				if progressPct > 100.0 {
					progressPct = 100.0
				}
			} else {
				progressText = fmt.Sprintf("%d samples", samplesDone)
			}
		}

		var latStr string = "—"
		if task.Latency != nil && (task.Latency.P50 > 0 || task.Latency.P95 > 0) {
			latStr = fmt.Sprintf("%.2fs / %.2fs", task.Latency.P50, task.Latency.P95)
			sumP50 += task.Latency.P50
			sumP95 += task.Latency.P95
			countLatency++
		}

		var tokensStr string = "—"
		var inTokensStr string = "—"
		var outTokensStr string = "—"
		var reasoningStr string = "—"
		var cacheReadStr string = "—"
		if task.Tokens != nil {
			hasTokens = true
			totalTokens += task.Tokens.Total
			tokensStr = fmt.Sprintf("%d", task.Tokens.Total)
			inTokensStr = fmt.Sprintf("%d", task.Tokens.Input)
			outTokensStr = fmt.Sprintf("%d", task.Tokens.Output)
			if task.Tokens.Reasoning > 0 {
				totalReasoningTokens += task.Tokens.Reasoning
				reasoningStr = fmt.Sprintf("%d", task.Tokens.Reasoning)
			}
			if task.Tokens.CacheRead > 0 {
				cacheReadStr = fmt.Sprintf("%d", task.Tokens.CacheRead)
			}
		}

		var costStr string = "—"
		if task.CostUSD != nil {
			hasCost = true
			totalCostUSD += *task.CostUSD
			costStr = fmt.Sprintf("$%.4f", *task.CostUSD)
		}

		taskRows = append(taskRows, RunTaskRow{
			ID:                   task.ID,
			Phase:                task.Phase,
			PhaseChip:            taskPhaseChipClass(task.Phase),
			RateStr:              rateStr,
			HasCI:                hasCI,
			CILeftPx:             leftPx,
			CIWidthPx:            widthPx,
			CIDotPx:              dotPx,
			CILabel:              ciLabel,
			Samples:              task.Samples,
			Successes:            task.Successes,
			Method:               method,
			LogPath:              task.LogPath,
			SamplesLink:          samplesLink,
			Message:              task.Message,
			SampleRange:          taskSampleRange,
			SamplesDone:          samplesDone,
			SamplesPlanned:       samplesPlanned,
			ProgressPct:          progressPct,
			ProgressText:         progressText,
			LatencyStr:           latStr,
			TokensStr:            tokensStr,
			InputTokens:          inTokensStr,
			OutputTokens:         outTokensStr,
			ReasoningStr:         reasoningStr,
			CacheReadStr:         cacheReadStr,
			CostStr:              costStr,
			ArtifactsUnavailable: artifactsUnavailable,
		})
	}

	if len(run.Status.Tasks) == 0 && run.Status.Discovery != nil {
		taskIDs := run.Status.Discovery.TaskIDs
		if s.cfg.ArtifactSink != nil && run.Status.Discovery.TaskCount > len(run.Status.Discovery.TaskIDs) && run.Status.Discovery.ManifestPath != "" {
			manifestBytes, loadErr := s.cfg.ArtifactSink.Load(r.Context(), run.Status.Discovery.ManifestPath)
			if loadErr != nil {
				// Fall back to capped taskIDs
			} else {
				var m v1alpha1.Manifest
				if parseErr := json.Unmarshal(manifestBytes, &m); parseErr != nil {
					// Fall back
				} else {
					var allIDs []string
					for _, t := range m.Tasks {
						allIDs = append(allIDs, t.ID)
					}
					taskIDs = allIDs
				}
			}
		}
		for _, id := range taskIDs {
			taskRows = append(taskRows, RunTaskRow{
				ID:        id,
				Phase:     "Discovered",
				PhaseChip: "neutral",
				RateStr:   "—",
			})
		}
	}

	tasksSummary := ""
	tasksCompleted := 0
	tasksFailed := 0
	tasksTotal := 0
	totalSamples := 0
	meanSuccessRate := ""
	successRateRange := ""
	compactRates := ""

	if run.Status.Summary != nil {
		tasksSummary = run.Status.Summary.Tasks
		tasksCompleted = run.Status.Summary.TasksCompleted
		tasksFailed = run.Status.Summary.TasksFailed
		tasksTotal = run.Status.Summary.TasksTotal
		totalSamples = run.Status.Summary.TotalSamples
		if run.Status.Summary.MeanSuccessRate != nil {
			meanSuccessRate = fmt.Sprintf("%.2f", *run.Status.Summary.MeanSuccessRate)
		}
		if run.Status.Summary.SuccessRateRange != nil {
			successRateRange = fmt.Sprintf("[%.2f, %.2f]", run.Status.Summary.SuccessRateRange.Min, run.Status.Summary.SuccessRateRange.Max)
		}
		compactRates = run.Status.Summary.Rates
	} else if run.Status.Discovery != nil && run.Status.Discovery.TaskCount > 0 {
		tasksSummary = fmt.Sprintf("0/%d", run.Status.Discovery.TaskCount)
		tasksTotal = run.Status.Discovery.TaskCount
	}

	var detailSampleRange string
	if run.Spec.SampleRange != nil {
		detailSampleRange = fmt.Sprintf("samples %d-%d", run.Spec.SampleRange.Start, run.Spec.SampleRange.End)
	} else {
		for _, t := range run.Status.Tasks {
			if t.SampleRange != nil && run.Spec.Limit == nil {
				detailSampleRange = fmt.Sprintf("samples %d-%d", t.SampleRange.Start, t.SampleRange.End)
				break
			}
		}
	}

	var totalCostFormatted string
	if hasCost {
		totalCostFormatted = fmt.Sprintf("$%.4f", totalCostUSD)
	}

	var totalTokensFormatted string
	if hasTokens {
		if totalReasoningTokens > 0 {
			totalTokensFormatted = fmt.Sprintf("%d (+%d R)", totalTokens, totalReasoningTokens)
		} else {
			totalTokensFormatted = fmt.Sprintf("%d", totalTokens)
		}
	}

	var latencySummaryFormatted string
	if countLatency > 0 {
		avgP50 := sumP50 / float64(countLatency)
		avgP95 := sumP95 / float64(countLatency)
		latencySummaryFormatted = fmt.Sprintf("%.2fs / %.2fs", avgP50, avgP95)
	}

	view := RunDetailView{
		ActiveTab:        NavTabRuns,
		Repo:             ref,
		Name:             run.Name,
		Namespace:        run.Namespace,
		Phase:            run.Status.Phase,
		PhaseChip:        phaseChipClass(run.Status.Phase),
		RepoURL:          run.Spec.Repo.URL,
		Commit:           run.Spec.Repo.Commit,
		CommitShort:      ShortCommit(run.Spec.Repo.Commit),
		Model:            run.Spec.Model,
		Age:              FormatAge(run.CreationTimestamp),
		ArtifactsPrefix:  run.Status.Artifacts,
		ArtifactFooter:   FormatArtifactFooter(s.cfg.ArtifactSink),
		Message:          run.Status.Message,
		Metric:           metricFilter,
		TotalCost:        totalCostFormatted,
		TokensSummary:    totalTokensFormatted,
		LatencySummary:   latencySummaryFormatted,
		TasksSummary:     tasksSummary,
		TasksCompleted:   tasksCompleted,
		TasksFailed:      tasksFailed,
		TasksTotal:       tasksTotal,
		TotalSamples:     totalSamples,
		SampleRange:      detailSampleRange,
		MeanSuccessRate:  meanSuccessRate,
		SuccessRateRange: successRateRange,
		CompactRates:     compactRates,
		Tasks:            taskRows,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplRunDetail.Execute(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func (s *Server) handleComparisonsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}

	offset, limit, err := parsePagination(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid pagination params", "invalid pagination query params", err)
		return
	}
	kind := ParseComparisonKind(r.URL.Query().Get("kind"))

	var compList v1alpha1.ComparisonList
	if err := s.cfg.Client.List(ctx, &compList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to namespace denied", fmt.Sprintf("access to namespace %q denied", ns), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to list comparisons", fmt.Sprintf("failed to list comparisons in namespace %s", ns), err)
		return
	}

	var runs map[string]*v1alpha1.Run
	if len(compList.Items) > 0 {
		runs = s.runsByName(ctx, ns)
	}

	// Rows are labeled candidate groups and unlabeled Comparisons, newest first.
	rows, totalCount := comparisonListRows(compList.Items, kind, runs)

	totalRows := len(rows)
	start := offset
	if start > totalRows {
		start = totalRows
	}
	end := start + limit
	if end > totalRows {
		end = totalRows
	}

	var items []ComparisonListItem
	var groups []ComparisonGroupRow
	for _, row := range rows[start:end] {
		if row.isGroup() {
			groups = append(groups, row.groupRow(runs))
		} else {
			items = append(items, row.listItem(runs))
		}
	}

	hasMore := end < totalRows
	hasPrev := start > 0

	var nextLink, prevLink string
	if hasMore {
		nextLink = buildPageURL("/comparisons", r.URL.Query(), end, limit)
	}
	if hasPrev {
		prevOffset := start - limit
		if prevOffset < 0 {
			prevOffset = 0
		}
		prevLink = buildPageURL("/comparisons", r.URL.Query(), prevOffset, limit)
	}

	showingRange := ""
	if totalRows > 0 {
		showingRange = fmt.Sprintf("Showing %d–%d of %d", start+1, end, totalRows)
	}

	query := r.URL.Query()
	view := ComparisonsListView{
		ActiveTab:          NavTabComparisons,
		Namespace:          ns,
		Comparisons:        items,
		TotalCount:         totalCount,
		Offset:             start,
		Limit:              limit,
		HasMore:            hasMore,
		NextLink:           nextLink,
		HasPrev:            hasPrev,
		PrevLink:           prevLink,
		ShowingRange:       showingRange,
		Kind:               kind,
		KindLabelKey:       kindLabelKey(kind),
		ShowKindFilter:     kind != ComparisonKindAll || hasLabeledComparison(compList.Items),
		KindAllLink:        comparisonsKindLink(query, ComparisonKindAll),
		KindBaselineLink:   comparisonsKindLink(query, ComparisonKindBaseline),
		KindPreviousLink:   comparisonsKindLink(query, ComparisonKindPrevious),
		Groups:             groups,
		ShowGroups:         len(groups) > 0 || kind != ComparisonKindAll,
		ShowBaselineColumn: kind != ComparisonKindPrevious,
		ShowPreviousColumn: kind != ComparisonKindBaseline,
		// The flat table also carries the empty state when a page has no rows at all.
		ShowFlat: kind == ComparisonKindAll && (len(items) > 0 || len(groups) == 0),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplComparisonsList.Execute(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

// runsByName lists the Runs in ns once per request so that each Comparison row can resolve
// its baseline and candidate Runs. A failed list is logged and returns nil: rows then show
// missing run deltas, and candidate groups are ordered by Comparison creation time.
func (s *Server) runsByName(ctx context.Context, ns string) map[string]*v1alpha1.Run {
	var runList v1alpha1.RunList
	if err := s.cfg.Client.List(ctx, &runList, client.InNamespace(ns)); err != nil {
		log.Printf("failed to list runs in namespace %s for the comparisons page: %v", ns, err)
		return nil
	}
	runs := make(map[string]*v1alpha1.Run, len(runList.Items))
	for i := range runList.Items {
		runs[runList.Items[i].Name] = &runList.Items[i]
	}
	return runs
}

func (s *Server) handleRepoComparisonsList(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.parseRouteRepo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "not found", "invalid repo route params", nil)
		return
	}
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}
	offset, limit, err := parsePagination(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid pagination params", "invalid pagination query params", err)
		return
	}
	kind := ParseComparisonKind(r.URL.Query().Get("kind"))
	ctx := r.Context()
	var compList v1alpha1.ComparisonList
	if err := s.cfg.Client.List(ctx, &compList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to namespace denied", fmt.Sprintf("access to namespace %q denied", ns), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to list comparisons", fmt.Sprintf("failed to list comparisons in namespace %s", ns), err)
		return
	}

	var runs map[string]*v1alpha1.Run
	if len(compList.Items) > 0 {
		runs = s.runsByName(ctx, ns)
	}

	var filteredComps []v1alpha1.Comparison
	for _, comp := range compList.Items {
		compRepo, ok := s.resolveComparisonRepo(ctx, &comp, runs)
		if ok && compRepo.Equal(ref) {
			filteredComps = append(filteredComps, comp)
		}
	}

	rows, totalCount := comparisonListRows(filteredComps, kind, runs)

	totalRows := len(rows)
	start := offset
	if start > totalRows {
		start = totalRows
	}
	end := start + limit
	if end > totalRows {
		end = totalRows
	}

	var items []ComparisonListItem
	var groups []ComparisonGroupRow
	for _, row := range rows[start:end] {
		if row.isGroup() {
			grp := row.groupRow(runs)
			grp.CandidateLink = repoLink(ref, "/runs/"+grp.CandidateRun, ns)
			if grp.VsBaseline != nil {
				grp.VsBaseline.Link = repoLink(ref, "/comparisons/"+grp.VsBaseline.Name, ns)
				if grp.VsBaseline.BaselineRun != "" {
					grp.VsBaseline.BaselineLink = repoLink(ref, "/runs/"+grp.VsBaseline.BaselineRun, ns)
				}
				for j := range grp.VsBaseline.Older {
					grp.VsBaseline.Older[j].Link = repoLink(ref, "/comparisons/"+grp.VsBaseline.Older[j].Name, ns)
				}
			}
			if grp.VsPrevious != nil {
				grp.VsPrevious.Link = repoLink(ref, "/comparisons/"+grp.VsPrevious.Name, ns)
				if grp.VsPrevious.BaselineRun != "" {
					grp.VsPrevious.BaselineLink = repoLink(ref, "/runs/"+grp.VsPrevious.BaselineRun, ns)
				}
				for j := range grp.VsPrevious.Older {
					grp.VsPrevious.Older[j].Link = repoLink(ref, "/comparisons/"+grp.VsPrevious.Older[j].Name, ns)
				}
			}
			groups = append(groups, grp)
		} else {
			item := row.listItem(runs)
			item.Link = repoLink(ref, "/comparisons/"+item.Name, ns)
			items = append(items, item)
		}
	}

	hasMore := end < totalRows
	hasPrev := start > 0

	var nextLink, prevLink string
	basePath := ref.Path() + "/comparisons"
	if hasMore {
		nextLink = buildPageURL(basePath, r.URL.Query(), end, limit)
	}
	if hasPrev {
		prevOffset := start - limit
		if prevOffset < 0 {
			prevOffset = 0
		}
		prevLink = buildPageURL(basePath, r.URL.Query(), prevOffset, limit)
	}

	showingRange := ""
	if totalRows > 0 {
		showingRange = fmt.Sprintf("Showing %d–%d of %d", start+1, end, totalRows)
	}

	query := r.URL.Query()
	view := ComparisonsListView{
		ActiveTab:          NavTabComparisons,
		Repo:               ref,
		Namespace:          ns,
		Comparisons:        items,
		TotalCount:         totalCount,
		Offset:             start,
		Limit:              limit,
		HasMore:            hasMore,
		NextLink:           nextLink,
		HasPrev:            hasPrev,
		PrevLink:           prevLink,
		ShowingRange:       showingRange,
		Kind:               kind,
		KindLabelKey:       kindLabelKey(kind),
		ShowKindFilter:     kind != ComparisonKindAll || hasLabeledComparison(filteredComps),
		KindAllLink:        comparisonsKindLink(query, ComparisonKindAll),
		KindBaselineLink:   comparisonsKindLink(query, ComparisonKindBaseline),
		KindPreviousLink:   comparisonsKindLink(query, ComparisonKindPrevious),
		Groups:             groups,
		ShowGroups:         len(groups) > 0 || kind != ComparisonKindAll,
		ShowBaselineColumn: kind != ComparisonKindPrevious,
		ShowPreviousColumn: kind != ComparisonKindBaseline,
		ShowFlat:           kind == ComparisonKindAll && (len(items) > 0 || len(groups) == 0),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplComparisonsList.Execute(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func (s *Server) handleComparisonDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns := r.PathValue("namespace")
	name := r.PathValue("name")
	if !isValidNamespace(ns) || !isValidResourceName(name) {
		httpError(w, http.StatusBadRequest, "invalid route params", fmt.Sprintf("invalid route params: namespace=%q name=%q", ns, name), nil)
		return
	}

	var comp v1alpha1.Comparison
	if err := s.cfg.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &comp); err != nil {
		if apierrors.IsNotFound(err) {
			httpError(w, http.StatusNotFound, "comparison not found", "", nil)
			return
		}
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to comparison denied", fmt.Sprintf("access to comparison %s/%s denied", ns, name), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to get comparison", fmt.Sprintf("failed to get comparison %s/%s", ns, name), err)
		return
	}

	if ref, ok := s.resolveComparisonRepo(ctx, &comp, nil); ok {
		target := repoLink(ref, "/comparisons/"+name, ns)
		http.Redirect(w, r, target, http.StatusFound)
		return
	}

	s.renderComparisonDetail(w, r, &comp, ns, RepoRef{})
}

func (s *Server) handleRepoComparisonDetail(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.parseRouteRepo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "not found", "invalid repo route params", nil)
		return
	}
	name := r.PathValue("name")
	if !isValidResourceName(name) {
		httpError(w, http.StatusBadRequest, "invalid route params", fmt.Sprintf("invalid comparison name: %q", name), nil)
		return
	}
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}
	ctx := r.Context()
	var comp v1alpha1.Comparison
	if err := s.cfg.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &comp); err != nil {
		if apierrors.IsNotFound(err) {
			httpError(w, http.StatusNotFound, "comparison not found", "", nil)
			return
		}
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to comparison denied", fmt.Sprintf("access to comparison %s/%s denied", ns, name), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to get comparison", fmt.Sprintf("failed to get comparison %s/%s", ns, name), err)
		return
	}

	compRepo, ok := s.resolveComparisonRepo(ctx, &comp, nil)
	if !ok || !compRepo.Equal(ref) {
		httpError(w, http.StatusNotFound, "comparison not found", fmt.Sprintf("comparison repo %v does not match route repo %v", compRepo, ref), nil)
		return
	}

	s.renderComparisonDetail(w, r, &comp, ns, ref)
}

func (s *Server) renderComparisonDetail(w http.ResponseWriter, r *http.Request, comp *v1alpha1.Comparison, ns string, ref RepoRef) {
	ctx := r.Context()
	// Fetch baseline and candidate Run resources to discover commits and sample counts
	var baseRunPtr, candRunPtr *v1alpha1.Run
	if comp.Spec.Baseline.Run != "" {
		var baseRun v1alpha1.Run
		if err := s.cfg.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: comp.Spec.Baseline.Run}, &baseRun); err != nil {
			if !apierrors.IsNotFound(err) {
				// Non-fatal lookup error, continue with run name reference
				_ = err
			}
		} else {
			baseRunPtr = &baseRun
		}
	}

	if comp.Spec.Candidate.Run != "" {
		var candRun v1alpha1.Run
		if err := s.cfg.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: comp.Spec.Candidate.Run}, &candRun); err != nil {
			if !apierrors.IsNotFound(err) {
				// Non-fatal lookup error, continue with run name reference
				_ = err
			}
		} else {
			candRunPtr = &candRun
		}
	}

	view := NewComparisonDetailView(comp, baseRunPtr, candRunPtr)
	if ref.Host != "" {
		view.Repo = ref
	}
	view.Metric = parseMetricFilter(r)
	s.populateComparisonHistories(ctx, comp, &view)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplComparisonDetail.Execute(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func (s *Server) handlePullsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}

	repoQuery := r.URL.Query().Get("repo")
	if repoQuery == "" {
		repoQuery = r.URL.Query().Get("repohash")
	}
	numberQuery := r.URL.Query().Get("number")
	if repoQuery != "" && numberQuery != "" {
		num, err := strconv.Atoi(numberQuery)
		if err != nil || num <= 0 {
			httpError(w, http.StatusBadRequest, "invalid route params", fmt.Sprintf("invalid pr number in query: %q", numberQuery), nil)
			return
		}
		s.renderPullHistory(w, r, ns, repoQuery, num)
		return
	}

	offset, limit, err := parsePagination(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid pagination params", "invalid pagination query params", err)
		return
	}

	var compList v1alpha1.ComparisonList
	if err := s.cfg.Client.List(ctx, &compList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to namespace denied", fmt.Sprintf("access to namespace %q denied", ns), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to list comparisons", fmt.Sprintf("failed to list comparisons in namespace %s", ns), err)
		return
	}

	type prGroup struct {
		repository  string
		repoHash    string
		number      int
		comparisons []v1alpha1.Comparison
	}

	groupsMap := make(map[string]*prGroup)
	var groupKeys []string

	for _, comp := range compList.Items {
		if comp.Spec.PullRequest == nil || comp.Spec.PullRequest.Repository == "" || comp.Spec.PullRequest.Number <= 0 {
			continue
		}
		repo := comp.Spec.PullRequest.Repository
		prNum := comp.Spec.PullRequest.Number
		key := fmt.Sprintf("%s#%d", strings.ToLower(strings.TrimSpace(repo)), prNum)

		grp, exists := groupsMap[key]
		if !exists {
			grp = &prGroup{
				repository: repo,
				repoHash:   RepoHash(repo),
				number:     prNum,
			}
			groupsMap[key] = grp
			groupKeys = append(groupKeys, key)
		}
		grp.comparisons = append(grp.comparisons, comp)
	}

	// For each group, sort comparisons newest first (by CreationTimestamp descending, tiebreaker Name ascending)
	groups := make([]*prGroup, 0, len(groupKeys))
	for _, key := range groupKeys {
		grp := groupsMap[key]
		sort.Slice(grp.comparisons, func(i, j int) bool {
			if !grp.comparisons[i].CreationTimestamp.Equal(&grp.comparisons[j].CreationTimestamp) {
				return grp.comparisons[i].CreationTimestamp.After(grp.comparisons[j].CreationTimestamp.Time)
			}
			return grp.comparisons[i].Name < grp.comparisons[j].Name
		})
		groups = append(groups, grp)
	}

	// Sort groups newest head first (by latest comparison CreationTimestamp descending, tiebreaker repo, then number)
	sort.Slice(groups, func(i, j int) bool {
		latestI := groups[i].comparisons[0]
		latestJ := groups[j].comparisons[0]
		if !latestI.CreationTimestamp.Equal(&latestJ.CreationTimestamp) {
			return latestI.CreationTimestamp.After(latestJ.CreationTimestamp.Time)
		}
		if groups[i].repository != groups[j].repository {
			return groups[i].repository < groups[j].repository
		}
		return groups[i].number < groups[j].number
	})

	totalCount := len(groups)
	start := offset
	if start > totalCount {
		start = totalCount
	}
	end := start + limit
	if end > totalCount {
		end = totalCount
	}

	pageGroups := groups[start:end]
	items := make([]PullRequestListItem, 0, len(pageGroups))
	for _, grp := range pageGroups {
		latest := grp.comparisons[0]
		pImp := "—"
		if latest.Status.PImprovement != nil {
			pImp = fmt.Sprintf("%.2f", *latest.Status.PImprovement)
		}

		historyLink := fmt.Sprintf("/pulls/%s/%d", grp.repoHash, grp.number)
		if ns != s.cfg.DefaultNamespace {
			historyLink += "?namespace=" + url.QueryEscape(ns)
		}

		items = append(items, PullRequestListItem{
			Repository:     grp.repository,
			RepoHash:       grp.repoHash,
			Number:         grp.number,
			GitHubURL:      BuildGitHubPRURL(grp.repository, grp.number),
			HeadSHA:        latest.Spec.PullRequest.HeadSHA,
			HeadSHAShort:   ShortCommit(latest.Spec.PullRequest.HeadSHA),
			Verdict:        latest.Status.Verdict,
			VerdictChip:    verdictChipClass(latest.Status.Verdict),
			PImprovement:   pImp,
			SamplesRun:     ComparisonSamplesRun(&latest),
			Batches:        len(latest.Status.Batches),
			CompletionTime: FormatCompletionTime(latest.Status.CompletionTime),
			ComparisonName: latest.Name,
			ComparisonLink: fmt.Sprintf("/comparisons/%s/%s", latest.Namespace, latest.Name),
			HistoryLink:    historyLink,
			Namespace:      latest.Namespace,
		})
	}

	hasMore := end < totalCount
	hasPrev := start > 0

	var nextLink, prevLink string
	if hasMore {
		nextLink = buildPageURL("/pulls", r.URL.Query(), end, limit)
	}
	if hasPrev {
		prevOffset := start - limit
		if prevOffset < 0 {
			prevOffset = 0
		}
		prevLink = buildPageURL("/pulls", r.URL.Query(), prevOffset, limit)
	}

	showingRange := ""
	if totalCount > 0 {
		showingRange = fmt.Sprintf("Showing %d–%d of %d", start+1, end, totalCount)
	}

	view := PullRequestsListView{
		ActiveTab:    NavTabPulls,
		Namespace:    ns,
		Pulls:        items,
		TotalCount:   totalCount,
		Offset:       start,
		Limit:        limit,
		HasMore:      hasMore,
		NextLink:     nextLink,
		HasPrev:      hasPrev,
		PrevLink:     prevLink,
		ShowingRange: showingRange,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplPullsList.Execute(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func (s *Server) handleRepoPullsList(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.parseRouteRepo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "not found", "invalid repo route params", nil)
		return
	}
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}
	offset, limit, err := parsePagination(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid pagination params", "invalid pagination query params", err)
		return
	}
	ctx := r.Context()
	var compList v1alpha1.ComparisonList
	if err := s.cfg.Client.List(ctx, &compList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to namespace denied", fmt.Sprintf("access to namespace %q denied", ns), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to list comparisons", fmt.Sprintf("failed to list comparisons in namespace %s", ns), err)
		return
	}

	type prGroup struct {
		number      int
		comparisons []v1alpha1.Comparison
	}

	groupsMap := make(map[int]*prGroup)
	var groupNumbers []int

	for _, comp := range compList.Items {
		if comp.Spec.PullRequest == nil || comp.Spec.PullRequest.Number <= 0 {
			continue
		}
		compRepo, ok := s.resolveComparisonRepo(ctx, &comp, nil)
		if !ok || !compRepo.Equal(ref) {
			continue
		}
		prNum := comp.Spec.PullRequest.Number
		grp, exists := groupsMap[prNum]
		if !exists {
			grp = &prGroup{number: prNum}
			groupsMap[prNum] = grp
			groupNumbers = append(groupNumbers, prNum)
		}
		grp.comparisons = append(grp.comparisons, comp)
	}

	groups := make([]*prGroup, 0, len(groupNumbers))
	for _, prNum := range groupNumbers {
		grp := groupsMap[prNum]
		sort.Slice(grp.comparisons, func(i, j int) bool {
			if !grp.comparisons[i].CreationTimestamp.Equal(&grp.comparisons[j].CreationTimestamp) {
				return grp.comparisons[i].CreationTimestamp.After(grp.comparisons[j].CreationTimestamp.Time)
			}
			return grp.comparisons[i].Name < grp.comparisons[j].Name
		})
		groups = append(groups, grp)
	}

	sort.Slice(groups, func(i, j int) bool {
		latestI := groups[i].comparisons[0]
		latestJ := groups[j].comparisons[0]
		if !latestI.CreationTimestamp.Equal(&latestJ.CreationTimestamp) {
			return latestI.CreationTimestamp.After(latestJ.CreationTimestamp.Time)
		}
		return groups[i].number < groups[j].number
	})

	totalCount := len(groups)
	start := offset
	if start > totalCount {
		start = totalCount
	}
	end := start + limit
	if end > totalCount {
		end = totalCount
	}

	pageGroups := groups[start:end]
	items := make([]PullRequestListItem, 0, len(pageGroups))
	for _, grp := range pageGroups {
		latest := grp.comparisons[0]
		pImp := "—"
		if latest.Status.PImprovement != nil {
			pImp = fmt.Sprintf("%.2f", *latest.Status.PImprovement)
		}

		items = append(items, PullRequestListItem{
			Repository:     ref.Owner + "/" + ref.Name,
			RepoHash:       RepoHash(ref.Owner + "/" + ref.Name),
			Number:         grp.number,
			GitHubURL:      BuildGitHubPRURL(ref.Owner+"/"+ref.Name, grp.number),
			HeadSHA:        latest.Spec.PullRequest.HeadSHA,
			HeadSHAShort:   ShortCommit(latest.Spec.PullRequest.HeadSHA),
			Verdict:        latest.Status.Verdict,
			VerdictChip:    verdictChipClass(latest.Status.Verdict),
			PImprovement:   pImp,
			SamplesRun:     ComparisonSamplesRun(&latest),
			Batches:        len(latest.Status.Batches),
			CompletionTime: FormatCompletionTime(latest.Status.CompletionTime),
			ComparisonName: latest.Name,
			ComparisonLink: repoLink(ref, "/comparisons/"+latest.Name, ns),
			HistoryLink:    repoLink(ref, fmt.Sprintf("/pulls/%d", grp.number), ns),
			Namespace:      latest.Namespace,
		})
	}

	hasMore := end < totalCount
	hasPrev := start > 0

	var nextLink, prevLink string
	basePath := ref.Path() + "/pulls"
	if hasMore {
		nextLink = buildPageURL(basePath, r.URL.Query(), end, limit)
	}
	if hasPrev {
		prevOffset := start - limit
		if prevOffset < 0 {
			prevOffset = 0
		}
		prevLink = buildPageURL(basePath, r.URL.Query(), prevOffset, limit)
	}

	showingRange := ""
	if totalCount > 0 {
		showingRange = fmt.Sprintf("Showing %d–%d of %d", start+1, end, totalCount)
	}

	view := PullRequestsListView{
		ActiveTab:    NavTabPulls,
		Repo:         ref,
		Namespace:    ns,
		Pulls:        items,
		TotalCount:   totalCount,
		Offset:       start,
		Limit:        limit,
		HasMore:      hasMore,
		NextLink:     nextLink,
		HasPrev:      hasPrev,
		PrevLink:     prevLink,
		ShowingRange: showingRange,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplPullsList.Execute(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func (s *Server) handlePullDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repohash := r.PathValue("repohash")
	numberStr := r.PathValue("number")
	if !isValidRepoHash(repohash) {
		httpError(w, http.StatusBadRequest, "invalid route params", fmt.Sprintf("invalid repo hash: %q", repohash), nil)
		return
	}
	num, err := strconv.Atoi(numberStr)
	if err != nil || num <= 0 {
		httpError(w, http.StatusBadRequest, "invalid route params", fmt.Sprintf("invalid pr number: %q", numberStr), nil)
		return
	}

	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}

	var compList v1alpha1.ComparisonList
	if err := s.cfg.Client.List(ctx, &compList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to namespace denied", fmt.Sprintf("access to namespace %q denied", ns), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to list comparisons", fmt.Sprintf("failed to list comparisons in namespace %s", ns), err)
		return
	}
	for _, comp := range compList.Items {
		if comp.Spec.PullRequest == nil || comp.Spec.PullRequest.Number != num {
			continue
		}
		repo := comp.Spec.PullRequest.Repository
		hash := RepoHash(repo)
		if strings.EqualFold(hash, repohash) || strings.EqualFold(repo, repohash) {
			if ref, ok := s.resolveComparisonRepo(ctx, &comp, nil); ok {
				target := repoLink(ref, fmt.Sprintf("/pulls/%d", num), ns)
				http.Redirect(w, r, target, http.StatusFound)
				return
			}
		}
	}

	s.renderPullHistory(w, r, ns, repohash, num)
}

func (s *Server) handleRepoPullDetail(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.parseRouteRepo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "not found", "invalid repo route params", nil)
		return
	}
	numberStr := r.PathValue("number")
	num, err := strconv.Atoi(numberStr)
	if err != nil || num <= 0 {
		httpError(w, http.StatusBadRequest, "invalid route params", fmt.Sprintf("invalid pr number: %q", numberStr), nil)
		return
	}
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}
	ctx := r.Context()
	var compList v1alpha1.ComparisonList
	if err := s.cfg.Client.List(ctx, &compList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to namespace denied", fmt.Sprintf("access to namespace %q denied", ns), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to list comparisons", fmt.Sprintf("failed to list comparisons in namespace %s", ns), err)
		return
	}

	var matched []v1alpha1.Comparison
	for _, comp := range compList.Items {
		if comp.Spec.PullRequest == nil || comp.Spec.PullRequest.Number != num {
			continue
		}
		compRepo, ok := s.resolveComparisonRepo(ctx, &comp, nil)
		if ok && compRepo.Equal(ref) {
			matched = append(matched, comp)
		}
	}

	if len(matched) == 0 {
		httpError(w, http.StatusNotFound, "pull request not found", fmt.Sprintf("no comparisons for %s #%d in %s", ref.String(), num, ns), nil)
		return
	}

	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].CreationTimestamp.Equal(&matched[j].CreationTimestamp) {
			return matched[i].CreationTimestamp.After(matched[j].CreationTimestamp.Time)
		}
		return matched[i].Name < matched[j].Name
	})

	historyItems := make([]PullRequestHistoryItem, 0, len(matched))
	for _, comp := range matched {
		pImp := "—"
		if comp.Status.PImprovement != nil {
			pImp = fmt.Sprintf("%.2f", *comp.Status.PImprovement)
		}

		headSHA := ""
		baseSHA := ""
		if comp.Spec.PullRequest != nil {
			headSHA = comp.Spec.PullRequest.HeadSHA
			baseSHA = comp.Spec.PullRequest.BaseSHA
		}
		headSHAShort := ShortCommit(headSHA)
		baseSHAShort := ShortCommit(baseSHA)

		historyItems = append(historyItems, PullRequestHistoryItem{
			Name:           comp.Name,
			Namespace:      comp.Namespace,
			HeadSHA:        headSHA,
			HeadSHAShort:   headSHAShort,
			BaseSHA:        baseSHA,
			BaseSHAShort:   baseSHAShort,
			Verdict:        comp.Status.Verdict,
			VerdictChip:    verdictChipClass(comp.Status.Verdict),
			PImprovement:   pImp,
			SamplesRun:     ComparisonSamplesRun(&comp),
			Batches:        len(comp.Status.Batches),
			CompletionTime: FormatCompletionTime(comp.Status.CompletionTime),
			Age:            FormatAge(comp.CreationTimestamp),
			Link:           repoLink(ref, "/comparisons/"+comp.Name, ns),
		})
	}

	view := PullRequestDetailView{
		ActiveTab:   NavTabPulls,
		Repo:        ref,
		Namespace:   ns,
		Repository:  ref.Owner + "/" + ref.Name,
		RepoHash:    RepoHash(ref.Owner + "/" + ref.Name),
		Number:      num,
		GitHubURL:   BuildGitHubPRURL(ref.Owner+"/"+ref.Name, num),
		BackLink:    repoLink(ref, "/pulls", ns),
		Comparisons: historyItems,
		TotalCount:  len(historyItems),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderPullDetail(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func (s *Server) renderPullHistory(w http.ResponseWriter, r *http.Request, ns string, repoOrHash string, num int) {
	ctx := r.Context()
	var compList v1alpha1.ComparisonList
	if err := s.cfg.Client.List(ctx, &compList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to namespace denied", fmt.Sprintf("access to namespace %q denied", ns), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to list comparisons", fmt.Sprintf("failed to list comparisons in namespace %s", ns), err)
		return
	}

	var matched []v1alpha1.Comparison
	repoDisplay := ""
	for _, comp := range compList.Items {
		if comp.Spec.PullRequest == nil || comp.Spec.PullRequest.Number != num {
			continue
		}
		repo := comp.Spec.PullRequest.Repository
		hash := RepoHash(repo)
		if strings.EqualFold(hash, repoOrHash) || strings.EqualFold(repo, repoOrHash) {
			matched = append(matched, comp)
			if repoDisplay == "" {
				repoDisplay = repo
			}
		}
	}

	if len(matched) == 0 {
		httpError(w, http.StatusNotFound, "pull request not found", fmt.Sprintf("no comparisons for %s #%d in %s", repoOrHash, num, ns), nil)
		return
	}

	// Sort matched comparisons newest first (by CreationTimestamp descending, tiebreaker Name ascending)
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].CreationTimestamp.Equal(&matched[j].CreationTimestamp) {
			return matched[i].CreationTimestamp.After(matched[j].CreationTimestamp.Time)
		}
		return matched[i].Name < matched[j].Name
	})

	historyItems := make([]PullRequestHistoryItem, 0, len(matched))
	for _, comp := range matched {
		pImp := "—"
		if comp.Status.PImprovement != nil {
			pImp = fmt.Sprintf("%.2f", *comp.Status.PImprovement)
		}

		headSHA := ""
		baseSHA := ""
		if comp.Spec.PullRequest != nil {
			headSHA = comp.Spec.PullRequest.HeadSHA
			baseSHA = comp.Spec.PullRequest.BaseSHA
		}

		historyItems = append(historyItems, PullRequestHistoryItem{
			Name:           comp.Name,
			Namespace:      comp.Namespace,
			HeadSHA:        headSHA,
			HeadSHAShort:   ShortCommit(headSHA),
			BaseSHA:        baseSHA,
			BaseSHAShort:   ShortCommit(baseSHA),
			Verdict:        comp.Status.Verdict,
			VerdictChip:    verdictChipClass(comp.Status.Verdict),
			PImprovement:   pImp,
			SamplesRun:     ComparisonSamplesRun(&comp),
			Batches:        len(comp.Status.Batches),
			CompletionTime: FormatCompletionTime(comp.Status.CompletionTime),
			Age:            FormatAge(comp.CreationTimestamp),
			Link:           fmt.Sprintf("/comparisons/%s/%s", comp.Namespace, comp.Name),
		})
	}

	backLink := "/pulls"
	if ns != s.cfg.DefaultNamespace {
		backLink += "?namespace=" + url.QueryEscape(ns)
	}

	view := PullRequestDetailView{
		ActiveTab:   NavTabPulls,
		Namespace:   ns,
		Repository:  repoDisplay,
		RepoHash:    RepoHash(repoDisplay),
		Number:      num,
		GitHubURL:   BuildGitHubPRURL(repoDisplay, num),
		BackLink:    backLink,
		Comparisons: historyItems,
		TotalCount:  len(historyItems),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplPullDetail.Execute(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func sanitizeBranchLabel(val string) string {
	val = strings.TrimSpace(val)
	if val == "" {
		return ""
	}
	var sb strings.Builder
	for _, r := range val {
		switch {
		case r == '/':
			sb.WriteRune('_')
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	return sb.String()
}

func runRepository(run *v1alpha1.Run) string {
	if run == nil {
		return ""
	}
	if run.Spec.Repo.URL != "" {
		cleaned := CleanRepo(run.Spec.Repo.URL)
		if cleaned != "" {
			return cleaned
		}
	}
	if labelVal, ok := run.Labels["evalrunner.gke-labs.dev/repo"]; ok && labelVal != "" {
		return strings.ReplaceAll(labelVal, "_", "/")
	}
	return ""
}

func runCommitTime(run *v1alpha1.Run) (time.Time, bool) {
	if run.Annotations != nil {
		if raw, ok := run.Annotations["evalrunner.gke-labs.dev/commit-time"]; ok && raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return run.CreationTimestamp.Time, true
			}
			return t, false
		}
	}
	return run.CreationTimestamp.Time, true
}

func (s *Server) handleSeriesList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}

	cohortFilter := strings.TrimSpace(r.URL.Query().Get("cohort"))

	var runList v1alpha1.RunList
	if err := s.cfg.Client.List(ctx, &runList, client.InNamespace(ns)); err != nil {
		httpError(w, http.StatusInternalServerError, "failed to list runs", "k8s list runs failed", err)
		return
	}

	type groupKey struct {
		repo   string
		branch string
	}
	counts := make(map[groupKey]int)
	repoHashes := make(map[string]string)

	for i := range runList.Items {
		run := &runList.Items[i]
		if !strings.HasPrefix(run.Name, "base-") {
			continue
		}
		branch := run.Labels["evalrunner.gke-labs.dev/branch"]
		if branch == "" {
			continue
		}
		if cohortFilter != "" && run.Spec.Cohort != cohortFilter {
			continue
		}

		repo := runRepository(run)
		if repo == "" {
			continue
		}

		gk := groupKey{repo: repo, branch: branch}
		counts[gk]++
		repoHashes[repo] = RepoHash(repo)
	}

	var seriesRows []SeriesListRow
	for gk, count := range counts {
		rHash := repoHashes[gk.repo]
		link := fmt.Sprintf("/series/%s/%s", url.PathEscape(rHash), url.PathEscape(gk.branch))
		q := url.Values{}
		if ns != s.cfg.DefaultNamespace {
			q.Set("namespace", ns)
		}
		if cohortFilter != "" {
			q.Set("cohort", cohortFilter)
		}
		if len(q) > 0 {
			link += "?" + q.Encode()
		}

		seriesRows = append(seriesRows, SeriesListRow{
			Repository: gk.repo,
			RepoHash:   rHash,
			Branch:     gk.branch,
			RunCount:   count,
			Link:       link,
		})
	}

	sort.Slice(seriesRows, func(i, j int) bool {
		if seriesRows[i].Repository != seriesRows[j].Repository {
			return seriesRows[i].Repository < seriesRows[j].Repository
		}
		return seriesRows[i].Branch < seriesRows[j].Branch
	})

	view := SeriesListView{
		ActiveTab:  NavTabSeries,
		Namespace:  ns,
		Cohort:     cohortFilter,
		Series:     seriesRows,
		TotalCount: len(seriesRows),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderSeriesList(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func (s *Server) handleSeriesDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}

	repoOrHash := r.PathValue("repohash")
	branch := r.PathValue("branch")
	if repoOrHash == "" {
		trimmed := strings.TrimPrefix(r.URL.Path, "/series/")
		parts := strings.SplitN(trimmed, "/", 2)
		if len(parts) >= 1 {
			repoOrHash = parts[0]
		}
		if len(parts) == 2 {
			branch = parts[1]
		}
	}

	if !isValidRepoHash(repoOrHash) {
		httpError(w, http.StatusBadRequest, "invalid repo hash", fmt.Sprintf("invalid repo hash: %q", repoOrHash), nil)
		return
	}

	if branch == "" {
		httpError(w, http.StatusBadRequest, "invalid branch", "empty branch in path", nil)
		return
	}

	cohortFilter := strings.TrimSpace(r.URL.Query().Get("cohort"))

	// Check if we can resolve the repository and 302 redirect
	var runList v1alpha1.RunList
	if err := s.cfg.Client.List(ctx, &runList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			httpError(w, http.StatusForbidden, "forbidden: access to namespace denied", fmt.Sprintf("access to namespace %q denied", ns), err)
			return
		}
		httpError(w, http.StatusInternalServerError, "failed to list runs", fmt.Sprintf("failed to list runs in namespace %s", ns), err)
		return
	}
	for i := range runList.Items {
		run := &runList.Items[i]
		if !strings.HasPrefix(run.Name, "base-") {
			continue
		}
		runBranch := run.Labels["evalrunner.gke-labs.dev/branch"]
		if runBranch != branch && runBranch != sanitizeBranchLabel(branch) {
			continue
		}
		repo := runRepository(run)
		h := RepoHash(repo)
		if strings.EqualFold(h, repoOrHash) || strings.EqualFold(repo, repoOrHash) {
			if ref, ok := runRepoRef(run); ok {
				target := repoLink(ref, "/series/"+branch, ns)
				if cohortFilter != "" {
					sep := "?"
					if strings.Contains(target, "?") {
						sep = "&"
					}
					target += sep + "cohort=" + url.QueryEscape(cohortFilter)
				}
				http.Redirect(w, r, target, http.StatusFound)
				return
			}
		}
	}

	s.renderSeriesDetailLegacy(w, r, ns, repoOrHash, branch)
}

func (s *Server) handleRepoSeriesList(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.parseRouteRepo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "not found", "invalid repo route params", nil)
		return
	}
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}
	cohortFilter := strings.TrimSpace(r.URL.Query().Get("cohort"))

	ctx := r.Context()
	var runList v1alpha1.RunList
	if err := s.cfg.Client.List(ctx, &runList, client.InNamespace(ns)); err != nil {
		httpError(w, http.StatusInternalServerError, "failed to list runs", "k8s list runs failed", err)
		return
	}

	counts := make(map[string]int)
	for i := range runList.Items {
		run := &runList.Items[i]
		if !strings.HasPrefix(run.Name, "base-") {
			continue
		}
		branch := run.Labels["evalrunner.gke-labs.dev/branch"]
		if branch == "" {
			continue
		}
		if cohortFilter != "" && run.Spec.Cohort != cohortFilter {
			continue
		}
		runRepo, ok := runRepoRef(run)
		if !ok || !runRepo.Equal(ref) {
			continue
		}
		counts[branch]++
	}

	var seriesRows []SeriesListRow
	for branch, count := range counts {
		link := repoLink(ref, "/series/"+branch, ns)
		if cohortFilter != "" {
			sep := "?"
			if strings.Contains(link, "?") {
				sep = "&"
			}
			link += sep + "cohort=" + url.QueryEscape(cohortFilter)
		}
		seriesRows = append(seriesRows, SeriesListRow{
			Repository: ref.Owner + "/" + ref.Name,
			RepoHash:   RepoHash(ref.Owner + "/" + ref.Name),
			Branch:     branch,
			RunCount:   count,
			Link:       link,
		})
	}

	sort.Slice(seriesRows, func(i, j int) bool {
		return seriesRows[i].Branch < seriesRows[j].Branch
	})

	view := SeriesListView{
		ActiveTab:  NavTabSeries,
		Repo:       ref,
		Namespace:  ns,
		Cohort:     cohortFilter,
		Series:     seriesRows,
		TotalCount: len(seriesRows),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderSeriesList(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func (s *Server) handleRepoSeriesDetail(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.parseRouteRepo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "not found", "invalid repo route params", nil)
		return
	}
	branch := r.PathValue("branch")
	if branch == "" {
		httpError(w, http.StatusBadRequest, "invalid branch", "empty branch in path", nil)
		return
	}
	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}
	s.renderRepoSeriesDetail(w, r, ns, ref, branch)
}

func (s *Server) renderRepoSeriesDetail(w http.ResponseWriter, r *http.Request, ns string, ref RepoRef, branch string) {
	ctx := r.Context()
	cohortFilter := strings.TrimSpace(r.URL.Query().Get("cohort"))

	n := 50
	if nStr := r.URL.Query().Get("n"); nStr != "" {
		if parsedN, err := strconv.Atoi(nStr); err == nil && parsedN > 0 {
			n = parsedN
		}
	}
	if n > 50 {
		n = 50
	}

	taskFilter := strings.TrimSpace(r.URL.Query().Get("task"))

	var runList v1alpha1.RunList
	if err := s.cfg.Client.List(ctx, &runList, client.InNamespace(ns)); err != nil {
		httpError(w, http.StatusInternalServerError, "failed to list runs", "k8s list runs failed", err)
		return
	}

	type runWithTime struct {
		run        *v1alpha1.Run
		commitTime time.Time
		isFallback bool
	}

	var matched []runWithTime

	for i := range runList.Items {
		run := &runList.Items[i]
		if !strings.HasPrefix(run.Name, "base-") {
			continue
		}
		runBranch := run.Labels["evalrunner.gke-labs.dev/branch"]
		if runBranch == "" {
			continue
		}
		if runBranch != branch && runBranch != sanitizeBranchLabel(branch) {
			continue
		}
		if cohortFilter != "" && run.Spec.Cohort != cohortFilter {
			continue
		}

		runRepo, ok := runRepoRef(run)
		if !ok || !runRepo.Equal(ref) {
			continue
		}

		cTime, fallback := runCommitTime(run)
		matched = append(matched, runWithTime{
			run:        run,
			commitTime: cTime,
			isFallback: fallback,
		})
	}

	if len(matched) == 0 {
		httpError(w, http.StatusNotFound, "series not found", fmt.Sprintf("no runs for repo %s branch %s in namespace %s", ref.String(), branch, ns), nil)
		return
	}

	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].commitTime.Equal(matched[j].commitTime) {
			return matched[i].commitTime.Before(matched[j].commitTime)
		}
		return matched[i].run.Name < matched[j].run.Name
	})

	totalRuns := len(matched)

	if len(matched) > n {
		matched = matched[len(matched)-n:]
	}

	orderedByCreationTime := false
	for _, m := range matched {
		if m.isFallback {
			orderedByCreationTime = true
			break
		}
	}
	var orderedNote string
	if orderedByCreationTime {
		orderedNote = "ordered by creation time"
	}

	taskSet := make(map[string]bool)
	for _, m := range matched {
		for _, t := range m.run.Status.Tasks {
			if t.ID != "" {
				taskSet[t.ID] = true
			}
		}
		for _, tid := range m.run.Spec.Tasks {
			if tid != "" {
				taskSet[tid] = true
			}
		}
	}

	var allTaskIDs []string
	for tid := range taskSet {
		allTaskIDs = append(allTaskIDs, tid)
	}
	sort.Strings(allTaskIDs)

	selectedTask := taskFilter
	if selectedTask == "" && len(allTaskIDs) > 0 {
		selectedTask = allTaskIDs[0]
	}

	var taskLinks []SeriesTaskLink
	for _, tid := range allTaskIDs {
		tParams := url.Values{}
		tParams.Set("task", tid)
		if n != 50 {
			tParams.Set("n", strconv.Itoa(n))
		}
		if cohortFilter != "" {
			tParams.Set("cohort", cohortFilter)
		}
		link := repoLink(ref, "/series/"+branch, ns)
		if len(tParams) > 0 {
			sep := "?"
			if strings.Contains(link, "?") {
				sep = "&"
			}
			link += sep + tParams.Encode()
		}
		taskLinks = append(taskLinks, SeriesTaskLink{
			ID:     tid,
			Link:   link,
			Active: tid == selectedTask,
		})
	}

	var items []SeriesItem
	for _, m := range matched {
		run := m.run
		commit := run.Spec.Repo.Commit
		if commit == "" {
			commit = run.Labels["evalrunner.gke-labs.dev/head-sha"]
		}

		commitTimeStr := "—"
		if !m.commitTime.IsZero() {
			commitTimeStr = m.commitTime.UTC().Format(time.RFC3339)
		}

		var matchedTask *v1alpha1.TaskStatus
		for idx := range run.Status.Tasks {
			if run.Status.Tasks[idx].ID == selectedTask {
				matchedTask = &run.Status.Tasks[idx]
				break
			}
		}

		item := SeriesItem{
			CommitSHA:      commit,
			CommitSHAShort: ShortCommit(commit),
			CommitTime:     m.commitTime,
			CommitTimeStr:  commitTimeStr,
			RunName:        run.Name,
			Namespace:      run.Namespace,
			Samples:        "—",
			RateStr:        "—",
			CIStr:          "—",
			TokensStr:      "—",
			CostStr:        "—",
			RunLink:        repoLink(ref, "/runs/"+run.Name, ns),
		}

		if matchedTask != nil && matchedTask.Phase == v1alpha1.TaskPhaseCompleted && matchedTask.SuccessRate != nil {
			item.HasPoint = true
			item.SuccessRate = matchedTask.SuccessRate
			item.CI = matchedTask.SuccessRateCI
			item.Phase = matchedTask.Phase
			item.PhaseChip = "good"
			item.Samples = strconv.Itoa(matchedTask.Samples)
			item.RateStr = fmt.Sprintf("%.2f", *matchedTask.SuccessRate)
			if matchedTask.SuccessRateCI != nil {
				item.CIStr = fmt.Sprintf("[%.2f, %.2f]", matchedTask.SuccessRateCI.Lower, matchedTask.SuccessRateCI.Upper)
			}
			if matchedTask.Tokens != nil {
				item.TokensStr = FormatTokens(matchedTask.Tokens.Total)
			}
			if matchedTask.CostUSD != nil {
				item.CostStr = fmt.Sprintf("$%.2f", *matchedTask.CostUSD)
			}
		} else {
			item.HasPoint = false
			if matchedTask != nil && matchedTask.Phase != "" {
				item.Phase = matchedTask.Phase
				item.PhaseChip = taskPhaseChipClass(matchedTask.Phase)
				if matchedTask.Samples > 0 {
					item.Samples = strconv.Itoa(matchedTask.Samples)
				}
				if matchedTask.Tokens != nil {
					item.TokensStr = FormatTokens(matchedTask.Tokens.Total)
				}
				if matchedTask.CostUSD != nil {
					item.CostStr = fmt.Sprintf("$%.2f", *matchedTask.CostUSD)
				}
			} else {
				item.Phase = v1alpha1.TaskPhase(run.Status.Phase)
				item.PhaseChip = phaseChipClass(run.Status.Phase)
			}
		}

		items = append(items, item)
	}

	svg := BuildSeriesSVG(items)

	backLink := repoLink(ref, "/series", ns)
	if cohortFilter != "" {
		sep := "?"
		if strings.Contains(backLink, "?") {
			sep = "&"
		}
		backLink += sep + "cohort=" + url.QueryEscape(cohortFilter)
	}

	view := SeriesDetailView{
		ActiveTab:             NavTabSeries,
		Repo:                  ref,
		Namespace:             ns,
		Repository:            ref.Owner + "/" + ref.Name,
		RepoHash:              RepoHash(ref.Owner + "/" + ref.Name),
		Branch:                branch,
		SelectedTask:          selectedTask,
		Tasks:                 taskLinks,
		N:                     n,
		Cohort:                cohortFilter,
		OrderedByCreationTime: orderedByCreationTime,
		OrderedNote:           orderedNote,
		SVG:                   svg,
		Items:                 items,
		TotalRuns:             totalRuns,
		BackLink:              backLink,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderSeriesDetail(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func (s *Server) renderSeriesDetailLegacy(w http.ResponseWriter, r *http.Request, ns string, repoOrHash string, branch string) {
	ctx := r.Context()
	cohortFilter := strings.TrimSpace(r.URL.Query().Get("cohort"))

	n := 50
	if nStr := r.URL.Query().Get("n"); nStr != "" {
		if parsedN, err := strconv.Atoi(nStr); err == nil && parsedN > 0 {
			n = parsedN
		}
	}
	if n > 50 {
		n = 50
	}

	taskFilter := strings.TrimSpace(r.URL.Query().Get("task"))

	var runList v1alpha1.RunList
	if err := s.cfg.Client.List(ctx, &runList, client.InNamespace(ns)); err != nil {
		httpError(w, http.StatusInternalServerError, "failed to list runs", "k8s list runs failed", err)
		return
	}

	type runWithTime struct {
		run        *v1alpha1.Run
		commitTime time.Time
		isFallback bool
	}

	var matched []runWithTime
	var repoDisplay string
	matchedRepoHash := repoOrHash

	for i := range runList.Items {
		run := &runList.Items[i]
		if !strings.HasPrefix(run.Name, "base-") {
			continue
		}
		runBranch := run.Labels["evalrunner.gke-labs.dev/branch"]
		if runBranch == "" {
			continue
		}
		if runBranch != branch && runBranch != sanitizeBranchLabel(branch) {
			continue
		}
		if cohortFilter != "" && run.Spec.Cohort != cohortFilter {
			continue
		}

		repo := runRepository(run)
		h := RepoHash(repo)
		if !strings.EqualFold(h, repoOrHash) && !strings.EqualFold(repo, repoOrHash) {
			continue
		}
		if repoDisplay == "" {
			repoDisplay = repo
			matchedRepoHash = h
		}

		cTime, fallback := runCommitTime(run)
		matched = append(matched, runWithTime{
			run:        run,
			commitTime: cTime,
			isFallback: fallback,
		})
	}

	if len(matched) == 0 {
		httpError(w, http.StatusNotFound, "series not found", fmt.Sprintf("no runs for repo %s branch %s in namespace %s", repoOrHash, branch, ns), nil)
		return
	}

	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].commitTime.Equal(matched[j].commitTime) {
			return matched[i].commitTime.Before(matched[j].commitTime)
		}
		return matched[i].run.Name < matched[j].run.Name
	})

	totalRuns := len(matched)

	if len(matched) > n {
		matched = matched[len(matched)-n:]
	}

	orderedByCreationTime := false
	for _, m := range matched {
		if m.isFallback {
			orderedByCreationTime = true
			break
		}
	}
	var orderedNote string
	if orderedByCreationTime {
		orderedNote = "ordered by creation time"
	}

	taskSet := make(map[string]bool)
	for _, m := range matched {
		for _, t := range m.run.Status.Tasks {
			if t.ID != "" {
				taskSet[t.ID] = true
			}
		}
		for _, tid := range m.run.Spec.Tasks {
			if tid != "" {
				taskSet[tid] = true
			}
		}
	}

	var allTaskIDs []string
	for tid := range taskSet {
		allTaskIDs = append(allTaskIDs, tid)
	}
	sort.Strings(allTaskIDs)

	selectedTask := taskFilter
	if selectedTask == "" && len(allTaskIDs) > 0 {
		selectedTask = allTaskIDs[0]
	}

	var taskLinks []SeriesTaskLink
	for _, tid := range allTaskIDs {
		tParams := url.Values{}
		tParams.Set("task", tid)
		if n != 50 {
			tParams.Set("n", strconv.Itoa(n))
		}
		if ns != s.cfg.DefaultNamespace {
			tParams.Set("namespace", ns)
		}
		if cohortFilter != "" {
			tParams.Set("cohort", cohortFilter)
		}
		link := fmt.Sprintf("/series/%s/%s?%s", url.PathEscape(matchedRepoHash), url.PathEscape(branch), tParams.Encode())
		taskLinks = append(taskLinks, SeriesTaskLink{
			ID:     tid,
			Link:   link,
			Active: tid == selectedTask,
		})
	}

	var items []SeriesItem
	for _, m := range matched {
		run := m.run
		commit := run.Spec.Repo.Commit
		if commit == "" {
			commit = run.Labels["evalrunner.gke-labs.dev/head-sha"]
		}

		commitTimeStr := "—"
		if !m.commitTime.IsZero() {
			commitTimeStr = m.commitTime.UTC().Format(time.RFC3339)
		}

		var matchedTask *v1alpha1.TaskStatus
		for idx := range run.Status.Tasks {
			if run.Status.Tasks[idx].ID == selectedTask {
				matchedTask = &run.Status.Tasks[idx]
				break
			}
		}

		item := SeriesItem{
			CommitSHA:      commit,
			CommitSHAShort: ShortCommit(commit),
			CommitTime:     m.commitTime,
			CommitTimeStr:  commitTimeStr,
			RunName:        run.Name,
			Namespace:      run.Namespace,
			Samples:        "—",
			RateStr:        "—",
			CIStr:          "—",
			TokensStr:      "—",
			CostStr:        "—",
			RunLink:        fmt.Sprintf("/runs/%s/%s", run.Namespace, run.Name),
		}

		if matchedTask != nil && matchedTask.Phase == v1alpha1.TaskPhaseCompleted && matchedTask.SuccessRate != nil {
			item.HasPoint = true
			item.SuccessRate = matchedTask.SuccessRate
			item.CI = matchedTask.SuccessRateCI
			item.Phase = matchedTask.Phase
			item.PhaseChip = "good"
			item.Samples = strconv.Itoa(matchedTask.Samples)
			item.RateStr = fmt.Sprintf("%.2f", *matchedTask.SuccessRate)
			if matchedTask.SuccessRateCI != nil {
				item.CIStr = fmt.Sprintf("[%.2f, %.2f]", matchedTask.SuccessRateCI.Lower, matchedTask.SuccessRateCI.Upper)
			}
			if matchedTask.Tokens != nil {
				item.TokensStr = FormatTokens(matchedTask.Tokens.Total)
			}
			if matchedTask.CostUSD != nil {
				item.CostStr = fmt.Sprintf("$%.2f", *matchedTask.CostUSD)
			}
		} else {
			item.HasPoint = false
			if matchedTask != nil && matchedTask.Phase != "" {
				item.Phase = matchedTask.Phase
				item.PhaseChip = taskPhaseChipClass(matchedTask.Phase)
				if matchedTask.Samples > 0 {
					item.Samples = strconv.Itoa(matchedTask.Samples)
				}
				if matchedTask.Tokens != nil {
					item.TokensStr = FormatTokens(matchedTask.Tokens.Total)
				}
				if matchedTask.CostUSD != nil {
					item.CostStr = fmt.Sprintf("$%.2f", *matchedTask.CostUSD)
				}
			} else {
				item.Phase = v1alpha1.TaskPhase(run.Status.Phase)
				item.PhaseChip = phaseChipClass(run.Status.Phase)
			}
		}

		items = append(items, item)
	}

	svg := BuildSeriesSVG(items)

	backLink := "/series"
	bParams := url.Values{}
	if ns != s.cfg.DefaultNamespace {
		bParams.Set("namespace", ns)
	}
	if cohortFilter != "" {
		bParams.Set("cohort", cohortFilter)
	}
	if len(bParams) > 0 {
		backLink += "?" + bParams.Encode()
	}

	view := SeriesDetailView{
		ActiveTab:             NavTabSeries,
		Namespace:             ns,
		Repository:            repoDisplay,
		RepoHash:              matchedRepoHash,
		Branch:                branch,
		SelectedTask:          selectedTask,
		Tasks:                 taskLinks,
		N:                     n,
		Cohort:                cohortFilter,
		OrderedByCreationTime: orderedByCreationTime,
		OrderedNote:           orderedNote,
		SVG:                   svg,
		Items:                 items,
		TotalRuns:             totalRuns,
		BackLink:              backLink,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderSeriesDetail(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

func (s *Server) loadInspectSamples(ctx context.Context, logPath string) ([]artifacts.ParsedInspectSample, bool, error) {
	if s.cache != nil {
		if samples, parseErr, ok := s.cache.get(logPath); ok {
			if parseErr != nil {
				return nil, true, parseErr
			}
			return samples, false, nil
		}
	}

	data, err := s.cfg.ArtifactSink.Load(ctx, logPath)
	if err != nil {
		if strings.Contains(err.Error(), "exceeds maximum") {
			return nil, true, err
		}
		return nil, false, err
	}

	samples, parseErr := artifacts.ParseInspectSamples(data)
	if parseErr != nil {
		if s.cache != nil {
			s.cache.set(logPath, int64(len(data)), nil, parseErr)
		}
		return nil, true, parseErr
	}

	if s.cache != nil {
		s.cache.set(logPath, int64(len(data)), samples, nil)
	}
	return samples, false, nil
}

func hasComparisonPrefix(path string) bool {
	clean := strings.TrimSpace(path)
	if strings.HasPrefix(clean, "gs://") {
		trimmed := strings.TrimPrefix(clean, "gs://")
		parts := strings.SplitN(trimmed, "/", 2)
		return len(parts) == 2 && strings.HasPrefix(parts[1], "comparisons/")
	}
	if strings.HasPrefix(clean, "mem://") {
		trimmed := strings.TrimPrefix(clean, "mem://")
		return strings.HasPrefix(strings.TrimPrefix(trimmed, "/"), "comparisons/")
	}
	clean = filepath.ToSlash(filepath.Clean(clean))
	return strings.HasPrefix(clean, "comparisons/") || strings.Contains(clean, "/comparisons/")
}

func (s *Server) loadComparisonArtifact(ctx context.Context, summaryPath string, isCompleted bool) (*artifacts.ComparisonArtifact, bool, error) {
	if isCompleted && s.compCache != nil {
		if art, loadErr, ok := s.compCache.get(summaryPath); ok {
			if loadErr != nil {
				return nil, true, loadErr
			}
			return art, false, nil
		}
	}

	data, err := s.cfg.ArtifactSink.Load(ctx, summaryPath)
	if err != nil {
		if strings.Contains(err.Error(), "exceeds maximum") {
			if isCompleted && s.compCache != nil {
				s.compCache.set(summaryPath, 0, nil, err)
			}
			return nil, true, err
		}
		return nil, false, err
	}

	var art artifacts.ComparisonArtifact
	if parseErr := json.Unmarshal(data, &art); parseErr != nil {
		if isCompleted && s.compCache != nil {
			s.compCache.set(summaryPath, int64(len(data)), nil, parseErr)
		}
		return nil, true, parseErr
	}

	if isCompleted && s.compCache != nil {
		s.compCache.set(summaryPath, int64(len(data)), &art, nil)
	}
	return &art, false, nil
}

func computeBatchBoundaries(batches []v1alpha1.ComparisonBatchStatus, totalPairs int) []int {
	if totalPairs < 2 {
		return nil
	}

	var rawBoundaries []int
	hasBatchWidths := false

	cumulative := 0
	for _, b := range batches {
		if b.SampleRange != nil && b.SampleRange.End >= b.SampleRange.Start {
			hasBatchWidths = true
			width := b.SampleRange.End - b.SampleRange.Start + 1
			cumulative += width
			if cumulative <= totalPairs {
				rawBoundaries = append(rawBoundaries, cumulative)
			} else {
				rawBoundaries = append(rawBoundaries, totalPairs)
				break
			}
		}
	}

	if hasBatchWidths {
		if len(rawBoundaries) > 0 && rawBoundaries[len(rawBoundaries)-1] < totalPairs {
			rawBoundaries = append(rawBoundaries, totalPairs)
		}
	} else {
		// Fall back to every 4 pairs when batches are absent
		for n := 4; n < totalPairs; n += 4 {
			rawBoundaries = append(rawBoundaries, n)
		}
		rawBoundaries = append(rawBoundaries, totalPairs)
	}

	// Filter for n >= 2 and strictly monotone
	var boundaries []int
	last := 0
	for _, n := range rawBoundaries {
		if n >= 2 && n <= totalPairs && n > last {
			boundaries = append(boundaries, n)
			last = n
		}
	}

	if len(boundaries) > 50 {
		boundaries = subsampleBoundaries(boundaries, 50)
	}

	return boundaries
}

func subsampleBoundaries(in []int, maxPoints int) []int {
	if len(in) <= maxPoints || maxPoints < 2 {
		return in
	}
	out := make([]int, maxPoints)
	out[0] = in[0]
	out[maxPoints-1] = in[len(in)-1]
	step := float64(len(in)-1) / float64(maxPoints-1)
	for i := 1; i < maxPoints-1; i++ {
		idx := int(math.Round(float64(i) * step))
		if idx <= 0 {
			idx = 1
		}
		if idx >= len(in)-1 {
			idx = len(in) - 2
		}
		out[i] = in[idx]
	}
	for i := 1; i < maxPoints; i++ {
		if out[i] <= out[i-1] {
			return in[:maxPoints]
		}
	}
	return out
}

func (s *Server) populateComparisonHistories(ctx context.Context, comp *v1alpha1.Comparison, view *ComparisonDetailView) {
	if s.cfg.ArtifactSink == nil {
		view.HistoryNote = "artifact storage unavailable"
		return
	}
	if comp.Status.SummaryPath == "" {
		return
	}
	if !hasComparisonPrefix(comp.Status.SummaryPath) {
		view.HistoryNote = "history not available"
		return
	}

	isCompleted := comp.Status.Phase == v1alpha1.ComparisonPhaseCompleted
	art, parseOrSizeErr, err := s.loadComparisonArtifact(ctx, comp.Status.SummaryPath, isCompleted)
	if err != nil {
		if errors.Is(err, artifacts.ErrNotFound) || parseOrSizeErr {
			view.HistoryNote = "history not available"
		} else {
			view.HistoryNote = "artifact storage unavailable"
		}
		return
	}
	if art == nil {
		view.HistoryNote = "history not available"
		return
	}

	margin := stats.DefaultEquivalenceMargin
	if comp.Spec.EquivalenceMargin != nil && *comp.Spec.EquivalenceMargin > 0 {
		margin = *comp.Spec.EquivalenceMargin
	}

	sampleCap := 0
	if comp.Spec.Sampling != nil {
		sampleCap = comp.Spec.Sampling.EffectiveMaxSamplesPerTask()
	}

	taskIDs := make([]string, 0, len(comp.Status.Tasks))
	for _, t := range comp.Status.Tasks {
		taskIDs = append(taskIDs, t.ID)
	}
	if len(taskIDs) == 0 {
		for _, t := range art.Tasks {
			taskIDs = append(taskIDs, t.ID)
		}
	}

	var histories []ComparisonTaskHistory
	for _, tid := range taskIDs {
		var matchedArt *artifacts.ComparisonTaskArtifact
		for i := range art.Tasks {
			if art.Tasks[i].ID == tid {
				matchedArt = &art.Tasks[i]
				break
			}
		}
		if matchedArt == nil || len(matchedArt.PairsTable) < 2 {
			continue
		}

		boundaries := computeBatchBoundaries(comp.Status.Batches, len(matchedArt.PairsTable))
		if len(boundaries) == 0 {
			continue
		}

		points := stats.ComputePairedCISeries(matchedArt.PairsTable, boundaries, 0.95, stats.DefaultSeed, margin, sampleCap)
		if len(points) == 0 {
			continue
		}

		historyPoints := make([]ComparisonHistoryPoint, len(points))
		for i, pt := range points {
			historyPoints[i] = ComparisonHistoryPoint{
				N:              pt.N,
				DeltaFormatted: fmt.Sprintf("%+.2f", pt.Estimate.MeanDelta),
				CIFormatted:    fmt.Sprintf("[%+.2f, %+.2f]", pt.Estimate.CI.Lower, pt.Estimate.CI.Upper),
				CIWidth:        pt.CIWidth,
				Decision:       string(pt.Decision.Label),
				DecisionChip:   DecisionChipClass(string(pt.Decision.Label)),
			}
		}

		svg := BuildComparisonHistorySVG(historyPoints, margin)
		histories = append(histories, ComparisonTaskHistory{
			TaskID: matchedArt.ID,
			Points: historyPoints,
			SVG:    svg,
		})
	}

	if len(histories) > 0 {
		view.Histories = histories
	}
}

// FormatArtifactFooter formats the artifact sink status for the page footer.
// Returns "artifacts: gs://<bucket>", "artifacts: dir <path>", or "artifacts: none".
func FormatArtifactFooter(sink artifacts.ArtifactSink) string {
	if sink == nil {
		return "artifacts: none"
	}
	type bucketGetter interface {
		Bucket() string
	}
	if bg, ok := sink.(bucketGetter); ok {
		return fmt.Sprintf("artifacts: gs://%s", bg.Bucket())
	}
	if ld, ok := sink.(*artifacts.LocalDirSink); ok {
		return fmt.Sprintf("artifacts: dir %s", ld.BaseDir)
	}
	if _, ok := sink.(*artifacts.InMemorySink); ok {
		return "artifacts: memory"
	}
	prefix := sink.ArtifactPrefix("dummy")
	if strings.HasPrefix(prefix, "gs://") {
		trimmed := strings.TrimPrefix(prefix, "gs://")
		parts := strings.Split(trimmed, "/")
		if len(parts) > 0 && parts[0] != "" {
			return fmt.Sprintf("artifacts: gs://%s", parts[0])
		}
	}
	return "artifacts: none"
}

// parseRunTaskRest parses the {rest...} path segment and query parameters into taskID and sampleID.
// It splits rest at the last "/samples" segment into (task, sample?).
// Query parameters ?task=&sample= act as an explicit escape hatch.
func parseRunTaskRest(rest string, q url.Values, fragment, rawFragment string) (taskID string, sampleID string, ok bool) {
	qTask := q.Get("task")
	qSample := q.Get("sample")

	var parsedTask, parsedSample string
	var foundSamples bool

	search := "/samples"
	for i := len(rest) - len(search); i >= 0; i-- {
		if rest[i:i+len(search)] == search {
			after := i + len(search)
			if after == len(rest) || rest[after] == '/' {
				parsedTask = rest[:i]
				parsedSample = strings.TrimPrefix(rest[after:], "/")
				foundSamples = true
				break
			}
		}
	}

	if !foundSamples {
		if rest == "samples" || strings.HasPrefix(rest, "samples/") {
			foundSamples = true
			parsedTask = ""
			if len(rest) > len("samples") {
				parsedSample = strings.TrimPrefix(rest[len("samples"):], "/")
			}
		}
	}

	if qTask != "" {
		taskID = qTask
	} else if foundSamples {
		parsedTask = strings.TrimPrefix(parsedTask, "/")
		if u, err := url.PathUnescape(parsedTask); err == nil {
			taskID = u
		} else {
			taskID = parsedTask
		}
	}

	if qSample != "" {
		sampleID = qSample
	} else if foundSamples && parsedSample != "" {
		parsedSample = strings.TrimSuffix(parsedSample, "/")
		if u, err := url.PathUnescape(parsedSample); err == nil {
			sampleID = u
		} else {
			sampleID = parsedSample
		}
		if fragment != "" && !strings.Contains(sampleID, "#") {
			sampleID = sampleID + "#" + fragment
		} else if rawFragment != "" && !strings.Contains(sampleID, "#") {
			sampleID = sampleID + "#" + rawFragment
		}
	}

	if taskID == "" {
		return "", "", false
	}
	return taskID, sampleID, true
}

func (s *Server) handleRunTasks(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")
	rest := r.PathValue("rest")

	if !isValidNamespace(ns) || !isValidResourceName(name) {
		httpError(w, http.StatusBadRequest, "invalid route params", "", nil)
		return
	}

	taskID, sampleID, ok := parseRunTaskRest(rest, r.URL.Query(), r.URL.Fragment, r.URL.RawFragment)
	if !ok || taskID == "" {
		httpError(w, http.StatusNotFound, "not found", "", nil)
		return
	}

	ctx := r.Context()
	var run v1alpha1.Run
	if err := s.cfg.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			httpError(w, http.StatusNotFound, "run not found", "", nil)
			return
		}
		httpError(w, http.StatusInternalServerError, "error loading run", fmt.Sprintf("error loading run %s/%s", ns, name), err)
		return
	}

	if ref, ok := runRepoRef(&run); ok {
		var suffix string
		if sampleID != "" {
			suffix = fmt.Sprintf("/runs/%s/tasks/%s/samples/%s", name, url.PathEscape(taskID), url.PathEscape(sampleID))
		} else {
			suffix = fmt.Sprintf("/runs/%s/tasks/%s/samples", name, url.PathEscape(taskID))
		}
		target := repoLink(ref, suffix, ns)
		q := r.URL.Query()
		q.Del("namespace")
		q.Del("task")
		q.Del("sample")
		if len(q) > 0 {
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target += sep + q.Encode()
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	}

	s.renderRunTasks(w, r, &run, ns, RepoRef{}, taskID, sampleID)
}

func (s *Server) handleRepoRunTasks(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.parseRouteRepo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "not found", "invalid repo route params", nil)
		return
	}
	name := r.PathValue("name")
	rest := r.PathValue("rest")

	if !isValidResourceName(name) {
		httpError(w, http.StatusBadRequest, "invalid route params", "", nil)
		return
	}

	taskID, sampleID, ok := parseRunTaskRest(rest, r.URL.Query(), r.URL.Fragment, r.URL.RawFragment)
	if !ok || taskID == "" {
		httpError(w, http.StatusNotFound, "not found", "", nil)
		return
	}

	ns, err := s.namespaceFor(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid namespace", "invalid namespace query param", err)
		return
	}

	ctx := r.Context()
	var run v1alpha1.Run
	if err := s.cfg.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			httpError(w, http.StatusNotFound, "run not found", "", nil)
			return
		}
		httpError(w, http.StatusInternalServerError, "error loading run", fmt.Sprintf("error loading run %s/%s", ns, name), err)
		return
	}

	runRepo, ok := runRepoRef(&run)
	if !ok || !runRepo.Equal(ref) {
		httpError(w, http.StatusNotFound, "run not found", fmt.Sprintf("run repo %v does not match route repo %v", runRepo, ref), nil)
		return
	}

	s.renderRunTasks(w, r, &run, ns, ref, taskID, sampleID)
}

func (s *Server) renderRunTasks(w http.ResponseWriter, r *http.Request, run *v1alpha1.Run, ns string, ref RepoRef, taskID, sampleID string) {
	name := run.Name
	var matchedTask *v1alpha1.TaskStatus
	for i := range run.Status.Tasks {
		if run.Status.Tasks[i].ID == taskID {
			matchedTask = &run.Status.Tasks[i]
			break
		}
	}

	if matchedTask == nil || matchedTask.LogPath == "" {
		httpError(w, http.StatusNotFound, "log path not found for task", "", nil)
		return
	}

	commit := run.Spec.Repo.Commit
	if s.cfg.ArtifactSink != nil {
		expectedPrefix := s.cfg.ArtifactSink.ArtifactPrefix(commit)
		if expectedPrefix != "" && !strings.HasPrefix(matchedTask.LogPath, expectedPrefix) {
			httpError(w, http.StatusForbidden, "forbidden: log path does not match run commit prefix", "", nil)
			return
		}
	}

	if s.cfg.ArtifactSink == nil {
		log.Printf("artifact storage not configured for UI (namespace=%s, run=%s, task=%s)", ns, name, taskID)
		if sampleID == "" {
			view := TaskSamplesListView{
				ActiveTab:      NavTabRuns,
				Repo:           ref,
				Namespace:      ns,
				RunName:        name,
				TaskID:         taskID,
				EscapedTaskID:  url.PathEscape(taskID),
				Commit:         ShortCommit(commit),
				Model:          run.Spec.Model,
				LogPath:        matchedTask.LogPath,
				StorageMessage: "artifact storage is not configured for the UI; set artifactBucket or artifactDir in eval-runner-config",
				ArtifactFooter: FormatArtifactFooter(s.cfg.ArtifactSink),
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := tmplTaskSamplesList.Execute(w, view); err != nil {
				httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
			}
			return
		}
		view := SampleTranscriptView{
			ActiveTab:       NavTabRuns,
			Repo:            ref,
			Namespace:       ns,
			RunName:         name,
			TaskID:          taskID,
			EscapedTaskID:   url.PathEscape(taskID),
			SampleID:        sampleID,
			EscapedSampleID: url.PathEscape(sampleID),
			LogPath:         matchedTask.LogPath,
			StorageMessage:  "artifact storage is not configured for the UI; set artifactBucket or artifactDir in eval-runner-config",
			ArtifactFooter:  FormatArtifactFooter(s.cfg.ArtifactSink),
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmplSampleTranscript.Execute(w, view); err != nil {
			httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
		}
		return
	}

	if sampleID == "" {
		s.renderTaskSamplesList(w, r, ns, ref, name, taskID, commit, run.Spec.Model, matchedTask.LogPath)
		return
	}
	s.renderSampleTranscript(w, r, ns, ref, name, taskID, sampleID, matchedTask.LogPath)
}

func (s *Server) renderTaskSamplesList(w http.ResponseWriter, r *http.Request, ns string, ref RepoRef, name, taskID, commit, model, logPath string) {
	ctx := r.Context()
	view := TaskSamplesListView{
		ActiveTab:      NavTabRuns,
		Repo:           ref,
		Namespace:      ns,
		RunName:        name,
		TaskID:         taskID,
		EscapedTaskID:  url.PathEscape(taskID),
		Commit:         ShortCommit(commit),
		Model:          model,
		LogPath:        logPath,
		ArtifactFooter: FormatArtifactFooter(s.cfg.ArtifactSink),
	}

	samples, isTruncated, err := s.loadInspectSamples(ctx, logPath)
	if isTruncated {
		view.IsTruncatedLog = true
		view.TruncatedMessage = "The evaluation log exceeded the size limit or was truncated during storage. Sample details cannot be displayed."
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmplTaskSamplesList.Execute(w, view); err != nil {
			httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
		}
		return
	}
	if err != nil {
		if errors.Is(err, artifacts.ErrNotFound) {
			httpError(w, http.StatusNotFound, "task log not found on storage", "", nil)
			return
		}
		httpError(w, http.StatusBadGateway, "artifact storage unavailable; see UI logs", fmt.Sprintf("storage error loading artifact %s", logPath), err)
		return
	}

	totalSamples := len(samples)
	view.TotalSamples = totalSamples

	page := 1
	if pStr := r.URL.Query().Get("page"); pStr != "" {
		if p, err := strconv.Atoi(pStr); err == nil && p > 1 {
			page = p
		}
	}

	const pageSize = 200
	totalPages := 1
	if totalSamples > 0 {
		totalPages = int(math.Ceil(float64(totalSamples) / float64(pageSize)))
	}
	if page > totalPages {
		page = totalPages
	}

	view.Page = page
	view.PageSize = pageSize
	view.TotalPages = totalPages

	startIdx := (page - 1) * pageSize
	endIdx := startIdx + pageSize
	if endIdx > totalSamples {
		endIdx = totalSamples
	}
	if startIdx > endIdx {
		startIdx = endIdx
	}

	view.ShowingStart = startIdx + 1
	if totalSamples == 0 {
		view.ShowingStart = 0
	}
	view.ShowingEnd = endIdx

	baseLink := BuildTaskSamplesListLink(ns, name, taskID)
	if ref.Host != "" {
		baseLink = repoLink(ref, fmt.Sprintf("/runs/%s/tasks/%s/samples", name, url.PathEscape(taskID)), ns)
	}

	linkSep := "?"
	if strings.Contains(baseLink, "?") {
		linkSep = "&"
	}

	if page > 1 {
		view.HasPrev = true
		view.PrevLink = fmt.Sprintf("%s%spage=%d", baseLink, linkSep, page-1)
	}
	if page < totalPages {
		view.HasNext = true
		view.NextLink = fmt.Sprintf("%s%spage=%d", baseLink, linkSep, page+1)
	}

	var sampleRows []TaskSampleRow
	for _, sRecord := range samples[startIdx:endIdx] {
		statusClass := "neutral"
		if sRecord.Status == "pass" {
			statusClass = "good"
		} else if sRecord.Status == "fail" {
			statusClass = "bad"
		}

		sampleLink := BuildTaskSampleLink(ns, name, taskID, sRecord.ID)
		if ref.Host != "" {
			sampleLink = repoLink(ref, fmt.Sprintf("/runs/%s/tasks/%s/samples/%s", name, url.PathEscape(taskID), url.PathEscape(sRecord.ID)), ns)
		}

		sampleRows = append(sampleRows, TaskSampleRow{
			ID:                 sRecord.ID,
			EscapedID:          url.PathEscape(sRecord.ID),
			Score:              sRecord.ScoreStr,
			Status:             sRecord.Status,
			StatusClass:        statusClass,
			InputPreview:       TruncateRunes(sRecord.Input, 200),
			OutputPreview:      TruncateRunes(sRecord.Output, 200),
			Target:             sRecord.Target,
			TargetPreview:      TruncateRunes(sRecord.Target, sampleNotePreviewRunes),
			Explanation:        sRecord.Explanation,
			ExplanationPreview: TruncateRunes(sRecord.Explanation, sampleNotePreviewRunes),
			LatencyStr:         FormatLatency(sRecord.Latency),
			TokensStr:          FormatTokens(sRecord.TotalTokens),
			Link:               sampleLink,
		})
	}
	view.Samples = sampleRows

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplTaskSamplesList.Execute(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}

// Transcript size guards.
const (
	maxEvents               = 500
	maxMessageBytes         = 16 * 1024
	maxTranscriptFieldBytes = 16 * 1024
)

func (s *Server) renderSampleTranscript(w http.ResponseWriter, r *http.Request, ns string, ref RepoRef, name, taskID, sampleID, logPath string) {
	ctx := r.Context()
	view := SampleTranscriptView{
		ActiveTab:       NavTabRuns,
		Repo:            ref,
		Namespace:       ns,
		RunName:         name,
		TaskID:          taskID,
		EscapedTaskID:   url.PathEscape(taskID),
		SampleID:        sampleID,
		EscapedSampleID: url.PathEscape(sampleID),
		LogPath:         logPath,
		ArtifactFooter:  FormatArtifactFooter(s.cfg.ArtifactSink),
	}

	samples, isTruncated, err := s.loadInspectSamples(ctx, logPath)
	if isTruncated {
		view.IsTruncatedLog = true
		view.TruncatedMessage = "The evaluation log exceeded the size limit or was truncated during storage. Transcript details cannot be displayed."
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmplSampleTranscript.Execute(w, view); err != nil {
			httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
		}
		return
	}
	if err != nil {
		if errors.Is(err, artifacts.ErrNotFound) {
			httpError(w, http.StatusNotFound, "task log not found on storage", "", nil)
			return
		}
		httpError(w, http.StatusBadGateway, "artifact storage unavailable; see UI logs", fmt.Sprintf("storage error loading artifact %s", logPath), err)
		return
	}

	var matchedSample *artifacts.ParsedInspectSample
	for i := range samples {
		if samples[i].ID == sampleID {
			matchedSample = &samples[i]
			break
		}
	}

	if matchedSample == nil {
		httpError(w, http.StatusNotFound, "sample not found", "", nil)
		return
	}

	statusClass := "neutral"
	statusUpper := "UNKNOWN"
	if matchedSample.Status == "pass" {
		statusClass = "good"
		statusUpper = "PASS"
	} else if matchedSample.Status == "fail" {
		statusClass = "bad"
		statusUpper = "FAIL"
	}

	view.Score = matchedSample.ScoreStr
	view.Status = matchedSample.Status
	view.StatusUpper = statusUpper
	view.StatusClass = statusClass
	view.LatencyStr = FormatLatency(matchedSample.Latency)
	view.TokensStr = FormatTokens(matchedSample.TotalTokens)
	view.Target = TruncateFieldBytes(matchedSample.Target, maxTranscriptFieldBytes)
	view.Explanation = TruncateFieldBytes(matchedSample.Explanation, maxTranscriptFieldBytes)

	totalEvents := len(matchedSample.Transcript)
	view.TotalEvents = totalEvents

	rows := matchedSample.Transcript
	if len(rows) > maxEvents {
		rows = rows[:maxEvents]
		view.IsEventsTruncated = true
		view.EventsTruncatedMessage = fmt.Sprintf("[Transcript truncated: showing first %d of %d events]", maxEvents, totalEvents)
	}

	lastAssistantIdx := -1
	for i, r := range rows {
		if r.Kind == "assistant" {
			lastAssistantIdx = i
		}
	}

	var rowViews []TranscriptRowView
	for i, r := range rows {
		class := ""
		switch r.Kind {
		case "tool", "tool_call":
			class = "tool"
		case "assistant":
			if matchedSample.Status == "fail" && i == lastAssistantIdx {
				class = "bad"
			}
		}
		rowViews = append(rowViews, TranscriptRowView{
			Kind:    r.Kind,
			Class:   class,
			Content: TruncateMessageBytes(r.Content, maxMessageBytes),
		})
	}
	view.Rows = rowViews

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplSampleTranscript.Execute(w, view); err != nil {
		httpError(w, http.StatusInternalServerError, "template render error", "template render error", err)
	}
}
