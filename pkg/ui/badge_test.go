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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBadgeSVG_Golden(t *testing.T) {
	data := BadgeData{
		Label:       "evals",
		Message:     "81% ± 4",
		Color:       "green",
		ColorHex:    "#4c1",
		CommitShort: "c0ffee1",
		Title:       "81% ± 4 (c0ffee1)",
		HasData:     true,
	}

	svg := RenderBadgeSVG(data)
	assertGolden(t, "badge.golden.svg", string(svg))
}

func setupBadgeTestServer(t *testing.T, runs ...*v1alpha1.Run) *Server {
	t.Helper()
	return setupBadgeTestServerWithNamespace(t, "eval-runner", runs...)
}

func setupBadgeTestServerWithNamespace(t *testing.T, ns string, runs ...*v1alpha1.Run) *Server {
	t.Helper()
	scheme := newTestScheme(t)
	builder := fake.NewClientBuilder().WithScheme(scheme)
	var objects []v1alpha1.Run
	for _, r := range runs {
		if r != nil {
			rc := r.DeepCopy()
			rc.Namespace = ns
			objects = append(objects, *rc)
		}
	}
	cl := builder.WithLists(&v1alpha1.RunList{Items: objects}).Build()
	return NewServer(Config{
		Client:           cl,
		DefaultNamespace: ns,
	})
}

func createTestRun(name, branch, commit string, commitTime time.Time, tasks []v1alpha1.TaskStatus) *v1alpha1.Run {
	return &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "eval-runner",
			CreationTimestamp: metav1.NewTime(commitTime),
			Labels: map[string]string{
				"evalrunner.gke-labs.dev/branch": sanitizeBranchLabel(branch),
			},
			Annotations: map[string]string{
				"evalrunner.gke-labs.dev/commit-time": commitTime.UTC().Format(time.RFC3339),
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: commit,
			},
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
			Tasks: tasks,
		},
	}
}

