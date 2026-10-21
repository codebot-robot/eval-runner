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

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/pkg/ui"
)

type uiSmokeLogger interface {
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
}

// runUISmoke verifies that the eval-runner-ui service is up and serving both
// the /healthz endpoint and a Run detail page. When requireSamples is true,
// it also verifies that the Run detail page links to task samples and that
// both the samples list and sample transcript pages return 200 OK.
func runUISmoke(t *testing.T, harness *Harness, runName string, requireSamples bool) {
	// Port forward to the UI service on local port
	cmd := exec.Command("kubectl", "port-forward", "-n", "eval-runner", "svc/eval-runner-ui", "18088:80")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start port-forward: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	// Wait for port-forward to be ready
	baseURL := "http://127.0.0.1:18088"
	ready := false
	for i := 0; i < 30; i++ {
		resp, err := http.Get(baseURL + "/healthz")
		if err == nil && resp.StatusCode == http.StatusOK {
			ready = true
			resp.Body.Close()
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("eval-runner-ui port-forward failed to become ready")
	}

	// 1. Curl /healthz
	resp, err := http.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatalf("failed to GET /healthz: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok\n" {
		t.Fatalf("unexpected /healthz response: status=%d body=%q", resp.StatusCode, string(body))
	}

	// 2. Curl Run page
	runURL := fmt.Sprintf("%s/runs/eval-runner/%s", baseURL, runName)
	respRun, err := http.Get(runURL)
	if err != nil {
		t.Fatalf("failed to GET %s: %v", runURL, err)
	}
	bodyRun, _ := io.ReadAll(respRun.Body)
	respRun.Body.Close()
	if respRun.StatusCode != http.StatusOK {
		t.Fatalf("unexpected run page status: %d body=%s", respRun.StatusCode, string(bodyRun))
	}
	if !strings.Contains(string(bodyRun), runName) {
		t.Errorf("expected run name %q in run detail HTML", runName)
	}
	reFooter := regexp.MustCompile(`<span>eval-runner [0-9a-f]{40}(-dirty)?</span>`)
	if !reFooter.MatchString(string(bodyRun)) {
		t.Errorf("expected run detail HTML footer to contain stamped eval-runner version")
	}

	// 3. Curl Run page via repository-scoped URL derived from spec.repo.url
	repoURLOut := harness.runCmd("kubectl", "get", "run", runName, "-n", "eval-runner", "-o", "jsonpath={.spec.repo.url}")
	repoRef, ok := ui.ParseRepoURL(strings.TrimSpace(repoURLOut))
	if !ok {
		t.Fatalf("failed to parse repo URL %q for run %s", repoURLOut, runName)
	}
	scopedRunURL := fmt.Sprintf("%s%s/runs/%s", baseURL, repoRef.Path(), runName)
	respScoped, err := http.Get(scopedRunURL)
	if err != nil {
		t.Fatalf("failed to GET %s: %v", scopedRunURL, err)
	}
	bodyScoped, _ := io.ReadAll(respScoped.Body)
	respScoped.Body.Close()
	if respScoped.StatusCode != http.StatusOK {
		t.Fatalf("unexpected scoped run page status: %d body=%s", respScoped.StatusCode, string(bodyScoped))
	}
	if !strings.Contains(string(bodyScoped), runName) {
		t.Errorf("expected run name %q in scoped run detail HTML", runName)
	}

	// 4. Curl /pulls page
	pullsURL := baseURL + "/pulls"
	respPulls, err := http.Get(pullsURL)
	if err != nil {
		t.Fatalf("failed to GET %s: %v", pullsURL, err)
	}
	bodyPulls, _ := io.ReadAll(respPulls.Body)
	respPulls.Body.Close()
	if respPulls.StatusCode != http.StatusOK {
		t.Fatalf("unexpected pulls page status: %d body=%s", respPulls.StatusCode, string(bodyPulls))
	}

	// 5. Curl repository-scoped /pulls page derived from spec.repo.url
	scopedPullsURL := fmt.Sprintf("%s%s/pulls", baseURL, repoRef.Path())
	respScopedPulls, err := http.Get(scopedPullsURL)
	if err != nil {
		t.Fatalf("failed to GET %s: %v", scopedPullsURL, err)
	}
	bodyScopedPulls, _ := io.ReadAll(respScopedPulls.Body)
	respScopedPulls.Body.Close()
	if respScopedPulls.StatusCode != http.StatusOK {
		t.Fatalf("unexpected scoped pulls page status: %d body=%s", respScopedPulls.StatusCode, string(bodyScopedPulls))
	}

	// 6. Follow the first samples href from the run page, then the first sample href.
	followUISampleHops(t, baseURL, string(bodyRun), runURL, requireSamples)
}

// followUISampleHops follows the first samples href from the run detail page and then the
// first sample transcript href, asserting 200 OK at each hop. If requireSamples is true,
// it fails if the Run page has no samples href, if the samples list has no sample href,
// or if either hop does not return 200. When requireSamples is false, it logs and skips
// hops if no samples href is present.
func followUISampleHops(t uiSmokeLogger, baseURL, bodyRun, runURL string, requireSamples bool) {
	reSamples := regexp.MustCompile(`href="(/runs/[^"]+/samples)"`)
	matchSamples := reSamples.FindStringSubmatch(bodyRun)
	if len(matchSamples) <= 1 {
		if requireSamples {
			t.Fatalf("expected samples href in run page %s, but none found (requireSamples=true); body:\n%s", runURL, bodyRun)
			return
		}
		t.Logf("no samples href found in run detail HTML, skipping samples hops (requireSamples=false)")
		return
	}

	samplesURL := baseURL + matchSamples[1]
	respSamples, err := http.Get(samplesURL)
	if err != nil {
		t.Fatalf("failed to GET samples page %s: %v", samplesURL, err)
		return
	}
	bodySamples, _ := io.ReadAll(respSamples.Body)
	respSamples.Body.Close()
	if respSamples.StatusCode != http.StatusOK {
		t.Fatalf("unexpected samples page status: %d body=%s", respSamples.StatusCode, string(bodySamples))
		return
	}

	reSample := regexp.MustCompile(`href="(` + regexp.QuoteMeta(matchSamples[1]) + `/[^"]+)"`)
	matchSample := reSample.FindStringSubmatch(string(bodySamples))
	if len(matchSample) <= 1 {
		if requireSamples {
			t.Fatalf("expected sample href in samples page %s, but none found (requireSamples=true); body:\n%s", samplesURL, string(bodySamples))
			return
		}
		t.Logf("no sample href found in samples page %s, skipping sample transcript hop (requireSamples=false)", samplesURL)
		return
	}

	sampleURL := baseURL + matchSample[1]
	respSample, err := http.Get(sampleURL)
	if err != nil {
		t.Fatalf("failed to GET sample transcript page %s: %v", sampleURL, err)
		return
	}
	bodySample, _ := io.ReadAll(respSample.Body)
	respSample.Body.Close()
	if respSample.StatusCode != http.StatusOK {
		t.Fatalf("unexpected sample transcript page status: %d body=%s", respSample.StatusCode, string(bodySample))
		return
	}
}

// TestUISmoke is retained for backwards compatibility with `go test -run TestUISmoke`.
// When running the full E2E suite, the smoke test is run as a subtest of TestRunDiscovery
// (TestRunDiscovery/UISmoke) to avoid redundant cluster setup and image building.
func TestUISmoke(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("skipping e2e test: RUN_E2E is not set")
	}

	t.Skip("TestUISmoke is now run as a subtest of TestRunDiscovery (TestRunDiscovery/UISmoke) to avoid redundant deploy overhead")
}

type fakeSmokeLogger struct {
	fatalMsg string
	logMsg   string
}

func (f *fakeSmokeLogger) Fatalf(format string, args ...any) {
	f.fatalMsg = fmt.Sprintf(format, args...)
}

func (f *fakeSmokeLogger) Logf(format string, args ...any) {
	f.logMsg = fmt.Sprintf(format, args...)
}

func TestFollowUISampleHops(t *testing.T) {
	t.Run("NoSamplesHref_RequireSamplesFalse_LogsAndSkips", func(t *testing.T) {
		logger := &fakeSmokeLogger{}
		bodyRun := `<html><body><h1>Run smoke-run</h1><p>(artifacts unavailable)</p></body></html>`
		runURL := "http://127.0.0.1:18088/runs/eval-runner/smoke-run"

		followUISampleHops(logger, "http://127.0.0.1:18088", bodyRun, runURL, false)

		if logger.fatalMsg != "" {
			t.Fatalf("expected no Fatalf call, got: %s", logger.fatalMsg)
		}
		if !strings.Contains(logger.logMsg, "skipping samples hops (requireSamples=false)") {
			t.Errorf("expected log message about skipping hops, got: %q", logger.logMsg)
		}
	})

	t.Run("NoSamplesHref_RequireSamplesTrue_Fails", func(t *testing.T) {
		logger := &fakeSmokeLogger{}
		bodyRun := `<html><body><h1>Run smoke-run</h1><p>(artifacts unavailable)</p></body></html>`
		runURL := "http://127.0.0.1:18088/runs/eval-runner/smoke-run"

		followUISampleHops(logger, "http://127.0.0.1:18088", bodyRun, runURL, true)

		if logger.fatalMsg == "" {
			t.Fatalf("expected Fatalf to be called when requireSamples=true and no samples href")
		}
		if !strings.Contains(logger.fatalMsg, "expected samples href in run page") {
			t.Errorf("expected error message to mention missing samples href, got: %q", logger.fatalMsg)
		}
		if !strings.Contains(logger.fatalMsg, "requireSamples=true") {
			t.Errorf("expected error message to mention requireSamples=true, got: %q", logger.fatalMsg)
		}
	})

	t.Run("SamplesPageNon200_Fails", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/runs/eval-runner/smoke-run/tasks/task-1/samples" {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			http.NotFound(w, r)
		}))
		defer server.Close()

		logger := &fakeSmokeLogger{}
		bodyRun := `<html><body><a href="/runs/eval-runner/smoke-run/tasks/task-1/samples">task-1</a></body></html>`
		runURL := server.URL + "/runs/eval-runner/smoke-run"

		followUISampleHops(logger, server.URL, bodyRun, runURL, true)

		if logger.fatalMsg == "" {
			t.Fatalf("expected Fatalf when samples page returns 500")
		}
		if !strings.Contains(logger.fatalMsg, "unexpected samples page status: 500") {
			t.Errorf("expected error to mention status 500, got: %q", logger.fatalMsg)
		}
	})

	t.Run("SamplesPageNoSampleHref_RequireSamplesTrue_Fails", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/runs/eval-runner/smoke-run/tasks/task-1/samples" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`<html><body><h1>No samples</h1></body></html>`))
				return
			}
			http.NotFound(w, r)
		}))
		defer server.Close()

		logger := &fakeSmokeLogger{}
		bodyRun := `<html><body><a href="/runs/eval-runner/smoke-run/tasks/task-1/samples">task-1</a></body></html>`
		runURL := server.URL + "/runs/eval-runner/smoke-run"

		followUISampleHops(logger, server.URL, bodyRun, runURL, true)

		if logger.fatalMsg == "" {
			t.Fatalf("expected Fatalf when samples page has no sample href and requireSamples=true")
		}
		if !strings.Contains(logger.fatalMsg, "expected sample href in samples page") {
			t.Errorf("expected error to mention expected sample href, got: %q", logger.fatalMsg)
		}
	})

	t.Run("SamplesPageNoSampleHref_RequireSamplesFalse_LogsAndSkips", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/runs/eval-runner/smoke-run/tasks/task-1/samples" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`<html><body><h1>No samples</h1></body></html>`))
				return
			}
			http.NotFound(w, r)
		}))
		defer server.Close()

		logger := &fakeSmokeLogger{}
		bodyRun := `<html><body><a href="/runs/eval-runner/smoke-run/tasks/task-1/samples">task-1</a></body></html>`
		runURL := server.URL + "/runs/eval-runner/smoke-run"

		followUISampleHops(logger, server.URL, bodyRun, runURL, false)

		if logger.fatalMsg != "" {
			t.Fatalf("expected no Fatalf when requireSamples=false, got: %s", logger.fatalMsg)
		}
		if !strings.Contains(logger.logMsg, "skipping sample transcript hop (requireSamples=false)") {
			t.Errorf("expected log message about skipping transcript hop, got: %q", logger.logMsg)
		}
	})

	t.Run("SampleTranscriptNon200_Fails", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/runs/eval-runner/smoke-run/tasks/task-1/samples":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`<html><body><a href="/runs/eval-runner/smoke-run/tasks/task-1/samples/sample-001">sample-001</a></body></html>`))
			case "/runs/eval-runner/smoke-run/tasks/task-1/samples/sample-001":
				http.Error(w, "transcript not found", http.StatusNotFound)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		logger := &fakeSmokeLogger{}
		bodyRun := `<html><body><a href="/runs/eval-runner/smoke-run/tasks/task-1/samples">task-1</a></body></html>`
		runURL := server.URL + "/runs/eval-runner/smoke-run"

		followUISampleHops(logger, server.URL, bodyRun, runURL, true)

		if logger.fatalMsg == "" {
			t.Fatalf("expected Fatalf when transcript page returns 404")
		}
		if !strings.Contains(logger.fatalMsg, "unexpected sample transcript page status: 404") {
			t.Errorf("expected error to mention transcript status 404, got: %q", logger.fatalMsg)
		}
	})

	t.Run("BothHops200_Succeeds", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/runs/eval-runner/smoke-run/tasks/task-1/samples":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`<html><body><a href="/runs/eval-runner/smoke-run/tasks/task-1/samples/sample-001">sample-001</a></body></html>`))
			case "/runs/eval-runner/smoke-run/tasks/task-1/samples/sample-001":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`<html><body><h1>Transcript for sample-001</h1></body></html>`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		logger := &fakeSmokeLogger{}
		bodyRun := `<html><body><a href="/runs/eval-runner/smoke-run/tasks/task-1/samples">task-1</a></body></html>`
		runURL := server.URL + "/runs/eval-runner/smoke-run"

		followUISampleHops(logger, server.URL, bodyRun, runURL, true)

		if logger.fatalMsg != "" {
			t.Fatalf("expected no Fatalf when both hops return 200, got: %s", logger.fatalMsg)
		}
	})
}
