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
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type scriptedModelTurn struct {
	response *Response
	err      error
	generate func(request Request) (*Response, error)
}

type scriptedModel struct {
	turns            []scriptedModelTurn
	turnIndex        int
	requestsReceived []Request
}

func (model *scriptedModel) Generate(ctx context.Context, request Request) (*Response, error) {
	model.requestsReceived = append(model.requestsReceived, request)
	if model.turnIndex >= len(model.turns) {
		return nil, errors.New("unexpected Generate call past configured scripted turns")
	}
	turn := model.turns[model.turnIndex]
	model.turnIndex++
	if turn.generate != nil {
		return turn.generate(request)
	}
	return turn.response, turn.err
}

type scriptedExecution struct {
	result  ExecuteResult
	err     error
	execute func(ctx context.Context, command string) (ExecuteResult, error)
}

type scriptedExecutor struct {
	executions       []scriptedExecution
	executionIndex   int
	executedCommands []string
}

func (executor *scriptedExecutor) Execute(ctx context.Context, command string) (ExecuteResult, error) {
	executor.executedCommands = append(executor.executedCommands, command)
	if executor.executionIndex >= len(executor.executions) {
		return ExecuteResult{ExitCode: 0}, nil
	}
	execution := executor.executions[executor.executionIndex]
	executor.executionIndex++
	if execution.execute != nil {
		return execution.execute(ctx, command)
	}
	return execution.result, execution.err
}

func TestCompletionAfterNSteps(t *testing.T) {
	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				response: &Response{
					Text: "running first command",
					ToolCalls: []ToolCall{
						{ID: "call-1", Command: "echo step-1"},
					},
					InputTokens:  10,
					OutputTokens: 5,
				},
			},
			{
				response: &Response{
					Text: "running second command",
					ToolCalls: []ToolCall{
						{ID: "call-2", Command: "echo step-2"},
					},
					InputTokens:  20,
					OutputTokens: 8,
				},
			},
			{
				response: &Response{
					Text:         "task completed successfully",
					InputTokens:  30,
					OutputTokens: 12,
				},
			},
		},
	}

	executor := &scriptedExecutor{
		executions: []scriptedExecution{
			{result: ExecuteResult{ExitCode: 0, Stdout: "step-1\n"}},
			{result: ExecuteResult{ExitCode: 0, Stdout: "step-2\n"}},
		},
	}

	options := Options{
		Instruction: "execute two steps",
	}

	result, err := Run(t.Context(), model, executor, options)
	if err != nil {
		t.Fatalf("unexpected error from Run: %v", err)
	}

	if result.StopReason != StopReasonCompleted {
		t.Errorf("expected StopReason %q, got %q", StopReasonCompleted, result.StopReason)
	}

	if len(result.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(result.Steps))
	}

	if len(result.Steps[0].ToolCalls) != 1 || result.Steps[0].ToolCalls[0].Command != "echo step-1" {
		t.Errorf("step 0 tool call mismatch: %+v", result.Steps[0].ToolCalls)
	}
	if len(result.Steps[0].ToolResults) != 1 || result.Steps[0].ToolResults[0].Output != "step-1\n" {
		t.Errorf("step 0 tool result mismatch: %+v", result.Steps[0].ToolResults)
	}
	if result.Steps[0].ToolResults[0].ID != result.Steps[0].ToolCalls[0].ID {
		t.Errorf("step 0 tool result ID %q does not match tool call ID %q",
			result.Steps[0].ToolResults[0].ID, result.Steps[0].ToolCalls[0].ID)
	}
	if result.Steps[0].ToolResults[0].Command != result.Steps[0].ToolCalls[0].Command {
		t.Errorf("step 0 tool result Command %q does not match tool call Command %q",
			result.Steps[0].ToolResults[0].Command, result.Steps[0].ToolCalls[0].Command)
	}

	if len(result.Steps[1].ToolCalls) != 1 || result.Steps[1].ToolCalls[0].Command != "echo step-2" {
		t.Errorf("step 1 tool call mismatch: %+v", result.Steps[1].ToolCalls)
	}
	if len(result.Steps[1].ToolResults) != 1 || result.Steps[1].ToolResults[0].Output != "step-2\n" {
		t.Errorf("step 1 tool result mismatch: %+v", result.Steps[1].ToolResults)
	}
	if result.Steps[1].ToolResults[0].ID != result.Steps[1].ToolCalls[0].ID {
		t.Errorf("step 1 tool result ID %q does not match tool call ID %q",
			result.Steps[1].ToolResults[0].ID, result.Steps[1].ToolCalls[0].ID)
	}
	if result.Steps[1].ToolResults[0].Command != result.Steps[1].ToolCalls[0].Command {
		t.Errorf("step 1 tool result Command %q does not match tool call Command %q",
			result.Steps[1].ToolResults[0].Command, result.Steps[1].ToolCalls[0].Command)
	}

	if len(result.Steps[2].ToolCalls) != 0 {
		t.Errorf("step 2 should have 0 tool calls, got: %+v", result.Steps[2].ToolCalls)
	}
	if result.Steps[2].Text != "task completed successfully" {
		t.Errorf("step 2 text mismatch: got %q", result.Steps[2].Text)
	}

	if len(model.requestsReceived) != 3 {
		t.Fatalf("expected 3 requests to model, got %d", len(model.requestsReceived))
	}

	firstRequestMessages := model.requestsReceived[0].Messages
	if len(firstRequestMessages) != 1 || firstRequestMessages[0].Role != RoleUser || firstRequestMessages[0].Text != "execute two steps" {
		t.Errorf("unexpected initial user message: %+v", firstRequestMessages)
	}

	secondRequestMessages := model.requestsReceived[1].Messages
	if len(secondRequestMessages) != 3 {
		t.Fatalf("expected 3 messages in second request, got %d", len(secondRequestMessages))
	}
	if secondRequestMessages[1].Role != RoleAssistant || secondRequestMessages[1].Text != "running first command" ||
		len(secondRequestMessages[1].ToolCalls) != 1 || secondRequestMessages[1].ToolCalls[0].Command != "echo step-1" {
		t.Errorf("assistant history message missing text or tool calls: %+v", secondRequestMessages[1])
	}

	thirdRequestMessages := model.requestsReceived[2].Messages
	if len(thirdRequestMessages) != 5 {
		t.Fatalf("expected 5 messages in third request, got %d", len(thirdRequestMessages))
	}
	if thirdRequestMessages[1].Role != RoleAssistant || thirdRequestMessages[2].Role != RoleTool ||
		thirdRequestMessages[3].Role != RoleAssistant || thirdRequestMessages[4].Role != RoleTool {
		t.Errorf("unexpected role progression in conversation: %+v", thirdRequestMessages)
	}
	if thirdRequestMessages[3].Role != RoleAssistant || thirdRequestMessages[3].Text != "running second command" ||
		len(thirdRequestMessages[3].ToolCalls) != 1 || thirdRequestMessages[3].ToolCalls[0].Command != "echo step-2" {
		t.Errorf("second assistant history message missing text or tool calls: %+v", thirdRequestMessages[3])
	}
}

