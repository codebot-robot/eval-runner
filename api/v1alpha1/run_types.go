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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RunPhase defines the phase of an eval Run.
// +kubebuilder:validation:Enum=Pending;Discovering;Discovered;Failed
type RunPhase string

const (
	// RunPhasePending means the Run has been created and is awaiting discovery.
	RunPhasePending RunPhase = "Pending"

	// RunPhaseDiscovering means discovery is actively running in an isolated sandbox.
	RunPhaseDiscovering RunPhase = "Discovering"

	// RunPhaseDiscovered means discovery completed successfully and the manifest is populated.
	RunPhaseDiscovered RunPhase = "Discovered"

	// RunPhaseFailed means discovery or execution encountered an error.
	RunPhaseFailed RunPhase = "Failed"
)

// ConditionTypeDiscovered is the condition type reporting whether discovery succeeded.
const ConditionTypeDiscovered = "Discovered"

// RepoSpec specifies the git repository and commit to evaluate.
type RepoSpec struct {
	// URL is the git clone URL for the repository under test.
	// +kubebuilder:validation:Required
	URL string `json:"url"`

	// Commit is the exact git commit SHA to checkout.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{7,64}$`
	Commit string `json:"commit"`
}

// RunSpec defines the desired state of Run.
type RunSpec struct {
	// Repo specifies the repository and commit to run.
	// +kubebuilder:validation:Required
	Repo RepoSpec `json:"repo"`
}

// ManifestResource describes a resource needed by an eval task.
type ManifestResource struct {
	// Type is the resource type (e.g., docker, kind, kubernetes-namespace).
	Type string `json:"type"`
}

// ManifestImage describes a container image needed by eval tasks.
type ManifestImage struct {
	// Name is the image name / tag.
	Name string `json:"name"`

	// Context is the build context directory path within the repository.
	Context string `json:"context"`
}

// ManifestTask describes a single eval task discovered from .evals/list.
type ManifestTask struct {
	// ID is the unique stable identifier for the task (e.g. triage/runbook_lookup).
	// +kubebuilder:validation:Required
	ID string `json:"id"`

	// Workdir is the directory to run the task from.
	// +optional
	Workdir string `json:"workdir,omitempty"`

	// Spec is the framework-specific task reference (e.g. file.py@task_fn).
	// +kubebuilder:validation:Required
	Spec string `json:"spec"`

	// Resources specifies the declarative resource requirements for the task.
	// +optional
	Resources []ManifestResource `json:"resources,omitempty"`

	// Images specifies which images built from the manifest this task requires.
	// +optional
	Images []string `json:"images,omitempty"`
}

// Manifest is the parsed output of .evals/list.
type Manifest struct {
	// Images lists the images declared by the repository.
	// +optional
	Images []ManifestImage `json:"images,omitempty"`

	// Tasks lists all discovered eval tasks.
	Tasks []ManifestTask `json:"tasks"`
}

// RunStatus defines the observed state of Run.
type RunStatus struct {
	// Phase is the current lifecycle phase of the Run.
	// +optional
	Phase RunPhase `json:"phase,omitempty"`

	// Message is human-readable detail on failure or current status.
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions represent the latest observations of the Run's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Manifest is the parsed output of .evals/list once discovery completes.
	// +optional
	Manifest *Manifest `json:"manifest,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=run
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Run is the Schema for the runs API.
type Run struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RunSpec   `json:"spec,omitempty"`
	Status RunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RunList contains a list of Run.
type RunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Run `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Run{}, &RunList{})
}
