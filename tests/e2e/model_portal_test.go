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
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

// TestModelPortalManifestConfiguration verifies that k8s/model-portal.yaml declares
// the model-portal Deployment and Service in explicit service mode:
// - Service model-portal in eval-runner routes port 80 to container port 8080.
// - Deployment model-portal runs a single replica with restricted-profile security context.
// - Upstream configuration points to the Gemini OpenAI-compatible endpoint (TARGET_URL).
// - Upstream authentication header is Authorization (which injects Bearer <token>).
// - Upstream authentication token is optionally loaded from Secret model-portal-secret (key token).
// - Readiness probe verifies TCP port 8080.
//
// Note: As specified in acceptance criterion 2, service-portals does not provide a built-in
// mock or echo upstream mode; therefore, this check asserts on declarative configuration.
func TestModelPortalManifestConfiguration(t *testing.T) {
	root := findRepoRoot(t)
	manifestPath := filepath.Join(root, "k8s", "model-portal.yaml")

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", manifestPath, err)
	}

	yamlReader := yaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	var deployment *appsv1.Deployment
	var service *corev1.Service

	for {
		doc, err := yamlReader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("failed to read yaml doc from %s: %v", manifestPath, err)
		}

		var typeMeta metav1.TypeMeta
		if err := sigsyaml.Unmarshal(doc, &typeMeta); err != nil {
			continue
		}

		switch typeMeta.Kind {
		case "Deployment":
			var parsedDeployment appsv1.Deployment
			if err := sigsyaml.Unmarshal(doc, &parsedDeployment); err != nil {
				t.Fatalf("failed to unmarshal Deployment: %v", err)
			}
			deployment = &parsedDeployment
		case "Service":
			var parsedService corev1.Service
			if err := sigsyaml.Unmarshal(doc, &parsedService); err != nil {
				t.Fatalf("failed to unmarshal Service: %v", err)
			}
			service = &parsedService
		}
	}

	if deployment == nil {
		t.Fatalf("expected Deployment in %s, got none", manifestPath)
	}
	if service == nil {
		t.Fatalf("expected Service in %s, got none", manifestPath)
	}

	// 1. Verify Service definition
	if service.Name != "model-portal" {
		t.Errorf("expected Service name 'model-portal', got %q", service.Name)
	}
	if service.Namespace != "eval-runner" {
		t.Errorf("expected Service namespace 'eval-runner', got %q", service.Namespace)
	}
	if service.Spec.Selector["app"] != "model-portal" {
		t.Errorf("expected Service selector app=model-portal, got %v", service.Spec.Selector)
	}
	var foundPort80 bool
	for _, port := range service.Spec.Ports {
		if port.Port == 80 && port.TargetPort.IntVal == 8080 {
			foundPort80 = true
			break
		}
	}
	if !foundPort80 {
		t.Errorf("expected Service port 80 -> targetPort 8080, got %v", service.Spec.Ports)
	}

	// 2. Verify Deployment definition
	if deployment.Name != "model-portal" {
		t.Errorf("expected Deployment name 'model-portal', got %q", deployment.Name)
	}
	if deployment.Namespace != "eval-runner" {
		t.Errorf("expected Deployment namespace 'eval-runner', got %q", deployment.Namespace)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Errorf("expected replicas=1, got %v", deployment.Spec.Replicas)
	}
	if deployment.Spec.Selector == nil || deployment.Spec.Selector.MatchLabels["app"] != "model-portal" {
		t.Errorf("expected selector matchLabels app=model-portal, got %v", deployment.Spec.Selector)
	}

	// 3. Verify Pod security context
	podSpec := deployment.Spec.Template.Spec
	if podSpec.SecurityContext == nil {
		t.Fatalf("expected Pod securityContext to be set")
	}
	if podSpec.SecurityContext.RunAsNonRoot == nil || !*podSpec.SecurityContext.RunAsNonRoot {
		t.Errorf("expected Pod runAsNonRoot=true")
	}
	if podSpec.SecurityContext.SeccompProfile == nil || podSpec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("expected Pod seccompProfile type RuntimeDefault, got %v", podSpec.SecurityContext.SeccompProfile)
	}

	// 4. Verify Container spec
	if len(podSpec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(podSpec.Containers))
	}
	container := podSpec.Containers[0]
	if container.Name != "model-portal" {
		t.Errorf("expected container name 'model-portal', got %q", container.Name)
	}
	if container.Image != "service-portal" {
		t.Errorf("expected container image 'service-portal', got %q", container.Image)
	}

	// Container security context
	if container.SecurityContext == nil {
		t.Fatalf("expected container securityContext to be set")
	}
	if container.SecurityContext.AllowPrivilegeEscalation == nil || *container.SecurityContext.AllowPrivilegeEscalation {
		t.Errorf("expected allowPrivilegeEscalation=false")
	}
	if container.SecurityContext.ReadOnlyRootFilesystem == nil || !*container.SecurityContext.ReadOnlyRootFilesystem {
		t.Errorf("expected readOnlyRootFilesystem=true")
	}
	if container.SecurityContext.RunAsNonRoot == nil || !*container.SecurityContext.RunAsNonRoot {
		t.Errorf("expected runAsNonRoot=true")
	}
	if container.SecurityContext.Capabilities == nil || len(container.SecurityContext.Capabilities.Drop) == 0 || container.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Errorf("expected capabilities drop ALL, got %v", container.SecurityContext.Capabilities)
	}

	// Container ports
	var foundPort8080 bool
	for _, port := range container.Ports {
		if port.ContainerPort == 8080 {
			foundPort8080 = true
			break
		}
	}
	if !foundPort8080 {
		t.Errorf("expected containerPort 8080, got %v", container.Ports)
	}

	// Environment variables
	envMap := make(map[string]corev1.EnvVar)
	for _, env := range container.Env {
		envMap[env.Name] = env
	}

	targetURL, ok := envMap["TARGET_URL"]
	if !ok || targetURL.Value != "https://generativelanguage.googleapis.com" {
		t.Errorf("expected TARGET_URL='https://generativelanguage.googleapis.com', got %v", targetURL)
	}

	authHeader, ok := envMap["UPSTREAM_AUTH_HEADER"]
	if !ok || authHeader.Value != "Authorization" {
		t.Errorf("expected UPSTREAM_AUTH_HEADER='Authorization', got %v", authHeader)
	}

	authToken, ok := envMap["UPSTREAM_AUTH_TOKEN"]
	if !ok || authToken.ValueFrom == nil || authToken.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("expected UPSTREAM_AUTH_TOKEN from secretKeyRef, got %v", authToken)
	}
	secretReference := authToken.ValueFrom.SecretKeyRef
	if secretReference.Name != "model-portal-secret" || secretReference.Key != "token" {
		t.Errorf("expected secretKeyRef model-portal-secret/token, got %s/%s", secretReference.Name, secretReference.Key)
	}
	if secretReference.Optional == nil || !*secretReference.Optional {
		t.Errorf("expected secretKeyRef optional=true, got %v", secretReference.Optional)
	}

	// Readiness probe
	if container.ReadinessProbe == nil || container.ReadinessProbe.TCPSocket == nil || container.ReadinessProbe.TCPSocket.Port.IntVal != 8080 {
		t.Errorf("expected tcpSocket readiness probe on port 8080, got %v", container.ReadinessProbe)
	}
}

// TestServicePortalDockerfile verifies that images/service-portal/Dockerfile cross-compiles
// using --platform=$BUILDPLATFORM and pins the SERVICE_PORTALS_COMMIT.
func TestServicePortalDockerfile(t *testing.T) {
	root := findRepoRoot(t)
	dockerfilePath := filepath.Join(root, "images", "service-portal", "Dockerfile")

	content, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", dockerfilePath, err)
	}
	text := string(content)

	if !strings.Contains(text, "FROM --platform=$BUILDPLATFORM") {
		t.Errorf("Dockerfile missing 'FROM --platform=$BUILDPLATFORM' for cross-compilation without emulation")
	}

	if !strings.Contains(text, ServicePortalsCommit) {
		t.Errorf("Dockerfile missing pinned ServicePortalsCommit %s", ServicePortalsCommit)
	}

	if !strings.Contains(text, "github.com/gke-labs/service-portals/cmd/service-portal@") {
		t.Errorf("Dockerfile missing go install cmd/service-portal invocation")
	}
}
