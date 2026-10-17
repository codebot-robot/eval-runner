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
	"fmt"
	"strings"
	"time"

	"github.com/gke-labs/eval-runner/pkg/discovery"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// DefaultModelSecret is the default Secret name in the sandbox namespace containing model provider credentials.
	DefaultModelSecret = "model-credentials"
)

// CheckModelSecretAtStartup verifies if the model credentials Secret exists in the sandbox namespace at startup,
// logging the result for operational visibility without failing controller startup.
func CheckModelSecretAtStartup(ctx context.Context, kubeClient kubernetes.Interface, sandboxNamespace, modelSecret string, logger logr.Logger) {
	if modelSecret == "" {
		logger.Info("model secret explicitly disabled; sample sandboxes will run without credentials")
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, err := kubeClient.CoreV1().Secrets(sandboxNamespace).Get(checkCtx, modelSecret, metav1.GetOptions{})
	if err == nil {
		logger.Info("model secret found in sandbox namespace", "secret", modelSecret, "namespace", sandboxNamespace)
	} else if apierrors.IsNotFound(err) {
		logger.Info("model secret not found in sandbox namespace; sample sandboxes will run without credentials unless created", "secret", modelSecret, "namespace", sandboxNamespace)
	} else {
		logger.Info("unable to check model secret in sandbox namespace at startup", "secret", modelSecret, "namespace", sandboxNamespace, "error", err)
	}
}

// CheckModelSecretExists checks whether the model secret exists in the sandbox namespace using the provided reader.
// It returns true if the secret exists, false if it does not exist (NotFound) or modelSecret is empty.
// Any other API error is returned to allow the caller to retry.
func CheckModelSecretExists(ctx context.Context, reader client.Reader, sandboxNamespace, modelSecret string) (bool, error) {
	if modelSecret == "" {
		return false, nil
	}
	var secret corev1.Secret
	err := reader.Get(ctx, types.NamespacedName{
		Namespace: sandboxNamespace,
		Name:      modelSecret,
	}, &secret)
	if err == nil {
		return true, nil
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

// IsMockModel reports whether the model identifier refers to a mock model that runs offline without credentials.
func IsMockModel(model string) bool {
	m := strings.TrimSpace(strings.ToLower(model))
	return m == "mockllm" || strings.HasPrefix(m, "mockllm/")
}

// ModelCredentialsMissingMessage returns the clear task-level failure message when a non-mock model is requested without credentials.
func ModelCredentialsMissingMessage(sandboxNamespace, modelSecret string) string {
	secretName := modelSecret
	if secretName == "" {
		secretName = DefaultModelSecret
	}
	if sandboxNamespace == "" {
		sandboxNamespace = discovery.DefaultSandboxNamespace
	}
	return fmt.Sprintf("no model credentials configured: create Secret %s/%s or set --model-secret", sandboxNamespace, secretName)
}
