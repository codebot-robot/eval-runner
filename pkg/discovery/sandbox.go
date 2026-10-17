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
	"crypto/sha256"
	"encoding/hex"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

const (
	// DefaultSandboxNamespace is the namespace where sandboxes run.
	DefaultSandboxNamespace = "eval-runner-sandbox"

	// DefaultControllerNamespace is the namespace where eval-runner controller runs.
	DefaultControllerNamespace = "eval-runner"

	// SandboxdImage is the pinned image containing the sandboxd binary.
	SandboxdImage = "us-central1-docker.pkg.dev/k8s-staging-images/agent-sandbox/sandboxd:v20261015-v1.0.7@sha256:1cce7c94565de950903595864098bd32bd87dbd10865cab3f94fd0c58668cd42"

	// DiscoveryImage is the pinned image used for discovery sandboxes.
	DiscoveryImage = "alpine/git:v2.47.2"

	// DiscoveryContainerName is the container name in the discovery sandbox.
	DiscoveryContainerName = "agent"

	// SandboxdInitContainerName is the init container name that injects sandboxd.
	SandboxdInitContainerName = "install-sandboxd"

	// LabelRunName tracks the name of the Run that owns the sandbox.
	LabelRunName = "evalrunner.gke-labs.dev/run-name"

	// LabelRunNamespace tracks the namespace of the Run.
	LabelRunNamespace = "evalrunner.gke-labs.dev/run-namespace"

	// LabelRunUID tracks the UID of the Run for garbage collection.
	LabelRunUID = "evalrunner.gke-labs.dev/run-uid"

	// LabelRole indicates the role of the sandbox.
	LabelRole = "evalrunner.gke-labs.dev/role"
)

// SandboxNameForRun deterministically generates a valid Kubernetes resource name
// for a Run's discovery sandbox (DNS-1123 label, max 63 characters).
func SandboxNameForRun(runName string) string {
	prefix := "discovery-"
	fullName := prefix + runName
	if len(fullName) <= 63 {
		return fullName
	}

	h := sha256.Sum256([]byte(runName))
	hashSuffix := "-" + hex.EncodeToString(h[:4]) // 9 chars: -12345678
	maxPrefixLen := 63 - len(hashSuffix)
	return fullName[:maxPrefixLen] + hashSuffix
}

