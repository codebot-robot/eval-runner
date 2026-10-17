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

// ComparisonPhase represents the lifecycle phase of a Comparison.
// +kubebuilder:validation:Enum=Pending;Running;Completed;Failed
type ComparisonPhase string

const (
	// ComparisonPhasePending means the comparison has been accepted but evaluation has not started.
	ComparisonPhasePending ComparisonPhase = "Pending"

	// ComparisonPhaseRunning means one or both runs are still running or pairing is in progress.
	ComparisonPhaseRunning ComparisonPhase = "Running"

	// ComparisonPhaseCompleted means both runs are terminal and the paired comparison is complete.
	ComparisonPhaseCompleted ComparisonPhase = "Completed"

	// ComparisonPhaseFailed means comparison failed due to missing runs, unrecoverable errors, etc.
	ComparisonPhaseFailed ComparisonPhase = "Failed"
)

// ComparisonTaskLabel represents the statistical decision label for a single task.
// +kubebuilder:validation:Enum=improved;regressed;unchanged;inconclusive;running
type ComparisonTaskLabel string

const (
	ComparisonTaskLabelImproved     ComparisonTaskLabel = "improved"
	ComparisonTaskLabelRegressed    ComparisonTaskLabel = "regressed"
	ComparisonTaskLabelUnchanged    ComparisonTaskLabel = "unchanged"
	ComparisonTaskLabelInconclusive ComparisonTaskLabel = "inconclusive"
	ComparisonTaskLabelRunning      ComparisonTaskLabel = "running"
)

// ComparisonVerdict represents the aggregate run-level verdict.
// +kubebuilder:validation:Enum=IMPROVED;REGRESSED;UNCHANGED;INCONCLUSIVE;RUNNING
type ComparisonVerdict string

const (
	ComparisonVerdictImproved     ComparisonVerdict = "IMPROVED"
	ComparisonVerdictRegressed    ComparisonVerdict = "REGRESSED"
	ComparisonVerdictUnchanged    ComparisonVerdict = "UNCHANGED"
	ComparisonVerdictInconclusive ComparisonVerdict = "INCONCLUSIVE"
	ComparisonVerdictRunning      ComparisonVerdict = "RUNNING"
)

// ComparisonRunRef specifies a reference to a Run resource.
type ComparisonRunRef struct {
	// Run is the name of the Run resource in the same namespace.
	// +kubebuilder:validation:Required
	Run string `json:"run"`
}

// ComparisonSpec defines the desired state of Comparison.
type ComparisonSpec struct {
	// Baseline specifies the baseline Run to compare against.
	// +kubebuilder:validation:Required
	Baseline ComparisonRunRef `json:"baseline"`

	// Candidate specifies the candidate (e.g. PR) Run.
	// +kubebuilder:validation:Required
	Candidate ComparisonRunRef `json:"candidate"`

	// EquivalenceMargin is the statistical equivalence margin (default: 0.05 for ±0.05).
	// +kubebuilder:default=0.05
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=0.5
	// +optional
	EquivalenceMargin float64 `json:"equivalenceMargin,omitempty"`

	// Tasks is an optional allow-list of task IDs to compare.
	// If empty, all tasks present in both candidate and baseline runs are compared.
	// +optional
	Tasks []string `json:"tasks,omitempty"`
}

// DeltaConfidenceInterval represents a confidence interval on a difference/delta in [-1, 1].
type DeltaConfidenceInterval struct {
	// Lower is the lower confidence bound.
	// +kubebuilder:validation:Minimum=-1
	// +kubebuilder:validation:Maximum=1
	Lower float64 `json:"lower"`

	// Upper is the upper confidence bound.
	// +kubebuilder:validation:Minimum=-1
	// +kubebuilder:validation:Maximum=1
	Upper float64 `json:"upper"`
}

// ComparisonDropped records the count of dropped samples that could not be paired.
type ComparisonDropped struct {
	// Candidate is the number of candidate samples dropped due to lack of a matching baseline.
	// +optional
	Candidate int `json:"candidate"`

	// Baseline is the number of baseline samples dropped due to lack of a matching candidate.
	// +optional
	Baseline int `json:"baseline"`
}

