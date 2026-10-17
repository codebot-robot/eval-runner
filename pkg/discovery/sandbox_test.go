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

package discovery

import (
	"strings"
	"testing"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestSandboxNameForRun(t *testing.T) {
	tests := []struct {
		runName  string
		expected string
	}{
		{
			runName:  "my-run",
			expected: "discovery-my-run",
		},
		{
			runName:  "support-agent-abc123",
			expected: "discovery-support-agent-abc123",
		},
	}

	for _, tc := range tests {
		got := SandboxNameForRun(tc.runName)
		if got != tc.expected {
			t.Errorf("SandboxNameForRun(%q) = %q; want %q", tc.runName, got, tc.expected)
		}
		if len(got) > 63 {
			t.Errorf("SandboxNameForRun(%q) length %d > 63", tc.runName, len(got))
		}
	}

	// Long run name test
	longRunName := strings.Repeat("long-run-name-test-part", 5) // > 100 chars
	gotLong := SandboxNameForRun(longRunName)
	if len(gotLong) > 63 {
		t.Errorf("SandboxNameForRun for long name exceeded 63 chars: length = %d", len(gotLong))
	}
	if !strings.HasPrefix(gotLong, "discovery-") {
		t.Errorf("SandboxNameForRun for long name missing prefix: %s", gotLong)
	}
}

func TestBuildSandbox(t *testing.T) {
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run-123",
			Namespace: "eval-runner",
			UID:       types.UID("1111-2222-3333"),
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "git://git-server/repo.git",
				Commit: "abc1234",
			},
		},
	}

	sb := BuildSandbox(run, "eval-runner-sandbox", AuthTokenDigest("test"))

	if sb.Name != "discovery-test-run-123" {
		t.Errorf("Unexpected sandbox name: %s", sb.Name)
	}
	if sb.Namespace != "eval-runner-sandbox" {
		t.Errorf("Unexpected sandbox namespace: %s", sb.Namespace)
	}

	// Labels
	if sb.Labels[LabelRunName] != run.Name {
		t.Errorf("Missing/incorrect %s label: %s", LabelRunName, sb.Labels[LabelRunName])
	}
	if sb.Labels[LabelRunNamespace] != run.Namespace {
		t.Errorf("Missing/incorrect %s label: %s", LabelRunNamespace, sb.Labels[LabelRunNamespace])
	}
	if sb.Labels[LabelRunUID] != string(run.UID) {
		t.Errorf("Missing/incorrect %s label: %s", LabelRunUID, sb.Labels[LabelRunUID])
	}
	if sb.Labels[LabelRole] != "discovery" {
		t.Errorf("Missing/incorrect %s label: %s", LabelRole, sb.Labels[LabelRole])
	}
	if sb.Labels[LabelSandbox] != "true" {
		t.Errorf("Missing/incorrect %s label: %s", LabelSandbox, sb.Labels[LabelSandbox])
	}
	if sb.Spec.PodTemplate.ObjectMeta.Labels[LabelSandbox] != "true" {
		t.Errorf("Missing/incorrect pod template %s label: %s", LabelSandbox, sb.Spec.PodTemplate.ObjectMeta.Labels[LabelSandbox])
	}

	// Zero-privilege checks (concept §3.2)
	podSpec := sb.Spec.PodTemplate.Spec
	if podSpec.AutomountServiceAccountToken == nil || *podSpec.AutomountServiceAccountToken != false {
		t.Errorf("AutomountServiceAccountToken must be false, got: %v", podSpec.AutomountServiceAccountToken)
	}

	if podSpec.SecurityContext == nil {
		t.Fatalf("Pod SecurityContext is nil")
	}
	if podSpec.SecurityContext.RunAsNonRoot == nil || !*podSpec.SecurityContext.RunAsNonRoot {
		t.Errorf("RunAsNonRoot must be true")
	}
	if podSpec.SecurityContext.RunAsUser == nil || *podSpec.SecurityContext.RunAsUser == 0 {
		t.Errorf("RunAsUser must be non-zero (non-root)")
	}
	if podSpec.SecurityContext.SeccompProfile == nil || podSpec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("Pod SeccompProfile must be RuntimeDefault, got: %v", podSpec.SecurityContext.SeccompProfile)
	}

	// InitContainers check (topology B injection)
	if len(podSpec.InitContainers) != 1 {
		t.Fatalf("Expected 1 initContainer, got %d", len(podSpec.InitContainers))
	}
	initC := podSpec.InitContainers[0]
	if initC.Name != SandboxdInitContainerName {
		t.Errorf("Expected initContainer name %s, got %s", SandboxdInitContainerName, initC.Name)
	}
	if initC.Image != SandboxdImage {
		t.Errorf("Expected sandboxd image %s, got %s", SandboxdImage, initC.Image)
	}
	if initC.SecurityContext == nil || initC.SecurityContext.RunAsNonRoot == nil || !*initC.SecurityContext.RunAsNonRoot {
		t.Errorf("InitContainer must run as non-root")
	}

	// App container check
	if len(podSpec.Containers) != 1 {
		t.Fatalf("Expected 1 container, got %d", len(podSpec.Containers))
	}
	c := podSpec.Containers[0]
	if c.Image != DiscoveryImage {
		t.Errorf("Expected pinned image %s, got %s", DiscoveryImage, c.Image)
	}

	if c.SecurityContext == nil {
		t.Fatalf("Container SecurityContext is nil")
	}
	if c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Errorf("AllowPrivilegeEscalation must be false")
	}
	if c.SecurityContext.RunAsNonRoot == nil || !*c.SecurityContext.RunAsNonRoot {
		t.Errorf("Container RunAsNonRoot must be true")
	}
	if c.SecurityContext.Capabilities == nil || len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Errorf("Expected Drop ALL capabilities, got: %v", c.SecurityContext.Capabilities)
	}
	if c.SecurityContext.SeccompProfile == nil || c.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("Container SeccompProfile must be RuntimeDefault, got: %v", c.SecurityContext.SeccompProfile)
	}

	// Command should run sandboxd
	if len(c.Command) == 0 || !strings.Contains(c.Command[0], "sandboxd") {
		t.Errorf("Expected container command to run sandboxd, got: %v", c.Command)
	}

	// Ports: 8080 (REST) and 9090 (gRPC)
	if len(c.Ports) != 2 {
		t.Fatalf("Expected 2 container ports, got %d", len(c.Ports))
	}
	var has8080, has9090 bool
	for _, p := range c.Ports {
		if p.ContainerPort == 8080 {
			has8080 = true
		}
		if p.ContainerPort == 9090 {
			has9090 = true
		}
	}
	if !has8080 || !has9090 {
		t.Errorf("Expected ports 8080 and 9090, got: %v", c.Ports)
	}

	// Readiness probe
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil {
		t.Fatalf("Expected HTTPGet readiness probe")
	}
	if c.ReadinessProbe.HTTPGet.Path != "/v1/health" || c.ReadinessProbe.HTTPGet.Port.IntValue() != 8080 {
		t.Errorf("Expected readiness probe GET /v1/health on 8080, got: %+v", c.ReadinessProbe.HTTPGet)
	}

	// Resources checks
	if c.Resources.Requests.Cpu().IsZero() || c.Resources.Requests.Memory().IsZero() {
		t.Errorf("Resource requests must be set, got: %+v", c.Resources.Requests)
	}
	if c.Resources.Limits.Cpu().IsZero() || c.Resources.Limits.Memory().IsZero() {
		t.Errorf("Resource limits must be set, got: %+v", c.Resources.Limits)
	}

	// Volumes
	if len(podSpec.Volumes) != 2 {
		t.Fatalf("Expected 2 volumes, got %d", len(podSpec.Volumes))
	}
}

