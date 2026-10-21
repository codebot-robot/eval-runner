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
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gke-labs/eval-runner/pkg/artifacts/sinkconfig"
	"github.com/gke-labs/eval-runner/pkg/discovery"
	"github.com/go-logr/logr"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var adoptLogOnce sync.Once

// ReconcileSandboxEgressNetworkPolicy reconciles the baseline egress NetworkPolicy (eval-runner-sandbox-egress)
// in the specified namespace.
// When fqdnActive is true, the internet rule is omitted (no 0.0.0.0/0:443 or :80 allow), narrowing
// HTTPS egress strictly to the declared FQDNNetworkPolicy hosts.
// When fqdnActive is false (e.g. non-Dataplane V2 clusters, kind, or when egress hosts is "none"),
// the internet rule allowing ports 80, 443, and 9418 is included.
func ReconcileSandboxEgressNetworkPolicy(
	ctx context.Context,
	c client.Client,
	namespace string,
	fqdnActive bool,
	logger logr.Logger,
) error {
	name := discovery.DefaultSandboxEgressNetworkPolicyName
	desired := discovery.RenderSandboxEgressNetworkPolicy(name, namespace, fqdnActive)

	existing := &networkingv1.NetworkPolicy{}
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing)
	if apierrors.IsNotFound(err) {
		if createErr := c.Create(ctx, desired); createErr != nil {
			return fmt.Errorf("failed to create sandbox egress NetworkPolicy %s/%s: %w", namespace, name, createErr)
		}
		logger.Info("created sandbox egress NetworkPolicy",
			"namespace", namespace,
			"name", name,
			"fqdnActive", fqdnActive,
		)
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get sandbox egress NetworkPolicy %s/%s: %w", namespace, name, err)
	}

	if len(existing.Spec.Egress) != len(desired.Spec.Egress) || !equality.Semantic.DeepDerivative(desired.Spec, existing.Spec) {
		existing.Spec = desired.Spec
		if updateErr := c.Update(ctx, existing); updateErr != nil {
			return fmt.Errorf("failed to update sandbox egress NetworkPolicy %s/%s: %w", namespace, name, updateErr)
		}
		logger.Info("updated sandbox egress NetworkPolicy",
			"namespace", namespace,
			"name", name,
			"fqdnActive", fqdnActive,
		)
	} else {
		adoptLogOnce.Do(func() {
			logger.Info("adopted existing sandbox egress NetworkPolicy",
				"namespace", namespace,
				"name", name,
				"fqdnActive", fqdnActive,
			)
		})
	}

	return nil
}

// ReconcileSandboxEgressPolicies reconciles both the baseline egress NetworkPolicy and,
// when fqdnVersion is non-empty, the FQDNNetworkPolicy in namespace.
// FQDN is considered active when fqdnVersion != "" and len(hosts) > 0.
func ReconcileSandboxEgressPolicies(
	ctx context.Context,
	c client.Client,
	fqdnVersion, namespace string,
	hosts []string,
	logger logr.Logger,
) error {
	fqdnActive := fqdnVersion != "" && len(hosts) > 0

	// 1. Reconcile baseline NetworkPolicy
	if err := ReconcileSandboxEgressNetworkPolicy(ctx, c, namespace, fqdnActive, logger); err != nil {
		return err
	}

	// 2. Reconcile FQDNNetworkPolicy (if supported by cluster)
	if fqdnVersion != "" {
		if err := ReconcileFQDNNetworkPolicy(ctx, c, fqdnVersion, namespace, hosts, logger); err != nil {
			return err
		}
	}

	return nil
}

