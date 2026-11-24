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
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type fakeTransport func(*http.Request) (*http.Response, error)

func (transport fakeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func runRetries(ctx context.Context, initialBackoff time.Duration, respond func(attempt int) (int, string)) ([]time.Duration, error) {
	var attemptTimes []time.Time
	client := &http.Client{Transport: fakeTransport(func(*http.Request) (*http.Response, error) {
		attemptTimes = append(attemptTimes, time.Now())
		statusCode, retryAfter := respond(len(attemptTimes))
		header := http.Header{}
		if retryAfter != "" {
			header.Set("Retry-After", retryAfter)
		}
		return &http.Response{StatusCode: statusCode, Status: http.StatusText(statusCode), Header: header, Body: io.NopCloser(strings.NewReader("overloaded"))}, nil
	})}
	response, err := PostWithRetry(ctx, PostOptions{
		HTTPClient: client, EndpointURL: "http://example.invalid/generate", InitialBackoff: initialBackoff,
		FormatError: func(statusText string, body []byte) error { return fmt.Errorf("%s: %s", statusText, body) },
	})
	if response != nil {
		_ = response.Body.Close()
	}
	waits := make([]time.Duration, 0, max(len(attemptTimes)-1, 0))
	for i := 1; i < len(attemptTimes); i++ {
		waits = append(waits, attemptTimes[i].Sub(attemptTimes[i-1]))
	}
	return waits, err
}

func TestPostWithRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		always503 := func(int) (int, string) { return http.StatusServiceUnavailable, "" }
		waits, err := runRetries(t.Context(), 0, always503)
		if err == nil || !strings.HasPrefix(err.Error(), "after 7 attempts:") || len(waits) != maxRetries {
			t.Fatalf("got waits=%d err=%v, want %d waits after 7 attempts", len(waits), err, maxRetries)
		}
		expectedBase, totalWait := defaultInitialBackoff, time.Duration(0)
		for i, waitDuration := range waits {
			if minimumWait, maximumWait := expectedBase-expectedBase/4, min(expectedBase+expectedBase/4, maxRetryAfter); waitDuration < minimumWait || waitDuration > maximumWait {
				t.Errorf("retry %d wait %v outside [%v, %v]", i+1, waitDuration, minimumWait, maximumWait)
			}
			totalWait += waitDuration
			expectedBase = min(expectedBase*2, maxRetryAfter)
		}
		if totalWait < 47*time.Second || totalWait > 79*time.Second {
			t.Errorf("total retry window %v outside expected ~60s range", totalWait)
		}

		waits, err = runRetries(t.Context(), 45*time.Second, func(int) (int, string) { return http.StatusTooManyRequests, "" })
		if err == nil || len(waits) != maxRetries {
			t.Fatalf("got waits=%d err=%v, want %d waits and error", len(waits), err, maxRetries)
		}
		distinctCapped := make(map[time.Duration]struct{})
		for i, waitDuration := range waits {
			if waitDuration > maxRetryAfter || (i > 0 && waitDuration < maxRetryAfter-maxRetryAfter/4) {
				t.Errorf("retry %d wait %v out of capped bounds", i+1, waitDuration)
			}
			if i > 0 {
				distinctCapped[waitDuration] = struct{}{}
			}
		}
		if len(distinctCapped) < 2 {
			t.Errorf("expected jittered capped waits to vary, got %v", distinctCapped)
		}

		if waits, err := runRetries(t.Context(), 0, func(attempt int) (int, string) {
			if attempt == 1 {
				return http.StatusTooManyRequests, "5"
			}
			return http.StatusOK, ""
		}); err != nil || len(waits) != 1 || waits[0] != 5*time.Second {
			t.Errorf("got waits=%v err=%v, want single 5s Retry-After wait", waits, err)
		}

		deadlineContext, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()
		if waits, err := runRetries(deadlineContext, 0, always503); err == nil || !strings.Contains(err.Error(), "stopped because no time left to retry") || len(waits) != 0 {
			t.Errorf("got waits=%v err=%v, want immediate deadline guard error", waits, err)
		}
		cancelContext, stop := context.WithCancel(t.Context())
		if _, err := runRetries(cancelContext, 0, func(int) (int, string) {
			go func() { time.Sleep(100 * time.Millisecond); stop() }()
			return http.StatusServiceUnavailable, ""
		}); !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	})
}
