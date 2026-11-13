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

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultMaxSteps is the default maximum number of model turns in an agent run.
	DefaultMaxSteps = 50
	// DefaultMaxOutputBytes is the default byte budget for command output before truncation (16 KiB).
	DefaultMaxOutputBytes = 16 * 1024
	// DefaultCommandTimeout is the default deadline applied per command execution.
	DefaultCommandTimeout = 2 * time.Minute
)

// Role defines the role of a message sender in a conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// StopReason describes why the agent loop finished.
type StopReason string

const (
	StopReasonCompleted   StopReason = "Completed"
	StopReasonMaxSteps    StopReason = "MaxSteps"
	StopReasonContextDone StopReason = "ContextDone"
	StopReasonModelError  StopReason = "ModelError"
)

// ToolCall represents a bash command execution requested by the model.
type ToolCall struct {
	ID      string `json:"id,omitempty"`
	Command string `json:"command"`
}

// ToolResult represents the output, exit code, and duration resulting from a tool execution.
type ToolResult struct {
	ID                   string `json:"id,omitempty"`
	Command              string `json:"command,omitempty"`
	Output               string `json:"output"`
	ExitCode             int    `json:"exitCode"`
	DurationMilliseconds int64  `json:"durationMilliseconds,omitempty"`
}

// Message represents one turn in a conversation.
type Message struct {
	Role        Role         `json:"role"`
	Text        string       `json:"text,omitempty"`
	ToolCalls   []ToolCall   `json:"toolCalls,omitempty"`
	ToolResults []ToolResult `json:"toolResults,omitempty"`
}

// Request provides the instruction and conversation history for model generation.
type Request struct {
	SystemInstruction string    `json:"systemInstruction,omitempty"`
	Messages          []Message `json:"messages,omitempty"`
}

// Response contains the model's generated text, requested tool calls, and token usage.
type Response struct {
	Text         string     `json:"text,omitempty"`
	ToolCalls    []ToolCall `json:"toolCalls,omitempty"`
	InputTokens  int        `json:"inputTokens,omitempty"`
	OutputTokens int        `json:"outputTokens,omitempty"`
}

// ExecuteResult holds the outcome of running a shell command.
type ExecuteResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// Options configures the execution parameters for the agent loop.
type Options struct {
	Instruction       string        `json:"instruction,omitempty"`
	SystemInstruction string        `json:"systemInstruction,omitempty"`
	MaxSteps          int           `json:"maxSteps,omitempty"`
	MaxOutputBytes    int           `json:"maxOutputBytes,omitempty"`
	CommandTimeout    time.Duration `json:"commandTimeout,omitempty"`
}

// Step records the assistant text, tool calls, tool results, durations, and token usage for one turn.
type Step struct {
	Text                 string       `json:"text,omitempty"`
	ToolCalls            []ToolCall   `json:"toolCalls,omitempty"`
	ToolResults          []ToolResult `json:"toolResults,omitempty"`
	DurationMilliseconds int64        `json:"durationMilliseconds,omitempty"`
	InputTokens          int          `json:"inputTokens,omitempty"`
	OutputTokens         int          `json:"outputTokens,omitempty"`
}

// Result summarizes the complete agent loop execution.
type Result struct {
	Steps        []Step     `json:"steps"`
	StopReason   StopReason `json:"stopReason"`
	InputTokens  int        `json:"inputTokens"`
	OutputTokens int        `json:"outputTokens"`
}

// Model produces the next assistant turn for a conversation.
type Model interface {
	Generate(ctx context.Context, request Request) (*Response, error)
}

// Executor runs one shell command in the task environment.
type Executor interface {
	Execute(ctx context.Context, command string) (ExecuteResult, error)
}

