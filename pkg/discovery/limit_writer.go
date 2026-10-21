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
	"bytes"
	"errors"
	"fmt"
)

const (
	// MaxStdoutBytes is the maximum allowed stdout size from discovery execution (1 MiB).
	MaxStdoutBytes = 1 * 1024 * 1024

	// MaxStderrBytes is the maximum allowed stderr size from discovery execution (64 KiB).
	MaxStderrBytes = 64 * 1024

	// MaxTasks is the maximum number of tasks allowed in a discovered manifest.
	MaxTasks = 1000

	// MaxTaskPaths is the maximum number of path globs allowed per task in a discovered manifest.
	MaxTaskPaths = 100

	// MaxPathLength is the maximum character length for a single path glob pattern.
	MaxPathLength = 1024

	// MaxCohortLength is the maximum character length for a cohort identifier (64 chars).
	// Note: Kubernetes label values cap at 63 characters; cohorts are bounded to 64 chars per issue #293.
	MaxCohortLength = 64

	// MaxMessageBytes is the maximum byte length for Run status.message on failure.
	MaxMessageBytes = 4096

	// MaxStderrLines is the maximum number of stderr lines retained in status.message on failure.
	MaxStderrLines = 10

	// MaxStderrTailBytes is the maximum byte length of stderr tail retained in failure messages.
	MaxStderrTailBytes = 3072
)

var (
	// ErrLimitExceeded is returned when a stream write exceeds its size limit.
	ErrLimitExceeded = errors.New("limit exceeded")
)

// LimitError indicates that an exec stream exceeded its maximum allowed size.
type LimitError struct {
	Stream string
	Limit  int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("%s limit exceeded (%d bytes)", e.Stream, e.Limit)
}

func (e *LimitError) Unwrap() error {
	return ErrLimitExceeded
}

// LimitWriter wraps an underlying buffer and enforces a maximum byte limit.
// If a write would exceed the limit, it writes up to the remaining capacity,
// sets the limit exceeded flag, and returns a LimitError.
type LimitWriter struct {
	buf           bytes.Buffer
	limit         int64
	bytesWritten  int64
	limitExceeded bool
	name          string
}

// NewLimitWriter creates a new LimitWriter with the specified limit and stream name.
func NewLimitWriter(limit int64, name string) *LimitWriter {
	return &LimitWriter{
		limit: limit,
		name:  name,
	}
}

// Write writes p to the internal buffer, up to the configured limit.
// If p would exceed the limit, it writes the remaining allowed bytes,
// records that the limit was exceeded, and returns a LimitError.
func (w *LimitWriter) Write(p []byte) (int, error) {
	if w.limit <= 0 {
		return w.buf.Write(p)
	}

	remaining := w.limit - w.bytesWritten
	if remaining <= 0 {
		w.limitExceeded = true
		return 0, &LimitError{Stream: w.name, Limit: w.limit}
	}

	if int64(len(p)) > remaining {
		n, _ := w.buf.Write(p[:remaining])
		w.bytesWritten += int64(n)
		w.limitExceeded = true
		return n, &LimitError{Stream: w.name, Limit: w.limit}
	}

	n, err := w.buf.Write(p)
	w.bytesWritten += int64(n)
	return n, err
}

// LimitExceeded returns true if the writer attempted to exceed the configured limit.
func (w *LimitWriter) LimitExceeded() bool {
	return w.limitExceeded
}

// String returns the contents of the buffer as a string.
func (w *LimitWriter) String() string {
	return w.buf.String()
}
