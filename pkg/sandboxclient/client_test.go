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
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gke-labs/eval-runner/pkg/discovery"
	"google.golang.org/grpc"
	processv1 "sigs.k8s.io/agent-sandbox/packages/sandboxd/spec/process/v1"
)

type mockProcessServer struct {
	processv1.UnimplementedProcessServiceServer
	execResp *processv1.ExecuteResponse
	execErr  error
	lastReq  *processv1.ExecuteRequest
}

func (m *mockProcessServer) Execute(ctx context.Context, req *processv1.ExecuteRequest) (*processv1.ExecuteResponse, error) {
	m.lastReq = req
	if m.execErr != nil {
		return nil, m.execErr
	}
	return m.execResp, nil
}

func TestSandboxClient_ExecuteAndLimits(t *testing.T) {
	grpcLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer grpcLis.Close()

	mockSrv := &mockProcessServer{
		execResp: &processv1.ExecuteResponse{
			ExitCode: 0,
			Stdout:   []byte("hello world"),
			Stderr:   []byte("some warning"),
		},
	}
	grpcServer := grpc.NewServer()
	processv1.RegisterProcessServiceServer(grpcServer, mockSrv)
	go func() {
		_ = grpcServer.Serve(grpcLis)
	}()
	defer grpcServer.Stop()

	_, portStr, err := net.SplitHostPort(grpcLis.Addr().String())
	if err != nil {
		t.Fatalf("failed to split host port: %v", err)
	}
	grpcPort, _ := strconv.Atoi(portStr)

	client, err := New("127.0.0.1", WithGRPCPort(grpcPort))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer client.Close()

	res, err := client.Execute(t.Context(), ProcessConfig{
		Command: []string{"echo", "hi"},
		Cwd:     "repo",
	})
	if err != nil {
		t.Fatalf("Execute unexpected error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", res.ExitCode)
	}
	if res.Stdout != "hello world" {
		t.Errorf("expected stdout 'hello world', got %q", res.Stdout)
	}
	if res.Stderr != "some warning" {
		t.Errorf("expected stderr 'some warning', got %q", res.Stderr)
	}
	if mockSrv.lastReq == nil || mockSrv.lastReq.Config.GetCwd() != "repo" {
		t.Errorf("expected Cwd 'repo', got: %+v", mockSrv.lastReq)
	}

	// Test stdout limit
	clientWithStdoutLimit, err := New("127.0.0.1", WithGRPCPort(grpcPort), WithStdoutLimit(5))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer clientWithStdoutLimit.Close()

	res, err = clientWithStdoutLimit.Execute(t.Context(), ProcessConfig{Command: []string{"test"}})
	if err == nil {
		t.Fatalf("expected stdout limit error, got nil")
	}
	var limitErr *discovery.LimitError
	if !errors.As(err, &limitErr) || limitErr.Stream != "stdout" {
		t.Fatalf("expected stdout LimitError, got: %v", err)
	}
	if res.Stdout != "hello" {
		t.Errorf("expected truncated stdout 'hello', got %q", res.Stdout)
	}

	// Test stderr limit
	clientWithStderrLimit, err := New("127.0.0.1", WithGRPCPort(grpcPort), WithStderrLimit(4))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer clientWithStderrLimit.Close()

	res, err = clientWithStderrLimit.Execute(t.Context(), ProcessConfig{Command: []string{"test"}})
	if err == nil {
		t.Fatalf("expected stderr limit error, got nil")
	}
	if !errors.As(err, &limitErr) || limitErr.Stream != "stderr" {
		t.Fatalf("expected stderr LimitError, got: %v", err)
	}
	if res.Stderr != "some" {
		t.Errorf("expected truncated stderr 'some', got %q", res.Stderr)
	}
}

func TestSandboxClient_ReadFile(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/files/repo/output.json" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result": "ok"}`))
			return
		}
		if r.URL.Path == "/v1/files/missing.txt" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code": "NOT_FOUND"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`server error`))
	}))
	defer httpServer.Close()

	_, portStr, err := net.SplitHostPort(httpServer.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to split host port: %v", err)
	}
	httpPort, _ := strconv.Atoi(portStr)

	client, err := New("127.0.0.1", WithRESTPort(httpPort), WithHTTPClient(httpServer.Client()))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer client.Close()

	// Read existing file
	data, err := client.ReadFile(t.Context(), "repo/output.json")
	if err != nil {
		t.Fatalf("unexpected error reading file: %v", err)
	}
	if string(data) != `{"result": "ok"}` {
		t.Errorf("expected file content '{\"result\": \"ok\"}', got %q", string(data))
	}

	// Read missing file
	_, err = client.ReadFile(t.Context(), "missing.txt")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 error, got: %v", err)
	}

	// Read failing file
	_, err = client.ReadFile(t.Context(), "server-error.txt")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 error, got: %v", err)
	}

	// Test read file limit
	clientWithFileLimit, err := New("127.0.0.1", WithRESTPort(httpPort), WithHTTPClient(httpServer.Client()), WithReadFileLimit(5))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer clientWithFileLimit.Close()

	_, err = clientWithFileLimit.ReadFile(t.Context(), "repo/output.json")
	if err == nil {
		t.Fatalf("expected LimitError for file read over limit, got nil")
	}
	var limitErr *discovery.LimitError
	if !errors.As(err, &limitErr) || limitErr.Stream != "file" {
		t.Errorf("expected LimitError for file stream, got: %v", err)
	}
}

func TestSandboxClient_Validation(t *testing.T) {
	_, err := New("")
	if err == nil {
		t.Errorf("expected error when podIP is empty")
	}
}
