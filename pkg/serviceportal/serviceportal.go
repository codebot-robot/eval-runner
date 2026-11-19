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

// Package serviceportal provides a client for binding egress policies and injecting secrets
// for sandbox pods via the Service Portal Controller.
package serviceportal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"k8s.io/apimachinery/pkg/types"

	portalsv1 "github.com/gke-labs/service-portals/pkg/portals/v1"
)

// Options configures the Service Portal client.
type Options struct {
	ControllerAddress string
	CAFile            string
	TokenFile         string
}

// Validate reports an error if any required option is missing.
func (options Options) Validate() error {
	if options.ControllerAddress == "" {
		return errors.New("service portal controller address is required")
	}
	if options.CAFile == "" {
		return errors.New("service portal CA file is required when controller address is set")
	}
	if options.TokenFile == "" {
		return errors.New("service portal token file is required when controller address is set")
	}
	return nil
}

// StatusError reports a pending or refused acceptance status from the Service Portal Controller.
type StatusError struct {
	Operation      string
	Status         portalsv1.AcceptanceStatus
	Reason         string
	MissingSecrets []string
}

func (e *StatusError) Error() string {
	var parts []string
	if e.Reason != "" {
		parts = append(parts, e.Reason)
	}
	if len(e.MissingSecrets) > 0 {
		parts = append(parts, "missing secrets: "+strings.Join(e.MissingSecrets, ", "))
	}
	statusString := "unknown"
	switch e.Status {
	case portalsv1.AcceptanceStatus_ACCEPTANCE_STATUS_PENDING:
		statusString = "pending"
	case portalsv1.AcceptanceStatus_ACCEPTANCE_STATUS_REFUSED:
		statusString = "refused"
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s %s", e.Operation, statusString)
	}
	return fmt.Sprintf("%s %s: %s", e.Operation, statusString, strings.Join(parts, ", "))
}

// Client is a gRPC client that communicates with the Service Portal Controller.
type Client struct {
	connection *grpc.ClientConn
	client     portalsv1.PodBindServiceClient
	tokenFile  string
}

// New creates a new Service Portal Client configured with options.
func New(options Options, dialOptions ...grpc.DialOption) (*Client, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	transportCredentials, err := credentials.NewClientTLSFromFile(options.CAFile, "")
	if err != nil {
		return nil, fmt.Errorf("loading CA cert from %s: %w", options.CAFile, err)
	}
	allDialOptions := append([]grpc.DialOption{grpc.WithTransportCredentials(transportCredentials)}, dialOptions...)
	connection, err := grpc.NewClient(options.ControllerAddress, allDialOptions...)
	if err != nil {
		return nil, fmt.Errorf("connecting to service portal controller at %s: %w", options.ControllerAddress, err)
	}
	return &Client{
		connection: connection,
		client:     portalsv1.NewPodBindServiceClient(connection),
		tokenFile:  options.TokenFile,
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	if c.connection != nil {
		return c.connection.Close()
	}
	return nil
}

func (c *Client) authenticatedContext(ctx context.Context) (context.Context, error) {
	tokenBytes, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return nil, fmt.Errorf("reading token file: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token), nil
}

func checkAcceptance(operation string, status portalsv1.AcceptanceStatus, reason string, missingSecrets []string) error {
	switch status {
	case portalsv1.AcceptanceStatus_ACCEPTANCE_STATUS_ACCEPTED:
		return nil
	case portalsv1.AcceptanceStatus_ACCEPTANCE_STATUS_REFUSED,
		portalsv1.AcceptanceStatus_ACCEPTANCE_STATUS_PENDING:
		return &StatusError{
			Operation:      operation,
			Status:         status,
			Reason:         reason,
			MissingSecrets: missingSecrets,
		}
	default:
		return fmt.Errorf("unexpected %s status: %v", operation, status)
	}
}

// Bind binds an egress policy to the pod identified by podUID.
func (c *Client) Bind(ctx context.Context, podUID types.UID, policy *portalsv1.EgressPolicy) error {
	callContext, err := c.authenticatedContext(ctx)
	if err != nil {
		return err
	}

	response, err := c.client.Bind(callContext, &portalsv1.PodPolicy{
		PodUid: string(podUID),
		Policy: policy,
		Wait:   true,
	})
	if err != nil {
		return fmt.Errorf("bind RPC failed: %w", err)
	}
	if response == nil {
		return errors.New("bind response is nil")
	}

	return checkAcceptance("bind", response.GetStatus(), response.GetRefusalReason(), response.GetMissingSecrets())
}

// InjectSecrets supplies named secrets for the pod identified by podUID.
// Note: Secret values must never be logged.
func (c *Client) InjectSecrets(ctx context.Context, podUID types.UID, secrets map[string][]byte) error {
	callContext, err := c.authenticatedContext(ctx)
	if err != nil {
		return err
	}

	response, err := c.client.InjectSecrets(callContext, &portalsv1.PodSecrets{
		PodUid:  string(podUID),
		Secrets: secrets,
		Wait:    true,
	})
	if err != nil {
		return fmt.Errorf("inject secrets RPC failed: %w", err)
	}
	if response == nil {
		return errors.New("inject secrets response is nil")
	}

	return checkAcceptance("inject secrets", response.GetStatus(), response.GetRefusalReason(), response.GetMissingSecrets())
}
