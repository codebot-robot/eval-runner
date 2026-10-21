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
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/gke-labs/eval-runner/pkg/discovery"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReconcileFQDNNetworkPolicy_CreateUpdateDelete(t *testing.T) {
	ctx := t.Context()
	s := runtime.NewScheme()

	client := fake.NewClientBuilder().WithScheme(s).Build()
	logger := logr.Discard()
	ns := "eval-runner-sandbox"
	policyName := discovery.DefaultFQDNNetworkPolicyName
	apiVersion := discovery.FQDNNetworkPolicyAPIVersion

	// 1. Initial reconcile: creates the policy
	initialHosts := []string{"github.com", "*.googleapis.com"}
	err := ReconcileFQDNNetworkPolicy(ctx, client, apiVersion, ns, initialHosts, logger)
	if err != nil {
		t.Fatalf("unexpected error creating policy: %v", err)
	}

	created := &unstructured.Unstructured{}
	created.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   discovery.FQDNNetworkPolicyGroup,
		Version: "v1alpha1",
		Kind:    discovery.FQDNNetworkPolicyKind,
	})
	err = client.Get(ctx, types.NamespacedName{Namespace: ns, Name: policyName}, created)
	if err != nil {
		t.Fatalf("failed to get created FQDNNetworkPolicy: %v", err)
	}

	spec := created.Object["spec"].(map[string]any)
	egress := spec["egress"].([]any)
	rule := egress[0].(map[string]any)
	matches := rule["matches"].([]any)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}

	// 2. Update with new hosts
	updatedHosts := []string{"github.com", "*.googleapis.com", "api.openai.com"}
	err = ReconcileFQDNNetworkPolicy(ctx, client, apiVersion, ns, updatedHosts, logger)
	if err != nil {
		t.Fatalf("unexpected error updating policy: %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   discovery.FQDNNetworkPolicyGroup,
		Version: "v1alpha1",
		Kind:    discovery.FQDNNetworkPolicyKind,
	})
	err = client.Get(ctx, types.NamespacedName{Namespace: ns, Name: policyName}, updated)
	if err != nil {
		t.Fatalf("failed to get updated FQDNNetworkPolicy: %v", err)
	}
	updatedMatches := updated.Object["spec"].(map[string]any)["egress"].([]any)[0].(map[string]any)["matches"].([]any)
	if len(updatedMatches) != 3 {
		t.Fatalf("expected 3 matches after update, got %d", len(updatedMatches))
	}

	// 3. No-op reconcile when hosts unchanged
	err = ReconcileFQDNNetworkPolicy(ctx, client, apiVersion, ns, updatedHosts, logger)
	if err != nil {
		t.Fatalf("unexpected error on idempotent reconcile: %v", err)
	}

	// 4. Delete when hosts is empty
	err = ReconcileFQDNNetworkPolicy(ctx, client, apiVersion, ns, nil, logger)
	if err != nil {
		t.Fatalf("unexpected error deleting policy: %v", err)
	}

	deleted := &unstructured.Unstructured{}
	deleted.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   discovery.FQDNNetworkPolicyGroup,
		Version: "v1alpha1",
		Kind:    discovery.FQDNNetworkPolicyKind,
	})
	err = client.Get(ctx, types.NamespacedName{Namespace: ns, Name: policyName}, deleted)
	if err == nil {
		t.Fatalf("expected policy to be deleted, but still found")
	}

	// 5. Empty apiVersion is a no-op
	err = ReconcileFQDNNetworkPolicy(ctx, client, "", ns, initialHosts, logger)
	if err != nil {
		t.Errorf("expected nil error for empty apiVersion, got %v", err)
	}
}

func TestConfigReloader_FQDNSyncFuncCalledOnConfigMapUpdate(t *testing.T) {
	ctx := t.Context()
	logger := logr.Discard()

	initialCfg := DefaultClusterConfig()
	initialCfg.SandboxEgressHosts = []string{"github.com"}

	var fqdnCalls atomic.Int32
	var lastHosts []string

	reloader := NewConfigReloader(ConfigReloaderOptions{
		Logger:        logger,
		InitialConfig: &initialCfg,
		FQDNSyncFunc: func(ctx context.Context, hosts []string) error {
			fqdnCalls.Add(1)
			lastHosts = hosts
			return nil
		},
	})

	// 1. Reload with same config -> FQDNSyncFunc should not be called
	cmSame := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigMapName, Namespace: "eval-runner"},
		Data: map[string]string{
			ConfigMapKeySandboxEgressHosts: "github.com",
		},
	}
	if err := reloader.Reload(ctx, cmSame); err != nil {
		t.Fatalf("unexpected reload error: %v", err)
	}
	if fqdnCalls.Load() != 0 {
		t.Errorf("expected 0 FQDN sync calls when hosts unchanged, got %d", fqdnCalls.Load())
	}

	// 2. Reload with updated hosts -> FQDNSyncFunc should be called
	cmUpdated := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigMapName, Namespace: "eval-runner"},
		Data: map[string]string{
			ConfigMapKeySandboxEgressHosts: "github.com, *.googleapis.com",
		},
	}
	if err := reloader.Reload(ctx, cmUpdated); err != nil {
		t.Fatalf("unexpected reload error: %v", err)
	}
	if fqdnCalls.Load() != 1 {
		t.Errorf("expected 1 FQDN sync call when hosts changed, got %d", fqdnCalls.Load())
	}
	expectedHosts := []string{"github.com", "*.googleapis.com"}
	if !reflect.DeepEqual(lastHosts, expectedHosts) {
		t.Errorf("expected hosts %v, got %v", expectedHosts, lastHosts)
	}
}
