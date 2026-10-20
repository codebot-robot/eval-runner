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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func setupMultiRepoTestServer(t *testing.T) (*Server, *artifacts.InMemorySink) {
	scheme := newTestScheme(t)
	now := time.Now()
	rate := 0.8
	sink := artifacts.NewInMemorySink()

	// Store inspect.json for task samples testing
	commit := "aaaa111122223333444455556666777788889999"
	logPath, err := sink.StoreArtifact(t.Context(), artifacts.RunMetadata{
		TaskID:     "task-1",
		Commit:     commit,
		Model:      "test-model",
		Samples:    1,
		StartedAt:  &now,
		FinishedAt: &now,
	}, "inspect.json", []byte(`{
		"version": 2,
		"samples": [
			{
				"id": "s-101",
				"epoch": 1,
				"input": "test input",
				"target": "expected target",
				"scores": {"exact_match": {"value": 1.0, "answer": "PASS"}},
				"transcript": [
					{"role": "user", "content": "hello"}
				]
			}
		]
	}`))
	if err != nil {
		t.Fatalf("failed to store test artifact: %v", err)
	}

	logPathDecoded, err := sink.StoreArtifact(t.Context(), artifacts.RunMetadata{
		TaskID:     "docs/readme_comprehension",
		Commit:     commit,
		Model:      "test-model",
		Samples:    1,
		StartedAt:  &now,
		FinishedAt: &now,
	}, "inspect.json", []byte(`{
		"version": 2,
		"samples": [
			{
				"id": "s-202",
				"epoch": 1,
				"input": "test prompt",
				"target": "expected output",
				"scores": {"exact_match": {"value": 1.0, "answer": "PASS"}},
				"messages": [
					{"role": "user", "content": "test prompt"},
					{"role": "assistant", "content": "reading docs"}
				]
			}
		]
	}`))
	if err != nil {
		t.Fatalf("failed to store test artifact: %v", err)
	}

	// Repo A objects
	runA := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "run-repo-a",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-20 * time.Minute)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/org-a/repo-a.git",
				Commit: commit,
			},
			Model: "test-model",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:          "task-1",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     1,
					SuccessRate: &rate,
					LogPath:     logPath,
				},
				{
					ID:          "docs/readme_comprehension",
					Phase:       v1alpha1.TaskPhaseCompleted,
					Samples:     1,
					SuccessRate: &rate,
					LogPath:     logPathDecoded,
				},
			},
		},
	}

	runABase := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-repo-a",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-30 * time.Minute)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "main",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/org-a/repo-a.git",
				Commit: "0000000000000000000000000000000000000000",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "task-1", Phase: v1alpha1.TaskPhaseCompleted, SuccessRate: &rate},
			},
		},
	}

	compA := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "comp-repo-a",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)),
		},
		Spec: v1alpha1.ComparisonSpec{
			Baseline:  v1alpha1.ComparisonRunRef{Run: "base-repo-a"},
			Candidate: v1alpha1.ComparisonRunRef{Run: "run-repo-a"},
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "org-a/repo-a",
				Number:     101,
				HeadSHA:    commit,
				BaseSHA:    "0000000000000000000000000000000000000000",
			},
		},
		Status: v1alpha1.ComparisonStatus{
			Phase:   v1alpha1.ComparisonPhaseCompleted,
			Verdict: v1alpha1.ComparisonVerdictImproved,
		},
	}

	// Repo B objects
	runB := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "run-repo-b",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-15 * time.Minute)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "release",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git@gitlab.com:org-b/repo-b.git",
				Commit: "bbbb111122223333444455556666777788889999",
			},
			Model: "test-model",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "task-b", Phase: v1alpha1.TaskPhaseCompleted, SuccessRate: &rate},
			},
		},
	}

	runBBase := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base-repo-b",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-25 * time.Minute)),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": "release",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git@gitlab.com:org-b/repo-b.git",
				Commit: "bbbb000000000000000000000000000000000000",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: []v1alpha1.TaskStatus{
				{ID: "task-b", Phase: v1alpha1.TaskPhaseCompleted, SuccessRate: &rate},
			},
		},
	}

	compB := &v1alpha1.Comparison{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "comp-repo-b",
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Minute)),
		},
		Spec: v1alpha1.ComparisonSpec{
			Baseline: v1alpha1.ComparisonRunRef{Run: "base-repo-b"},
			PullRequest: &v1alpha1.PullRequestSpec{
				Repository: "https://gitlab.com/org-b/repo-b",
				Number:     202,
				HeadSHA:    "bbbb111122223333444455556666777788889999",
				BaseSHA:    "bbbb000000000000000000000000000000000000",
			},
		},
		Status: v1alpha1.ComparisonStatus{
			Phase:   v1alpha1.ComparisonPhaseCompleted,
			Verdict: v1alpha1.ComparisonVerdictInconclusive,
		},
	}

	// An unresolvable run (no URL, no label)
	runUnresolvable := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-unresolvable",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Model: "test-model",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
		},
	}

	// An object in custom-ns
	runCustomNS := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-custom-ns",
			Namespace: "custom-ns",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL: "https://github.com/org-a/repo-a.git",
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(runA, runABase, compA, runB, runBBase, compB, runUnresolvable, runCustomNS).
		Build()

	server := NewServer(Config{
		Client:           cl,
		DefaultNamespace: "eval-runner",
		ArtifactSink:     sink,
	})

	return server, sink
}

