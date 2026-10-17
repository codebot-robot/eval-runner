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
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// DefaultMaxLoadBytes is the default cap on loaded artifact size (10 MiB).
	DefaultMaxLoadBytes int64 = 10 * 1024 * 1024
)

// NoopSink is an ArtifactSink that does not store artifacts and returns an empty log path.
type NoopSink struct{}

var _ ArtifactSink = (*NoopSink)(nil)

// StoreArtifact implements ArtifactSink for NoopSink.
func (s *NoopSink) StoreArtifact(ctx context.Context, meta RunMetadata, filename string, data []byte) (string, error) {
	return "", nil
}

// StoreManifest implements ArtifactSink for NoopSink.
func (s *NoopSink) StoreManifest(ctx context.Context, meta ManifestMetadata, data []byte) (string, error) {
	return "", nil
}

// StoreComparison implements ArtifactSink for NoopSink.
func (s *NoopSink) StoreComparison(ctx context.Context, comparisonName string, data []byte) (string, error) {
	return "", nil
}

// Load implements ArtifactSink for NoopSink.
func (s *NoopSink) Load(ctx context.Context, path string) ([]byte, error) {
	return nil, fmt.Errorf("noop sink cannot load artifacts")
}

// ArtifactPrefix implements ArtifactSink for NoopSink.
func (s *NoopSink) ArtifactPrefix(commit string) string {
	return ""
}

// InMemorySink is an in-memory ArtifactSink used for local development and fallback when no durable sink is configured.
// Mid-run controller restarts lose artifacts stored in-memory, requiring cheap re-discovery.
type InMemorySink struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

var _ ArtifactSink = (*InMemorySink)(nil)

// NewInMemorySink returns a new thread-safe InMemorySink.
func NewInMemorySink() *InMemorySink {
	return &InMemorySink{
		objects: make(map[string][]byte),
	}
}

// StoreArtifact implements ArtifactSink for InMemorySink.
func (s *InMemorySink) StoreArtifact(ctx context.Context, meta RunMetadata, filename string, data []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	escapedTaskID := url.QueryEscape(meta.TaskID)
	var relPath string
	if meta.Commit != "" {
		relPath = fmt.Sprintf("runs/%s/%s/%s", meta.Commit, escapedTaskID, filename)
	} else {
		relPath = fmt.Sprintf("%s/%s/%s/%s", meta.RunNamespace, meta.RunName, escapedTaskID, filename)
	}
	uri := "mem://" + relPath

	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)
	s.objects[uri] = dataCopy
	s.objects[relPath] = dataCopy
	return uri, nil
}

// StoreManifest implements ArtifactSink for InMemorySink.
func (s *InMemorySink) StoreManifest(ctx context.Context, meta ManifestMetadata, data []byte) (string, error) {
	if strings.TrimSpace(meta.Commit) == "" {
		return "", fmt.Errorf("commit must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	relPath := fmt.Sprintf("runs/%s/manifest.json", meta.Commit)
	uri := "mem://" + relPath

	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)
	s.objects[uri] = dataCopy
	s.objects[relPath] = dataCopy
	return uri, nil
}

// StoreComparison implements ArtifactSink for InMemorySink.
func (s *InMemorySink) StoreComparison(ctx context.Context, comparisonName string, data []byte) (string, error) {
	if strings.TrimSpace(comparisonName) == "" {
		return "", fmt.Errorf("comparisonName must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	relPath := fmt.Sprintf("comparisons/%s.json", comparisonName)
	uri := "mem://" + relPath

	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)
	s.objects[uri] = dataCopy
	s.objects[relPath] = dataCopy
	return uri, nil
}

// Load implements ArtifactSink for InMemorySink.
func (s *InMemorySink) Load(ctx context.Context, path string) ([]byte, error) {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return nil, fmt.Errorf("artifact path must not be empty")
	}

	trimmed := strings.TrimPrefix(cleanPath, "mem://")
	trimmed = strings.TrimPrefix(trimmed, "/")
	if !strings.HasPrefix(trimmed, "runs/") && !strings.HasPrefix(trimmed, "comparisons/") {
		return nil, fmt.Errorf("artifact path %q does not have required 'runs/' or 'comparisons/' prefix", path)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	data, ok := s.objects[cleanPath]
	if !ok {
		data, ok = s.objects[trimmed]
		if !ok {
			data, ok = s.objects["mem://"+trimmed]
		}
	}
	if !ok {
		return nil, fmt.Errorf("artifact %q not found in memory sink", path)
	}

	limit := DefaultMaxLoadBytes
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("artifact %q exceeds maximum load size of %d bytes", path, limit)
	}

	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)
	return dataCopy, nil
}

