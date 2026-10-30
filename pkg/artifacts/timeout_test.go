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

package artifacts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/pkg/artifacts"
)

type blockingSink struct {
	artifacts.ArtifactSink
	blockStoreManifest   chan struct{}
	blockStoreArtifact   chan struct{}
	blockStoreComparison chan struct{}
	blockLoad            chan struct{}
}

func (b *blockingSink) StoreManifest(ctx context.Context, meta artifacts.RunMetadata, data []byte) (string, error) {
	if b.blockStoreManifest != nil {
		select {
		case <-b.blockStoreManifest:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "gs://bucket/manifest.json", nil
}

func (b *blockingSink) StoreArtifact(ctx context.Context, meta artifacts.RunMetadata, filename string, data []byte) (string, error) {
	if b.blockStoreArtifact != nil {
		select {
		case <-b.blockStoreArtifact:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "gs://bucket/artifact.json", nil
}

func (b *blockingSink) StoreComparison(ctx context.Context, comparisonName string, data []byte) (string, error) {
	if b.blockStoreComparison != nil {
		select {
		case <-b.blockStoreComparison:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "gs://bucket/comparison.json", nil
}

func (b *blockingSink) Load(ctx context.Context, path string) ([]byte, error) {
	if b.blockLoad != nil {
		select {
		case <-b.blockLoad:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []byte("data"), nil
}

func TestTimeoutWrappers_CancelPromptly(t *testing.T) {
	sink := &blockingSink{
		blockStoreManifest:   make(chan struct{}),
		blockStoreArtifact:   make(chan struct{}),
		blockStoreComparison: make(chan struct{}),
		blockLoad:            make(chan struct{}),
	}
	meta := artifacts.RunMetadata{
		RunName: "test-run",
		Commit:  "abcdef",
	}

	t.Run("StoreManifestWithTimeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := artifacts.StoreManifestWithTimeout(ctx, sink, meta, []byte("{}"))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 1*time.Second {
			t.Fatalf("StoreManifestWithTimeout took too long: %v", elapsed)
		}
	})

	t.Run("StoreArtifactWithTimeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := artifacts.StoreArtifactWithTimeout(ctx, sink, meta, "eval.json", []byte("{}"))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 1*time.Second {
			t.Fatalf("StoreArtifactWithTimeout took too long: %v", elapsed)
		}
	})

	t.Run("StoreComparisonWithTimeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := artifacts.StoreComparisonWithTimeout(ctx, sink, "comp", []byte("{}"))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 1*time.Second {
			t.Fatalf("StoreComparisonWithTimeout took too long: %v", elapsed)
		}
	})

	t.Run("LoadWithTimeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := artifacts.LoadWithTimeout(ctx, sink, "gs://bucket/runs/abcdef/manifest.json")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 1*time.Second {
			t.Fatalf("LoadWithTimeout took too long: %v", elapsed)
		}
	})

	t.Run("NilSink", func(t *testing.T) {
		ctx := t.Context()
		if _, err := artifacts.StoreManifestWithTimeout(ctx, nil, meta, []byte("{}")); !errors.Is(err, artifacts.ErrNoSinkConfigured) {
			t.Errorf("expected ErrNoSinkConfigured, got %v", err)
		}
		if _, err := artifacts.StoreArtifactWithTimeout(ctx, nil, meta, "f", []byte("{}")); !errors.Is(err, artifacts.ErrNoSinkConfigured) {
			t.Errorf("expected ErrNoSinkConfigured, got %v", err)
		}
		if _, err := artifacts.StoreComparisonWithTimeout(ctx, nil, "c", []byte("{}")); !errors.Is(err, artifacts.ErrNoSinkConfigured) {
			t.Errorf("expected ErrNoSinkConfigured, got %v", err)
		}
		if _, err := artifacts.LoadWithTimeout(ctx, nil, "p"); !errors.Is(err, artifacts.ErrNoSinkConfigured) {
			t.Errorf("expected ErrNoSinkConfigured, got %v", err)
		}
	})
}
