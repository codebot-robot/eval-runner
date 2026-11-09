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
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/affinity"
	"k8s.io/apimachinery/pkg/api/resource"
)

var (
	validCohortRegex = regexp.MustCompile(`^[a-zA-Z0-9]([-_a-zA-Z0-9.]*[a-zA-Z0-9])?$`)

	maxTaskCPULimit    = resource.MustParse(v1alpha1.MaxTaskCPULimit)
	maxTaskMemoryLimit = resource.MustParse(v1alpha1.MaxTaskMemoryLimit)
)

const (
	// MaxManifestBytes is the maximum allowed size of a manifest JSON file (1 MiB).
	MaxManifestBytes int64 = 1 * 1024 * 1024

	// DiscoveryManifestWorkspaceDir is the directory in the sandbox pod where discovery writes the manifest.
	DiscoveryManifestWorkspaceDir = "/workspace/out"

	// DiscoveryManifestFilename is the filename of the discovered manifest JSON.
	DiscoveryManifestFilename = "manifest.json"

	// DiscoveryManifestWritePath is the absolute path inside the sandbox where discovery writes manifest.json.
	// The discovery step executes: mkdir -p /workspace/out && ./.evals/list > /workspace/out/manifest.json
	DiscoveryManifestWritePath = "/workspace/out/manifest.json"

	// DiscoveryManifestReadPath is the relative path from sandboxd's root-dir (/workspace) used by REST ReadFile.
	// Since sandboxd runs with --root-dir=/workspace, requesting /out/manifest.json resolves to /workspace/out/manifest.json.
	DiscoveryManifestReadPath = "/out/manifest.json"

	// DiscoveryCommand is the shell command used to discover tasks and write the manifest file.
	DiscoveryCommand = "mkdir -p /workspace/out && ./.evals/list > /workspace/out/manifest.json"
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
// - Each task must have a non-empty Spec that does not start with '-'
// - Each task's Workdir (if set) must be relative and not contain '..'
// - Each task specifying command requires a non-blank image
// - Each task specifying sandbox resource requires a non-blank image
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

		if len(task.Command) > 0 && strings.TrimSpace(task.Image) == "" {
			return fmt.Errorf("task %q specifies command without image: command requires a non-blank image", id)
		}

		for _, resource := range task.Resources {
			if resource.Type == v1alpha1.ResourceTypeSandbox && strings.TrimSpace(task.Image) == "" {
				return fmt.Errorf("task %q specifies sandbox resource without image: sandbox resource requires a non-blank image", id)
			}
		}

		if task.Workdir != "" {
			cleanWorkdir := filepath.Clean(task.Workdir)
			if filepath.IsAbs(task.Workdir) || strings.HasPrefix(cleanWorkdir, "..") || cleanWorkdir == ".." || strings.Contains(task.Workdir, "..") {
				return fmt.Errorf("task %q has invalid workdir %q: must be relative and not contain '..'", id, task.Workdir)
			}
		}

		if len(task.Paths) > MaxTaskPaths {
			return fmt.Errorf("task %q has %d paths, exceeding maximum allowed (%d)", id, len(task.Paths), MaxTaskPaths)
		}
		for _, p := range task.Paths {
			if len(p) == 0 {
				return fmt.Errorf("task %q has empty path pattern", id)
			}
			if len(p) > MaxPathLength {
				return fmt.Errorf("task %q path pattern exceeds maximum length (%d)", id, MaxPathLength)
			}
			if err := affinity.ValidatePattern(p); err != nil {
				return fmt.Errorf("task %q has invalid path pattern %q: %w", id, p, err)
			}
		}

		if task.Cohort == "" {
			m.Tasks[i].Cohort = v1alpha1.DefaultCohort
		} else {
			if len(task.Cohort) > MaxCohortLength {
				return fmt.Errorf("task %q cohort %q exceeds maximum length (%d)", id, task.Cohort, MaxCohortLength)
			}
			if !validCohortRegex.MatchString(task.Cohort) {
				return fmt.Errorf("task %q has invalid cohort %q: must be label-safe (alphanumeric, '-', '_', or '.', starting and ending with alphanumeric)", id, task.Cohort)
			}
		}
	}

	return nil
}

// ValidateTaskResources validates declarative resource requirements for a task:
// - cpu and memory limits are only valid on resources of type sandbox.
// - cpu and memory limits must be valid Kubernetes quantities.
// - cpu and memory limits must be positive (> 0).
// - cpu limit must not exceed MaxTaskCPULimit (16).
// - memory limit must not exceed MaxTaskMemoryLimit (64Gi).
func ValidateTaskResources(task v1alpha1.ManifestTask) error {
	taskID := task.ID
	for _, manifestResource := range task.Resources {
		if manifestResource.Type != v1alpha1.ResourceTypeSandbox {
			if manifestResource.CPU != "" {
				return fmt.Errorf("task %q has invalid cpu %q: cpu limit is only valid on sandbox resources", taskID, manifestResource.CPU)
			}
			if manifestResource.Memory != "" {
				return fmt.Errorf("task %q has invalid memory %q: memory limit is only valid on sandbox resources", taskID, manifestResource.Memory)
			}
			continue
		}

		if manifestResource.CPU != "" {
			quantity, err := resource.ParseQuantity(manifestResource.CPU)
			if err != nil {
				return fmt.Errorf("task %q has invalid cpu %q: %w", taskID, manifestResource.CPU, err)
			}
			if quantity.Sign() <= 0 {
				return fmt.Errorf("task %q has invalid cpu %q: must be positive", taskID, manifestResource.CPU)
			}
			if quantity.Cmp(maxTaskCPULimit) > 0 {
				return fmt.Errorf("task %q has invalid cpu %q: exceeds maximum allowed %s", taskID, manifestResource.CPU, v1alpha1.MaxTaskCPULimit)
			}
		}

		if manifestResource.Memory != "" {
			quantity, err := resource.ParseQuantity(manifestResource.Memory)
			if err != nil {
				return fmt.Errorf("task %q has invalid memory %q: %w", taskID, manifestResource.Memory, err)
			}
			if quantity.Sign() <= 0 {
				return fmt.Errorf("task %q has invalid memory %q: must be positive", taskID, manifestResource.Memory)
			}
			if quantity.Cmp(maxTaskMemoryLimit) > 0 {
				return fmt.Errorf("task %q has invalid memory %q: exceeds maximum allowed %s", taskID, manifestResource.Memory, v1alpha1.MaxTaskMemoryLimit)
			}
		}
	}
	return nil
}

// TaskResourceLimits returns the effective CPU and memory limit strings and resource.Quantity values for a task.
// For sandbox tasks, if limits are specified in resources, those values are used; otherwise, the defaults (500m / 512Mi) are returned.
func TaskResourceLimits(task v1alpha1.ManifestTask) (string, string, resource.Quantity, resource.Quantity) {
	cpuLimit := ""
	memoryLimit := ""
	for _, manifestResource := range task.Resources {
		if manifestResource.Type == v1alpha1.ResourceTypeSandbox {
			cpuLimit = manifestResource.CPU
			memoryLimit = manifestResource.Memory
			break
		}
	}
	if cpuLimit == "" {
		cpuLimit = v1alpha1.DefaultTaskCPULimit
	}
	if memoryLimit == "" {
		memoryLimit = v1alpha1.DefaultTaskMemoryLimit
	}
	return cpuLimit, memoryLimit, resource.MustParse(cpuLimit), resource.MustParse(memoryLimit)
}
