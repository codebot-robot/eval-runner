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

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/sandboxclient"
	portalsv1 "github.com/gke-labs/service-portals/pkg/portals/v1"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type fakeGCPProjectProvisioner struct {
	mu             sync.Mutex
	created        []createdGCPProjectCall
	deleted        []string
	createErr      error
	deleteErr      error
	deleteErrOnce  bool
	deleteErrFired bool
}

type createdGCPProjectCall struct {
	parentFolderID   string
	billingAccountID string
	projectID        string
	taskRoles        []string
}

func (provisioner *fakeGCPProjectProvisioner) CreateProject(_ context.Context, parentFolderID, billingAccountID, projectID string, taskRoles []string) error {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	if provisioner.createErr != nil {
		return provisioner.createErr
	}
	provisioner.created = append(provisioner.created, createdGCPProjectCall{
		parentFolderID:   parentFolderID,
		billingAccountID: billingAccountID,
		projectID:        projectID,
		taskRoles:        append([]string(nil), taskRoles...),
	})
	return nil
}

func (provisioner *fakeGCPProjectProvisioner) DeleteProject(_ context.Context, projectID string) error {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	if provisioner.deleteErr != nil && (!provisioner.deleteErrOnce || !provisioner.deleteErrFired) {
		provisioner.deleteErrFired = true
		return provisioner.deleteErr
	}
	provisioner.deleted = append(provisioner.deleted, projectID)
	return nil
}

type fakeGCPAccessTokenMinter struct {
	mu                sync.Mutex
	requestedProjects []string
	tokensByProject   map[string]string
	defaultToken      string
	mintErr           error
}

func (minter *fakeGCPAccessTokenMinter) MintAccessToken(_ context.Context, projectID string) (string, error) {
	minter.mu.Lock()
	defer minter.mu.Unlock()
	minter.requestedProjects = append(minter.requestedProjects, projectID)
	if minter.mintErr != nil {
		return "", minter.mintErr
	}
	if token, ok := minter.tokensByProject[projectID]; ok {
		return token, nil
	}
	if minter.defaultToken != "" {
		return minter.defaultToken, nil
	}
	return "ya29.impersonated-" + projectID, nil
}

type recordedServicePortalBind struct {
	podUID  string
	policy  *portalsv1.EgressPolicy
	secrets map[string][]byte
}

type fakeServicePortalClient struct {
	mu      sync.Mutex
	binds   []recordedServicePortalBind
	bindErr error
}

func (portal *fakeServicePortalClient) Bind(_ context.Context, podUID string, policy *portalsv1.EgressPolicy, secrets map[string][]byte) error {
	portal.mu.Lock()
	defer portal.mu.Unlock()
	copiedSecrets := make(map[string][]byte, len(secrets))
	for key, value := range secrets {
		copiedSecrets[key] = append([]byte(nil), value...)
	}
	portal.binds = append(portal.binds, recordedServicePortalBind{
		podUID:  podUID,
		policy:  policy,
		secrets: copiedSecrets,
	})
	return portal.bindErr
}

func createScheduledReadySandboxPod(t *testing.T, kubeClient client.Client, sandboxName, podUID, podIP string) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: "eval-runner-sandbox",
			UID:       types.UID(podUID),
		},
		Spec: corev1.PodSpec{
			NodeName: "worker-node-1",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: podIP,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
	if err := kubeClient.Create(t.Context(), pod); err != nil {
		t.Fatalf("create scheduled ready sandbox pod: %v", err)
	}
}

