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
)

type echoServerResponseBody struct {
	Request struct {
		Headers map[string]string `json:"headers"`
	} `json:"request"`
}

func TestPortalNodeAgentInterception(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("skipping e2e test: RUN_E2E is not set")
	}

	clusterName := os.Getenv("KIND_CLUSTER_NAME")
	if clusterName == "" {
		clusterName = "kind"
	}

	harness := NewHarness(t, clusterName)
	harness.Setup()

	// Ensure portal-node-agent is ready; deploy the stack if not already deployed.
	daemonSetCheckCommand := exec.Command("kubectl", "-n", "service-portal-system", "rollout", "status", "daemonset/portal-node-agent", "--timeout=5s")
	if daemonSetCheckError := daemonSetCheckCommand.Run(); daemonSetCheckError != nil {
		harness.DeployEvalRunner()
	}

	namespace := harness.CreateNamespace()

	// 1. Deploy an in-cluster HTTP echo backend (Deployment + Service returning request headers).
	echoBackendManifest := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo-backend
  namespace: %[1]s
  labels:
    app: echo-backend
spec:
  replicas: 1
  selector:
    matchLabels:
      app: echo-backend
  template:
    metadata:
      labels:
        app: echo-backend
    spec:
      containers:
      - name: echo-server
        image: ealen/echo-server:latest
        imagePullPolicy: IfNotPresent
        ports:
        - containerPort: 80
---
apiVersion: v1
kind: Service
metadata:
  name: echo-backend
  namespace: %[1]s
spec:
  selector:
    app: echo-backend
  ports:
  - name: http
    port: 80
    targetPort: 80
`, namespace)
	harness.KubectlApplyContent(echoBackendManifest)
	harness.runCmd("kubectl", "wait", "--namespace", namespace, "--for=condition=available", "deployment/echo-backend", "--timeout=90s")

	// 2. Provide rules to node agent: PortalRule for backend Service hostname with authHeader and test authToken.
	const testAuthToken = "test-injected-auth-token"
	const placeholderAuthToken = "placeholder-unauthenticated-token"
	backendServiceHostname := fmt.Sprintf("echo-backend.%s.svc", namespace)

	portalRulesSecretManifest := fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: portal-rules
  namespace: service-portal-system
stringData:
  echo-backend-rule.yaml: |
    apiVersion: portals.gke.io/v1alpha1
    kind: PortalRule
    metadata:
      name: echo-backend
    spec:
      host: %[1]s
      rewriteUrl: http://%[1]s
      authHeader: x-goog-api-key
      authToken: %[2]s
`, backendServiceHostname, testAuthToken)
	harness.KubectlApplyContent(portalRulesSecretManifest)

	// Clean up the portal-rules Secret and restore the DaemonSet when the test finishes.
	t.Cleanup(func() {
		harness.runCmd("kubectl", "-n", "service-portal-system", "delete", "secret", "portal-rules", "--ignore-not-found")
		harness.runCmd("kubectl", "-n", "service-portal-system", "rollout", "restart", "daemonset/portal-node-agent")
		harness.runCmd("kubectl", "-n", "service-portal-system", "rollout", "status", "daemonset/portal-node-agent", "--timeout=90s")
	})

	// Restart the DaemonSet so the proxy reloads the newly mounted portal-rules Secret.
	harness.runCmd("kubectl", "-n", "service-portal-system", "rollout", "restart", "daemonset/portal-node-agent")
	harness.runCmd("kubectl", "-n", "service-portal-system", "rollout", "status", "daemonset/portal-node-agent", "--timeout=90s")

	// 3. Start client pod with label service-portal.gke.io/intercept: "true" and annotation service-portal.gke.io/policy: "*".
	interceptedClientPodManifest := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: client-intercepted
  namespace: %[1]s
  labels:
    app: client-intercepted
    service-portal.gke.io/intercept: "true"
  annotations:
    service-portal.gke.io/policy: "*"
spec:
  containers:
  - name: curl
    image: curlimages/curl:8.12.1
    imagePullPolicy: IfNotPresent
    command: ["sleep", "3600"]
`, namespace)
	harness.KubectlApplyContent(interceptedClientPodManifest)
	harness.runCmd("kubectl", "wait", "--namespace", namespace, "--for=condition=ready", "pod/client-intercepted", "--timeout=90s")

	// 4(a). Assert the backend saw the test token, not the placeholder.
	backendURL := fmt.Sprintf("http://%s", backendServiceHostname)
	interceptedCurlOutput := harness.runCmd("kubectl", "exec", "-n", namespace, "client-intercepted", "--",
		"curl", "-s", "-H", fmt.Sprintf("x-goog-api-key: %s", placeholderAuthToken), backendURL)

	var interceptedResponseBody echoServerResponseBody
	if err := json.Unmarshal([]byte(interceptedCurlOutput), &interceptedResponseBody); err != nil {
		t.Fatalf("failed to decode echo backend JSON response: %v\nOutput was: %s", err, interceptedCurlOutput)
	}

	receivedHeaderValue := interceptedResponseBody.Request.Headers["x-goog-api-key"]
	if receivedHeaderValue != testAuthToken {
		t.Fatalf("expected backend to receive test token %q, but got %q", testAuthToken, receivedHeaderValue)
	}
	if receivedHeaderValue == placeholderAuthToken {
		t.Fatalf("backend received placeholder token %q: credential was not injected by node proxy", placeholderAuthToken)
	}

	// 4(b). Assert a request to a host with no rule fails (DNS NXDOMAIN or HTTP 403 Forbidden).
	unlistedHostCommand := exec.Command("kubectl", "exec", "-n", namespace, "client-intercepted", "--",
		"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-m", "5", "http://unlisted-host-without-rule.invalid")
	unlistedHostOutput, unlistedHostError := unlistedHostCommand.CombinedOutput()
	unlistedHttpStatus := strings.TrimSpace(string(unlistedHostOutput))
	if unlistedHostError == nil && unlistedHttpStatus != "403" {
		t.Fatalf("expected request to unlisted host to fail with DNS error or 403 Forbidden, but succeeded with status code: %s", unlistedHttpStatus)
	}

	// 4(c). Assert a client pod without the label reaches the backend directly and sees the placeholder.
	unlabeledClientPodManifest := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: client-unlabeled
  namespace: %[1]s
  labels:
    app: client-unlabeled
spec:
  containers:
  - name: curl
    image: curlimages/curl:8.12.1
    imagePullPolicy: IfNotPresent
    command: ["sleep", "3600"]
`, namespace)
	harness.KubectlApplyContent(unlabeledClientPodManifest)
	harness.runCmd("kubectl", "wait", "--namespace", namespace, "--for=condition=ready", "pod/client-unlabeled", "--timeout=90s")

	unlabeledCurlOutput := harness.runCmd("kubectl", "exec", "-n", namespace, "client-unlabeled", "--",
		"curl", "-s", "-H", fmt.Sprintf("x-goog-api-key: %s", placeholderAuthToken), backendURL)

	var unlabeledResponseBody echoServerResponseBody
	if err := json.Unmarshal([]byte(unlabeledCurlOutput), &unlabeledResponseBody); err != nil {
		t.Fatalf("failed to decode echo backend JSON response from unlabeled client: %v\nOutput was: %s", err, unlabeledCurlOutput)
	}

	unlabeledHeaderValue := unlabeledResponseBody.Request.Headers["x-goog-api-key"]
	if unlabeledHeaderValue != placeholderAuthToken {
		t.Fatalf("expected unlabeled client request to preserve placeholder token %q, but got %q", placeholderAuthToken, unlabeledHeaderValue)
	}
}
