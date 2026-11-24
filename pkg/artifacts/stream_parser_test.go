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
	parse func(io.Reader) ([]ParsedInspectSample, error),
	sampleCount int,
	payloadSize int,
) (uint64, error) {
	stream := buildSyntheticInspectLogStream(sampleCount, payloadSize)

	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)

	observer := &memoryObservingReader{reader: stream, peakHeap: baseline.HeapAlloc}
	samples, err := parse(observer)
	if err != nil {
		return 0, err
	}
	if len(samples) != sampleCount {
		return 0, fmt.Errorf("expected %d samples, got %d", sampleCount, len(samples))
	}

	return observer.peakHeap - baseline.HeapAlloc, nil
}

func TestParseInspectSamplesStream_BoundedMemoryAcrossLogSizes(t *testing.T) {
	const payloadSize = 500 * 1024           // 500 KB dropped data per sample
	const maxAllowedGrowth = 5 * 1024 * 1024 // 5 MiB

	parsers := []struct {
		name  string
		parse func(io.Reader) ([]ParsedInspectSample, error)
	}{
		{name: "Inspect", parse: ParseInspectSamplesStream},
		{name: "TaskDispatch", parse: ParseTaskSamplesStream},
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
