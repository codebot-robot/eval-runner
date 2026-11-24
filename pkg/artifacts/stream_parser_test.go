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
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
)

func TestParseInspectSamplesStream_ScorerPrecedence(t *testing.T) {
	testCases := []struct {
		name     string
		document string
	}{
		{
			name:     "ResultsAfterSamples",
			document: `{"version": 2, "samples": [{"id": "s1", "scores": {"aux": {"value": 0.0}, "prim": {"value": 1.0}}}], "results": {"scores": [{"name": "prim"}]}}`,
		},
		{
			name:     "EvalAfterSamplesOverridesResults",
			document: `{"version": 2, "results": {"scores": [{"name": "aux"}]}, "samples": [{"id": "s1", "scores": {"aux": {"value": 0.0}, "prim": {"value": 1.0}}}], "eval": {"scorers": [{"name": "prim"}]}}`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			samples, err := ParseInspectSamplesStream(strings.NewReader(testCase.document))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(samples) != 1 || samples[0].Status != "pass" || samples[0].Score == nil || *samples[0].Score != 1.0 {
				t.Errorf("expected primary scorer (pass, 1.0), got: %+v", samples)
			}
		})
	}
}

func TestParseInspectSamplesStream_TruncatedInput(t *testing.T) {
	truncated := `{"version": 2, "samples": [{"id": "s1", "scores": {"acc": {"value":`
	if _, err := ParseInspectSamplesStream(strings.NewReader(truncated)); err == nil {
		t.Fatalf("expected error for truncated input, got nil")
	}
}

func TestParseResultsSamplesStream_KeyOrderAndErrors(t *testing.T) {
	t.Run("SchemaVersionAfterSamples", func(t *testing.T) {
		document := `{
			"samples": [
				{"id": "sample-1", "passed": true, "score": 0.75, "message": "partial"},
				{"id": "sample-2", "passed": false}
			],
			"schemaVersion": 1
		}`
		samples, err := ParseResultsSamplesStream(strings.NewReader(document))
		if err != nil {
			t.Fatalf("ParseResultsSamplesStream failed: %v", err)
		}
		if len(samples) != 2 {
			t.Fatalf("expected 2 samples, got %d", len(samples))
		}
		if samples[0].ID != "sample-1" || samples[0].Status != "pass" || samples[0].Score == nil ||
			*samples[0].Score != 0.75 || samples[0].ScoreStr != "0.75" || samples[0].Explanation != "partial" {
			t.Errorf("sample-1 mismatch: %+v", samples[0])
		}
		if samples[1].ID != "sample-2" || samples[1].Status != "fail" || samples[1].Score == nil ||
			*samples[1].Score != 0.0 {
			t.Errorf("sample-2 mismatch: %+v", samples[1])
		}
	})

	errorCases := []struct {
		name          string
		document      string
		expectedError string
	}{
		{
			name:          "TruncatedSample",
			document:      `{"schemaVersion": 1, "samples": [{"id": "sample-1", "passed":`,
			expectedError: "failed to unmarshal results JSON",
		},
		{
			name:          "UnsupportedSchemaVersionAfterSamples",
			document:      `{"samples": [{"id": "sample-1", "passed": true}], "schemaVersion": 2}`,
			expectedError: "unsupported results schemaVersion 2",
		},
		{
			name:          "TrailingGarbage",
			document:      `{"schemaVersion": 1, "samples": []} trailing`,
			expectedError: "failed to unmarshal results JSON",
		},
	}

	for _, testCase := range errorCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseResultsSamplesStream(strings.NewReader(testCase.document))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", testCase.expectedError)
			}
			if !strings.Contains(err.Error(), testCase.expectedError) {
				t.Errorf("expected error containing %q, got: %v", testCase.expectedError, err)
			}
		})
	}
}