func TestRepoRoutes_AllNewRoutes(t *testing.T) {
	server, _ := setupMultiRepoTestServer(t)

	tests := []struct {
		name         string
		path         string
		wantCode     int
		wantContains []string
	}{
		{
			name:         "RepoIndex",
			path:         "/",
			wantCode:     http.StatusOK,
			wantContains: []string{"Repositories", "github.com/org-a/repo-a", "gitlab.com/org-b/repo-b"},
		},
		{
			name:         "OverviewWithTrailingSlash",
			path:         "/github.com/org-a/repo-a/",
			wantCode:     http.StatusOK,
			wantContains: []string{"Overview", "run-repo-a"},
		},
		{
			name:         "OverviewWithoutTrailingSlash",
			path:         "/github.com/org-a/repo-a",
			wantCode:     http.StatusOK,
			wantContains: []string{"Overview", "run-repo-a"},
		},
		{
			name:         "RunsList",
			path:         "/github.com/org-a/repo-a/runs",
			wantCode:     http.StatusOK,
			wantContains: []string{"run-repo-a", "base-repo-a"},
		},
		{
			name:         "RunDetail",
			path:         "/github.com/org-a/repo-a/runs/run-repo-a",
			wantCode:     http.StatusOK,
			wantContains: []string{"run-repo-a", "task-1"},
		},
		{
			name:         "TaskSamplesList",
			path:         "/github.com/org-a/repo-a/runs/run-repo-a/tasks/task-1/samples",
			wantCode:     http.StatusOK,
			wantContains: []string{"s-101", "task-1"},
		},
		{
			name:         "SampleTranscript",
			path:         "/github.com/org-a/repo-a/runs/run-repo-a/tasks/task-1/samples/s-101",
			wantCode:     http.StatusOK,
			wantContains: []string{"s-101", "PASS", "test input"},
		},
		{
			name:         "ProxyDecodedTaskSamplesList",
			path:         "/github.com/org-a/repo-a/runs/run-repo-a/tasks/docs/readme_comprehension/samples",
			wantCode:     http.StatusOK,
			wantContains: []string{"s-202", "docs/readme_comprehension"},
		},
		{
			name:         "ProxyDecodedSampleTranscript",
			path:         "/github.com/org-a/repo-a/runs/run-repo-a/tasks/docs/readme_comprehension/samples/s-202",
			wantCode:     http.StatusOK,
			wantContains: []string{"s-202", "PASS", "reading docs"},
		},
		{
			name:         "CanonicalEscapedTaskSamplesList",
			path:         "/github.com/org-a/repo-a/runs/run-repo-a/tasks/docs%2Freadme_comprehension/samples",
			wantCode:     http.StatusOK,
			wantContains: []string{"s-202", "docs/readme_comprehension"},
		},
		{
			name:         "QueryParamTaskAndSample",
			path:         "/github.com/org-a/repo-a/runs/run-repo-a/tasks?task=docs/readme_comprehension&sample=s-202",
			wantCode:     http.StatusOK,
			wantContains: []string{"s-202", "PASS", "reading docs"},
		},
		{
			name:         "ComparisonsList",
			path:         "/github.com/org-a/repo-a/comparisons",
			wantCode:     http.StatusOK,
			wantContains: []string{"comp-repo-a", "IMPROVED"},
		},
		{
			name:         "ComparisonDetail",
			path:         "/github.com/org-a/repo-a/comparisons/comp-repo-a",
			wantCode:     http.StatusOK,
			wantContains: []string{"comp-repo-a", "IMPROVED"},
		},
		{
			name:         "PullsList",
			path:         "/github.com/org-a/repo-a/pulls",
			wantCode:     http.StatusOK,
			wantContains: []string{"#101", "comp-repo-a"},
		},
		{
			name:         "PullDetail",
			path:         "/github.com/org-a/repo-a/pulls/101",
			wantCode:     http.StatusOK,
			wantContains: []string{"#101", "comp-repo-a", "IMPROVED"},
		},
		{
			name:         "SeriesList",
			path:         "/github.com/org-a/repo-a/series",
			wantCode:     http.StatusOK,
			wantContains: []string{"main", "org-a/repo-a"},
		},
		{
			name:         "SeriesDetail",
			path:         "/github.com/org-a/repo-a/series/main",
			wantCode:     http.StatusOK,
			wantContains: []string{"base-repo-a", "main"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("GET %s returned code %d, want %d; body: %s", tc.path, w.Code, tc.wantCode, w.Body.String())
			}
			body := w.Body.String()
			for _, exp := range tc.wantContains {
				if !strings.Contains(body, exp) {
					t.Errorf("GET %s body missing %q; body: %s", tc.path, exp, body)
				}
			}
		})
	}
}

