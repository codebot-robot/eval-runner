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

package controller

import (
	"strings"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

// PricesUpdated is the date when default model prices were last updated.
const PricesUpdated = "2026-10-21"

// DefaultModelPrices returns a map of built-in default model prices in USD per 1M tokens.
// These cover models the project ships or documents.
func DefaultModelPrices() map[string]ModelPrice {
	return map[string]ModelPrice{
		// Source: https://ai.google.dev/pricing (checked 2026-10-21)
		"google/gemini-3.5-flash-lite": {InputPer1M: 0.30, OutputPer1M: 2.50},
		// Source: https://ai.google.dev/pricing (checked 2026-10-21)
		"google/gemini-2.5-flash": {InputPer1M: 0.30, OutputPer1M: 2.50},
		// Source: https://ai.google.dev/pricing (checked 2026-10-21)
		"google/gemini-2.5-pro": {InputPer1M: 1.25, OutputPer1M: 10.00},
		// Source: internal mock runner (zero cost, checked 2026-10-21)
		"mockllm/model": {InputPer1M: 0.0, OutputPer1M: 0.0},
	}
}

// DefaultModelPriceSources returns the source provenance for DefaultModelPrices.
func DefaultModelPriceSources() map[string]v1alpha1.CostSource {
	defaults := DefaultModelPrices()
	sources := make(map[string]v1alpha1.CostSource, len(defaults))
	for model := range defaults {
		sources[model] = v1alpha1.CostSourceDefault
	}
	return sources
}

// LookupModelPrice resolves pricing for model against the effective prices and source map.
//
// Resolution order:
//  1. Exact match from ConfigMap
//  2. Longest prefix match from ConfigMap (keys formatted as "prefix:<model-prefix>")
//  3. Exact match from built-in defaults (or fallback if sources is unspecified)
//
// Returns (price, costSource, found).
func LookupModelPrice(model string, prices map[string]ModelPrice, sources map[string]v1alpha1.CostSource) (ModelPrice, v1alpha1.CostSource, bool) {
	if prices == nil {
		return ModelPrice{}, "", false
	}

	// 1. Exact match from configmap
	if sources != nil {
		if sources[model] == v1alpha1.CostSourceConfigMap {
			if p, ok := prices[model]; ok {
				return p, v1alpha1.CostSourceConfigMap, true
			}
		}
	} else {
		if p, ok := prices[model]; ok {
			defP, isDef := DefaultModelPrices()[model]
			if !isDef || defP != p {
				return p, v1alpha1.CostSourceConfigMap, true
			}
		}
	}

	// 2. Longest prefix match from configmap ("prefix:<model-prefix>")
	var bestConfigMapPrice ModelPrice
	bestConfigMapPrefixLen := -1
	for k, p := range prices {
		if strings.HasPrefix(k, "prefix:") {
			pfx := strings.TrimPrefix(k, "prefix:")
			if pfx != "" && strings.HasPrefix(model, pfx) {
				if sources == nil || sources[k] == v1alpha1.CostSourceConfigMap {
					if len(pfx) > bestConfigMapPrefixLen {
						bestConfigMapPrefixLen = len(pfx)
						bestConfigMapPrice = p
					}
				}
			}
		}
	}
	if bestConfigMapPrefixLen >= 0 {
		return bestConfigMapPrice, v1alpha1.CostSourceConfigMap, true
	}

	// 3. Exact match from defaults (or fallback if sources is unspecified)
	if p, ok := prices[model]; ok {
		src := v1alpha1.CostSourceDefault
		if sources != nil && sources[model] != "" {
			src = sources[model]
		}
		return p, src, true
	}

	return ModelPrice{}, "", false
}
