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

package modelclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	if d, ok := ParseRetryAfter(""); ok || d != 0 {
		t.Errorf("expected false, 0 for empty, got %v, %v", ok, d)
	}
	if d, ok := ParseRetryAfter("invalid"); ok || d != 0 {
		t.Errorf("expected false, 0 for invalid, got %v, %v", ok, d)
	}
	if d, ok := ParseRetryAfter("5"); !ok || d != 5*time.Second {
		t.Errorf("expected 5s, got %v, %v", ok, d)
	}
	if d, ok := ParseRetryAfter("0.05"); !ok || d != 50*time.Millisecond {
		t.Errorf("expected 50ms, got %v, %v", ok, d)
	}
	if d, ok := ParseRetryAfter("120"); !ok || d != MaxRetryAfter {
		t.Errorf("expected %v, got %v, %v", MaxRetryAfter, ok, d)
	}
	future := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	if d, ok := ParseRetryAfter(future); !ok || d <= 0 || d > 11*time.Second {
		t.Errorf("expected ~10s for HTTP-date, got %v, %v", ok, d)
	}
}

func TestFormatAttempts(t *testing.T) {
	if got := FormatAttempts(1); got != "1 attempt" {
		t.Errorf("got %q, want %q", got, "1 attempt")
	}
	if got := FormatAttempts(4); got != "4 attempts" {
		t.Errorf("got %q, want %q", got, "4 attempts")
	}
}

func TestRedact(t *testing.T) {
	redact := Redact("secret-token-12345")
	if got := redact("Bearer secret-token-12345 / secret-token-12345"); got != "Bearer [REDACTED] / [REDACTED]" {
		t.Errorf("got %q", got)
	}
	if got := Redact("")("plain"); got != "plain" {
		t.Errorf("got %q", got)
	}
}

func TestRetryClient_PostJSON(t *testing.T) {
	t.Run("retry transient 503 and succeed", func(t *testing.T) {
		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if atomic.AddInt32(&calls, 1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"message":"high demand"}}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":"recovered"}`))
		}))
		defer server.Close()

		client := &RetryClient{HTTPClient: server.Client(), InitialBackoff: time.Millisecond}
		body, err := client.PostJSON(t.Context(), server.URL, nil, []byte("{}"))
		if err != nil || string(body) != `{"result":"recovered"}` || atomic.LoadInt32(&calls) != 2 {
			t.Fatalf("unexpected result: body=%s, calls=%d, err=%v", body, calls, err)
		}
	})

	t.Run("context cancelled terminates retry early", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		ctx, cancel := context.WithCancel(t.Context())
		client := &RetryClient{HTTPClient: server.Client(), InitialBackoff: 100 * time.Millisecond}
		go func() { time.Sleep(10 * time.Millisecond); cancel() }()
		if _, err := client.PostJSON(ctx, server.URL, nil, []byte("{}")); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	})
}
