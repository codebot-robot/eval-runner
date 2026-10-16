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
	"flag"
	"fmt"
	"os"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/controller"
	"github.com/gke-labs/eval-runner/pkg/discovery"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	discoveryk8s "k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	utilruntime.Must(sandboxv1beta1.AddToScheme(scheme))
}

func checkSandboxCRD(discoveryClient discoveryk8s.DiscoveryInterface) error {
	resources, err := discoveryClient.ServerResourcesForGroupVersion("agents.x-k8s.io/v1beta1")
	if err != nil {
		return fmt.Errorf("groupVersion agents.x-k8s.io/v1beta1 not found: %w", err)
	}
	for _, r := range resources.APIResources {
		if r.Kind == "Sandbox" {
			return nil
		}
	}
	return fmt.Errorf("resource kind Sandbox not found in agents.x-k8s.io/v1beta1")
}

func main() {
	var evalRunnerNamespace string
	var sandboxNamespace string
	var probeAddr string
	var maxStdoutBytes int64
	var maxStderrBytes int64

	flag.StringVar(&evalRunnerNamespace, "namespace", discovery.DefaultControllerNamespace, "Namespace in which the controller operates")
	flag.StringVar(&sandboxNamespace, "sandbox-namespace", discovery.DefaultSandboxNamespace, "Namespace in which sandboxes are spawned")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.Int64Var(&maxStdoutBytes, "max-stdout-bytes", discovery.MaxStdoutBytes, "Maximum stdout bytes buffered from discovery execution")
	flag.Int64Var(&maxStderrBytes, "max-stderr-bytes", discovery.MaxStderrBytes, "Maximum stderr bytes buffered from discovery execution")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	cfg := ctrl.GetConfigOrDie()

	// Pre-requisite check: Fail fast if agent-sandbox CRD is missing
	discoveryClient, err := discoveryk8s.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		setupLog.Error(err, "unable to create discovery client")
		os.Exit(1)
	}

	if err := checkSandboxCRD(discoveryClient); err != nil {
		setupLog.Error(err, "agent-sandbox CRD (agents.x-k8s.io/v1beta1) is missing; agent-sandbox must be installed as a prerequisite")
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		setupLog.Error(err, "unable to create clientset")
		os.Exit(1)
	}

	// Least-privilege namespaced caching per object:
	// Runs are only cached in evalRunnerNamespace.
	// Sandboxes and Pods are only cached in sandboxNamespace.
	cacheOptions := cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&v1alpha1.Run{}: {
				Namespaces: map[string]cache.Config{
					evalRunnerNamespace: {},
				},
			},
			&sandboxv1beta1.Sandbox{}: {
				Namespaces: map[string]cache.Config{
					sandboxNamespace: {},
				},
			},
			&corev1.Pod{}: {
				Namespaces: map[string]cache.Config{
					sandboxNamespace: {},
				},
			},
		},
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Cache:                  cacheOptions,
		LeaderElection:         false,
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	executor := &controller.SPDYPodExecutor{
		Config:      cfg,
		Clientset:   clientset,
		StdoutLimit: maxStdoutBytes,
		StderrLimit: maxStderrBytes,
	}

	reconciler := &controller.RunReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Executor:         executor,
		SandboxNamespace: sandboxNamespace,
		MaxStdoutBytes:   maxStdoutBytes,
		MaxStderrBytes:   maxStderrBytes,
		MaxMessageBytes:  discovery.MaxMessageBytes,
	}

	if err = reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Run")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
