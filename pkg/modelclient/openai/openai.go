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

// Package openai implements an OpenAI-compatible chat-completions client with a single bash tool.
package openai

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
	"sync"
	"time"
)

const (
	defaultHTTPTimeout    = 2 * time.Minute
	defaultInitialBackoff = 1 * time.Second
	maxRetryAfter         = 60 * time.Second
	maxRetries            = 3
	bashToolName          = "bash"
)

var bashTool = []map[string]any{{"type": "function", "function": map[string]any{
	"name": bashToolName, "description": "Run a bash command in the sandbox",
	"parameters": map[string]any{
		"type": "object", "required": []string{"command"},
		"properties": map[string]any{"command": map[string]any{"type": "string", "description": "The command to run"}},
	},
}}}

// Options configures a Client.
type Options struct {
	BaseURL, Model, Token string
	HTTPClient            *http.Client
	InitialBackoff        time.Duration
}

// Client calls an OpenAI-compatible chat-completions endpoint. Use one Client per conversation.
type Client struct {
	baseURL, model, token string
	httpClient            *http.Client
	initialBackoff        time.Duration
	mutex                 sync.Mutex
	rawToolCalls          map[string]json.RawMessage
}

// Request contains prompt instructions and history for generation.
type Request struct {
	SystemInstruction string
	Messages          []Message
}

// Message is a single turn in the conversation.
type Message struct {
	Role, Text  string
	ToolCalls   []ToolCall
	ToolResults []ToolResult
}

// ToolCall represents a single tool invocation emitted by the model.
type ToolCall struct{ ID, Command string }

// ToolResult represents the output and exit code of a completed tool execution.
type ToolResult struct {
	ID, Output string
	ExitCode   int
}

// Response contains the model output text, emitted tool calls, and token usage counts.
type Response struct {
	Text                      string
	ToolCalls                 []ToolCall
	InputTokens, OutputTokens int
}

type openAIRequest struct {
	Model    string           `json:"model"`
	Messages []openAIMessage  `json:"messages"`
	Tools    []map[string]any `json:"tools,omitempty"`
}

