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
	"bufio"
	"context"
	"fmt"
	"strings"
)

// ScriptedModel is a deterministic Model implementation used for testing.
// Its commands come from a fenced ```scripted-bash block in the fixture instruction.
type ScriptedModel struct{}

// NewScriptedModel constructs a ScriptedModel.
func NewScriptedModel() *ScriptedModel {
	return &ScriptedModel{}
}

// Generate implements the Model interface deterministically with zero token counts.
func (model *ScriptedModel) Generate(ctx context.Context, request Request) (*Response, error) {
	var commands []string
	for _, message := range request.Messages {
		if message.Role == RoleUser {
			commands = parseScriptedCommands(message.Text)
			break
		}
	}

	toolResultCount := 0
	for _, message := range request.Messages {
		if message.Role == RoleTool {
			toolResultCount++
		}
	}

	if toolResultCount < len(commands) {
		return &Response{
			ToolCalls: []ToolCall{
				{
					ID:      fmt.Sprintf("call-%d", toolResultCount+1),
					Command: commands[toolResultCount],
				},
			},
		}, nil
	}

	return &Response{Text: "completed"}, nil
}

func parseScriptedCommands(instruction string) []string {
	var commands []string
	inBlock := false
	scanner := bufio.NewScanner(strings.NewReader(instruction))
	for scanner.Scan() {
		line := scanner.Text()
		if !inBlock {
			if strings.TrimRight(line, " \t\r") == "```scripted-bash" {
				inBlock = true
			}
			continue
		}
		if strings.HasPrefix(line, "```") {
			break
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			commands = append(commands, trimmed)
		}
	}
	return commands
}
