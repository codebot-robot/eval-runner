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

package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConversation(t *testing.T) {
	var step int
	var capturedAuth, capturedPath, capturedToolCallJSON, capturedToolResultContent string
	var capturedToolsCount, capturedMessagesCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		if step == 1 {
			w.Header().Set("Retry-After", "0.01")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit"}}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if step == 2 {
			capturedPath = r.URL.Path
			capturedAuth = r.Header.Get("Authorization")
			if tools, ok := body["tools"].([]any); ok {
				capturedToolsCount = len(tools)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"choices": [{"message": {"role": "assistant", "tool_calls": [{
					"id": "call_1", "type": "function",
					"function": {"name": "bash", "arguments": "{\"command\":\"ls -la\"}"},
					"extra_content": {"google": {"thought_signature": "sig123"}},
					"custom_field": 42
				}]}}],
				"usage": {"prompt_tokens": 15, "completion_tokens": 5}
			}`))
			return
		}
		if msgs, ok := body["messages"].([]any); ok {
			capturedMessagesCount = len(msgs)
			for _, m := range msgs {
				if mm, ok := m.(map[string]any); ok {
					if mm["role"] == "assistant" {
						if calls, ok := mm["tool_calls"].([]any); ok && len(calls) > 0 {
							raw, _ := json.Marshal(calls[0])
							capturedToolCallJSON = string(raw)
						}
					}
					if mm["role"] == "tool" {
						capturedToolResultContent, _ = mm["content"].(string)
					}
				}
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Done."}}],"usage":{"prompt_tokens":30,"completion_tokens":8}}`))
	}))
	defer server.Close()

	client, err := New(Options{BaseURL: server.URL, Model: "test-model", Token: "secret-token", HTTPClient: server.Client(), InitialBackoff: time.Millisecond})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Turn 1: retry on 429 then receive tool call with extension fields
	resp1, err := client.Generate(t.Context(), Request{
		SystemInstruction: "You are a bash agent.",
		Messages:          []Message{{Role: "user", Text: "run ls"}},
	})
	if err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}
	if capturedPath != "/chat/completions" || capturedAuth != "Bearer secret-token" || capturedToolsCount != 1 {
		t.Errorf("turn 1 request mismatch: path=%q auth=%q tools=%d", capturedPath, capturedAuth, capturedToolsCount)
	}
	if len(resp1.ToolCalls) != 1 || resp1.ToolCalls[0].ID != "call_1" || resp1.ToolCalls[0].Command != "ls -la" {
		t.Fatalf("turn 1 tool call mismatch: %+v", resp1.ToolCalls)
	}
	if resp1.InputTokens != 15 || resp1.OutputTokens != 5 {
		t.Errorf("turn 1 usage mismatch: %d, %d", resp1.InputTokens, resp1.OutputTokens)
	}

	// Turn 2: echo back tool call as {ID, Command} and tool result; assert echoed raw extension fields
	resp2, err := client.Generate(t.Context(), Request{
		SystemInstruction: "You are a bash agent.",
		Messages: []Message{
			{Role: "user", Text: "run ls"},
			{Role: "assistant", ToolCalls: resp1.ToolCalls},
			{Role: "tool", ToolResults: []ToolResult{{ID: "call_1", Output: "total 0\n", ExitCode: 0}}},
		},
	})
	if err != nil {
		t.Fatalf("turn 2 failed: %v", err)
	}
	if capturedMessagesCount != 4 {
		t.Errorf("expected 4 messages in turn 2, got %d", capturedMessagesCount)
	}
	if !strings.Contains(capturedToolCallJSON, "sig123") || !strings.Contains(capturedToolCallJSON, "custom_field") {
		t.Errorf("tool call extensions not round-tripped: %s", capturedToolCallJSON)
	}
	if !strings.Contains(capturedToolResultContent, "total 0") || !strings.Contains(capturedToolResultContent, `"exit_code":0`) {
		t.Errorf("tool result content mismatch: %s", capturedToolResultContent)
	}
	if resp2.Text != "Done." || resp2.InputTokens != 30 || resp2.OutputTokens != 8 {
		t.Errorf("turn 2 response mismatch: %+v", resp2)
	}
}

func TestErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad_request/chat/completions":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"bad request with secret-token-12345"}}`))
		case "/empty_choices/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[]}`))
		case "/tool_errors/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"other","arguments":"{}"}},
				{"id":"c2","type":"function","function":{"name":"bash","arguments":"bad-json"}},
				{"id":"c3","type":"function","function":{"name":"bash","arguments":"{}"}}
			]}}]}`))
		}
	}))
	defer server.Close()

	// non-2xx error redacts token
	client, _ := New(Options{BaseURL: server.URL + "/bad_request", Model: "m", Token: "secret-token-12345", HTTPClient: server.Client(), InitialBackoff: time.Millisecond})
	_, err := client.Generate(t.Context(), Request{Messages: []Message{{Role: "user", Text: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "[REDACTED]") || strings.Contains(err.Error(), "secret-token-12345") {
		t.Errorf("expected redacted error without token, got: %v", err)
	}

	// empty choices error
	client.baseURL = server.URL + "/empty_choices"
	if _, err := client.Generate(t.Context(), Request{Messages: []Message{{Role: "user", Text: "hi"}}}); err == nil {
		t.Error("expected error on empty choices, got nil")
	}

	// malformed tool calls return failing commands
	client.baseURL = server.URL + "/tool_errors"
	resp, err := client.Generate(t.Context(), Request{Messages: []Message{{Role: "user", Text: "hi"}}})
	if err != nil || len(resp.ToolCalls) != 3 {
		t.Fatalf("unexpected resp: %v, err: %v", resp, err)
	}
	for _, tc := range resp.ToolCalls {
		if !strings.HasPrefix(tc.Command, "echo 'invalid tool call:") || !strings.HasSuffix(tc.Command, "exit 2") {
			t.Errorf("expected failing command, got: %q", tc.Command)
		}
	}
}
