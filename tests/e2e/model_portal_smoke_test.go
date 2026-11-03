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
	"encoding/base64"
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
	if token == "" {
		secretOutput, err := exec.Command("kubectl", "get", "secret", "model-portal-secret", "-n", "eval-runner", "-o", "jsonpath={.data.token}").CombinedOutput()
		if err == nil {
			rawSecret := strings.TrimSpace(string(secretOutput))
			if decoded, decodeErr := base64.StdEncoding.DecodeString(rawSecret); decodeErr == nil && len(decoded) > 0 {
				token = string(decoded)
			}
		}
	}

	fakeUpstreamURL := os.Getenv("MODEL_PORTAL_FAKE_UPSTREAM_URL")
	if token == "" && fakeUpstreamURL == "" {
		t.Skip("skipping model portal real upstream check: neither GEMINI_API_KEY nor MODEL_PORTAL_FAKE_UPSTREAM_URL is configured")
	}

	if fakeUpstreamURL != "" {
		harness.runCmd("kubectl", "set", "env", "deployment/model-portal", "-n", "eval-runner", "TARGET_URL="+fakeUpstreamURL)
		harness.runCmd("kubectl", "rollout", "status", "deployment/model-portal", "-n", "eval-runner", "--timeout=60s")
		defer func() {
			harness.runCmd("kubectl", "set", "env", "deployment/model-portal", "-n", "eval-runner", "TARGET_URL=https://generativelanguage.googleapis.com")
			harness.runCmd("kubectl", "rollout", "status", "deployment/model-portal", "-n", "eval-runner", "--timeout=60s")
		}()
	} else if token != "" {
		secretManifest := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: model-portal-secret
  namespace: eval-runner
type: Opaque
stringData:
  token: %s
`, token)
		harness.KubectlApplyContent(secretManifest)
		harness.runCmd("kubectl", "rollout", "restart", "deployment/model-portal", "-n", "eval-runner")
		harness.runCmd("kubectl", "rollout", "status", "deployment/model-portal", "-n", "eval-runner", "--timeout=60s")
	}

	probePodName := fmt.Sprintf("model-portal-smoke-probe-%d", time.Now().UnixNano()%100000)
	probeManifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: eval-runner-sandbox
  labels:
    evalrunner.gke-labs.dev/sandbox: "true"
spec:
  restartPolicy: Never
  dnsConfig:
    searches:
    - eval-runner.svc.cluster.local
  containers:
  - name: probe
    image: alpine/git:v2.47.2
    command: ["/bin/sh", "-c"]
    args:
    - |
      wget -q -O- --timeout=15 http://model-portal/v1beta/models
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

	if !strings.Contains(logs, "models") {
		t.Fatalf("expected response from model portal to contain 'models', got:\n%s", logs)
	}
}
