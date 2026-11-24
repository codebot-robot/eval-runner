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
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"

	"github.com/gke-labs/eval-runner/pkg/stats"
)

// streamingJSONArray implements json.UnmarshalerFrom to decode a JSON array
// one element at a time without buffering the full array in memory.
type streamingJSONArray struct {
	onElement func(index int, decoder *jsontext.Decoder) error
}

func (array *streamingJSONArray) UnmarshalJSONFrom(decoder *jsontext.Decoder) error {
	token, err := decoder.ReadToken()
	if err != nil {
		return fmt.Errorf("failed to read samples token: %w", err)
	}
	if token.Kind() == jsontext.KindNull {
		return nil
	}
	if token.Kind() != jsontext.KindBeginArray {
		return fmt.Errorf("expected JSON array for samples, got %s", token.Kind())
	}
	for index := 0; decoder.PeekKind() != jsontext.KindEndArray; index++ {
		if err := array.onElement(index, decoder); err != nil {
			return err
		}
	}
	if _, err := decoder.ReadToken(); err != nil {
		return fmt.Errorf("failed to read closing samples delimiter: %w", err)
	}
	return nil
}

func unmarshalJSONStream(reader io.Reader, target any) error {
	decoder := jsontext.NewDecoder(reader)
	if err := json.UnmarshalDecode(decoder, target); err != nil {
		return err
	}
	if _, err := decoder.ReadToken(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON data")
		}
		return err
	}
	return nil
}

func decodeResultsSample(index int, decoder *jsontext.Decoder) (ParsedInspectSample, error) {
	var sample SampleResult
	if err := json.UnmarshalDecode(decoder, &sample); err != nil {
		return ParsedInspectSample{}, fmt.Errorf("failed to decode sample: %w", err)
	}
	return convertResultsSample(index, sample)
}

type inspectSampleCollector struct {
	samples []ParsedInspectSample
	scores  []map[string]InspectSampleScore
}

func (collector *inspectSampleCollector) add(sample InspectSample) {
	if parsedSample, ok := convertInspectSample(sample); ok {
		collector.samples = append(collector.samples, parsedSample)
		collector.scores = append(collector.scores, sample.Scores)
	}
}

func (collector *inspectSampleCollector) decodeFrom(decoder *jsontext.Decoder) error {
	var sample InspectSample
	if err := json.UnmarshalDecode(decoder, &sample); err != nil {
		return fmt.Errorf("failed to decode sample: %w", err)
	}
	collector.add(sample)
	return nil
}

func (collector *inspectSampleCollector) unmarshalFrom(rawSample jsontext.Value) error {
	var sample InspectSample
	if err := json.Unmarshal(rawSample, &sample); err != nil {
		return fmt.Errorf("failed to decode sample: %w", err)
	}
	collector.add(sample)
	return nil
}

func (collector *inspectSampleCollector) finalize(eval InspectEval, results *InspectResults) []ParsedInspectSample {
	if len(collector.samples) == 0 {
		return nil
	}
	primaryScorer := resolvePrimaryScorer(eval, results)
	for index := range collector.samples {
		scoreInspectSample(&collector.samples[index], collector.scores[index], primaryScorer)
	}
	return collector.samples
}

func resolvePrimaryScorer(eval InspectEval, results *InspectResults) string {
	if len(eval.Scorers) > 0 {
		return eval.Scorers[0].Name
	}
	if results != nil && len(results.Scores) > 0 {
		return results.Scores[0].Name
	}
	return ""
}

// ParseInspectSamplesStream parses an Inspect JSON log from an io.Reader stream one sample at a time.
func ParseInspectSamplesStream(reader io.Reader) ([]ParsedInspectSample, error) {
	var collector inspectSampleCollector
	document := struct {
		Eval    InspectEval        `json:"eval"`
		Results *InspectResults    `json:"results"`
		Samples streamingJSONArray `json:"samples"`
	}{
		Samples: streamingJSONArray{
			onElement: func(_ int, decoder *jsontext.Decoder) error {
				return collector.decodeFrom(decoder)
			},
		},
	}

	if err := unmarshalJSONStream(reader, &document); err != nil {
		return nil, fmt.Errorf("failed to read inspect log: %w", err)
	}
	return collector.finalize(document.Eval, document.Results), nil
}