// WatchSandboxEgressNetworkPolicy watches the baseline egress NetworkPolicy in namespace using the Kubernetes API.
// When the policy is modified or deleted, onEvent is invoked.
func WatchSandboxEgressNetworkPolicy(
	ctx context.Context,
	client kubernetes.Interface,
	namespace, name string,
	onEvent func(eventType watch.EventType, np *networkingv1.NetworkPolicy),
) error {
	if client == nil {
		return errors.New("kubernetes client is nil")
	}
	if name == "" {
		name = discovery.DefaultSandboxEgressNetworkPolicyName
	}

	fieldSelector := "metadata.name=" + name

	return sinkconfig.WatchNamedResource(ctx, sinkconfig.WatchNamedResourceOptions{
		Get: func(ctx context.Context) (metav1.Object, error) {
			return client.NetworkingV1().NetworkPolicies(namespace).Get(ctx, name, metav1.GetOptions{})
		},
		Watch: func(ctx context.Context, rv string) (watch.Interface, error) {
			return client.NetworkingV1().NetworkPolicies(namespace).Watch(ctx, metav1.ListOptions{
				FieldSelector:       fieldSelector,
				ResourceVersion:     rv,
				AllowWatchBookmarks: true,
			})
		},
		OnEvent: func(eventType watch.EventType, obj runtime.Object) {
			if onEvent == nil {
				return
			}
			if eventType == watch.Deleted || obj == nil {
				onEvent(watch.Deleted, nil)
				return
			}
			if np, ok := obj.(*networkingv1.NetworkPolicy); ok {
				onEvent(eventType, np)
			}
		},
	})
}

// WatchSandboxFQDNNetworkPolicy watches the sandbox FQDNNetworkPolicy in namespace using dynamic client.
// When the policy is modified or deleted, onEvent is invoked.
func WatchSandboxFQDNNetworkPolicy(
	ctx context.Context,
	dynClient dynamic.Interface,
	gvr schema.GroupVersionResource,
	namespace, name string,
	onEvent func(eventType watch.EventType, u *unstructured.Unstructured),
) error {
	if dynClient == nil {
		return errors.New("dynamic client is nil")
	}
	if name == "" {
		name = discovery.DefaultFQDNNetworkPolicyName
	}

	fieldSelector := "metadata.name=" + name

	return sinkconfig.WatchNamedResource(ctx, sinkconfig.WatchNamedResourceOptions{
		Get: func(ctx context.Context) (metav1.Object, error) {
			return dynClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		},
		Watch: func(ctx context.Context, rv string) (watch.Interface, error) {
			return dynClient.Resource(gvr).Namespace(namespace).Watch(ctx, metav1.ListOptions{
				FieldSelector:       fieldSelector,
				ResourceVersion:     rv,
				AllowWatchBookmarks: true,
			})
		},
		OnEvent: func(eventType watch.EventType, obj runtime.Object) {
			if onEvent == nil {
				return
			}
			if eventType == watch.Deleted || obj == nil {
				onEvent(watch.Deleted, nil)
				return
			}
			if u, ok := obj.(*unstructured.Unstructured); ok {
				onEvent(eventType, u)
			}
		},
	})
}

// SandboxEgressWatcherOptions holds configuration options for SandboxEgressWatcher.
type SandboxEgressWatcherOptions struct {
	Client           client.Client
	KubeClient       kubernetes.Interface
	DynamicClient    dynamic.Interface
	Namespace        string
	FQDNVersion      string
	Logger           logr.Logger
	ResyncPeriod     time.Duration
	MinWatchInterval time.Duration
	InitialHosts     []string
}

// SandboxEgressWatcher manages the reconciliation, watching, and automatic repair
// of the sandbox egress policies (baseline NetworkPolicy and FQDNNetworkPolicy).
type SandboxEgressWatcher struct {
	client           client.Client
	kubeClient       kubernetes.Interface
	dynamicClient    dynamic.Interface
	namespace        string
	fqdnVersion      string
	logger           logr.Logger
	resyncPeriod     time.Duration
	minWatchInterval time.Duration

	reconcileMu  sync.Mutex
	mu           sync.RWMutex
	currentHosts []string
	triggerCh    chan struct{}
}

// NewSandboxEgressWatcher creates a new SandboxEgressWatcher.
func NewSandboxEgressWatcher(opts SandboxEgressWatcherOptions) *SandboxEgressWatcher {
	period := opts.ResyncPeriod
	if period <= 0 {
		period = 1 * time.Minute
	}
	minInterval := opts.MinWatchInterval
	if minInterval <= 0 {
		minInterval = 5 * time.Second
	}
	return &SandboxEgressWatcher{
		client:           opts.Client,
		kubeClient:       opts.KubeClient,
		dynamicClient:    opts.DynamicClient,
		namespace:        opts.Namespace,
		fqdnVersion:      opts.FQDNVersion,
		logger:           opts.Logger,
		resyncPeriod:     period,
		minWatchInterval: minInterval,
		currentHosts:     discovery.CloneSandboxEgressHosts(opts.InitialHosts),
		triggerCh:        make(chan struct{}, 1),
	}
}

