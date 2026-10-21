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

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	discoveryk8s "k8s.io/client-go/discovery"
	"sigs.k8s.io/yaml"
)

const (
	// DefaultSandboxEgressNetworkPolicyName is the default name for the sandbox baseline egress NetworkPolicy.
	DefaultSandboxEgressNetworkPolicyName = "eval-runner-sandbox-egress"

	// DefaultSandboxIngressNetworkPolicyName is the default name for the sandbox ingress NetworkPolicy.
	DefaultSandboxIngressNetworkPolicyName = "eval-runner-sandbox-ingress"

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

// RenderSandboxEgressNetworkPolicy renders the baseline NetworkPolicy for sandbox egress.
// When fqdnActive is false (e.g. non-Dataplane V2 clusters, kind, or when egress hosts is "none"),
// it allows DNS, in-cluster git fixture on 9418, and internet egress on 80, 443, 9418.
// When fqdnActive is true (FQDNNetworkPolicy is active), the internet rule allows only
// ports not covered by hostnames (none by default; keep 9418 to the fixture), removing
// the 0.0.0.0/0:443 and :80 rules so that HTTPS/HTTP egress is narrowed to the declared hosts.
func RenderSandboxEgressNetworkPolicy(name, namespace string, fqdnActive bool, extraInternetPorts ...int32) *networkingv1.NetworkPolicy {
	udpProto := corev1.ProtocolUDP
	tcpProto := corev1.ProtocolTCP

	dnsRule := networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"kubernetes.io/metadata.name": "kube-system",
					},
				},
				PodSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "k8s-app",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"kube-dns", "node-local-dns"},
						},
					},
				},
			},
			{
				IPBlock: &networkingv1.IPBlock{
					CIDR: "169.254.20.10/32",
				},
			},
			{
				IPBlock: &networkingv1.IPBlock{
					CIDR: "169.254.169.254/32",
				},
			},
		},
		Ports: []networkingv1.NetworkPolicyPort{
			{
				Protocol: &udpProto,
				Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
			},
			{
				Protocol: &tcpProto,
				Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
			},
		},
	}

	gitFixtureRule := networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": "git-server",
					},
				},
			},
		},
		Ports: []networkingv1.NetworkPolicyPort{
			{
				Protocol: &tcpProto,
				Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 9418},
			},
		},
	}

	var egressRules []networkingv1.NetworkPolicyEgressRule
	egressRules = append(egressRules, dnsRule)

	// Internet rule:
	// When fqdnActive is false, allow 80, 443, 9418.
	// When fqdnActive is true, allow only extraInternetPorts (none by default).
	var internetPorts []int32
	if !fqdnActive {
		internetPorts = []int32{80, 443, 9418}
	}
	for _, p := range extraInternetPorts {
		found := false
		for _, existing := range internetPorts {
			if existing == p {
				found = true
				break
			}
		}
		if !found {
			internetPorts = append(internetPorts, p)
		}
	}

	if len(internetPorts) > 0 {
		var ports []networkingv1.NetworkPolicyPort
		for _, p := range internetPorts {
			portVal := p
			ports = append(ports, networkingv1.NetworkPolicyPort{
				Protocol: &tcpProto,
				Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: portVal},
			})
		}

		internetRule := networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{
				{
					IPBlock: &networkingv1.IPBlock{
						CIDR: "0.0.0.0/0",
						Except: []string{
							"10.0.0.0/8",
							"172.16.0.0/12",
							"192.168.0.0/16",
							"100.64.0.0/10",
							"169.254.0.0/16",
						},
					},
				},
			},
			Ports: ports,
		}
		egressRules = append(egressRules, internetRule)
	}

	egressRules = append(egressRules, gitFixtureRule)

	return &networkingv1.NetworkPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.k8s.io/v1",
			Kind:       "NetworkPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					LabelSandbox: "true",
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeEgress,
			},
			Egress: egressRules,
		},
	}
}

// RenderSandboxEgressNetworkPolicyYAML renders the baseline egress policy and returns its YAML string representation.
func RenderSandboxEgressNetworkPolicyYAML(name, namespace string, fqdnActive bool, extraInternetPorts ...int32) (string, error) {
	np := RenderSandboxEgressNetworkPolicy(name, namespace, fqdnActive, extraInternetPorts...)
	data, err := yaml.Marshal(np)
	if err != nil {
		return "", fmt.Errorf("failed to marshal NetworkPolicy to YAML: %w", err)
	}
	return string(data), nil
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
