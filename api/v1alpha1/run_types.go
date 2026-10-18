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

// RunPhase represents the lifecycle phase of a Run.
// +kubebuilder:validation:Enum=Pending;Discovering;Discovered;Running;Completed;Failed
type RunPhase string

const (
	// RunPhasePending means the Run has been created and is awaiting discovery.
	RunPhasePending RunPhase = "Pending"

	// RunPhaseDiscovering means discovery is actively running in an isolated sandbox.
	RunPhaseDiscovering RunPhase = "Discovering"

	// RunPhaseDiscovered means discovery completed successfully and the manifest is populated.
	RunPhaseDiscovered RunPhase = "Discovered"

	// RunPhaseRunning means sample tasks are actively executing in sandboxes.
	RunPhaseRunning RunPhase = "Running"

	// RunPhaseCompleted means all sample tasks have finished executing.
	RunPhaseCompleted RunPhase = "Completed"

	// RunPhaseFailed means discovery or execution encountered an error.
	RunPhaseFailed RunPhase = "Failed"
)

// TaskPhase defines the lifecycle phase of an individual eval task.
// +kubebuilder:validation:Enum=Pending;Running;Completed;Failed
type TaskPhase string

const (
	// TaskPhasePending means the task is waiting to run in a sandbox.
	TaskPhasePending TaskPhase = "Pending"

	// TaskPhaseRunning means the task is currently executing in a sandbox.
	TaskPhaseRunning TaskPhase = "Running"

	// TaskPhaseCompleted means the task completed execution and scores were recorded.
	TaskPhaseCompleted TaskPhase = "Completed"

	// TaskPhaseFailed means the task encountered an error or failed to run.
	TaskPhaseFailed TaskPhase = "Failed"
)

// ConfidenceInterval represents a statistical confidence interval for a metric.
type ConfidenceInterval struct {
	// Lower is the lower confidence bound.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	Lower float64 `json:"lower"`

	// Upper is the upper confidence bound.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	Upper float64 `json:"upper"`

	// Confidence is the confidence level (e.g. 0.95 for 95% confidence).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	Confidence float64 `json:"confidence"`

	// Method is the estimation method used (e.g. "wilson", "t").
	Method string `json:"method"`
}

// TaskStatus records the execution status and scores for a single eval task.
type TaskStatus struct {
	// ID is the unique identifier of the task (e.g. docs/readme_comprehension).
	// +kubebuilder:validation:Required
	ID string `json:"id"`

	// Phase is the current execution phase of this task.
	// +optional
	Phase TaskPhase `json:"phase,omitempty"`

	// StartedAt is the time when the task transitioned to Running.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// Samples is the number of samples scored.
	// +optional
	Samples int `json:"samples"`

	// Successes is the number of samples with score == 1 (for binary scorers).
	// +optional
	Successes int `json:"successes"`

	// MeanScore is the mean of per-sample scores in [0, 1].
	// +optional
	MeanScore *float64 `json:"meanScore,omitempty"`

	// SuccessRate is the point estimate of the success rate in [0, 1].
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	SuccessRate *float64 `json:"successRate,omitempty"`

	// SuccessRateCI is the confidence interval for the success rate.
	// +optional
	SuccessRateCI *ConfidenceInterval `json:"successRateCI,omitempty"`

	// Message contains failure detail or bounded error information.
	// +optional
	Message string `json:"message,omitempty"`

	// LogPath is the path or URI where the Inspect log was stored by the artifact sink.
	// +optional
	LogPath string `json:"logPath,omitempty"`
}

// ConditionTypeDiscovered is the condition type reporting whether discovery succeeded.
const ConditionTypeDiscovered = "Discovered"