func TestBadgeHandler_DataPresent(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	rate81 := 0.81
	rate40 := 0.40
	rate65 := 0.65

	// 1. High score (>= 80% -> green)
	runGreen := createTestRun("base-green", "main", "c0ffee1111111111111111111111111111111111", now, []v1alpha1.TaskStatus{
		{
			ID:          "suite/task-1",
			Phase:       v1alpha1.TaskPhaseCompleted,
			Samples:     200,
			Successes:   162,
			SuccessRate: &rate81,
		},
	})

	configuredNS := "custom-secret-ns"
	server := setupBadgeTestServerWithNamespace(t, configuredNS, runGreen)

	t.Run("badge.svg_green", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.svg", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "image/svg+xml" {
			t.Errorf("expected Content-Type: image/svg+xml, got %q", ct)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=300" {
			t.Errorf("expected Cache-Control: public, max-age=300, got %q", cc)
		}
		if etag := w.Header().Get("ETag"); etag == "" {
			t.Errorf("expected non-empty ETag")
		}
		if cookie := w.Header().Get("Set-Cookie"); cookie != "" {
			t.Errorf("expected no cookies, got %q", cookie)
		}

		body := w.Body.String()
		if !strings.Contains(body, `<svg `) || !strings.Contains(body, `width="`) || !strings.Contains(body, `height="20"`) {
			t.Errorf("expected SVG with explicit width and height, got:\n%s", body)
		}
		if strings.Contains(body, "<foreignObject") {
			t.Errorf("expected text as <text>, found foreignObject")
		}
		if strings.Contains(body, "<script") {
			t.Errorf("expected no JavaScript in SVG")
		}
		if !strings.Contains(body, ">evals<") {
			t.Errorf("expected label 'evals' in badge SVG")
		}
		if !strings.Contains(body, "81%") {
			t.Errorf("expected '81%%' in badge body")
		}
		if !strings.Contains(body, "#4c1") {
			t.Errorf("expected green fill #4c1 for score >= 80, body:\n%s", body)
		}
		if !strings.Contains(body, "<title>") || !strings.Contains(body, "c0ffee1") {
			t.Errorf("expected short commit SHA 'c0ffee1' in <title> tooltip, got:\n%s", body)
		}
		if strings.Contains(body, configuredNS) {
			t.Errorf("must not leak deployment namespace %q in SVG, got body:\n%s", configuredNS, body)
		}
	})

	t.Run("sparkline.svg_dataPresent", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/sparkline.svg", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "image/svg+xml" {
			t.Errorf("expected Content-Type: image/svg+xml, got %q", ct)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=300" {
			t.Errorf("expected Cache-Control: public, max-age=300, got %q", cc)
		}
		if etag := w.Header().Get("ETag"); etag == "" {
			t.Errorf("expected non-empty ETag")
		}
		if cookie := w.Header().Get("Set-Cookie"); cookie != "" {
			t.Errorf("expected no cookies, got %q", cookie)
		}

		body := w.Body.String()
		if !strings.Contains(body, `width="120"`) || !strings.Contains(body, `height="28"`) {
			t.Errorf("expected default width=120 and height=28 in sparkline SVG, got:\n%s", body)
		}
		if !strings.Contains(body, "<polygon") || !strings.Contains(body, "fill-opacity=\"0.15\"") {
			t.Errorf("expected shaded CI band polygon in sparkline SVG, got:\n%s", body)
		}
		if !strings.Contains(body, "<circle") {
			t.Errorf("expected circle point in sparkline SVG")
		}
		if strings.Contains(body, "<foreignObject") || strings.Contains(body, "<script") {
			t.Errorf("sparkline SVG must not contain foreignObject or script")
		}
		if strings.Contains(body, configuredNS) {
			t.Errorf("sparkline SVG must not leak deployment namespace %q", configuredNS)
		}
	})

	t.Run("badge.json_schema", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.json", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("expected application/json, got %q", ct)
		}

		var resp ShieldsEndpointResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse shields endpoint json: %v", err)
		}
		if resp.SchemaVersion != 1 {
			t.Errorf("expected schemaVersion 1, got %d", resp.SchemaVersion)
		}
		if resp.Label != "evals" {
			t.Errorf("expected label 'evals', got %q", resp.Label)
		}
		if !strings.HasPrefix(resp.Message, "81%") {
			t.Errorf("expected message starting with '81%%', got %q", resp.Message)
		}
		if resp.Color != "green" {
			t.Errorf("expected color 'green', got %q", resp.Color)
		}
	})

	// 2. Yellow score (< 80, >= 50)
	runYellow := createTestRun("base-yellow", "main", "2222222222222222222222222222222222222222", now, []v1alpha1.TaskStatus{
		{
			ID:          "suite/task-1",
			Phase:       v1alpha1.TaskPhaseCompleted,
			Samples:     100,
			Successes:   65,
			SuccessRate: &rate65,
		},
	})
	serverYellow := setupBadgeTestServer(t, runYellow)
	reqY := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.svg", nil)
	wY := httptest.NewRecorder()
	serverYellow.ServeHTTP(wY, reqY)
	if !strings.Contains(wY.Body.String(), "#dfb317") {
		t.Errorf("expected yellow fill #dfb317 for 65%%, got:\n%s", wY.Body.String())
	}

	// 3. Red score (< 50)
	runRed := createTestRun("base-red", "main", "3333333333333333333333333333333333333333", now, []v1alpha1.TaskStatus{
		{
			ID:          "suite/task-1",
			Phase:       v1alpha1.TaskPhaseCompleted,
			Samples:     100,
			Successes:   40,
			SuccessRate: &rate40,
		},
	})
	serverRed := setupBadgeTestServer(t, runRed)
	reqR := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.svg", nil)
	wR := httptest.NewRecorder()
	serverRed.ServeHTTP(wR, reqR)
	if !strings.Contains(wR.Body.String(), "#e05d44") {
		t.Errorf("expected red fill #e05d44 for 40%%, got:\n%s", wR.Body.String())
	}
}

