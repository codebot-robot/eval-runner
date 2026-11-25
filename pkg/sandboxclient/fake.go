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
	"crypto/tls"
	"fmt"
	"strings"
	"sync"

	"github.com/gke-labs/eval-runner/pkg/discovery"
	"google.golang.org/grpc/keepalive"
)

// AsyncStartCall records the arguments of a StartAsync call on FakeClient.
type AsyncStartCall struct {
	ExecutionID string
	Config      ProcessConfig
}

// FakeClient is a test fake implementing Client and Factory.
type FakeClient struct {
	mu         sync.Mutex
	CallCount  int
	Calls      []ProcessConfig
	ExecFn     func(call int, ctx context.Context, cfg ProcessConfig) (*ExecuteResult, error)
	CloseFn    func() error
	CloseCalls int

	StartAsyncCalls []AsyncStartCall
	StartAsyncFn    func(ctx context.Context, executionID string, cfg ProcessConfig) error
	PollAsyncCalls  []string
	PollAsyncFn     func(ctx context.Context, executionID string) (*AsyncTaskStatus, error)
	asyncTasks      map[string]*AsyncTaskStatus

	// Step-based convenience defaults
	ListExit   int
	ListStdout string
	ListStderr string
	ListErr    error

	CloneErr    error
	CloneExit   int
	CloneStderr string

	CheckoutErr    error
	CheckoutExit   int
	CheckoutStderr string

	InspectErr    error
	InspectExit   int
	InspectStdout string
	InspectStderr string
	InspectLog    []byte
	ManifestBytes []byte

	ReadFileCalls []string
	ReadFileFn    func(ctx context.Context, path string) ([]byte, error)

	LastPodIP     string
	LastAuthToken string
	LastTLSConfig *tls.Config

	StdoutLimit   int64
	StderrLimit   int64
	ReadFileLimit int64

	KeepaliveParams *keepalive.ClientParameters
}

// NewFakeClient returns a new FakeClient.
func NewFakeClient() *FakeClient {
	return &FakeClient{}
}

// NewClient implements Factory by returning this FakeClient.
func (f *FakeClient) NewClient(podIP string, opts ...Option) (Client, error) {
	var o ClientOptions
	for _, opt := range opts {
		opt(&o)
	}
	f.mu.Lock()
	f.LastPodIP = podIP
	f.LastAuthToken = o.AuthToken
	f.LastTLSConfig = o.TLSConfig
	f.KeepaliveParams = o.KeepaliveParams
	if o.StdoutLimit > 0 {
		f.StdoutLimit = o.StdoutLimit
	}
	if o.StderrLimit > 0 {
		f.StderrLimit = o.StderrLimit
	}
	if o.ReadFileLimit > 0 {
		f.ReadFileLimit = o.ReadFileLimit
	}
	f.mu.Unlock()
	return f, nil
}

// ReadFile logs the call and delegates to ReadFileFn if set.
func (f *FakeClient) ReadFile(ctx context.Context, path string) ([]byte, error) {
	f.mu.Lock()
	f.ReadFileCalls = append(f.ReadFileCalls, path)
	fn := f.ReadFileFn
	inspectLog := f.InspectLog
	manifestBytes := f.ManifestBytes
	listStdout := f.ListStdout
	readFileLimit := f.ReadFileLimit
	f.mu.Unlock()

	if fn != nil {
		return fn(ctx, path)
	}
	if strings.HasSuffix(path, "manifest.json") {
		var data []byte
		if manifestBytes != nil {
			data = manifestBytes
		} else if listStdout != "" {
			data = []byte(listStdout)
		}
		if data != nil {
			if readFileLimit > 0 && int64(len(data)) > readFileLimit {
				return nil, &discovery.LimitError{
					Stream: "file",
					Limit:  readFileLimit,
				}
			}
			return data, nil
		}
	}
	if inspectLog != nil {
		if readFileLimit > 0 && int64(len(inspectLog)) > readFileLimit {
			return nil, &discovery.LimitError{
				Stream: "file",
				Limit:  readFileLimit,
			}
		}
		return inspectLog, nil
	}
	return nil, nil
}

// Close increments CloseCalls and delegates to CloseFn if set.
func (f *FakeClient) Close() error {
	f.mu.Lock()
	f.CloseCalls++
	fn := f.CloseFn
	f.mu.Unlock()

	if fn != nil {
		return fn()
	}
	return nil
}