func TestRESTGCPProjectProvisioner_CreatePollLinkAndDelete(t *testing.T) {
	const accessToken = "ya29.super-secret-gcp-token"
	var mu sync.Mutex
	var requests []string
	var pollCount int
	var savedPolicy gcpIAMPolicy

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got := req.Header.Get("Authorization"); got != "Bearer "+accessToken {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		requests = append(requests, req.Method+" "+req.URL.Path)
		mu.Unlock()

		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/v3/projects":
			var body map[string]string
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if body["parent"] != "folders/123456" {
				http.Error(w, fmt.Sprintf("unexpected create body %v", body), http.StatusBadRequest)
				return
			}
			switch body["projectId"] {
			case "eval-billfail":
				_ = json.NewEncoder(w).Encode(map[string]any{"name": "operations/cp.43", "done": true})
			case "eval-iamfail":
				_ = json.NewEncoder(w).Encode(map[string]any{"name": "operations/cp.44", "done": true})
			case "eval-conflict-sa":
				_ = json.NewEncoder(w).Encode(map[string]any{"name": "operations/cp.45", "done": true})
			case "eval-deadbeef":
				_ = json.NewEncoder(w).Encode(map[string]any{"name": "operations/cp.42", "done": false})
			default:
				http.Error(w, fmt.Sprintf("unexpected create body %v", body), http.StatusBadRequest)
			}
		case req.Method == http.MethodGet && req.URL.Path == "/v3/operations/cp.42":
			mu.Lock()
			pollCount++
			done := pollCount >= 2
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "operations/cp.42", "done": done})
		case req.Method == http.MethodPut && (req.URL.Path == "/v1/projects/eval-deadbeef/billingInfo" || req.URL.Path == "/v1/projects/eval-iamfail/billingInfo" || req.URL.Path == "/v1/projects/eval-conflict-sa/billingInfo"):
			var body map[string]string
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if body["billingAccountName"] != "billingAccounts/012345-6789AB-CDEF01" {
				http.Error(w, fmt.Sprintf("unexpected billing body %v", body), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"billingEnabled": true})
		case req.Method == http.MethodPut && req.URL.Path == "/v1/projects/eval-billfail/billingInfo":
			http.Error(w, "billing quota exceeded", http.StatusForbidden)
		case req.Method == http.MethodPost && (req.URL.Path == "/v1/projects/eval-deadbeef/serviceAccounts" || req.URL.Path == "/v1/projects/eval-iamfail/serviceAccounts"):
			var body struct {
				AccountID      string            `json:"accountId"`
				ServiceAccount map[string]string `json:"serviceAccount"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if body.AccountID != "eval-task" {
				http.Error(w, fmt.Sprintf("unexpected accountId %q", body.AccountID), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"email": "eval-task@project.iam.gserviceaccount.com"})
		case req.Method == http.MethodPost && req.URL.Path == "/v1/projects/eval-conflict-sa/serviceAccounts":
			http.Error(w, `{"error":{"code":409,"status":"ALREADY_EXISTS"}}`, http.StatusConflict)
		case req.Method == http.MethodPost && (req.URL.Path == "/v3/projects/eval-deadbeef:getIamPolicy" || req.URL.Path == "/v3/projects/eval-conflict-sa:getIamPolicy"):
			_ = json.NewEncoder(w).Encode(gcpIAMPolicy{
				Version: 1,
				Etag:    "etag-v1",
				Bindings: []gcpIAMBinding{
					{Role: "roles/owner", Members: []string{"user:admin@example.com"}},
					{Role: "roles/viewer", Members: []string{"user:auditor@example.com"}},
				},
			})
		case req.Method == http.MethodPost && (req.URL.Path == "/v3/projects/eval-deadbeef:setIamPolicy" || req.URL.Path == "/v3/projects/eval-conflict-sa:setIamPolicy"):
			var body struct {
				Policy gcpIAMPolicy `json:"policy"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			savedPolicy = body.Policy
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(body.Policy)
		case req.Method == http.MethodPost && req.URL.Path == "/v3/projects/eval-iamfail:getIamPolicy":
			http.Error(w, "iam policy permission denied", http.StatusForbidden)
		case req.Method == http.MethodDelete && (req.URL.Path == "/v3/projects/eval-deadbeef" || req.URL.Path == "/v3/projects/eval-billfail" || req.URL.Path == "/v3/projects/eval-iamfail"):
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "operations/dp.42", "done": true})
		case req.Method == http.MethodDelete && req.URL.Path == "/v3/projects/eval-missing":
			http.Error(w, "not found", http.StatusNotFound)
		case req.Method == http.MethodDelete && req.URL.Path == "/v3/projects/eval-deleting":
			http.Error(w, `{"error":{"code":400,"message":"project is already in DELETE_REQUESTED state"}}`, http.StatusBadRequest)
		case req.Method == http.MethodDelete && req.URL.Path == "/v3/projects/eval-leak":
			http.Error(w, "internal error echoing "+accessToken, http.StatusInternalServerError)
		default:
			http.Error(w, "unexpected request: "+req.Method+" "+req.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	provisioner := &RESTGCPProjectProvisioner{
		TokenSource:            oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken}),
		HTTPClient:             server.Client(),
		ResourceManagerBaseURL: server.URL,
		CloudBillingBaseURL:    server.URL,
		IAMBaseURL:             server.URL,
		PollInterval:           time.Millisecond,
	}

	if err := provisioner.CreateProject(t.Context(), "123456", "012345-6789AB-CDEF01", "eval-deadbeef", []string{"roles/viewer", "roles/editor"}); err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}
	wantPolicy := gcpIAMPolicy{
		Version: 1,
		Etag:    "etag-v1",
		Bindings: []gcpIAMBinding{
			{Role: "roles/owner", Members: []string{"user:admin@example.com"}},
			{Role: "roles/viewer", Members: []string{"user:auditor@example.com", "serviceAccount:eval-task@eval-deadbeef.iam.gserviceaccount.com"}},
			{Role: "roles/editor", Members: []string{"serviceAccount:eval-task@eval-deadbeef.iam.gserviceaccount.com"}},
		},
	}
	if !reflect.DeepEqual(savedPolicy, wantPolicy) {
		t.Fatalf("saved IAM policy = %+v, want %+v", savedPolicy, wantPolicy)
	}

	if err := provisioner.CreateProject(t.Context(), "123456", "012345-6789AB-CDEF01", "eval-conflict-sa", DefaultGCPProjectTaskRoles); err != nil {
		t.Fatalf("CreateProject with 409 existing service account should succeed idempotently, got: %v", err)
	}
	if err := provisioner.CreateProject(t.Context(), "123456", "012345-6789AB-CDEF01", "eval-billfail", DefaultGCPProjectTaskRoles); err == nil {
		t.Fatal("expected billing link failure")
	}
	if err := provisioner.CreateProject(t.Context(), "123456", "012345-6789AB-CDEF01", "eval-iamfail", DefaultGCPProjectTaskRoles); err == nil {
		t.Fatal("expected IAM policy failure")
	}
	if err := provisioner.DeleteProject(t.Context(), "eval-deadbeef"); err != nil {
		t.Fatalf("DeleteProject failed: %v", err)
	}
	if err := provisioner.DeleteProject(t.Context(), "eval-missing"); err != nil {
		t.Fatalf("DeleteProject 404 should succeed idempotently, got: %v", err)
	}
	if err := provisioner.DeleteProject(t.Context(), "eval-deleting"); err != nil {
		t.Fatalf("DeleteProject DELETE_REQUESTED should succeed idempotently, got: %v", err)
	}
	leakErr := provisioner.DeleteProject(t.Context(), "eval-leak")
	if leakErr == nil {
		t.Fatal("expected error for 500 delete response")
	}
	if strings.Contains(leakErr.Error(), accessToken) {
		t.Fatalf("expected token redacted from error, got: %v", leakErr)
	}

	wantOrder := []string{
		"POST /v3/projects",
		"GET /v3/operations/cp.42",
		"GET /v3/operations/cp.42",
		"PUT /v1/projects/eval-deadbeef/billingInfo",
		"POST /v1/projects/eval-deadbeef/serviceAccounts",
		"POST /v3/projects/eval-deadbeef:getIamPolicy",
		"POST /v3/projects/eval-deadbeef:setIamPolicy",
		"POST /v3/projects",
		"PUT /v1/projects/eval-conflict-sa/billingInfo",
		"POST /v1/projects/eval-conflict-sa/serviceAccounts",
		"POST /v3/projects/eval-conflict-sa:getIamPolicy",
		"POST /v3/projects/eval-conflict-sa:setIamPolicy",
		"POST /v3/projects",
		"PUT /v1/projects/eval-billfail/billingInfo",
		"DELETE /v3/projects/eval-billfail",
		"POST /v3/projects",
		"PUT /v1/projects/eval-iamfail/billingInfo",
		"POST /v1/projects/eval-iamfail/serviceAccounts",
		"POST /v3/projects/eval-iamfail:getIamPolicy",
		"DELETE /v3/projects/eval-iamfail",
		"DELETE /v3/projects/eval-deadbeef",
		"DELETE /v3/projects/eval-missing",
		"DELETE /v3/projects/eval-deleting",
		"DELETE /v3/projects/eval-leak",
	}
	if len(requests) != len(wantOrder) {
		t.Fatalf("requests = %v, want %v", requests, wantOrder)
	}
	for i := range wantOrder {
		if requests[i] != wantOrder[i] {
			t.Errorf("request[%d] = %q, want %q", i, requests[i], wantOrder[i])
		}
	}
}