func TestParseTaskSamplesStream_FormatDispatch(t *testing.T) {
	testCases := []struct {
		name           string
		document       string
		expectedID     string
		expectedStatus string
		expectedScore  float64
		expectedNote   string
	}{
		{
			name:           "ResultsSchemaVersionBeforeSamples",
			document:       `{"schemaVersion": 1, "samples": [{"id": "res-1", "passed": true, "score": 0.8, "message": "ok"}]}`,
			expectedID:     "res-1",
			expectedStatus: "pass",
			expectedScore:  0.8,
			expectedNote:   "ok",
		},
		{
			name:           "ResultsSchemaVersionAfterSamples",
			document:       `{"samples": [{"id": "res-2", "passed": false, "score": 0.25, "message": "partial"}], "schemaVersion": 1}`,
			expectedID:     "res-2",
			expectedStatus: "fail",
			expectedScore:  0.25,
			expectedNote:   "partial",
		},
		{
			name:           "InspectLogStandard",
			document:       `{"version": 2, "eval": {"scorers": [{"name": "acc"}]}, "samples": [{"id": "insp-1", "scores": {"acc": {"value": 1.0, "explanation": "matched"}}}]}`,
			expectedID:     "insp-1",
			expectedStatus: "pass",
			expectedScore:  1.0,
			expectedNote:   "matched",
		},
		{
			name:           "InspectLogEvalAfterSamples",
			document:       `{"version": 2, "samples": [{"id": "insp-2", "scores": {"aux": {"value": 0.0}, "prim": {"value": 1.0}}}], "eval": {"scorers": [{"name": "prim"}]}}`,
			expectedID:     "insp-2",
			expectedStatus: "pass",
			expectedScore:  1.0,
		},
		{
			name:           "InspectLogWithNestedSchemaVersion",
			document:       `{"version": 2, "eval": {"dataset": {"schemaVersion": 1}, "scorers": [{"name": "acc"}]}, "samples": [{"id": "insp-3", "input": {"schemaVersion": 1}, "scores": {"acc": {"value": 1.0}}}]}`,
			expectedID:     "insp-3",
			expectedStatus: "pass",
			expectedScore:  1.0,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			samples, err := ParseTaskSamplesStream(strings.NewReader(testCase.document))
			if err != nil {
				t.Fatalf("ParseTaskSamplesStream failed: %v", err)
			}
			if len(samples) != 1 {
				t.Fatalf("expected 1 sample, got %d", len(samples))
			}
			sample := samples[0]
			if sample.ID != testCase.expectedID || sample.Status != testCase.expectedStatus ||
				sample.Score == nil || *sample.Score != testCase.expectedScore || sample.Explanation != testCase.expectedNote {
				t.Errorf("unexpected sample: %+v", sample)
			}
		})
	}
}

func TestParseTaskSamplesStream_InvalidAndTruncated(t *testing.T) {
	errorCases := []struct {
		name          string
		document      string
		expectedError string
	}{
		{
			name:          "EmptyInput",
			document:      "   \n\t ",
			expectedError: "empty artifact data",
		},
		{
			name:          "MalformedJSON",
			document:      "{invalid",
			expectedError: "failed to parse task log",
		},
		{
			name:          "TruncatedResults",
			document:      `{"schemaVersion": 1, "samples": [{"id": "s1", "passed":`,
			expectedError: "failed to parse task log",
		},
		{
			name:          "TruncatedInspect",
			document:      `{"version": 2, "samples": [{"id": "s1", "scores": {"acc": {"value":`,
			expectedError: "failed to parse task log",
		},
		{
			name:          "UnsupportedSchemaVersionAfterSamples",
			document:      `{"samples": [{"id": "s1", "passed": true}], "schemaVersion": 2}`,
			expectedError: "unsupported results schemaVersion 2",
		},
		{
			name:          "ResultsMissingPassedBeforeSchemaVersion",
			document:      `{"samples": [{"id": "s1", "score": 1.0}], "schemaVersion": 1}`,
			expectedError: "missing required 'passed' field",
		},
	}

	for _, testCase := range errorCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseTaskSamplesStream(strings.NewReader(testCase.document))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", testCase.expectedError)
			}
			if !strings.Contains(err.Error(), testCase.expectedError) {
				t.Errorf("expected error containing %q, got: %v", testCase.expectedError, err)
			}
		})
	}
}

type memoryObservingReader struct {
	reader    io.Reader
	peakHeap  uint64
	readCount int
}

func (observer *memoryObservingReader) Read(buffer []byte) (int, error) {
	bytesRead, err := observer.reader.Read(buffer)
	observer.readCount++
	if observer.readCount%10 == 0 {
		runtime.GC()
	}
	var memoryStats runtime.MemStats
	runtime.ReadMemStats(&memoryStats)
	if memoryStats.HeapAlloc > observer.peakHeap {
		observer.peakHeap = memoryStats.HeapAlloc
	}
	return bytesRead, err
}

