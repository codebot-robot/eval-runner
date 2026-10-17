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
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestParseInspectLog_Binary20_17(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "binary_20_17.json"))
	if err != nil {
		t.Fatalf("Failed to read testdata: %v", err)
	}

	agg, err := ParseInspectLog(data)
	if err != nil {
		t.Fatalf("ParseInspectLog returned unexpected error: %v", err)
	}

	if agg.Samples != 20 {
		t.Errorf("Expected 20 samples, got %d", agg.Samples)
	}
	if agg.Successes != 17 {
		t.Errorf("Expected 17 successes, got %d", agg.Successes)
	}
	if agg.MeanScore == nil {
		t.Fatalf("Expected non-nil MeanScore")
	}
	if math.Abs(*agg.MeanScore-0.85) > 1e-6 {
		t.Errorf("Expected meanScore 0.85, got %f", *agg.MeanScore)
	}
}

func TestParseInspectLog_MockLLM2(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "mockllm_2.json"))
	if err != nil {
		t.Fatalf("Failed to read testdata: %v", err)
	}

	agg, err := ParseInspectLog(data)
	if err != nil {
		t.Fatalf("ParseInspectLog returned unexpected error: %v", err)
	}

	if agg.Samples != 2 {
		t.Errorf("Expected 2 samples, got %d", agg.Samples)
	}
	if agg.Successes != 0 {
		t.Errorf("Expected 0 successes, got %d", agg.Successes)
	}
	if agg.MeanScore == nil {
		t.Fatalf("Expected non-nil MeanScore")
	}
	if *agg.MeanScore != 0.0 {
		t.Errorf("Expected meanScore 0.0, got %f", *agg.MeanScore)
	}
}

func TestParseInspectLog_MixedScores(t *testing.T) {
	rawJSON := `{
		"version": 2,
		"eval": {"task": "test", "scorers": [{"name": "scorer1"}]},
		"samples": [
			{"id": 1, "scores": {"scorer1": {"value": 1.0}}},
			{"id": 2, "scores": {"scorer1": {"value": 0.5}}},
			{"id": 3, "scores": {"scorer1": {"value": "P"}}},
			{"id": 4, "scores": {"scorer1": {"value": true}}},
			{"id": 5, "scores": {"scorer1": {"value": false}}},
			{"id": 6, "scores": {"scorer1": {"value": "0.75"}}}
		]
	}`

	agg, err := ParseInspectLog([]byte(rawJSON))
	if err != nil {
		t.Fatalf("ParseInspectLog returned unexpected error: %v", err)
	}

	// Scores: 1.0 + 0.5 + 0.5 + 1.0 + 0.0 + 0.75 = 3.75 over 6 samples
	if agg.Samples != 6 {
		t.Errorf("Expected 6 samples, got %d", agg.Samples)
	}
	// Successes (score >= 1.0): samples 1 and 4 -> 2
	if agg.Successes != 2 {
		t.Errorf("Expected 2 successes, got %d", agg.Successes)
	}
	if agg.MeanScore == nil {
		t.Fatalf("Expected non-nil MeanScore")
	}
	expectedMean := 3.75 / 6.0
	if math.Abs(*agg.MeanScore-expectedMean) > 1e-6 {
		t.Errorf("Expected meanScore %f, got %f", expectedMean, *agg.MeanScore)
	}
}

func TestParseInspectLog_ResultsFallback(t *testing.T) {
	rawJSON := `{
		"version": 2,
		"eval": {"task": "test"},
		"results": {
			"completed_samples": 10,
			"scores": [
				{
					"name": "choice",
					"scored_samples": 10,
					"metrics": {
						"accuracy": {"value": 0.7}
					}
				}
			]
		}
	}`

	agg, err := ParseInspectLog([]byte(rawJSON))
	if err != nil {
		t.Fatalf("ParseInspectLog returned unexpected error: %v", err)
	}

	if agg.Samples != 10 {
		t.Errorf("Expected 10 samples, got %d", agg.Samples)
	}
	if agg.Successes != 7 {
		t.Errorf("Expected 7 successes, got %d", agg.Successes)
	}
	if agg.MeanScore == nil || math.Abs(*agg.MeanScore-0.7) > 1e-6 {
		t.Errorf("Expected meanScore 0.7, got %v", agg.MeanScore)
	}
}

func TestParseInspectLog_InvalidAndEmpty(t *testing.T) {
	if _, err := ParseInspectLog([]byte("")); err == nil {
		t.Error("Expected error for empty bytes")
	}
	if _, err := ParseInspectLog([]byte("   \n\t ")); err == nil {
		t.Error("Expected error for whitespace-only bytes")
	}
	if _, err := ParseInspectLog([]byte("not-json")); err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestScoreToFloat(t *testing.T) {
	tests := []struct {
		input     any
		wantVal   float64
		wantValid bool
	}{
		{nil, 0.0, false},
		{1.0, 1.0, true},
		{float32(0.5), 0.5, true},
		{1, 1.0, true},
		{int64(0), 0.0, true},
		{true, 1.0, true},
		{false, 0.0, true},
		{"C", 1.0, true},
		{"CORRECT", 1.0, true},
		{"yes", 1.0, true},
		{"true", 1.0, true},
		{"P", 0.5, true},
		{"PARTIAL", 0.5, true},
		{"I", 0.0, true},
		{"INCORRECT", 0.0, true},
		{"N", 0.0, true},
		{"NOANSWER", 0.0, true},
		{"no", 0.0, true},
		{"false", 0.0, true},
		{"0.85", 0.85, true},
		{"invalid", 0.0, false},
		{math.NaN(), 0.0, false},
		{math.Inf(1), 0.0, false},
	}

	for _, tt := range tests {
		val, valid := ScoreToFloat(tt.input)
		if valid != tt.wantValid {
			t.Errorf("ScoreToFloat(%v) valid=%v, want %v", tt.input, valid, tt.wantValid)
		}
		if valid && math.Abs(val-tt.wantVal) > 1e-6 {
			t.Errorf("ScoreToFloat(%v) val=%f, want %f", tt.input, val, tt.wantVal)
		}
	}
}

func TestParseInspectLog_MultipleScorersDeterministic(t *testing.T) {
	// Scorers without explicit primary scorer: "beta" has score 0.0, "alpha" has score 1.0.
	// Since keys are sorted alphabetically, "alpha" should always be chosen deterministically.
	rawJSON := `{
		"version": 2,
		"eval": {"task": "multi-scorer-test"},
		"samples": [
			{
				"id": 1,
				"scores": {
					"beta": {"value": 0.0},
					"alpha": {"value": 1.0}
				}
			}
		]
	}`

	for range 10 {
		agg, err := ParseInspectLog([]byte(rawJSON))
		if err != nil {
			t.Fatalf("ParseInspectLog failed: %v", err)
		}
		if agg.Samples != 1 {
			t.Errorf("Expected 1 sample, got %d", agg.Samples)
		}
		if agg.Successes != 1 {
			t.Errorf("Expected 1 success (alpha chosen), got %d", agg.Successes)
		}
		if agg.MeanScore == nil || *agg.MeanScore != 1.0 {
			t.Errorf("Expected meanScore 1.0, got %v", agg.MeanScore)
		}
	}
}