func TestRESTGCPProjectProvisioner_MintAccessToken(t *testing.T) {
	const controllerToken = "controller-wif-secret-token"
	const impersonatedToken = "ya29.project-scoped-impersonated-token"
	const leakedResponseToken = "ya29.leaked-in-error-body"

	var receivedPath string
	var receivedBody map[string][]string
	var requestCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requestCount++
		if got := req.Header.Get("Authorization"); got != "Bearer "+controllerToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		receivedPath = req.URL.Path
		if err := json.NewDecoder(req.Body).Decode(&receivedBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		switch req.URL.Path {
		case "/v1/projects/-/serviceAccounts/eval-task@eval-proj-1.iam.gserviceaccount.com:generateAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"accessToken": impersonatedToken,
				"expireTime":  "2026-11-26T23:59:59Z",
			})
		case "/v1/projects/-/serviceAccounts/eval-task@eval-proj-denied.iam.gserviceaccount.com:generateAccessToken":
			http.Error(w, "denied for "+controllerToken+" and "+leakedResponseToken, http.StatusForbidden)
		case "/v1/projects/-/serviceAccounts/eval-task@eval-proj-empty.iam.gserviceaccount.com:generateAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": "   "})
		case "/v1/projects/-/serviceAccounts/eval-task@eval-proj-badheader.iam.gserviceaccount.com:generateAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": "ya29.bad\ntoken"})
		default:
			http.Error(w, "not found: "+req.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	provisioner := &RESTGCPProjectProvisioner{
		TokenSource:           oauth2.StaticTokenSource(&oauth2.Token{AccessToken: controllerToken}),
		HTTPClient:            server.Client(),
		IAMCredentialsBaseURL: server.URL,
	}

	t.Run("mints project-scoped token with expected URL and scope body", func(t *testing.T) {
		gotToken, err := provisioner.MintAccessToken(t.Context(), "eval-proj-1")
		if err != nil {
			t.Fatalf("MintAccessToken failed: %v", err)
		}
		if gotToken != impersonatedToken {
			t.Fatalf("got token %q, want %q", gotToken, impersonatedToken)
		}
		wantPath := "/v1/projects/-/serviceAccounts/eval-task@eval-proj-1.iam.gserviceaccount.com:generateAccessToken"
		if receivedPath != wantPath {
			t.Errorf("request path = %q, want %q", receivedPath, wantPath)
		}
		wantBody := map[string][]string{"scope": {gcpCloudPlatformScope}}
		if !reflect.DeepEqual(receivedBody, wantBody) {
			t.Errorf("request body = %+v, want %+v", receivedBody, wantBody)
		}
	})

	t.Run("empty projectID fails closed without calling IAM Credentials API", func(t *testing.T) {
		beforeCount := requestCount
		if _, err := provisioner.MintAccessToken(t.Context(), "   "); err == nil {
			t.Fatal("expected error for empty projectID")
		}
		if requestCount != beforeCount {
			t.Fatalf("expected no HTTP calls for empty projectID, got %d", requestCount-beforeCount)
		}
	})

	t.Run("error response redacts controller token and OAuth tokens", func(t *testing.T) {
		_, err := provisioner.MintAccessToken(t.Context(), "eval-proj-denied")
		if err == nil {
			t.Fatal("expected error for 403 response")
		}
		if strings.Contains(err.Error(), controllerToken) || strings.Contains(err.Error(), leakedResponseToken) {
			t.Fatalf("expected tokens redacted from error, got: %q", err.Error())
		}
		if !strings.Contains(err.Error(), "[REDACTED]") {
			t.Fatalf("expected [REDACTED] marker in error, got: %q", err.Error())
		}
	})

	t.Run("empty and invalid header tokens fail closed without leaking token", func(t *testing.T) {
		if _, err := provisioner.MintAccessToken(t.Context(), "eval-proj-empty"); err == nil {
			t.Fatal("expected error for empty accessToken")
		}
		_, badErr := provisioner.MintAccessToken(t.Context(), "eval-proj-badheader")
		if badErr == nil {
			t.Fatal("expected error for token containing newline")
		}
		if strings.Contains(badErr.Error(), "ya29.bad") {
			t.Fatalf("invalid token leaked into error: %q", badErr.Error())
		}
	})
}

func TestBuildServicePortalEgressPolicyWithCredentials_GCPAndExactModelRule(t *testing.T) {
	options := ServicePortalOptions{
		AllowedHosts: append([]string(nil), DefaultServicePortalAllowedHosts...),
	}
	geminiCredential, ok, err := ModelCredentialForModel("google/gemini-2.5-pro", "")
	if err != nil || !ok {
		t.Fatalf("ModelCredentialForModel failed: ok=%v err=%v", ok, err)
	}

	policy := BuildServicePortalEgressPolicyWithCredentials(options, &geminiCredential, true)
	if err := policy.Validate(); err != nil {
		t.Fatalf("EgressPolicy.Validate() failed with both GCP wildcard and exact model rule: %v", err)
	}

	// Exact host rule for generativelanguage.googleapis.com must win over *.googleapis.com.
	modelRule := policy.Match(portalsv1.ProtocolHTTPS, "generativelanguage.googleapis.com", 443)
	if modelRule == nil || modelRule.GetHttps() == nil || modelRule.GetHttps().GetEffects() == nil {
		t.Fatalf("expected HTTPS rule with effects for generativelanguage.googleapis.com, got %v", modelRule)
	}
	modelHeaders := modelRule.GetHttps().GetEffects().GetReplaceHeaders()
	if len(modelHeaders) != 1 || modelHeaders[0].Header != "x-goog-api-key" || modelHeaders[0].CredentialUri != "secret://gemini-api-key" {
		t.Fatalf("generativelanguage.googleapis.com replace headers = %+v, want x-goog-api-key secret://gemini-api-key", modelHeaders)
	}

	// Any other *.googleapis.com host must match the GCP Bearer rule.
	gcpRule := policy.Match(portalsv1.ProtocolHTTPS, "storage.googleapis.com", 443)
	if gcpRule == nil || gcpRule.GetHttps() == nil || gcpRule.GetHttps().GetEffects() == nil {
		t.Fatalf("expected HTTPS rule with effects for storage.googleapis.com, got %v", gcpRule)
	}
	gcpHeaders := gcpRule.GetHttps().GetEffects().GetReplaceHeaders()
	if len(gcpHeaders) != 1 || gcpHeaders[0].Header != "Authorization" || gcpHeaders[0].Prefix != "Bearer " || gcpHeaders[0].CredentialUri != "secret://gcp-access-token" {
		t.Fatalf("storage.googleapis.com replace headers = %+v, want Authorization Bearer secret://gcp-access-token", gcpHeaders)
	}
}