// Execute logs the call and delegates to ExecFn or evaluates step-based defaults.
func (f *FakeClient) Execute(ctx context.Context, cfg ProcessConfig) (*ExecuteResult, error) {
	f.mu.Lock()
	f.CallCount++
	f.Calls = append(f.Calls, cfg)
	call := f.CallCount
	execFn := f.ExecFn
	stdoutLimit := f.StdoutLimit
	if stdoutLimit <= 0 {
		stdoutLimit = discovery.MaxStdoutBytes
	}
	f.mu.Unlock()

	if execFn != nil {
		res, err := execFn(call, ctx, cfg)
		if err != nil && isResourceExhausted(err) {
			return res, &discovery.LimitError{
				Stream: "stdout",
				Limit:  stdoutLimit,
			}
		}
		return res, err
	}

	if len(cfg.Command) == 0 {
		return &ExecuteResult{ExitCode: 0}, nil
	}

	cmd0 := cfg.Command[0]
	if cmd0 == "rm" {
		return &ExecuteResult{ExitCode: 0}, nil
	}

	if cmd0 == "git" && len(cfg.Command) > 1 && cfg.Command[1] == "clone" {
		if f.CloneErr != nil {
			if isResourceExhausted(f.CloneErr) {
				return nil, &discovery.LimitError{
					Stream: "stdout",
					Limit:  stdoutLimit,
				}
			}
			return nil, f.CloneErr
		}
		return &ExecuteResult{ExitCode: f.CloneExit, Stderr: f.CloneStderr}, nil
	}

	if cmd0 == "git" && len(cfg.Command) > 1 && cfg.Command[1] == "checkout" {
		if f.CheckoutErr != nil {
			if isResourceExhausted(f.CheckoutErr) {
				return nil, &discovery.LimitError{
					Stream: "stdout",
					Limit:  stdoutLimit,
				}
			}
			return nil, f.CheckoutErr
		}
		return &ExecuteResult{ExitCode: f.CheckoutExit, Stderr: f.CheckoutStderr}, nil
	}

	if cmd0 == "./.evals/list" || (len(cfg.Command) >= 3 && strings.Contains(cfg.Command[2], "./.evals/list")) {
		if f.ListErr != nil {
			if isResourceExhausted(f.ListErr) {
				return nil, &discovery.LimitError{
					Stream: "stdout",
					Limit:  stdoutLimit,
				}
			}
			return nil, f.ListErr
		}
		return &ExecuteResult{ExitCode: f.ListExit, Stdout: f.ListStdout, Stderr: f.ListStderr}, nil
	}

	if cmd0 == "inspect" {
		if f.InspectErr != nil {
			if isResourceExhausted(f.InspectErr) {
				return nil, &discovery.LimitError{
					Stream: "stdout",
					Limit:  stdoutLimit,
				}
			}
			return nil, f.InspectErr
		}
		return &ExecuteResult{ExitCode: f.InspectExit, Stdout: f.InspectStdout, Stderr: f.InspectStderr}, nil
	}

	if cmd0 == "ls" {
		return &ExecuteResult{ExitCode: 0, Stdout: "sample_log.json\n"}, nil
	}

	return &ExecuteResult{ExitCode: 0}, nil
}

// StartAsync records the call and delegates to StartAsyncFn or marks the task as running in memory.
func (f *FakeClient) StartAsync(ctx context.Context, executionID string, cfg ProcessConfig) error {
	if err := validateExecutionID(executionID); err != nil {
		return err
	}
	if len(cfg.Command) == 0 {
		return fmt.Errorf("command is required")
	}

	f.mu.Lock()
	f.StartAsyncCalls = append(f.StartAsyncCalls, AsyncStartCall{
		ExecutionID: executionID,
		Config:      cfg,
	})
	startFn := f.StartAsyncFn
	if startFn == nil {
		if f.asyncTasks == nil {
			f.asyncTasks = make(map[string]*AsyncTaskStatus)
		}
		if _, exists := f.asyncTasks[executionID]; exists {
			f.mu.Unlock()
			return fmt.Errorf("async task %q already exists: %w", executionID, ErrAsyncTaskAlreadyExists)
		}
		f.asyncTasks[executionID] = &AsyncTaskStatus{State: AsyncTaskRunning}
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	return startFn(ctx, executionID, cfg)
}

// PollAsync records the call and delegates to PollAsyncFn or returns the in-memory task status.
func (f *FakeClient) PollAsync(ctx context.Context, executionID string) (*AsyncTaskStatus, error) {
	if err := validateExecutionID(executionID); err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.PollAsyncCalls = append(f.PollAsyncCalls, executionID)
	pollFn := f.PollAsyncFn
	if pollFn == nil {
		taskStatus, exists := f.asyncTasks[executionID]
		if !exists {
			f.mu.Unlock()
			return &AsyncTaskStatus{State: AsyncTaskNotFound}, nil
		}
		statusCopy := *taskStatus
		stdoutLimit := f.StdoutLimit
		stderrLimit := f.StderrLimit
		f.mu.Unlock()

		if statusCopy.State == AsyncTaskCompleted {
			if stdoutLimit > 0 && int64(len(statusCopy.Stdout)) > stdoutLimit {
				statusCopy.Stdout = statusCopy.Stdout[:stdoutLimit]
			}
			if stderrLimit > 0 && int64(len(statusCopy.Stderr)) > stderrLimit {
				statusCopy.Stderr = statusCopy.Stderr[:stderrLimit]
			}
		}
		return &statusCopy, nil
	}
	f.mu.Unlock()

	return pollFn(ctx, executionID)
}

// SetAsyncTaskStatus sets the in-memory status returned by PollAsync for executionID.
func (f *FakeClient) SetAsyncTaskStatus(executionID string, taskStatus AsyncTaskStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.asyncTasks == nil {
		f.asyncTasks = make(map[string]*AsyncTaskStatus)
	}
	statusCopy := taskStatus
	f.asyncTasks[executionID] = &statusCopy
}