func TestRepoRoutes_RepoFiltering(t *testing.T) {
	server, _ := setupMultiRepoTestServer(t)

	// In org-a's runs list, org-b's run must not appear
	t.Run("RunsFilter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/org-a/repo-a/runs", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		body := w.Body.String()
		if !strings.Contains(body, "run-repo-a") {
			t.Errorf("expected run-repo-a in org-a runs")
		}
		if strings.Contains(body, "run-repo-b") {
			t.Errorf("unexpected run-repo-b in org-a runs")
		}
	})

	// In org-a's comparisons list, org-b's comparison must not appear
	t.Run("ComparisonsFilter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/org-a/repo-a/comparisons", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		body := w.Body.String()
		if !strings.Contains(body, "comp-repo-a") {
			t.Errorf("expected comp-repo-a in org-a comparisons")
		}
		if strings.Contains(body, "comp-repo-b") {
			t.Errorf("unexpected comp-repo-b in org-a comparisons")
		}
	})

	// In org-a's pulls list, org-b's pull must not appear
	t.Run("PullsFilter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/org-a/repo-a/pulls", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		body := w.Body.String()
		if !strings.Contains(body, "#101") {
			t.Errorf("expected #101 in org-a pulls")
		}
		if strings.Contains(body, "#202") {
			t.Errorf("unexpected #202 in org-a pulls")
		}
	})

	// In org-a's series list, org-b's series must not appear
	t.Run("SeriesFilter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/org-a/repo-a/series", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		body := w.Body.String()
		if !strings.Contains(body, "main") {
			t.Errorf("expected main in org-a series")
		}
		if strings.Contains(body, "release") {
			t.Errorf("unexpected release in org-a series")
		}
	})
}

func TestRepoRoutes_CrossRepo404(t *testing.T) {
	server, _ := setupMultiRepoTestServer(t)

	routes := []string{
		"/gitlab.com/org-b/repo-b/runs/run-repo-a",
		"/gitlab.com/org-b/repo-b/runs/run-repo-a/tasks/task-1/samples",
		"/gitlab.com/org-b/repo-b/runs/run-repo-a/tasks/task-1/samples/s-101",
		"/gitlab.com/org-b/repo-b/comparisons/comp-repo-a",
		"/gitlab.com/org-b/repo-b/pulls/101",
		"/gitlab.com/org-b/repo-b/series/main",
	}

	for _, path := range routes {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Errorf("GET %s returned code %d, want 404", path, w.Code)
			}
		})
	}
}