func TestGCPProjectTask_FullLifecycleServicePortalAndCleanup(t *testing.T) {
	const controllerAccessToken = "ya29.controller-broad-token-must-never-be-injected"
	const realAccessToken = "ya29.real-short-lived-gcp-token-secret"
	projectIDPattern := regexp.MustCompile(`^eval-[0-9a-f]{8}$`)

	newFixture := func(t *testing.T, exitCode int) (*RunReconciler, client.Client, *fakeGCPProjectProvisioner, *fakeGCPAccessTokenMinter, *fakeServicePortalClient, *sandboxclient.FakeClient, ctrl.Request) {
		t.Helper()
		scheme := setupScheme(t)
		manifest := &v1alpha1.Manifest{
			Tasks: []v1alpha1.ManifestTask{{
				ID:      "gcp-task",
				Spec:    "eval.py@gcp",
				Image:   "gcp-task-image:v1",
				Command: []string{"./run-gcp-eval.sh"},
				Resources: []v1alpha1.ManifestResource{
					{Type: v1alpha1.ResourceTypeSandbox},
					{Type: v1alpha1.ResourceTypeGCPProject},
				},
			}},
		}
		run := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{Name: "run-gcp-lifecycle", Namespace: "eval-runner"},
			Spec:       v1alpha1.RunSpec{Repo: v1alpha1.RepoSpec{URL: "git://repo", Commit: "c1"}, Model: "mockllm/model"},
			Status:     v1alpha1.RunStatus{Phase: v1alpha1.RunPhaseRunning},
		}
		fakeSandboxClient := &sandboxclient.FakeClient{
			PollAsyncFn: func(_ context.Context, _ string) (*sandboxclient.AsyncTaskStatus, error) {
				return &sandboxclient.AsyncTaskStatus{State: sandboxclient.AsyncTaskCompleted, ExitCode: exitCode}, nil
			},
			ReadFileFn: func(_ context.Context, path string) ([]byte, error) {
				if path == "/out/results.json" {
					return []byte(`{"schemaVersion": 1, "samples": [{"id": "s1", "passed": true, "score": 1.0}]}`), nil
				}
				return nil, fmt.Errorf("unexpected file %q", path)
			},
		}
		provisioner := &fakeGCPProjectProvisioner{}
		minter := &fakeGCPAccessTokenMinter{defaultToken: realAccessToken}
		portalClient := &fakeServicePortalClient{}
		kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).WithStatusSubresource(run).Build()
		reconciler := &RunReconciler{
			Client:                kubeClient,
			Scheme:                scheme,
			InstanceID:            "controller-1",
			SandboxNamespace:      "eval-runner-sandbox",
			SandboxClient:         fakeSandboxClient,
			ArtifactSink:          artifacts.NewInMemorySink(),
			ServicePortalClient:   portalClient,
			GCPProjectProvisioner: provisioner,
			GCPAccessTokenMinter:  minter,
			GCPTokenSource:        oauth2.StaticTokenSource(&oauth2.Token{AccessToken: controllerAccessToken}),
			Config: ClusterConfig{
				GCPProjectParentFolderID:   "987654321",
				GCPProjectBillingAccountID: "01ABCD-23EF45-67GH89",
				GCPProjectTaskRoles:        []string{"roles/editor"},
				ServicePortal: ServicePortalOptions{
					AllowedHosts: append([]string(nil), DefaultServicePortalAllowedHosts...),
				},
			},
		}
		reconciler.SetCachedManifest(run.Namespace, run.Name, manifest)
		req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
		return reconciler, kubeClient, provisioner, minter, portalClient, fakeSandboxClient, req
	}

	t.Run("completion creates project, injects portal credentials and placeholder env, and deletes project", func(t *testing.T) {
		reconciler, kubeClient, provisioner, minter, portalClient, fakeSandboxClient, req := newFixture(t, 0)

		// Turn 1: initializes task, provisions GCP project, persists TaskStatus.GCPProject, creates sandbox.
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 1 error: %v", err)
		}
		if len(provisioner.created) != 1 {
			t.Fatalf("expected 1 created GCP project, got %+v", provisioner.created)
		}
		created := provisioner.created[0]
		if created.parentFolderID != "987654321" || created.billingAccountID != "01ABCD-23EF45-67GH89" {
			t.Fatalf("unexpected folder/billing args: %+v", created)
		}
		if !reflect.DeepEqual(created.taskRoles, []string{"roles/editor"}) {
			t.Fatalf("created taskRoles = %v, want [roles/editor]", created.taskRoles)
		}
		if !projectIDPattern.MatchString(created.projectID) || len(created.projectID) > 30 {
			t.Fatalf("invalid project ID %q", created.projectID)
		}

		var updatedRun v1alpha1.Run
		if err := kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
			t.Fatalf("get run: %v", err)
		}
		if len(updatedRun.Status.Tasks) != 1 || updatedRun.Status.Tasks[0].GCPProject != created.projectID {
			t.Fatalf("expected persisted GCPProject %q, got %+v", created.projectID, updatedRun.Status.Tasks)
		}

		var sandboxList sandboxv1beta1.SandboxList
		if err := kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox")); err != nil || len(sandboxList.Items) != 1 {
			t.Fatalf("expected 1 sandbox, got %d (err=%v)", len(sandboxList.Items), err)
		}
		createScheduledReadySandboxPod(t, kubeClient, sandboxList.Items[0].Name, "pod-uid-gcp-1", "10.244.1.20")

		// Turn 2: binds service portal with *.googleapis.com Bearer secret://gcp-access-token and starts async command.
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 2 error: %v", err)
		}
		if len(portalClient.binds) != 1 {
			t.Fatalf("expected 1 service portal bind, got %d", len(portalClient.binds))
		}
		bind := portalClient.binds[0]
		if bind.podUID != "pod-uid-gcp-1" {
			t.Errorf("bind podUID = %q, want pod-uid-gcp-1", bind.podUID)
		}
		if err := bind.policy.Validate(); err != nil {
			t.Fatalf("bound EgressPolicy invalid: %v", err)
		}
		matched := bind.policy.Match(portalsv1.ProtocolHTTPS, "cloudresourcemanager.googleapis.com", 443)
		if matched == nil || matched.GetHttps() == nil || matched.GetHttps().GetEffects() == nil {
			t.Fatalf("expected *.googleapis.com HTTPS rule with effects, got %v", matched)
		}
		headers := matched.GetHttps().GetEffects().GetReplaceHeaders()
		if len(headers) != 1 || headers[0].Header != "Authorization" || headers[0].Prefix != "Bearer " || headers[0].CredentialUri != "secret://gcp-access-token" {
			t.Fatalf("unexpected ReplaceHeaders: %+v", headers)
		}
		if string(bind.secrets["gcp-access-token"]) != realAccessToken {
			t.Fatalf("expected impersonated access token in service portal secrets, got %q", string(bind.secrets["gcp-access-token"]))
		}
		if !reflect.DeepEqual(minter.requestedProjects, []string{created.projectID}) {
			t.Fatalf("minter requestedProjects = %v, want [%q]", minter.requestedProjects, created.projectID)
		}

		if len(fakeSandboxClient.StartAsyncCalls) != 1 {
			t.Fatalf("expected 1 StartAsync call, got %d", len(fakeSandboxClient.StartAsyncCalls))
		}
		gotEnv := fakeSandboxClient.StartAsyncCalls[0].Config.Env
		wantEnv := map[string]string{
			"GOOGLE_CLOUD_PROJECT":       created.projectID,
			"CLOUDSDK_CORE_PROJECT":      created.projectID,
			"PROJECT_ID":                 created.projectID,
			"CLOUDSDK_AUTH_ACCESS_TOKEN": "service-portal-placeholder",
			"GOOGLE_OAUTH_ACCESS_TOKEN":  "service-portal-placeholder",
		}
		if len(gotEnv) != len(wantEnv) {
			t.Fatalf("StartAsync env = %v, want %v", gotEnv, wantEnv)
		}
		for key, wantValue := range wantEnv {
			if gotEnv[key] != wantValue {
				t.Errorf("StartAsync env[%q] = %q, want %q", key, gotEnv[key], wantValue)
			}
			if strings.Contains(gotEnv[key], realAccessToken) {
				t.Fatalf("real access token leaked into sandbox env[%q]", key)
			}
		}

		// Turn 3: polls completed async task, clears TaskStatus.GCPProject, deletes GCP project.
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 3 error: %v", err)
		}
		if len(provisioner.deleted) != 1 || provisioner.deleted[0] != created.projectID {
			t.Fatalf("expected deleted projects [%q], got %v", created.projectID, provisioner.deleted)
		}
		if err := kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
			t.Fatalf("get completed run: %v", err)
		}
		if updatedRun.Status.Tasks[0].Phase != v1alpha1.TaskPhaseCompleted || updatedRun.Status.Tasks[0].GCPProject != "" {
			t.Fatalf("expected Completed task with empty GCPProject, got %+v", updatedRun.Status.Tasks[0])
		}
	})

	t.Run("task failure deletes ephemeral project", func(t *testing.T) {
		reconciler, kubeClient, provisioner, _, _, _, req := newFixture(t, 1)
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 1 error: %v", err)
		}
		var sandboxList sandboxv1beta1.SandboxList
		_ = kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox"))
		createScheduledReadySandboxPod(t, kubeClient, sandboxList.Items[0].Name, "pod-uid-fail", "10.244.1.21")
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 2 error: %v", err)
		}
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 3 error: %v", err)
		}
		if len(provisioner.deleted) != 1 || provisioner.deleted[0] != provisioner.created[0].projectID {
			t.Fatalf("expected deleted project %q on failure, got %v", provisioner.created[0].projectID, provisioner.deleted)
		}
	})

	t.Run("task restart deletes old project and provisions fresh project", func(t *testing.T) {
		reconciler, kubeClient, provisioner, _, _, _, req := newFixture(t, 0)
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 1 error: %v", err)
		}
		firstProject := provisioner.created[0].projectID

		// Simulate controller restart before ExecutionID is recorded so sandbox is dirty -> Restart.
		reconciler.InstanceID = "controller-2"
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 2 (restart) error: %v", err)
		}
		if len(provisioner.deleted) != 1 || provisioner.deleted[0] != firstProject {
			t.Fatalf("expected old project %q deleted on restart, got %v", firstProject, provisioner.deleted)
		}
		var updatedRun v1alpha1.Run
		if err := kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
			t.Fatalf("get run: %v", err)
		}
		if updatedRun.Status.Tasks[0].Phase != v1alpha1.TaskPhasePending || updatedRun.Status.Tasks[0].GCPProject != "" {
			t.Fatalf("expected Pending task with cleared GCPProject after restart, got %+v", updatedRun.Status.Tasks[0])
		}

		// Next turn starts fresh attempt with a newly provisioned project.
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 3 (re-provision) error: %v", err)
		}
		if len(provisioner.created) != 2 || provisioner.created[1].projectID == firstProject {
			t.Fatalf("expected a fresh second project different from %q, got %+v", firstProject, provisioner.created)
		}
		if err := kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
			t.Fatalf("get run after re-provision: %v", err)
		}
		if updatedRun.Status.Tasks[0].GCPProject != provisioner.created[1].projectID {
			t.Fatalf("expected persisted second project %q, got %+v", provisioner.created[1].projectID, updatedRun.Status.Tasks[0])
		}
	})

	t.Run("execution deadline cutoff deletes ephemeral project", func(t *testing.T) {
		reconciler, kubeClient, provisioner, _, _, _, req := newFixture(t, 0)
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 1 error: %v", err)
		}
		createdProject := provisioner.created[0].projectID

		var run v1alpha1.Run
		if err := kubeClient.Get(t.Context(), req.NamespacedName, &run); err != nil {
			t.Fatalf("get run: %v", err)
		}
		expired := metav1.NewTime(time.Now().Add(-1 * time.Minute))
		run.Status.ExecutionDeadline = &expired
		if err := kubeClient.Status().Update(t.Context(), &run); err != nil {
			t.Fatalf("update deadline: %v", err)
		}

		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("deadline reconcile error: %v", err)
		}
		if len(provisioner.deleted) != 1 || provisioner.deleted[0] != createdProject {
			t.Fatalf("expected project %q deleted on deadline cutoff, got %v", createdProject, provisioner.deleted)
		}
	})

	t.Run("run deletion finalizer deletes ephemeral project and retries on transient delete error", func(t *testing.T) {
		reconciler, kubeClient, provisioner, _, _, _, req := newFixture(t, 0)
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 1 error: %v", err)
		}
		createdProject := provisioner.created[0].projectID

		var run v1alpha1.Run
		if err := kubeClient.Get(t.Context(), req.NamespacedName, &run); err != nil {
			t.Fatalf("get run: %v", err)
		}
		if err := kubeClient.Delete(t.Context(), &run); err != nil {
			t.Fatalf("delete run: %v", err)
		}

		// First deletion reconcile fails transiently -> finalizer must remain attached.
		provisioner.deleteErr = errors.New("transient crm error")
		provisioner.deleteErrOnce = true
		if _, err := reconciler.Reconcile(t.Context(), req); err == nil {
			t.Fatal("expected transient delete error")
		}
		if err := kubeClient.Get(t.Context(), req.NamespacedName, &run); err != nil {
			t.Fatalf("expected run still present while finalizer holds: %v", err)
		}
		if !controllerutil.ContainsFinalizer(&run, v1alpha1.FinalizerDiscoverySandbox) {
			t.Fatal("expected finalizer retained after transient delete failure")
		}

		// Second deletion reconcile succeeds -> project deleted and finalizer removed.
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("finalizer retry error: %v", err)
		}
		if len(provisioner.deleted) != 1 || provisioner.deleted[0] != createdProject {
			t.Fatalf("expected project %q deleted by finalizer, got %v", createdProject, provisioner.deleted)
		}
	})

	t.Run("service portal bind error redacts gcp access token", func(t *testing.T) {
		reconciler, kubeClient, _, _, portalClient, _, req := newFixture(t, 0)
		portalClient.bindErr = errors.New("portal refused token " + realAccessToken)

		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 1 error: %v", err)
		}
		var sandboxList sandboxv1beta1.SandboxList
		_ = kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox"))
		createScheduledReadySandboxPod(t, kubeClient, sandboxList.Items[0].Name, "pod-uid-redact", "10.244.1.22")

		// Exhaust failure attempts so failure message lands on Run.Status.
		var run v1alpha1.Run
		_ = kubeClient.Get(t.Context(), req.NamespacedName, &run)
		run.Status.Tasks[0].FailureAttempts = maxTaskAttempts - 1
		_ = kubeClient.Status().Update(t.Context(), &run)

		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 2 error: %v", err)
		}
		_ = kubeClient.Get(t.Context(), req.NamespacedName, &run)
		if run.Status.Tasks[0].Phase != v1alpha1.TaskPhaseFailed {
			t.Fatalf("expected Failed phase, got %+v", run.Status.Tasks[0])
		}
		if strings.Contains(run.Status.Tasks[0].Message, realAccessToken) {
			t.Fatalf("real GCP token leaked into task message: %q", run.Status.Tasks[0].Message)
		}
		if !strings.Contains(run.Status.Tasks[0].Message, "[REDACTED]") {
			t.Fatalf("expected [REDACTED] in task message, got: %q", run.Status.Tasks[0].Message)
		}
	})
}

