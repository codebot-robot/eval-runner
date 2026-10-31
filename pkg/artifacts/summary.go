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
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DefaultInspectLogFilename is the canonical filename for inspect evaluation logs in artifact storage.
const DefaultInspectLogFilename = "inspect.json"

// ArtifactMeta describes repository, commit, task ID, and artifact prefix metadata for locating artifacts.
type ArtifactMeta struct {
	Repo           string
	Commit         string
	TaskID         string
	ArtifactPrefix string
	Filename       string
}

// ExpectedLogPath derives the expected log storage location for a task deterministically
// from ArtifactMeta (repo, commit, task ID, and artifact prefix).
func ExpectedLogPath(meta ArtifactMeta) string {
	prefix := strings.TrimSpace(meta.ArtifactPrefix)
	taskID := strings.TrimSpace(meta.TaskID)
	if taskID == "" {
		return ""
	}
	taskID = strings.Trim(taskID, "/")
	filename := strings.TrimSpace(meta.Filename)
	if filename == "" {
		filename = DefaultInspectLogFilename
	}
	filename = strings.TrimPrefix(filename, "/")

	if prefix != "" {
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		return prefix + taskID + "/" + filename
	}
	if meta.Commit != "" {
		return fmt.Sprintf("runs/%s/%s/%s", meta.Commit, taskID, filename)
	}
	return ""
}

// TaskLogPath is an alias for ExpectedLogPath.
func TaskLogPath(meta ArtifactMeta) string {
	return ExpectedLogPath(meta)
}

// TaskSummaryURI resolves the summary.json URI or path for a given artifact or log URI.
// If uri is already a summary.json URI or path, it is returned trimmed.
func TaskSummaryURI(uri string) string {
	cleanURI := strings.TrimSpace(uri)
	if strings.HasSuffix(cleanURI, "/summary.json") || cleanURI == "summary.json" {
		return cleanURI
	}
	idx := strings.LastIndex(cleanURI, "/")
	if idx == -1 {
		return "summary.json"
	}
	return cleanURI[:idx+1] + "summary.json"
}

// ReadTaskSummary loads and parses summary.json for a task from the specified ArtifactSink.
// uri may be the direct summary.json URI or an Inspect log file URI.
func ReadTaskSummary(ctx context.Context, sink ArtifactSink, uri string) (*TaskSummary, error) {
	if sink == nil {
		return nil, fmt.Errorf("artifact sink cannot be nil")
	}
	cleanURI := strings.TrimSpace(uri)
	if cleanURI == "" {
		return nil, fmt.Errorf("summary uri cannot be empty")
	}

	summaryURI := TaskSummaryURI(cleanURI)
	data, err := sink.Load(ctx, summaryURI)
	if err != nil {
		return nil, err
	}

	var summary TaskSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return nil, fmt.Errorf("failed to parse summary.json from %s: %w", summaryURI, err)
	}

	if err := ValidateTaskSummary(summary); err != nil {
		return nil, fmt.Errorf("invalid summary.json at %s: %w", summaryURI, err)
	}

	return &summary, nil
}