func TestMaxSteps(t *testing.T) {
	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				response: &Response{
					Text: "turn 1",
					ToolCalls: []ToolCall{
						{ID: "call-1", Command: "echo 1"},
					},
				},
			},
			{
				response: &Response{
					Text: "turn 2",
					ToolCalls: []ToolCall{
						{ID: "call-2", Command: "echo 2"},
					},
				},
			},
			{
				response: &Response{
					Text: "turn 3",
					ToolCalls: []ToolCall{
						{ID: "call-3", Command: "echo 3"},
					},
				},
			},
		},
	}

	executor := &scriptedExecutor{}

	options := Options{
		Instruction: "run indefinitely",
		MaxSteps:    2,
	}

	result, err := Run(t.Context(), model, executor, options)
	if err != nil {
		t.Fatalf("unexpected error from Run: %v", err)
	}

	if result.StopReason != StopReasonMaxSteps {
		t.Errorf("expected StopReason %q, got %q", StopReasonMaxSteps, result.StopReason)
	}

	if len(result.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(result.Steps))
	}

	if len(model.requestsReceived) != 2 {
		t.Errorf("expected 2 model calls, got %d", len(model.requestsReceived))
	}
}

func TestDefaultOptions(t *testing.T) {
	if DefaultMaxSteps != 50 {
		t.Errorf("expected DefaultMaxSteps 50, got %d", DefaultMaxSteps)
	}
	if DefaultMaxOutputBytes != 16*1024 {
		t.Errorf("expected DefaultMaxOutputBytes 16384, got %d", DefaultMaxOutputBytes)
	}
	if DefaultCommandTimeout != 2*time.Minute {
		t.Errorf("expected DefaultCommandTimeout 2m, got %v", DefaultCommandTimeout)
	}

	t.Run("DefaultMaxStepsApplied", func(t *testing.T) {
		turns := make([]scriptedModelTurn, 60)
		for i := range turns {
			turns[i] = scriptedModelTurn{
				response: &Response{
					Text: "looping",
					ToolCalls: []ToolCall{
						{ID: "call", Command: "echo loop"},
					},
				},
			}
		}
		model := &scriptedModel{turns: turns}
		executor := &scriptedExecutor{}

		result, err := Run(t.Context(), model, executor, Options{Instruction: "test defaults"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.StopReason != StopReasonMaxSteps {
			t.Errorf("expected StopReasonMaxSteps with default MaxSteps, got %v", result.StopReason)
		}
		if len(result.Steps) != 50 {
			t.Errorf("expected default 50 steps, got %d", len(result.Steps))
		}
	})

	t.Run("DefaultMaxOutputBytesApplied", func(t *testing.T) {
		largeOutput := strings.Repeat("A", 16*1024+10)
		model := &scriptedModel{
			turns: []scriptedModelTurn{
				{
					response: &Response{
						Text: "run command with large output",
						ToolCalls: []ToolCall{
							{ID: "call-1", Command: "cat large.txt"},
						},
					},
				},
				{
					response: &Response{Text: "done"},
				},
			},
		}
		executor := &scriptedExecutor{
			executions: []scriptedExecution{
				{result: ExecuteResult{ExitCode: 0, Stdout: largeOutput}},
			},
		}

		result, err := Run(t.Context(), model, executor, Options{Instruction: "test default output bytes"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		toolResultOutput := result.Steps[0].ToolResults[0].Output
		if !strings.Contains(toolResultOutput, "[... 10 bytes omitted ...]") {
			t.Errorf("expected truncation with default MaxOutputBytes, got %q", toolResultOutput)
		}
	})

	t.Run("DefaultCommandTimeoutApplied", func(t *testing.T) {
		var observedTimeout time.Duration
		model := &scriptedModel{
			turns: []scriptedModelTurn{
				{
					response: &Response{
						Text: "check timeout",
						ToolCalls: []ToolCall{
							{ID: "call-1", Command: "check-timeout"},
						},
					},
				},
				{
					response: &Response{Text: "done"},
				},
			},
		}
		executor := &scriptedExecutor{
			executions: []scriptedExecution{
				{
					execute: func(ctx context.Context, command string) (ExecuteResult, error) {
						deadline, ok := ctx.Deadline()
						if ok {
							observedTimeout = time.Until(deadline)
						}
						return ExecuteResult{ExitCode: 0}, nil
					},
				},
			},
		}

		_, err := Run(t.Context(), model, executor, Options{Instruction: "test default command timeout"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if observedTimeout < 110*time.Second || observedTimeout > 125*time.Second {
			t.Errorf("expected default timeout around 2m, got %v", observedTimeout)
		}
	})
}

func TestFailingCommandFedBackToModel(t *testing.T) {
	var toolResultObservedByModel ToolResult
	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				response: &Response{
					Text: "running a command that fails",
					ToolCalls: []ToolCall{
						{ID: "failing-call", Command: "cat non_existent_file"},
					},
				},
			},
			{
				generate: func(request Request) (*Response, error) {
					lastMessage := request.Messages[len(request.Messages)-1]
					if lastMessage.Role == RoleTool && len(lastMessage.ToolResults) > 0 {
						toolResultObservedByModel = lastMessage.ToolResults[0]
					}
					return &Response{
						Text: "handled failure and recovered",
					}, nil
				},
			},
		},
	}

	executor := &scriptedExecutor{
		executions: []scriptedExecution{
			{
				result: ExecuteResult{
					ExitCode: 1,
					Stderr:   "cat: non_existent_file: No such file or directory\n",
				},
			},
		},
	}

	options := Options{
		Instruction: "read missing file",
	}

	result, err := Run(t.Context(), model, executor, options)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.StopReason != StopReasonCompleted {
		t.Errorf("expected StopReason %q, got %q", StopReasonCompleted, result.StopReason)
	}

	if toolResultObservedByModel.ID != "failing-call" {
		t.Errorf("expected tool result ID %q, got %q", "failing-call", toolResultObservedByModel.ID)
	}
	if toolResultObservedByModel.Command != "cat non_existent_file" {
		t.Errorf("expected tool result Command %q, got %q", "cat non_existent_file", toolResultObservedByModel.Command)
	}
	if toolResultObservedByModel.ExitCode != 1 {
		t.Errorf("expected exit code 1 fed back to model, got %d", toolResultObservedByModel.ExitCode)
	}
	if !strings.Contains(toolResultObservedByModel.Output, "No such file or directory") {
		t.Errorf("expected error message in tool result output, got %q", toolResultObservedByModel.Output)
	}

	if len(result.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(result.Steps))
	}
	if result.Steps[0].ToolResults[0].ExitCode != 1 {
		t.Errorf("expected recorded step exit code 1, got %d", result.Steps[0].ToolResults[0].ExitCode)
	}
}

func TestExecuteTimeoutFedBackToModel(t *testing.T) {
	var toolResultObservedByModel ToolResult
	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				response: &Response{
					Text: "running slow command",
					ToolCalls: []ToolCall{
						{ID: "timeout-call", Command: "sleep 10"},
					},
				},
			},
			{
				generate: func(request Request) (*Response, error) {
					lastMessage := request.Messages[len(request.Messages)-1]
					if lastMessage.Role == RoleTool && len(lastMessage.ToolResults) > 0 {
						toolResultObservedByModel = lastMessage.ToolResults[0]
					}
					return &Response{
						Text: "handled timeout gracefully",
					}, nil
				},
			},
		},
	}

	executor := &scriptedExecutor{
		executions: []scriptedExecution{
			{
				execute: func(ctx context.Context, command string) (ExecuteResult, error) {
					<-ctx.Done()
					return ExecuteResult{ExitCode: 0}, ctx.Err()
				},
			},
		},
	}

	options := Options{
		Instruction:    "run command that will time out",
		CommandTimeout: 20 * time.Millisecond,
	}

	result, err := Run(t.Context(), model, executor, options)
	if err != nil {
		t.Fatalf("unexpected error from Run: %v", err)
	}

	if result.StopReason != StopReasonCompleted {
		t.Errorf("expected StopReason %q, got %q", StopReasonCompleted, result.StopReason)
	}

	if toolResultObservedByModel.ID != "timeout-call" {
		t.Errorf("expected tool result ID %q, got %q", "timeout-call", toolResultObservedByModel.ID)
	}
	if toolResultObservedByModel.ExitCode == 0 {
		t.Errorf("expected non-zero exit code on timeout error, got 0")
	}
	if !strings.Contains(toolResultObservedByModel.Output, "context deadline exceeded") {
		t.Errorf("expected deadline exceeded in tool result output, got %q", toolResultObservedByModel.Output)
	}
}

