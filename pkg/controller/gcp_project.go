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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gke-labs/eval-runner/pkg/configkeys"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/google/externalaccount"
)

const (
	defaultResourceManagerBaseURL = "https://cloudresourcemanager.googleapis.com"
	defaultCloudBillingBaseURL    = "https://cloudbilling.googleapis.com"
	defaultIAMBaseURL             = "https://iam.googleapis.com"
	defaultIAMCredentialsBaseURL  = "https://iamcredentials.googleapis.com"
	defaultOperationPollInterval  = 2 * time.Second
	gcpCloudPlatformScope         = "https://www.googleapis.com/auth/cloud-platform"
	gcpTaskServiceAccountID       = "eval-task"
	gcpAccessTokenPlaceholder     = "service-portal-placeholder"
	maxGCPResponseBytes           = 64 * 1024
)

var gcpOAuthTokenPattern = regexp.MustCompile(`ya29\.[A-Za-z0-9._-]+`)

// GCPProjectProvisioner manages ephemeral GCP projects for eval tasks.
type GCPProjectProvisioner interface {
	CreateProject(ctx context.Context, parentFolderID, billingAccountID, projectID string, taskRoles []string) error
	DeleteProject(ctx context.Context, projectID string) error
}

// GCPAccessTokenMinter mints short-lived project-scoped OAuth2 access tokens by
// impersonating each project's eval-task service account.
type GCPAccessTokenMinter interface {
	MintAccessToken(ctx context.Context, projectID string) (string, error)
}

// NewGCPTokenSource returns an OAuth2 TokenSource scoped to cloud-platform,
// using Workload Identity Federation when workloadIdentityProvider is set or Google ADC otherwise.
func NewGCPTokenSource(ctx context.Context, workloadIdentityProvider string) (oauth2.TokenSource, error) {
	if provider := strings.TrimSpace(workloadIdentityProvider); provider != "" {
		return externalaccount.NewTokenSource(ctx, externalaccount.Config{
			Audience:         provider,
			SubjectTokenType: "urn:ietf:params:oauth:token-type:jwt",
			TokenURL:         "https://sts.googleapis.com/v1/token",
			CredentialSource: &externalaccount.CredentialSource{File: configkeys.DefaultWorkloadIdentityTokenFile},
			Scopes:           []string{gcpCloudPlatformScope},
		})
	}
	return google.DefaultTokenSource(ctx, gcpCloudPlatformScope)
}

// RESTGCPProjectProvisioner provisions and deletes GCP projects and mints impersonated
// per-project task access tokens using lightweight REST calls.
type RESTGCPProjectProvisioner struct {
	TokenSource            oauth2.TokenSource
	HTTPClient             *http.Client
	ResourceManagerBaseURL string
	CloudBillingBaseURL    string
	IAMBaseURL             string
	IAMCredentialsBaseURL  string
	PollInterval           time.Duration
}