func TestRepoRoutes_InvalidRouteParams404(t *testing.T) {
	server, _ := setupMultiRepoTestServer(t)

	// Host without dot or invalid chars
	invalidPaths := []string{
		"/localhost/org-a/repo-a",
		"/bad_host/org-a/repo-a",
		"/github.com/bad@owner/repo-a",
		"/github.com/org-a/bad$repo",
	}

	for _, path := range invalidPaths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Errorf("GET %s returned code %d, want 404", path, w.Code)
			}
		})
	}
}

func TestRepoRoutes_LegacyRedirects(t *testing.T) {
	server, _ := setupMultiRepoTestServer(t)
	hashA := RepoHash("org-a/repo-a")

	tests := []struct {
		name         string
		path         string
		wantCode     int
		wantLocation string
	}{
		{
			name:         "LegacyRunDetail",
			path:         "/runs/eval-runner/run-repo-a",
			wantCode:     http.StatusFound,
			wantLocation: "/github.com/org-a/repo-a/runs/run-repo-a",
		},
		{
			name:         "LegacyTaskSamplesList",
			path:         "/runs/eval-runner/run-repo-a/tasks/task-1/samples",
			wantCode:     http.StatusFound,
			wantLocation: "/github.com/org-a/repo-a/runs/run-repo-a/tasks/task-1/samples",
		},
		{
			name:         "LegacySampleTranscript",
			path:         "/runs/eval-runner/run-repo-a/tasks/task-1/samples/s-101",
			wantCode:     http.StatusFound,
			wantLocation: "/github.com/org-a/repo-a/runs/run-repo-a/tasks/task-1/samples/s-101",
		},
		{
			name:         "LegacyComparisonDetail",
			path:         "/comparisons/eval-runner/comp-repo-a",
			wantCode:     http.StatusFound,
			wantLocation: "/github.com/org-a/repo-a/comparisons/comp-repo-a",
		},
		{
			name:         "LegacyPullDetail",
			path:         fmt.Sprintf("/pulls/%s/101", hashA),
			wantCode:     http.StatusFound,
			wantLocation: "/github.com/org-a/repo-a/pulls/101",
		},
		{
			name:         "LegacySeriesDetail",
			path:         fmt.Sprintf("/series/%s/main", hashA),
			wantCode:     http.StatusFound,
			wantLocation: "/github.com/org-a/repo-a/series/main",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("GET %s returned code %d, want %d", tc.path, w.Code, tc.wantCode)
			}
			if loc := w.Header().Get("Location"); loc != tc.wantLocation {
				t.Errorf("GET %s Location = %q, want %q", tc.path, loc, tc.wantLocation)
			}
		})
	}

	// Unresolvable run renders legacy page instead of 302
	t.Run("UnresolvableRun_RendersLegacyPage", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/run-unresolvable", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for unresolvable run, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "run-unresolvable") {
			t.Errorf("expected run-unresolvable in body")
		}
	})
}

func TestRepoRoutes_NamespacePropagation(t *testing.T) {
	server, _ := setupMultiRepoTestServer(t)

	// 1. In custom namespace, legacy redirect appends ?namespace=custom-ns
	t.Run("RedirectPropagatesNonDefaultNamespace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs/custom-ns/run-custom-ns", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusFound {
			t.Fatalf("expected 302 Found, got %d", w.Code)
		}
		loc := w.Header().Get("Location")
		if loc != "/github.com/org-a/repo-a/runs/run-custom-ns?namespace=custom-ns" {
			t.Errorf("expected Location with ?namespace=custom-ns, got %q", loc)
		}
	})

	// 2. In default namespace, legacy redirect does not append ?namespace=
	t.Run("RedirectOmitsDefaultNamespace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/runs/eval-runner/run-repo-a", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusFound {
			t.Fatalf("expected 302 Found, got %d", w.Code)
		}
		loc := w.Header().Get("Location")
		if loc != "/github.com/org-a/repo-a/runs/run-repo-a" {
			t.Errorf("expected Location without ?namespace=, got %q", loc)
		}
	})

	// 3. In custom namespace, links in page have ?namespace=custom-ns
	t.Run("PageLinksPropagateNonDefaultNamespace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/org-a/repo-a/runs?namespace=custom-ns", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "?namespace=custom-ns") {
			t.Errorf("expected ?namespace=custom-ns in page links, got body:\n%s", body)
		}
	})
}
