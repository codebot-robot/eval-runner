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

package discovery

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

// ParseManifest unmarshals raw JSON bytes into a Manifest.
func ParseManifest(data []byte) (*v1alpha1.Manifest, error) {
	var m v1alpha1.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to parse manifest JSON: %w", err)
	}
	return &m, nil
}

// ValidateManifest validates the discovered manifest:
// - Manifest must not be nil
// - tasks must not be empty
// - tasks must not exceed MaxTasks
// - Each task must have a non-empty, unique ID
// - Each task must have a non-empty Spec
func ValidateManifest(m *v1alpha1.Manifest) error {
	if m == nil {
		return fmt.Errorf("manifest cannot be nil")
	}

	if len(m.Tasks) == 0 {
		return fmt.Errorf("manifest contains no tasks")
	}

	if len(m.Tasks) > MaxTasks {
		return fmt.Errorf("manifest contains %d tasks, exceeding maximum allowed (%d)", len(m.Tasks), MaxTasks)
	}

	seenIDs := make(map[string]struct{}, len(m.Tasks))
	for i, task := range m.Tasks {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			return fmt.Errorf("task at index %d has empty id", i)
		}
		if _, exists := seenIDs[id]; exists {
			return fmt.Errorf("duplicate task id %q at index %d", id, i)
		}
		seenIDs[id] = struct{}{}

		if strings.TrimSpace(task.Spec) == "" {
			return fmt.Errorf("task %q has empty spec", id)
		}
	}

	return nil
}
