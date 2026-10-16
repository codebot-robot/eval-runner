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
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	AgentSandboxVersion      = "v1.0.7"
	AgentSandboxURL          = "https://github.com/kubernetes-sigs/agent-sandbox/releases/download/" + AgentSandboxVersion + "/sandbox.yaml"
	AgentSandboxSandboxImage = "alpine/git:v2.47.2"
)

type Harness struct {
	t           *testing.T
	clusterName string
}

func NewHarness(t *testing.T, clusterName string) *Harness {
	return &Harness{
		t:           t,
		clusterName: clusterName,
	}
}

func (h *Harness) Setup() {
	h.t.Logf("Setting up harness for cluster %s", h.clusterName)

	// Ensure .build/bin is checked if tools are missing from PATH
	gitRoot := h.GetGitRoot()
	buildBin := filepath.Join(gitRoot, ".build", "bin")
	currentPath := os.Getenv("PATH")
	if !strings.Contains(currentPath, buildBin) {
		if info, err := os.Stat(buildBin); err != nil {
			if !os.IsNotExist(err) {
				h.t.Logf("Checking %s: %v", buildBin, err)
			}
		} else if info.IsDir() {
			if err := os.Setenv("PATH", buildBin+string(os.PathListSeparator)+currentPath); err != nil {
				h.t.Fatalf("Failed to update PATH: %v", err)
			}
		}
	}

	if _, err := exec.LookPath("kind"); err != nil {
		h.t.Fatalf("kind not found on PATH: %v (run ./dev/tasks/test-e2e to install)", err)
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		h.t.Fatalf("kubectl not found on PATH: %v (run ./dev/tasks/test-e2e to install)", err)
	}

	// Create kind cluster if it doesn't exist
	clusters := h.runCmd("kind", "get", "clusters")
	exists := false
	for _, cluster := range strings.Split(clusters, "\n") {
		if strings.TrimSpace(cluster) == h.clusterName {
			exists = true
			break
		}
	}

	if !exists {
		h.t.Logf("Creating kind cluster %s", h.clusterName)
		h.runCmd("kind", "create", "cluster", "--name", h.clusterName)
		h.t.Cleanup(func() {
			if os.Getenv("SKIP_CLEANUP") == "" {
				h.t.Logf("Deleting kind cluster %s", h.clusterName)
				h.runCmd("kind", "delete", "cluster", "--name", h.clusterName)
			} else {
				h.t.Logf("Skipping deletion of kind cluster %s (SKIP_CLEANUP set)", h.clusterName)
			}
		})
	} else {
		h.t.Logf("Reusing existing kind cluster %s", h.clusterName)
	}

	// Switch context to the target cluster
	contextName := "kind-" + h.clusterName
	h.runCmd("kubectl", "config", "use-context", contextName)

	h.InstallAgentSandbox()
}

func (h *Harness) InstallAgentSandbox() {
	h.t.Logf("Installing agent-sandbox %s", AgentSandboxVersion)
	h.KubectlApplyFileWithRetry(AgentSandboxURL, 60*time.Second)
	h.runCmd("kubectl", "wait", "--namespace", "agent-sandbox-system", "--for=condition=available", "deployment/agent-sandbox-controller", "--timeout=120s")
}

func (h *Harness) CreateNamespace() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		h.t.Fatalf("Failed to generate random namespace suffix: %v", err)
	}
	ns := fmt.Sprintf("test-eval-%x", b)
	h.t.Logf("Creating test namespace %s", ns)
	h.runCmd("kubectl", "create", "namespace", ns)
	h.t.Cleanup(func() {
		if os.Getenv("SKIP_CLEANUP") == "" {
			h.t.Logf("Cleaning up test namespace %s", ns)
			h.runCmd("kubectl", "delete", "namespace", ns, "--ignore-not-found")
		} else {
			h.t.Logf("Skipping cleanup of test namespace %s (SKIP_CLEANUP set)", ns)
		}
	})
	return ns
}

func (h *Harness) GetGitRoot() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		h.t.Fatalf("Failed to get git root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func (h *Harness) DockerBuild(tag, dockerfile, context string) {
	h.t.Logf("Building docker image %s", tag)
	h.runCmd("docker", "build", "-t", tag, "-f", dockerfile, context)
}

