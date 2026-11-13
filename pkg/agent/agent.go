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
	DefaultMaxSteps       = 50
	DefaultMaxOutputBytes = 16 * 1024
	DefaultCommandTimeout = 2 * time.Minute

	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"

	StopReasonCompleted   StopReason = "Completed"
	StopReasonMaxSteps    StopReason = "MaxSteps"
	StopReasonContextDone StopReason = "ContextDone"
	StopReasonModelError  StopReason = "ModelError"
)

type (
	Role       string
	StopReason string

	ToolCall struct {
		ID      string `json:"id,omitempty"`
		Command string `json:"command"`
	}

	ToolResult struct {
		ID                   string `json:"id,omitempty"`
		Command              string `json:"command,omitempty"`
		Output               string `json:"output"`
		ExitCode             int    `json:"exitCode"`
		DurationMilliseconds int64  `json:"durationMilliseconds"`
	}

	Message struct {
		Role        Role         `json:"role"`
		Text        string       `json:"text,omitempty"`
		ToolCalls   []ToolCall   `json:"toolCalls,omitempty"`
		ToolResults []ToolResult `json:"toolResults,omitempty"`
	}

	Request struct {
		SystemInstruction string    `json:"systemInstruction,omitempty"`
		Messages          []Message `json:"messages,omitempty"`
	}

	Response struct {
		Text         string     `json:"text,omitempty"`
		ToolCalls    []ToolCall `json:"toolCalls,omitempty"`
		InputTokens  int        `json:"inputTokens,omitempty"`
		OutputTokens int        `json:"outputTokens,omitempty"`
	}

	ExecuteResult struct {
		ExitCode int    `json:"exitCode"`
		Stdout   string `json:"stdout,omitempty"`
		Stderr   string `json:"stderr,omitempty"`
	}

	Options struct {
		Instruction       string        `json:"instruction,omitempty"`
		SystemInstruction string        `json:"systemInstruction,omitempty"`
		MaxSteps          int           `json:"maxSteps,omitempty"`
		MaxOutputBytes    int           `json:"maxOutputBytes,omitempty"`
		CommandTimeout    time.Duration `json:"commandTimeout,omitempty"`
	}

	Step struct {
		Text                      string       `json:"text,omitempty"`
		ToolCalls                 []ToolCall   `json:"toolCalls,omitempty"`
		ToolResults               []ToolResult `json:"toolResults,omitempty"`
		ModelDurationMilliseconds int64        `json:"modelDurationMilliseconds"`
		InputTokens               int          `json:"inputTokens,omitempty"`
		OutputTokens              int          `json:"outputTokens,omitempty"`
	}

	Result struct {
		Steps        []Step     `json:"steps"`
		StopReason   StopReason `json:"stopReason"`
		InputTokens  int        `json:"inputTokens"`
		OutputTokens int        `json:"outputTokens"`
	}

	Model interface {
		Generate(ctx context.Context, request Request) (*Response, error)
	}

	Executor interface {
		Execute(ctx context.Context, command string) (ExecuteResult, error)
	}
)

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

	result, messages := &Result{Steps: []Step{}}, []Message{{Role: RoleUser, Text: options.Instruction}}
	for {
		if err := ctx.Err(); err != nil {
			result.StopReason = StopReasonContextDone
			return result, err
		}

		start := time.Now()
		response, err := model.Generate(ctx, Request{SystemInstruction: options.SystemInstruction, Messages: messages})
		modelDuration := time.Since(start)

		if ctxErr := ctx.Err(); ctxErr != nil {
			result.StopReason = StopReasonContextDone
			return result, ctxErr
		}
		if err != nil || response == nil {
			result.StopReason = StopReasonModelError
			if err == nil {
				err = errors.New("model returned nil response without error")
			}
			return result, err
		}

		result.InputTokens += response.InputTokens
		result.OutputTokens += response.OutputTokens

		var toolResults []ToolResult
		appendStep := func() {
			result.Steps = append(result.Steps, Step{
				Text:                      response.Text,
				ToolCalls:                 append([]ToolCall(nil), response.ToolCalls...),
				ToolResults:               append([]ToolResult(nil), toolResults...),
				ModelDurationMilliseconds: modelDuration.Milliseconds(),
				InputTokens:               response.InputTokens,
				OutputTokens:              response.OutputTokens,
			})
		}

		if len(response.ToolCalls) == 0 {
			appendStep()
			result.StopReason = StopReasonCompleted
			return result, nil
		}

		for _, toolCall := range response.ToolCalls {
			cmdStart := time.Now()
			cmdCtx, cancel := context.WithTimeout(ctx, commandTimeout)
			execResult, execErr := executor.Execute(cmdCtx, toolCall.Command)
			cancel()

			if ctxErr := ctx.Err(); ctxErr != nil {
				appendStep()
				result.StopReason = StopReasonContextDone
				return result, ctxErr
			}
			toolResults = append(toolResults, formatToolResult(toolCall, execResult, execErr, time.Since(cmdStart), maxOutputBytes))
		}

		appendStep()
		messages = append(messages,
			Message{Role: RoleAssistant, Text: response.Text, ToolCalls: append([]ToolCall(nil), response.ToolCalls...)},
			Message{Role: RoleTool, ToolResults: append([]ToolResult(nil), toolResults...)},
		)
		if len(result.Steps) >= maxSteps {
			result.StopReason = StopReasonMaxSteps
			return result, nil
		}
	}
}

func formatToolResult(call ToolCall, res ExecuteResult, err error, d time.Duration, maxBytes int) ToolResult {
	output, exitCode := res.Stdout, res.ExitCode
	if res.Stderr != "" {
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		output += res.Stderr
	}
	if err != nil {
		if exitCode == 0 {
			exitCode = 1
		}
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		output += err.Error()
	}
	return ToolResult{
		ID:                   call.ID,
		Command:              call.Command,
		Output:               truncateOutput(output, maxBytes),
		ExitCode:             exitCode,
		DurationMilliseconds: d.Milliseconds(),
	}
}

func truncateOutput(output string, maxOutputBytes int) string {
	if maxOutputBytes <= 0 || len(output) <= maxOutputBytes {
		return output
	}
	head := maxOutputBytes / 2
	for head > 0 && !utf8.RuneStart(output[head]) {
		head--
	}
	tail := len(output) - (maxOutputBytes - (maxOutputBytes / 2))
	for tail < len(output) && !utf8.RuneStart(output[tail]) {
		tail++
	}
	return fmt.Sprintf("%s[... %d bytes omitted ...]%s", output[:head], tail-head, output[tail:])
}
