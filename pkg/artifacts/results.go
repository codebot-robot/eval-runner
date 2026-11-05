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
	"encoding/json"
	"fmt"
	"strings"
)

// ResultsFile represents the schema of /out/results.json written by sandbox task execution.
type ResultsFile struct {
	SchemaVersion int            `json:"schemaVersion"`
	Samples       []SampleResult `json:"samples"`
}

// SampleResult represents an individual sample result within /out/results.json.
type SampleResult struct {
	ID      string   `json:"id"`
	Passed  *bool    `json:"passed"`
	Score   *float64 `json:"score,omitempty"`
	Message string   `json:"message,omitempty"`
}

// ParseSandboxResults parses raw JSON bytes from /out/results.json, validates the schema,
// and computes TaskAggregates and per-sample TaskSampleSummary entries.
func ParseSandboxResults(data []byte) (*TaskAggregates, []TaskSampleSummary, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil, fmt.Errorf("empty results data")
	}

	var file ResultsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal results JSON: %w", err)
	}

	if file.SchemaVersion != 1 {
		return nil, nil, fmt.Errorf("unsupported results schemaVersion %d, expected 1", file.SchemaVersion)
	}

	totalSamples := len(file.Samples)
	successes := 0
	scores := make([]float64, totalSamples)
	summaries := make([]TaskSampleSummary, totalSamples)

	for i, sample := range file.Samples {
		if strings.TrimSpace(sample.ID) == "" {
			return nil, nil, fmt.Errorf("sample at index %d has empty id", i)
		}
		if sample.Passed == nil {
			return nil, nil, fmt.Errorf("sample %q missing required 'passed' field", sample.ID)
		}
		if sample.Score != nil && (*sample.Score < 0.0 || *sample.Score > 1.0) {
			return nil, nil, fmt.Errorf("sample %q score %f out of range [0, 1]", sample.ID, *sample.Score)
		}

		var scoreValue float64
		if sample.Score != nil {
			scoreValue = *sample.Score
		} else if *sample.Passed {
			scoreValue = 1.0
		} else {
			scoreValue = 0.0
		}
		scores[i] = scoreValue

		status := "fail"
		if *sample.Passed {
			successes++
			status = "pass"
		}

		summaries[i] = TaskSampleSummary{
			ID:     sample.ID,
			Score:  &scores[i],
			Status: status,
		}
	}

	var meanScore *float64
	if totalSamples > 0 {
		var sum float64
		for _, score := range scores {
			sum += score
		}
		mean := sum / float64(totalSamples)
		meanScore = &mean
	}

	aggregate := &TaskAggregates{
		Samples:   totalSamples,
		Successes: successes,
		MeanScore: meanScore,
		Scores:    scores,
	}

	return aggregate, summaries, nil
}