// BuildSandbox constructs the Sandbox CR for discovering tasks for a Run.
// The pod spec implements deployment topology B (injecting sandboxd binary into rootfs)
// and concept §3.2 "empty resources = zero privilege":
// - automountServiceAccountToken: false
// - non-root user (UID 1000)
// - no extra capabilities (drop ALL)
// - RuntimeDefault seccomp profile
// - pinned images
// - resource requests and limits
// - sandboxd gRPC (9090) and REST (8080) ports exposed
// - readiness probe targeting sandboxd /v1/health
func BuildSandbox(run *v1alpha1.Run, sandboxNamespace string) *sandboxv1beta1.Sandbox {
	if sandboxNamespace == "" {
		sandboxNamespace = DefaultSandboxNamespace
	}

	automountToken := false
	runAsNonRoot := true
	runAsUser := int64(1000)
	runAsGroup := int64(1000)
	allowPrivilegeEscalation := false
	seccompProfile := corev1.SeccompProfile{
		Type: corev1.SeccompProfileTypeRuntimeDefault,
	}

	labels := map[string]string{
		LabelRunName:      run.Name,
		LabelRunNamespace: run.Namespace,
		LabelRole:         "discovery",
	}
	if run.UID != "" {
		labels[LabelRunUID] = string(run.UID)
	}

	return &sandboxv1beta1.Sandbox{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sandboxv1beta1.GroupVersion.String(),
			Kind:       "Sandbox",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      SandboxNameForRun(run.Name),
			Namespace: sandboxNamespace,
			Labels:    labels,
		},
		Spec: sandboxv1beta1.SandboxSpec{
			SandboxBlueprint: sandboxv1beta1.SandboxBlueprint{
				PodTemplate: sandboxv1beta1.PodTemplate{
					ObjectMeta: sandboxv1beta1.PodMetadata{
						Labels: labels,
					},
					Spec: corev1.PodSpec{
						AutomountServiceAccountToken: &automountToken,
						SecurityContext: &corev1.PodSecurityContext{
							RunAsNonRoot:   &runAsNonRoot,
							RunAsUser:      &runAsUser,
							RunAsGroup:     &runAsGroup,
							FSGroup:        &runAsGroup,
							SeccompProfile: &seccompProfile,
						},
						RestartPolicy: corev1.RestartPolicyNever,
						InitContainers: []corev1.Container{
							{
								Name:  SandboxdInitContainerName,
								Image: SandboxdImage,
								Command: []string{
									"/bin/sh",
									"-c",
									"cp /usr/local/bin/sandboxd /sandboxd-bin/sandboxd",
								},
								SecurityContext: &corev1.SecurityContext{
									AllowPrivilegeEscalation: &allowPrivilegeEscalation,
									RunAsNonRoot:             &runAsNonRoot,
									RunAsUser:                &runAsUser,
									RunAsGroup:               &runAsGroup,
									Capabilities: &corev1.Capabilities{
										Drop: []corev1.Capability{"ALL"},
									},
									SeccompProfile: &seccompProfile,
								},
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("100m"),
										corev1.ResourceMemory: resource.MustParse("128Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("500m"),
										corev1.ResourceMemory: resource.MustParse("512Mi"),
									},
								},
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "sandboxd-bin",
										MountPath: "/sandboxd-bin",
									},
								},
							},
						},
						Containers: []corev1.Container{
							{
								Name:  DiscoveryContainerName,
								Image: DiscoveryImage,
								Command: []string{
									"/sandboxd-bin/sandboxd",
									"--root-dir=/workspace",
									"--rest-port=8080",
									"--grpc-port=9090",
								},
								Ports: []corev1.ContainerPort{
									{
										Name:          "rest",
										ContainerPort: 8080,
										Protocol:      corev1.ProtocolTCP,
									},
									{
										Name:          "grpc",
										ContainerPort: 9090,
										Protocol:      corev1.ProtocolTCP,
									},
								},
								ReadinessProbe: &corev1.Probe{
									ProbeHandler: corev1.ProbeHandler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/v1/health",
											Port: intstr.FromInt(8080),
										},
									},
									InitialDelaySeconds: 1,
									PeriodSeconds:       1,
									TimeoutSeconds:      2,
									SuccessThreshold:    1,
									FailureThreshold:    3,
								},
								SecurityContext: &corev1.SecurityContext{
									AllowPrivilegeEscalation: &allowPrivilegeEscalation,
									RunAsNonRoot:             &runAsNonRoot,
									RunAsUser:                &runAsUser,
									RunAsGroup:               &runAsGroup,
									Capabilities: &corev1.Capabilities{
										Drop: []corev1.Capability{"ALL"},
									},
									SeccompProfile: &seccompProfile,
								},
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("100m"),
										corev1.ResourceMemory: resource.MustParse("128Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("500m"),
										corev1.ResourceMemory: resource.MustParse("512Mi"),
									},
								},
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "sandboxd-bin",
										MountPath: "/sandboxd-bin",
										ReadOnly:  true,
									},
									{
										Name:      "workspace",
										MountPath: "/workspace",
									},
								},
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: "sandboxd-bin",
								VolumeSource: corev1.VolumeSource{
									EmptyDir: &corev1.EmptyDirVolumeSource{},
								},
							},
							{
								Name: "workspace",
								VolumeSource: corev1.VolumeSource{
									EmptyDir: &corev1.EmptyDirVolumeSource{},
								},
							},
						},
					},
				},
			},
		},
	}
}
