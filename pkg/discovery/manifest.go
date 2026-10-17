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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

// ManifestHash returns the SHA-256 digest of data formatted as sha256:<hex>.
func ManifestHash(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", h)
}

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
// - Each task must have a non-empty Spec that does not start with '-'
// - Each task's Workdir (if set) must be relative and not contain '..'
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

		spec := strings.TrimSpace(task.Spec)
		if spec == "" {
			return fmt.Errorf("task %q has empty spec", id)
		}
		if strings.HasPrefix(spec, "-") {
			return fmt.Errorf("task %q has invalid spec %q: must not start with '-'", id, task.Spec)
		}

		if task.Workdir != "" {
			cleanWorkdir := filepath.Clean(task.Workdir)
			if filepath.IsAbs(task.Workdir) || strings.HasPrefix(cleanWorkdir, "..") || cleanWorkdir == ".." || strings.Contains(task.Workdir, "..") {
				return fmt.Errorf("task %q has invalid workdir %q: must be relative and not contain '..'", id, task.Workdir)
			}
		}
	}

	return nil
}
