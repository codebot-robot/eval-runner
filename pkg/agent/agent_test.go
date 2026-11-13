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
)

type scriptedModelTurn struct {
	response *Response
	err      error
	fn       func(request Request) (*Response, error)
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
	if turn.fn != nil {
		return turn.fn(request)
	}
	return turn.response, turn.err
}

type scriptedExecution struct {
	result ExecuteResult
	err    error
	fn     func(ctx context.Context, command string) (ExecuteResult, error)
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
	if execution.fn != nil {
		return execution.fn(ctx, command)
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

	result, err := Run(context.Background(), model, executor, options)
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

	if len(result.Steps[1].ToolCalls) != 1 || result.Steps[1].ToolCalls[0].Command != "echo step-2" {
		t.Errorf("step 1 tool call mismatch: %+v", result.Steps[1].ToolCalls)
	}
	if len(result.Steps[1].ToolResults) != 1 || result.Steps[1].ToolResults[0].Output != "step-2\n" {
		t.Errorf("step 1 tool result mismatch: %+v", result.Steps[1].ToolResults)
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

	thirdRequestMessages := model.requestsReceived[2].Messages
	if len(thirdRequestMessages) != 5 {
		t.Fatalf("expected 5 messages in third request, got %d", len(thirdRequestMessages))
	}
	if thirdRequestMessages[1].Role != RoleAssistant || thirdRequestMessages[2].Role != RoleTool ||
		thirdRequestMessages[3].Role != RoleAssistant || thirdRequestMessages[4].Role != RoleTool {
		t.Errorf("unexpected role progression in conversation: %+v", thirdRequestMessages)
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
			{
				response: &Response{
					Text: "turn 4 should not be reached",
					ToolCalls: []ToolCall{
						{ID: "call-4", Command: "echo 4"},
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

	result, err := Run(context.Background(), model, executor, options)
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
				fn: func(request Request) (*Response, error) {
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

	result, err := Run(context.Background(), model, executor, options)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.StopReason != StopReasonCompleted {
		t.Errorf("expected StopReason %q, got %q", StopReasonCompleted, result.StopReason)
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
				fn: func(request Request) (*Response, error) {
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
				fn: func(ctx context.Context, command string) (ExecuteResult, error) {
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

	result, err := Run(context.Background(), model, executor, options)
	if err != nil {
		t.Fatalf("unexpected error from Run: %v", err)
	}

	if result.StopReason != StopReasonCompleted {
		t.Errorf("expected StopReason %q, got %q", StopReasonCompleted, result.StopReason)
	}

	if toolResultObservedByModel.ExitCode == 0 {
		t.Errorf("expected non-zero exit code on timeout error, got 0")
	}
	if !strings.Contains(toolResultObservedByModel.Output, "context deadline exceeded") {
		t.Errorf("expected deadline exceeded in tool result output, got %q", toolResultObservedByModel.Output)
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

	result, err := Run(context.Background(), model, executor, options)
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

func TestTruncation(t *testing.T) {
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
				fn: func(request Request) (*Response, error) {
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

	options := Options{
		Instruction:    "read big file",
		MaxOutputBytes: maxOutputBytes,
	}

	result, err := Run(context.Background(), model, executor, options)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedHalf := maxOutputBytes / 2
	expectedHead := fullOutput[:expectedHalf]
	expectedTail := fullOutput[len(fullOutput)-(maxOutputBytes-expectedHalf):]
	expectedOmitted := len(fullOutput) - maxOutputBytes
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

	if expectedOmitted != 42 {
		t.Fatalf("calculated omitted mismatch: %d", expectedOmitted)
	}

	if result.Steps[0].ToolResults[0].Output != truncatedOutput {
		t.Errorf("step tool result output should match truncated output")
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

	result, err := Run(context.Background(), model, executor, Options{Instruction: "count tokens"})
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
	t.Run("cancelled before run starts", func(t *testing.T) {
		cancelledCtx, cancel := context.WithCancel(context.Background())
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
	})

	t.Run("cancelled during execution", func(t *testing.T) {
		parentCtx, cancelParent := context.WithCancel(context.Background())

		model := &scriptedModel{
			turns: []scriptedModelTurn{
				{
					response: &Response{
						Text: "step before cancellation",
						ToolCalls: []ToolCall{
							{ID: "call-1", Command: "cancel-now"},
						},
					},
				},
			},
		}

		executor := &scriptedExecutor{
			executions: []scriptedExecution{
				{
					fn: func(ctx context.Context, command string) (ExecuteResult, error) {
						cancelParent()
						return ExecuteResult{ExitCode: 0}, nil
					},
				},
			},
		}

		result, err := Run(parentCtx, model, executor, Options{Instruction: "cancel during run"})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled error, got %v", err)
		}
		if result.StopReason != StopReasonContextDone {
			t.Errorf("expected StopReason %q, got %q", StopReasonContextDone, result.StopReason)
		}
	})
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

	result, err := Run(context.Background(), model, executor, Options{Instruction: "test json"})
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
}

func TestMultipleToolCallsInOneTurn(t *testing.T) {
	var messagesInSecondTurn []Message
	model := &scriptedModel{
		turns: []scriptedModelTurn{
			{
				response: &Response{
					Text: "running two parallel commands",
					ToolCalls: []ToolCall{
						{ID: "c1", Command: "echo first"},
						{ID: "c2", Command: "echo second"},
					},
				},
			},
			{
				fn: func(request Request) (*Response, error) {
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

	result, err := Run(context.Background(), model, executor, Options{Instruction: "parallel tools"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Steps[0].ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls in step 0, got %d", len(result.Steps[0].ToolCalls))
	}
	if len(result.Steps[0].ToolResults) != 2 {
		t.Fatalf("expected 2 tool results in step 0, got %d", len(result.Steps[0].ToolResults))
	}
	if len(result.Steps[0].Durations) != 2 {
		t.Fatalf("expected 2 durations in step 0, got %d", len(result.Steps[0].Durations))
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
}
