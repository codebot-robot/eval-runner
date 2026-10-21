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
	"syscall"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/artifacts/sinkconfig"
	"github.com/gke-labs/eval-runner/pkg/ui"
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

	restCfg, err := config.GetConfig()
	if err != nil {
		log.Fatalf("unable to get kubeconfig: %v", err)
	}

	k8sClient, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Fatalf("unable to create kubernetes client: %v", err)
	}

	bucket := artifactBucket
	dir := artifactDir
	artCfg, err := sinkconfig.LoadArtifactConfig(context.Background(), k8sClient, namespace, sinkconfig.DefaultConfigMapName)
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
		s, err := sinkconfig.NewArtifactSink(context.Background(), bucket, dir, 0)
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

	if sink != nil {
		defer func() {
			if err := sink.Close(); err != nil {
				log.Printf("error closing artifact sink on shutdown: %v", err)
			}
		}()
	}

	server := ui.NewServer(ui.Config{
		Client:           k8sClient,
		DefaultNamespace: namespace,
		ArtifactSink:     sink,
	})

	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           server,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
