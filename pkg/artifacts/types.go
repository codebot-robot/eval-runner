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

package artifacts

import (
	"context"
	"errors"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/stats"
)

// ErrNotFound indicates that an artifact was not found in the sink.
var ErrNotFound = errors.New("artifact not found")

// IsNotFound checks if the error indicates a missing artifact.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// RunMetadata provides metadata about the Run and Task for artifact storage.
type RunMetadata struct {
	RunName      string
	RunNamespace string
	TaskID       string
	Model        string
	Commit       string
	TaskCount    int

	// Task execution aggregates and timestamps for summary.json
	Samples         int
	Successes       int
	MeanScore       *float64
	StartedAt       *time.Time
	FinishedAt      *time.Time
	SampleRange     *SampleRange
	Tokens          *TaskTokens
	Latency         *TaskLatency
	CostUSD         *float64
	PossibleSamples int
}

// TaskLatency records latency percentiles aggregated across evaluated samples.
type TaskLatency struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
}

// TaskTokens records token usage breakdown for an evaluation task.
// Invariant: Total == Input + Output across all reporting surfaces.
type TaskTokens struct {
	Input      int `json:"input,omitempty"`
	Output     int `json:"output,omitempty"`
	Total      int `json:"total,omitempty"`
	Reasoning  int `json:"reasoning,omitempty"`
	CacheRead  int `json:"cacheRead,omitempty"`
	CacheWrite int `json:"cacheWrite,omitempty"`
}

// SampleRange specifies a 1-based, inclusive window of samples.
type SampleRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// TaskSummary records per-task aggregates and execution metadata written alongside the Inspect log.
type TaskSummary struct {
	TaskID          string       `json:"taskId"`
	Model           string       `json:"model"`
	Commit          string       `json:"commit"`
	Samples         int          `json:"samples"`
	Successes       int          `json:"successes"`
	MeanScore       *float64     `json:"meanScore"`
	StartedAt       *time.Time   `json:"startedAt,omitempty"`
	FinishedAt      *time.Time   `json:"finishedAt,omitempty"`
	Capped          bool         `json:"capped"`
	CapBytes        int64        `json:"capBytes,omitempty"`
	SampleRange     *SampleRange `json:"sampleRange,omitempty"`
	Tokens          *TaskTokens  `json:"tokens,omitempty"`
	Latency         *TaskLatency `json:"latency,omitempty"`
	CostUSD         *float64     `json:"costUSD,omitempty"`
	PossibleSamples int          `json:"possibleSamples,omitempty"`
}

// ComparisonTaskArtifact records paired comparison details for a single task in the artifact JSON.
type ComparisonTaskArtifact struct {
	ID               string                           `json:"id"`
	BaseRate         float64                          `json:"baseRate"`
	CandidateRate    float64                          `json:"candidateRate"`
	Delta            float64                          `json:"delta"`
	DeltaCI          v1alpha1.DeltaConfidenceInterval `json:"deltaCI"`
	Pairs            int                              `json:"pairs"`
	Wins             int                              `json:"wins"`
	Losses           int                              `json:"losses"`
	Ties             int                              `json:"ties"`
	Dropped          v1alpha1.ComparisonDropped       `json:"dropped"`
	Label            string                           `json:"label"`
	PImprovement     float64                          `json:"pImprovement"`
	SamplesRun       int                              `json:"samplesRun,omitempty"`
	Decision         string                           `json:"decision,omitempty"`
	SuggestedNext    int                              `json:"suggestedNext,omitempty"`
	Tokens           *v1alpha1.TaskTokens             `json:"tokens,omitempty"`
	CandidateTokens  *v1alpha1.TaskTokens             `json:"candidateTokens,omitempty"`
	BaseTokens       *v1alpha1.TaskTokens             `json:"baseTokens,omitempty"`
	Latency          *v1alpha1.TaskLatency            `json:"latency,omitempty"`
	CandidateLatency *v1alpha1.TaskLatency            `json:"candidateLatency,omitempty"`
	BaseLatency      *v1alpha1.TaskLatency            `json:"baseLatency,omitempty"`
	CostUSD          *float64                         `json:"costUSD,omitempty"`
	CandidateCostUSD *float64                         `json:"candidateCostUSD,omitempty"`
	BaseCostUSD      *float64                         `json:"baseCostUSD,omitempty"`
	PossibleSamples  int                              `json:"possibleSamples,omitempty"`
	PairsTable       []stats.PairedSampleScore        `json:"pairsTable,omitempty"`
}

// ComparisonArtifact is the schema stored as comparisons/<name>.json in the artifact sink.
type ComparisonArtifact struct {
	Comparison            string                           `json:"comparison"`
	Namespace             string                           `json:"namespace"`
	Baseline              string                           `json:"baseline"`
	Candidate             string                           `json:"candidate"`
	Verdict               string                           `json:"verdict"`
	PImprovement          float64                          `json:"pImprovement"`
	CostUSD               *float64                         `json:"costUSD,omitempty"`
	PossibleSamples       int                              `json:"possibleSamples,omitempty"`
	Summary               string                           `json:"summary,omitempty"`
	Tasks                 []ComparisonTaskArtifact         `json:"tasks"`
	Batches               []v1alpha1.ComparisonBatchStatus `json:"batches,omitempty"`
	CreatedAt             time.Time                        `json:"createdAt"`
	SkippedNotPlanned     []string                         `json:"skippedNotPlanned,omitempty"`
	NotExecuted           []string                         `json:"notExecuted,omitempty"`
	NotExecutedByBothRuns []string                         `json:"notExecutedByBothRuns,omitempty"`
	FailedInBothRuns      []string                         `json:"failedInBothRuns,omitempty"`
	CandidateFailed       []string                         `json:"candidateFailed,omitempty"`
}

// ArtifactSink defines the interface for storing Inspect evaluation logs and artifacts.
type ArtifactSink interface {
	// StoreArtifact persists the raw log bytes for a task and returns the storage path or URI.
	StoreArtifact(ctx context.Context, meta RunMetadata, filename string, data []byte) (string, error)

	// StoreComparison persists comparison summary data as comparisons/<comparisonName>.json and returns the storage path or URI.
	StoreComparison(ctx context.Context, comparisonName string, data []byte) (string, error)

	// StoreManifest persists the discovered manifest JSON as runs/<commit>/manifest.json and returns the storage path or URI.
	StoreManifest(ctx context.Context, meta RunMetadata, data []byte) (string, error)

	// Load retrieves raw artifact bytes from the given storage path or URI, bounded by the maximum allowed artifact size.
	Load(ctx context.Context, path string) ([]byte, error)

	// ArtifactPrefix returns the run-level storage prefix/URI for a given commit, or empty string if not applicable.
	ArtifactPrefix(commit string) string

	// IsDurable reports whether the sink provides durable persistent storage across restarts.
	IsDurable() bool

	// Close releases any resources held by the sink (e.g. underlying network connections or storage clients).
	Close() error
}
