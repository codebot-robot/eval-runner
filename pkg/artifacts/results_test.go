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

	aggregate, summaries, err := ParseSandboxResults(data)
	if err != nil {
		t.Fatalf("ParseSandboxResults failed: %v", err)
	}

	if aggregate.Samples != 1 {
		t.Errorf("Expected 1 sample, got %d", aggregate.Samples)
	}
	if aggregate.Successes != 1 {
		t.Errorf("Expected 1 success, got %d", aggregate.Successes)
	}
	if aggregate.MeanScore == nil || *aggregate.MeanScore != 1.0 {
		t.Errorf("Expected meanScore 1.0, got %v", aggregate.MeanScore)
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

	aggregate, summaries, err := ParseSandboxResults(data)
	if err != nil {
		t.Fatalf("ParseSandboxResults failed: %v", err)
	}

	if aggregate.Samples != 3 {
		t.Errorf("Expected 3 samples, got %d", aggregate.Samples)
	}
	if aggregate.Successes != 1 {
		t.Errorf("Expected 1 success, got %d", aggregate.Successes)
	}
	// Scores are 1.0, 0.0, 0.5 -> sum 1.5 -> mean 0.5
	if aggregate.MeanScore == nil || *aggregate.MeanScore != 0.5 {
		t.Errorf("Expected meanScore 0.5, got %v", aggregate.MeanScore)
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

func TestClassifyArtifact(t *testing.T) {
	testCases := []struct {
		name         string
		data         []byte
		expectedKind ArtifactKind
	}{
		{
			name:         "EmptyBytes",
			data:         []byte(""),
			expectedKind: ArtifactKindUnknown,
		},
		{
			name:         "WhitespaceOnly",
			data:         []byte("   \n\t  "),
			expectedKind: ArtifactKindUnknown,
		},
		{
			name:         "ResultsFileSchemaVersion1",
			data:         []byte(`{"schemaVersion": 1, "samples": [{"id": "sample-1", "passed": true}]}`),
			expectedKind: ArtifactKindResultsFile,
		},
		{
			name:         "ResultsFileSchemaVersion2",
			data:         []byte(`{"schemaVersion": 2, "samples": []}`),
			expectedKind: ArtifactKindResultsFile,
		},
		{
			name:         "InspectLogVersion2",
			data:         []byte(`{"version": 2, "eval": {"task": "test"}, "samples": []}`),
			expectedKind: ArtifactKindInspectLog,
		},
		{
			name:         "GenericJSONWithoutSchemaVersion",
			data:         []byte(`{"name": "eval", "samples": []}`),
			expectedKind: ArtifactKindInspectLog,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actualKind := ClassifyArtifact(testCase.data)
			if actualKind != testCase.expectedKind {
				t.Errorf("expected %q, got %q", testCase.expectedKind, actualKind)
			}
		})
	}
}

func TestParseResultsSamples_Valid(t *testing.T) {
	data := []byte(`{
		"schemaVersion": 1,
		"samples": [
			{"id": "sample-1", "passed": true, "score": 1.0, "message": "all tests passed"},
			{"id": "sample-2", "passed": false, "message": "assertion error"},
			{"id": "sample-3", "passed": true},
			{"id": "sample-4", "passed": false, "score": 0.5, "message": "partial credit"}
		]
	}`)

	parsedSamples, err := ParseResultsSamples(data)
	if err != nil {
		t.Fatalf("ParseResultsSamples failed: %v", err)
	}

	if len(parsedSamples) != 4 {
		t.Fatalf("expected 4 samples, got %d", len(parsedSamples))
	}

	// sample-1
	if parsedSamples[0].ID != "sample-1" || parsedSamples[0].Status != "pass" || !parsedSamples[0].HasScore ||
		parsedSamples[0].Score == nil || *parsedSamples[0].Score != 1.0 || parsedSamples[0].ScoreStr != "1.00" ||
		parsedSamples[0].Explanation != "all tests passed" {
		t.Errorf("sample-1 mismatch: %+v", parsedSamples[0])
	}

	// sample-2 (default score for failed sample is 0.0)
	if parsedSamples[1].ID != "sample-2" || parsedSamples[1].Status != "fail" || !parsedSamples[1].HasScore ||
		parsedSamples[1].Score == nil || *parsedSamples[1].Score != 0.0 || parsedSamples[1].ScoreStr != "0.00" ||
		parsedSamples[1].Explanation != "assertion error" {
		t.Errorf("sample-2 mismatch: %+v", parsedSamples[1])
	}

	// sample-3 (default score for passed sample is 1.0, empty message)
	if parsedSamples[2].ID != "sample-3" || parsedSamples[2].Status != "pass" || !parsedSamples[2].HasScore ||
		parsedSamples[2].Score == nil || *parsedSamples[2].Score != 1.0 || parsedSamples[2].ScoreStr != "1.00" ||
		parsedSamples[2].Explanation != "" {
		t.Errorf("sample-3 mismatch: %+v", parsedSamples[2])
	}

	// sample-4 (explicit score 0.5 with passed: false)
	if parsedSamples[3].ID != "sample-4" || parsedSamples[3].Status != "fail" || !parsedSamples[3].HasScore ||
		parsedSamples[3].Score == nil || *parsedSamples[3].Score != 0.5 || parsedSamples[3].ScoreStr != "0.50" ||
		parsedSamples[3].Explanation != "partial credit" {
		t.Errorf("sample-4 mismatch: %+v", parsedSamples[3])
	}
}

func TestParseResultsSamples_EmptySamples(t *testing.T) {
	data := []byte(`{"schemaVersion": 1, "samples": []}`)
	parsedSamples, err := ParseResultsSamples(data)
	if err != nil {
		t.Fatalf("expected nil error for empty samples, got %v", err)
	}
	if len(parsedSamples) != 0 {
		t.Errorf("expected 0 samples, got %d", len(parsedSamples))
	}
}

func TestParseResultsSamples_ValidationErrors(t *testing.T) {
	testCases := []struct {
		name          string
		data          []byte
		expectedError string
	}{
		{
			name:          "EmptyData",
			data:          []byte("   "),
			expectedError: "empty results data",
		},
		{
			name:          "MalformedJSON",
			data:          []byte("{invalid"),
			expectedError: "failed to unmarshal results JSON",
		},
		{
			name:          "WrongSchemaVersion",
			data:          []byte(`{"schemaVersion": 2, "samples": [{"id": "sample-1", "passed": true}]}`),
			expectedError: "unsupported results schemaVersion 2",
		},
		{
			name:          "EmptyID",
			data:          []byte(`{"schemaVersion": 1, "samples": [{"id": "", "passed": true}]}`),
			expectedError: "empty id",
		},
		{
			name:          "MissingPassed",
			data:          []byte(`{"schemaVersion": 1, "samples": [{"id": "sample-1", "score": 1.0}]}`),
			expectedError: "missing required 'passed' field",
		},
		{
			name:          "OutOfRangeScore",
			data:          []byte(`{"schemaVersion": 1, "samples": [{"id": "sample-1", "passed": true, "score": 1.5}]}`),
			expectedError: "out of range [0, 1]",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseResultsSamples(testCase.data)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", testCase.expectedError)
			}
			if !strings.Contains(err.Error(), testCase.expectedError) {
				t.Errorf("expected error containing %q, got: %v", testCase.expectedError, err)
			}
		})
	}
}
