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

func (collector *inspectSampleCollector) finalize(eval InspectEval, results *InspectResults) []ParsedInspectSample {
	if len(collector.samples) == 0 {
		return nil
	}
	primaryScorer := ""
	if len(eval.Scorers) > 0 {
		primaryScorer = eval.Scorers[0].Name
	} else if results != nil && len(results.Scores) > 0 {
		primaryScorer = results.Scores[0].Name
	}
	for index := range collector.samples {
		scoreInspectSample(&collector.samples[index], collector.scores[index], primaryScorer)
	}
	return collector.samples
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

// ParseTaskSamplesStream parses samples from either a sandbox results.json artifact or an
// Inspect JSON evaluation log from an io.Reader stream one sample at a time, dispatching on
// top-level schemaVersion regardless of key order.
func ParseTaskSamplesStream(reader io.Reader) ([]ParsedInspectSample, error) {
	var (
		inspectCollector inspectSampleCollector
		inspectErr       error

		resultsSamples []ParsedInspectSample
		resultsErr     error
	)

	decodeAndAppendResult := func(index int, decoder *jsontext.Decoder) error {
		parsedSample, err := decodeResultsSample(index, decoder)
		if err != nil {
			return err
		}
		resultsSamples = append(resultsSamples, parsedSample)
		return nil
	}

	document := struct {
		SchemaVersion int                `json:"schemaVersion"`
		Eval          InspectEval        `json:"eval"`
		Results       *InspectResults    `json:"results"`
		Samples       streamingJSONArray `json:"samples"`
	}{}
	document.Samples = streamingJSONArray{
		onElement: func(index int, decoder *jsontext.Decoder) error {
			if document.SchemaVersion > 0 {
				if document.SchemaVersion != 1 {
					return decoder.SkipValue()
				}
				return decodeAndAppendResult(index, decoder)
			}

			switch {
			case inspectErr == nil && resultsErr == nil:
				rawSample, err := decoder.ReadValue()
				if err != nil {
					return fmt.Errorf("failed to read sample: %w", err)
				}

				var inspectSample InspectSample
				if err := json.Unmarshal(rawSample, &inspectSample); err != nil {
					inspectErr = fmt.Errorf("failed to decode sample: %w", err)
					inspectCollector = inspectSampleCollector{}
				} else {
					inspectCollector.add(inspectSample)
				}

				var resultSample SampleResult
				if err := json.Unmarshal(rawSample, &resultSample); err != nil {
					resultsErr = fmt.Errorf("failed to decode sample: %w", err)
					resultsSamples = nil
				} else if parsedSample, err := convertResultsSample(index, resultSample); err != nil {
					resultsErr = err
					resultsSamples = nil
				} else {
					resultsSamples = append(resultsSamples, parsedSample)
				}
				return nil

			case inspectErr == nil:
				return inspectCollector.decodeFrom(decoder)

			case resultsErr == nil:
				return decodeAndAppendResult(index, decoder)

			default:
				return decoder.SkipValue()
			}
		},
	}

	if err := unmarshalJSONStream(reader, &document); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("empty artifact data")
		}
		return nil, fmt.Errorf("failed to parse task log: %w", err)
	}

	if document.SchemaVersion > 0 {
		if document.SchemaVersion != 1 {
			return nil, fmt.Errorf("unsupported results schemaVersion %d, expected 1", document.SchemaVersion)
		}
		if resultsErr != nil {
			return nil, resultsErr
		}
		if len(resultsSamples) == 0 {
			return nil, nil
		}
		return resultsSamples, nil
	}

	if inspectErr != nil {
		return nil, inspectErr
	}
	return inspectCollector.finalize(document.Eval, document.Results), nil
}
