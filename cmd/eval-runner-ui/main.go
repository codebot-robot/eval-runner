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
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/artifacts/sinkconfig"
	"github.com/gke-labs/eval-runner/pkg/ui"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
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
		defaultNs = "eval-runner"
	}

	var listenAddr string
	var namespace string
	var artifactBucket string
	var artifactDir string

	flag.StringVar(&listenAddr, "listen-addr", ":8080", "The TCP address for the HTTP server to listen on")
	flag.StringVar(&namespace, "namespace", defaultNs, "Default Kubernetes namespace to query for Runs and Comparisons")
	flag.StringVar(&artifactBucket, "artifact-bucket", "", "Optional GCS bucket for reading artifacts")
	flag.StringVar(&artifactDir, "artifact-dir", "", "Optional local filesystem directory for reading artifacts")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	restCfg, err := config.GetConfig()
	if err != nil {
		log.Fatalf("unable to get kubeconfig: %v", err)
	}

	k8sClient, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Fatalf("unable to create kubernetes client: %v", err)
	}

	// Namespaced informer cache for Runs and Comparisons in the default namespace.
	// Sandboxes and Pods are explicitly excluded to bound memory usage.
	cacheOptions := cache.Options{
		Scheme:                      scheme,
		ReaderFailOnMissingInformer: true,
		ByObject: map[client.Object]cache.ByObject{
			&v1alpha1.Run{}: {
				Namespaces: map[string]cache.Config{
					namespace: {},
				},
			},
			&v1alpha1.Comparison{}: {
				Namespaces: map[string]cache.Config{
					namespace: {},
				},
			},
		},
	}

	// cache.New performs REST-mapper discovery to verify object namespacing.
	// This requires Run and Comparison CRDs to already be installed in the cluster;
	// on a fresh install, ensure CRDs are applied before the UI deployment rolls out.
	informerCache, err := cache.New(restCfg, cacheOptions)
	if err != nil {
		log.Fatalf("unable to create cache: %v", err)
	}

	if _, err := informerCache.GetInformer(ctx, &v1alpha1.Run{}, cache.BlockUntilSynced(false)); err != nil {
		log.Fatalf("unable to initialize run informer: %v", err)
	}
	if _, err := informerCache.GetInformer(ctx, &v1alpha1.Comparison{}, cache.BlockUntilSynced(false)); err != nil {
		log.Fatalf("unable to initialize comparison informer: %v", err)
	}

	go func() {
		if err := informerCache.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Fatalf("informer cache failed: %v", err)
		}
	}()

	var cacheSynced atomic.Bool
	go func() {
		if informerCache.WaitForCacheSync(ctx) {
			cacheSynced.Store(true)
			log.Printf("informer cache synced for namespace %s", namespace)
		}
	}()

	bucket := artifactBucket
	dir := artifactDir
	artCfg, err := sinkconfig.LoadArtifactConfig(ctx, k8sClient, namespace, sinkconfig.DefaultConfigMapName)
	if err != nil {
		log.Printf("warning: unable to read cluster configmap %s/%s: %v", namespace, sinkconfig.DefaultConfigMapName, err)
	} else {
		if bucket == "" {
			bucket = artCfg.ArtifactBucket
		}
		if dir == "" {
			dir = artCfg.ArtifactDir
		}
	}

	var sink artifacts.ArtifactSink
	if bucket != "" || dir != "" {
		s, err := sinkconfig.NewArtifactSink(ctx, bucket, dir, 0)
		if err != nil {
			log.Printf("warning: unable to configure artifact sink (bucket=%q, dir=%q): %v", bucket, dir, err)
		} else {
			sink = s
			if sink != nil {
				log.Printf("configured artifact sink for UI (bucket=%q, dir=%q)", bucket, dir)
			}
		}
	} else {
		log.Printf("no artifact storage configured; sample trajectories will show configuration guidance")
	}

	server := ui.NewServer(ui.Config{
		Client:           k8sClient,
		Cache:            informerCache,
		CacheSyncChecker: cacheSynced.Load,
		DefaultNamespace: namespace,
		ArtifactSink:     sink,
		InitialBucket:    bucket,
		InitialDir:       dir,
	})
	defer func() {
		if err := server.ArtifactSink().Close(); err != nil {
			log.Printf("error closing artifact sink on shutdown: %v", err)
		}
	}()

	kubeClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		log.Printf("warning: unable to create kubernetes client for configmap watch: %v", err)
	} else {
		go func() {
			if watchErr := sinkconfig.WatchConfigMap(ctx, kubeClient, namespace, sinkconfig.DefaultConfigMapName, func(cm *corev1.ConfigMap) {
				if reloadErr := server.ReloadConfigMap(ctx, cm, artifactBucket, artifactDir, nil); reloadErr != nil {
					log.Printf("warning: error reloading configmap: %v", reloadErr)
				}
			}); watchErr != nil && !errors.Is(watchErr, context.Canceled) {
				log.Printf("warning: configmap watcher exited: %v", watchErr)
			}
		}()
	}

	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           server,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("eval-runner-ui listening on %s (default namespace: %s)", listenAddr, namespace)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down eval-runner-ui gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}
	fmt.Println("eval-runner-ui stopped")
}
