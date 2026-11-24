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
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	processv1 "sigs.k8s.io/agent-sandbox/packages/sandboxd/spec/process/v1"
)

type localProcessServer struct {
	processv1.UnimplementedProcessServiceServer
	workspaceRoot string
}

func (s *localProcessServer) Execute(ctx context.Context, req *processv1.ExecuteRequest) (*processv1.ExecuteResponse, error) {
	command := req.GetConfig().GetCommand()
	process := exec.CommandContext(ctx, command[0], command[1:]...)

	workingDirectory := req.GetConfig().GetCwd()
	if workingDirectory == "" {
		process.Dir = s.workspaceRoot
	} else if filepath.IsAbs(workingDirectory) {
		process.Dir = workingDirectory
	} else {
		process.Dir = filepath.Join(s.workspaceRoot, workingDirectory)
	}

	if environmentVariables := req.GetConfig().GetEnvVars(); len(environmentVariables) > 0 {
		environment := append([]string(nil), os.Environ()...)
		for key, value := range environmentVariables {
			environment = append(environment, key+"="+value)
		}
		process.Env = environment
	}

	var stdoutBuffer bytes.Buffer
	var stderrBuffer bytes.Buffer
	process.Stdout = &stdoutBuffer
	process.Stderr = &stderrBuffer

	err := process.Run()
	exitCode := int32(0)
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = int32(exitError.ExitCode())
		} else {
			return nil, err
		}
	}

	return &processv1.ExecuteResponse{
		ExitCode: exitCode,
		Stdout:   stdoutBuffer.Bytes(),
		Stderr:   stderrBuffer.Bytes(),
	}, nil
}

func newLocalAsyncSandboxClient(t *testing.T, opts ...Option) (*sandboxdClient, string) {
	t.Helper()

	workspaceRoot := t.TempDir()

	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen for gRPC: %v", err)
	}
	grpcServer := grpc.NewServer()
	processv1.RegisterProcessServiceServer(grpcServer, &localProcessServer{workspaceRoot: workspaceRoot})
	go func() {
		_ = grpcServer.Serve(grpcListener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = grpcListener.Close()
	})

	_, grpcPortString, err := net.SplitHostPort(grpcListener.Addr().String())
	if err != nil {
		t.Fatalf("failed to split gRPC host/port: %v", err)
	}
	grpcPort, err := strconv.Atoi(grpcPortString)
	if err != nil {
		t.Fatalf("failed to parse gRPC port: %v", err)
	}

	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/v1/files/") {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		relativePath := strings.TrimPrefix(request.URL.Path, "/v1/files/")
		fullPath := filepath.Join(workspaceRoot, filepath.FromSlash(relativePath))
		data, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(data)
	}))
	t.Cleanup(httpServer.Close)

	_, restPortString, err := net.SplitHostPort(httpServer.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to split REST host/port: %v", err)
	}
	restPort, err := strconv.Atoi(restPortString)
	if err != nil {
		t.Fatalf("failed to parse REST port: %v", err)
	}

	clientOptions := append([]Option{
		WithGRPCPort(grpcPort),
		WithRESTPort(restPort),
		WithHTTPClient(httpServer.Client()),
	}, opts...)

	rawClient, err := New("127.0.0.1", clientOptions...)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	t.Cleanup(func() {
		_ = rawClient.Close()
	})

	client := rawClient.(*sandboxdClient)
	client.workspaceRoot = workspaceRoot
	return client, workspaceRoot
}

