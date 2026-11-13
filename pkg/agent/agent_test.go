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
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

type scriptedModel struct {
	turns  []*Response
	errors []error
	calls  int
}

func (model *scriptedModel) Generate(context.Context, Request) (*Response, error) {
	if model.calls >= len(model.turns) {
		return nil, errors.New("unexpected Generate call")
	}
	index := model.calls
	model.calls++
	var err error
	if index < len(model.errors) {
		err = model.errors[index]
	}
	return model.turns[index], err
}

type scriptedExecutor struct {
	results []ExecuteResult
	errors  []error
	onCall  func(int)
	calls   int
}

func (executor *scriptedExecutor) Execute(context.Context, string) (ExecuteResult, error) {
	if executor.onCall != nil {
		executor.onCall(executor.calls)
	}
	index := executor.calls
	executor.calls++
	var result ExecuteResult
	var err error
	if index < len(executor.results) {
		result = executor.results[index]
	}
	if index < len(executor.errors) {
		err = executor.errors[index]
	}
	return result, err
}

func TestRun(t *testing.T) {
	cases := []struct {
		name           string
		options        Options
		turns          []*Response
		turnErrors     []error
		executeResults []ExecuteResult
		executeErrors  []error
		setup          func(context.Context) (context.Context, func(int))
		wantReason     StopReason
		wantErr        bool
		wantTokens     [2]int
		wantSteps      []Step
	}{
		{
			name: "completes after two steps",
			turns: []*Response{
				{ToolCalls: []ToolCall{{ID: "c1", Command: "echo 1"}}, InputTokens: 10, OutputTokens: 2},
				{Text: "done", InputTokens: 12, OutputTokens: 3},
			},
			executeResults: []ExecuteResult{{Stdout: "1\n"}},
			wantReason:     StopReasonCompleted,
			wantTokens:     [2]int{22, 5},
			wantSteps: []Step{
				{ToolCalls: []ToolCall{{ID: "c1", Command: "echo 1"}}, ToolResults: []ToolResult{{ID: "c1", Command: "echo 1", Output: "1\n"}}, InputTokens: 10, OutputTokens: 2},
				{Text: "done", InputTokens: 12, OutputTokens: 3},
			},
		},
		{
			name:           "failing command and command timeout are fed back",
			turns:          []*Response{{ToolCalls: []ToolCall{{ID: "c1", Command: "cat missing"}, {ID: "c2", Command: "sleep 10"}}}, {Text: "recovered"}},
			executeResults: []ExecuteResult{{ExitCode: 1, Stderr: "no such file\n"}, {}},
			executeErrors:  []error{nil, context.DeadlineExceeded},
			wantReason:     StopReasonCompleted,
			wantSteps: []Step{
				{
					ToolCalls: []ToolCall{{ID: "c1", Command: "cat missing"}, {ID: "c2", Command: "sleep 10"}},
					ToolResults: []ToolResult{
						{ID: "c1", Command: "cat missing", Output: "no such file\n", ExitCode: 1},
						{ID: "c2", Command: "sleep 10", Output: "context deadline exceeded", ExitCode: 1},
					},
				},
				{Text: "recovered"},
			},
		},
		{
			name:           "max steps",
			options:        Options{MaxSteps: 2},
			turns:          []*Response{{ToolCalls: []ToolCall{{ID: "c1", Command: "echo 1"}}}, {ToolCalls: []ToolCall{{ID: "c2", Command: "echo 2"}}}},
			executeResults: []ExecuteResult{{Stdout: "1\n"}, {Stdout: "2\n"}},
			wantReason:     StopReasonMaxSteps,
			wantSteps: []Step{
				{ToolCalls: []ToolCall{{ID: "c1", Command: "echo 1"}}, ToolResults: []ToolResult{{ID: "c1", Command: "echo 1", Output: "1\n"}}},
				{ToolCalls: []ToolCall{{ID: "c2", Command: "echo 2"}}, ToolResults: []ToolResult{{ID: "c2", Command: "echo 2", Output: "2\n"}}},
			},
		},
		{
			name:           "model error returns the partial result",
			turns:          []*Response{{ToolCalls: []ToolCall{{ID: "c1", Command: "echo 1"}}, InputTokens: 5, OutputTokens: 1}, nil},
			turnErrors:     []error{nil, errors.New("upstream model failure")},
			executeResults: []ExecuteResult{{Stdout: "1\n"}},
			wantReason:     StopReasonModelError,
			wantErr:        true,
			wantTokens:     [2]int{5, 1},
			wantSteps: []Step{
				{ToolCalls: []ToolCall{{ID: "c1", Command: "echo 1"}}, ToolResults: []ToolResult{{ID: "c1", Command: "echo 1", Output: "1\n"}}, InputTokens: 5, OutputTokens: 1},
			},
		},
		{
			name: "context cancelled mid-step keeps the partial step",
			setup: func(ctx context.Context) (context.Context, func(int)) {
				cancelledContext, cancel := context.WithCancel(ctx)
				return cancelledContext, func(call int) {
					if call == 1 {
						cancel()
					}
				}
			},
			turns:          []*Response{{Text: "step", ToolCalls: []ToolCall{{ID: "c1", Command: "echo 1"}, {ID: "c2", Command: "echo 2"}}, InputTokens: 7, OutputTokens: 3}},
			executeResults: []ExecuteResult{{Stdout: "1\n"}, {}},
			executeErrors:  []error{nil, context.Canceled},
			wantReason:     StopReasonContextDone,
			wantErr:        true,
			wantTokens:     [2]int{7, 3},
			wantSteps: []Step{
				{
					Text:        "step",
					ToolCalls:   []ToolCall{{ID: "c1", Command: "echo 1"}, {ID: "c2", Command: "echo 2"}},
					ToolResults: []ToolResult{{ID: "c1", Command: "echo 1", Output: "1\n"}},
					InputTokens: 7, OutputTokens: 3,
				},
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := t.Context()
			var onCall func(int)
			if testCase.setup != nil {
				ctx, onCall = testCase.setup(ctx)
			}
			result, err := Run(ctx, &scriptedModel{turns: testCase.turns, errors: testCase.turnErrors}, &scriptedExecutor{results: testCase.executeResults, errors: testCase.executeErrors, onCall: onCall}, testCase.options)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("Run() error = %v, wantErr %v", err, testCase.wantErr)
			}
			if result.StopReason != testCase.wantReason || [2]int{result.InputTokens, result.OutputTokens} != testCase.wantTokens {
				t.Errorf("got reason %q, tokens %v; want %q, %v", result.StopReason, [2]int{result.InputTokens, result.OutputTokens}, testCase.wantReason, testCase.wantTokens)
			}
			for i := range result.Steps {
				result.Steps[i].ModelDurationMilliseconds = 0
				for j := range result.Steps[i].ToolResults {
					result.Steps[i].ToolResults[j].DurationMilliseconds = 0
				}
			}
			if !reflect.DeepEqual(result.Steps, testCase.wantSteps) {
				t.Errorf("steps mismatch:\ngot  %+v\nwant %+v", result.Steps, testCase.wantSteps)
			}
		})
	}
}

func TestTruncateOutput(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		maxBytes int
		want     string
	}{
		{"ASCII", "0123456789", 6, "012[... 4 bytes omitted ...]789"},
		{"OddSize", "12345678", 7, "123[... 1 bytes omitted ...]5678"},
		{"MultiByteHead", "ééééééééééé", 7, "é[... 16 bytes omitted ...]éé"},
		{"MultiByteTail", "ab" + strings.Repeat("é", 10), 9, "abé[... 14 bytes omitted ...]éé"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := truncateOutput(testCase.input, testCase.maxBytes); got != testCase.want || !utf8.ValidString(got) {
				t.Fatalf("truncateOutput(%q, %d) = %q, want %q", testCase.input, testCase.maxBytes, got, testCase.want)
			}
		})
	}
}
