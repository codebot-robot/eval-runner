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

// Package modelclient provides shared HTTP execution, retry, and formatting logic for model providers.
package modelclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultHTTPTimeout is the default HTTP timeout for model requests.
	DefaultHTTPTimeout = 2 * time.Minute
	// DefaultInitialBackoff is the default initial retry backoff duration.
	DefaultInitialBackoff = 1 * time.Second
	// MaxRetryAfter is the maximum duration respected from a Retry-After header.
	MaxRetryAfter = 60 * time.Second
	// DefaultMaxRetries is the default maximum number of retries (up to 4 attempts total).
	DefaultMaxRetries = 3
	// MaxResponseBytes is the maximum response body size read from a model provider (16 MiB).
	MaxResponseBytes = 16 * 1024 * 1024
)

// ParseRetryAfter parses a Retry-After header as decimal seconds or an HTTP-date.
func ParseRetryAfter(header string) (time.Duration, bool) {
	if header == "" {
		return 0, false
	}
	if seconds, parseErr := strconv.ParseFloat(header, 64); parseErr == nil && seconds >= 0 {
		return min(time.Duration(seconds*float64(time.Second)), MaxRetryAfter), true
	}
	parsedTime, parseTimeErr := http.ParseTime(header)
	if parseTimeErr != nil {
		return 0, false
	}
	return min(max(time.Until(parsedTime), 0), MaxRetryAfter), true
}

// FormatAttempts formats the count of attempts as "1 attempt" or "N attempts".
func FormatAttempts(n int) string {
	if n == 1 {
		return "1 attempt"
	}
	return fmt.Sprintf("%d attempts", n)
}

// Redact returns a function that redacts credentials from error messages.
func Redact(secret string) func(string) string {
	return func(text string) string {
		if secret == "" {
			return text
		}
		text = strings.ReplaceAll(text, "Bearer "+secret, "Bearer [REDACTED]")
		if len(secret) >= 4 {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
		return text
	}
}

// FormatAPIError formats a provider's non-2xx response, redacting any configured secret.
func FormatAPIError(provider, statusText string, responseBytes []byte, redact func(string) string) error {
	errorMessage := statusText
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(responseBytes, &envelope); err == nil && envelope.Error.Message != "" {
		errorMessage = envelope.Error.Message
	} else if len(responseBytes) > 0 {
		errorMessage = strings.TrimSpace(string(responseBytes))
	}
	msg := fmt.Sprintf("%s API error (%s): %s", provider, statusText, errorMessage)
	if redact != nil {
		msg = redact(msg)
	}
	return errors.New(msg)
}

// RetryClient executes HTTP POST requests with exponential backoff on transient errors.
type RetryClient struct {
	HTTPClient     *http.Client
	InitialBackoff time.Duration
	MaxRetries     int
	Redact         func(string) string
	FormatError    func(statusText string, responseBytes []byte) error
}

// PostJSON sends an HTTP POST request with a JSON payload, retrying transient errors.
func (c *RetryClient) PostJSON(ctx context.Context, endpointURL string, header http.Header, body []byte) ([]byte, error) {
	initialBackoff, maxRetries := c.InitialBackoff, c.MaxRetries
	if initialBackoff <= 0 {
		initialBackoff = DefaultInitialBackoff
	}
	if maxRetries <= 0 {
		maxRetries = DefaultMaxRetries
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	redact := c.Redact
	if redact == nil {
		redact = func(s string) string { return s }
	}
	formatError := c.FormatError
	if formatError == nil {
		formatError = func(status string, b []byte) error {
			return errors.New(redact(fmt.Sprintf("API error (%s): %s", status, strings.TrimSpace(string(b)))))
		}
	}

	backoffDuration := initialBackoff
	var lastErr error
	var attempts int

	for attempt := 0; attempt <= maxRetries; attempt++ {
		attempts = attempt + 1
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		retry := func(retryAfterHeader string) (bool, error) {
			if attempt >= maxRetries {
				return false, nil
			}
			waitDuration := backoffDuration
			backoffDuration *= 2
			if retryAfter, ok := ParseRetryAfter(retryAfterHeader); ok {
				waitDuration = retryAfter
			}
			timer := time.NewTimer(waitDuration)
			select {
			case <-ctx.Done():
				timer.Stop()
				return false, ctx.Err()
			case <-timer.C:
				return true, nil
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		if header != nil {
			req.Header = header.Clone()
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = errors.New(redact(fmt.Sprintf("execute request: %v", err)))
			if retried, sleepErr := retry(""); sleepErr != nil {
				return nil, sleepErr
			} else if retried {
				continue
			}
			break
		}

		responseBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes))
		closeErr := resp.Body.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if readErr != nil {
			lastErr = errors.New(redact(fmt.Sprintf("read response: %v", readErr)))
			if retried, sleepErr := retry(""); sleepErr != nil {
				return nil, sleepErr
			} else if retried {
				continue
			}
			break
		}
		if closeErr != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil, fmt.Errorf("close response: %w", closeErr)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return responseBytes, nil
		}

		lastErr = formatError(resp.Status, responseBytes)
		if resp.StatusCode != http.StatusTooManyRequests && (resp.StatusCode < 500 || resp.StatusCode > 599) {
			return nil, lastErr
		}
		if retried, sleepErr := retry(resp.Header.Get("Retry-After")); sleepErr != nil {
			return nil, sleepErr
		} else if retried {
			continue
		}
	}

	return nil, fmt.Errorf("after %s: %w", FormatAttempts(attempts), lastErr)
}
