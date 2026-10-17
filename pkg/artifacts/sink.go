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
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// NoopSink is an ArtifactSink that does not store artifacts and returns an empty log path.
type NoopSink struct{}

var _ ArtifactSink = (*NoopSink)(nil)

// StoreArtifact implements ArtifactSink for NoopSink.
func (s *NoopSink) StoreArtifact(ctx context.Context, meta RunMetadata, filename string, data []byte) (string, error) {
	return "", nil
}

// ArtifactPrefix implements ArtifactSink for NoopSink.
func (s *NoopSink) ArtifactPrefix(commit string) string {
	return ""
}

// LocalDirSink is an ArtifactSink that writes artifacts to a local directory hierarchy.
type LocalDirSink struct {
	BaseDir string
}

var _ ArtifactSink = (*LocalDirSink)(nil)

// ArtifactPrefix implements ArtifactSink for LocalDirSink.
func (s *LocalDirSink) ArtifactPrefix(commit string) string {
	if s.BaseDir == "" {
		return ""
	}
	return filepath.Join(s.BaseDir, "runs", commit) + "/"
}

func validateSafeComponent(name, val string) error {
	if strings.TrimSpace(val) == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if strings.Contains(val, "/") || strings.Contains(val, "\\") || strings.Contains(val, "..") || strings.ContainsRune(val, 0) {
		return fmt.Errorf("%s %q contains invalid characters or traversal elements", name, val)
	}
	return nil
}

// StoreArtifact implements ArtifactSink for LocalDirSink.
func (s *LocalDirSink) StoreArtifact(ctx context.Context, meta RunMetadata, filename string, data []byte) (string, error) {
	if strings.TrimSpace(s.BaseDir) == "" {
		return "", fmt.Errorf("base directory must not be empty")
	}

	if err := validateSafeComponent("runNamespace", meta.RunNamespace); err != nil {
		return "", err
	}
	if err := validateSafeComponent("runName", meta.RunName); err != nil {
		return "", err
	}
	if strings.TrimSpace(meta.TaskID) == "" {
		return "", fmt.Errorf("taskID must not be empty")
	}
	if strings.Contains(meta.TaskID, "..") || strings.ContainsRune(meta.TaskID, 0) {
		return "", fmt.Errorf("taskID %q contains invalid traversal elements", meta.TaskID)
	}
	if err := validateSafeComponent("filename", filename); err != nil {
		return "", err
	}

	escapedTaskID := url.QueryEscape(meta.TaskID)
	targetDir := filepath.Join(s.BaseDir, meta.RunNamespace, meta.RunName, escapedTaskID)
	targetPath := filepath.Join(targetDir, filename)

	cleanBase := filepath.Clean(s.BaseDir)
	cleanTarget := filepath.Clean(targetPath)
	rel, err := filepath.Rel(cleanBase, cleanTarget)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return "", fmt.Errorf("artifact path %q escapes base directory %q", targetPath, s.BaseDir)
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return "", err
	}

	if err := os.WriteFile(targetPath, data, 0644); err != nil {
		return "", err
	}

	return targetPath, nil
}
