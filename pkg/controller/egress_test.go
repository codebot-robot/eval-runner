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
	"sync"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/pkg/discovery"
	"github.com/go-logr/logr"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/watch"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReconcileSandboxEgressNetworkPolicy_CreateUpdateNoop(t *testing.T) {
	ctx := t.Context()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("failed to add clientgo scheme: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(s).Build()
	logger := logr.Discard()
	ns := "eval-runner-sandbox"
	policyName := discovery.DefaultSandboxEgressNetworkPolicyName

	// 1. Initial reconcile with FQDN absent (fqdnActive = false): creates policy with 0.0.0.0/0:443
	if err := ReconcileSandboxEgressNetworkPolicy(ctx, c, ns, false, logger); err != nil {
		t.Fatalf("unexpected error creating policy: %v", err)
	}

	created := &networkingv1.NetworkPolicy{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: policyName}, created); err != nil {
		t.Fatalf("failed to get created NetworkPolicy: %v", err)
	}
	if len(created.Spec.Egress) != 3 {
		t.Fatalf("expected 3 egress rules when FQDN absent, got %d", len(created.Spec.Egress))
	}

	// Verify internet rule exists and contains 443
	has443 := false
	for _, rule := range created.Spec.Egress {
		for _, port := range rule.Ports {
			if port.Port != nil && port.Port.IntVal == 443 {
				has443 = true
			}
		}
	}
	if !has443 {
		t.Errorf("expected 0.0.0.0/0:443 rule in baseline when FQDN absent")
	}

	// 2. Reconcile with FQDN active (fqdnActive = true): updates policy to remove 0.0.0.0/0:443
	if err := ReconcileSandboxEgressNetworkPolicy(ctx, c, ns, true, logger); err != nil {
		t.Fatalf("unexpected error updating policy: %v", err)
	}

	updated := &networkingv1.NetworkPolicy{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: policyName}, updated); err != nil {
		t.Fatalf("failed to get updated NetworkPolicy: %v", err)
	}
	if len(updated.Spec.Egress) != 2 {
		t.Fatalf("expected 2 egress rules when FQDN active, got %d", len(updated.Spec.Egress))
	}
	for _, rule := range updated.Spec.Egress {
		for _, port := range rule.Ports {
			if port.Port != nil && port.Port.IntVal == 443 {
				t.Fatalf("found port 443 in baseline egress NetworkPolicy when FQDN active")
			}
		}
	}

	// 3. Idempotent reconcile: no-op
	if err := ReconcileSandboxEgressNetworkPolicy(ctx, c, ns, true, logger); err != nil {
		t.Fatalf("unexpected error on idempotent reconcile: %v", err)
	}
}

func TestReconcileSandboxEgressPolicies_FQDNActiveDisabledAbsent(t *testing.T) {
	ctx := t.Context()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("failed to add clientgo scheme: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(s).Build()
	logger := logr.Discard()
	ns := "eval-runner-sandbox"
	baseName := discovery.DefaultSandboxEgressNetworkPolicyName
	fqdnName := discovery.DefaultFQDNNetworkPolicyName
	fqdnVersion := discovery.FQDNNetworkPolicyAPIVersion

	// 1. FQDN active: non-empty hosts and fqdnVersion set
	hosts := []string{"github.com", "*.googleapis.com"}
	if err := ReconcileSandboxEgressPolicies(ctx, c, fqdnVersion, ns, hosts, logger); err != nil {
		t.Fatalf("unexpected error reconciling with FQDN active: %v", err)
	}

	// Baseline NetworkPolicy must NOT have 443 rule
	baseNP := &networkingv1.NetworkPolicy{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: baseName}, baseNP); err != nil {
		t.Fatalf("failed to get baseline NetworkPolicy: %v", err)
	}
	for _, rule := range baseNP.Spec.Egress {
		for _, port := range rule.Ports {
			if port.Port != nil && port.Port.IntVal == 443 {
				t.Errorf("expected no port 443 in baseline when FQDN active")
			}
		}
	}

	// FQDNNetworkPolicy must exist
	fqdnNP := &unstructured.Unstructured{}
	fqdnNP.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   discovery.FQDNNetworkPolicyGroup,
		Version: "v1alpha1",
		Kind:    discovery.FQDNNetworkPolicyKind,
	})
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: fqdnName}, fqdnNP); err != nil {
		t.Fatalf("failed to get created FQDNNetworkPolicy: %v", err)
	}

	// 2. FQDN disabled: hosts is empty (or "none")
	if err := ReconcileSandboxEgressPolicies(ctx, c, fqdnVersion, ns, nil, logger); err != nil {
		t.Fatalf("unexpected error reconciling with FQDN disabled: %v", err)
	}

	// Baseline NetworkPolicy must now HAVE 443 rule
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: baseName}, baseNP); err != nil {
		t.Fatalf("failed to get baseline NetworkPolicy: %v", err)
	}
	has443 := false
	for _, rule := range baseNP.Spec.Egress {
		for _, port := range rule.Ports {
			if port.Port != nil && port.Port.IntVal == 443 {
				has443 = true
			}
		}
	}
	if !has443 {
		t.Errorf("expected port 443 in baseline when FQDN disabled")
	}

	// FQDNNetworkPolicy must be deleted
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: fqdnName}, fqdnNP); err == nil {
		t.Fatalf("expected FQDNNetworkPolicy to be deleted when hosts is nil, but still exists")
	}

	// 3. FQDN absent: fqdnVersion is "" (e.g. kind cluster)
	if err := ReconcileSandboxEgressPolicies(ctx, c, "", ns, hosts, logger); err != nil {
		t.Fatalf("unexpected error reconciling with FQDN absent: %v", err)
	}

	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: baseName}, baseNP); err != nil {
		t.Fatalf("failed to get baseline NetworkPolicy: %v", err)
	}
	has443 = false
	for _, rule := range baseNP.Spec.Egress {
		for _, port := range rule.Ports {
			if port.Port != nil && port.Port.IntVal == 443 {
				has443 = true
			}
		}
	}
	if !has443 {
		t.Errorf("expected port 443 in baseline when FQDN CRD absent")
	}
}

