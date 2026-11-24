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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/controller"
	"github.com/gke-labs/eval-runner/pkg/discovery"
	corev1 "k8s.io/api/core/v1"
)

type modelStandinRecord struct {
	Method              string `json:"method"`
	Host                string `json:"host"`
	Path                string `json:"path"`
	HadHeader           bool   `json:"had_header"`
	MatchedExpectedKey  bool   `json:"matched_expected_key"`
	ReceivedPlaceholder bool   `json:"received_placeholder"`
}

func TestServicePortalModelCredentialInjection(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("skipping e2e test: RUN_E2E is not set")
	}

	harness := GetSharedHarness(t)
	const (
		expectedRealKey = "e2e-service-portal-real-gemini-key"
		standinHost     = "model-standin.eval-runner-sandbox.svc.cluster.local"
		otherHost       = "other-standin.eval-runner-sandbox.svc.cluster.local"
		standinBaseURL  = "http://model-standin.eval-runner-sandbox.svc.cluster.local"
	)

	// Capture existing servicePortalAllowedHTTPHosts so cleanup restores the shared cluster cleanly.
	originalAllowedHTTPHosts := strings.TrimSpace(harness.runCmd(
		"kubectl", "get", "configmap", "eval-runner-config", "-n", "eval-runner",
		"-o", "jsonpath={.data.servicePortalAllowedHTTPHosts}",
	))

	t.Cleanup(func() {
		harness.runCmd("kubectl", "delete", "run", "--all", "-n", "eval-runner", "--ignore-not-found")
		harness.WaitForAllSandboxesDeleted("eval-runner-sandbox", 30*time.Second)
		restoreData := map[string]any{
			"servicePortalModelBaseURL": nil,
		}
		if originalAllowedHTTPHosts != "" {
			restoreData["servicePortalAllowedHTTPHosts"] = originalAllowedHTTPHosts
		}
		restorePatch, err := json.Marshal(map[string]any{"data": restoreData})
		if err == nil {
			_ = exec.Command("kubectl", "patch", "configmap", "eval-runner-config", "-n", "eval-runner", "--type=merge", "-p", string(restorePatch)).Run()
		}
		_ = exec.Command("kubectl", "delete", "secret", controller.DefaultModelCredentialsSecretName, "-n", "eval-runner", "--ignore-not-found").Run()
		_ = exec.Command("kubectl", "delete", "deployment", "model-standin", "-n", "eval-runner-sandbox", "--ignore-not-found").Run()
		_ = exec.Command("kubectl", "delete", "service", "model-standin", "other-standin", "-n", "eval-runner-sandbox", "--ignore-not-found").Run()
		_ = exec.Command("kubectl", "delete", "configmap", "model-standin-script", "-n", "eval-runner-sandbox", "--ignore-not-found").Run()
	})

	// Ensure no model-credentials Secret exists in the sandbox namespace.
	_ = exec.Command("kubectl", "delete", "secret", controller.DefaultModelCredentialsSecretName, "-n", "eval-runner-sandbox", "--ignore-not-found").Run()

	// Create the real model API key Secret exclusively in the controller's namespace.
	controllerSecretManifest := fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: eval-runner
type: Opaque
stringData:
  GEMINI_API_KEY: %s
