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

package sandboxclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// AsyncTaskState describes the lifecycle state of an asynchronous sandbox task.
type AsyncTaskState string

const (
	// AsyncTaskRunning indicates the task process is still running.
	AsyncTaskRunning AsyncTaskState = "running"

	// AsyncTaskCompleted indicates the task has finished executing.
	AsyncTaskCompleted AsyncTaskState = "completed"

	// AsyncTaskNotFound indicates no state exists for the requested execution ID.
	AsyncTaskNotFound AsyncTaskState = "not_found"
)

// AsyncTaskStatus holds the observed state and captured output of an async task.
type AsyncTaskStatus struct {
	State    AsyncTaskState
	ExitCode int
	Stdout   string
	Stderr   string
}

// ErrAsyncTaskAlreadyExists is returned by StartAsync when state already exists for executionID.
var ErrAsyncTaskAlreadyExists = errors.New("async task already exists")

const (
	defaultWorkspaceRoot           = "/workspace"
	asyncTasksRelativeDirectory    = "out/.sandbox-tasks"
	asyncTaskAlreadyExistsExitCode = 17
	killedProcessExitCode          = 137
	maxExecutionIDLength           = 255
)

var validExecutionIDPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

const asyncWrapperScript = `trap '' HUP
__sandbox_task_dir="$1"
shift
"$@"
__sandbox_exit_code=$?
printf '%s\n' "$__sandbox_exit_code" > "$__sandbox_task_dir/exit_code.tmp"
mv "$__sandbox_task_dir/exit_code.tmp" "$__sandbox_task_dir/exit_code"
`

const asyncStartScript = `set -eu
__sandbox_task_dir="$1"
__sandbox_wrapper="$2"
shift 2
__sandbox_tasks_root="${__sandbox_task_dir%/*}"
mkdir -p "$__sandbox_tasks_root"
if ! mkdir "$__sandbox_task_dir" 2>/dev/null; then
	if [ -e "$__sandbox_task_dir" ]; then
		echo "async task already exists" >&2
		exit 17
	fi
	mkdir "$__sandbox_task_dir"
fi
: > "$__sandbox_task_dir/stdout"
: > "$__sandbox_task_dir/stderr"
if command -v setsid >/dev/null 2>&1; then
	setsid /bin/sh -c "$__sandbox_wrapper" _ "$__sandbox_task_dir" "$@" </dev/null >"$__sandbox_task_dir/stdout" 2>"$__sandbox_task_dir/stderr" &
elif command -v nohup >/dev/null 2>&1; then
	nohup /bin/sh -c "$__sandbox_wrapper" _ "$__sandbox_task_dir" "$@" </dev/null >"$__sandbox_task_dir/stdout" 2>"$__sandbox_task_dir/stderr" &
else
	/bin/sh -c "$__sandbox_wrapper" _ "$__sandbox_task_dir" "$@" </dev/null >"$__sandbox_task_dir/stdout" 2>"$__sandbox_task_dir/stderr" &
fi
__sandbox_pid=$!
printf '%s\n' "$__sandbox_pid" > "$__sandbox_task_dir/pid.tmp"
mv "$__sandbox_task_dir/pid.tmp" "$__sandbox_task_dir/pid"
`

func validateExecutionID(executionID string) error {
	if executionID == "" {
		return fmt.Errorf("executionID is required")
	}
	if len(executionID) > maxExecutionIDLength {
		return fmt.Errorf("executionID %q exceeds maximum length %d", executionID, maxExecutionIDLength)
	}
	if executionID == "." || executionID == ".." || strings.Contains(executionID, "..") {
		return fmt.Errorf("invalid executionID %q: path traversal is not allowed", executionID)
	}
	if !validExecutionIDPattern.MatchString(executionID) {
		return fmt.Errorf("invalid executionID %q: must match %s", executionID, validExecutionIDPattern.String())
	}
	return nil
}

func (c *sandboxdClient) asyncTaskDirectory(executionID string) string {
	workspaceRoot := c.workspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = defaultWorkspaceRoot
	}
	return path.Join(workspaceRoot, asyncTasksRelativeDirectory, executionID)
}

func asyncTaskReadPath(executionID, fileName string) string {
	return path.Join(asyncTasksRelativeDirectory, executionID, fileName)
}

