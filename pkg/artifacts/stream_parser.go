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
	"fmt"
	"io"
)

// parseSamplesArray decodes the samples JSON array one sample at a time.
func parseSamplesArray(decoder *jsontext.Decoder) ([]ParsedInspectSample, []map[string]InspectSampleScore, error) {
	token, err := decoder.ReadToken()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read samples token: %w", err)
	}
	if token.Kind() == jsontext.KindNull {
		return nil, nil, nil
	}
	if token.Kind() != jsontext.KindBeginArray {
		return nil, nil, fmt.Errorf("expected JSON array for samples, got %s", token.Kind())
	}

	var parsedSamples []ParsedInspectSample
	var sampleScores []map[string]InspectSampleScore
	for decoder.PeekKind() != jsontext.KindEndArray {
		var sample InspectSample
		if err := json.UnmarshalDecode(decoder, &sample); err != nil {
			return nil, nil, fmt.Errorf("failed to decode sample: %w", err)
		}
		if parsedSample, ok := convertInspectSample(sample); ok {
			parsedSamples = append(parsedSamples, parsedSample)
			sampleScores = append(sampleScores, sample.Scores)
		}
	}
	if _, err := decoder.ReadToken(); err != nil {
		return nil, nil, fmt.Errorf("failed to read closing samples delimiter: %w", err)
	}
	return parsedSamples, sampleScores, nil
}

// ParseInspectSamplesStream parses an Inspect JSON log from an io.Reader stream one sample at a time.
func ParseInspectSamplesStream(reader io.Reader) ([]ParsedInspectSample, error) {
	decoder := jsontext.NewDecoder(reader)
	token, err := decoder.ReadToken()
	if err != nil {
		return nil, fmt.Errorf("failed to read inspect log: %w", err)
	}
	if token.Kind() == jsontext.KindNull {
		return nil, nil
	}
	if token.Kind() != jsontext.KindBeginObject {
		return nil, fmt.Errorf("expected JSON object for inspect log, got %s", token.Kind())
	}

	var eval InspectEval
	var results *InspectResults
	var parsedSamples []ParsedInspectSample
	var sampleScores []map[string]InspectSampleScore

	for decoder.PeekKind() != jsontext.KindEndObject {
		keyToken, err := decoder.ReadToken()
		if err != nil {
			return nil, fmt.Errorf("failed to read inspect log key: %w", err)
		}
		key := keyToken.String()
		switch key {
		case "eval":
			err = json.UnmarshalDecode(decoder, &eval)
		case "results":
			err = json.UnmarshalDecode(decoder, &results)
		case "samples":
			parsedSamples, sampleScores, err = parseSamplesArray(decoder)
		default:
			err = decoder.SkipValue()
		}
		if err != nil {
			return nil, fmt.Errorf("failed to parse inspect log field %q: %w", key, err)
		}
	}
	if _, err := decoder.ReadToken(); err != nil {
		return nil, fmt.Errorf("failed to read closing object delimiter: %w", err)
	}
	if len(parsedSamples) == 0 {
		return nil, nil
	}

	primaryScorer := ""
	if len(eval.Scorers) > 0 {
		primaryScorer = eval.Scorers[0].Name
	} else if results != nil && len(results.Scores) > 0 {
		primaryScorer = results.Scores[0].Name
	}
	for index := range parsedSamples {
		scoreInspectSample(&parsedSamples[index], sampleScores[index], primaryScorer)
	}
	return parsedSamples, nil
}