// ParseResultsSamplesStream parses a sandbox results.json artifact from an io.Reader stream
// one sample at a time.
func ParseResultsSamplesStream(reader io.Reader) ([]ParsedInspectSample, error) {
	var parsedSamples []ParsedInspectSample

	document := struct {
		SchemaVersion int                `json:"schemaVersion"`
		Samples       streamingJSONArray `json:"samples"`
	}{}
	document.Samples = streamingJSONArray{
		onElement: func(index int, decoder *jsontext.Decoder) error {
			if document.SchemaVersion != 0 && document.SchemaVersion != 1 {
				return decoder.SkipValue()
			}
			parsedSample, err := decodeResultsSample(index, decoder)
			if err != nil {
				return err
			}
			parsedSamples = append(parsedSamples, parsedSample)
			return nil
		},
	}

	if err := unmarshalJSONStream(reader, &document); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("empty results data")
		}
		return nil, fmt.Errorf("failed to unmarshal results JSON: %w", err)
	}
	if document.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported results schemaVersion %d, expected 1", document.SchemaVersion)
	}
	if len(parsedSamples) == 0 {
		return nil, nil
	}
	return parsedSamples, nil
}

type taskLogDocument struct {
	SchemaVersion int                `json:"schemaVersion"`
	Eval          InspectEval        `json:"eval"`
	Results       *InspectResults    `json:"results"`
	Stats         *InspectStats      `json:"stats"`
	Samples       streamingJSONArray `json:"samples"`
}

type taskStreamCallbacks struct {
	decodeInspect    func(decoder *jsontext.Decoder) error
	unmarshalInspect func(rawSample jsontext.Value) error
	resetInspect     func()
	addResult        func(sample ParsedInspectSample)
	resetResults     func()
}

func decodeDualTaskSample(index int, decoder *jsontext.Decoder, callbacks taskStreamCallbacks) (error, error, error) {
	rawSample, err := decoder.ReadValue()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read sample: %w", err)
	}

	var inspectErr, resultsErr error
	if err := callbacks.unmarshalInspect(rawSample); err != nil {
		inspectErr = err
		callbacks.resetInspect()
	}

	var resultSample SampleResult
	if err := json.Unmarshal(rawSample, &resultSample); err != nil {
		resultsErr = fmt.Errorf("failed to decode sample: %w", err)
		callbacks.resetResults()
	} else if parsedSample, err := convertResultsSample(index, resultSample); err != nil {
		resultsErr = err
		callbacks.resetResults()
	} else {
		callbacks.addResult(parsedSample)
	}
	return inspectErr, resultsErr, nil
}

func parseTaskLogStream(reader io.Reader, callbacks taskStreamCallbacks) (taskLogDocument, error) {
	var (
		inspectErr error
		resultsErr error
		document   taskLogDocument
	)
	decodeAndAddResult := func(index int, decoder *jsontext.Decoder) error {
		parsedSample, err := decodeResultsSample(index, decoder)
		if err != nil {
			return err
		}
		callbacks.addResult(parsedSample)
		return nil
	}

	document.Samples = streamingJSONArray{
		onElement: func(index int, decoder *jsontext.Decoder) error {
			if document.SchemaVersion > 0 {
				if document.SchemaVersion != 1 {
					return decoder.SkipValue()
				}
				return decodeAndAddResult(index, decoder)
			}
			switch {
			case inspectErr == nil && resultsErr == nil:
				var readErr error
				inspectErr, resultsErr, readErr = decodeDualTaskSample(index, decoder, callbacks)
				return readErr
			case inspectErr == nil:
				return callbacks.decodeInspect(decoder)
			case resultsErr == nil:
				return decodeAndAddResult(index, decoder)
			default:
				return decoder.SkipValue()
			}
		},
	}

	if err := unmarshalJSONStream(reader, &document); err != nil {
		if errors.Is(err, io.EOF) {
			return document, fmt.Errorf("empty artifact data")
		}
		return document, fmt.Errorf("failed to parse task log: %w", err)
	}
	if document.SchemaVersion > 0 {
		if document.SchemaVersion != 1 {
			return document, fmt.Errorf("unsupported results schemaVersion %d, expected 1", document.SchemaVersion)
		}
		return document, resultsErr
	}
	return document, inspectErr
}