func waitForAsyncCompletion(t *testing.T, client Client, executionID string) *AsyncTaskStatus {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		taskStatus, err := client.PollAsync(t.Context(), executionID)
		if err != nil {
			t.Fatalf("PollAsync(%q) failed: %v", executionID, err)
		}
		if taskStatus.State == AsyncTaskCompleted {
			return taskStatus
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for async task %q to complete", executionID)
	return nil
}

func TestSandboxClient_AsyncValidation(t *testing.T) {
	client, _ := newLocalAsyncSandboxClient(t)

	invalidIDs := []string{
		"",
		".",
		"..",
		"../escape",
		"a..b",
		"nested/id",
		`nested\id`,
		"with space",
		"semi;colon",
		"$HOME",
		strings.Repeat("a", maxExecutionIDLength+1),
	}

	for _, invalidID := range invalidIDs {
		if err := client.StartAsync(t.Context(), invalidID, ProcessConfig{Command: []string{"true"}}); err == nil {
			t.Errorf("StartAsync(%q) expected validation error, got nil", invalidID)
		}
		if _, err := client.PollAsync(t.Context(), invalidID); err == nil {
			t.Errorf("PollAsync(%q) expected validation error, got nil", invalidID)
		}
	}

	if err := client.StartAsync(t.Context(), "valid-task_1.0", ProcessConfig{}); err == nil {
		t.Errorf("StartAsync with empty command expected error, got nil")
	}
}

func TestSandboxClient_AsyncRunningToCompletedAndDuplicate(t *testing.T) {
	client, workspaceRoot := newLocalAsyncSandboxClient(t)
	gateFile := filepath.Join(workspaceRoot, "release.gate")

	startTime := time.Now()
	err := client.StartAsync(t.Context(), "task-1.attempt_0", ProcessConfig{
		Command: []string{
			"/bin/sh", "-c",
			`while [ ! -f "$1" ]; do sleep 0.02; done; echo "hello stdout"; echo "hello stderr" >&2; exit 7`,
			"_", gateFile,
		},
	})
	if err != nil {
		t.Fatalf("StartAsync failed: %v", err)
	}
	if elapsed := time.Since(startTime); elapsed > 2*time.Second {
		t.Errorf("StartAsync blocked unexpectedly for %v", elapsed)
	}

	// Duplicate executionID should return ErrAsyncTaskAlreadyExists.
	duplicateErr := client.StartAsync(t.Context(), "task-1.attempt_0", ProcessConfig{
		Command: []string{"echo", "duplicate"},
	})
	if !errors.Is(duplicateErr, ErrAsyncTaskAlreadyExists) {
		t.Fatalf("expected ErrAsyncTaskAlreadyExists on duplicate StartAsync, got: %v", duplicateErr)
	}

	runningStatus, err := client.PollAsync(t.Context(), "task-1.attempt_0")
	if err != nil {
		t.Fatalf("PollAsync while running failed: %v", err)
	}
	if runningStatus.State != AsyncTaskRunning {
		t.Fatalf("expected state %q, got %q", AsyncTaskRunning, runningStatus.State)
	}

	if err := os.WriteFile(gateFile, []byte("go"), 0o600); err != nil {
		t.Fatalf("failed to write gate file: %v", err)
	}

	completedStatus := waitForAsyncCompletion(t, client, "task-1.attempt_0")
	if completedStatus.ExitCode != 7 {
		t.Errorf("expected ExitCode 7, got %d", completedStatus.ExitCode)
	}
	if completedStatus.Stdout != "hello stdout\n" {
		t.Errorf("expected Stdout %q, got %q", "hello stdout\n", completedStatus.Stdout)
	}
	if completedStatus.Stderr != "hello stderr\n" {
		t.Errorf("expected Stderr %q, got %q", "hello stderr\n", completedStatus.Stderr)
	}
}

func TestSandboxClient_AsyncNotFound(t *testing.T) {
	client, _ := newLocalAsyncSandboxClient(t)

	taskStatus, err := client.PollAsync(t.Context(), "does-not-exist")
	if err != nil {
		t.Fatalf("PollAsync unexpected error: %v", err)
	}
	if taskStatus.State != AsyncTaskNotFound {
		t.Errorf("expected state %q, got %q", AsyncTaskNotFound, taskStatus.State)
	}
}

func TestSandboxClient_AsyncCwdAndEnvForwarding(t *testing.T) {
	client, workspaceRoot := newLocalAsyncSandboxClient(t)
	repoDirectory := filepath.Join(workspaceRoot, "repo")
	if err := os.MkdirAll(repoDirectory, 0o755); err != nil {
		t.Fatalf("failed to create repo directory: %v", err)
	}

	err := client.StartAsync(t.Context(), "cwd-env-task", ProcessConfig{
		Command: []string{"/bin/sh", "-c", `printf '%s|%s' "$(pwd)" "$CUSTOM_EVAL_VAR"`},
		Cwd:     "repo",
		Env: map[string]string{
			"CUSTOM_EVAL_VAR": "forwarded-value",
		},
	})
	if err != nil {
		t.Fatalf("StartAsync failed: %v", err)
	}

	completedStatus := waitForAsyncCompletion(t, client, "cwd-env-task")
	if completedStatus.ExitCode != 0 {
		t.Fatalf("expected ExitCode 0, got %d (stderr: %q)", completedStatus.ExitCode, completedStatus.Stderr)
	}
	expectedOutput := repoDirectory + "|forwarded-value"
	if completedStatus.Stdout != expectedOutput {
		t.Errorf("expected Stdout %q, got %q", expectedOutput, completedStatus.Stdout)
	}
}

func TestSandboxClient_AsyncOutputTruncation(t *testing.T) {
	client, _ := newLocalAsyncSandboxClient(t,
		WithStdoutLimit(10),
		WithStderrLimit(8),
		WithReadFileLimit(12),
	)

	err := client.StartAsync(t.Context(), "truncation-task", ProcessConfig{
		Command: []string{
			"/bin/sh", "-c",
			`printf '0123456789abcdefghij'; printf 'ABCDEFGHIJKLMNOP' >&2`,
		},
	})
	if err != nil {
		t.Fatalf("StartAsync failed: %v", err)
	}

	completedStatus := waitForAsyncCompletion(t, client, "truncation-task")
	if completedStatus.ExitCode != 0 {
		t.Fatalf("expected ExitCode 0, got %d", completedStatus.ExitCode)
	}
	if completedStatus.Stdout != "0123456789" {
		t.Errorf("expected truncated Stdout %q, got %q", "0123456789", completedStatus.Stdout)
	}
	if completedStatus.Stderr != "ABCDEFGH" {
		t.Errorf("expected truncated Stderr %q, got %q", "ABCDEFGH", completedStatus.Stderr)
	}
}

func TestSandboxClient_AsyncDeadProcessWithoutExitCode(t *testing.T) {
	client, workspaceRoot := newLocalAsyncSandboxClient(t)

	err := client.StartAsync(t.Context(), "killed-task", ProcessConfig{
		Command: []string{
			"/bin/sh", "-c",
			`printf 'before kill stdout'; printf 'before kill stderr' >&2; sleep 30`,
		},
	})
	if err != nil {
		t.Fatalf("StartAsync failed: %v", err)
	}

	processIDPath := filepath.Join(workspaceRoot, asyncTasksRelativeDirectory, "killed-task", "pid")
	processIDBytes, err := os.ReadFile(processIDPath)
	if err != nil {
		t.Fatalf("failed to read pid file: %v", err)
	}
	processID, err := strconv.Atoi(strings.TrimSpace(string(processIDBytes)))
	if err != nil {
		t.Fatalf("failed to parse pid %q: %v", string(processIDBytes), err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-processID, syscall.SIGKILL)
	})

	// Wait briefly so the child writes partial output before being killed.
	stdoutPath := filepath.Join(workspaceRoot, asyncTasksRelativeDirectory, "killed-task", "stdout")
	waitDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(waitDeadline) {
		data, _ := os.ReadFile(stdoutPath)
		if string(data) == "before kill stdout" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Kill the detached process group (including wrapper shell) with SIGKILL so exit_code is never written.
	if err := syscall.Kill(-processID, syscall.SIGKILL); err != nil {
		t.Fatalf("failed to SIGKILL process group %d: %v", processID, err)
	}

	completedStatus := waitForAsyncCompletion(t, client, "killed-task")
	if completedStatus.ExitCode != killedProcessExitCode {
		t.Errorf("expected ExitCode %d for killed process, got %d", killedProcessExitCode, completedStatus.ExitCode)
	}
	if completedStatus.Stdout != "before kill stdout" {
		t.Errorf("expected Stdout %q, got %q", "before kill stdout", completedStatus.Stdout)
	}
	if completedStatus.Stderr != "before kill stderr" {
		t.Errorf("expected Stderr %q, got %q", "before kill stderr", completedStatus.Stderr)
	}
}

func TestSandboxClient_AsyncDeadProcessExitCodeRace(t *testing.T) {
	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer grpcListener.Close()

	mockServer := &mockProcessServer{
		execResp: &processv1.ExecuteResponse{ExitCode: 1}, // kill -0 reports process dead
	}
	grpcServer := grpc.NewServer()
	processv1.RegisterProcessServiceServer(grpcServer, mockServer)
	go func() {
		_ = grpcServer.Serve(grpcListener)
	}()
	defer grpcServer.Stop()

	_, grpcPortString, _ := net.SplitHostPort(grpcListener.Addr().String())
	grpcPort, _ := strconv.Atoi(grpcPortString)

	var exitCodeReads atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/files/out/.sandbox-tasks/race-task/exit_code":
			// First read misses exit_code; second read (after kill -0 fails) finds it.
			if exitCodeReads.Add(1) == 1 {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte("42\n"))
		case "/v1/files/out/.sandbox-tasks/race-task/pid":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte("99999\n"))
		case "/v1/files/out/.sandbox-tasks/race-task/stdout":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte("raced stdout"))
		case "/v1/files/out/.sandbox-tasks/race-task/stderr":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte("raced stderr"))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer httpServer.Close()

	_, restPortString, _ := net.SplitHostPort(httpServer.Listener.Addr().String())
	restPort, _ := strconv.Atoi(restPortString)

	client, err := New("127.0.0.1",
		WithGRPCPort(grpcPort),
		WithRESTPort(restPort),
		WithHTTPClient(httpServer.Client()),
	)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer client.Close()

	taskStatus, err := client.PollAsync(t.Context(), "race-task")
	if err != nil {
		t.Fatalf("PollAsync failed: %v", err)
	}
	if taskStatus.State != AsyncTaskCompleted {
		t.Fatalf("expected state %q, got %q", AsyncTaskCompleted, taskStatus.State)
	}
	if taskStatus.ExitCode != 42 {
		t.Errorf("expected ExitCode 42 from re-checked exit_code, got %d", taskStatus.ExitCode)
	}
	if taskStatus.Stdout != "raced stdout" {
		t.Errorf("expected Stdout %q, got %q", "raced stdout", taskStatus.Stdout)
	}
	if taskStatus.Stderr != "raced stderr" {
		t.Errorf("expected Stderr %q, got %q", "raced stderr", taskStatus.Stderr)
	}
	if exitCodeReads.Load() != 2 {
		t.Errorf("expected 2 exit_code reads, got %d", exitCodeReads.Load())
	}
}