func TestBuildSandbox_WithAuthToken(t *testing.T) {
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run-auth",
			Namespace: "eval-runner",
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL: "git://git-server/repo.git",
			},
		},
	}

	token := "test-secret-token-12345"
	digest := AuthTokenDigest(token)
	if digest == "" || len(digest) != 64 {
		t.Fatalf("Expected 64-char hex digest, got %q", digest)
	}

	sb := BuildSandbox(run, "eval-runner-sandbox", digest)
	containers := sb.Spec.PodTemplate.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("Expected 1 container, got %d", len(containers))
	}

	cmd := containers[0].Command
	expectedFlag := "--auth-token-sha256=" + digest
	var found bool
	for _, arg := range cmd {
		if arg == expectedFlag {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected command to contain %q, got: %v", expectedFlag, cmd)
	}
}

func TestAuthSecretNameForRun(t *testing.T) {
	name := AuthSecretNameForRun("short-name")
	if name != "discovery-auth-short-name" {
		t.Errorf("Unexpected secret name: %s", name)
	}

	longName := strings.Repeat("a", 100)
	gotLong := AuthSecretNameForRun(longName)
	if len(gotLong) > 63 {
		t.Errorf("Secret name exceeds 63 characters: %d (%s)", len(gotLong), gotLong)
	}
	if !strings.HasPrefix(gotLong, "discovery-auth-") {
		t.Errorf("Secret name missing prefix: %s", gotLong)
	}
}

func TestBuildSandbox_EmptyAuthTokenPanics(t *testing.T) {
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run-empty-token",
			Namespace: "eval-runner",
		},
	}
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("Expected BuildSandbox to panic on empty authTokenDigest")
		}
	}()
	BuildSandbox(run, "eval-runner-sandbox", "")
}
