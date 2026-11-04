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
	"strings"
	"testing"
)

func TestParseSandboxResults_ValidExample(t *testing.T) {
	data := []byte(`{"schemaVersion": 1, "samples": [{"id": "sample-1", "passed": true, "score": 1.0, "message": ""}]}`)

	agg, summaries, err := ParseSandboxResults(data)
	if err != nil {
		t.Fatalf("ParseSandboxResults failed: %v", err)
	}

	if agg.Samples != 1 {
		t.Errorf("Expected 1 sample, got %d", agg.Samples)
	}
	if agg.Successes != 1 {
		t.Errorf("Expected 1 success, got %d", agg.Successes)
	}
	if agg.MeanScore == nil || *agg.MeanScore != 1.0 {
		t.Errorf("Expected meanScore 1.0, got %v", agg.MeanScore)
	}
	if len(summaries) != 1 {
		t.Fatalf("Expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].ID != "sample-1" {
		t.Errorf("Expected summary ID 'sample-1', got %q", summaries[0].ID)
	}
	if summaries[0].Status != "pass" {
		t.Errorf("Expected summary Status 'pass', got %q", summaries[0].Status)
	}
	if summaries[0].Score == nil || *summaries[0].Score != 1.0 {
		t.Errorf("Expected summary Score 1.0, got %v", summaries[0].Score)
	}
}

func TestParseSandboxResults_MultipleSamplesWithDefaults(t *testing.T) {
	data := []byte(`{
		"schemaVersion": 1,
		"samples": [
			{"id": "s1", "passed": true},
			{"id": "s2", "passed": false},
			{"id": "s3", "passed": false, "score": 0.5, "message": "partial credit"}
		]
	}`)

	agg, summaries, err := ParseSandboxResults(data)
	if err != nil {
		t.Fatalf("ParseSandboxResults failed: %v", err)
	}

	if agg.Samples != 3 {
		t.Errorf("Expected 3 samples, got %d", agg.Samples)
	}
	if agg.Successes != 1 {
		t.Errorf("Expected 1 success, got %d", agg.Successes)
	}
	// Scores are 1.0, 0.0, 0.5 -> sum 1.5 -> mean 0.5
	if agg.MeanScore == nil || *agg.MeanScore != 0.5 {
		t.Errorf("Expected meanScore 0.5, got %v", agg.MeanScore)
	}

	expectedStatuses := []string{"pass", "fail", "fail"}
	expectedScores := []float64{1.0, 0.0, 0.5}
	for i, summary := range summaries {
		if summary.Status != expectedStatuses[i] {
			t.Errorf("Summary %d: expected status %q, got %q", i, expectedStatuses[i], summary.Status)
		}
		if summary.Score == nil || *summary.Score != expectedScores[i] {
			t.Errorf("Summary %d: expected score %f, got %v", i, expectedScores[i], summary.Score)
		}
	}
}

func TestParseSandboxResults_RejectsWrongSchemaVersion(t *testing.T) {
	data := []byte(`{"schemaVersion": 2, "samples": [{"id": "sample-1", "passed": true}]}`)

	_, _, err := ParseSandboxResults(data)
	if err == nil {
		t.Fatalf("Expected error for schemaVersion 2, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported results schemaVersion 2") {
		t.Errorf("Expected error to mention unsupported schemaVersion 2, got: %v", err)
	}
}

func TestParseSandboxResults_RejectsMalformedJSON(t *testing.T) {
	data := []byte(`{"schemaVersion": 1, "samples": [bad json]}`)

	_, _, err := ParseSandboxResults(data)
	if err == nil {
		t.Fatalf("Expected error for malformed JSON, got nil")
	}
	if !strings.Contains(err.Error(), "failed to unmarshal results JSON") {
		t.Errorf("Expected unmarshal error, got: %v", err)
	}
}

func TestParseSandboxResults_RejectsEmptyData(t *testing.T) {
	_, _, err := ParseSandboxResults([]byte("   "))
	if err == nil {
		t.Fatalf("Expected error for empty data, got nil")
	}
	if !strings.Contains(err.Error(), "empty results data") {
		t.Errorf("Expected empty results data error, got: %v", err)
	}
}

func TestParseSandboxResults_RejectsMissingPassedField(t *testing.T) {
	data := []byte(`{"schemaVersion": 1, "samples": [{"id": "sample-1", "score": 1.0}]}`)

	_, _, err := ParseSandboxResults(data)
	if err == nil {
		t.Fatalf("Expected error for missing passed field, got nil")
	}
	if !strings.Contains(err.Error(), "missing required 'passed' field") {
		t.Errorf("Expected missing passed field error, got: %v", err)
	}
}

func TestParseSandboxResults_RejectsOutOfRangeScore(t *testing.T) {
	data := []byte(`{"schemaVersion": 1, "samples": [{"id": "sample-1", "passed": true, "score": 1.5}]}`)

	_, _, err := ParseSandboxResults(data)
	if err == nil {
		t.Fatalf("Expected error for score out of range, got nil")
	}
	if !strings.Contains(err.Error(), "out of range [0, 1]") {
		t.Errorf("Expected out of range error, got: %v", err)
	}
}

func TestParseSandboxResults_RejectsEmptyID(t *testing.T) {
	data := []byte(`{"schemaVersion": 1, "samples": [{"id": "  ", "passed": true}]}`)

	_, _, err := ParseSandboxResults(data)
	if err == nil {
		t.Fatalf("Expected error for empty id, got nil")
	}
	if !strings.Contains(err.Error(), "empty id") {
		t.Errorf("Expected empty id error, got: %v", err)
	}
}