func (h *Harness) KindLoad(tag string) {
	h.t.Logf("Loading image %s into kind cluster %s", tag, h.clusterName)
	h.runCmd("kind", "load", "docker-image", tag, "--name", h.clusterName)
}

func (h *Harness) KubectlApplyContent(content string) {
	h.t.Logf("Applying kubectl content:\n%s", content)
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(content)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		h.t.Fatalf("kubectl apply failed: %v\nStderr: %s", err, stderr.String())
	}
}

func (h *Harness) KubectlApplyFileWithRetry(path string, timeout time.Duration) {
	h.t.Logf("Applying kubectl file (with retry): %s", path)
	start := time.Now()
	var lastErr error
	var lastStderr string
	for {
		cmd := exec.Command("kubectl", "apply", "-f", path)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err == nil {
			return
		} else {
			lastErr = err
			lastStderr = stderr.String()
		}

		if time.Since(start) > timeout {
			h.t.Fatalf("Timeout waiting for kubectl apply %s to succeed: %v\nStderr: %s", path, lastErr, lastStderr)
		}
		time.Sleep(2 * time.Second)
	}
}

func (h *Harness) DeployGitServer(namespace string) {
	h.t.Log("Building and deploying git server")
	gitRoot := h.GetGitRoot()
	h.DockerBuild("git-server:e2e", filepath.Join(gitRoot, "images/git-server/Dockerfile"), gitRoot)
	h.KindLoad("git-server:e2e")

	manifest := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: git-server
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: git-server
  template:
    metadata:
      labels:
        app: git-server
    spec:
      containers:
      - name: git-server
        image: git-server:e2e
        imagePullPolicy: Never
        ports:
        - containerPort: 9418
---
apiVersion: v1
kind: Service
metadata:
  name: git-server
  namespace: %s
spec:
  selector:
    app: git-server
  ports:
  - port: 9418
    targetPort: 9418
`, namespace, namespace)

	h.KubectlApplyContent(manifest)
	h.runCmd("kubectl", "wait", "--namespace", namespace, "--for=condition=available", "deployment/git-server", "--timeout=90s")
}

func (h *Harness) DeploySandbox(namespace, name string) {
	h.t.Logf("Deploying sandbox %s in namespace %s", name, namespace)

	manifest := fmt.Sprintf(`
apiVersion: agents.x-k8s.io/v1beta1
kind: Sandbox
metadata:
  name: %s
  namespace: %s
spec:
  podTemplate:
    spec:
      containers:
      - name: agent
        image: %s
        command: ["sh", "-c", "sleep 3600"]
      restartPolicy: Never
`, name, namespace, AgentSandboxSandboxImage)

	h.KubectlApplyContent(manifest)
	// Wait for the Sandbox condition to report Ready first
	h.runCmd("kubectl", "wait", "--namespace", namespace, "--for=condition=Ready", "sandbox/"+name, "--timeout=120s")
	// Also wait for the backing pod to be Ready
	h.runCmd("kubectl", "wait", "--namespace", namespace, "--for=condition=Ready", "pod/"+name, "--timeout=30s")
}

func (h *Harness) ExecInPodWithRetry(namespace, podName string, timeout time.Duration, args ...string) string {
	h.t.Logf("Executing command in pod %s/%s (with retry up to %v): %v", namespace, podName, timeout, args)
	start := time.Now()
	var lastErr error
	var lastStderr string
	var lastStdout string

	cmdArgs := append([]string{"exec", "--namespace", namespace, podName, "--"}, args...)

	for {
		cmd := exec.Command("kubectl", cmdArgs...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err == nil {
			return stdout.String()
		}
		lastErr = err
		lastStdout = stdout.String()
		lastStderr = stderr.String()

		if time.Since(start) > timeout {
			h.t.Fatalf("Timeout waiting for exec command in %s/%s to succeed: %v\nStdout: %s\nStderr: %s", namespace, podName, lastErr, lastStdout, lastStderr)
		}
		time.Sleep(2 * time.Second)
	}
}

func (h *Harness) runCmd(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		h.t.Fatalf("Command %s %v failed: %v\nStdout: %s\nStderr: %s", name, args, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}
