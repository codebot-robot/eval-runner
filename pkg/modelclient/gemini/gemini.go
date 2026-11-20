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

// Package gemini implements a Gemini generate-content model client for the agent loop.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gke-labs/eval-runner/pkg/agent"
)

const (
	defaultBaseURL     = "https://generativelanguage.googleapis.com"
	defaultHTTPTimeout = 2 * time.Minute
	bashToolName       = "bash"
	apiKeyHeaderName   = "x-goog-api-key"
)

// Options configures a Client.
type Options struct {
	BaseURL    string
	Model      string
	APIKey     string
	HTTPClient *http.Client
}

// Client calls the Gemini generate-content endpoint.
type Client struct {
	baseURL    string
	model      string
	apiKey     string
	httpClient *http.Client
}

var _ agent.Model = (*Client)(nil)

type geminiRequest struct {
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
	Tools             []geminiTool    `json:"tools,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
}

type geminiFunctionCall struct {
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

type geminiFunctionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

var defaultTools = []geminiTool{
	{
		FunctionDeclarations: []geminiFunctionDeclaration{
			{
				Name:        bashToolName,
				Description: "Run a bash command in the sandbox",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"command": map[string]any{"type": "string", "description": "The command to run"}},
					"required":   []string{"command"},
				},
			},
		},
	},
}

func parseModelName(modelName string) (string, error) {
	trimmed := strings.TrimSpace(modelName)
	if trimmed == "" {
		return "", errors.New("model name is required")
	}
	normalized := strings.TrimPrefix(trimmed, "google/")
	normalized = strings.TrimPrefix(normalized, "models/")
	if !strings.HasPrefix(normalized, "gemini-") {
		return "", fmt.Errorf("unsupported model %q: only Gemini models are supported", modelName)
	}
	return normalized, nil
}

// IsGeminiModel reports whether modelName refers to a supported Gemini model.
func IsGeminiModel(modelName string) bool {
	_, err := parseModelName(modelName)
	return err == nil
}

// New creates a new Gemini model client.
func New(options Options) (*Client, error) {
	modelName, err := parseModelName(options.Model)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimRight(options.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Client{
		baseURL:    baseURL,
		model:      modelName,
		apiKey:     options.APIKey,
		httpClient: httpClient,
	}, nil
}

// Available reports whether the client has an API key configured.
func (client *Client) Available() bool {
	return client.apiKey != ""
}

// Generate sends the conversation history to Gemini and returns the model response.
func (client *Client) Generate(ctx context.Context, request agent.Request) (*agent.Response, error) {
	if !client.Available() {
		return nil, errors.New("model credentials unavailable: GEMINI_API_KEY is not configured")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	requestPayload := geminiRequest{
		Tools: defaultTools,
	}

	if request.SystemInstruction != "" {
		requestPayload.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: request.SystemInstruction}},
		}
	}

	for _, message := range request.Messages {
		switch message.Role {
		case agent.RoleUser:
			requestPayload.Contents = append(requestPayload.Contents, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: message.Text}},
			})
		case agent.RoleAssistant:
			var parts []geminiPart
			if message.Text != "" {
				parts = append(parts, geminiPart{Text: message.Text})
			}
			for _, toolCall := range message.ToolCalls {
				parts = append(parts, geminiPart{
					FunctionCall: &geminiFunctionCall{
						ID:        toolCall.ID,
						Name:      bashToolName,
						Arguments: map[string]any{"command": toolCall.Command},
					},
					ThoughtSignature: toolCall.Opaque,
				})
			}
			requestPayload.Contents = append(requestPayload.Contents, geminiContent{
				Role:  "model",
				Parts: parts,
			})
		case agent.RoleTool:
			var parts []geminiPart
			for _, toolResult := range message.ToolResults {
				parts = append(parts, geminiPart{
					FunctionResponse: &geminiFunctionResponse{
						Name: bashToolName,
						Response: map[string]any{
							"output":   toolResult.Output,
							"exitCode": toolResult.ExitCode,
						},
					},
				})
			}
			if len(message.ToolResults) == 0 && message.Text != "" {
				parts = append(parts, geminiPart{Text: message.Text})
			}
			requestPayload.Contents = append(requestPayload.Contents, geminiContent{
				Role:  "user",
				Parts: parts,
			})
		default:
			return nil, fmt.Errorf("unsupported message role: %q", message.Role)
		}
	}

	encodedBody, err := json.Marshal(requestPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	endpointURL := fmt.Sprintf("%s/v1beta/models/%s:generateContent", client.baseURL, client.model)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(encodedBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set(apiKeyHeaderName, client.apiKey)

	httpResponse, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("execute request: %w", err)
	}

	responseBytes, readErr := io.ReadAll(io.LimitReader(httpResponse.Body, 16*1024*1024))
	if closeErr := httpResponse.Body.Close(); closeErr != nil && readErr == nil {
		return nil, fmt.Errorf("close response: %w", closeErr)
	}
	if readErr != nil {
		return nil, fmt.Errorf("read response: %w", readErr)
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return nil, client.formatAPIError(httpResponse.Status, responseBytes)
	}

	return client.parseResponse(responseBytes)
}

func (client *Client) parseResponse(responseBytes []byte) (*agent.Response, error) {
	var apiResponse geminiResponse
	if err := json.Unmarshal(responseBytes, &apiResponse); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(apiResponse.Candidates) == 0 {
		return nil, errors.New("gemini API returned empty candidates")
	}

	candidate := apiResponse.Candidates[0]
	result := &agent.Response{}
	if apiResponse.UsageMetadata != nil {
		result.InputTokens = apiResponse.UsageMetadata.PromptTokenCount
		result.OutputTokens = apiResponse.UsageMetadata.CandidatesTokenCount
	}

	var textParts []string
	callIndex := 1
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			textParts = append(textParts, part.Text)
		}
		if part.FunctionCall != nil {
			callID := part.FunctionCall.ID
			if callID == "" {
				callID = fmt.Sprintf("call_%d", callIndex)
			}
			callIndex++

			var command string
			if part.FunctionCall.Name != bashToolName {
				command = fmt.Sprintf("echo 'invalid tool call: unknown function %q' >&2; exit 2", part.FunctionCall.Name)
			} else if commandValue, ok := part.FunctionCall.Arguments["command"].(string); ok {
				command = commandValue
			} else {
				command = "echo 'invalid tool call: missing command field' >&2; exit 2"
			}
			result.ToolCalls = append(result.ToolCalls, agent.ToolCall{
				ID:      callID,
				Command: command,
				Opaque:  part.ThoughtSignature,
			})
		}
	}
	result.Text = strings.Join(textParts, "\n")
	if len(result.ToolCalls) == 0 && result.Text == "" {
		finishReason := candidate.FinishReason
		if finishReason == "" {
			finishReason = "UNSPECIFIED"
		}
		return nil, fmt.Errorf("gemini candidate stopped with no content (finishReason: %s)", finishReason)
	}
	return result, nil
}

func (client *Client) formatAPIError(statusText string, responseBytes []byte) error {
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
	if client.apiKey != "" {
		errorMessage = strings.ReplaceAll(errorMessage, client.apiKey, "[REDACTED]")
	}
	return fmt.Errorf("gemini API error (%s): %s", statusText, errorMessage)
}
