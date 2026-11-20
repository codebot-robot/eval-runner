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

package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/pkg/agent"
)

func TestConversation_ToolCallAndFinalTurn(t *testing.T) {
	var step int
	var capturedPath, capturedAPIKey string
	var capturedToolsCount int
	var capturedThoughtSignature string
	var capturedFunctionResponseOutput string

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		step++
		capturedPath = req.URL.Path
		capturedAPIKey = req.Header.Get("x-goog-api-key")

		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		if tools, ok := body["tools"].([]any); ok {
			capturedToolsCount = len(tools)
		}

		if step == 1 {
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{
				"candidates": [{
					"content": {
						"parts": [{
							"functionCall": {
								"id": "call_1",
								"name": "bash",
								"args": {"command": "ls -la"}
							},
							"thoughtSignature": "test-thought-signature-opaque-token"
						}]
					}
				}],
				"usageMetadata": {"promptTokenCount": 15, "candidatesTokenCount": 5}
			}`))
			return
		}

		if contents, ok := body["contents"].([]any); ok && len(contents) >= 3 {
			if secondContent, ok := contents[1].(map[string]any); ok {
				if parts, ok := secondContent["parts"].([]any); ok && len(parts) > 0 {
					if partMap, ok := parts[0].(map[string]any); ok {
						capturedThoughtSignature, _ = partMap["thoughtSignature"].(string)
					}
				}
			}
			if thirdContent, ok := contents[2].(map[string]any); ok {
				if parts, ok := thirdContent["parts"].([]any); ok && len(parts) > 0 {
					if partMap, ok := parts[0].(map[string]any); ok {
						if functionResponse, ok := partMap["functionResponse"].(map[string]any); ok {
							if responseData, ok := functionResponse["response"].(map[string]any); ok {
								capturedFunctionResponseOutput, _ = responseData["output"].(string)
							}
						}
					}
				}
			}
		}

		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{
			"candidates": [{
				"content": {
					"parts": [{"text": "Found files: file1.txt file2.txt"}]
				}
			}],
			"usageMetadata": {"promptTokenCount": 35, "candidatesTokenCount": 10}
		}`))
	}))
	defer server.Close()

	client, err := New(Options{
		BaseURL:    server.URL,
		Model:      "google/gemini-2.5-flash",
		APIKey:     "secret-gemini-key",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Turn 1: user instruction prompts tool call with thought signature
	response1, err := client.Generate(t.Context(), agent.Request{
		SystemInstruction: "You are a bash assistant.",
		Messages: []agent.Message{
			{Role: agent.RoleUser, Text: "List the files."},
		},
	})
	if err != nil {
		t.Fatalf("turn 1 Generate() failed: %v", err)
	}
	if capturedPath != "/v1beta/models/gemini-2.5-flash:generateContent" {
		t.Errorf("turn 1 path mismatch: got %q", capturedPath)
	}
	if capturedAPIKey != "secret-gemini-key" {
		t.Errorf("turn 1 api key mismatch: got %q", capturedAPIKey)
	}
	if capturedToolsCount != 1 {
		t.Errorf("turn 1 tools count mismatch: got %d, want 1", capturedToolsCount)
	}
	if len(response1.ToolCalls) != 1 || response1.ToolCalls[0].ID != "call_1" || response1.ToolCalls[0].Command != "ls -la" {
		t.Fatalf("turn 1 tool call mismatch: %+v", response1.ToolCalls)
	}
	if response1.ToolCalls[0].Opaque != "test-thought-signature-opaque-token" {
		t.Errorf("turn 1 opaque signature mismatch: got %q", response1.ToolCalls[0].Opaque)
	}
	if response1.InputTokens != 15 || response1.OutputTokens != 5 {
		t.Errorf("turn 1 usage mismatch: inputTokens=%d, outputTokens=%d", response1.InputTokens, response1.OutputTokens)
	}

	// Turn 2: tool results fed back, model returns final text, thought signature round-tripped
	response2, err := client.Generate(t.Context(), agent.Request{
		SystemInstruction: "You are a bash assistant.",
		Messages: []agent.Message{
			{Role: agent.RoleUser, Text: "List the files."},
			{Role: agent.RoleAssistant, ToolCalls: response1.ToolCalls},
			{Role: agent.RoleTool, ToolResults: []agent.ToolResult{
				{ID: "call_1", Command: "ls -la", Output: "file1.txt\nfile2.txt\n", ExitCode: 0},
			}},
		},
	})
	if err != nil {
		t.Fatalf("turn 2 Generate() failed: %v", err)
	}
	if capturedThoughtSignature != "test-thought-signature-opaque-token" {
		t.Errorf("turn 2 thought signature mismatch: got %q", capturedThoughtSignature)
	}
	if capturedFunctionResponseOutput != "file1.txt\nfile2.txt\n" {
		t.Errorf("turn 2 tool result output mismatch: got %q", capturedFunctionResponseOutput)
	}
	if response2.Text != "Found files: file1.txt file2.txt" {
		t.Errorf("turn 2 text mismatch: got %q", response2.Text)
	}
	if response2.InputTokens != 35 || response2.OutputTokens != 10 {
		t.Errorf("turn 2 usage mismatch: inputTokens=%d, outputTokens=%d", response2.InputTokens, response2.OutputTokens)
	}
}

func TestGenerate_ErrorRedactsAPIKey(t *testing.T) {
	secretAPIKey := "super-secret-key-12345678"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{
			"error": {
				"message": "Invalid API key: super-secret-key-12345678 provided in request"
			}
		}`))
	}))
	defer server.Close()

	client, err := New(Options{
		BaseURL:    server.URL,
		Model:      "gemini-2.5-flash",
		APIKey:     secretAPIKey,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	_, err = client.Generate(t.Context(), agent.Request{
		Messages: []agent.Message{{Role: agent.RoleUser, Text: "hello"}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errorMessage := err.Error()
	if strings.Contains(errorMessage, secretAPIKey) {
		t.Fatalf("error contains raw secret key: %s", errorMessage)
	}
	if !strings.Contains(errorMessage, "[REDACTED]") {
		t.Fatalf("error does not contain [REDACTED]: %s", errorMessage)
	}
}

func TestGenerate_CandidateWithNoPartsReturnsFinishReasonError(t *testing.T) {
	testCases := []struct {
		name         string
		responseJSON string
		expectedText string
	}{
		{
			name:         "safety block without parts",
			responseJSON: `{"candidates": [{"finishReason": "SAFETY"}]}`,
			expectedText: "finishReason: SAFETY",
		},
		{
			name:         "max tokens with empty parts",
			responseJSON: `{"candidates": [{"finishReason": "MAX_TOKENS", "content": {"parts": []}}]}`,
			expectedText: "finishReason: MAX_TOKENS",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
				writer.WriteHeader(http.StatusOK)
				_, _ = writer.Write([]byte(testCase.responseJSON))
			}))
			defer server.Close()

			client, err := New(Options{
				BaseURL:    server.URL,
				Model:      "gemini-2.5-flash",
				APIKey:     "test-key",
				HTTPClient: server.Client(),
			})
			if err != nil {
				t.Fatalf("New() failed: %v", err)
			}

			_, err = client.Generate(t.Context(), agent.Request{
				Messages: []agent.Message{{Role: agent.RoleUser, Text: "run something"}},
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), testCase.expectedText) {
				t.Errorf("error %q does not contain expected %q", err.Error(), testCase.expectedText)
			}
		})
	}
}

func TestGenerate_ContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		time.Sleep(200 * time.Millisecond)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"candidates": [{"content": {"parts": [{"text": "slow"}]}}]}`))
	}))
	defer server.Close()

	client, err := New(Options{
		BaseURL:    server.URL,
		Model:      "gemini-2.5-flash",
		APIKey:     "some-key",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	cancelledContext, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = client.Generate(cancelledContext, agent.Request{
		Messages: []agent.Message{{Role: agent.RoleUser, Text: "hello"}},
	})
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("expected context canceled error, got: %v", err)
	}
}

