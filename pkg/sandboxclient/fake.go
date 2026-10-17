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
	"sync"
)

// FakeClient is a test fake implementing Client and Factory.
type FakeClient struct {
	mu         sync.Mutex
	CallCount  int
	Calls      []ProcessConfig
	ExecFn     func(call int, ctx context.Context, cfg ProcessConfig) (*ExecuteResult, error)
	CloseFn    func() error
	CloseCalls int

	// Step-based convenience defaults
	ListStdout string
	ListStderr string
	ListErr    error

	CloneErr    error
	CloneExit   int
	CloneStderr string

	CheckoutErr    error
	CheckoutExit   int
	CheckoutStderr string

	ReadFileCalls []string
	ReadFileFn    func(ctx context.Context, path string) ([]byte, error)

	LastPodIP     string
	LastAuthToken string
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
	f.mu.Unlock()
	return f, nil
}

// ReadFile logs the call and delegates to ReadFileFn if set.
func (f *FakeClient) ReadFile(ctx context.Context, path string) ([]byte, error) {
	f.mu.Lock()
	f.ReadFileCalls = append(f.ReadFileCalls, path)
	fn := f.ReadFileFn
	f.mu.Unlock()

	if fn != nil {
		return fn(ctx, path)
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
	f.mu.Unlock()

	if execFn != nil {
		return execFn(call, ctx, cfg)
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
			return nil, f.CloneErr
		}
		return &ExecuteResult{ExitCode: f.CloneExit, Stderr: f.CloneStderr}, nil
	}

	if cmd0 == "git" && len(cfg.Command) > 1 && cfg.Command[1] == "checkout" {
		if f.CheckoutErr != nil {
			return nil, f.CheckoutErr
		}
		return &ExecuteResult{ExitCode: f.CheckoutExit, Stderr: f.CheckoutStderr}, nil
	}

	if cmd0 == "./.evals/list" {
		if f.ListErr != nil {
			return nil, f.ListErr
		}
		return &ExecuteResult{ExitCode: 0, Stdout: f.ListStdout, Stderr: f.ListStderr}, nil
	}

	return &ExecuteResult{ExitCode: 0}, nil
}
