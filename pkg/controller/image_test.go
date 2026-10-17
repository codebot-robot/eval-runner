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

package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDeriveSampleRunnerImage_RegistryPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "gcr prefix with tag",
			input:    "gcr.io/p/eval-runner:abc123",
			expected: "gcr.io/p/sample-runner:abc123",
		},
		{
			name:     "gcr prefix with knotai project",
			input:    "gcr.io/justinsb-knotai-dev/eval-runner:835f2bf",
			expected: "gcr.io/justinsb-knotai-dev/sample-runner:835f2bf",
		},
		{
			name:     "localhost with port",
			input:    "localhost:5000/eval-runner:latest",
			expected: "localhost:5000/sample-runner:latest",
		},
		{
			name:     "deep nested registry path with port",
			input:    "my-registry.example.com:8443/org/team/eval-runner:v1.2.3",
			expected: "my-registry.example.com:8443/org/team/sample-runner:v1.2.3",
		},
		{
			name:     "eval-runner in path segment before final",
			input:    "gcr.io/eval-runner/eval-runner:tag1",
			expected: "gcr.io/eval-runner/sample-runner:tag1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeriveSampleRunnerImage(tc.input)
			if err != nil {
				t.Fatalf("DeriveSampleRunnerImage(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.expected {
				t.Errorf("DeriveSampleRunnerImage(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestDeriveSampleRunnerImage_NoPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "e2e tag",
			input:    "eval-runner:e2e",
			expected: "sample-runner:e2e",
		},
		{
			name:     "latest tag",
			input:    "eval-runner:latest",
			expected: "sample-runner:latest",
		},
		{
			name:     "commit sha tag",
			input:    "eval-runner:abc1234",
			expected: "sample-runner:abc1234",
		},
		{
			name:     "semver tag",
			input:    "eval-runner:v0.1.0-alpha.1",
			expected: "sample-runner:v0.1.0-alpha.1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeriveSampleRunnerImage(tc.input)
			if err != nil {
				t.Fatalf("DeriveSampleRunnerImage(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.expected {
				t.Errorf("DeriveSampleRunnerImage(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestDeriveSampleRunnerImage_TagVsDigest(t *testing.T) {
	// A tag succeeds
	tagImg := "gcr.io/p/eval-runner:abc123"
	got, err := DeriveSampleRunnerImage(tagImg)
	if err != nil {
		t.Fatalf("expected tagged image to succeed, got error: %v", err)
	}
	if got != "gcr.io/p/sample-runner:abc123" {
		t.Errorf("got %q, want %q", got, "gcr.io/p/sample-runner:abc123")
	}

	// Digest references fail fast asking for --sample-runner-image
	digestTests := []struct {
		name  string
		input string
	}{
		{
			name:  "prefix with digest",
			input: "gcr.io/p/eval-runner@sha256:45b23dee08af5e43a7fa6c4c1e9ac094c78d6dd527cbd37882204c373b6b551c",
		},
		{
			name:  "no prefix with digest",
			input: "eval-runner@sha256:45b23dee08af5e43a7fa6c4c1e9ac094c78d6dd527cbd37882204c373b6b551c",
		},
		{
			name:  "tag and digest",
			input: "gcr.io/p/eval-runner:tag@sha256:45b23dee08af5e43a7fa6c4c1e9ac094c78d6dd527cbd37882204c373b6b551c",
		},
	}

	for _, tc := range digestTests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DeriveSampleRunnerImage(tc.input)
			if err == nil {
				t.Fatalf("DeriveSampleRunnerImage(%q) expected error for digest, got nil", tc.input)
			}
			if !strings.Contains(err.Error(), "digest reference") {
				t.Errorf("error %q should mention 'digest reference'", err.Error())
			}
			if !strings.Contains(err.Error(), "--sample-runner-image") {
				t.Errorf("error %q should ask for '--sample-runner-image'", err.Error())
			}
		})
	}
}

func TestDeriveSampleRunnerImage_MismatchError(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "bare image name without tag",
			input: "eval-runner",
		},
		{
			name:  "prefix image without tag",
			input: "gcr.io/p/eval-runner",
		},
		{
			name:  "different image name with tag",
			input: "other-controller:tag",
		},
		{
			name:  "different image with registry prefix",
			input: "gcr.io/p/other-controller:tag",
		},
		{
			name:  "eval-runner suffix in different image name",
			input: "gcr.io/p/not-eval-runner:tag",
		},
		{
			name:  "eval-runner prefix in different image name",
			input: "gcr.io/p/eval-runner-sidecar:tag",
		},
		{
			name:  "empty tag with colon",
			input: "eval-runner:",
		},
		{
			name:  "empty string",
			input: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DeriveSampleRunnerImage(tc.input)
			if err == nil {
				t.Fatalf("DeriveSampleRunnerImage(%q) expected mismatch error, got nil", tc.input)
			}
			if !strings.Contains(err.Error(), "does not end in eval-runner:<tag>") {
				t.Errorf("error %q should state that image does not end in eval-runner:<tag>", err.Error())
			}
			if !strings.Contains(err.Error(), "--sample-runner-image") {
				t.Errorf("error %q should ask for '--sample-runner-image'", err.Error())
			}
		})
	}
}