// ParseTaskSamplesStream parses samples from either a sandbox results.json artifact or an
// Inspect JSON evaluation log from an io.Reader stream one sample at a time, dispatching on
// top-level schemaVersion regardless of key order.
func ParseTaskSamplesStream(reader io.Reader) ([]ParsedInspectSample, error) {
	var (
		inspectCollector inspectSampleCollector
		resultsSamples   []ParsedInspectSample
	)
	document, err := parseTaskLogStream(reader, taskStreamCallbacks{
		decodeInspect:    inspectCollector.decodeFrom,
		unmarshalInspect: inspectCollector.unmarshalFrom,
		resetInspect:     func() { inspectCollector = inspectSampleCollector{} },
		addResult:        func(sample ParsedInspectSample) { resultsSamples = append(resultsSamples, sample) },
		resetResults:     func() { resultsSamples = nil },
	})
	if err != nil {
		return nil, err
	}
	if document.SchemaVersion > 0 {
		if len(resultsSamples) == 0 {
			return nil, nil
		}
		return resultsSamples, nil
	}
	return inspectCollector.finalize(document.Eval, document.Results), nil
}

// TaskLogMetrics holds per-sample scores, token usage, sample latencies, and dataset size
// extracted from a task evaluation log (Inspect JSON or sandbox results.json) in a single pass.
type TaskLogMetrics struct {
	SampleScores   []stats.SampleScore
	Tokens         *TaskTokens
	Latencies      []float64
	DatasetSamples int
}

type inspectScoreValue struct {
	Value any `json:"value"`
}

type inspectMetricsSample struct {
	ID          any                          `json:"id"`
	Epoch       int                          `json:"epoch,omitempty"`
	StartedAt   string                       `json:"started_at,omitempty"`
	CompletedAt string                       `json:"completed_at,omitempty"`
	TotalTime   float64                      `json:"total_time,omitempty"`
	Scores      map[string]inspectScoreValue `json:"scores,omitempty"`
	ModelUsage  map[string]InspectModelUsage `json:"model_usage,omitempty"`
}

type pendingInspectScore struct {
	id     string
	scores map[string]inspectScoreValue
}

type inspectMetricsCollector struct {
	pendingScores   []pendingInspectScore
	latencies       []float64
	sampleTokens    TaskTokens
	hasSampleTokens bool
}

func addModelUsage(target *TaskTokens, modelUsage map[string]InspectModelUsage) bool {
	found := false
	for _, usage := range modelUsage {
		if usage.TotalTokens > 0 || usage.InputTokens > 0 || usage.OutputTokens > 0 ||
			usage.ReasoningTokens > 0 || usage.InputTokensCacheRead > 0 || usage.InputTokensCacheWrite > 0 {
			target.Input += usage.InputTokens
			target.Output += usage.OutputTokens
			target.Reasoning += usage.ReasoningTokens
			target.CacheRead += usage.InputTokensCacheRead
			target.CacheWrite += usage.InputTokensCacheWrite
			found = true
		}
	}
	if found {
		target.Total = target.Input + target.Output
	}
	return found
}

