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
	mu              sync.Mutex
	ExecuteHandler  func(ctx context.Context, cfg ProcessConfig) (*ExecuteResult, error)
	ReadFileHandler func(ctx context.Context, path string) ([]byte, error)
	CloseHandler    func() error

	Calls         []ProcessConfig
	ReadFileCalls []string
	CloseCalls    int
}

// NewFakeClient returns a new FakeClient.
func NewFakeClient() *FakeClient {
	return &FakeClient{}
}

// NewClient implements Factory by returning this FakeClient.
func (f *FakeClient) NewClient(podIP string) (Client, error) {
	return f, nil
}

// Execute logs the call and delegates to ExecuteHandler if set.
func (f *FakeClient) Execute(ctx context.Context, cfg ProcessConfig) (*ExecuteResult, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, cfg)
	handler := f.ExecuteHandler
	f.mu.Unlock()

	if handler != nil {
		return handler(ctx, cfg)
	}
	return &ExecuteResult{ExitCode: 0}, nil
}

// ReadFile logs the call and delegates to ReadFileHandler if set.
func (f *FakeClient) ReadFile(ctx context.Context, path string) ([]byte, error) {
	f.mu.Lock()
	f.ReadFileCalls = append(f.ReadFileCalls, path)
	handler := f.ReadFileHandler
	f.mu.Unlock()

	if handler != nil {
		return handler(ctx, path)
	}
	return nil, nil
}

// Close increments CloseCalls and delegates to CloseHandler if set.
func (f *FakeClient) Close() error {
	f.mu.Lock()
	f.CloseCalls++
	handler := f.CloseHandler
	f.mu.Unlock()

	if handler != nil {
		return handler()
	}
	return nil
}

// CallCount returns the number of Execute invocations.
func (f *FakeClient) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}