func TestParentDeadlineToContextDoneDuringCommand(t *testing.T) {
	parentCtx, cancelParent := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancelParent()

	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				response: &Response{
					Text: "executing command during parent deadline expiration",
					ToolCalls: []ToolCall{
						{ID: "call-1", Command: "sleep 5"},
					},
				},
			},
		},
	}

	executor := &scriptedExecutor{
		executions: []scriptedExecution{
			{
				execute: func(ctx context.Context, command string) (ExecuteResult, error) {
					<-parentCtx.Done()
					return ExecuteResult{ExitCode: 0}, parentCtx.Err()
				},
			},
		},
	}

	result, err := Run(parentCtx, model, executor, Options{
		Instruction:    "run under parent deadline",
		CommandTimeout: 1 * time.Minute,
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
	if result.StopReason != StopReasonContextDone {
		t.Errorf("expected StopReason %q, got %q", StopReasonContextDone, result.StopReason)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("expected partial Step appended on parent deadline, got %d steps", len(result.Steps))
	}
	if len(result.Steps[0].ToolResults) != 0 {
		t.Errorf("expected 0 completed tool results for interrupted command, got %d", len(result.Steps[0].ToolResults))
	}
	if len(result.Steps[0].ToolCalls) != 1 {
		t.Errorf("expected 1 tool call on partial step, got %d", len(result.Steps[0].ToolCalls))
	}
}

func TestContextCancellationMidStep(t *testing.T) {
	parentCtx, cancelParent := context.WithCancel(t.Context())

	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				response: &Response{
					Text: "turn with two tool calls",
					ToolCalls: []ToolCall{
						{ID: "call-1", Command: "echo first"},
						{ID: "call-2", Command: "echo second"},
					},
					InputTokens:  7,
					OutputTokens: 3,
				},
			},
		},
	}

	executor := &scriptedExecutor{
		executions: []scriptedExecution{
			{
				result: ExecuteResult{ExitCode: 0, Stdout: "first output\n"},
			},
			{
				execute: func(ctx context.Context, command string) (ExecuteResult, error) {
					cancelParent()
					return ExecuteResult{ExitCode: 0}, context.Canceled
				},
			},
		},
	}

	result, err := Run(parentCtx, model, executor, Options{Instruction: "run two commands"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", err)
	}
	if result.StopReason != StopReasonContextDone {
		t.Errorf("expected StopReason %q, got %q", StopReasonContextDone, result.StopReason)
	}

	if len(result.Steps) != 1 {
		t.Fatalf("expected 1 partial step appended, got %d", len(result.Steps))
	}

	partialStep := result.Steps[0]
	if partialStep.Text != "turn with two tool calls" {
		t.Errorf("partial step text mismatch: got %q", partialStep.Text)
	}
	if len(partialStep.ToolCalls) != 2 {
		t.Errorf("expected both tool calls preserved on partial step, got %d", len(partialStep.ToolCalls))
	}
	if len(partialStep.ToolResults) != 1 {
		t.Fatalf("expected 1 tool result from first completed command, got %d", len(partialStep.ToolResults))
	}
	if partialStep.ToolResults[0].ID != "call-1" || partialStep.ToolResults[0].Output != "first output\n" {
		t.Errorf("unexpected first tool result: %+v", partialStep.ToolResults[0])
	}
	if partialStep.InputTokens != 7 || partialStep.OutputTokens != 3 {
		t.Errorf("step tokens mismatch: (%d, %d)", partialStep.InputTokens, partialStep.OutputTokens)
	}
	if result.InputTokens != 7 || result.OutputTokens != 3 {
		t.Errorf("result token totals mismatch: (%d, %d)", result.InputTokens, result.OutputTokens)
	}
}