type gcpOperation struct {
	Name  string `json:"name"`
	Done  bool   `json:"done"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type gcpIAMBinding struct {
	Role      string          `json:"role"`
	Members   []string        `json:"members"`
	Condition json.RawMessage `json:"condition,omitempty"`
}

type gcpIAMPolicy struct {
	Version  int             `json:"version,omitempty"`
	Bindings []gcpIAMBinding `json:"bindings,omitempty"`
	Etag     string          `json:"etag,omitempty"`
}

// CreateProject creates a GCP project under parentFolderID, polls the creation operation,
// links the project to billingAccountID, creates the eval-task service account, and grants taskRoles.
func (provisioner *RESTGCPProjectProvisioner) CreateProject(ctx context.Context, parentFolderID, billingAccountID, projectID string, taskRoles []string) error {
	resourceManagerBaseURL := baseURLOrDefault(provisioner.ResourceManagerBaseURL, defaultResourceManagerBaseURL)
	cloudBillingBaseURL := baseURLOrDefault(provisioner.CloudBillingBaseURL, defaultCloudBillingBaseURL)
	pollInterval := provisioner.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultOperationPollInterval
	}

	createPayload := map[string]string{
		"projectId":   projectID,
		"displayName": projectID,
		"parent":      "folders/" + strings.TrimPrefix(strings.TrimSpace(parentFolderID), "folders/"),
	}
	statusCode, responseBody, err := provisioner.doJSONRequest(ctx, http.MethodPost, resourceManagerBaseURL+"/v3/projects", createPayload)
	if err != nil {
		return err
	}
	if statusCode < 200 || statusCode >= 300 {
		return fmt.Errorf("create gcp project %q failed (HTTP %d): %s", projectID, statusCode, strings.TrimSpace(string(responseBody)))
	}

	var operation gcpOperation
	if err := json.Unmarshal(responseBody, &operation); err != nil {
		return fmt.Errorf("decode create gcp project %q operation: %w", projectID, err)
	}
	for !operation.Done {
		if operation.Name == "" {
			return fmt.Errorf("create gcp project %q returned incomplete operation without name", projectID)
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		pollStatus, pollBody, pollErr := provisioner.doJSONRequest(ctx, http.MethodGet, resourceManagerBaseURL+"/v3/"+strings.TrimPrefix(operation.Name, "/"), nil)
		if pollErr != nil {
			return pollErr
		}
		if pollStatus < 200 || pollStatus >= 300 {
			return fmt.Errorf("poll gcp project %q operation failed (HTTP %d): %s", projectID, pollStatus, strings.TrimSpace(string(pollBody)))
		}
		operation = gcpOperation{}
		if err := json.Unmarshal(pollBody, &operation); err != nil {
			return fmt.Errorf("decode gcp project %q poll operation: %w", projectID, err)
		}
	}
	if operation.Error != nil {
		return fmt.Errorf("create gcp project %q operation failed (code %d): %s", projectID, operation.Error.Code, operation.Error.Message)
	}

	cleanupOnError := func(stepErr error) error {
		cleanupContext, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		cleanupErr := provisioner.DeleteProject(cleanupContext, projectID)
		cleanupCancel()
		if cleanupErr != nil {
			return fmt.Errorf("%w (cleanup delete failed: %v)", stepErr, cleanupErr)
		}
		return stepErr
	}

	billingPayload := map[string]string{
		"billingAccountName": "billingAccounts/" + strings.TrimPrefix(strings.TrimSpace(billingAccountID), "billingAccounts/"),
	}
	billingURL := cloudBillingBaseURL + "/v1/projects/" + url.PathEscape(projectID) + "/billingInfo"
	billingStatus, billingBody, billingErr := provisioner.doJSONRequest(ctx, http.MethodPut, billingURL, billingPayload)
	if billingErr != nil || billingStatus < 200 || billingStatus >= 300 {
		if billingErr == nil {
			billingErr = fmt.Errorf("link billing for gcp project %q failed (HTTP %d): %s", projectID, billingStatus, strings.TrimSpace(string(billingBody)))
		}
		return cleanupOnError(billingErr)
	}

	if err := provisioner.ensureTaskServiceAccount(ctx, projectID); err != nil {
		return cleanupOnError(err)
	}
	if err := provisioner.bindTaskServiceAccountRoles(ctx, projectID, taskRoles); err != nil {
		return cleanupOnError(err)
	}
	return nil
}

func (provisioner *RESTGCPProjectProvisioner) ensureTaskServiceAccount(ctx context.Context, projectID string) error {
	iamBaseURL := baseURLOrDefault(provisioner.IAMBaseURL, defaultIAMBaseURL)
	requestURL := iamBaseURL + "/v1/projects/" + url.PathEscape(projectID) + "/serviceAccounts"
	payload := map[string]any{
		"accountId": gcpTaskServiceAccountID,
		"serviceAccount": map[string]string{
			"displayName": "eval-runner task service account",
		},
	}
	statusCode, responseBody, err := provisioner.doJSONRequest(ctx, http.MethodPost, requestURL, payload)
	if err != nil {
		return fmt.Errorf("create gcp task service account for project %q: %w", projectID, err)
	}
	if (statusCode >= 200 && statusCode < 300) || statusCode == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("create gcp task service account for project %q failed (HTTP %d): %s", projectID, statusCode, strings.TrimSpace(string(responseBody)))
}

func (provisioner *RESTGCPProjectProvisioner) bindTaskServiceAccountRoles(ctx context.Context, projectID string, taskRoles []string) error {
	var roles []string
	seen := make(map[string]struct{}, len(taskRoles))
	for _, rawRole := range taskRoles {
		role := strings.TrimSpace(rawRole)
		if role == "" {
			continue
		}
		if _, duplicate := seen[role]; duplicate {
			continue
		}
		seen[role] = struct{}{}
		roles = append(roles, role)
	}
	if len(roles) == 0 {
		return nil
	}

	resourceManagerBaseURL := baseURLOrDefault(provisioner.ResourceManagerBaseURL, defaultResourceManagerBaseURL)
	projectURL := resourceManagerBaseURL + "/v3/projects/" + url.PathEscape(projectID)
	getStatus, getBody, err := provisioner.doJSONRequest(ctx, http.MethodPost, projectURL+":getIamPolicy", map[string]any{})
	if err != nil {
		return fmt.Errorf("get iam policy for gcp project %q: %w", projectID, err)
	}
	if getStatus < 200 || getStatus >= 300 {
		return fmt.Errorf("get iam policy for gcp project %q failed (HTTP %d): %s", projectID, getStatus, strings.TrimSpace(string(getBody)))
	}

	var policy gcpIAMPolicy
	if err := json.Unmarshal(getBody, &policy); err != nil {
		return fmt.Errorf("decode iam policy for gcp project %q: %w", projectID, err)
	}

	member := "serviceAccount:" + gcpTaskServiceAccountEmail(projectID)
	for _, role := range roles {
		matched := false
		for i := range policy.Bindings {
			if policy.Bindings[i].Role == role && len(policy.Bindings[i].Condition) == 0 {
				if !slices.Contains(policy.Bindings[i].Members, member) {
					policy.Bindings[i].Members = append(policy.Bindings[i].Members, member)
				}
				matched = true
				break
			}
		}
		if !matched {
			policy.Bindings = append(policy.Bindings, gcpIAMBinding{
				Role:    role,
				Members: []string{member},
			})
		}
	}

	setStatus, setBody, err := provisioner.doJSONRequest(ctx, http.MethodPost, projectURL+":setIamPolicy", map[string]any{"policy": policy})
	if err != nil {
		return fmt.Errorf("set iam policy for gcp project %q: %w", projectID, err)
	}
	if setStatus < 200 || setStatus >= 300 {
		return fmt.Errorf("set iam policy for gcp project %q failed (HTTP %d): %s", projectID, setStatus, strings.TrimSpace(string(setBody)))
	}
	return nil
}

// MintAccessToken mints a short-lived project-scoped OAuth2 access token by impersonating
// eval-task@<projectID>.iam.gserviceaccount.com via the IAM Credentials API.
func (provisioner *RESTGCPProjectProvisioner) MintAccessToken(ctx context.Context, projectID string) (string, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return "", fmt.Errorf("generate gcp access token: project id is empty")
	}
	serviceAccountEmail := gcpTaskServiceAccountEmail(trimmedProjectID)
	iamCredentialsBaseURL := baseURLOrDefault(provisioner.IAMCredentialsBaseURL, defaultIAMCredentialsBaseURL)
	requestURL := iamCredentialsBaseURL + "/v1/projects/-/serviceAccounts/" + url.PathEscape(serviceAccountEmail) + ":generateAccessToken"
	payload := map[string][]string{
		"scope": {gcpCloudPlatformScope},
	}

	statusCode, responseBody, err := provisioner.doJSONRequest(ctx, http.MethodPost, requestURL, payload)
	if err != nil {
		return "", fmt.Errorf("generate gcp access token for %q: %s", serviceAccountEmail, redactGCPTokens(err.Error()))
	}
	if statusCode < 200 || statusCode >= 300 {
		return "", fmt.Errorf("generate gcp access token for %q failed (HTTP %d): %s", serviceAccountEmail, statusCode, redactGCPTokens(strings.TrimSpace(string(responseBody))))
	}

	var tokenResponse struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(responseBody, &tokenResponse); err != nil {
		return "", fmt.Errorf("decode generate gcp access token response for %q: %s", serviceAccountEmail, redactGCPTokens(err.Error()))
	}
	accessToken := strings.TrimSpace(tokenResponse.AccessToken)
	if accessToken == "" {
		return "", fmt.Errorf("generate gcp access token for %q returned empty access token", serviceAccountEmail)
	}
	if !isValidHTTPHeaderValue(accessToken) {
		return "", fmt.Errorf("generate gcp access token for %q returned token with invalid HTTP header characters", serviceAccountEmail)
	}
	return accessToken, nil
}

// DeleteProject deletes a GCP project, treating 404 or already-deleting responses as idempotent success.
func (provisioner *RESTGCPProjectProvisioner) DeleteProject(ctx context.Context, projectID string) error {
	deleteURL := baseURLOrDefault(provisioner.ResourceManagerBaseURL, defaultResourceManagerBaseURL) + "/v3/projects/" + url.PathEscape(projectID)
	statusCode, responseBody, err := provisioner.doJSONRequest(ctx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		return err
	}
	if (statusCode >= 200 && statusCode < 300) || isGCPProjectAlreadyDeleted(statusCode, responseBody) {
		return nil
	}
	return fmt.Errorf("delete gcp project %q failed (HTTP %d): %s", projectID, statusCode, strings.TrimSpace(string(responseBody)))
}

func (provisioner *RESTGCPProjectProvisioner) doJSONRequest(ctx context.Context, method, requestURL string, payload any) (int, []byte, error) {
	if provisioner.TokenSource == nil {
		return 0, nil, fmt.Errorf("gcp token source is not configured")
	}
	token, err := provisioner.TokenSource.Token()
	if err != nil {
		return 0, nil, fmt.Errorf("obtain gcp access token: %s", redactGCPTokens(err.Error()))
	}
	accessToken := strings.TrimSpace(token.AccessToken)
	if accessToken == "" {
		return 0, nil, fmt.Errorf("obtained empty gcp access token")
	}
	secrets := map[string][]byte{gcpAccessTokenCredentialName: []byte(accessToken)}

	var bodyReader io.Reader
	if payload != nil {
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return 0, nil, marshalErr
		}
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, bodyReader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpClient := provisioner.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s", redactGCPTokens(redactSecrets(err.Error(), secrets)))
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxGCPResponseBytes))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return resp.StatusCode, nil, fmt.Errorf("%s", redactGCPTokens(redactSecrets(readErr.Error(), secrets)))
	}
	if closeErr != nil {
		return resp.StatusCode, nil, fmt.Errorf("%s", redactGCPTokens(redactSecrets(closeErr.Error(), secrets)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, []byte(redactGCPTokens(redactSecrets(string(responseBody), secrets))), nil
	}
	return resp.StatusCode, responseBody, nil
}

func gcpTaskServiceAccountEmail(projectID string) string {
	return gcpTaskServiceAccountID + "@" + projectID + ".iam.gserviceaccount.com"
}

func redactGCPTokens(message string) string {
	return gcpOAuthTokenPattern.ReplaceAllString(message, "[REDACTED]")
}

func baseURLOrDefault(configured, fallback string) string {
	if trimmed := strings.TrimRight(strings.TrimSpace(configured), "/"); trimmed != "" {
		return trimmed
	}
	return fallback
}

func isGCPProjectAlreadyDeleted(statusCode int, body []byte) bool {
	if statusCode == http.StatusNotFound {
		return true
	}
	if statusCode == http.StatusBadRequest || statusCode == http.StatusConflict {
		lower := strings.ToLower(string(body))
		return strings.Contains(lower, "delete_requested") ||
			strings.Contains(lower, "already being deleted") ||
			strings.Contains(lower, "already deleted") ||
			strings.Contains(lower, "not active")
	}
	return false
}

func generateGCPProjectID() (string, error) {
	randomBytes := make([]byte, 4)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate gcp project id: %w", err)
	}
	return "eval-" + hex.EncodeToString(randomBytes), nil
}

func gcpProjectTaskEnv(projectID string) map[string]string {
	if projectID == "" {
		return nil
	}
	return map[string]string{
		"GOOGLE_CLOUD_PROJECT":       projectID,
		"CLOUDSDK_CORE_PROJECT":      projectID,
		"PROJECT_ID":                 projectID,
		"CLOUDSDK_AUTH_ACCESS_TOKEN": gcpAccessTokenPlaceholder,
		"GOOGLE_OAUTH_ACCESS_TOKEN":  gcpAccessTokenPlaceholder,
	}
}
