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
	"errors"
	"testing"
)

func TestLimitWriterWithinLimit(t *testing.T) {
	lw := NewLimitWriter(100, "stdout")
	input := "hello world"

	n, err := lw.Write([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error writing within limit: %v", err)
	}
	if n != len(input) {
		t.Errorf("expected %d bytes written, got %d", len(input), n)
	}
	if lw.LimitExceeded() {
		t.Errorf("expected LimitExceeded to be false")
	}
	if lw.String() != input {
		t.Errorf("expected %q, got %q", input, lw.String())
	}
}

func TestLimitWriterExactLimit(t *testing.T) {
	lw := NewLimitWriter(5, "stderr")
	input := "12345"

	n, err := lw.Write([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error on exact limit write: %v", err)
	}
	if n != 5 {
		t.Errorf("expected 5 bytes written, got %d", n)
	}
	if lw.LimitExceeded() {
		t.Errorf("expected LimitExceeded to be false on exact limit")
	}
	if lw.String() != input {
		t.Errorf("expected %q, got %q", input, lw.String())
	}
}

func TestLimitWriterSingleWriteExceedsLimit(t *testing.T) {
	lw := NewLimitWriter(5, "stdout")
	input := "1234567890"

	n, err := lw.Write([]byte(input))
	if err == nil {
		t.Fatalf("expected error when write exceeds limit")
	}
	if !errors.Is(err, ErrLimitExceeded) {
		t.Errorf("expected ErrLimitExceeded, got: %v", err)
	}
	var limitErr *LimitError
	if !errors.As(err, &limitErr) {
		t.Errorf("expected LimitError, got: %v", err)
	} else {
		if limitErr.Stream != "stdout" || limitErr.Limit != 5 {
			t.Errorf("unexpected LimitError contents: %+v", limitErr)
		}
	}
	if n != 5 {
		t.Errorf("expected 5 bytes written, got %d", n)
	}
	if !lw.LimitExceeded() {
		t.Errorf("expected LimitExceeded to be true")
	}
	if lw.String() != "12345" {
		t.Errorf("expected buffer to hold '12345', got %q", lw.String())
	}

	// Subsequent write should return 0 and error immediately without allocating more
	n2, err2 := lw.Write([]byte("more"))
	if err2 == nil || n2 != 0 {
		t.Errorf("expected subsequent write to fail with 0 bytes, got %d, %v", n2, err2)
	}
	if lw.String() != "12345" {
		t.Errorf("expected buffer to remain capped at '12345', got %q", lw.String())
	}
}

func TestLimitWriterMultiWriteExceedsLimit(t *testing.T) {
	lw := NewLimitWriter(10, "stderr")

	// First write: 6 bytes (ok)
	n1, err1 := lw.Write([]byte("123456"))
	if err1 != nil || n1 != 6 {
		t.Fatalf("unexpected error on first write: n=%d, err=%v", n1, err1)
	}
	if lw.LimitExceeded() {
		t.Errorf("did not expect limit exceeded yet")
	}

	// Second write: 8 bytes (exceeds 10 limit by 4 bytes)
	n2, err2 := lw.Write([]byte("78901234"))
	if err2 == nil {
		t.Fatalf("expected error on exceeding write")
	}
	if !errors.Is(err2, ErrLimitExceeded) {
		t.Errorf("expected ErrLimitExceeded, got: %v", err2)
	}
	if n2 != 4 {
		t.Errorf("expected 4 bytes written up to limit, got %d", n2)
	}
	if !lw.LimitExceeded() {
		t.Errorf("expected LimitExceeded to be true")
	}
	if lw.String() != "1234567890" {
		t.Errorf("expected buffer '1234567890', got %q", lw.String())
	}
}
