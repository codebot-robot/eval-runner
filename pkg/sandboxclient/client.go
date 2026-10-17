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
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gke-labs/eval-runner/pkg/discovery"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	processv1 "sigs.k8s.io/agent-sandbox/packages/sandboxd/spec/process/v1"
)

const (
	// DefaultGRPCPort is the default port for sandboxd gRPC ProcessService.
	DefaultGRPCPort = 9090

	// DefaultRESTPort is the default port for sandboxd REST FilesystemService.
	DefaultRESTPort = 8080

	// DefaultHTTPTimeout is the default timeout for REST HTTP operations.
	DefaultHTTPTimeout = 30 * time.Second

	// DefaultReadFileLimit is the default maximum size for files read via REST (10 MiB).
	DefaultReadFileLimit = 10 * 1024 * 1024
)

// ProcessConfig configures a process execution in the sandbox.
type ProcessConfig struct {
	Command []string
	Cwd     string
	Env     map[string]string
}

// ExecuteResult holds the outcome of a process execution.
type ExecuteResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Client interacts with a sandbox pod running sandboxd.
type Client interface {
	// Execute synchronously runs a command in the sandbox.
	Execute(ctx context.Context, cfg ProcessConfig) (*ExecuteResult, error)

	// ReadFile retrieves a file from the sandbox filesystem via REST.
	ReadFile(ctx context.Context, path string) ([]byte, error)

	// Close releases any underlying network connections.
	Close() error
}

// Factory creates Client instances for sandbox pod IPs.
type Factory interface {
	NewClient(podIP string) (Client, error)
}

// FactoryFunc adapts a function into a Factory.
type FactoryFunc func(podIP string) (Client, error)

// NewClient implements Factory.
func (f FactoryFunc) NewClient(podIP string) (Client, error) {
	return f(podIP)
}

// ClientOptions configures a Client instance.
type ClientOptions struct {
	GRPCPort      int
	RESTPort      int
	StdoutLimit   int64
	StderrLimit   int64
	ReadFileLimit int64
	HTTPClient    *http.Client
}

// Option configures ClientOptions.
type Option func(*ClientOptions)

// WithGRPCPort overrides the gRPC port.
func WithGRPCPort(port int) Option {
	return func(o *ClientOptions) {
		o.GRPCPort = port
	}
}

// WithRESTPort overrides the REST port.
func WithRESTPort(port int) Option {
	return func(o *ClientOptions) {
		o.RESTPort = port
	}
}

// WithStdoutLimit overrides the stdout limit.
func WithStdoutLimit(limit int64) Option {
	return func(o *ClientOptions) {
		o.StdoutLimit = limit
	}
}

// WithStderrLimit overrides the stderr limit.
func WithStderrLimit(limit int64) Option {
	return func(o *ClientOptions) {
		o.StderrLimit = limit
	}
}

// WithReadFileLimit overrides the file read limit.
func WithReadFileLimit(limit int64) Option {
	return func(o *ClientOptions) {
		o.ReadFileLimit = limit
	}
}

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(o *ClientOptions) {
		o.HTTPClient = client
	}
}

type sandboxdClient struct {
	grpcConn      *grpc.ClientConn
	processClient processv1.ProcessServiceClient
	httpClient    *http.Client
	restBaseURL   string
	stdoutLimit   int64
	stderrLimit   int64
	readFileLimit int64
}

// DefaultFactory returns a Factory that constructs sandboxd clients with the provided default options.
func DefaultFactory(opts ...Option) Factory {
	return FactoryFunc(func(podIP string) (Client, error) {
		return New(podIP, opts...)
	})
}

// New creates a new Client connected to sandboxd on the given pod IP.
func New(podIP string, opts ...Option) (Client, error) {
	if podIP == "" {
		return nil, fmt.Errorf("pod IP is required")
	}

	options := ClientOptions{
		GRPCPort:      DefaultGRPCPort,
		RESTPort:      DefaultRESTPort,
		StdoutLimit:   discovery.MaxStdoutBytes,
		StderrLimit:   discovery.MaxStderrBytes,
		ReadFileLimit: DefaultReadFileLimit,
	}
	for _, opt := range opts {
		opt(&options)
	}

	grpcTarget := net.JoinHostPort(podIP, fmt.Sprintf("%d", options.GRPCPort))
	conn, err := grpc.NewClient(
		grpcTarget,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC client for %s: %w", grpcTarget, err)
	}

	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: DefaultHTTPTimeout,
		}
	}

	restBaseURL := fmt.Sprintf("http://%s", net.JoinHostPort(podIP, fmt.Sprintf("%d", options.RESTPort)))

	return &sandboxdClient{
		grpcConn:      conn,
		processClient: processv1.NewProcessServiceClient(conn),
		httpClient:    httpClient,
		restBaseURL:   restBaseURL,
		stdoutLimit:   options.StdoutLimit,
		stderrLimit:   options.StderrLimit,
		readFileLimit: options.ReadFileLimit,
	}, nil
}

func (c *sandboxdClient) Execute(ctx context.Context, cfg ProcessConfig) (*ExecuteResult, error) {
	req := &processv1.ExecuteRequest{
		Config: &processv1.ProcessConfig{
			Command: cfg.Command,
			EnvVars: cfg.Env,
		},
	}
	if cfg.Cwd != "" {
		req.Config.Cwd = &cfg.Cwd
	}

	resp, err := c.processClient.Execute(ctx, req)
	if err != nil {
		return nil, err
	}

	stdoutLimit := c.stdoutLimit
	if stdoutLimit <= 0 {
		stdoutLimit = discovery.MaxStdoutBytes
	}
	stderrLimit := c.stderrLimit
	if stderrLimit <= 0 {
		stderrLimit = discovery.MaxStderrBytes
	}

	stdoutWriter := discovery.NewLimitWriter(stdoutLimit, "stdout")
	stderrWriter := discovery.NewLimitWriter(stderrLimit, "stderr")

	_, stdoutErr := stdoutWriter.Write(resp.GetStdout())
	_, stderrErr := stderrWriter.Write(resp.GetStderr())

	result := &ExecuteResult{
		ExitCode: int(resp.GetExitCode()),
		Stdout:   stdoutWriter.String(),
		Stderr:   stderrWriter.String(),
	}

	if stdoutErr != nil {
		return result, stdoutErr
	}
	if stderrErr != nil {
		return result, stderrErr
	}

	return result, nil
}

func (c *sandboxdClient) ReadFile(ctx context.Context, path string) ([]byte, error) {
	cleanPath := strings.TrimPrefix(path, "/")
	reqURL := fmt.Sprintf("%s/v1/files/%s", c.restBaseURL, cleanPath)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to execute HTTP GET for %s: %w", path, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			_ = closeErr
		}
	}()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("file %q not found (HTTP 404)", path)
	}

	limit := c.readFileLimit
	if limit <= 0 {
		limit = DefaultReadFileLimit
	}

	limitedReader := io.LimitReader(resp.Body, limit+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body for %s: %w", path, err)
	}

	if int64(len(body)) > limit {
		return nil, &discovery.LimitError{
			Stream: "file",
			Limit:  limit,
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP GET %s failed with status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return body, nil
}

func (c *sandboxdClient) Close() error {
	if c.grpcConn != nil {
		return c.grpcConn.Close()
	}
	return nil
}