func (c *sandboxdClient) StartAsync(ctx context.Context, executionID string, cfg ProcessConfig) error {
	if err := validateExecutionID(executionID); err != nil {
		return err
	}
	if len(cfg.Command) == 0 {
		return fmt.Errorf("command is required")
	}

	taskDirectory := c.asyncTaskDirectory(executionID)
	command := make([]string, 0, 6+len(cfg.Command))
	command = append(command, "/bin/sh", "-c", asyncStartScript, "_", taskDirectory, asyncWrapperScript)
	command = append(command, cfg.Command...)

	result, err := c.Execute(ctx, ProcessConfig{
		Command: command,
		Cwd:     cfg.Cwd,
		Env:     cfg.Env,
	})
	if err != nil {
		return fmt.Errorf("failed to start async task %q: %w", executionID, err)
	}
	if result.ExitCode == asyncTaskAlreadyExistsExitCode {
		return fmt.Errorf("async task %q already exists: %w", executionID, ErrAsyncTaskAlreadyExists)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to start async task %q (exit code %d): %s", executionID, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func (c *sandboxdClient) PollAsync(ctx context.Context, executionID string) (*AsyncTaskStatus, error) {
	if err := validateExecutionID(executionID); err != nil {
		return nil, err
	}

	exitCode, found, err := c.readExitCode(ctx, executionID)
	if err != nil {
		return nil, err
	}
	if found {
		return c.readCompletedTaskStatus(ctx, executionID, exitCode)
	}

	processIDBytes, err := c.ReadFile(ctx, asyncTaskReadPath(executionID, "pid"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &AsyncTaskStatus{State: AsyncTaskNotFound}, nil
		}
		return nil, fmt.Errorf("failed to read pid for async task %q: %w", executionID, err)
	}

	rawProcessID := strings.TrimSpace(string(processIDBytes))
	processID, err := strconv.Atoi(rawProcessID)
	if err != nil || processID <= 0 {
		return nil, fmt.Errorf("invalid pid %q for async task %q", rawProcessID, executionID)
	}

	alive, err := c.isProcessAlive(ctx, processID)
	if err != nil {
		return nil, fmt.Errorf("failed to check process %d for async task %q: %w", processID, executionID, err)
	}
	if alive {
		return &AsyncTaskStatus{State: AsyncTaskRunning}, nil
	}

	// Re-check exit_code once to avoid a race where the task finished between
	// our initial exit_code read and the liveness check.
	exitCode, found, err = c.readExitCode(ctx, executionID)
	if err != nil {
		return nil, err
	}
	if found {
		return c.readCompletedTaskStatus(ctx, executionID, exitCode)
	}

	return c.readCompletedTaskStatus(ctx, executionID, killedProcessExitCode)
}

func (c *sandboxdClient) readExitCode(ctx context.Context, executionID string) (int, bool, error) {
	exitCodeBytes, err := c.ReadFile(ctx, asyncTaskReadPath(executionID, "exit_code"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("failed to read exit_code for async task %q: %w", executionID, err)
	}

	rawExitCode := strings.TrimSpace(string(exitCodeBytes))
	exitCode, err := strconv.Atoi(rawExitCode)
	if err != nil {
		return 0, false, fmt.Errorf("invalid exit_code %q for async task %q: %w", rawExitCode, executionID, err)
	}
	return exitCode, true, nil
}

const asyncProcessAliveScript = `kill -0 "$1" 2>/dev/null || exit 1
if [ -r "/proc/$1/status" ]; then
	while IFS= read -r __sandbox_line; do
		case "$__sandbox_line" in
			State:*)
				case "$__sandbox_line" in
					*Z*|*X*) exit 1 ;;
				esac
				exit 0
				;;
		esac
	done < "/proc/$1/status" 2>/dev/null || exit 1
fi
`

func (c *sandboxdClient) isProcessAlive(ctx context.Context, processID int) (bool, error) {
	result, err := c.Execute(ctx, ProcessConfig{
		Command: []string{"/bin/sh", "-c", asyncProcessAliveScript, "_", strconv.Itoa(processID)},
	})
	if err != nil {
		return false, err
	}
	return result.ExitCode == 0, nil
}

func (c *sandboxdClient) readCompletedTaskStatus(ctx context.Context, executionID string, exitCode int) (*AsyncTaskStatus, error) {
	stdout, err := c.readTruncatedOutput(ctx, asyncTaskReadPath(executionID, "stdout"), c.effectiveStdoutLimit())
	if err != nil {
		return nil, fmt.Errorf("failed to read stdout for async task %q: %w", executionID, err)
	}
	stderr, err := c.readTruncatedOutput(ctx, asyncTaskReadPath(executionID, "stderr"), c.effectiveStderrLimit())
	if err != nil {
		return nil, fmt.Errorf("failed to read stderr for async task %q: %w", executionID, err)
	}
	return &AsyncTaskStatus{
		State:    AsyncTaskCompleted,
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}

func (c *sandboxdClient) readTruncatedOutput(ctx context.Context, filePath string, limit int64) (string, error) {
	data, err := c.readFile(ctx, filePath, limit, true)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}
