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
	"time"
)

// RunMetadata provides metadata about the Run and Task for artifact storage.
type RunMetadata struct {
	RunName      string
	RunNamespace string
	TaskID       string
	Model        string
	Commit       string

	// Task execution aggregates and timestamps for summary.json
	Samples    int
	Successes  int
	MeanScore  *float64
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// TaskSummary records per-task aggregates and execution metadata written alongside the Inspect log.
type TaskSummary struct {
	TaskID     string     `json:"taskId"`
	Model      string     `json:"model"`
	Commit     string     `json:"commit"`
	Samples    int        `json:"samples"`
	Successes  int        `json:"successes"`
	MeanScore  *float64   `json:"meanScore"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Capped     bool       `json:"capped"`
	CapBytes   int64      `json:"capBytes,omitempty"`
}

// ManifestMetadata provides metadata about the manifest for artifact storage.
type ManifestMetadata struct {
	RunName      string
	RunNamespace string
	Commit       string
	TaskCount    int
}

// ArtifactSink defines the interface for storing Inspect evaluation logs and artifacts.
type ArtifactSink interface {
	// StoreArtifact persists the raw log bytes for a task and returns the storage path or URI.
	StoreArtifact(ctx context.Context, meta RunMetadata, filename string, data []byte) (string, error)

	// StoreManifest persists the raw manifest JSON as runs/<commit>/manifest.json and returns the storage path or URI.
	StoreManifest(ctx context.Context, meta ManifestMetadata, data []byte) (string, error)

	// StoreComparison persists comparison summary data as comparisons/<comparisonName>.json and returns the storage path or URI.
	StoreComparison(ctx context.Context, comparisonName string, data []byte) (string, error)

	// Load retrieves raw artifact bytes from the given storage path or URI, bounded by the maximum allowed artifact size.
	Load(ctx context.Context, path string) ([]byte, error)

	// ArtifactPrefix returns the run-level storage prefix/URI for a given commit, or empty string if not applicable.
	ArtifactPrefix(commit string) string
}