func TestGCPProjectTask_FailClosedGuards(t *testing.T) {
	manifest := &v1alpha1.Manifest{
		Tasks: []v1alpha1.ManifestTask{{
			ID:      "gcp-task",
			Spec:    "eval.py@gcp",
			Image:   "gcp-task-image:v1",
			Command: []string{"./run.sh"},
			Resources: []v1alpha1.ManifestResource{
				{Type: v1alpha1.ResourceTypeSandbox},
				{Type: v1alpha1.ResourceTypeGCPProject},
			},
		}},
	}

	cases := []struct {
		name             string
		portalClient     ServicePortalClient
		parentFolderID   string
		billingAccountID string
		wantMessagePart  string
	}{
		{
			name:             "service portal disabled",
			portalClient:     nil,
			parentFolderID:   "12345",
			billingAccountID: "012345-6789AB-CDEF01",
			wantMessagePart:  "service portal",
		},
		{
			name:             "parent folder empty",
			portalClient:     &fakeServicePortalClient{},
			parentFolderID:   "",
			billingAccountID: "012345-6789AB-CDEF01",
			wantMessagePart:  "gcpProjectParentFolderID",
		},
		{
			name:             "billing account empty",
			portalClient:     &fakeServicePortalClient{},
			parentFolderID:   "12345",
			billingAccountID: "",
			wantMessagePart:  "gcpProjectBillingAccountID",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			scheme := setupScheme(t)
			provisioner := &fakeGCPProjectProvisioner{}
			run := &v1alpha1.Run{
				ObjectMeta: metav1.ObjectMeta{Name: "run-fail-closed", Namespace: "eval-runner"},
				Spec:       v1alpha1.RunSpec{Model: "mockllm/model"},
				Status: v1alpha1.RunStatus{
					Phase: v1alpha1.RunPhaseRunning,
					Tasks: []v1alpha1.TaskStatus{{ID: "gcp-task", Phase: v1alpha1.TaskPhasePending}},
				},
			}
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).WithStatusSubresource(run).Build()
			reconciler := &RunReconciler{
				Client:                kubeClient,
				Scheme:                scheme,
				SandboxNamespace:      "eval-runner-sandbox",
				ServicePortalClient:   testCase.portalClient,
				GCPProjectProvisioner: provisioner,
				Config: ClusterConfig{
					GCPProjectParentFolderID:   testCase.parentFolderID,
					GCPProjectBillingAccountID: testCase.billingAccountID,
				},
			}
			reconciler.SetCachedManifest(run.Namespace, run.Name, manifest)

			// 1. initializeTasks fails closed.
			initialized := reconciler.initializeTasks(run.Spec, manifest)
			if len(initialized) != 1 || initialized[0].Phase != v1alpha1.TaskPhaseFailed || !strings.Contains(initialized[0].Message, testCase.wantMessagePart) {
				t.Fatalf("initializeTasks = %+v, want Failed containing %q", initialized, testCase.wantMessagePart)
			}

			// 2. startPendingTask also fails closed if a Pending task already exists.
			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
			if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
				t.Fatalf("reconcile error: %v", err)
			}
			var updatedRun v1alpha1.Run
			if err := kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
				t.Fatalf("get run: %v", err)
			}
			if updatedRun.Status.Tasks[0].Phase != v1alpha1.TaskPhaseFailed || !strings.Contains(updatedRun.Status.Tasks[0].Message, testCase.wantMessagePart) {
				t.Fatalf("task status = %+v, want Failed containing %q", updatedRun.Status.Tasks[0], testCase.wantMessagePart)
			}
			if len(provisioner.created) != 0 {
				t.Fatalf("expected no GCP projects created, got %+v", provisioner.created)
			}
		})
	}
}