func TestBadgeHandler_NoData(t *testing.T) {
	now := time.Now()
	// Repo has a run, but 0 completed tasks/samples
	runEmpty := createTestRun("base-empty", "main", "1111111111111111111111111111111111111111", now, nil)
	server := setupBadgeTestServer(t, runEmpty)

	t.Run("badge.svg_noData", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.svg", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for no data, got %d: %s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "image/svg+xml" {
			t.Errorf("expected Content-Type: image/svg+xml, got %q", ct)
		}
		body := w.Body.String()
		if !strings.Contains(body, "no data") {
			t.Errorf("expected 'no data' in SVG body, got:\n%s", body)
		}
		if !strings.Contains(body, "#9f9f9f") {
			t.Errorf("expected grey #9f9f9f for no data, got:\n%s", body)
		}
	})

	t.Run("sparkline.svg_noData", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/sparkline.svg", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for no data sparkline, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "no data") {
			t.Errorf("expected 'no data' in sparkline body, got:\n%s", body)
		}
	})

	t.Run("badge.json_noData", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.json", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", w.Code)
		}
		var resp ShieldsEndpointResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if resp.Message != "no data" {
			t.Errorf("expected message 'no data', got %q", resp.Message)
		}
		if resp.Color != "grey" {
			t.Errorf("expected color 'grey', got %q", resp.Color)
		}
	})
}

func TestBadgeHandler_EvalFilter(t *testing.T) {
	now := time.Now()
	rateA := 0.90
	rateB := 0.30

	run := createTestRun("base-eval-filter", "main", "abcdef1111111111111111111111111111111111", now, []v1alpha1.TaskStatus{
		{
			ID:          "suite/task-a",
			Phase:       v1alpha1.TaskPhaseCompleted,
			Samples:     100,
			Successes:   90,
			SuccessRate: &rateA,
		},
		{
			ID:          "suite/task-b",
			Phase:       v1alpha1.TaskPhaseCompleted,
			Samples:     100,
			Successes:   30,
			SuccessRate: &rateB,
		},
	})

	server := setupBadgeTestServer(t, run)

	// Filter on task-a: label should be "suite/task-a", score 90%
	t.Run("filter_task_a", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.svg?eval=suite/task-a", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "suite/task-a") {
			t.Errorf("expected label 'suite/task-a' in badge SVG, got:\n%s", body)
		}
		if !strings.Contains(body, "90%") {
			t.Errorf("expected '90%%' in badge SVG for task-a, got:\n%s", body)
		}
		if !strings.Contains(body, "#4c1") {
			t.Errorf("expected green color for task-a, got:\n%s", body)
		}
	})

	// Filter on task-b: label should be "suite/task-b", score 30% (red)
	t.Run("filter_task_b", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.svg?eval=suite/task-b", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "suite/task-b") {
			t.Errorf("expected label 'suite/task-b' in badge SVG, got:\n%s", body)
		}
		if !strings.Contains(body, "30%") {
			t.Errorf("expected '30%%' in badge SVG for task-b, got:\n%s", body)
		}
		if !strings.Contains(body, "#e05d44") {
			t.Errorf("expected red color for task-b, got:\n%s", body)
		}
	})

	// Filter on non-existent task
	t.Run("filter_task_nonexistent", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.svg?eval=suite/task-ghost", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for known repo with no data on task, got %d: %s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "suite/task-ghost") {
			t.Errorf("expected label 'suite/task-ghost' in badge SVG, got:\n%s", body)
		}
		if !strings.Contains(body, "no data") {
			t.Errorf("expected 'no data' for task without observations, got:\n%s", body)
		}
	})

	// Filter with long task ID (> 40 chars)
	t.Run("filter_task_long_truncated", func(t *testing.T) {
		longTaskID := "suite/very_long_task_name_that_exceeds_forty_characters_limit"
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.svg?eval="+longTaskID, nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		expectedTruncated := longTaskID[:37] + "..."
		if !strings.Contains(body, expectedTruncated) {
			t.Errorf("expected truncated label %q in badge SVG, got:\n%s", expectedTruncated, body)
		}
	})
}

