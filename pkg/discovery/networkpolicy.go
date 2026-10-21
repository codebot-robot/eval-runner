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

package discovery

import (
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryk8s "k8s.io/client-go/discovery"
	"sigs.k8s.io/yaml"
)

const (
	// DefaultFQDNNetworkPolicyName is the default name for the sandbox FQDNNetworkPolicy.
	DefaultFQDNNetworkPolicyName = "eval-runner-sandbox-fqdn-egress"

	// FQDNNetworkPolicyAPIVersion is the built-in GKE Dataplane V2 API version for FQDNNetworkPolicy (using matches[].name|pattern).
	FQDNNetworkPolicyAPIVersion = "networking.gke.io/v1alpha1"

	// FQDNNetworkPolicyAPIVersionV1Alpha3 is the API version used by the open-source gke-fqdnnetworkpolicies-golang operator
	// (using to[].fqdns), rather than the built-in GKE Dataplane V2 feature.
	FQDNNetworkPolicyAPIVersionV1Alpha3 = "networking.gke.io/v1alpha3"

	// FQDNNetworkPolicyKind is the resource kind for FQDNNetworkPolicy.
	FQDNNetworkPolicyKind = "FQDNNetworkPolicy"

	// FQDNNetworkPolicyGroup is the API group for GKE networking resources.
	FQDNNetworkPolicyGroup = "networking.gke.io"

	// FQDNNetworkPolicyResource is the plural resource name for FQDNNetworkPolicy.
	FQDNNetworkPolicyResource = "fqdnnetworkpolicies"
)

// DefaultSandboxEgressHosts defines the default allow-list of FQDN hostnames
// permitted for HTTPS egress from sandbox pods on GKE Dataplane V2:
// - Model APIs (*.googleapis.com)
// - Git hosts (github.com, *.github.com, *.githubusercontent.com)
// - Python package repositories (pypi.org, *.pypi.org, files.pythonhosted.org, *.pythonhosted.org)
var DefaultSandboxEgressHosts = []string{
	"*.googleapis.com",
	"github.com",
	"*.github.com",
	"*.githubusercontent.com",
	"pypi.org",
	"*.pypi.org",
	"files.pythonhosted.org",
	"*.pythonhosted.org",
}

// CloneSandboxEgressHosts returns a defensive copy of the provided hosts slice.
func CloneSandboxEgressHosts(hosts []string) []string {
	if hosts == nil {
		return nil
	}
	out := make([]string, len(hosts))
	copy(out, hosts)
	return out
}

// ParseHostList parses a comma-separated list of hostnames, trimming whitespace,
// dropping empty items, and deduplicating while preserving order.
func ParseHostList(s string) []string {
	var hosts []string
	seen := make(map[string]bool)
	for _, h := range strings.Split(s, ",") {
		trimmed := strings.TrimSpace(h)
		if trimmed != "" && !seen[trimmed] {
			seen[trimmed] = true
			hosts = append(hosts, trimmed)
		}
	}
	return hosts
}

// RenderFQDNNetworkPolicy renders an FQDNNetworkPolicy using the default GKE Dataplane V2 API version (v1alpha1).
func RenderFQDNNetworkPolicy(name, namespace string, hosts []string) *unstructured.Unstructured {
	return RenderFQDNNetworkPolicyWithVersion(FQDNNetworkPolicyAPIVersion, name, namespace, hosts)
}

// RenderFQDNNetworkPolicyWithVersion renders an FQDNNetworkPolicy unstructured object for the given API version,
// targeting sandbox pods (LabelSandbox: "true") in the specified namespace and allowing TCP 443 egress to the given hosts.
func RenderFQDNNetworkPolicyWithVersion(apiVersion, name, namespace string, hosts []string) *unstructured.Unstructured {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		gv = schema.GroupVersion{Group: FQDNNetworkPolicyGroup, Version: "v1alpha1"}
	}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gv.Group,
		Version: gv.Version,
		Kind:    FQDNNetworkPolicyKind,
	})
	u.SetName(name)
	u.SetNamespace(namespace)

	// Clean and deduplicate hosts
	var cleanHosts []string
	seen := make(map[string]bool)
	for _, h := range hosts {
		trimmed := strings.TrimSpace(h)
		if trimmed != "" && !seen[trimmed] {
			seen[trimmed] = true
			cleanHosts = append(cleanHosts, trimmed)
		}
	}

	podSelector := map[string]any{
		"matchLabels": map[string]any{
			LabelSandbox: "true",
		},
	}

	ports := []any{
		map[string]any{
			"protocol": "TCP",
			"port":     int64(443),
		},
	}

	var egress []any
	if len(cleanHosts) > 0 {
		if gv.Version == "v1alpha3" {
			// v1alpha3 spec uses egress[].to[].fqdns
			toFQDNs := make([]any, len(cleanHosts))
			for i, h := range cleanHosts {
				toFQDNs[i] = h
			}
			egress = []any{
				map[string]any{
					"to": []any{
						map[string]any{
							"fqdns": toFQDNs,
						},
					},
					"ports": ports,
				},
			}
		} else {
			// v1alpha1 (built-in GKE Dataplane V2) spec uses egress[].matches[].name / pattern
			matches := make([]any, len(cleanHosts))
			for i, h := range cleanHosts {
				if strings.Contains(h, "*") {
					matches[i] = map[string]any{
						"pattern": h,
					}
				} else {
					matches[i] = map[string]any{
						"name": h,
					}
				}
			}
			egress = []any{
				map[string]any{
					"matches": matches,
					"ports":   ports,
				},
			}
		}
	}

	spec := map[string]any{
		"podSelector": podSelector,
	}
	if len(egress) > 0 {
		spec["egress"] = egress
	}

	u.Object["spec"] = spec
	return u
}

// RenderFQDNNetworkPolicyYAML renders the policy and returns its YAML string representation.
func RenderFQDNNetworkPolicyYAML(apiVersion, name, namespace string, hosts []string) (string, error) {
	u := RenderFQDNNetworkPolicyWithVersion(apiVersion, name, namespace, hosts)
	data, err := yaml.Marshal(u.Object)
	if err != nil {
		return "", fmt.Errorf("failed to marshal FQDNNetworkPolicy to YAML: %w", err)
	}
	return string(data), nil
}

// DetectFQDNNetworkPolicyAPIVersion queries discoveryClient to check if FQDNNetworkPolicy CRD
// is registered in the cluster. It checks networking.gke.io/v1alpha1 first, then networking.gke.io/v1alpha3.
// Returns the detected apiVersion and true if found, or ("", false, nil) if not found.
func DetectFQDNNetworkPolicyAPIVersion(discoveryClient discoveryk8s.DiscoveryInterface) (string, bool, error) {
	if discoveryClient == nil {
		return "", false, nil
	}

	candidateVersions := []string{
		FQDNNetworkPolicyAPIVersion,
		FQDNNetworkPolicyAPIVersionV1Alpha3,
	}

	for _, gv := range candidateVersions {
		resources, err := discoveryClient.ServerResourcesForGroupVersion(gv)
		if err != nil {
			if apierrors.IsNotFound(err) || discoveryk8s.IsGroupDiscoveryFailedError(err) {
				// Group or version not found is expected on clusters without FQDN support
				continue
			}
			return "", false, fmt.Errorf("failed to query discovery for %s: %w", gv, err)
		}
		for _, r := range resources.APIResources {
			if r.Kind == FQDNNetworkPolicyKind {
				return gv, true, nil
			}
		}
	}

	return "", false, nil
}