func TestSandboxEgressWatcher_RepairOnWatchEventAndSync(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("failed to add clientgo scheme: %v", err)
	}

	ctrlClient := fake.NewClientBuilder().WithScheme(s).Build()
	logger := logr.Discard()
	ns := "eval-runner-sandbox"
	baseName := discovery.DefaultSandboxEgressNetworkPolicyName
	fqdnName := discovery.DefaultFQDNNetworkPolicyName
	fqdnVersion := discovery.FQDNNetworkPolicyAPIVersion

	// Setup fake kubernetes clientset with watch support
	kubeClientset := k8sfake.NewSimpleClientset()
	var watchReady sync.WaitGroup
	watchReady.Add(1)
	var watchOnce sync.Once

	kubeClientset.PrependWatchReactor("networkpolicies", func(action k8stesting.Action) (bool, watch.Interface, error) {
		w, err := kubeClientset.Tracker().Watch(action.GetResource(), action.GetNamespace())
		watchOnce.Do(func() { watchReady.Done() })
		return true, w, err
	})

	initialHosts := []string{"github.com", "*.googleapis.com"}
	watcher := NewSandboxEgressWatcher(SandboxEgressWatcherOptions{
		Client:       ctrlClient,
		KubeClient:   kubeClientset,
		Namespace:    ns,
		FQDNVersion:  fqdnVersion,
		Logger:       logger,
		ResyncPeriod: 100 * time.Millisecond,
		InitialHosts: initialHosts,
	})

	// Start watcher in background
	go func() {
		_ = watcher.Start(ctx)
	}()

	// Wait for initial reconcile
	var baseNP networkingv1.NetworkPolicy
	requireEventually(t, func() bool {
		err := ctrlClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: baseName}, &baseNP)
		return err == nil
	}, "waiting for initial NetworkPolicy creation")

	// 1. Test manual deletion repair: delete baseline NetworkPolicy
	if err := ctrlClient.Delete(ctx, &baseNP); err != nil {
		t.Fatalf("failed to delete NetworkPolicy: %v", err)
	}

	// Trigger watcher and wait for repair
	watcher.Trigger()
	requireEventually(t, func() bool {
		err := ctrlClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: baseName}, &baseNP)
		return err == nil
	}, "waiting for NetworkPolicy repair after deletion")

	// 2. Test manual modification repair: tamper with spec by adding an unauthorized rule
	unauthorizedPort := intstr.FromInt32(12345)
	baseNP.Spec.Egress = append(baseNP.Spec.Egress, networkingv1.NetworkPolicyEgressRule{
		Ports: []networkingv1.NetworkPolicyPort{
			{Port: &unauthorizedPort},
		},
	})
	if err := ctrlClient.Update(ctx, &baseNP); err != nil {
		t.Fatalf("failed to tamper with NetworkPolicy: %v", err)
	}

	// Trigger watcher and wait for repair (spec restored)
	watcher.Trigger()
	requireEventually(t, func() bool {
		if err := ctrlClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: baseName}, &baseNP); err != nil {
			return false
		}
		return len(baseNP.Spec.Egress) == 2 // restored to exact 2 rules
	}, "waiting for NetworkPolicy spec repair after tampering")

	// 3. Test Sync on ConfigMap update: change hosts to "none" (empty)
	if err := watcher.Sync(ctx, nil); err != nil {
		t.Fatalf("failed to sync watcher with empty hosts: %v", err)
	}

	// Baseline NetworkPolicy should now have 3 rules (including internet rule)
	requireEventually(t, func() bool {
		if err := ctrlClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: baseName}, &baseNP); err != nil {
			return false
		}
		return len(baseNP.Spec.Egress) == 3
	}, "waiting for NetworkPolicy to include internet rule when hosts disabled")

	// FQDNNetworkPolicy should be deleted
	fqdnNP := &unstructured.Unstructured{}
	fqdnNP.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   discovery.FQDNNetworkPolicyGroup,
		Version: "v1alpha1",
		Kind:    discovery.FQDNNetworkPolicyKind,
	})
	requireEventually(t, func() bool {
		err := ctrlClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: fqdnName}, fqdnNP)
		return err != nil // deleted
	}, "waiting for FQDNNetworkPolicy to be deleted when hosts disabled")
}

func requireEventually(t *testing.T, condition func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", msg)
}