// Sync updates the allowed hosts and immediately reconciles both egress policies.
func (w *SandboxEgressWatcher) Sync(ctx context.Context, hosts []string) error {
	w.mu.Lock()
	w.currentHosts = discovery.CloneSandboxEgressHosts(hosts)
	w.mu.Unlock()

	return w.Reconcile(ctx)
}

// Trigger queues a reconciliation run.
func (w *SandboxEgressWatcher) Trigger() {
	select {
	case w.triggerCh <- struct{}{}:
	default:
	}
}

// Reconcile executes a full reconciliation of the egress policies using current configuration.
func (w *SandboxEgressWatcher) Reconcile(ctx context.Context) error {
	w.reconcileMu.Lock()
	defer w.reconcileMu.Unlock()

	w.mu.RLock()
	hosts := discovery.CloneSandboxEgressHosts(w.currentHosts)
	w.mu.RUnlock()

	return ReconcileSandboxEgressPolicies(ctx, w.client, w.fqdnVersion, w.namespace, hosts, w.logger)
}

// Start runs the periodic resync and watchers. It implements manager.Runnable.
func (w *SandboxEgressWatcher) Start(ctx context.Context) error {
	// 1. Initial reconcile
	if err := w.Reconcile(ctx); err != nil {
		w.logger.Error(err, "initial sandbox egress policies reconcile failed")
	}

	// 2. Start watcher goroutine for NetworkPolicy
	if w.kubeClient != nil {
		go func() {
			err := WatchSandboxEgressNetworkPolicy(ctx, w.kubeClient, w.namespace, discovery.DefaultSandboxEgressNetworkPolicyName, func(eventType watch.EventType, np *networkingv1.NetworkPolicy) {
				w.Trigger()
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				w.logger.Error(err, "sandbox egress NetworkPolicy watcher exited")
			}
		}()
	}

	// 3. Start watcher goroutine for FQDNNetworkPolicy (if supported)
	if w.fqdnVersion != "" && w.dynamicClient != nil {
		gv, err := schema.ParseGroupVersion(w.fqdnVersion)
		if err != nil {
			w.logger.Error(err, "invalid FQDN apiVersion; skipping FQDN watcher", "apiVersion", w.fqdnVersion)
		} else {
			gvr := schema.GroupVersionResource{
				Group:    gv.Group,
				Version:  gv.Version,
				Resource: discovery.FQDNNetworkPolicyResource,
			}
			go func() {
				err := WatchSandboxFQDNNetworkPolicy(ctx, w.dynamicClient, gvr, w.namespace, discovery.DefaultFQDNNetworkPolicyName, func(eventType watch.EventType, u *unstructured.Unstructured) {
					w.Trigger()
				})
				if err != nil && !errors.Is(err, context.Canceled) {
					w.logger.Error(err, "sandbox FQDNNetworkPolicy watcher exited")
				}
			}()
		}
	}

	// 4. Main reconcile loop driven by triggers (rate-limited) and periodic resync ticker
	ticker := time.NewTicker(w.resyncPeriod)
	defer ticker.Stop()

	var lastWatchReconcile time.Time

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-w.triggerCh:
			now := time.Now()
			if elapsed := now.Sub(lastWatchReconcile); elapsed < w.minWatchInterval {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(w.minWatchInterval - elapsed):
				}
			}
			lastWatchReconcile = time.Now()
			if err := w.Reconcile(ctx); err != nil {
				w.logger.Error(err, "error repairing sandbox egress policies on watch event")
			}
		case <-ticker.C:
			if err := w.Reconcile(ctx); err != nil {
				w.logger.Error(err, "error reconciling sandbox egress policies on periodic resync")
			}
		}
	}
}
