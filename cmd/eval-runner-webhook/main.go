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

package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/webhook"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
}

func main() {
	defaultNs := os.Getenv("POD_NAMESPACE")
	if defaultNs == "" {
		defaultNs = webhook.DefaultNamespace
	}

	var listenAddr string
	var namespace string
	var configMapName string
	var webhookSecretName string
	var defaultModel string
	var defaultLimit int
	var defaultBatchSize int
	var defaultMaxSamplesPerTask int
	var commandUsers string
	var githubTokenSecret string
	var githubAppSecret string
	var webhookRepositories string
	var workers int
	var queueSize int

	flag.StringVar(&listenAddr, "listen-addr", webhook.DefaultListenAddr, "TCP address for HTTP webhook server to listen on")
	flag.StringVar(&namespace, "namespace", defaultNs, "Kubernetes namespace to manage Runs and Comparisons in")
	flag.StringVar(&configMapName, "configmap-name", webhook.DefaultConfigMapName, "Name of ConfigMap containing cluster settings")
	flag.StringVar(&webhookSecretName, "github-webhook-secret", webhook.DefaultGitHubWebhookSecret, "Name of Secret containing GitHub webhook secret")
	flag.StringVar(&defaultModel, "default-model", "", "Default model identifier for created Runs (e.g. mockllm/model)")
	flag.IntVar(&defaultLimit, "default-limit", 0, "Default sample limit for created Runs (0 = unlimited)")
	flag.IntVar(&defaultBatchSize, "default-batch-size", webhook.DefaultBatchSize, "Default batch size for paired adaptive sampling")
	flag.IntVar(&defaultMaxSamplesPerTask, "default-max-samples-per-task", webhook.DefaultMaxSamplesPerTask, "Default maximum samples per task across batches")
	flag.StringVar(&commandUsers, "command-users", "", "Comma-separated allow-list of GitHub usernames permitted to run comment commands")
	flag.StringVar(&githubTokenSecret, "github-token-secret", "github-token", "Name of Secret containing GitHub token")
	flag.StringVar(&githubAppSecret, "github-app-secret", "github-app", "Name of Secret containing GitHub App credentials")
	flag.StringVar(&webhookRepositories, "webhook-repositories", "", "Comma-separated allow-list of repositories (owner/repo)")
	flag.IntVar(&workers, "workers", webhook.DefaultWorkers, "Number of concurrent worker goroutines")
	flag.IntVar(&queueSize, "queue-size", webhook.DefaultQueueSize, "Queue buffer size for incoming webhook events")
	flag.Parse()

	explicit := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		explicit[f.Name] = true
	})

	var limitPtr *int
	if explicit["default-limit"] && defaultLimit > 0 {
		limitPtr = &defaultLimit
	}
	var batchSizePtr *int
	if explicit["default-batch-size"] && defaultBatchSize > 0 {
		batchSizePtr = &defaultBatchSize
	}
	var maxSamplesPtr *int
	if explicit["default-max-samples-per-task"] && defaultMaxSamplesPerTask > 0 {
		maxSamplesPtr = &defaultMaxSamplesPerTask
	}

	rawFlags := webhook.RawWebhookFlags{
		Namespace:                namespace,
		ConfigMapName:            configMapName,
		ListenAddr:               listenAddr,
		GitHubWebhookSecret:      webhookSecretName,
		DefaultModel:             defaultModel,
		DefaultLimit:             limitPtr,
		DefaultBatchSize:         batchSizePtr,
		DefaultMaxSamplesPerTask: maxSamplesPtr,
		CommandUsers:             commandUsers,
		GitHubTokenSecret:        githubTokenSecret,
		GitHubAppSecret:          githubAppSecret,
		WebhookRepositories:      webhookRepositories,
		Workers:                  workers,
		QueueSize:                queueSize,
		Explicit:                 explicit,
	}

	restCfg, err := config.GetConfig()
	if err != nil {
		log.Fatalf("unable to get kubeconfig: %v", err)
	}

	k8sClient, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Fatalf("unable to create kubernetes client: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cm, err := webhook.LoadClusterConfig(ctx, k8sClient, namespace, configMapName)
	if err != nil {
		log.Printf("warning: could not load configmap %s/%s: %v", namespace, configMapName, err)
	}

	cfg, err := webhook.ResolveWebhookConfig(cm, rawFlags, k8sClient)
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	if len(cfg.WebhookRepositories) == 0 {
		log.Println("warning: webhookRepositories allow-list is empty; all repositories will be rejected (set to '*' to allow all)")
	} else {
		log.Printf("webhookRepositories allow-list configured: %v", cfg.WebhookRepositories)
	}

	server := webhook.NewServer(cfg)
	server.Start()
	defer server.Stop()

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           server,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("eval-runner-webhook listening on %s (namespace: %s, configmap: %s)", cfg.ListenAddr, cfg.Namespace, cfg.ConfigMapName)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down eval-runner-webhook gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}
}