func buildSyntheticInspectLogStream(sampleCount int, payloadSize int) io.Reader {
	payloadJSON := `"` + strings.Repeat("D", payloadSize) + `"`
	readers := make([]io.Reader, 0, sampleCount*3+2)
	readers = append(readers, strings.NewReader(`{"version": 2, "eval": {"scorers": [{"name": "acc"}]}, "samples": [`))
	for sampleIndex := 0; sampleIndex < sampleCount; sampleIndex++ {
		prefix := fmt.Sprintf(`{"id": "s-%d", "scores": {"acc": {"value": 1.0}}, "events": [{"event": "step", "result": `, sampleIndex)
		if sampleIndex > 0 {
			prefix = "," + prefix
		}
		readers = append(readers,
			strings.NewReader(prefix),
			strings.NewReader(payloadJSON),
			strings.NewReader(`}]}`),
		)
	}
	readers = append(readers, strings.NewReader(`]}`))
	return io.MultiReader(readers...)
}

func runStreamParseWithMemoryTracking(
	parse func(io.Reader) (int, error),
	sampleCount int,
	payloadSize int,
) (uint64, error) {
	stream := buildSyntheticInspectLogStream(sampleCount, payloadSize)

	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)

	observer := &memoryObservingReader{reader: stream, peakHeap: baseline.HeapAlloc}
	parsedCount, err := parse(observer)
	if err != nil {
		return 0, err
	}
	if parsedCount != sampleCount {
		return 0, fmt.Errorf("expected %d samples, got %d", sampleCount, parsedCount)
	}

	return observer.peakHeap - baseline.HeapAlloc, nil
}

func TestParseInspectSamplesStream_BoundedMemoryAcrossLogSizes(t *testing.T) {
	const payloadSize = 500 * 1024           // 500 KB dropped data per sample
	const maxAllowedGrowth = 5 * 1024 * 1024 // 5 MiB

	parsers := []struct {
		name  string
		parse func(io.Reader) (int, error)
	}{
		{"Inspect", func(reader io.Reader) (int, error) {
			samples, err := ParseInspectSamplesStream(reader)
			return len(samples), err
		}},
		{"TaskDispatch", func(reader io.Reader) (int, error) {
			samples, err := ParseTaskSamplesStream(reader)
			return len(samples), err
		}},
		{"TaskMetrics", func(reader io.Reader) (int, error) {
			metrics, err := ParseTaskMetricsStream(reader)
			if err != nil {
				return 0, err
			}
			return len(metrics.SampleScores), nil
		}},
	}

	for _, parser := range parsers {
		t.Run(parser.name, func(t *testing.T) {
			for _, sampleCount := range []int{10, 30} {
				peakGrowth, err := runStreamParseWithMemoryTracking(parser.parse, sampleCount, payloadSize)
				if err != nil {
					t.Fatalf("stream parse failed for %d samples: %v", sampleCount, err)
				}

				// For a whole-document parser on a 15 MB log, peak HeapAlloc during parse exceeds 25 MB.
				// For streaming one sample at a time, HeapAlloc is bounded by a single sample plus overhead,
				// staying below 5 MB across log sizes.
				if peakGrowth > maxAllowedGrowth {
					t.Errorf("peak HeapAlloc growth %d bytes exceeded bound %d for %d samples", peakGrowth, maxAllowedGrowth, sampleCount)
				}
			}
		})
	}
}