func TestBadgeHandler_UnknownRepo_404SVG(t *testing.T) {
	// Server has NO runs for github.com/other-owner/unknown-repo
	server := setupBadgeTestServer(t)

	t.Run("badge.svg_unknownRepo", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/other-owner/unknown-repo/badge.svg", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found for unknown repo, got %d", w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "image/svg+xml" {
			t.Errorf("expected Content-Type: image/svg+xml, got %q", ct)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=300" {
			t.Errorf("expected Cache-Control header on 404, got %q", cc)
		}
		if etag := w.Header().Get("ETag"); etag == "" {
			t.Errorf("expected ETag header on 404")
		}

		body := w.Body.String()
		if !strings.Contains(body, "<svg") {
			t.Errorf("expected SVG body on 404, got:\n%s", body)
		}
		if !strings.Contains(body, "unknown") {
			t.Errorf("expected SVG body to say 'unknown', got:\n%s", body)
		}
	})

	t.Run("sparkline.svg_unknownRepo", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/other-owner/unknown-repo/sparkline.svg", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found for unknown repo, got %d", w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "image/svg+xml" {
			t.Errorf("expected Content-Type: image/svg+xml, got %q", ct)
		}

		body := w.Body.String()
		if !strings.Contains(body, "<svg") {
			t.Errorf("expected SVG body on 404, got:\n%s", body)
		}
		if !strings.Contains(body, "unknown") {
			t.Errorf("expected sparkline SVG body to say 'unknown', got:\n%s", body)
		}
	})

	t.Run("badge.json_unknownRepo", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/other-owner/unknown-repo/badge.json", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found for unknown repo, got %d", w.Code)
		}
		var resp ShieldsEndpointResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse shields endpoint json: %v", err)
		}
		if resp.Message != "unknown" {
			t.Errorf("expected message 'unknown', got %q", resp.Message)
		}
	})
}

func TestBadgeHandler_SizeBounds(t *testing.T) {
	now := time.Now()
	rate := 0.75
	run := createTestRun("base-size", "main", "1234567890123456789012345678901234567890", now, []v1alpha1.TaskStatus{
		{
			ID:          "task-1",
			Phase:       v1alpha1.TaskPhaseCompleted,
			Samples:     50,
			Successes:   38,
			SuccessRate: &rate,
		},
	})
	server := setupBadgeTestServer(t, run)

	tests := []struct {
		name       string
		query      string
		wantWidth  string
		wantHeight string
	}{
		{
			name:       "defaults",
			query:      "",
			wantWidth:  `width="120"`,
			wantHeight: `height="28"`,
		},
		{
			name:       "custom valid",
			query:      "?w=300&h=60",
			wantWidth:  `width="300"`,
			wantHeight: `height="60"`,
		},
		{
			name:       "clamped min width and height",
			query:      "?w=10&h=2",
			wantWidth:  `width="30"`,
			wantHeight: `height="10"`,
		},
		{
			name:       "clamped max width and height",
			query:      "?w=99999&h=88888",
			wantWidth:  `width="1000"`,
			wantHeight: `height="300"`,
		},
		{
			name:       "invalid integers fallback to default",
			query:      "?w=not-a-number&h=-10",
			wantWidth:  `width="120"`,
			wantHeight: `height="10"`, // -10 clamped to min 10
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/sparkline.svg"+tc.query, nil)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 OK, got %d", w.Code)
			}
			body := w.Body.String()
			if !strings.Contains(body, tc.wantWidth) {
				t.Errorf("expected %s in sparkline SVG, got:\n%s", tc.wantWidth, body)
			}
			if !strings.Contains(body, tc.wantHeight) {
				t.Errorf("expected %s in sparkline SVG, got:\n%s", tc.wantHeight, body)
			}
		})
	}
}