// Run executes the agent loop until the model completes, MaxSteps is reached, or the context is done.
func Run(ctx context.Context, model Model, executor Executor, options Options) (*Result, error) {
	maxSteps, maxOutputBytes, commandTimeout := options.MaxSteps, options.MaxOutputBytes, options.CommandTimeout
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = DefaultMaxOutputBytes
	}
	if commandTimeout <= 0 {
		commandTimeout = DefaultCommandTimeout
	}

	result := &Result{Steps: []Step{}}
	messages := []Message{{Role: RoleUser, Text: options.Instruction}}

	for {
		if contextErr := ctx.Err(); contextErr != nil {
			result.StopReason = StopReasonContextDone
			return result, contextErr
		}

		modelStartTime := time.Now()
		response, generateErr := model.Generate(ctx, Request{
			SystemInstruction: options.SystemInstruction,
			Messages:          messages,
		})
		modelDuration := time.Since(modelStartTime)

		if contextErr := ctx.Err(); contextErr != nil {
			result.StopReason = StopReasonContextDone
			return result, contextErr
		}
		if generateErr != nil || response == nil {
			result.StopReason = StopReasonModelError
			if generateErr == nil {
				generateErr = errors.New("model returned nil response without error")
			}
			return result, generateErr
		}

		result.InputTokens += response.InputTokens
		result.OutputTokens += response.OutputTokens

		if len(response.ToolCalls) == 0 {
			result.Steps = append(result.Steps, Step{
				Text:                 response.Text,
				DurationMilliseconds: modelDuration.Milliseconds(),
				InputTokens:          response.InputTokens,
				OutputTokens:         response.OutputTokens,
			})
			result.StopReason = StopReasonCompleted
			return result, nil
		}

		var toolResults []ToolResult
		appendPartialStep := func() {
			result.Steps = append(result.Steps, Step{
				Text:                 response.Text,
				ToolCalls:            response.ToolCalls,
				ToolResults:          toolResults,
				DurationMilliseconds: modelDuration.Milliseconds(),
				InputTokens:          response.InputTokens,
				OutputTokens:         response.OutputTokens,
			})
		}

		for _, toolCall := range response.ToolCalls {
			if contextErr := ctx.Err(); contextErr != nil {
				appendPartialStep()
				result.StopReason = StopReasonContextDone
				return result, contextErr
			}

			commandStartTime := time.Now()
			commandContext, cancelCommand := context.WithTimeout(ctx, commandTimeout)
			executeResult, executeErr := executor.Execute(commandContext, toolCall.Command)
			cancelCommand()
			commandDuration := time.Since(commandStartTime)

			if contextErr := ctx.Err(); contextErr != nil {
				appendPartialStep()
				result.StopReason = StopReasonContextDone
				return result, contextErr
			}

			toolResults = append(toolResults, formatToolResult(toolCall, executeResult, executeErr, commandDuration, maxOutputBytes))
		}

		appendPartialStep()
		messages = append(messages,
			Message{Role: RoleAssistant, Text: response.Text, ToolCalls: response.ToolCalls},
			Message{Role: RoleTool, ToolResults: toolResults},
		)

		if len(result.Steps) >= maxSteps {
			result.StopReason = StopReasonMaxSteps
			return result, nil
		}
	}
}

func formatToolResult(toolCall ToolCall, executeResult ExecuteResult, executeErr error, duration time.Duration, maxOutputBytes int) ToolResult {
	output := executeResult.Stdout
	if executeResult.Stderr != "" {
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		output += executeResult.Stderr
	}

	exitCode := executeResult.ExitCode
	if executeErr != nil {
		if exitCode == 0 {
			exitCode = 1
		}
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		output += executeErr.Error()
	}

	return ToolResult{
		ID:                   toolCall.ID,
		Command:              toolCall.Command,
		Output:               truncateOutput(output, maxOutputBytes),
		ExitCode:             exitCode,
		DurationMilliseconds: duration.Milliseconds(),
	}
}

func truncateOutput(output string, maxOutputBytes int) string {
	if maxOutputBytes <= 0 || len(output) <= maxOutputBytes {
		return output
	}
	headBytes := maxOutputBytes / 2
	for headBytes > 0 && !utf8.RuneStart(output[headBytes]) {
		headBytes--
	}
	tailStart := len(output) - (maxOutputBytes - (maxOutputBytes / 2))
	for tailStart < len(output) && !utf8.RuneStart(output[tailStart]) {
		tailStart++
	}
	omittedBytes := tailStart - headBytes
	return fmt.Sprintf("%s[... %d bytes omitted ...]%s", output[:headBytes], omittedBytes, output[tailStart:])
}