func TestNew_ModelValidation(t *testing.T) {
	testCases := []struct {
		name          string
		modelName     string
		expectedModel string
		expectError   bool
	}{
		{name: "standard", modelName: "gemini-2.5-flash", expectedModel: "gemini-2.5-flash"},
		{name: "google prefix", modelName: "google/gemini-3.5-flash-lite", expectedModel: "gemini-3.5-flash-lite"},
		{name: "models prefix", modelName: "models/gemini-2.5-pro", expectedModel: "gemini-2.5-pro"},
		{name: "empty", modelName: "", expectError: true},
		{name: "unsupported", modelName: "mockllm/model", expectError: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			client, err := New(Options{
				Model:  testCase.modelName,
				APIKey: "test-key",
			})
			if testCase.expectError {
				if err == nil {
					t.Fatalf("expected error for model %q, got nil", testCase.modelName)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for model %q: %v", testCase.modelName, err)
				}
				if client.model != testCase.expectedModel {
					t.Errorf("model mismatch: got %q, want %q", client.model, testCase.expectedModel)
				}
			}
		})
	}
}

func TestIsGeminiModel(t *testing.T) {
	testCases := []struct {
		modelName string
		expected  bool
	}{
		{modelName: "gemini-2.5-flash", expected: true},
		{modelName: "google/gemini-2.5-flash", expected: true},
		{modelName: "models/gemini-2.5-flash", expected: true},
		{modelName: "google/gemini-3.5-flash-lite", expected: true},
		{modelName: "mockllm/model", expected: false},
		{modelName: "openai/gpt-4o", expected: false},
		{modelName: "", expected: false},
		{modelName: "   ", expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.modelName, func(t *testing.T) {
			actual := IsGeminiModel(testCase.modelName)
			if actual != testCase.expected {
				t.Errorf("IsGeminiModel(%q) = %v, want %v", testCase.modelName, actual, testCase.expected)
			}
		})
	}
}
