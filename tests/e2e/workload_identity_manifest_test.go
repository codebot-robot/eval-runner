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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

const (
	expectedWorkloadIdentityAudience   = "eval-runner.gke-labs.dev/gcp-workload-identity"
	expectedWorkloadIdentityPath       = "token"
	expectedWorkloadIdentityExpiration = int64(3600)
	expectedWorkloadIdentityTokenMount = "/var/run/secrets/eval-runner/gcp"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
}

func loadDeploymentsFromManifest(t *testing.T, path string) []*appsv1.Deployment {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	yamlReader := yaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	var deployments []*appsv1.Deployment
	for {
		doc, err := yamlReader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("failed to read yaml doc from %s: %v", path, err)
		}

		var typeMeta metav1.TypeMeta
		if err := sigsyaml.Unmarshal(doc, &typeMeta); err != nil {
			continue
		}
		if typeMeta.Kind != "Deployment" {
			continue
		}

		var deploy appsv1.Deployment
		if err := sigsyaml.Unmarshal(doc, &deploy); err != nil {
			t.Fatalf("failed to unmarshal Deployment from %s: %v", path, err)
		}
		deployments = append(deployments, &deploy)
	}
	return deployments
}

// TestDeploymentManifests_GCPWorkloadIdentity asserts that controller and UI
// deployments declare the projected token volume and volume mount on the controller
// and UI containers, and that no credentials ConfigMap volume, mount, or
// GOOGLE_APPLICATION_CREDENTIALS env var exists.
func TestDeploymentManifests_GCPWorkloadIdentity(t *testing.T) {
	root := findRepoRoot(t)

	tests := []struct {
		manifestPath       string
		deploymentName     string
		targetContainer    string
		expectWorkloadAuth bool
	}{
		{
			manifestPath:       filepath.Join(root, "k8s", "runner-deployment.yaml"),
			deploymentName:     "eval-runner-controller",
			targetContainer:    "controller",
			expectWorkloadAuth: true,
		},
		{
			manifestPath:       filepath.Join(root, "k8s", "ui-deployment.yaml"),
			deploymentName:     "eval-runner-ui",
			targetContainer:    "ui",
			expectWorkloadAuth: true,
		},
		{
			manifestPath:       filepath.Join(root, "k8s", "webhook-deployment.yaml"),
			deploymentName:     "eval-runner-webhook",
			targetContainer:    "webhook",
			expectWorkloadAuth: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.deploymentName, func(t *testing.T) {
			deployments := loadDeploymentsFromManifest(t, tc.manifestPath)
			if len(deployments) != 1 {
				t.Fatalf("expected 1 Deployment in %s, got %d", tc.manifestPath, len(deployments))
			}
			deploy := deployments[0]
			if deploy.Name != tc.deploymentName {
				t.Fatalf("expected Deployment name %q, got %q", tc.deploymentName, deploy.Name)
			}

			podSpec := deploy.Spec.Template.Spec

			// 1. Locate projected token volume and ensure no credentials configmap volume exists
			var tokenVolumeName string

			for _, vol := range podSpec.Volumes {
				if vol.Projected != nil {
					for _, src := range vol.Projected.Sources {
						if src.ServiceAccountToken != nil {
							tokenVolumeName = vol.Name
							if src.ServiceAccountToken.Audience != expectedWorkloadIdentityAudience {
								t.Errorf("volume %q projected serviceAccountToken audience = %q, want %q",
									vol.Name, src.ServiceAccountToken.Audience, expectedWorkloadIdentityAudience)
							}
							if src.ServiceAccountToken.Path != expectedWorkloadIdentityPath {
								t.Errorf("volume %q projected serviceAccountToken path = %q, want %q",
									vol.Name, src.ServiceAccountToken.Path, expectedWorkloadIdentityPath)
							}
							if src.ServiceAccountToken.ExpirationSeconds == nil || *src.ServiceAccountToken.ExpirationSeconds != expectedWorkloadIdentityExpiration {
								t.Errorf("volume %q projected serviceAccountToken expirationSeconds = %v, want %d",
									vol.Name, src.ServiceAccountToken.ExpirationSeconds, expectedWorkloadIdentityExpiration)
							}
						}
					}
				}
				if vol.Name == "gcp-credentials" {
					t.Errorf("unexpected credentials volume %q found in %s", vol.Name, tc.manifestPath)
				}
			}

			if tc.expectWorkloadAuth {
				if tokenVolumeName == "" {
					t.Fatalf("expected projected ServiceAccount token volume with audience %q in %s",
						expectedWorkloadIdentityAudience, tc.manifestPath)
				}
			} else {
				if tokenVolumeName != "" {
					t.Errorf("unexpected projected ServiceAccount token volume %q in %s", tokenVolumeName, tc.manifestPath)
				}
			}

			// Helper to check containers
			assertContainer := func(c corev1.Container, isTarget bool) {
				hasTokenMount := false

				for _, vm := range c.VolumeMounts {
					if vm.MountPath == expectedWorkloadIdentityTokenMount {
						hasTokenMount = true
						if !vm.ReadOnly {
							t.Errorf("container %q mount %q readOnly = false, want true", c.Name, expectedWorkloadIdentityTokenMount)
						}
						if tokenVolumeName != "" && vm.Name != tokenVolumeName {
							t.Errorf("container %q mount %q volume name = %q, want %q",
								c.Name, expectedWorkloadIdentityTokenMount, vm.Name, tokenVolumeName)
						}
					}
					if vm.Name == "gcp-credentials" || vm.MountPath == "/etc/eval-runner/gcp" {
						t.Errorf("container %q has unexpected credentials mount at %q", c.Name, vm.MountPath)
					}
				}

				for _, env := range c.Env {
					if env.Name == "GOOGLE_APPLICATION_CREDENTIALS" {
						t.Errorf("container %q has unexpected env var GOOGLE_APPLICATION_CREDENTIALS", c.Name)
					}
				}

				if isTarget && tc.expectWorkloadAuth {
					if !hasTokenMount {
						t.Errorf("container %q missing mount at %q", c.Name, expectedWorkloadIdentityTokenMount)
					}
				} else {
					if hasTokenMount {
						t.Errorf("non-target container %q has unexpected token mount at %q", c.Name, expectedWorkloadIdentityTokenMount)
					}
				}
			}

			targetFound := false
			for _, c := range podSpec.Containers {
				isTarget := c.Name == tc.targetContainer
				if isTarget {
					targetFound = true
				}
				assertContainer(c, isTarget)
			}
			if !targetFound {
				t.Fatalf("target container %q not found in %s", tc.targetContainer, tc.manifestPath)
			}

			for _, c := range podSpec.InitContainers {
				assertContainer(c, false)
			}
		})
	}
}