func TestFakeClient_AsyncSupport(t *testing.T) {
	fakeClient := NewFakeClient()
	fakeClient.StdoutLimit = 5
	fakeClient.StderrLimit = 4

	if err := fakeClient.StartAsync(t.Context(), "../bad", ProcessConfig{Command: []string{"echo"}}); err == nil {
		t.Errorf("expected validation error for invalid executionID")
	}
	if err := fakeClient.StartAsync(t.Context(), "task-1", ProcessConfig{}); err == nil {
		t.Errorf("expected error for empty command")
	}

	notFoundStatus, err := fakeClient.PollAsync(t.Context(), "missing-task")
	if err != nil {
		t.Fatalf("PollAsync unexpected error: %v", err)
	}
	if notFoundStatus.State != AsyncTaskNotFound {
		t.Errorf("expected %q, got %q", AsyncTaskNotFound, notFoundStatus.State)
	}

	cfg := ProcessConfig{
		Command: []string{"echo", "hi"},
		Cwd:     "repo",
		Env:     map[string]string{"KEY": "VAL"},
	}
	if err := fakeClient.StartAsync(t.Context(), "task-1", cfg); err != nil {
		t.Fatalf("StartAsync failed: %v", err)
	}
	if len(fakeClient.StartAsyncCalls) != 1 || fakeClient.StartAsyncCalls[0].ExecutionID != "task-1" {
		t.Fatalf("unexpected StartAsyncCalls: %+v", fakeClient.StartAsyncCalls)
	}
	if err := fakeClient.StartAsync(t.Context(), "task-1", cfg); !errors.Is(err, ErrAsyncTaskAlreadyExists) {
		t.Fatalf("expected ErrAsyncTaskAlreadyExists, got %v", err)
	}

	runningStatus, err := fakeClient.PollAsync(t.Context(), "task-1")
	if err != nil {
		t.Fatalf("PollAsync failed: %v", err)
	}
	if runningStatus.State != AsyncTaskRunning {
		t.Errorf("expected %q, got %q", AsyncTaskRunning, runningStatus.State)
	}

	fakeClient.SetAsyncTaskStatus("task-1", AsyncTaskStatus{
		State:    AsyncTaskCompleted,
		ExitCode: 3,
		Stdout:   "1234567890",
		Stderr:   "abcdef",
	})
	completedStatus, err := fakeClient.PollAsync(t.Context(), "task-1")
	if err != nil {
		t.Fatalf("PollAsync failed: %v", err)
	}
	if completedStatus.State != AsyncTaskCompleted || completedStatus.ExitCode != 3 {
		t.Errorf("unexpected completed status: %+v", completedStatus)
	}
	if completedStatus.Stdout != "12345" {
		t.Errorf("expected truncated Stdout %q, got %q", "12345", completedStatus.Stdout)
	}
	if completedStatus.Stderr != "abcd" {
		t.Errorf("expected truncated Stderr %q, got %q", "abcd", completedStatus.Stderr)
	}

	fakeClient.StartAsyncFn = func(ctx context.Context, executionID string, cfg ProcessConfig) error {
		return errors.New("custom start error")
	}
	if err := fakeClient.StartAsync(t.Context(), "task-2", cfg); err == nil || err.Error() != "custom start error" {
		t.Errorf("expected custom start error, got: %v", err)
	}

	fakeClient.PollAsyncFn = func(ctx context.Context, executionID string) (*AsyncTaskStatus, error) {
		return &AsyncTaskStatus{State: AsyncTaskCompleted, ExitCode: 99}, nil
	}
	hookStatus, err := fakeClient.PollAsync(t.Context(), "task-2")
	if err != nil || hookStatus.ExitCode != 99 {
		t.Errorf("expected hook status with ExitCode 99, got status=%+v err=%v", hookStatus, err)
	}
}
