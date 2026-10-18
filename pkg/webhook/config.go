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

package webhook

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// DefaultNamespace is the default namespace where webhook and runs reside.
	DefaultNamespace = "eval-runner"

	// DefaultConfigMapName is the cluster ConfigMap name.
	DefaultConfigMapName = "eval-runner-config"

	// DefaultGitHubWebhookSecret is the default Secret name containing the webhook HMAC secret.
	DefaultGitHubWebhookSecret = "github-webhook"

	// DefaultListenAddr is the default HTTP listen address.
	DefaultListenAddr = ":8080"

	// DefaultWorkers is the default number of worker goroutines.
	DefaultWorkers = 2

	// DefaultQueueSize is the default buffer size for the async work queue.
	DefaultQueueSize = 100

	// ConfigMapKeyDefaultModel is the ConfigMap key for the default model.
	ConfigMapKeyDefaultModel = "defaultModel"

	// ConfigMapKeyDefaultLimit is the ConfigMap key for the default sample limit.
	ConfigMapKeyDefaultLimit = "defaultLimit"

	// ConfigMapKeyGitHubWebhookSecret is the ConfigMap key for the webhook secret name.
	ConfigMapKeyGitHubWebhookSecret = "githubWebhookSecret"

	// ConfigMapKeyWebhookRepositories is the ConfigMap key for repository allow-list.
	ConfigMapKeyWebhookRepositories = "webhookRepositories"
)

// Config holds settings for the eval-runner-webhook service.
type Config struct {
	Namespace           string
	ConfigMapName       string
	ListenAddr          string
	GitHubWebhookSecret string
	DefaultModel        string
	DefaultLimit        *int
	WebhookRepositories []string
	Workers             int
	QueueSize           int

	// StaticWebhookSecret optionally provides the HMAC secret directly (useful for testing or env vars).
	StaticWebhookSecret []byte

	// Client is the Kubernetes client used for reading Secrets/ConfigMaps and managing Runs/Comparisons.
	Client client.Client
}

// RawWebhookFlags captures explicit flags passed on the command line.
type RawWebhookFlags struct {
	Namespace           string
	ConfigMapName       string
	ListenAddr          string
	GitHubWebhookSecret string
	DefaultModel        string
	DefaultLimit        *int
	WebhookRepositories string
	Workers             int
	QueueSize           int
	Explicit            map[string]bool
}

// IsExplicit reports whether a flag was explicitly set on the command line.
func (f RawWebhookFlags) IsExplicit(name string) bool {
	if f.Explicit == nil {
		return false
	}
	return f.Explicit[name]
}

// DefaultConfig returns a Config initialized with built-in default values.
func DefaultConfig() Config {
	return Config{
		Namespace:           DefaultNamespace,
		ConfigMapName:       DefaultConfigMapName,
		ListenAddr:          DefaultListenAddr,
		GitHubWebhookSecret: DefaultGitHubWebhookSecret,
		DefaultModel:        "",
		DefaultLimit:        nil,
		WebhookRepositories: nil,
		Workers:             DefaultWorkers,
		QueueSize:           DefaultQueueSize,
	}
}

// LoadClusterConfig loads the specified ConfigMap from the Kubernetes API.
func LoadClusterConfig(ctx context.Context, cl client.Reader, namespace, configMapName string) (*corev1.ConfigMap, error) {
	if cl == nil {
		return nil, nil
	}
	var cm corev1.ConfigMap
	key := types.NamespacedName{
		Namespace: namespace,
		Name:      configMapName,
	}
	if err := cl.Get(ctx, key, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get cluster ConfigMap %s/%s: %w", namespace, configMapName, err)
	}
	return &cm, nil
}

// ResolveWebhookConfig resolves configuration using precedence:
// explicit flag > ConfigMap value > built-in default.
func ResolveWebhookConfig(cm *corev1.ConfigMap, flags RawWebhookFlags, cl client.Client) (Config, error) {
	cfg := DefaultConfig()
	cfg.Client = cl

	if flags.IsExplicit("namespace") && flags.Namespace != "" {
		cfg.Namespace = flags.Namespace
	}
	if flags.IsExplicit("configmap-name") && flags.ConfigMapName != "" {
		cfg.ConfigMapName = flags.ConfigMapName
	}
	if flags.IsExplicit("listen-addr") && flags.ListenAddr != "" {
		cfg.ListenAddr = flags.ListenAddr
	}
	if flags.IsExplicit("workers") && flags.Workers > 0 {
		cfg.Workers = flags.Workers
	}
	if flags.IsExplicit("queue-size") && flags.QueueSize > 0 {
		cfg.QueueSize = flags.QueueSize
	}

	// 1. Apply from ConfigMap if present
	if cm != nil && cm.Data != nil {
		if val, ok := cm.Data[ConfigMapKeyGitHubWebhookSecret]; ok {
			trimmed := strings.TrimSpace(val)
			if trimmed != "" {
				cfg.GitHubWebhookSecret = trimmed
			}
		}
		if val, ok := cm.Data[ConfigMapKeyDefaultModel]; ok {
			cfg.DefaultModel = strings.TrimSpace(val)
		}
		if val, ok := cm.Data[ConfigMapKeyDefaultLimit]; ok {
			trimmed := strings.TrimSpace(val)
			if trimmed != "" {
				limit, err := strconv.Atoi(trimmed)
				if err != nil || limit <= 0 {
					return Config{}, fmt.Errorf("invalid %s %q in ConfigMap %s/%s: must be a positive integer",
						ConfigMapKeyDefaultLimit, val, cm.Namespace, cm.Name)
				}
				cfg.DefaultLimit = &limit
			}
		}
		if val, ok := cm.Data[ConfigMapKeyWebhookRepositories]; ok {
			cfg.WebhookRepositories = ParseRepositoryList(val)
		}
	}

	// 2. Apply explicit flags (highest precedence)
	if flags.IsExplicit("github-webhook-secret") && flags.GitHubWebhookSecret != "" {
		cfg.GitHubWebhookSecret = flags.GitHubWebhookSecret
	}
	if flags.IsExplicit("default-model") {
		cfg.DefaultModel = flags.DefaultModel
	}
	if flags.IsExplicit("default-limit") && flags.DefaultLimit != nil {
		cfg.DefaultLimit = flags.DefaultLimit
	}
	if flags.IsExplicit("webhook-repositories") {
		cfg.WebhookRepositories = ParseRepositoryList(flags.WebhookRepositories)
	}

	return cfg, nil
}

// ParseRepositoryList parses a comma-separated list of repository names ("owner/repo").
func ParseRepositoryList(s string) []string {
	var repos []string
	for _, part := range strings.Split(s, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			repos = append(repos, trimmed)
		}
	}
	return repos
}

// IsRepositoryAllowed checks whether repo (e.g. "gke-labs/eval-runner") is allowed.
// If allowList is empty, all repositories are rejected (fail-closed).
// An entry of "*" allows all repositories.
func IsRepositoryAllowed(repo string, allowList []string) bool {
	if len(allowList) == 0 {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(repo))
	if normalized == "" {
		return false
	}
	for _, allowed := range allowList {
		trimmed := strings.TrimSpace(allowed)
		if trimmed == "*" || strings.ToLower(trimmed) == normalized {
			return true
		}
	}
	return false
}
