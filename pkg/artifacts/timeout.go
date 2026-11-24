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
	"strings"
	"time"

	"k8s.io/klog/v2"
)

const defaultGraceWindow = 250 * time.Millisecond

func manifestTarget(sink ArtifactSink, commit string) string {
	if sink != nil {
		if prefix := sink.ArtifactPrefix(commit); prefix != "" {
			return prefix + "manifest.json"
		}
	}
	return commit
}

// ArtifactTarget resolves the storage target or URI for a task artifact.
// If the sink implements TaskArtifactPather, it delegates to TaskArtifactPath.
// Otherwise it derives the path using the sink's ArtifactPrefix.
func ArtifactTarget(sink ArtifactSink, commit, taskID, filename string) string {
	if pather, ok := sink.(TaskArtifactPather); ok {
		if path := pather.TaskArtifactPath(commit, taskID, filename); path != "" {
			return path
		}
	}
	if sink != nil {
		if prefix := sink.ArtifactPrefix(commit); prefix != "" {
			return fmt.Sprintf("%s%s/%s", prefix, taskID, filename)
		}
	}
	if commit != "" && taskID != "" {
		return fmt.Sprintf("%s/%s/%s", commit, taskID, filename)
	}
	if taskID != "" {
		return fmt.Sprintf("%s/%s", taskID, filename)
	}
	if commit != "" {
		return fmt.Sprintf("%s/%s", commit, filename)
	}
	return filename
}

func comparisonTarget(sink ArtifactSink, compName string) string {
	if sink != nil {
		if prefix := sink.ArtifactPrefix("_"); prefix != "" && strings.HasSuffix(prefix, "runs/_/") {
			return strings.TrimSuffix(prefix, "runs/_/") + fmt.Sprintf("comparisons/%s.json", compName)
		}
	}
	return fmt.Sprintf("comparisons/%s.json", compName)
}

func storeWithTimeout[T any](ctx context.Context, op, target string, fn func() (T, error)) (T, error) {
	type result struct {
		val T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		val, err := fn()
		ch <- result{val: val, err: err}
	}()

	select {
	case r := <-ch:
		return r.val, r.err
	case <-ctx.Done():
		select {
		case r := <-ch:
			return r.val, r.err
		case <-time.After(defaultGraceWindow):
			var zero T
			return zero, fmt.Errorf("%s for %s: sink did not report completion within deadline: %w", op, target, ctx.Err())
		}
	}
}

// StoreManifestWithTimeout executes sink.StoreManifest concurrently, returning immediately if ctx expires.
func StoreManifestWithTimeout(ctx context.Context, sink ArtifactSink, meta RunMetadata, data []byte) (string, error) {
	if sink == nil {
		return "", ErrNoSinkConfigured
	}
	target := manifestTarget(sink, meta.Commit)
	return storeWithTimeout(ctx, "store manifest", target, func() (string, error) {
		return sink.StoreManifest(ctx, meta, data)
	})
}

// StoreArtifactWithTimeout executes sink.StoreArtifact concurrently, returning immediately if ctx expires.
func StoreArtifactWithTimeout(ctx context.Context, sink ArtifactSink, meta RunMetadata, filename string, data []byte) (StoredArtifact, error) {
	if sink == nil {
		return StoredArtifact{}, ErrNoSinkConfigured
	}
	target := ArtifactTarget(sink, meta.Commit, meta.TaskID, filename)
	return storeWithTimeout(ctx, "store artifact", target, func() (StoredArtifact, error) {
		return sink.StoreArtifact(ctx, meta, filename, data)
	})
}

// StoreTaskFileWithTimeout executes StoreTaskFile concurrently if supported by sink, returning immediately if ctx expires.
func StoreTaskFileWithTimeout(ctx context.Context, sink ArtifactSink, meta RunMetadata, filename string, data []byte) (string, error) {
	if sink == nil {
		return "", ErrNoSinkConfigured
	}
	storer, ok := sink.(TaskFileStorer)
	if !ok {
		return "", fmt.Errorf("artifact sink %T does not support storing task files", sink)
	}
	target := ArtifactTarget(sink, meta.Commit, meta.TaskID, filename)
	return storeWithTimeout(ctx, "store task file "+filename, target, func() (string, error) {
		return storer.StoreTaskFile(ctx, meta, filename, data)
	})
}

// StoreComparisonWithTimeout executes sink.StoreComparison concurrently, returning immediately if ctx expires.
func StoreComparisonWithTimeout(ctx context.Context, sink ArtifactSink, compName string, data []byte) (string, error) {
	if sink == nil {
		return "", ErrNoSinkConfigured
	}
	target := comparisonTarget(sink, compName)
	return storeWithTimeout(ctx, "store comparison", target, func() (string, error) {
		return sink.StoreComparison(ctx, compName, data)
	})
}

// LoadWithTimeout executes sink.Load concurrently, returning immediately if ctx expires.
func LoadWithTimeout(ctx context.Context, sink ArtifactSink, path string) ([]byte, error) {
	if sink == nil {
		return nil, ErrNoSinkConfigured
	}
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := sink.Load(ctx, path)
		ch <- result{data: data, err: err}
	}()

	select {
	case r := <-ch:
		return r.data, r.err
	case <-ctx.Done():
		select {
		case r := <-ch:
			return r.data, r.err
		case <-time.After(defaultGraceWindow):
			return nil, fmt.Errorf("load artifact from %s: sink did not report completion within deadline: %w", path, ctx.Err())
		}
	}
}

// OpenStreamWithTimeout executes sink.OpenStream concurrently, returning immediately if ctx expires.
// If the sink returns a stream after the deadline elapses, the stream is closed to prevent leaking handles.
func OpenStreamWithTimeout(ctx context.Context, sink ArtifactSink, path string, hash string) (io.ReadCloser, error) {
	if sink == nil {
		return nil, ErrNoSinkConfigured
	}
	type result struct {
		stream io.ReadCloser
		err    error
	}
	resultChannel := make(chan result, 1)
	timedOut := make(chan struct{})
	go func() {
		stream, err := sink.OpenStream(ctx, path, hash)
		select {
		case <-timedOut:
			if stream != nil {
				if closeErr := stream.Close(); closeErr != nil {
					klog.Warningf("failed to close late artifact stream from %s: %v", path, closeErr)
				}
			}
		case resultChannel <- result{stream: stream, err: err}:
		}
	}()

	select {
	case openResult := <-resultChannel:
		return openResult.stream, openResult.err
	case <-ctx.Done():
		select {
		case openResult := <-resultChannel:
			return openResult.stream, openResult.err
		case <-time.After(defaultGraceWindow):
			close(timedOut)
			deadlineErr := fmt.Errorf("open artifact stream from %s: sink did not report completion within deadline: %w", path, ctx.Err())
			select {
			case lateResult := <-resultChannel:
				if lateResult.stream != nil {
					if closeErr := lateResult.stream.Close(); closeErr != nil {
						return nil, errors.Join(deadlineErr, closeErr)
					}
				}
			default:
			}
			return nil, deadlineErr
		}
	}
}