func TestGCPProjectPool_LeaseReleaseAndContention(t *testing.T) {
	manifest := &v1alpha1.Manifest{
		Tasks: []v1alpha1.ManifestTask{{
			ID:      "kube-agents/kind-cluster",
			Command: []string{"/bin/sh", "-c", "true"},
			Resources: []v1alpha1.ManifestResource{
				{Type: v1alpha1.ResourceTypeSandbox},
				{Type: v1alpha1.ResourceTypeGCPProject},
			},
		}},
	}

	t.Run("leases pooled project, injects portal credentials without leaking keys, and releases without DeleteProject", func(t *testing.T) {
		scheme := setupScheme(t)
		const realModelKey = "real-gemini-api-key"
		const controllerAccessToken = "controller-broad-oauth-token"
		const realAccessToken = "ya29.pool-proj-1-impersonated-token"

		modelSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: DefaultModelCredentialsSecretName, Namespace: "eval-runner"},
			Data:       map[string][]byte{"GEMINI_API_KEY": []byte(realModelKey)},
		}
		run := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{Name: "run-pool-complete", Namespace: "eval-runner"},
			Spec:       v1alpha1.RunSpec{Repo: v1alpha1.RepoSpec{URL: "git://repo", Commit: "c1"}, Model: "google/gemini-2.5-flash"},
			Status:     v1alpha1.RunStatus{Phase: v1alpha1.RunPhaseRunning},
		}
		fakeSandboxClient := &sandboxclient.FakeClient{
			PollAsyncFn: func(_ context.Context, _ string) (*sandboxclient.AsyncTaskStatus, error) {
				return &sandboxclient.AsyncTaskStatus{State: sandboxclient.AsyncTaskCompleted, ExitCode: 0}, nil
			},
			ReadFileFn: func(_ context.Context, path string) ([]byte, error) {
				if path == "/out/results.json" {
					return []byte(`{"schemaVersion": 1, "samples": [{"id": "s1", "passed": true, "score": 1.0}]}`), nil
				}
				return nil, fmt.Errorf("unexpected file %q", path)
			},
		}
		provisioner := &fakeGCPProjectProvisioner{}
		minter := &fakeGCPAccessTokenMinter{
			tokensByProject: map[string]string{"pool-proj-1": realAccessToken},
		}
		portalClient := &fakeServicePortalClient{}
		kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(modelSecret, run).WithStatusSubresource(run).Build()
		reconciler := &RunReconciler{
			Client:                kubeClient,
			APIReader:             kubeClient,
			Scheme:                scheme,
			InstanceID:            "controller-1",
			ControllerNamespace:   "eval-runner",
			SandboxNamespace:      "eval-runner-sandbox",
			SandboxClient:         fakeSandboxClient,
			ArtifactSink:          artifacts.NewInMemorySink(),
			ServicePortalClient:   portalClient,
			GCPProjectProvisioner: provisioner,
			GCPAccessTokenMinter:  minter,
			GCPTokenSource:        oauth2.StaticTokenSource(&oauth2.Token{AccessToken: controllerAccessToken}),
			Config: ClusterConfig{
				GCPProjectPool: []string{"pool-proj-1", "pool-proj-2"},
				ServicePortal: ServicePortalOptions{
					AllowedHosts: append([]string(nil), DefaultServicePortalAllowedHosts...),
				},
			},
		}
		reconciler.SetCachedManifest(run.Namespace, run.Name, manifest)
		req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}

		// Turn 1: leases pool-proj-1 without CreateProject.
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 1 error: %v", err)
		}
		if len(provisioner.created) != 0 {
			t.Fatalf("expected no CreateProject calls when using pool, got %+v", provisioner.created)
		}
		var updatedRun v1alpha1.Run
		if err := kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
			t.Fatalf("get run: %v", err)
		}
		if len(updatedRun.Status.Tasks) != 1 || updatedRun.Status.Tasks[0].Phase != v1alpha1.TaskPhaseRunning || updatedRun.Status.Tasks[0].GCPProject != "pool-proj-1" {
			t.Fatalf("expected Running task with leased pool-proj-1, got %+v", updatedRun.Status.Tasks)
		}

		var sandboxList sandboxv1beta1.SandboxList
		if err := kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox")); err != nil || len(sandboxList.Items) != 1 {
			t.Fatalf("expected 1 sandbox, got %d (err=%v)", len(sandboxList.Items), err)
		}
		// Never inject real model API key into sandbox pod env.
		for _, container := range sandboxList.Items[0].Spec.PodTemplate.Spec.Containers {
			for _, environmentVariable := range container.Env {
				if strings.Contains(environmentVariable.Value, realModelKey) || strings.Contains(environmentVariable.Value, realAccessToken) {
					t.Fatalf("real secret leaked into sandbox pod env %s", environmentVariable.Name)
				}
			}
		}
		createScheduledReadySandboxPod(t, kubeClient, sandboxList.Items[0].Name, "pod-uid-pool-1", "10.244.1.30")

		// Turn 2: binds service portal with both model and GCP rules and starts async task.
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 2 error: %v", err)
		}
		if len(portalClient.binds) != 1 {
			t.Fatalf("expected 1 service portal bind, got %d", len(portalClient.binds))
		}
		bind := portalClient.binds[0]
		if err := bind.policy.Validate(); err != nil {
			t.Fatalf("bound policy invalid: %v", err)
		}
		if string(bind.secrets["gemini-api-key"]) != realModelKey || string(bind.secrets["gcp-access-token"]) != realAccessToken {
			t.Fatalf("expected both gemini-api-key and gcp-access-token in service portal secrets")
		}
		if string(bind.secrets["gcp-access-token"]) == controllerAccessToken {
			t.Fatalf("pooled task must never receive controller access token")
		}
		if !reflect.DeepEqual(minter.requestedProjects, []string{"pool-proj-1"}) {
			t.Fatalf("minter requestedProjects = %v, want [pool-proj-1]", minter.requestedProjects)
		}
		if len(fakeSandboxClient.StartAsyncCalls) != 1 || fakeSandboxClient.StartAsyncCalls[0].Config.Env["GOOGLE_CLOUD_PROJECT"] != "pool-proj-1" {
			t.Fatalf("unexpected StartAsync env: %+v", fakeSandboxClient.StartAsyncCalls)
		}
		for key, value := range fakeSandboxClient.StartAsyncCalls[0].Config.Env {
			if strings.Contains(value, realModelKey) || strings.Contains(value, realAccessToken) {
				t.Fatalf("real secret leaked into StartAsync env[%q]", key)
			}
		}

		// Turn 3: completes task, clears GCPProject, and never calls DeleteProject.
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("turn 3 error: %v", err)
		}
		if len(provisioner.deleted) != 0 {
			t.Fatalf("expected no DeleteProject calls for pooled project, got %v", provisioner.deleted)
		}
		if err := kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
			t.Fatalf("get completed run: %v", err)
		}
		if updatedRun.Status.Tasks[0].Phase != v1alpha1.TaskPhaseCompleted || updatedRun.Status.Tasks[0].GCPProject != "" {
			t.Fatalf("expected Completed task with cleared GCPProject, got %+v", updatedRun.Status.Tasks[0])
		}
	})

	t.Run("restart and deadline cutoff release pooled project without DeleteProject", func(t *testing.T) {
		scheme := setupScheme(t)
		now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
		pastDeadline := metav1.NewTime(now.Add(-time.Minute))
		startedAt := metav1.NewTime(now.Add(-2 * time.Minute))

		restartRun := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{Name: "run-pool-restart", Namespace: "eval-runner"},
			Spec:       v1alpha1.RunSpec{Model: "mockllm/model"},
			Status: v1alpha1.RunStatus{
				Phase: v1alpha1.RunPhaseRunning,
				Tasks: []v1alpha1.TaskStatus{{
					ID:         "kube-agents/kind-cluster",
					Phase:      v1alpha1.TaskPhaseRunning,
					StartedAt:  &startedAt,
					GCPProject: "pool-proj-1",
				}},
			},
		}
		cutoffRun := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{Name: "run-pool-cutoff", Namespace: "eval-runner"},
			Spec:       v1alpha1.RunSpec{Model: "mockllm/model"},
			Status: v1alpha1.RunStatus{
				Phase:             v1alpha1.RunPhaseRunning,
				ExecutionDeadline: &pastDeadline,
				Tasks: []v1alpha1.TaskStatus{{
					ID:         "kube-agents/kind-cluster",
					Phase:      v1alpha1.TaskPhaseRunning,
					StartedAt:  &startedAt,
					GCPProject: "pool-proj-2",
				}},
			},
		}
		provisioner := &fakeGCPProjectProvisioner{}
		kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(restartRun, cutoffRun).WithStatusSubresource(restartRun, cutoffRun).Build()
		reconciler := &RunReconciler{
			Client:                kubeClient,
			APIReader:             kubeClient,
			Scheme:                scheme,
			InstanceID:            "controller-new",
			SandboxNamespace:      "eval-runner-sandbox",
			ArtifactSink:          artifacts.NewInMemorySink(),
			ServicePortalClient:   &fakeServicePortalClient{},
			GCPProjectProvisioner: provisioner,
			Now:                   func() time.Time { return now },
			Config: ClusterConfig{
				GCPProjectPool:  []string{"pool-proj-1", "pool-proj-2"},
				TaskGracePeriod: 10 * time.Second,
			},
		}
		reconciler.SetCachedManifest(restartRun.Namespace, restartRun.Name, manifest)
		reconciler.SetCachedManifest(cutoffRun.Namespace, cutoffRun.Name, manifest)

		// Missing sandbox past grace period triggers task restart.
		restartRequest := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: restartRun.Namespace, Name: restartRun.Name}}
		if _, err := reconciler.Reconcile(t.Context(), restartRequest); err != nil {
			t.Fatalf("restart reconcile error: %v", err)
		}
		var updatedRestart v1alpha1.Run
		if err := kubeClient.Get(t.Context(), restartRequest.NamespacedName, &updatedRestart); err != nil {
			t.Fatalf("get restart run: %v", err)
		}
		if updatedRestart.Status.Tasks[0].Phase != v1alpha1.TaskPhasePending || updatedRestart.Status.Tasks[0].GCPProject != "" {
			t.Fatalf("expected restarted Pending task with cleared GCPProject, got %+v", updatedRestart.Status.Tasks[0])
		}

		// Exceeded execution deadline cuts off running task.
		cutoffRequest := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: cutoffRun.Namespace, Name: cutoffRun.Name}}
		if _, err := reconciler.Reconcile(t.Context(), cutoffRequest); err != nil {
			t.Fatalf("cutoff reconcile error: %v", err)
		}
		var updatedCutoff v1alpha1.Run
		if err := kubeClient.Get(t.Context(), cutoffRequest.NamespacedName, &updatedCutoff); err != nil {
			t.Fatalf("get cutoff run: %v", err)
		}
		if updatedCutoff.Status.Tasks[0].Phase != v1alpha1.TaskPhaseFailed || updatedCutoff.Status.Tasks[0].GCPProject != "" {
			t.Fatalf("expected cut-off Failed task with cleared GCPProject, got %+v", updatedCutoff.Status.Tasks[0])
		}
		if len(provisioner.deleted) != 0 {
			t.Fatalf("expected no DeleteProject calls on restart or cutoff, got %v", provisioner.deleted)
		}
	})

	t.Run("contention across runs leaves waiting task Pending and requeues until project is freed", func(t *testing.T) {
		scheme := setupScheme(t)
		activeRun := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{Name: "run-holder", Namespace: "eval-runner"},
			Spec:       v1alpha1.RunSpec{Model: "mockllm/model"},
			Status: v1alpha1.RunStatus{
				Phase: v1alpha1.RunPhaseRunning,
				Tasks: []v1alpha1.TaskStatus{{
					ID:         "kube-agents/kind-cluster",
					Phase:      v1alpha1.TaskPhaseRunning,
					GCPProject: "pool-proj-1",
				}},
			},
		}
		waitingRun := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{Name: "run-waiter", Namespace: "eval-runner"},
			Spec:       v1alpha1.RunSpec{Model: "mockllm/model"},
			Status: v1alpha1.RunStatus{
				Phase: v1alpha1.RunPhaseRunning,
				Tasks: []v1alpha1.TaskStatus{{
					ID:    "kube-agents/kind-cluster",
					Phase: v1alpha1.TaskPhasePending,
				}},
			},
		}
		provisioner := &fakeGCPProjectProvisioner{}
		kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(activeRun, waitingRun).WithStatusSubresource(activeRun, waitingRun).Build()
		reconciler := &RunReconciler{
			Client:                kubeClient,
			APIReader:             kubeClient,
			Scheme:                scheme,
			InstanceID:            "controller-1",
			SandboxNamespace:      "eval-runner-sandbox",
			ArtifactSink:          artifacts.NewInMemorySink(),
			ServicePortalClient:   &fakeServicePortalClient{},
			GCPProjectProvisioner: provisioner,
			Config: ClusterConfig{
				GCPProjectPool: []string{"pool-proj-1"},
			},
		}
		reconciler.SetCachedManifest(waitingRun.Namespace, waitingRun.Name, manifest)

		waitingRequest := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: waitingRun.Namespace, Name: waitingRun.Name}}
		result, err := reconciler.Reconcile(t.Context(), waitingRequest)
		if err != nil {
			t.Fatalf("reconcile waiting run error: %v", err)
		}
		if result.RequeueAfter <= 0 {
			t.Fatalf("expected positive RequeueAfter while pool is exhausted, got %+v", result)
		}

		var updatedWaiting v1alpha1.Run
		if err := kubeClient.Get(t.Context(), waitingRequest.NamespacedName, &updatedWaiting); err != nil {
			t.Fatalf("get waiting run: %v", err)
		}
		if updatedWaiting.Status.Tasks[0].Phase != v1alpha1.TaskPhasePending || updatedWaiting.Status.Tasks[0].GCPProject != "" {
			t.Fatalf("expected waiting task to stay Pending with empty GCPProject, got %+v", updatedWaiting.Status.Tasks[0])
		}
		var sandboxList sandboxv1beta1.SandboxList
		if err := kubeClient.List(t.Context(), &sandboxList, client.InNamespace("eval-runner-sandbox")); err != nil || len(sandboxList.Items) != 0 {
			t.Fatalf("expected 0 sandboxes created while waiting, got %d (err=%v)", len(sandboxList.Items), err)
		}

		// Free the pooled project on run-holder; run-waiter can now lease pool-proj-1.
		activeRun.Status.Phase = v1alpha1.RunPhaseCompleted
		activeRun.Status.Tasks[0].Phase = v1alpha1.TaskPhaseCompleted
		activeRun.Status.Tasks[0].GCPProject = ""
		if err := kubeClient.Status().Update(t.Context(), activeRun); err != nil {
			t.Fatalf("update active run status: %v", err)
		}

		if _, err := reconciler.Reconcile(t.Context(), waitingRequest); err != nil {
			t.Fatalf("reconcile after release error: %v", err)
		}
		if err := kubeClient.Get(t.Context(), waitingRequest.NamespacedName, &updatedWaiting); err != nil {
			t.Fatalf("get waiting run after release: %v", err)
		}
		if updatedWaiting.Status.Tasks[0].Phase != v1alpha1.TaskPhaseRunning || updatedWaiting.Status.Tasks[0].GCPProject != "pool-proj-1" {
			t.Fatalf("expected waiting task to lease pool-proj-1 once free, got %+v", updatedWaiting.Status.Tasks[0])
		}
		if len(provisioner.created) != 0 || len(provisioner.deleted) != 0 {
			t.Fatalf("expected no CreateProject/DeleteProject calls, got created=%v deleted=%v", provisioner.created, provisioner.deleted)
		}
	})

	t.Run("contention within same run never shares a pooled project across parallel tasks", func(t *testing.T) {
		scheme := setupScheme(t)
		twoTaskManifest := &v1alpha1.Manifest{
			Tasks: []v1alpha1.ManifestTask{
				{
					ID:      "kube-agents/task-1",
					Command: []string{"/bin/sh", "-c", "true"},
					Resources: []v1alpha1.ManifestResource{
						{Type: v1alpha1.ResourceTypeSandbox},
						{Type: v1alpha1.ResourceTypeGCPProject},
					},
				},
				{
					ID:      "kube-agents/task-2",
					Command: []string{"/bin/sh", "-c", "true"},
					Resources: []v1alpha1.ManifestResource{
						{Type: v1alpha1.ResourceTypeSandbox},
						{Type: v1alpha1.ResourceTypeGCPProject},
					},
				},
			},
		}
		run := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{Name: "run-same-contention", Namespace: "eval-runner"},
			Spec:       v1alpha1.RunSpec{Model: "mockllm/model"},
			Status:     v1alpha1.RunStatus{Phase: v1alpha1.RunPhaseRunning},
		}
		kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).WithStatusSubresource(run).Build()
		reconciler := &RunReconciler{
			Client:                kubeClient,
			APIReader:             kubeClient,
			Scheme:                scheme,
			InstanceID:            "controller-1",
			SandboxNamespace:      "eval-runner-sandbox",
			ArtifactSink:          artifacts.NewInMemorySink(),
			ServicePortalClient:   &fakeServicePortalClient{},
			GCPProjectProvisioner: &fakeGCPProjectProvisioner{},
			Config: ClusterConfig{
				MaxParallelTasks: 2,
				GCPProjectPool:   []string{"pool-proj-1"},
			},
		}
		reconciler.SetCachedManifest(run.Namespace, run.Name, twoTaskManifest)

		req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
		if _, err := reconciler.Reconcile(t.Context(), req); err != nil {
			t.Fatalf("reconcile error: %v", err)
		}
		var updatedRun v1alpha1.Run
		if err := kubeClient.Get(t.Context(), req.NamespacedName, &updatedRun); err != nil {
			t.Fatalf("get run: %v", err)
		}
		if len(updatedRun.Status.Tasks) != 2 {
			t.Fatalf("expected 2 tasks, got %+v", updatedRun.Status.Tasks)
		}
		if updatedRun.Status.Tasks[0].Phase != v1alpha1.TaskPhaseRunning || updatedRun.Status.Tasks[0].GCPProject != "pool-proj-1" {
			t.Fatalf("expected task-1 Running with pool-proj-1, got %+v", updatedRun.Status.Tasks[0])
		}
		if updatedRun.Status.Tasks[1].Phase != v1alpha1.TaskPhasePending || updatedRun.Status.Tasks[1].GCPProject != "" {
			t.Fatalf("expected task-2 Pending with empty GCPProject, got %+v", updatedRun.Status.Tasks[1])
		}
	})
}
