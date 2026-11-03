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
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestModelPortalSmoke(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("skipping e2e test: RUN_E2E is not set")
	}
	t.Skip("TestModelPortalSmoke is now run as a subtest of TestRunDiscovery (TestRunDiscovery/ModelPortalSmoke) to avoid redundant deploy overhead")
}

func runModelPortalSmoke(t *testing.T, harness *Harness) {
	token := os.Getenv("GEMINI_API_KEY")
	if token != "" {
		// Create or update model-portal-secret from GEMINI_API_KEY via stdin directly
		// to avoid passing or logging the token through harness.KubectlApplyContent or harness.runCmd.
		cmd := exec.Command("kubectl", "create", "secret", "generic", "model-portal-secret",
			"-n", "eval-runner",
			"--from-literal=token="+token,
			"--dry-run=client", "-o", "yaml")
		secretYAML, err := cmd.Output()
		if err != nil {
			t.Fatalf("failed to generate model-portal-secret manifest: %v", err)
		}
		applyCmd := exec.Command("kubectl", "apply", "-f", "-")
		applyCmd.Stdin = bytes.NewReader(secretYAML)
		if out, err := applyCmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to apply model-portal-secret: %v, out: %s", err, string(out))
		}
		harness.runCmd("kubectl", "rollout", "restart", "deployment/model-portal", "-n", "eval-runner")
		harness.runCmd("kubectl", "rollout", "status", "deployment/model-portal", "-n", "eval-runner", "--timeout=60s")
	}

	probePodName := fmt.Sprintf("model-portal-smoke-probe-%d", time.Now().UnixNano()%100000)
	probeManifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: eval-runner-sandbox
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 65534
    seccompProfile:
      type: RuntimeDefault
  containers:
  - name: probe
    image: alpine:3.21
    securityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      runAsNonRoot: true
      runAsUser: 65534
      capabilities:
        drop:
        - ALL
      seccompProfile:
        type: RuntimeDefault
    command: ["/bin/sh", "-c"]
    args:
    - |
      wget -S -O- --timeout=15 http://model-portal.eval-runner.svc/v1beta/models 2>&1 || true
`, probePodName)

	harness.KubectlApplyContent(probeManifest)
	defer harness.runCmd("kubectl", "delete", "pod", probePodName, "-n", "eval-runner-sandbox", "--ignore-not-found")

	var podPhase string
	for i := 0; i < 30; i++ {
		podPhase = strings.TrimSpace(harness.runCmd("kubectl", "get", "pod", probePodName, "-n", "eval-runner-sandbox", "-o", "jsonpath={.status.phase}"))
		if podPhase == "Succeeded" || podPhase == "Failed" {
			break
		}
		time.Sleep(1 * time.Second)
	}

	logs := harness.runCmd("kubectl", "logs", probePodName, "-n", "eval-runner-sandbox")
	if podPhase != "Succeeded" {
		t.Fatalf("model portal probe pod %s ended with phase %s; logs:\n%s", probePodName, podPhase, logs)
	}

	if token != "" {
		if !strings.Contains(logs, "200 OK") && !strings.Contains(logs, "HTTP/1.1 200") {
			t.Fatalf("expected HTTP 200 response from model portal with GEMINI_API_KEY set, got logs:\n%s", logs)
		}
		if !strings.Contains(logs, "models") {
			t.Fatalf("expected response body from model portal to contain 'models', got logs:\n%s", logs)
		}
	} else {
		if !strings.Contains(logs, "HTTP/1.1") && !strings.Contains(logs, "HTTP/") {
			t.Fatalf("expected an HTTP response from model portal, got logs:\n%s", logs)
		}
	}
}