type openAIMessage struct {
	Role       string            `json:"role"`
	Content    *string           `json:"content,omitempty"`
	ToolCalls  []json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content   *string           `json:"content"`
			ToolCalls []json.RawMessage `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct{ Message string } `json:"error"`
}

// New creates a new OpenAI-compatible client.
func New(options Options) (*Client, error) {
	if options.BaseURL == "" {
		return nil, errors.New("base URL is required")
	}
	if options.Model == "" {
		return nil, errors.New("model is required")
	}
	httpClient, initialBackoff := options.HTTPClient, options.InitialBackoff
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if initialBackoff <= 0 {
		initialBackoff = defaultInitialBackoff
	}
	return &Client{
		baseURL: strings.TrimRight(options.BaseURL, "/"), model: options.Model, token: options.Token,
		httpClient: httpClient, initialBackoff: initialBackoff, rawToolCalls: make(map[string]json.RawMessage),
	}, nil
}

func (client *Client) synthesizeToolCall(toolCall ToolCall) (json.RawMessage, error) {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if raw, ok := client.rawToolCalls[toolCall.ID]; ok {
		return raw, nil
	}
	argumentsBytes, err := json.Marshal(map[string]string{"command": toolCall.Command})
	if err != nil {
		return nil, fmt.Errorf("marshal tool command: %w", err)
	}
	return json.Marshal(map[string]any{
		"id": toolCall.ID, "type": "function",
		"function": map[string]any{"name": bashToolName, "arguments": string(argumentsBytes)},
	})
}

// Generate sends the chat history to the model and returns the response.
func (client *Client) Generate(ctx context.Context, request Request) (*Response, error) {
	var messages []openAIMessage
	if request.SystemInstruction != "" {
		messages = append(messages, openAIMessage{Role: "system", Content: &request.SystemInstruction})
	}
	for _, message := range request.Messages {
		switch message.Role {
		case "user":
			messages = append(messages, openAIMessage{Role: "user", Content: &message.Text})
		case "assistant":
			var rawCalls []json.RawMessage
			for _, toolCall := range message.ToolCalls {
				raw, synthesizeErr := client.synthesizeToolCall(toolCall)
				if synthesizeErr != nil {
					return nil, synthesizeErr
				}
				rawCalls = append(rawCalls, raw)
			}
			var contentPtr *string
			if message.Text != "" {
				contentPtr = &message.Text
			}
			messages = append(messages, openAIMessage{Role: "assistant", Content: contentPtr, ToolCalls: rawCalls})
		case "tool":
			for _, toolResult := range message.ToolResults {
				resultBytes, marshalErr := json.Marshal(map[string]any{"output": toolResult.Output, "exit_code": toolResult.ExitCode})
				if marshalErr != nil {
					return nil, fmt.Errorf("marshal tool result: %w", marshalErr)
				}
				content := string(resultBytes)
				messages = append(messages, openAIMessage{Role: "tool", ToolCallID: toolResult.ID, Content: &content})
			}
			if len(message.ToolResults) == 0 && message.Text != "" {
				messages = append(messages, openAIMessage{Role: "tool", Content: &message.Text})
			}
		default:
			return nil, fmt.Errorf("unsupported message role: %q", message.Role)
		}
	}

	encodedBody, marshalErr := json.Marshal(openAIRequest{Model: client.model, Messages: messages, Tools: bashTool})
	if marshalErr != nil {
		return nil, fmt.Errorf("marshal request: %w", marshalErr)
	}
	endpointURL, backoffDuration := client.baseURL+"/chat/completions", client.initialBackoff

	for attempt := 0; ; attempt++ {
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(encodedBody))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		httpRequest.Header.Set("Content-Type", "application/json")
		if client.token != "" {
			httpRequest.Header.Set("Authorization", "Bearer "+client.token)
		}

		httpResponse, err := client.httpClient.Do(httpRequest)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New(client.redactToken(fmt.Sprintf("execute request: %v", err)))
		}

		if httpResponse.StatusCode >= 200 && httpResponse.StatusCode < 300 {
			response, parseErr := client.parseResponse(io.LimitReader(httpResponse.Body, 16*1024*1024))
			if closeErr := httpResponse.Body.Close(); closeErr != nil && parseErr == nil {
				return nil, fmt.Errorf("close response: %w", closeErr)
			}
			return response, parseErr
		}

		responseBytes, readErr := io.ReadAll(io.LimitReader(httpResponse.Body, 64*1024))
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
		return nil, client.formatAPIError(httpResponse.Status, responseBytes)
	}
}

func (client *Client) parseResponse(bodyReader io.Reader) (*Response, error) {
	var apiResponse openAIResponse
	if err := json.NewDecoder(bodyReader).Decode(&apiResponse); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if apiResponse.Error != nil && apiResponse.Error.Message != "" {
		return nil, fmt.Errorf("openai API error: %s", client.redactToken(apiResponse.Error.Message))
	}
	if len(apiResponse.Choices) == 0 {
		return nil, errors.New("openai API returned empty choices")
	}
	result := &Response{}
	if apiResponse.Usage != nil {
		result.InputTokens, result.OutputTokens = apiResponse.Usage.PromptTokens, apiResponse.Usage.CompletionTokens
	}
	if choice := apiResponse.Choices[0].Message; choice.Content != nil {
		result.Text = *choice.Content
	}
	client.mutex.Lock()
	defer client.mutex.Unlock()
	for _, rawCall := range apiResponse.Choices[0].Message.ToolCalls {
		var parsedCall struct {
			ID       string `json:"id"`
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		}
		if unmarshalErr := json.Unmarshal(rawCall, &parsedCall); unmarshalErr != nil {
			continue
		}
		callID := parsedCall.ID
		if callID == "" || client.rawToolCalls[callID] != nil {
			callID = fmt.Sprintf("call_%d", len(client.rawToolCalls)+1)
		}
		var rawMap map[string]any
		if unmarshalErr := json.Unmarshal(rawCall, &rawMap); unmarshalErr != nil {
			continue
		}
		rawMap["id"] = callID
		updated, marshalErr := json.Marshal(rawMap)
		if marshalErr != nil {
			return nil, fmt.Errorf("marshal updated raw tool call: %w", marshalErr)
		}
		rawCall = updated
		client.rawToolCalls[callID] = rawCall
		result.ToolCalls = append(result.ToolCalls, ToolCall{ID: callID, Command: extractCommand(parsedCall.Function.Name, parsedCall.Function.Arguments)})
	}
	return result, nil
}

func extractCommand(name string, argumentsRaw json.RawMessage) string {
	if name != bashToolName {
		return fmt.Sprintf("echo 'invalid tool call: unknown function %q' >&2; exit 2", name)
	}
	argumentsBytes := bytes.TrimSpace(argumentsRaw)
	if len(argumentsBytes) > 0 && argumentsBytes[0] == '"' {
		var rawString string
		if unmarshalErr := json.Unmarshal(argumentsBytes, &rawString); unmarshalErr != nil {
			return "echo 'invalid tool call: malformed JSON arguments' >&2; exit 2"
		}
		argumentsBytes = []byte(rawString)
	}
	var payload struct{ Command *string }
	if unmarshalErr := json.Unmarshal(argumentsBytes, &payload); unmarshalErr != nil {
		return "echo 'invalid tool call: malformed JSON arguments' >&2; exit 2"
	}
	if payload.Command == nil {
		return "echo 'invalid tool call: missing command field' >&2; exit 2"
	}
	return *payload.Command
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

func (client *Client) formatAPIError(statusText string, responseBytes []byte) error {
	errorMessage := statusText
	var envelope struct{ Error struct{ Message string } }
	if parseErr := json.Unmarshal(responseBytes, &envelope); parseErr != nil {
		if len(responseBytes) > 0 {
			errorMessage = strings.TrimSpace(string(responseBytes))
		}
	} else if envelope.Error.Message != "" {
		errorMessage = envelope.Error.Message
	}
	return errors.New(client.redactToken(fmt.Sprintf("openai API error (%s): %s", statusText, errorMessage)))
}

func (client *Client) redactToken(text string) string {
	if client.token != "" {
		text = strings.ReplaceAll(text, "Bearer "+client.token, "Bearer [REDACTED]")
		if len(client.token) >= 8 {
			text = strings.ReplaceAll(text, client.token, "[REDACTED]")
		}
	}
	return text
}