func (collector *inspectMetricsCollector) add(sample inspectMetricsSample) {
	if addModelUsage(&collector.sampleTokens, sample.ModelUsage) {
		collector.hasSampleTokens = true
	}

	startTime, hasStart := parseSampleTime(sample.StartedAt)
	endTime, hasEnd := parseSampleTime(sample.CompletedAt)
	if hasStart && hasEnd && !endTime.Before(startTime) {
		collector.latencies = append(collector.latencies, endTime.Sub(startTime).Seconds())
	} else if sample.TotalTime > 0 {
		collector.latencies = append(collector.latencies, sample.TotalTime)
	}

	if len(sample.Scores) == 0 {
		return
	}
	sampleID := FormatSampleID(sample.ID, sample.Epoch)
	if sampleID == "" {
		return
	}
	collector.pendingScores = append(collector.pendingScores, pendingInspectScore{
		id:     sampleID,
		scores: sample.Scores,
	})
}

func (collector *inspectMetricsCollector) decodeFrom(decoder *jsontext.Decoder) error {
	var sample inspectMetricsSample
	if err := json.UnmarshalDecode(decoder, &sample); err != nil {
		return fmt.Errorf("failed to decode sample: %w", err)
	}
	collector.add(sample)
	return nil
}

func (collector *inspectMetricsCollector) unmarshalFrom(rawSample jsontext.Value) error {
	var sample inspectMetricsSample
	if err := json.Unmarshal(rawSample, &sample); err != nil {
		return fmt.Errorf("failed to decode sample: %w", err)
	}
	collector.add(sample)
	return nil
}

func (collector *inspectMetricsCollector) finalize(eval InspectEval, results *InspectResults, inspectStats *InspectStats) *TaskLogMetrics {
	metrics := &TaskLogMetrics{
		Latencies:      collector.latencies,
		DatasetSamples: eval.DatasetSamples(),
	}

	var statsTokens TaskTokens
	if inspectStats != nil && addModelUsage(&statsTokens, inspectStats.ModelUsage) {
		metrics.Tokens = &statsTokens
	} else if collector.hasSampleTokens {
		sampleTokens := collector.sampleTokens
		metrics.Tokens = &sampleTokens
	}

	if len(collector.pendingScores) == 0 {
		return metrics
	}

	primaryScorer := resolvePrimaryScorer(eval, results)
	for _, pending := range collector.pendingScores {
		targetScore, found := selectPrimaryScore(pending.scores, primaryScorer)
		if !found {
			continue
		}
		scoreValue, valid := ScoreToFloat(targetScore.Value)
		if !valid {
			continue
		}
		metrics.SampleScores = append(metrics.SampleScores, stats.SampleScore{
			ID:    pending.id,
			Score: scoreValue,
		})
	}
	return metrics
}

// ParseTaskMetricsStream extracts sample scores, token usage, sample latencies, and dataset size
// from either a sandbox results.json artifact or an Inspect JSON evaluation log in a single
// streaming pass, dispatching on top-level schemaVersion regardless of key order.
func ParseTaskMetricsStream(reader io.Reader) (*TaskLogMetrics, error) {
	var (
		inspectCollector inspectMetricsCollector
		resultsScores    []stats.SampleScore
	)
	document, err := parseTaskLogStream(reader, taskStreamCallbacks{
		decodeInspect:    inspectCollector.decodeFrom,
		unmarshalInspect: inspectCollector.unmarshalFrom,
		resetInspect:     func() { inspectCollector = inspectMetricsCollector{} },
		addResult: func(sample ParsedInspectSample) {
			resultsScores = append(resultsScores, stats.SampleScore{ID: sample.ID, Score: *sample.Score})
		},
		resetResults: func() { resultsScores = nil },
	})
	if err != nil {
		return nil, err
	}
	if document.SchemaVersion > 0 {
		return &TaskLogMetrics{SampleScores: resultsScores}, nil
	}
	return inspectCollector.finalize(document.Eval, document.Results, document.Stats), nil
}