func TestCancellationDuringGenerate(t *testing.T) {
	parentCtx, cancelParent := context.WithCancel(t.Context())

	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				generate: func(request Request) (*Response, error) {
					cancelParent()
					return nil, parentCtx.Err()
				},
			},
		},
	}

	executor := &scriptedExecutor{}

	result, err := Run(parentCtx, model, executor, Options{Instruction: "cancel during generate"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if result.StopReason != StopReasonContextDone {
		t.Errorf("expected StopReason %q, got %q", StopReasonContextDone, result.StopReason)
	}
	if len(result.Steps) != 0 {
		t.Errorf("expected 0 steps, got %d", len(result.Steps))
	}
}

func TestGenerateNilResponseReturnsModelError(t *testing.T) {
	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				response: nil,
				err:      nil,
			},
		},
	}

	executor := &scriptedExecutor{}

	result, err := Run(t.Context(), model, executor, Options{Instruction: "nil response"})
	if err == nil {
		t.Fatal("expected error from (nil, nil) Generate response, got nil")
	}
	if result.StopReason != StopReasonModelError {
		t.Errorf("expected StopReason %q, got %q", StopReasonModelError, result.StopReason)
	}
	if len(result.Steps) != 0 {
		t.Errorf("expected 0 steps, got %d", len(result.Steps))
	}
}