func TestBadgeHandler_CachingAndETag(t *testing.T) {
	now := time.Now()
	rate := 0.85
	run := createTestRun("base-etag", "main", "etag111111111111111111111111111111111111", now, []v1alpha1.TaskStatus{
		{
			ID:          "task-1",
			Phase:       v1alpha1.TaskPhaseCompleted,
			Samples:     100,
			Successes:   85,
			SuccessRate: &rate,
		},
	})
	server := setupBadgeTestServer(t, run)

	req1 := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.svg", nil)
	w1 := httptest.NewRecorder()
	server.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w1.Code)
	}
	etag := w1.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("expected non-empty ETag")
	}

	// Second request with If-None-Match
	req2 := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.svg", nil)
	req2.Header.Set("If-None-Match", etag)
	w2 := httptest.NewRecorder()
	server.ServeHTTP(w2, req2)

	if w2.Code != http.StatusNotModified {
		t.Errorf("expected 304 Not Modified, got %d", w2.Code)
	}
	if w2.Body.Len() != 0 {
		t.Errorf("expected empty body on 304, got %d bytes", w2.Body.Len())
	}
}

func TestBadgeHandler_DefaultBranchResolution(t *testing.T) {
	now := time.Now()
	rateMain := 0.88
	rateDev := 0.66
	runMain := createTestRun("base-main", "main", "main111111111111111111111111111111111111", now, []v1alpha1.TaskStatus{
		{
			ID:          "task-1",
			Phase:       v1alpha1.TaskPhaseCompleted,
			Samples:     100,
			Successes:   88,
			SuccessRate: &rateMain,
		},
	})
	runDev := createTestRun("base-dev", "develop", "dev1111111111111111111111111111111111111", now, []v1alpha1.TaskStatus{
		{
			ID:          "task-1",
			Phase:       v1alpha1.TaskPhaseCompleted,
			Samples:     100,
			Successes:   66,
			SuccessRate: &rateDev,
		},
	})
	server := setupBadgeTestServer(t, runMain, runDev)

	// Call without ?branch=: defaults to "main" (88%)
	reqDefault := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.svg", nil)
	wDefault := httptest.NewRecorder()
	server.ServeHTTP(wDefault, reqDefault)
	if wDefault.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", wDefault.Code)
	}
	if !strings.Contains(wDefault.Body.String(), "88%") {
		t.Errorf("expected default branch to pick main and show 88%%, got:\n%s", wDefault.Body.String())
	}

	// Call with ?branch=develop: picks develop (66%)
	reqDev := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.svg?branch=develop", nil)
	wDev := httptest.NewRecorder()
	server.ServeHTTP(wDev, reqDev)
	if wDev.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", wDev.Code)
	}
	if !strings.Contains(wDev.Body.String(), "66%") {
		t.Errorf("expected ?branch=develop to pick develop and show 66%%, got:\n%s", wDev.Body.String())
	}
}

func TestBadgeHandler_InternalError_DegradesGracefully(t *testing.T) {
	// A server where namespace lookup fails or reader fails
	server := NewServer(Config{
		DefaultNamespace: "eval-runner",
		// Client is nil -> List returns error -> 500
	})

	t.Run("badge.svg_500", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.svg", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 InternalServerError, got %d", w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "image/svg+xml" {
			t.Errorf("expected Content-Type: image/svg+xml on 500, got %q", ct)
		}
		if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("expected no-cache on 500, got %q", cc)
		}
		body := w.Body.String()
		if !strings.Contains(body, "<svg") || !strings.Contains(body, "error") {
			t.Errorf("expected grey error badge on 500, got:\n%s", body)
		}
	})

	t.Run("sparkline.svg_500", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/sparkline.svg", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 InternalServerError, got %d", w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "image/svg+xml" {
			t.Errorf("expected Content-Type: image/svg+xml on 500, got %q", ct)
		}
		if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("expected no-cache on 500, got %q", cc)
		}
		body := w.Body.String()
		if !strings.Contains(body, "<svg") || !strings.Contains(body, "error") {
			t.Errorf("expected error sparkline on 500, got:\n%s", body)
		}
	})

	t.Run("badge.json_500", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/github.com/gke-labs/eval-runner/badge.json", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 InternalServerError, got %d", w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("expected application/json on 500, got %q", ct)
		}
		if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("expected no-cache on 500, got %q", cc)
		}
		var resp ShieldsEndpointResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse shields endpoint json on 500: %v", err)
		}
		if resp.Message != "error" {
			t.Errorf("expected message 'error', got %q", resp.Message)
		}
	})
}