// ArtifactPrefix implements ArtifactSink for InMemorySink.
func (s *InMemorySink) ArtifactPrefix(commit string) string {
	if strings.TrimSpace(commit) == "" {
		return ""
	}
	return fmt.Sprintf("mem://runs/%s/", commit)
}

// LocalDirSink is an ArtifactSink that writes artifacts to a local directory hierarchy.
type LocalDirSink struct {
	BaseDir string
}

var _ ArtifactSink = (*LocalDirSink)(nil)

// StoreComparison implements ArtifactSink for LocalDirSink.
func (s *LocalDirSink) StoreComparison(ctx context.Context, comparisonName string, data []byte) (string, error) {
	if strings.TrimSpace(s.BaseDir) == "" {
		return "", fmt.Errorf("base directory must not be empty")
	}
	if err := validateSafeComponent("comparisonName", comparisonName); err != nil {
		return "", err
	}

	targetDir := filepath.Join(s.BaseDir, "comparisons")
	targetPath := filepath.Join(targetDir, comparisonName+".json")

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

// Load implements ArtifactSink for LocalDirSink.
func (s *LocalDirSink) Load(ctx context.Context, path string) ([]byte, error) {
	if strings.TrimSpace(s.BaseDir) == "" {
		return nil, fmt.Errorf("base directory must not be empty")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("artifact path must not be empty")
	}

	cleanBase := filepath.Clean(s.BaseDir)
	cleanPath := filepath.Clean(path)
	rel, err := filepath.Rel(cleanBase, cleanPath)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return nil, fmt.Errorf("artifact path %q escapes base directory %q", path, s.BaseDir)
	}
	if !strings.HasPrefix(rel, "runs"+string(filepath.Separator)) && rel != "runs" {
		return nil, fmt.Errorf("artifact path %q does not have required 'runs/' prefix", path)
	}

	f, err := os.Open(cleanPath)
	if err != nil {
		return nil, err
	}

	limit := DefaultMaxLoadBytes
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	closeErr := f.Close()
	if err != nil {
		return nil, errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("artifact %q exceeds maximum load size of %d bytes", path, limit)
	}

	return data, nil
}

// ArtifactPrefix implements ArtifactSink for LocalDirSink.
func (s *LocalDirSink) ArtifactPrefix(commit string) string {
	if s.BaseDir == "" || strings.TrimSpace(commit) == "" {
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

// StoreManifest implements ArtifactSink for LocalDirSink.
func (s *LocalDirSink) StoreManifest(ctx context.Context, meta ManifestMetadata, data []byte) (string, error) {
	if strings.TrimSpace(s.BaseDir) == "" {
		return "", fmt.Errorf("base directory must not be empty")
	}
	if err := validateSafeComponent("commit", meta.Commit); err != nil {
		return "", err
	}

	targetDir := filepath.Join(s.BaseDir, "runs", meta.Commit)
	targetPath := filepath.Join(targetDir, "manifest.json")

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
	var targetDir string
	if meta.Commit != "" {
		if err := validateSafeComponent("commit", meta.Commit); err != nil {
			return "", err
		}
		targetDir = filepath.Join(s.BaseDir, "runs", meta.Commit, escapedTaskID)
	} else {
		targetDir = filepath.Join(s.BaseDir, meta.RunNamespace, meta.RunName, escapedTaskID)
	}
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