func TestGenerateErrorReturningPartialResult(t *testing.T) {
	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				response: &Response{
					Text: "step 1 succeeded",
					ToolCalls: []ToolCall{
						{ID: "call-1", Command: "echo success"},
					},
					InputTokens:  100,
					OutputTokens: 25,
				},
			},
			{
				err: errors.New("upstream model quota exhausted"),
			},
		},
	}

	executor := &scriptedExecutor{
		executions: []scriptedExecution{
			{result: ExecuteResult{ExitCode: 0, Stdout: "success\n"}},
		},
	}

	options := Options{
		Instruction: "execute until failure",
	}

	result, err := Run(t.Context(), model, executor, options)
	if err == nil {
		t.Fatal("expected error from Generate failure, got nil")
	}
	if !strings.Contains(err.Error(), "upstream model quota exhausted") {
		t.Errorf("unexpected error message: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil partial result")
	}

	if result.StopReason != StopReasonModelError {
		t.Errorf("expected StopReason %q, got %q", StopReasonModelError, result.StopReason)
	}

	if len(result.Steps) != 1 {
		t.Fatalf("expected 1 completed step in partial result, got %d", len(result.Steps))
	}
	if result.InputTokens != 100 {
		t.Errorf("expected 100 InputTokens, got %d", result.InputTokens)
	}
	if result.OutputTokens != 25 {
		t.Errorf("expected 25 OutputTokens, got %d", result.OutputTokens)
	}
}

