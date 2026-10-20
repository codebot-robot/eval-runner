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
	"strings"
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
	if len(agg.Scores) != 20 {
		t.Errorf("Expected 20 per-sample scores, got %d", len(agg.Scores))
	}
	if !agg.IsBinary() {
		t.Errorf("Expected binary_20_17 to be binary")
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
	if len(agg.Scores) != 6 {
		t.Errorf("Expected 6 per-sample scores, got %d", len(agg.Scores))
	}
	if agg.IsBinary() {
		t.Errorf("Expected MixedScores to be non-binary")
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

func TestParseInspectSampleScores(t *testing.T) {
	t.Run("Binary20_17", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("testdata", "binary_20_17.json"))
		if err != nil {
			t.Fatalf("Failed to read testdata: %v", err)
		}

		scores, err := ParseInspectSampleScores(data)
		if err != nil {
			t.Fatalf("ParseInspectSampleScores failed: %v", err)
		}

		if len(scores) != 20 {
			t.Fatalf("Expected 20 sample scores, got %d", len(scores))
		}

		if scores[0].ID != "1" || scores[0].Score != 1.0 {
			t.Errorf("Expected sample 0 to be ID '1' with score 1.0, got %+v", scores[0])
		}
		if scores[17].ID != "18" || scores[17].Score != 0.0 {
			t.Errorf("Expected sample 17 to be ID '18' with score 0.0, got %+v", scores[17])
		}
	})

	t.Run("MockLLM2", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("testdata", "mockllm_2.json"))
		if err != nil {
			t.Fatalf("Failed to read testdata: %v", err)
		}

		scores, err := ParseInspectSampleScores(data)
		if err != nil {
			t.Fatalf("ParseInspectSampleScores failed: %v", err)
		}

		if len(scores) != 2 {
			t.Fatalf("Expected 2 sample scores, got %d", len(scores))
		}

		if scores[0].ID != "1" || scores[0].Score != 0.0 {
			t.Errorf("Expected sample 0 ID '1' score 0.0, got %+v", scores[0])
		}
		if scores[1].ID != "2" || scores[1].Score != 0.0 {
			t.Errorf("Expected sample 1 ID '2' score 0.0, got %+v", scores[1])
		}
	})

	t.Run("EmptyAndInvalid", func(t *testing.T) {
		if _, err := ParseInspectSampleScores([]byte("")); err == nil {
			t.Errorf("Expected error for empty input")
		}
		if _, err := ParseInspectSampleScores([]byte("{invalid")); err == nil {
			t.Errorf("Expected error for invalid JSON")
		}
		emptySamples, err := ParseInspectSampleScores([]byte(`{"version": 2, "samples": []}`))
		if err != nil {
			t.Errorf("Unexpected error for empty samples: %v", err)
		}
		if len(emptySamples) != 0 {
			t.Errorf("Expected 0 sample scores, got %d", len(emptySamples))
		}
	})

	t.Run("EpochAndLargeID", func(t *testing.T) {
		raw := `{
			"version": 2,
			"samples": [
				{"id": 120000000, "epoch": 1, "scores": {"accuracy": {"value": 1.0}}},
				{"id": 120000000, "epoch": 2, "scores": {"accuracy": {"value": 0.5}}}
			]
		}`
		scores, err := ParseInspectSampleScores([]byte(raw))
		if err != nil {
			t.Fatalf("ParseInspectSampleScores failed: %v", err)
		}
		if len(scores) != 2 {
			t.Fatalf("Expected 2 scores, got %d", len(scores))
		}
		if scores[0].ID != "120000000" || scores[0].Score != 1.0 {
			t.Errorf("Expected score 0 to be ID '120000000' and score 1.0, got %+v", scores[0])
		}
		if scores[1].ID != "120000000#2" || scores[1].Score != 0.5 {
			t.Errorf("Expected score 1 to be ID '120000000#2' and score 0.5, got %+v", scores[1])
		}
	})
}

