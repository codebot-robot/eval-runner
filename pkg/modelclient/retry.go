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

// Package modelclient provides shared helpers for LLM model clients.
package modelclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"
)

const (
	defaultInitialBackoff = 1 * time.Second
	maxRetryAfter         = 60 * time.Second
	maxRetries            = 15
	maxErrorResponseBytes = 64 * 1024
)

// PostOptions configures an HTTP POST request executed with backoff and retry.
type PostOptions struct {
	HTTPClient     *http.Client
	EndpointURL    string
	Header         http.Header
	Body           []byte
	InitialBackoff time.Duration
	FormatError    func(statusText string, responseBody []byte) error
	Redact         func(string) string
}

// PostWithRetry executes an HTTP POST request with capped, jittered exponential backoff and retry for rate-limit and server errors.
func PostWithRetry(ctx context.Context, options PostOptions) (*http.Response, error) {
	backoffDuration := options.InitialBackoff
	if backoffDuration <= 0 {
		backoffDuration = defaultInitialBackoff
	}
	backoffDuration = min(backoffDuration, maxRetryAfter)

	for attempt := 0; ; attempt++ {
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, options.EndpointURL, bytes.NewReader(options.Body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		if options.Header != nil {
			httpRequest.Header = options.Header.Clone()
		}

		var lastError error
		var retryAfterDuration time.Duration
		hasRetryAfter := false

		httpResponse, err := options.HTTPClient.Do(httpRequest)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastError = fmt.Errorf("execute request: %w", err)
			if options.Redact != nil {
				lastError = errors.New(options.Redact(lastError.Error()))
			}
			if !isRetryableNetworkError(err) {
				return nil, lastError
			}
		} else if httpResponse.StatusCode >= 200 && httpResponse.StatusCode < 300 {
			return httpResponse, nil
		} else {
			responseBytes, readErr := io.ReadAll(io.LimitReader(httpResponse.Body, maxErrorResponseBytes))
			if closeErr := httpResponse.Body.Close(); closeErr != nil && readErr == nil {
				return nil, fmt.Errorf("close response: %w", closeErr)
			}
			if readErr != nil {
				return nil, fmt.Errorf("read response: %w", readErr)
			}
			lastError = options.FormatError(httpResponse.Status, responseBytes)
			if httpResponse.StatusCode != http.StatusTooManyRequests && (httpResponse.StatusCode < 500 || httpResponse.StatusCode > 599) {
				return nil, lastError
			}
			retryAfterDuration, hasRetryAfter = parseRetryAfter(httpResponse.Header.Get("Retry-After"))
		}

		if attempt >= maxRetries {
			return nil, fmt.Errorf("after %s: %w", formatAttempts(attempt+1), lastError)
		}
		waitDuration := retryAfterDuration
		if !hasRetryAfter {
			waitDuration = jitterBackoff(backoffDuration)
		}
		if deadline, hasDeadline := ctx.Deadline(); hasDeadline && time.Until(deadline) < waitDuration {
			return nil, fmt.Errorf("after %s (stopped because no time left to retry): %w", formatAttempts(attempt+1), lastError)
		}
		backoffDuration = min(backoffDuration*2, maxRetryAfter)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(waitDuration):
		}
	}
}

func jitterBackoff(baseDuration time.Duration) time.Duration {
	capped := min(baseDuration, maxRetryAfter)
	if capped <= 0 {
		return 0
	}
	quarter := capped / 4
	if quarter == 0 {
		return capped
	}
	minimumWait := capped - quarter
	maximumWait := min(capped+quarter, maxRetryAfter)
	return minimumWait + time.Duration(rand.Int64N(int64(maximumWait-minimumWait)+1))
}

func isRetryableNetworkError(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var netError net.Error
	return errors.As(err, &netError) && netError.Timeout()
}

func formatAttempts(attempts int) string {
	if attempts == 1 {
		return "1 attempt"
	}
	return fmt.Sprintf("%d attempts", attempts)
}

func parseRetryAfter(header string) (time.Duration, bool) {
	if header == "" {
		return 0, false
	}
	if seconds, parseErr := strconv.ParseFloat(header, 64); parseErr == nil && seconds >= 0 {
		return min(time.Duration(seconds*float64(time.Second)), maxRetryAfter), true
	}
	parsedTime, parseTimeErr := http.ParseTime(header)
	if parseTimeErr != nil {
		return 0, false
	}
	return min(max(time.Until(parsedTime), 0), maxRetryAfter), true
}