func TestSystemInstructionPassedToModel(t *testing.T) {
	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				response: &Response{
					Text: "first turn",
					ToolCalls: []ToolCall{
						{ID: "c1", Command: "echo 1"},
					},
				},
			},
			{
				response: &Response{Text: "done"},
			},
		},
	}

	executor := &scriptedExecutor{}

	options := Options{
		Instruction:       "task instruction",
		SystemInstruction: "You are an expert engineer.",
	}

	_, err := Run(t.Context(), model, executor, options)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(model.requestsReceived) != 2 {
		t.Fatalf("expected 2 requests received, got %d", len(model.requestsReceived))
	}

	for i, req := range model.requestsReceived {
		if req.SystemInstruction != "You are an expert engineer." {
			t.Errorf("request %d: expected SystemInstruction %q, got %q",
				i, "You are an expert engineer.", req.SystemInstruction)
		}
	}
}

func TestTruncation(t *testing.T) {
	t.Run("ExactEqualityEvenSize", func(t *testing.T) {
		got := truncateOutput("abcdef", 6)
		if got != "abcdef" {
			t.Errorf("expected no truncation for exact equality, got %q", got)
		}
	})

	t.Run("OffByOneEvenSize", func(t *testing.T) {
		got := truncateOutput("abcdefg", 6)
		if got != "abc[... 1 bytes omitted ...]efg" {
			t.Errorf("unexpected truncation: %q", got)
		}
	})

	t.Run("ExactEqualityOddSize", func(t *testing.T) {
		got := truncateOutput("1234567", 7)
		if got != "1234567" {
			t.Errorf("expected no truncation for exact equality odd size, got %q", got)
		}
	})

	t.Run("OffByOneOddSize", func(t *testing.T) {
		got := truncateOutput("12345678", 7)
		if got != "123[... 1 bytes omitted ...]5678" {
			t.Errorf("unexpected truncation: %q", got)
		}
	})

	t.Run("MultiByteRuneBoundaries", func(t *testing.T) {
		multiByteString := "ééééééééééé"
		got := truncateOutput(multiByteString, 7)
		if !utf8.ValidString(got) {
			t.Fatalf("truncated output contains invalid UTF-8: %q", got)
		}
		if got != "é[... 16 bytes omitted ...]éé" {
			t.Errorf("unexpected multi-byte truncation: %q", got)
		}
	})

	t.Run("FullExecutionTruncation", func(t *testing.T) {
		fullOutput := "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
		maxOutputBytes := 20

		var toolResultObservedByModel ToolResult
		model := &scriptedModel{
			turns: []scriptedModelTurn{
				{
					response: &Response{
						Text: "reading big output",
						ToolCalls: []ToolCall{
							{ID: "big-call", Command: "cat big.txt"},
						},
					},
				},
				{
					generate: func(request Request) (*Response, error) {
						lastMessage := request.Messages[len(request.Messages)-1]
						if lastMessage.Role == RoleTool && len(lastMessage.ToolResults) > 0 {
							toolResultObservedByModel = lastMessage.ToolResults[0]
						}
						return &Response{Text: "done"}, nil
					},
				},
			},
		}

		executor := &scriptedExecutor{
			executions: []scriptedExecution{
				{result: ExecuteResult{ExitCode: 0, Stdout: fullOutput}},
			},
		}

		result, err := Run(t.Context(), model, executor, Options{
			Instruction:    "read big file",
			MaxOutputBytes: maxOutputBytes,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedHead := fullOutput[:10]
		expectedTail := fullOutput[len(fullOutput)-10:]
		expectedMarker := "[... 42 bytes omitted ...]"

		truncatedOutput := toolResultObservedByModel.Output
		if !strings.HasPrefix(truncatedOutput, expectedHead) {
			t.Errorf("expected output to start with %q, got %q", expectedHead, truncatedOutput)
		}
		if !strings.HasSuffix(truncatedOutput, expectedTail) {
			t.Errorf("expected output to end with %q, got %q", expectedTail, truncatedOutput)
		}
		if !strings.Contains(truncatedOutput, expectedMarker) {
			t.Errorf("expected output to contain %q, got %q", expectedMarker, truncatedOutput)
		}

		if result.Steps[0].ToolResults[0].Output != truncatedOutput {
			t.Errorf("step tool result output should match truncated output")
		}
	})
}

func TestDurationsRecorded(t *testing.T) {
	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				generate: func(request Request) (*Response, error) {
					time.Sleep(5 * time.Millisecond)
					return &Response{
						Text: "command turn",
						ToolCalls: []ToolCall{
							{ID: "call-1", Command: "sleep-command"},
						},
					}, nil
				},
			},
			{
				generate: func(request Request) (*Response, error) {
					time.Sleep(5 * time.Millisecond)
					return &Response{Text: "final turn"}, nil
				},
			},
		},
	}

	executor := &scriptedExecutor{
		executions: []scriptedExecution{
			{
				execute: func(ctx context.Context, command string) (ExecuteResult, error) {
					time.Sleep(5 * time.Millisecond)
					return ExecuteResult{ExitCode: 0, Stdout: "done\n"}, nil
				},
			},
		},
	}

	result, err := Run(t.Context(), model, executor, Options{Instruction: "check durations"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(result.Steps))
	}

	step0 := result.Steps[0]
	if step0.DurationMilliseconds <= 0 {
		t.Errorf("expected positive step 0 model DurationMilliseconds, got %d", step0.DurationMilliseconds)
	}
	if len(step0.ToolResults) != 1 {
		t.Fatalf("expected 1 tool result in step 0, got %d", len(step0.ToolResults))
	}
	if step0.ToolResults[0].DurationMilliseconds <= 0 {
		t.Errorf("expected positive tool result DurationMilliseconds, got %d", step0.ToolResults[0].DurationMilliseconds)
	}

	step1 := result.Steps[1]
	if step1.DurationMilliseconds <= 0 {
		t.Errorf("expected positive step 1 model DurationMilliseconds, got %d", step1.DurationMilliseconds)
	}
}

