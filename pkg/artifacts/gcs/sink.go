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

package gcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
)

const (
	// DefaultMaxUploadBytes is the default cap on uploaded artifact size (10 MiB).
	DefaultMaxUploadBytes int64 = 10 * 1024 * 1024
)

// StorageClient defines the interface for interacting with GCS.
type StorageClient interface {
	WriteObject(ctx context.Context, bucket, object, contentType string, data []byte) error
	Close() error
}

type realStorageClient struct {
	client *storage.Client
}

func (c *realStorageClient) WriteObject(ctx context.Context, bucket, object, contentType string, data []byte) error {
	w := c.client.Bucket(bucket).Object(object).NewWriter(ctx)
	if contentType != "" {
		w.ContentType = contentType
	}
	if _, err := w.Write(data); err != nil {
		return errors.Join(err, w.Close())
	}
	return w.Close()
}

func (c *realStorageClient) Close() error {
	return c.client.Close()
}

// GCSSink implements artifacts.ArtifactSink to write Inspect logs and summary.json to Google Cloud Storage.
type GCSSink struct {
	bucket         string
	maxUploadBytes int64
	client         StorageClient
}

var _ artifacts.ArtifactSink = (*GCSSink)(nil)

// Option configures GCSSink.
type Option func(*GCSSink)

// WithStorageClient injects a custom StorageClient (e.g. for testing).
func WithStorageClient(c StorageClient) Option {
	return func(s *GCSSink) {
		s.client = c
	}
}

// WithMaxUploadBytes sets the maximum artifact upload size in bytes.
func WithMaxUploadBytes(bytes int64) Option {
	return func(s *GCSSink) {
		s.maxUploadBytes = bytes
	}
}

// New creates a new GCSSink targeting the specified bucket.
func New(ctx context.Context, bucket string, opts ...Option) (*GCSSink, error) {
	b, err := normalizeAndValidateBucket(bucket)
	if err != nil {
		return nil, err
	}

	sink := &GCSSink{
		bucket:         b,
		maxUploadBytes: DefaultMaxUploadBytes,
	}
	for _, opt := range opts {
		opt(sink)
	}

	if sink.client == nil {
		sc, err := storage.NewClient(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create GCS client: %w", err)
		}
		sink.client = &realStorageClient{client: sc}
	}

	return sink, nil
}

// Bucket returns the target bucket name.
func (s *GCSSink) Bucket() string {
	return s.bucket
}

// MaxUploadBytes returns the configured maximum upload byte size.
func (s *GCSSink) MaxUploadBytes() int64 {
	return s.maxUploadBytes
}

// Close closes the underlying storage client.
func (s *GCSSink) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// ArtifactPrefix returns the run-level storage prefix for a given commit.
func (s *GCSSink) ArtifactPrefix(commit string) string {
	if s.bucket == "" || strings.TrimSpace(commit) == "" {
		return ""
	}
	return fmt.Sprintf("gs://%s/runs/%s/", s.bucket, commit)
}

// StoreArtifact writes the inspect log and summary.json to GCS and returns the gs:// URI.
func (s *GCSSink) StoreArtifact(ctx context.Context, meta artifacts.RunMetadata, filename string, data []byte) (string, error) {
	if err := validateSafeComponent("commit", meta.Commit); err != nil {
		return "", err
	}
	if err := validateTaskID(meta.TaskID); err != nil {
		return "", err
	}
	if err := validateSafeComponent("filename", filename); err != nil {
		return "", err
	}
	if s.bucket == "" {
		return "", fmt.Errorf("bucket name is empty")
	}

	capLimit := s.maxUploadBytes
	if capLimit <= 0 {
		capLimit = DefaultMaxUploadBytes
	}

	capped := false
	uploadData := data
	if int64(len(data)) > capLimit {
		capped = true
		uploadData = data[:capLimit]
	}

	logObject := fmt.Sprintf("runs/%s/%s/%s", meta.Commit, meta.TaskID, filename)
	if err := s.client.WriteObject(ctx, s.bucket, logObject, "application/json", uploadData); err != nil {
		return "", fmt.Errorf("failed to upload inspect log to gs://%s/%s: %w", s.bucket, logObject, err)
	}

	summary := artifacts.TaskSummary{
		TaskID:     meta.TaskID,
		Model:      meta.Model,
		Commit:     meta.Commit,
		Samples:    meta.Samples,
		Successes:  meta.Successes,
		MeanScore:  meta.MeanScore,
		StartedAt:  meta.StartedAt,
		FinishedAt: meta.FinishedAt,
		Capped:     capped,
	}
	if capped {
		summary.CapBytes = capLimit
	}

	summaryBytes, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal summary.json: %w", err)
	}
	summaryBytes = append(summaryBytes, '\n')

	summaryObject := fmt.Sprintf("runs/%s/%s/summary.json", meta.Commit, meta.TaskID)
	if err := s.client.WriteObject(ctx, s.bucket, summaryObject, "application/json", summaryBytes); err != nil {
		return "", fmt.Errorf("failed to upload summary.json to gs://%s/%s: %w", s.bucket, summaryObject, err)
	}

	return fmt.Sprintf("gs://%s/%s", s.bucket, logObject), nil
}

func normalizeAndValidateBucket(raw string) (string, error) {
	b := strings.TrimSpace(raw)
	b = strings.TrimPrefix(b, "gs://")
	b = strings.TrimSuffix(b, "/")
	b = strings.TrimSpace(b)
	if b == "" {
		return "", fmt.Errorf("bucket name must not be empty")
	}
	if strings.Contains(b, "/") || strings.Contains(b, "\\") || strings.Contains(b, "..") || strings.ContainsRune(b, 0) {
		return "", fmt.Errorf("bucket name %q contains invalid characters", raw)
	}
	return b, nil
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

func validateTaskID(taskID string) error {
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("taskID must not be empty")
	}
	if strings.Contains(taskID, "\\") || strings.Contains(taskID, "..") || strings.ContainsRune(taskID, 0) {
		return fmt.Errorf("taskID %q contains invalid characters or traversal elements", taskID)
	}
	if strings.HasPrefix(taskID, "/") || strings.HasSuffix(taskID, "/") {
		return fmt.Errorf("taskID %q must not start or end with a slash", taskID)
	}
	parts := strings.Split(taskID, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return fmt.Errorf("taskID %q contains empty or invalid path segment %q", taskID, p)
		}
	}
	return nil
}
