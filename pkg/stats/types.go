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

package stats

// Interval represents a lower and upper confidence bound.
type Interval struct {
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
}

// Estimate represents a statistical point estimate with a confidence interval.
type Estimate struct {
	Point      float64  `json:"point"`
	CI         Interval `json:"ci"`
	Confidence float64  `json:"confidence"`
	N          int      `json:"n"`
}

// SampleScore represents an evaluation score for a single sample.
type SampleScore struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
}

// PairedSampleScore records paired scores for a single sample ID.
type PairedSampleScore struct {
	ID             string  `json:"id"`
	BaseScore      float64 `json:"baseScore"`
	CandidateScore float64 `json:"candidateScore"`
	Delta          float64 `json:"delta"`
}

// PairedCISeriesPoint represents a single evaluation point in a CI-vs-n history series.
type PairedCISeriesPoint struct {
	N        int            `json:"n"`
	Estimate PairedEstimate `json:"estimate"`
	CIWidth  float64        `json:"ciWidth"`
	Decision Decision       `json:"decision"`
}

// PairedEstimate contains the statistics from a paired comparison between
// a candidate configuration (e.g. PR) and baseline.
type PairedEstimate struct {
	// MeanDelta is the sample mean difference (candidate - baseline).
	MeanDelta float64 `json:"mean_delta"`

	// CI is the default confidence interval on the paired difference (bootstrap percentile).
	CI Interval `json:"ci"`

	// BootstrapCI is the non-parametric bootstrap percentile confidence interval.
	BootstrapCI Interval `json:"bootstrap_ci"`

	// NormalCI is the classical normal approximation confidence interval.
	NormalCI Interval `json:"normal_ci"`

	// PImprovement is the posterior probability that the candidate improves over baseline:
	// P(Δ > 0) + 0.5 · P(Δ = 0), splitting ties evenly.
	PImprovement float64 `json:"p_improvement"`

	// NormalPImprovement is the probability of improvement under the classical normal approximation:
	// P(Δ > 0) + 0.5 · P(Δ = 0), splitting ties evenly.
	NormalPImprovement float64 `json:"normal_p_improvement,omitempty"`

	// BaseMean is the mean score of baseline samples.
	BaseMean float64 `json:"base_mean"`

	// CandidateMean is the mean score of candidate (PR) samples.
	CandidateMean float64 `json:"candidate_mean"`

	// Confidence level used (e.g. 0.95).
	Confidence float64 `json:"confidence"`

	// NumPairs is the count of matched (paired) samples.
	NumPairs int `json:"num_pairs"`

	// Wins is the count of pairs where candidate scored higher than baseline (delta > 0).
	Wins int `json:"wins"`

	// Losses is the count of pairs where candidate scored lower than baseline (delta < 0).
	Losses int `json:"losses"`

	// Ties is the count of pairs where candidate and baseline scored equally (delta == 0).
	Ties int `json:"ties"`

	// DroppedCandidate is the number of candidate samples dropped due to lack of a matching baseline.
	DroppedCandidate int `json:"dropped_candidate"`

	// DroppedBaseline is the number of baseline samples dropped due to lack of a matching candidate.
	DroppedBaseline int `json:"dropped_baseline"`
}

// TaskLabel categorizes the status or outcome of an individual eval task.
type TaskLabel string

const (
	TaskLabelImproved     TaskLabel = "improved"
	TaskLabelRegressed    TaskLabel = "regressed"
	TaskLabelUnchanged    TaskLabel = "unchanged"
	TaskLabelInconclusive TaskLabel = "inconclusive"
	TaskLabelContinue     TaskLabel = "continue"
	TaskLabelRunning      TaskLabel = "running"
	TaskLabelQueued       TaskLabel = "queued"
)

// Decision represents the stopping rule outcome for an eval task.
type Decision struct {
	// Label is the task status: "improved", "regressed", "unchanged", "inconclusive", or "continue".
	Label TaskLabel `json:"label"`

	// Stop is true if sampling should terminate, false if more samples should be gathered.
	Stop bool `json:"stop"`

	// Reason explains the rationale behind the decision.
	Reason string `json:"reason"`

	// NextBatchSize suggests how many samples to run in the next batch if continuing.
	NextBatchSize int `json:"next_batch_size,omitempty"`

	// EstimatedSamples gives a rough estimate of total additional samples needed to resolve.
	EstimatedSamples int `json:"estimated_samples,omitempty"`
}

// VerdictType represents the run-level verdict across all tasks.
type VerdictType string

const (
	VerdictImproved     VerdictType = "IMPROVED"
	VerdictRegressed    VerdictType = "REGRESSED"
	VerdictUnchanged    VerdictType = "UNCHANGED"
	VerdictInconclusive VerdictType = "INCONCLUSIVE"
	VerdictRunning      VerdictType = "RUNNING"
)

// RunVerdict represents the aggregate verdict and summary for an entire run.
type RunVerdict struct {
	// Verdict is one of IMPROVED, REGRESSED, UNCHANGED, INCONCLUSIVE, or RUNNING.
	Verdict VerdictType `json:"verdict"`

	// PImprovement is the headline posterior probability of net improvement across tasks.
	PImprovement float64 `json:"p_improvement"`

	// Summary is a human-readable explanation of the verdict.
	Summary string `json:"summary,omitempty"`
}

// TaskReport encapsulates the evaluation result of an individual task for run-level aggregation.
type TaskReport struct {
	ID           string         `json:"id,omitempty"`
	Label        TaskLabel      `json:"label"`
	Estimate     PairedEstimate `json:"estimate"`
	PImprovement float64        `json:"p_improvement"`
}
