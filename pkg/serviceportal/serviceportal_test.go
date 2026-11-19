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

package serviceportal

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"k8s.io/apimachinery/pkg/types"

	portalsv1 "github.com/gke-labs/service-portals/pkg/portals/v1"
)

func generateTestCertificate(t *testing.T, ip net.IP) (certPEM, keyPEM []byte) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: ip.String()},
		IPAddresses:  []net.IP{ip},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKeyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateKeyDER})
	return certPEM, keyPEM
}

type fakeBindServer struct {
	portalsv1.UnimplementedPodBindServiceServer
	lastToken string
	response  *portalsv1.BindResponse
}

func (s *fakeBindServer) Bind(ctx context.Context, request *portalsv1.PodPolicy) (*portalsv1.BindResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if auth := md.Get("authorization"); len(auth) > 0 {
			s.lastToken = auth[0]
		}
	}
	return s.response, nil
}

func TestClient(t *testing.T) {
	ip := net.ParseIP("127.0.0.1")
	certPEM, keyPEM := generateTestCertificate(t, ip)
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("tls.X509KeyPair: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer listener.Close()

	fakeServer := &fakeBindServer{}
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewServerTLSFromCert(&tlsCert)))
	portalsv1.RegisterPodBindServiceServer(grpcServer, fakeServer)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	defer grpcServer.Stop()

	temporaryDirectory := t.TempDir()
	caFile := filepath.Join(temporaryDirectory, "ca.crt")
	if err := os.WriteFile(caFile, certPEM, 0600); err != nil {
		t.Fatalf("writing CA file: %v", err)
	}

	tokenFile := filepath.Join(temporaryDirectory, "token")
	if err := os.WriteFile(tokenFile, []byte("test-sa-token"), 0600); err != nil {
		t.Fatalf("writing token file: %v", err)
	}

	options := Options{
		ControllerAddress: listener.Addr().String(),
		CAFile:            caFile,
		TokenFile:         tokenFile,
	}
	client, err := New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	const testPodUID = types.UID("pod-uid-test")
	policy := &portalsv1.EgressPolicy{}

	// 1. Accepted: returns nil and verifies server received token
	fakeServer.response = &portalsv1.BindResponse{
		PodUid: string(testPodUID),
		Status: portalsv1.AcceptanceStatus_ACCEPTANCE_STATUS_ACCEPTED,
	}
	if err := client.Bind(context.Background(), testPodUID, policy); err != nil {
		t.Errorf("expected nil error on accepted bind, got: %v", err)
	}
	if fakeServer.lastToken != "Bearer test-sa-token" {
		t.Errorf("server saw token %q, want 'Bearer test-sa-token'", fakeServer.lastToken)
	}

	// 2. Refused with a reason: returns error with reason
	fakeServer.response = &portalsv1.BindResponse{
		PodUid:        string(testPodUID),
		Status:        portalsv1.AcceptanceStatus_ACCEPTANCE_STATUS_REFUSED,
		RefusalReason: "pod not found on node",
	}
	err = client.Bind(context.Background(), testPodUID, policy)
	if err == nil {
		t.Fatal("expected error on refused bind, got nil")
	}
	var statusError *StatusError
	if !errors.As(err, &statusError) {
		t.Fatalf("expected StatusError, got: %T (%v)", err, err)
	}
	if statusError.Status != portalsv1.AcceptanceStatus_ACCEPTANCE_STATUS_REFUSED || statusError.Reason != "pod not found on node" {
		t.Errorf("unexpected StatusError: %+v", statusError)
	}
	if !strings.Contains(err.Error(), "pod not found on node") {
		t.Errorf("expected refusal reason in error message: %v", err)
	}

	// 3. Pending with missing secrets: returns error with reason and missing secrets
	fakeServer.response = &portalsv1.BindResponse{
		PodUid:         string(testPodUID),
		Status:         portalsv1.AcceptanceStatus_ACCEPTANCE_STATUS_PENDING,
		RefusalReason:  "waiting for secrets",
		MissingSecrets: []string{"model-api-key"},
	}
	err = client.Bind(context.Background(), testPodUID, policy)
	if err == nil {
		t.Fatal("expected error on pending bind, got nil")
	}
	if !errors.As(err, &statusError) {
		t.Fatalf("expected StatusError, got: %T (%v)", err, err)
	}
	if statusError.Status != portalsv1.AcceptanceStatus_ACCEPTANCE_STATUS_PENDING {
		t.Errorf("Status = %v, want PENDING", statusError.Status)
	}
	if !strings.Contains(err.Error(), "model-api-key") {
		t.Errorf("expected missing secret in error message: %v", err)
	}

	// 4. Wrong CA: fails connection / handshake
	wrongCertPEM, _ := generateTestCertificate(t, ip)
	wrongCAFile := filepath.Join(temporaryDirectory, "wrong-ca.crt")
	if err := os.WriteFile(wrongCAFile, wrongCertPEM, 0600); err != nil {
		t.Fatalf("writing wrong CA file: %v", err)
	}

	wrongClient, err := New(Options{
		ControllerAddress: listener.Addr().String(),
		CAFile:            wrongCAFile,
		TokenFile:         tokenFile,
	})
	if err != nil {
		t.Fatalf("New with wrong CA: %v", err)
	}
	defer wrongClient.Close()

	if err := wrongClient.Bind(context.Background(), testPodUID, policy); err == nil {
		t.Error("expected error with wrong CA, got nil")
	}
}