`, controller.DefaultModelCredentialsSecretName, expectedRealKey)
	harness.KubectlApplyContent(controllerSecretManifest)

	// Reuse the sample-runner image already loaded into kind (contains python3) for the stand-in server.
	sampleRunnerImage := strings.TrimSpace(harness.runCmd(
		"kubectl", "get", "deployment", "eval-runner-controller", "-n", "eval-runner",
		"-o", `jsonpath={.spec.template.metadata.annotations.evalrunner\.gke-labs\.dev/sample-runner-image}`,
	))
	if sampleRunnerImage == "" {
		sampleRunnerImage = "images.local/sample-runner:dev"
	}

	standinManifest := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: model-standin-script
  namespace: eval-runner-sandbox
data:
  server.py: |
    import json
    import os
    import threading
    from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

    EXPECTED_KEY = os.environ["EXPECTED_API_KEY"]
    PLACEHOLDER_KEY = os.environ["PLACEHOLDER_API_KEY"]
    RECORDS = []
    LOCK = threading.Lock()

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, format, *args):
            pass

        def _record(self):
            raw_key = self.headers.get("x-goog-api-key")
            entry = {
                "method": self.command,
                "host": (self.headers.get("Host") or "").split(":")[0].lower(),
                "path": self.path,
                "had_header": raw_key is not None,
                "matched_expected_key": raw_key == EXPECTED_KEY,
                "received_placeholder": raw_key == PLACEHOLDER_KEY,
            }
            with LOCK:
                RECORDS.append(entry)
            return entry

        def _send_json(self, code, payload):
            body = json.dumps(payload).encode("utf-8")
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            if self.path == "/healthz":
                self._send_json(200, {"status": "ok"})
                return
            if self.path == "/records":
                with LOCK:
                    snapshot = list(RECORDS)
                self._send_json(200, snapshot)
                return
            entry = self._record()
            self._send_json(200, entry)

        def do_POST(self):
            length = int(self.headers.get("Content-Length") or "0")
            if length > 0:
                self.rfile.read(length)
            entry = self._record()
            if not entry["matched_expected_key"]:
                self._send_json(401, {"error": {"code": 401, "message": "API key not valid", "status": "UNAUTHENTICATED"}})
                return
            self._send_json(200, {
                "candidates": [{
                    "content": {"parts": [{"text": "OK"}], "role": "model"},
                    "finishReason": "STOP",
                }],
                "usageMetadata": {
                    "promptTokenCount": 10,
                    "candidatesTokenCount": 2,
                    "totalTokenCount": 12,
                },
            })

    ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: model-standin
  namespace: eval-runner-sandbox
spec:
  replicas: 1
  selector:
    matchLabels:
      app: model-standin
  template:
    metadata:
      labels:
        app: model-standin
    spec:
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        runAsGroup: 1000
        seccompProfile:
          type: RuntimeDefault
      containers:
      - name: standin
        image: %s
        imagePullPolicy: IfNotPresent
        command: ["python3", "/scripts/server.py"]
        env:
        - name: EXPECTED_API_KEY
          value: %q
        - name: PLACEHOLDER_API_KEY
          value: %q
        ports:
        - containerPort: 8080
        readinessProbe:
          httpGet:
            path: /healthz
            port: 8080
          initialDelaySeconds: 1
          periodSeconds: 2
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop:
            - ALL
        volumeMounts:
        - name: script
          mountPath: /scripts
          readOnly: true
      volumes:
      - name: script
        configMap:
          name: model-standin-script
---
apiVersion: v1
kind: Service
metadata:
  name: model-standin
  namespace: eval-runner-sandbox
spec:
  selector:
    app: model-standin
  ports:
  - port: 80
    targetPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: other-standin
  namespace: eval-runner-sandbox
spec:
  selector:
    app: model-standin
  ports:
  - port: 80
    targetPort: 8080
`, sampleRunnerImage, expectedRealKey, discovery.PlaceholderModelCredential)
	harness.KubectlApplyContent(standinManifest)
	harness.runCmd("kubectl", "wait", "-n", "eval-runner-sandbox", "--for=condition=available", "deployment/model-standin", "--timeout=90s")

	// Configure eval-runner-config with servicePortalModelBaseURL and allow other-standin over HTTP.
	updatedHTTPHosts := otherHost
	if originalAllowedHTTPHosts != "" {
		updatedHTTPHosts = originalAllowedHTTPHosts + "," + otherHost
	}
	configPatch, err := json.Marshal(map[string]any{
		"data": map[string]string{
			"servicePortalModelBaseURL":     standinBaseURL,
			"servicePortalAllowedHTTPHosts": updatedHTTPHosts,
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal ConfigMap patch: %v", err)
	}
	harness.runCmd("kubectl", "patch", "configmap", "eval-runner-config", "-n", "eval-runner", "--type=merge", "-p", string(configPatch))

	// Wait for controller runtime config reload to log the updated servicePortalModelBaseURL.
	reloadDeadline := time.Now().Add(30 * time.Second)
	reloaded := false
	for time.Now().Before(reloadDeadline) {
		logs, logError := exec.Command("kubectl", "logs", "-n", "eval-runner", "deployment/eval-runner-controller", "--tail=80").CombinedOutput()
		if logError == nil && strings.Contains(string(logs), standinBaseURL) {
			reloaded = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !reloaded {
		t.Fatalf("controller did not reload servicePortalModelBaseURL=%s within 30s", standinBaseURL)
	}

	// Push a temporary branch to git-server with an Inspect eval task that verifies:
	// 1. Pod env holds only the placeholder credential.
	// 2. Model host request without the header is forwarded without the credential.
	// 3. Another host request with the placeholder header is forwarded without credential swap.
	// 4. Real google-genai SDK generate() call gets the real key injected by the service portal.
	gitDirectory := "/git/docs-eval.git"
	branchName := fmt.Sprintf("service-portal-credential-%d", time.Now().UnixNano()%1000000)
	harness.runCmd("kubectl", "exec", "-n", "eval-runner-sandbox", "deployment/git-server", "--",
		"sh", "-c", fmt.Sprintf(`
			rm -rf /tmp/service-portal-work &&
			git clone %s /tmp/service-portal-work &&
			cd /tmp/service-portal-work &&
			cat <<'PYEOF' > evals/service_portal_credential_check.py
import asyncio
import json
import os
import urllib.request
from inspect_ai import Task, task
from inspect_ai.dataset import Sample
from inspect_ai.scorer import exact
from inspect_ai.solver import Generate, TaskState, generate, solver

@solver
def verify_egress_boundaries():
    async def solve(state: TaskState, generate_function: Generate) -> TaskState:
        assert os.environ.get("GEMINI_API_KEY") == "%s", "expected placeholder GEMINI_API_KEY"
        await asyncio.sleep(3)
        with urllib.request.urlopen("http://%s/probe", timeout=10) as response:
            no_header = json.loads(response.read().decode())
        assert not no_header["had_header"], f"unexpected header on model host probe: {no_header}"
        assert not no_header["matched_expected_key"], f"credential leaked without header: {no_header}"

        other_request = urllib.request.Request(
            "http://%s/probe",
            headers={"x-goog-api-key": os.environ["GEMINI_API_KEY"]},
        )
        with urllib.request.urlopen(other_request, timeout=10) as response:
            other_host = json.loads(response.read().decode())
        assert other_host["had_header"], f"expected placeholder header on other host: {other_host}"
        assert other_host["received_placeholder"], f"expected placeholder preserved on other host: {other_host}"
        assert not other_host["matched_expected_key"], f"credential leaked to other host: {other_host}"
        return state
    return solve

@task
def credential_check() -> Task:
    return Task(
        name="service-portal/credential-check",
        dataset=[Sample(input="Reply with OK", target="OK")],
        solver=[verify_egress_boundaries(), generate()],
        scorer=exact(),
    )
PYEOF
			cat <<'LISTEOF' > .evals/list
#!/bin/sh
cat <<'EOF'
{
  "tasks": [
    { "id": "service-portal/credential-check", "workdir": "evals", "spec": "service_portal_credential_check.py@credential_check", "paths": ["evals/**"], "resources": [] }
  ]
}
EOF
LISTEOF
			chmod +x .evals/list &&
			git add evals/service_portal_credential_check.py .evals/list &&
			git -c user.name=eval-runner-e2e -c user.email=e2e@example.com commit -m "add service portal credential check task" &&
			git push origin HEAD:%s &&
			rm -rf /tmp/service-portal-work
		`, gitDirectory, discovery.PlaceholderModelCredential, standinHost, otherHost, branchName))

	t.Cleanup(func() {
		_ = exec.Command("kubectl", "exec", "-n", "eval-runner-sandbox", "deployment/git-server", "--",
			"git", "--git-dir="+gitDirectory, "branch", "-D", branchName).Run()
	})

	commit := strings.TrimSpace(harness.runCmd("kubectl", "exec", "-n", "eval-runner-sandbox", "deployment/git-server", "--",
		"git", "--git-dir="+gitDirectory, "rev-parse", "refs/heads/"+branchName))

	runName := "service-portal-credential-injection"
	runManifest := fmt.Sprintf(`
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: %s
  namespace: eval-runner
spec:
  repo:
    url: http://git-server.eval-runner-sandbox.svc/docs-eval.git
    commit: %s
  model: google/gemini-3.5-flash-lite
  limit: 1
  tasks:
  - service-portal/credential-check
`, runName, commit)
	harness.KubectlApplyContent(runManifest)

	// Poll the Run until Completed while observing the live sample-runner pod.
	deadline := time.Now().Add(180 * time.Second)
	var completedRun *v1alpha1.Run
	var podObserved bool
	for time.Now().Before(deadline) {
		runOutput, getError := exec.Command("kubectl", "get", "run", runName, "-n", "eval-runner", "-o", "json").Output()
		if getError == nil {
			var currentRun v1alpha1.Run
			if json.Unmarshal(runOutput, &currentRun) == nil {
				for _, task := range currentRun.Status.Tasks {
					if task.Phase == v1alpha1.TaskPhaseRunning && !podObserved {
						sandboxName := discovery.TaskSandboxNameForRun(runName, task.ID)
						podOutput, podError := exec.Command("kubectl", "get", "pod", sandboxName, "-n", "eval-runner-sandbox", "-o", "json").Output()
						if podError == nil {
							var pod corev1.Pod
							if json.Unmarshal(podOutput, &pod) == nil && len(pod.Spec.Containers) > 0 {
								podObserved = true
								harness.AssertPodHasNoModelSecret(pod, "service portal credential sample-runner task")
								harness.AssertPodHasPlaceholderModelCredentials(pod, "service portal credential sample-runner task")
							}
						}
					}
				}
				if currentRun.Status.Phase == v1alpha1.RunPhaseCompleted || currentRun.Status.Phase == v1alpha1.RunPhaseFailed {
					completedRun = &currentRun
					break
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if completedRun == nil {
		harness.logTimeoutDiagnostics("eval-runner", runName)
		t.Fatalf("timeout waiting for Run %s to finish", runName)
	}
	if !podObserved {
		t.Fatalf("expected to observe running sample-runner pod for Run %s", runName)
	}
	if completedRun.Status.Phase != v1alpha1.RunPhaseCompleted {
		harness.logTimeoutDiagnostics("eval-runner", runName)
		t.Fatalf("expected Run %s to complete, got phase %s (message: %s, tasks: %+v)",
			runName, completedRun.Status.Phase, completedRun.Status.Message, completedRun.Status.Tasks)
	}
	if len(completedRun.Status.Tasks) != 1 || completedRun.Status.Tasks[0].Phase != v1alpha1.TaskPhaseCompleted {
		t.Fatalf("expected 1 completed task, got %+v", completedRun.Status.Tasks)
	}

	// Independently verify all three request outcomes recorded by the in-cluster stand-in.
	recordsJSON := harness.runCmd(
		"kubectl", "exec", "-n", "eval-runner-sandbox", "deployment/model-standin", "--",
		"python3", "-c", "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:8080/records').read().decode())",
	)
	var records []modelStandinRecord
	if err := json.Unmarshal([]byte(recordsJSON), &records); err != nil {
		t.Fatalf("failed to decode stand-in records %q: %v", recordsJSON, err)
	}
	var sawModelCallWithRealKey, sawModelHostWithoutHeader, sawOtherHostWithoutSwap bool
	for _, record := range records {
		if record.Method == "POST" && record.Host == standinHost && record.HadHeader && record.MatchedExpectedKey && !record.ReceivedPlaceholder {
			sawModelCallWithRealKey = true
		}
		if record.Method == "GET" && record.Host == standinHost && record.Path == "/probe" && !record.HadHeader && !record.MatchedExpectedKey {
			sawModelHostWithoutHeader = true
		}
		if record.Method == "GET" && record.Host == otherHost && record.Path == "/probe" && record.HadHeader && record.ReceivedPlaceholder && !record.MatchedExpectedKey {
			sawOtherHostWithoutSwap = true
		}
	}
	if !sawModelCallWithRealKey {
		t.Errorf("expected stand-in to receive model POST with real key injected; records=%+v", records)
	}
	if !sawModelHostWithoutHeader {
		t.Errorf("expected stand-in to receive headerless GET to model host without credential; records=%+v", records)
	}
	if !sawOtherHostWithoutSwap {
		t.Errorf("expected stand-in to receive GET to other host without credential swap; records=%+v", records)
	}

	// Verify the real key never appears in controller logs, Run status, or Kubernetes Events.
	controllerLogs := harness.runCmd("kubectl", "logs", "-n", "eval-runner", "deployment/eval-runner-controller")
	if strings.Contains(controllerLogs, expectedRealKey) {
		t.Errorf("real model API key leaked into eval-runner-controller logs")
	}
	runJSON, _ := json.Marshal(completedRun)
	if strings.Contains(string(runJSON), expectedRealKey) {
		t.Errorf("real model API key leaked into Run resource status")
	}
	eventsOutput := harness.runCmd("kubectl", "get", "events", "-A", "-o", "json")
	if strings.Contains(eventsOutput, expectedRealKey) {
		t.Errorf("real model API key leaked into Kubernetes Events")
	}
}