func TestParseTaskMetricsStream(t *testing.T) {
	t.Run("InspectLogWithTrailingEvalAndStats", func(t *testing.T) {
		document := `{"version":2,"samples":[` +
			`{"id":1,"epoch":2,"started_at":"2026-10-21T12:00:00Z","completed_at":"2026-10-21T12:00:02.5Z","scores":{"alpha":{"value":"I"},"primary":{"value":"C"}},"model_usage":{"m":{"input_tokens":10,"output_tokens":5}}},` +
			`{"id":"s-2","total_time":1.25,"scores":{"primary":{"value":0.5}},"model_usage":{"m":{"input_tokens":20,"output_tokens":10}}}],` +
			`"eval":{"scorers":[{"name":"primary"}],"dataset":{"samples":42}},` +
			`"stats":{"model_usage":{"gemini":{"input_tokens":100,"output_tokens":50,"total_tokens":999,"reasoning_tokens":12,"input_tokens_cache_read":30,"input_tokens_cache_write":7}}}}`
		metrics, err := ParseTaskMetricsStream(strings.NewReader(document))
		if err != nil {
			t.Fatalf("ParseTaskMetricsStream failed: %v", err)
		}
		if len(metrics.SampleScores) != 2 || metrics.SampleScores[0].ID != "1#2" || metrics.SampleScores[0].Score != 1.0 || metrics.SampleScores[1].ID != "s-2" || metrics.SampleScores[1].Score != 0.5 {
			t.Errorf("unexpected sample scores: %+v", metrics.SampleScores)
		}
		wantTokens := TaskTokens{Input: 100, Output: 50, Total: 150, Reasoning: 12, CacheRead: 30, CacheWrite: 7}
		if metrics.Tokens == nil || *metrics.Tokens != wantTokens {
			t.Errorf("tokens = %+v, want %+v", metrics.Tokens, wantTokens)
		}
		if len(metrics.Latencies) != 2 || metrics.Latencies[0] != 2.5 || metrics.Latencies[1] != 1.25 || metrics.DatasetSamples != 42 {
			t.Errorf("unexpected latencies=%v datasetSamples=%d", metrics.Latencies, metrics.DatasetSamples)
		}
	})

	t.Run("InspectLogPerSampleTokenFallback", func(t *testing.T) {
		document := `{"version":2,"samples":[` +
			`{"id":"1","scores":{"acc":{"value":1.0}},"model_usage":{"m":{"input_tokens":10,"output_tokens":5,"reasoning_tokens":3,"input_tokens_cache_read":4,"input_tokens_cache_write":2}}},` +
			`{"id":"2","scores":{"acc":{"value":0.0}},"model_usage":{"m":{"input_tokens":20,"output_tokens":15}}}]}`
		metrics, err := ParseTaskMetricsStream(strings.NewReader(document))
		if err != nil {
			t.Fatalf("ParseTaskMetricsStream failed: %v", err)
		}
		wantTokens := TaskTokens{Input: 30, Output: 20, Total: 50, Reasoning: 3, CacheRead: 4, CacheWrite: 2}
		if metrics.Tokens == nil || *metrics.Tokens != wantTokens {
			t.Errorf("fallback tokens = %+v, want %+v", metrics.Tokens, wantTokens)
		}
	})

	t.Run("ResultsJSONKeyOrders", func(t *testing.T) {
		documents := []string{
			`{"schemaVersion":1,"samples":[{"id":"s1","passed":true},{"id":"s2","passed":false,"score":0.25}]}`,
			`{"samples":[{"id":"s1","passed":true},{"id":"s2","passed":false,"score":0.25}],"schemaVersion":1}`,
		}
		for index, document := range documents {
			metrics, err := ParseTaskMetricsStream(strings.NewReader(document))
			if err != nil {
				t.Fatalf("case %d: ParseTaskMetricsStream failed: %v", index, err)
			}
			if len(metrics.SampleScores) != 2 || metrics.SampleScores[0].ID != "s1" || metrics.SampleScores[0].Score != 1.0 || metrics.SampleScores[1].ID != "s2" || metrics.SampleScores[1].Score != 0.25 {
				t.Errorf("case %d: unexpected scores: %+v", index, metrics.SampleScores)
			}
			if metrics.Tokens != nil || len(metrics.Latencies) != 0 || metrics.DatasetSamples != 0 {
				t.Errorf("case %d: unexpected inspect-only fields: %+v", index, metrics)
			}
		}
	})

	t.Run("Errors", func(t *testing.T) {
		for _, testCase := range []struct{ document, expectedError string }{
			{"  ", "empty artifact data"},
			{"{bad", "failed to parse task log"},
			{`{"schemaVersion":2,"samples":[]}`, "unsupported results schemaVersion 2"},
			{`{"schemaVersion":1,"samples":[{"id":"s1"}]}`, "missing required 'passed' field"},
		} {
			if _, err := ParseTaskMetricsStream(strings.NewReader(testCase.document)); err == nil || !strings.Contains(err.Error(), testCase.expectedError) {
				t.Errorf("document %q: expected error containing %q, got %v", testCase.document, testCase.expectedError, err)
			}
		}
	})
}
