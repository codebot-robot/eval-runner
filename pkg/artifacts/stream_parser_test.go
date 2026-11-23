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

func runStreamParseWithMemoryTracking(sampleCount int, payloadSize int) (uint64, error) {
	stream := buildSyntheticInspectLogStream(sampleCount, payloadSize)

	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)

	observer := &memoryObservingReader{reader: stream, peakHeap: baseline.HeapAlloc}
	samples, err := ParseInspectSamplesStream(observer)
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

	for _, sampleCount := range []int{10, 30} {
		peakGrowth, err := runStreamParseWithMemoryTracking(sampleCount, payloadSize)
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
}
