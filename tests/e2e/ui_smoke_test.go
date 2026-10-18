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
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// runUISmoke verifies that the eval-runner-ui service is up and serving both
// the /healthz endpoint and a Run detail page.
func runUISmoke(t *testing.T, harness *Harness, runName string) {
	// Port forward to the UI service on local port
	cmd := exec.Command("kubectl", "port-forward", "-n", "eval-runner", "svc/eval-runner-ui", "18088:80")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start port-forward: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
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