func TestTokenTotals(t *testing.T) {
	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				response: &Response{
					Text: "first turn",
					ToolCalls: []ToolCall{
						{ID: "call-1", Command: "echo 1"},
					},
					InputTokens:  120,
					OutputTokens: 30,
				},
			},
			{
				response: &Response{
					Text: "second turn",
					ToolCalls: []ToolCall{
						{ID: "call-2", Command: "echo 2"},
					},
					InputTokens:  200,
					OutputTokens: 50,
				},
			},
			{
				response: &Response{
					Text:         "final turn",
					InputTokens:  280,
					OutputTokens: 40,
				},
			},
		},
	}

	executor := &scriptedExecutor{}

	result, err := Run(t.Context(), model, executor, Options{Instruction: "count tokens"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.InputTokens != (120 + 200 + 280) {
		t.Errorf("expected total InputTokens %d, got %d", 120+200+280, result.InputTokens)
	}
	if result.OutputTokens != (30 + 50 + 40) {
		t.Errorf("expected total OutputTokens %d, got %d", 30+50+40, result.OutputTokens)
	}

	if result.Steps[0].InputTokens != 120 || result.Steps[0].OutputTokens != 30 {
		t.Errorf("step 0 tokens mismatch: %+v", result.Steps[0])
	}
	if result.Steps[1].InputTokens != 200 || result.Steps[1].OutputTokens != 50 {
		t.Errorf("step 1 tokens mismatch: %+v", result.Steps[1])
	}
	if result.Steps[2].InputTokens != 280 || result.Steps[2].OutputTokens != 40 {
		t.Errorf("step 2 tokens mismatch: %+v", result.Steps[2])
	}
}

func TestContextCancellation(t *testing.T) {
	cancelledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	model := &scriptedModel{}
	executor := &scriptedExecutor{}

	result, err := Run(cancelledCtx, model, executor, Options{Instruction: "do nothing"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", err)
	}
	if result.StopReason != StopReasonContextDone {
		t.Errorf("expected StopReason %q, got %q", StopReasonContextDone, result.StopReason)
	}
	if len(result.Steps) != 0 {
		t.Errorf("expected 0 steps, got %d", len(result.Steps))
	}
}

func TestJSONSerializability(t *testing.T) {
	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				response: &Response{
					Text: "executing command",
					ToolCalls: []ToolCall{
						{ID: "c1", Command: "echo hello"},
					},
					InputTokens:  15,
					OutputTokens: 5,
				},
			},
			{
				response: &Response{
					Text:         "finished",
					InputTokens:  20,
					OutputTokens: 2,
				},
			},
		},
	}

	executor := &scriptedExecutor{
		executions: []scriptedExecution{
			{result: ExecuteResult{ExitCode: 0, Stdout: "hello\n"}},
		},
	}

	result, err := Run(t.Context(), model, executor, Options{Instruction: "test json"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal Result to JSON: %v", err)
	}

	var unmarshaled Result
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal JSON to Result: %v", err)
	}

	if unmarshaled.StopReason != result.StopReason {
		t.Errorf("stopReason mismatch: got %v, want %v", unmarshaled.StopReason, result.StopReason)
	}
	if unmarshaled.InputTokens != result.InputTokens || unmarshaled.OutputTokens != result.OutputTokens {
		t.Errorf("token totals mismatch: got (%d, %d), want (%d, %d)",
			unmarshaled.InputTokens, unmarshaled.OutputTokens, result.InputTokens, result.OutputTokens)
	}
	if len(unmarshaled.Steps) != len(result.Steps) {
		t.Fatalf("steps count mismatch: got %d, want %d", len(unmarshaled.Steps), len(result.Steps))
	}
	if len(unmarshaled.Steps[0].ToolCalls) != 1 || unmarshaled.Steps[0].ToolCalls[0].Command != "echo hello" {
		t.Errorf("step 0 tool calls mismatch: %+v", unmarshaled.Steps[0].ToolCalls)
	}
	if len(unmarshaled.Steps[0].ToolResults) != 1 || unmarshaled.Steps[0].ToolResults[0].Command != "echo hello" {
		t.Errorf("step 0 tool results mismatch: %+v", unmarshaled.Steps[0].ToolResults)
	}
}