func TestFindEvalRunnerImage(t *testing.T) {
	t.Run("nil pod", func(t *testing.T) {
		_, err := FindEvalRunnerImage(nil)
		if err == nil {
			t.Fatal("expected error for nil pod")
		}
	})

	t.Run("empty containers", func(t *testing.T) {
		pod := &corev1.Pod{}
		_, err := FindEvalRunnerImage(pod)
		if err == nil {
			t.Fatal("expected error for pod with no containers")
		}
	})

	t.Run("single container named controller", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "controller-pod", Namespace: "eval-runner"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "controller", Image: "gcr.io/p/eval-runner:123"},
				},
			},
		}
		img, err := FindEvalRunnerImage(pod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if img != "gcr.io/p/eval-runner:123" {
			t.Errorf("got %q, want %q", img, "gcr.io/p/eval-runner:123")
		}
	})

	t.Run("container named eval-runner with sidecars", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "eval-runner-pod", Namespace: "eval-runner"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "istio-proxy", Image: "docker.io/istio/proxyv2:1.18"},
					{Name: "eval-runner", Image: "gcr.io/p/eval-runner:456"},
				},
			},
		}
		img, err := FindEvalRunnerImage(pod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if img != "gcr.io/p/eval-runner:456" {
			t.Errorf("got %q, want %q", img, "gcr.io/p/eval-runner:456")
		}
	})

	t.Run("container named controller with sidecars", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "eval-runner-pod", Namespace: "eval-runner"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "istio-proxy", Image: "docker.io/istio/proxyv2:1.18"},
					{Name: "controller", Image: "gcr.io/p/eval-runner:789"},
				},
			},
		}
		img, err := FindEvalRunnerImage(pod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if img != "gcr.io/p/eval-runner:789" {
			t.Errorf("got %q, want %q", img, "gcr.io/p/eval-runner:789")
		}
	})

	t.Run("unnamed container matched by image", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "eval-runner-pod", Namespace: "eval-runner"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "agent", Image: "docker.io/agent:v1"},
					{Name: "workload", Image: "gcr.io/p/eval-runner:xyz"},
				},
			},
		}
		img, err := FindEvalRunnerImage(pod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if img != "gcr.io/p/eval-runner:xyz" {
			t.Errorf("got %q, want %q", img, "gcr.io/p/eval-runner:xyz")
		}
	})

	t.Run("multiple containers none matching", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "eval-runner-pod", Namespace: "eval-runner"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "agent", Image: "docker.io/agent:v1"},
					{Name: "worker", Image: "docker.io/worker:v1"},
				},
			},
		}
		_, err := FindEvalRunnerImage(pod)
		if err == nil {
			t.Fatal("expected error when no containers match")
		}
	})
}
