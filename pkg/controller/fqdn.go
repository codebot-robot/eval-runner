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
	"reflect"

	"github.com/gke-labs/eval-runner/pkg/discovery"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// fqdnSpecMatches checks if existing matches desired with subset semantics on maps
// and exact equality on owned slices, ensuring extra server-defaulted map fields
// do not trigger updates while appended ports or peers are repaired.
func fqdnSpecMatches(desired, existing any) bool {
	if dMap, ok := desired.(map[string]any); ok {
		eMap, ok := existing.(map[string]any)
		if !ok {
			return false
		}
		for k, dVal := range dMap {
			eVal, exists := eMap[k]
			if !exists {
				return false
			}
			if !fqdnSpecMatches(dVal, eVal) {
				return false
			}
		}
		return true
	}

	if dSlice, ok := desired.([]any); ok {
		eSlice, ok := existing.([]any)
		if !ok {
			return false
		}
		if len(dSlice) != len(eSlice) {
			return false
		}
		for i := range dSlice {
			if !fqdnSpecMatches(dSlice[i], eSlice[i]) {
				return false
			}
		}
		return true
	}

	if dNum, ok := toInt64(desired); ok {
		if eNum, ok := toInt64(existing); ok {
			return dNum == eNum
		}
	}

	return reflect.DeepEqual(desired, existing)
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
	}
	return 0, false
}

// ReconcileFQDNNetworkPolicy reconciles the sandbox FQDNNetworkPolicy in the target namespace.
// If the FQDNNetworkPolicy CRD is not present in the cluster (e.g. non-GKE or kind clusters),
// it gracefully returns nil without failing.
// If hosts is empty (or disabled), any existing FQDNNetworkPolicy is removed.
// Otherwise, the FQDNNetworkPolicy is created or updated to allow HTTPS egress to the specified hosts.
func ReconcileFQDNNetworkPolicy(
	ctx context.Context,
	c client.Client,
	apiVersion, namespace string,
	hosts []string,
	logger logr.Logger,
) error {
	if apiVersion == "" {
		return nil
	}

	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return fmt.Errorf("invalid FQDNNetworkPolicy apiVersion %q: %w", apiVersion, err)
	}

	name := discovery.DefaultFQDNNetworkPolicyName

	// If hosts is empty, remove the policy if it exists.
	if len(hosts) == 0 {
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   gv.Group,
			Version: gv.Version,
			Kind:    discovery.FQDNNetworkPolicyKind,
		})
		err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing)
		if err == nil {
			if delErr := c.Delete(ctx, existing); delErr != nil && !apierrors.IsNotFound(delErr) {
				return fmt.Errorf("failed to delete FQDNNetworkPolicy %s/%s: %w", namespace, name, delErr)
			}
			logger.Info("deleted FQDNNetworkPolicy as egress hosts are disabled", "namespace", namespace, "name", name)
		} else if meta.IsNoMatchError(err) {
			return nil
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to check existing FQDNNetworkPolicy %s/%s: %w", namespace, name, err)
		}
		return nil
	}

	desired := discovery.RenderFQDNNetworkPolicyWithVersion(apiVersion, name, namespace, hosts)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   gv.Group,
			Version: gv.Version,
			Kind:    discovery.FQDNNetworkPolicyKind,
		})

		err = c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, existing)
		if meta.IsNoMatchError(err) {
			logger.Info("FQDNNetworkPolicy CRD not found; skipping reconcile (GKE Dataplane V2 only)")
			return nil
		}

		if apierrors.IsNotFound(err) {
			if createErr := c.Create(ctx, desired); createErr != nil {
				if meta.IsNoMatchError(createErr) {
					logger.Info("FQDNNetworkPolicy CRD not found on create; skipping (GKE Dataplane V2 only)")
					return nil
				}
				if apierrors.IsAlreadyExists(createErr) {
					return apierrors.NewConflict(schema.GroupResource{Group: gv.Group, Resource: discovery.FQDNNetworkPolicyResource}, name, createErr)
				}
				return fmt.Errorf("failed to create FQDNNetworkPolicy %s/%s: %w", namespace, name, createErr)
			}
			logger.Info("created FQDNNetworkPolicy for sandbox egress narrowing",
				"namespace", namespace,
				"name", name,
				"apiVersion", apiVersion,
				"hosts", hosts,
			)
			return nil
		} else if err != nil {
			return fmt.Errorf("failed to get FQDNNetworkPolicy %s/%s: %w", namespace, name, err)
		}

		// Compare spec with subset semantics on maps and exact equality on owned slices
		desiredSpec := desired.Object["spec"]
		if !fqdnSpecMatches(desiredSpec, existing.Object["spec"]) {
			existing.Object["spec"] = desiredSpec
			if updateErr := c.Update(ctx, existing); updateErr != nil {
				return updateErr
			}
			logger.Info("updated FQDNNetworkPolicy for sandbox egress narrowing",
				"namespace", namespace,
				"name", name,
				"apiVersion", apiVersion,
				"hosts", hosts,
			)
		}
		return nil
	})
}
