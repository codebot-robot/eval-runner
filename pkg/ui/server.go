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
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/discovery"
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
	cfg Config
	mux *http.ServeMux
}

// NewServer initializes a new Server with route registration.
func NewServer(cfg Config) *Server {
	if cfg.DefaultNamespace == "" {
		cfg.DefaultNamespace = "eval-runner"
	}

	s := &Server{
		cfg: cfg,
		mux: http.NewServeMux(),
	}

	s.routes()
	return s
}

// ServeHTTP delegates to the internal mux.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /{$}", s.handleRoot)
	s.mux.HandleFunc("GET /runs", s.handleRunsList)
	s.mux.HandleFunc("GET /runs/{namespace}/{name}", s.handleRunDetail)
	s.mux.HandleFunc("GET /comparisons", s.handleComparisonsList)
	s.mux.HandleFunc("GET /comparisons/{namespace}/{name}", s.handleComparisonDetail)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("ok\n")); err != nil {
		return
	}
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/runs", http.StatusFound)
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
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	offset, limit, err := parsePagination(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var runList v1alpha1.RunList
	if err := s.cfg.Client.List(ctx, &runList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			http.Error(w, fmt.Sprintf("forbidden: access to namespace %q denied: %v", ns, err), http.StatusForbidden)
			return
		}
		http.Error(w, fmt.Sprintf("failed to list runs in namespace %s: %v", ns, err), http.StatusInternalServerError)
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
		if run.Status.Summary != nil {
			if run.Status.Summary.Tasks != "" {
				tasks = run.Status.Summary.Tasks
			}
			samples = run.Status.Summary.TotalSamples
			rates = run.Status.Summary.Rates
		} else if run.Status.Discovery != nil && run.Status.Discovery.TaskCount > 0 {
			tasks = fmt.Sprintf("0/%d", run.Status.Discovery.TaskCount)
		}

		items = append(items, RunListItem{
			Name:      run.Name,
			Namespace: run.Namespace,
			Phase:     run.Status.Phase,
			PhaseChip: phaseChipClass(run.Status.Phase),
			Tasks:     tasks,
			Samples:   samples,
			Rates:     rates,
			Commit:    ShortCommit(run.Spec.Repo.Commit),
			Age:       FormatAge(run.CreationTimestamp),
			Link:      fmt.Sprintf("/runs/%s/%s", run.Namespace, run.Name),
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
		http.Error(w, fmt.Sprintf("template render error: %v", err), http.StatusInternalServerError)
	}
}

func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns := r.PathValue("namespace")
	name := r.PathValue("name")
	if !isValidNamespace(ns) || !isValidResourceName(name) {
		http.Error(w, fmt.Sprintf("invalid route params: namespace=%q name=%q", ns, name), http.StatusBadRequest)
		return
	}

	var run v1alpha1.Run
	if err := s.cfg.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, fmt.Sprintf("Run %s/%s not found", ns, name), http.StatusNotFound)
			return
		}
		if apierrors.IsForbidden(err) {
			http.Error(w, fmt.Sprintf("forbidden: access to run %s/%s denied: %v", ns, name, err), http.StatusForbidden)
			return
		}
		http.Error(w, fmt.Sprintf("failed to get run %s/%s: %v", ns, name, err), http.StatusInternalServerError)
		return
	}

	// Prepare task rows
	taskRows := make([]RunTaskRow, 0, len(run.Status.Tasks))
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

		taskRows = append(taskRows, RunTaskRow{
			ID:        task.ID,
			Phase:     task.Phase,
			PhaseChip: taskPhaseChipClass(task.Phase),
			RateStr:   rateStr,
			HasCI:     hasCI,
			CILeftPx:  leftPx,
			CIWidthPx: widthPx,
			CIDotPx:   dotPx,
			CILabel:   ciLabel,
			Samples:   task.Samples,
			Successes: task.Successes,
			Method:    method,
			LogPath:   task.LogPath,
			Message:   task.Message,
		})
	}

	if len(run.Status.Tasks) == 0 && run.Status.Discovery != nil {
		taskIDs := run.Status.Discovery.TaskIDs
		if s.cfg.ArtifactSink != nil && run.Status.Discovery.TaskCount > len(run.Status.Discovery.TaskIDs) && run.Status.Discovery.ManifestPath != "" {
			manifestBytes, loadErr := s.cfg.ArtifactSink.Load(r.Context(), run.Status.Discovery.ManifestPath)
			if loadErr != nil {
				// Fall back to capped taskIDs
			} else {
				m, parseErr := discovery.ParseManifest(manifestBytes)
				if parseErr != nil || m == nil {
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

	view := RunDetailView{
		ActiveTab:        NavTabRuns,
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
		Message:          run.Status.Message,
		TasksSummary:     tasksSummary,
		TasksCompleted:   tasksCompleted,
		TasksFailed:      tasksFailed,
		TasksTotal:       tasksTotal,
		TotalSamples:     totalSamples,
		MeanSuccessRate:  meanSuccessRate,
		SuccessRateRange: successRateRange,
		CompactRates:     compactRates,
		Tasks:            taskRows,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplRunDetail.Execute(w, view); err != nil {
		http.Error(w, fmt.Sprintf("template render error: %v", err), http.StatusInternalServerError)
	}
}

func (s *Server) handleComparisonsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns, err := s.namespaceFor(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	offset, limit, err := parsePagination(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var compList v1alpha1.ComparisonList
	if err := s.cfg.Client.List(ctx, &compList, client.InNamespace(ns)); err != nil {
		if apierrors.IsForbidden(err) {
			http.Error(w, fmt.Sprintf("forbidden: access to namespace %q denied: %v", ns, err), http.StatusForbidden)
			return
		}
		http.Error(w, fmt.Sprintf("failed to list comparisons in namespace %s: %v", ns, err), http.StatusInternalServerError)
		return
	}

	// Sort comparisons by CreationTimestamp descending (newest first), with name tiebreaker
	sort.Slice(compList.Items, func(i, j int) bool {
		if !compList.Items[i].CreationTimestamp.Equal(&compList.Items[j].CreationTimestamp) {
			return compList.Items[i].CreationTimestamp.After(compList.Items[j].CreationTimestamp.Time)
		}
		return compList.Items[i].Name < compList.Items[j].Name
	})

	totalCount := len(compList.Items)
	start := offset
	if start > totalCount {
		start = totalCount
	}
	end := start + limit
	if end > totalCount {
		end = totalCount
	}

	pageComps := compList.Items[start:end]
	items := make([]ComparisonListItem, 0, len(pageComps))
	for _, comp := range pageComps {
		pImp := "—"
		if comp.Status.PImprovement != nil {
			pImp = fmt.Sprintf("%.2f", *comp.Status.PImprovement)
		}

		tasks := "—"
		if comp.Status.Summary != nil && comp.Status.Summary.Tasks != "" {
			tasks = comp.Status.Summary.Tasks
		}

		items = append(items, ComparisonListItem{
			Name:         comp.Name,
			Namespace:    comp.Namespace,
			Verdict:      comp.Status.Verdict,
			VerdictChip:  verdictChipClass(comp.Status.Verdict),
			PImprovement: pImp,
			Tasks:        tasks,
			BaselineRun:  comp.Spec.Baseline.Run,
			CandidateRun: comp.Spec.Candidate.Run,
			Age:          FormatAge(comp.CreationTimestamp),
			Link:         fmt.Sprintf("/comparisons/%s/%s", comp.Namespace, comp.Name),
		})
	}

	hasMore := end < totalCount
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
	if totalCount > 0 {
		showingRange = fmt.Sprintf("Showing %d–%d of %d", start+1, end, totalCount)
	}

	view := ComparisonsListView{
		ActiveTab:    NavTabComparisons,
		Namespace:    ns,
		Comparisons:  items,
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
	if err := tmplComparisonsList.Execute(w, view); err != nil {
		http.Error(w, fmt.Sprintf("template render error: %v", err), http.StatusInternalServerError)
	}
}

func (s *Server) handleComparisonDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns := r.PathValue("namespace")
	name := r.PathValue("name")
	if !isValidNamespace(ns) || !isValidResourceName(name) {
		http.Error(w, fmt.Sprintf("invalid route params: namespace=%q name=%q", ns, name), http.StatusBadRequest)
		return
	}

	var comp v1alpha1.Comparison
	if err := s.cfg.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &comp); err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, fmt.Sprintf("Comparison %s/%s not found", ns, name), http.StatusNotFound)
			return
		}
		if apierrors.IsForbidden(err) {
			http.Error(w, fmt.Sprintf("forbidden: access to comparison %s/%s denied: %v", ns, name, err), http.StatusForbidden)
			return
		}
		http.Error(w, fmt.Sprintf("failed to get comparison %s/%s: %v", ns, name, err), http.StatusInternalServerError)
		return
	}

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

	summary := ComparisonSummary(&comp, baseRunPtr, candRunPtr)
	taskRows := ComparisonRows(&comp)

	view := ComparisonDetailView{
		ActiveTab:        NavTabComparisons,
		Name:             comp.Name,
		Namespace:        comp.Namespace,
		Verdict:          comp.Status.Verdict,
		VerdictChip:      verdictChipClass(comp.Status.Verdict),
		Headline:         summary.Headline,
		PImprovement:     summary.PImprovement,
		SummaryPath:      comp.Status.SummaryPath,
		Message:          comp.Status.Message,
		Tasks:            taskRows,
		Summary:          summary,
		BaseRun:          summary.BaseRun,
		BaseCommit:       summary.BaseCommit,
		BaseCommitShort:  summary.BaseCommitShort,
		CandRun:          summary.CandRun,
		CandCommit:       summary.CandCommit,
		CandCommitShort:  summary.CandCommitShort,
		TotalSamples:     summary.TotalSamples,
		TotalPairs:       summary.TotalPairs,
		TotalDroppedCand: summary.TotalDroppedCand,
		TotalDroppedBase: summary.TotalDroppedBase,
		SamplesSummary:   summary.SamplesSummary,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmplComparisonDetail.Execute(w, view); err != nil {
		http.Error(w, fmt.Sprintf("template render error: %v", err), http.StatusInternalServerError)
	}
}