// FinalizerDiscoverySandbox is the finalizer placed on Runs to ensure discovery and task sandboxes are deleted.
const FinalizerDiscoverySandbox = "evalrunner.gke-labs.dev/discovery-sandbox"

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

	// Model is the Inspect model identifier (e.g. mockllm/model, google/gemini-3.5-flash-lite).
	// If empty, the Run stops after discovery.
	// +optional
	Model string `json:"model,omitempty"`

	// Tasks is an optional allow-list of task IDs to run.
	// If empty, all tasks with empty resources are eligible.
	// +optional
	Tasks []string `json:"tasks,omitempty"`

	// Limit is an optional per-task cap on samples (passed to Inspect as --limit).
	// +optional
	Limit *int `json:"limit,omitempty"`
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

// RunDiscoveryStatus records pointers, counts, and checksum from eval discovery.
type RunDiscoveryStatus struct {
	// ManifestPath is the URI or path where the discovered manifest is stored in the artifact store.
	// +optional
	ManifestPath string `json:"manifestPath,omitempty"`

	// ManifestHash is the sha256 checksum (in sha256:<hex> format) of the stored manifest.
	// +optional
	ManifestHash string `json:"manifestHash,omitempty"`

	// TaskCount is the total number of tasks in the discovered manifest.
	// +optional
	TaskCount int `json:"taskCount,omitempty"`

	// EligibleTaskCount is the number of tasks in the manifest eligible for evaluation.
	// +optional
	EligibleTaskCount int `json:"eligibleTaskCount,omitempty"`

	// TaskIDs is a list of discovered task IDs, capped at 50 to bound status size.
	// +optional
	TaskIDs []string `json:"taskIDs,omitempty"`
}

// ScoreRange represents the minimum and maximum values of a score or rate.
type ScoreRange struct {
	// Min is the minimum value in the range.
	Min float64 `json:"min"`

	// Max is the maximum value in the range.
	Max float64 `json:"max"`
}

// RunSummary provides aggregate statistics across tasks in a Run.
type RunSummary struct {
	// Tasks is a compact string representing completed vs total tasks (e.g. "2/2").
	// +optional
	Tasks string `json:"tasks,omitempty"`

	// TasksCompleted is the number of tasks that completed successfully.
	TasksCompleted int `json:"tasksCompleted"`

	// TasksFailed is the number of tasks that failed.
	TasksFailed int `json:"tasksFailed"`

	// TasksTotal is the total number of tasks.
	TasksTotal int `json:"tasksTotal"`

	// TotalSamples is the total number of samples evaluated across all tasks.
	TotalSamples int `json:"totalSamples"`

	// MeanSuccessRate is the simple mean of task success rates across completed tasks.
	// +optional
	MeanSuccessRate *float64 `json:"meanSuccessRate,omitempty"`

	// SuccessRateRange is the min and max of task success rates across completed tasks.
	// +optional
	SuccessRateRange *ScoreRange `json:"successRateRange,omitempty"`

	// Rates is a compact string of per-task rates and confidence intervals for wide printer column display.
	// +optional
	Rates string `json:"rates,omitempty"`
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

	// Discovery records pointers, hash, and counts for the discovered manifest.
	// +optional
	Discovery *RunDiscoveryStatus `json:"discovery,omitempty"`

	// DiscoveryDeadline is the deadline by which discovery must complete.
	// +optional
	DiscoveryDeadline *metav1.Time `json:"discoveryDeadline,omitempty"`

	// ExecutionDeadline is the deadline by which sample execution must complete.
	// +optional
	ExecutionDeadline *metav1.Time `json:"executionDeadline,omitempty"`

	// Tasks records the status and results for each executed eval task.
	// +optional
	Tasks []TaskStatus `json:"tasks,omitempty"`

	// Artifacts is the base URI or prefix where artifacts for this Run are stored (e.g. gs://bucket/runs/<commit>/).
	// +optional
	Artifacts string `json:"artifacts,omitempty"`

	// Summary provides aggregate statistics across tasks in the Run.
	// +optional
	Summary *RunSummary `json:"summary,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=run
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Tasks",type="string",JSONPath=".status.summary.tasks"
// +kubebuilder:printcolumn:name="Samples",type="integer",JSONPath=".status.summary.totalSamples"
// +kubebuilder:printcolumn:name="Rates",type="string",priority=1,JSONPath=".status.summary.rates"
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
