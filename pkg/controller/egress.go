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
	"reflect"
	"sync"
	"time"

	"github.com/gke-labs/eval-runner/pkg/discovery"
	"github.com/go-logr/logr"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

	if !reflect.DeepEqual(existing.Spec, desired.Spec) {
		existing.Spec = desired.Spec
		if updateErr := c.Update(ctx, existing); updateErr != nil {
			return fmt.Errorf("failed to update sandbox egress NetworkPolicy %s/%s: %w", namespace, name, updateErr)
		}
		logger.Info("updated sandbox egress NetworkPolicy",
			"namespace", namespace,
			"name", name,
			"fqdnActive", fqdnActive,
		)
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
	var rv string

	np, err := client.NetworkingV1().NetworkPolicies(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		rv = np.ResourceVersion
	} else if apierrors.IsNotFound(err) {
		rv = ""
	} else if ctx.Err() != nil {
		return ctx.Err()
	}

	backoff := 1 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		watcher, err := client.NetworkingV1().NetworkPolicies(namespace).Watch(ctx, metav1.ListOptions{
			FieldSelector:   fieldSelector,
			ResourceVersion: rv,
		})
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			np, err := client.NetworkingV1().NetworkPolicies(namespace).Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				rv = np.ResourceVersion
			} else if apierrors.IsNotFound(err) {
				rv = ""
				if onEvent != nil {
					onEvent(watch.Deleted, nil)
				}
			}
			continue
		}

		backoff = 1 * time.Second

		stopped := false
		for !stopped {
			select {
			case <-ctx.Done():
				watcher.Stop()
				return ctx.Err()
			case event, ok := <-watcher.ResultChan():
				if !ok {
					stopped = true
					break
				}
				switch event.Type {
				case watch.Added, watch.Modified:
					if updatedNP, ok := event.Object.(*networkingv1.NetworkPolicy); ok {
						if updatedNP.ResourceVersion != "" && updatedNP.ResourceVersion == rv {
							continue
						}
						rv = updatedNP.ResourceVersion
						if onEvent != nil {
							onEvent(event.Type, updatedNP)
						}
					}
				case watch.Deleted:
					rv = ""
					var deletedNP *networkingv1.NetworkPolicy
					if p, ok := event.Object.(*networkingv1.NetworkPolicy); ok {
						deletedNP = p
					}
					if onEvent != nil {
						onEvent(watch.Deleted, deletedNP)
					}
				}
			}
		}
	}
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
	var rv string

	u, err := dynClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		rv = u.GetResourceVersion()
	} else if apierrors.IsNotFound(err) {
		rv = ""
	} else if ctx.Err() != nil {
		return ctx.Err()
	}

	backoff := 1 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		watcher, err := dynClient.Resource(gvr).Namespace(namespace).Watch(ctx, metav1.ListOptions{
			FieldSelector:   fieldSelector,
			ResourceVersion: rv,
		})
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			u, err := dynClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				rv = u.GetResourceVersion()
			} else if apierrors.IsNotFound(err) {
				rv = ""
				if onEvent != nil {
					onEvent(watch.Deleted, nil)
				}
			}
			continue
		}

		backoff = 1 * time.Second

		stopped := false
		for !stopped {
			select {
			case <-ctx.Done():
				watcher.Stop()
				return ctx.Err()
			case event, ok := <-watcher.ResultChan():
				if !ok {
					stopped = true
					break
				}
				switch event.Type {
				case watch.Added, watch.Modified:
					if updatedU, ok := event.Object.(*unstructured.Unstructured); ok {
						if updatedU.GetResourceVersion() != "" && updatedU.GetResourceVersion() == rv {
							continue
						}
						rv = updatedU.GetResourceVersion()
						if onEvent != nil {
							onEvent(event.Type, updatedU)
						}
					}
				case watch.Deleted:
					rv = ""
					var deletedU *unstructured.Unstructured
					if u, ok := event.Object.(*unstructured.Unstructured); ok {
						deletedU = u
					}
					if onEvent != nil {
						onEvent(watch.Deleted, deletedU)
					}
				}
			}
		}
	}
}

// SandboxEgressWatcherOptions holds configuration options for SandboxEgressWatcher.
type SandboxEgressWatcherOptions struct {
	Client        client.Client
	KubeClient    kubernetes.Interface
	DynamicClient dynamic.Interface
	Namespace     string
	FQDNVersion   string
	Logger        logr.Logger
	ResyncPeriod  time.Duration
	InitialHosts  []string
}

// SandboxEgressWatcher manages the reconciliation, watching, and automatic repair
// of the sandbox egress policies (baseline NetworkPolicy and FQDNNetworkPolicy).
type SandboxEgressWatcher struct {
	client        client.Client
	kubeClient    kubernetes.Interface
	dynamicClient dynamic.Interface
	namespace     string
	fqdnVersion   string
	logger        logr.Logger
	resyncPeriod  time.Duration

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
	return &SandboxEgressWatcher{
		client:        opts.Client,
		kubeClient:    opts.KubeClient,
		dynamicClient: opts.DynamicClient,
		namespace:     opts.Namespace,
		fqdnVersion:   opts.FQDNVersion,
		logger:        opts.Logger,
		resyncPeriod:  period,
		currentHosts:  discovery.CloneSandboxEgressHosts(opts.InitialHosts),
		triggerCh:     make(chan struct{}, 1),
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
				if eventType == watch.Deleted {
					w.Trigger()
					return
				}
				if eventType == watch.Modified && np != nil {
					w.mu.RLock()
					hosts := w.currentHosts
					fqdnActive := w.fqdnVersion != "" && len(hosts) > 0
					w.mu.RUnlock()
					desired := discovery.RenderSandboxEgressNetworkPolicy(discovery.DefaultSandboxEgressNetworkPolicyName, w.namespace, fqdnActive)
					if !reflect.DeepEqual(np.Spec, desired.Spec) {
						w.Trigger()
					}
				}
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
					if eventType == watch.Deleted {
						w.mu.RLock()
						active := len(w.currentHosts) > 0
						w.mu.RUnlock()
						if active {
							w.Trigger()
						}
						return
					}
					if eventType == watch.Modified && u != nil {
						w.mu.RLock()
						hosts := w.currentHosts
						w.mu.RUnlock()
						if len(hosts) > 0 {
							desired := discovery.RenderFQDNNetworkPolicyWithVersion(w.fqdnVersion, discovery.DefaultFQDNNetworkPolicyName, w.namespace, hosts)
							if !reflect.DeepEqual(u.Object["spec"], desired.Object["spec"]) {
								w.Trigger()
							}
						}
					}
				})
				if err != nil && !errors.Is(err, context.Canceled) {
					w.logger.Error(err, "sandbox FQDNNetworkPolicy watcher exited")
				}
			}()
		}
	}

	// 4. Main reconcile loop driven by triggers and periodic resync ticker
	ticker := time.NewTicker(w.resyncPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-w.triggerCh:
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