// ComparisonTaskStatus records the paired comparison results for a single task.
type ComparisonTaskStatus struct {
	// ID is the unique identifier of the task (e.g. docs/readme_comprehension).
	// +kubebuilder:validation:Required
	ID string `json:"id"`

	// BaseRate is the mean score or success rate of baseline samples.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	BaseRate *float64 `json:"baseRate,omitempty"`

	// CandidateRate is the mean score or success rate of candidate samples.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	CandidateRate *float64 `json:"candidateRate,omitempty"`

	// Delta is the sample mean difference (candidate - baseline).
	// +kubebuilder:validation:Minimum=-1
	// +kubebuilder:validation:Maximum=1
	// +optional
	Delta *float64 `json:"delta,omitempty"`

	// DeltaCI is the confidence interval on the paired delta.
	// +optional
	DeltaCI *DeltaConfidenceInterval `json:"deltaCI,omitempty"`

	// Pairs is the number of matched (paired) sample pairs.
	// +optional
	Pairs int `json:"pairs"`

	// Wins is the number of paired samples where the candidate scored higher than baseline.
	// +optional
	Wins int `json:"wins"`

	// Losses is the number of paired samples where the candidate scored lower than baseline.
	// +optional
	Losses int `json:"losses"`

	// Ties is the number of paired samples where candidate and baseline scored equally.
	// +optional
	Ties int `json:"ties"`

	// Dropped records the number of unpaired samples dropped from candidate and baseline.
	// +optional
	Dropped ComparisonDropped `json:"dropped"`

	// Label is the decision outcome for this task: improved, regressed, unchanged, inconclusive, or running.
	// +optional
	Label ComparisonTaskLabel `json:"label,omitempty"`
}

// ComparisonSummary provides aggregate statistics across tasks in a Comparison.
type ComparisonSummary struct {
	// Tasks is a compact string representing completed vs total tasks (e.g. "2/2").
	// +optional
	Tasks string `json:"tasks,omitempty"`
}

// ComparisonStatus defines the observed state of Comparison.
type ComparisonStatus struct {
	// Phase is the lifecycle phase of the comparison (Running, Completed, Failed).
	// +optional
	Phase ComparisonPhase `json:"phase,omitempty"`

	// Verdict is the aggregate run-level verdict (IMPROVED, REGRESSED, UNCHANGED, INCONCLUSIVE, RUNNING).
	// +optional
	Verdict ComparisonVerdict `json:"verdict,omitempty"`

	// PImprovement is the posterior probability that the candidate is a net improvement.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	PImprovement *float64 `json:"pImprovement,omitempty"`

	// Tasks records the status and paired comparison results for each evaluated task.
	// +optional
	Tasks []ComparisonTaskStatus `json:"tasks,omitempty"`

	// Summary provides aggregate statistics across tasks in the Comparison.
	// +optional
	Summary *ComparisonSummary `json:"summary,omitempty"`

	// SummaryPath is the storage path or URI where comparisons/<name>.json was written to the artifact sink.
	// +optional
	SummaryPath string `json:"summaryPath,omitempty"`

	// Message is human-readable detail on failure or current status.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=comp;comparison
// +kubebuilder:printcolumn:name="Verdict",type="string",JSONPath=".status.verdict"
// +kubebuilder:printcolumn:name="P(imp)",type="number",JSONPath=".status.pImprovement"
// +kubebuilder:printcolumn:name="Tasks",type="string",JSONPath=".status.summary.tasks"
// +kubebuilder:printcolumn:name="Baseline",type="string",JSONPath=".spec.baseline.run"
// +kubebuilder:printcolumn:name="Candidate",type="string",JSONPath=".spec.candidate.run"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Comparison is the Schema for the comparisons API.
type Comparison struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ComparisonSpec   `json:"spec,omitempty"`
	Status ComparisonStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ComparisonList contains a list of Comparison.
type ComparisonList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Comparison `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Comparison{}, &ComparisonList{})
}