func TestMultipleToolCallsInOneTurn(t *testing.T) {
	var messagesInSecondTurn []Message
	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				response: &Response{
					Text: "running two commands in sequence",
					ToolCalls: []ToolCall{
						{ID: "c1", Command: "echo first"},
						{ID: "c2", Command: "echo second"},
					},
				},
			},
			{
				generate: func(request Request) (*Response, error) {
					messagesInSecondTurn = request.Messages
					return &Response{Text: "done"}, nil
				},
			},
		},
	}

	executor := &scriptedExecutor{
		executions: []scriptedExecution{
			{result: ExecuteResult{ExitCode: 0, Stdout: "first\n"}},
			{result: ExecuteResult{ExitCode: 0, Stdout: "second\n"}},
		},
	}

	result, err := Run(t.Context(), model, executor, Options{Instruction: "sequential tools"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(executor.executedCommands) != 2 ||
		executor.executedCommands[0] != "echo first" ||
		executor.executedCommands[1] != "echo second" {
		t.Fatalf("tool calls executed in incorrect order: %+v", executor.executedCommands)
	}

	if len(result.Steps[0].ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls in step 0, got %d", len(result.Steps[0].ToolCalls))
	}
	if len(result.Steps[0].ToolResults) != 2 {
		t.Fatalf("expected 2 tool results in step 0, got %d", len(result.Steps[0].ToolResults))
	}
	if result.Steps[0].ToolResults[0].ID != "c1" || result.Steps[0].ToolResults[0].Command != "echo first" {
		t.Errorf("tool result 0 mismatch: %+v", result.Steps[0].ToolResults[0])
	}
	if result.Steps[0].ToolResults[1].ID != "c2" || result.Steps[0].ToolResults[1].Command != "echo second" {
		t.Errorf("tool result 1 mismatch: %+v", result.Steps[0].ToolResults[1])
	}

	if len(messagesInSecondTurn) != 3 {
		t.Fatalf("expected 3 messages in second turn, got %d", len(messagesInSecondTurn))
	}
	toolMessage := messagesInSecondTurn[2]
	if toolMessage.Role != RoleTool {
		t.Errorf("expected RoleTool, got %v", toolMessage.Role)
	}
	if len(toolMessage.ToolResults) != 2 {
		t.Errorf("expected 2 tool results in single tool message, got %d", len(toolMessage.ToolResults))
	}
	if toolMessage.ToolResults[0].ID != "c1" || toolMessage.ToolResults[1].ID != "c2" {
		t.Errorf("tool message results IDs mismatch: %+v", toolMessage.ToolResults)
	}
}
