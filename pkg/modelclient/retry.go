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
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultInitialBackoff is the default initial delay before retrying a failed request.
	DefaultInitialBackoff = 1 * time.Second
	maxRetryAfter         = 60 * time.Second
	maxRetries            = 3
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

// PostWithRetry executes an HTTP POST request with exponential backoff and retry for rate-limit and server errors.
func PostWithRetry(ctx context.Context, options PostOptions) (*http.Response, error) {
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	endpointURL, backoffDuration := options.EndpointURL, options.InitialBackoff
	if backoffDuration <= 0 {
		backoffDuration = DefaultInitialBackoff
	}

	for attempt := 0; ; attempt++ {
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(options.Body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		if options.Header != nil {
			httpRequest.Header = options.Header.Clone()
		}

		httpResponse, err := httpClient.Do(httpRequest)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if options.Redact != nil {
				return nil, errors.New(options.Redact(fmt.Sprintf("execute request: %v", err)))
			}
			return nil, fmt.Errorf("execute request: %w", err)
		}

		if httpResponse.StatusCode >= 200 && httpResponse.StatusCode < 300 {
			return httpResponse, nil
		}

		responseBytes, readErr := io.ReadAll(io.LimitReader(httpResponse.Body, maxErrorResponseBytes))
		if closeErr := httpResponse.Body.Close(); closeErr != nil && readErr == nil {
			return nil, fmt.Errorf("close response: %w", closeErr)
		}
		if readErr != nil {
			return nil, fmt.Errorf("read response: %w", readErr)
		}

		if (httpResponse.StatusCode == http.StatusTooManyRequests || (httpResponse.StatusCode >= 500 && httpResponse.StatusCode <= 599)) && attempt < maxRetries {
			waitDuration := backoffDuration
			backoffDuration *= 2
			if retryAfter, ok := parseRetryAfter(httpResponse.Header.Get("Retry-After")); ok {
				waitDuration = retryAfter
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(waitDuration):
			}
			continue
		}
		if options.FormatError != nil {
			return nil, options.FormatError(httpResponse.Status, responseBytes)
		}
		return nil, fmt.Errorf("API error (%s): %s", httpResponse.Status, strings.TrimSpace(string(responseBytes)))
	}
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