func TestExtractInspectTokens(t *testing.T) {
	t.Run("StatsModelUsagePresent", func(t *testing.T) {
		raw := `{
			"version": 2,
			"status": "success",
			"stats": {
				"model_usage": {
					"mockllm/model": {
						"input_tokens": 258,
						"output_tokens": 66,
						"total_tokens": 324
					}
				}
			}
		}`
		tokens, err := ExtractInspectTokens([]byte(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tokens == nil {
			t.Fatalf("expected non-nil tokens")
		}
		if tokens.Input != 258 || tokens.Output != 66 || tokens.Total != 324 {
			t.Fatalf("expected input=258, output=66, total=324, got %+v", tokens)
		}
	})

	t.Run("SamplesModelUsageFallback", func(t *testing.T) {
		raw := `{
			"version": 2,
			"status": "success",
			"samples": [
				{
					"id": 1,
					"model_usage": {
						"mockllm/model": {
							"input_tokens": 100,
							"output_tokens": 20,
							"total_tokens": 120,
							"reasoning_tokens": 10,
							"input_tokens_cache_read": 80,
							"input_tokens_cache_write": 5
						}
					}
				},
				{
					"id": 2,
					"model_usage": {
						"mockllm/model": {
							"input_tokens": 50,
							"output_tokens": 10,
							"total_tokens": 60,
							"reasoning_tokens": 5,
							"input_tokens_cache_read": 40,
							"input_tokens_cache_write": 2
						}
					}
				}
			]
		}`
		tokens, err := ExtractInspectTokens([]byte(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tokens == nil {
			t.Fatalf("expected non-nil tokens")
		}
		if tokens.Input != 150 || tokens.Output != 30 || tokens.Total != 180 {
			t.Fatalf("expected input=150, output=30, total=180, got %+v", tokens)
		}
		if tokens.Reasoning != 15 || tokens.CacheRead != 120 || tokens.CacheWrite != 7 {
			t.Fatalf("expected reasoning=15, cacheRead=120, cacheWrite=7, got %+v", tokens)
		}
	})

	t.Run("FixtureWithCacheAndReasoningEnforcesInvariant", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("testdata", "model_usage_cache_reasoning.json"))
		if err != nil {
			t.Fatalf("failed to read fixture: %v", err)
		}
		tokens, err := ExtractInspectTokens(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tokens == nil {
			t.Fatalf("expected non-nil tokens")
		}
		// Invariant: Total == Input + Output, dropping Inspect's total_tokens (272146).
		if tokens.Input != 140708 {
			t.Errorf("expected input=140708, got %d", tokens.Input)
		}
		if tokens.Output != 788 {
			t.Errorf("expected output=788, got %d", tokens.Output)
		}
		expectedTotal := tokens.Input + tokens.Output
		if tokens.Total != expectedTotal {
			t.Errorf("token accounting invariant violated: total %d != input %d + output %d", tokens.Total, tokens.Input, tokens.Output)
		}
		if tokens.Total != 141496 {
			t.Errorf("expected total=141496, got %d", tokens.Total)
		}
		if tokens.Reasoning != 150 {
			t.Errorf("expected reasoning=150, got %d", tokens.Reasoning)
		}
		if tokens.CacheRead != 130000 {
			t.Errorf("expected cacheRead=130000, got %d", tokens.CacheRead)
		}
		if tokens.CacheWrite != 650 {
			t.Errorf("expected cacheWrite=650, got %d", tokens.CacheWrite)
		}
	})

	t.Run("NoTokensReturnsNil", func(t *testing.T) {
		raw := `{
			"version": 2,
			"status": "success",
			"samples": [
				{"id": 1, "scores": {"accuracy": {"value": 1.0}}}
			]
		}`
		tokens, err := ExtractInspectTokens([]byte(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tokens != nil {
			t.Fatalf("expected nil tokens, got %v", tokens)
		}
	})
}

func TestExtractInspectLatency(t *testing.T) {
	t.Run("ValidTimestampsComputesP50AndP95", func(t *testing.T) {
		raw := `{
			"version": 2,
			"status": "success",
			"samples": [
				{
					"id": 1,
					"started_at": "2026-10-19T02:00:00.000Z",
					"completed_at": "2026-10-19T02:00:01.000Z"
				},
				{
					"id": 2,
					"started_at": "2026-10-19T02:00:00.000Z",
					"completed_at": "2026-10-19T02:00:02.000Z"
				},
				{
					"id": 3,
					"started_at": "2026-10-19T02:00:00.000Z",
					"completed_at": "2026-10-19T02:00:03.000Z"
				},
				{
					"id": 4,
					"started_at": "2026-10-19T02:00:00.000Z",
					"completed_at": "2026-10-19T02:00:04.000Z"
				},
				{
					"id": 5,
					"started_at": "2026-10-19T02:00:00.000Z",
					"completed_at": "2026-10-19T02:00:05.000Z"
				}
			]
		}`
		lat, err := ExtractInspectLatency([]byte(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if lat == nil {
			t.Fatalf("expected non-nil latency")
		}
		if math.Abs(lat.P50-3.0) > 1e-9 {
			t.Errorf("expected P50=3.0, got %f", lat.P50)
		}
		if math.Abs(lat.P95-4.8) > 1e-9 {
			t.Errorf("expected P95=4.8, got %f", lat.P95)
		}
	})

	t.Run("FallbackTotalTime", func(t *testing.T) {
		raw := `{
			"version": 2,
			"status": "success",
			"samples": [
				{"id": 1, "total_time": 0.5},
				{"id": 2, "total_time": 1.5}
			]
		}`
		lat, err := ExtractInspectLatency([]byte(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if lat == nil {
			t.Fatalf("expected non-nil latency")
		}
		if math.Abs(lat.P50-1.0) > 1e-9 {
			t.Errorf("expected P50=1.0, got %f", lat.P50)
		}
	})

	t.Run("NoTimestampsReturnsNil", func(t *testing.T) {
		raw := `{"version": 2, "status": "success", "samples": [{"id": 1}]}`
		lat, err := ExtractInspectLatency([]byte(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if lat != nil {
			t.Errorf("expected nil latency, got %+v", lat)
		}
	})

	t.Run("InvalidOrEmptyLog", func(t *testing.T) {
		if _, err := ExtractInspectLatency([]byte("")); err == nil {
			t.Errorf("expected error for empty data")
		}
		if _, err := ExtractInspectLatency([]byte("{invalid")); err == nil {
			t.Errorf("expected error for invalid json")
		}
	})
}

func TestExtractInspectDatasetSamples(t *testing.T) {
	t.Run("DatasetSamplesPresent", func(t *testing.T) {
		raw := `{
			"version": 2,
			"status": "success",
			"eval": {
				"task": "docs/readme_comprehension",
				"model": "mockllm/model",
				"dataset": {
					"name": "readme_questions",
					"samples": 21
				}
			}
		}`
		n, err := ExtractInspectDatasetSamples([]byte(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 21 {
			t.Errorf("expected dataset samples 21, got %d", n)
		}
	})

	t.Run("DatasetOmittedOrString", func(t *testing.T) {
		raw := `{
			"version": 2,
			"status": "success",
			"eval": {
				"task": "docs/readme_comprehension",
				"model": "mockllm/model",
				"dataset": "readme_questions"
			}
		}`
		n, err := ExtractInspectDatasetSamples([]byte(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 0 {
			t.Errorf("expected 0 for string dataset, got %d", n)
		}
	})

	t.Run("InvalidOrEmptyLog", func(t *testing.T) {
		if _, err := ExtractInspectDatasetSamples([]byte("")); err == nil {
			t.Errorf("expected error for empty data")
		}
		if _, err := ExtractInspectDatasetSamples([]byte("{invalid")); err == nil {
			t.Errorf("expected error for invalid json")
		}
	})
}

func TestExtractInspectProgress(t *testing.T) {
	t.Run("ValidProgressWithSamplesAndDataset", func(t *testing.T) {
		raw := `{
			"version": 2,
			"status": "started",
			"eval": {
				"task": "docs/readme_comprehension",
				"model": "mockllm/model",
				"dataset": {
					"name": "readme_questions",
					"samples": 21
				}
			},
			"samples": [
				{"id": 1, "input": "q1"},
				{"id": 2, "input": "q2"}
			]
		}`
		count, datasetSamples, err := ExtractInspectProgress([]byte(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 2 {
			t.Errorf("expected 2 samples, got %d", count)
		}
		if datasetSamples != 21 {
			t.Errorf("expected dataset samples 21, got %d", datasetSamples)
		}
	})

	t.Run("ZeroSamplesEmptyArray", func(t *testing.T) {
		raw := `{
			"version": 2,
			"status": "started",
			"eval": {
				"dataset": {"samples": 10}
			},
			"samples": []
		}`
		count, datasetSamples, err := ExtractInspectProgress([]byte(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 0 {
			t.Errorf("expected 0 samples, got %d", count)
		}
		if datasetSamples != 10 {
			t.Errorf("expected dataset samples 10, got %d", datasetSamples)
		}
	})

	t.Run("InvalidOrEmptyLog", func(t *testing.T) {
		if _, _, err := ExtractInspectProgress([]byte("")); err == nil {
			t.Errorf("expected error for empty data")
		}
		if _, _, err := ExtractInspectProgress([]byte("{invalid")); err == nil {
			t.Errorf("expected error for invalid json")
		}
	})
}

func TestParseInspectSamples(t *testing.T) {
	t.Run("TrajectoryMessagesFixture", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("testdata", "trajectory_messages.json"))
		if err != nil {
			t.Fatalf("failed to read fixture: %v", err)
		}

		samples, err := ParseInspectSamples(data)
		if err != nil {
			t.Fatalf("ParseInspectSamples failed: %v", err)
		}

		if len(samples) != 2 {
			t.Fatalf("expected 2 samples, got %d", len(samples))
		}

		// Sample 1: sample-017 (pass)
		s1 := samples[0]
		if s1.ID != "sample-017" {
			t.Errorf("expected ID 'sample-017', got %q", s1.ID)
		}
		if !s1.HasScore || s1.Score == nil || *s1.Score != 1.0 {
			t.Errorf("expected score 1.0, got %v", s1.Score)
		}
		if s1.Status != "pass" {
			t.Errorf("expected status 'pass', got %q", s1.Status)
		}
		if s1.ScoreStr != "1.00" {
			t.Errorf("expected scoreStr '1.00', got %q", s1.ScoreStr)
		}
		if s1.Latency == nil || *s1.Latency != 3.0 {
			t.Errorf("expected latency 3.0, got %v", s1.Latency)
		}
		if s1.TotalTokens != 1500 || s1.InputTokens != 1200 || s1.OutputTokens != 300 {
			t.Errorf("expected tokens 1500 (1200+300), got %d (%d+%d)", s1.TotalTokens, s1.InputTokens, s1.OutputTokens)
		}
		if !strings.Contains(s1.Input, "HTTP 200") {
			t.Errorf("unexpected input: %q", s1.Input)
		}
		if !strings.Contains(s1.Output, "Cleared CDN cache") {
			t.Errorf("unexpected output: %q", s1.Output)
		}
		if s1.Target != "Resolved" {
			t.Errorf("expected target 'Resolved', got %q", s1.Target)
		}
		if s1.Explanation != "pass: the reply names the cache purge and the HTTP 200 recheck" {
			t.Errorf("expected the scorer explanation, got %q", s1.Explanation)
		}

		// Verify transcript rows for s1
		if len(s1.Transcript) == 0 {
			t.Fatalf("expected non-empty transcript for sample-017")
		}
		foundToolCall := false
		foundTool := false
		for _, row := range s1.Transcript {
			if row.Kind == "tool_call" && strings.Contains(row.Content, "check_cdn") {
				foundToolCall = true
			}
			if row.Kind == "tool" && strings.Contains(row.Content, "CDN status 404") {
				foundTool = true
			}
		}
		if !foundToolCall {
			t.Errorf("expected tool_call check_cdn in transcript: %+v", s1.Transcript)
		}
		if !foundTool {
			t.Errorf("expected tool result in transcript: %+v", s1.Transcript)
		}

		// Sample 2: sample-019 (fail)
		s2 := samples[1]
		if s2.ID != "sample-019" {
			t.Errorf("expected ID 'sample-019', got %q", s2.ID)
		}
		if !s2.HasScore || s2.Score == nil || *s2.Score != 0.0 {
			t.Errorf("expected score 0.0, got %v", s2.Score)
		}
		if s2.Status != "fail" {
			t.Errorf("expected status 'fail', got %q", s2.Status)
		}
		if s2.ScoreStr != "0.00" {
			t.Errorf("expected scoreStr '0.00', got %q", s2.ScoreStr)
		}
		if s2.Latency == nil || *s2.Latency != 13.0 {
			t.Errorf("expected latency 13.0, got %v", s2.Latency)
		}
		if s2.TotalTokens != 41200 {
			t.Errorf("expected tokens 41200, got %d", s2.TotalTokens)
		}
		if s2.Target != "Resolved via runbook" {
			t.Errorf("expected target 'Resolved via runbook', got %q", s2.Target)
		}
		if s2.Explanation != "failed runbook check" {
			t.Errorf("expected the scorer reason as the explanation fallback, got %q", s2.Explanation)
		}

		// Verify transcript has tool_calls for classify_severity and page_oncall
		var kinds []string
		for _, row := range s2.Transcript {
			kinds = append(kinds, row.Kind)
		}
		// Expect user, tool_call, tool_call, tool, tool, assistant
		expectedKinds := []string{"user", "tool_call", "tool_call", "tool", "tool", "assistant"}
		if len(kinds) != len(expectedKinds) {
			t.Fatalf("expected %d transcript rows, got %d (%v)", len(expectedKinds), len(kinds), kinds)
		}
		for i, exp := range expectedKinds {
			if kinds[i] != exp {
				t.Errorf("at row %d, expected kind %q, got %q", i, exp, kinds[i])
			}
		}
	})

	t.Run("TargetAndExplanationShapes", func(t *testing.T) {
		raw := `{
			"version": 2,
			"status": "success",
			"eval": {"task": "docs/readme", "scorers": [{"name": "rubric"}]},
			"samples": [
				{
					"id": "structured",
					"target": ["mentions the install command", "links the quickstart"],
					"scores": {
						"rubric": {"value": "C", "explanation": {"text": "incorrect: the install command is missing"}},
						"aux": {"value": 1, "explanation": "aux scorer, not primary"}
					}
				},
				{
					"id": "missing",
					"scores": {"rubric": {"value": 1}}
				},
				{
					"id": "unscored",
					"target": "exact answer"
				}
			]
		}`
		samples, err := ParseInspectSamples([]byte(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(samples) != 3 {
			t.Fatalf("expected 3 samples, got %d", len(samples))
		}
		if got := samples[0].Target; got != "mentions the install command\nlinks the quickstart" {
			t.Errorf("expected list target joined by newlines, got %q", got)
		}
		if got := samples[0].Explanation; got != "incorrect: the install command is missing" {
			t.Errorf("expected the primary scorer's structured explanation, got %q", got)
		}
		if samples[1].Target != "" || samples[1].Explanation != "" {
			t.Errorf("expected empty target and explanation, got %q and %q", samples[1].Target, samples[1].Explanation)
		}
		if samples[2].Target != "exact answer" || samples[2].Explanation != "" {
			t.Errorf("expected target without explanation for an unscored sample, got %q and %q", samples[2].Target, samples[2].Explanation)
		}
	})

	t.Run("FallbackEventsWhenMessagesEmpty", func(t *testing.T) {
		raw := `{
			"version": 2,
			"status": "success",
			"eval": {"task": "triage/runbook_lookup"},
			"samples": [
				{
					"id": "sample-ev-1",
					"epoch": 1,
					"events": [
						{
							"event": "tool",
							"function": "page_oncall",
							"arguments": {"team": "ops"},
							"result": "Paged ops"
						},
						{
							"event": "model",
							"output": {"completion": "All done"}
						}
					]
				}
			]
		}`
		samples, err := ParseInspectSamples([]byte(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(samples) != 1 {
			t.Fatalf("expected 1 sample, got %d", len(samples))
		}
		tr := samples[0].Transcript
		if len(tr) != 3 {
			t.Fatalf("expected 3 rows (tool_call, tool, assistant), got %d (%+v)", len(tr), tr)
		}
		if tr[0].Kind != "tool_call" || tr[1].Kind != "tool" || tr[2].Kind != "assistant" {
			t.Errorf("unexpected transcript kinds: %+v", tr)
		}
	})

	t.Run("EmptyAndInvalid", func(t *testing.T) {
		if _, err := ParseInspectSamples([]byte("")); err == nil {
			t.Errorf("expected error for empty data")
		}
		if _, err := ParseInspectSamples([]byte("{invalid")); err == nil {
			t.Errorf("expected error for invalid json")
		}
		empty, err := ParseInspectSamples([]byte(`{"version": 2, "samples": []}`))
		if err != nil {
			t.Fatalf("unexpected error for empty samples: %v", err)
		}
		if len(empty) != 0 {
			t.Errorf("expected 0 samples, got %d", len(empty))
		}
	})
}
