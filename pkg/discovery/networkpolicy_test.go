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
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd(): %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
}

func loadNetworkPolicies(t *testing.T) []networkingv1.NetworkPolicy {
	t.Helper()
	root := findRepoRoot(t)
	manifestPath := filepath.Join(root, "k8s", "sandbox-networkpolicy.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", manifestPath, err)
	}

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var policies []networkingv1.NetworkPolicy
	for {
		var np networkingv1.NetworkPolicy
		err := decoder.Decode(&np)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to decode NetworkPolicy from %s: %v", manifestPath, err)
		}
		if np.Kind == "NetworkPolicy" {
			policies = append(policies, np)
		}
	}
	return policies
}

func TestSandboxNetworkPolicies(t *testing.T) {
	policies := loadNetworkPolicies(t)
	if len(policies) != 2 {
		t.Fatalf("expected 2 NetworkPolicies in k8s/sandbox-networkpolicy.yaml, got %d", len(policies))
	}

	var ingressPolicy, egressPolicy *networkingv1.NetworkPolicy
	for i := range policies {
		p := &policies[i]
		switch p.Name {
		case "eval-runner-sandbox-ingress":
			ingressPolicy = p
		case "eval-runner-sandbox-egress":
			egressPolicy = p
		default:
			t.Errorf("unexpected NetworkPolicy name: %s", p.Name)
		}
	}

	if ingressPolicy == nil {
		t.Fatal("missing eval-runner-sandbox-ingress policy")
	}
	if egressPolicy == nil {
		t.Fatal("missing eval-runner-sandbox-egress policy")
	}

	// Verify namespace and podSelector for both
	for _, p := range []*networkingv1.NetworkPolicy{ingressPolicy, egressPolicy} {
		if p.Namespace != "eval-runner-sandbox" {
			t.Errorf("%s: expected namespace eval-runner-sandbox, got %q", p.Name, p.Namespace)
		}
		if p.Spec.PodSelector.MatchLabels[LabelSandbox] != "true" {
			t.Errorf("%s: expected podSelector %s=true, got %v", p.Name, LabelSandbox, p.Spec.PodSelector.MatchLabels)
		}
	}

	// Assert Ingress policy
	t.Run("IngressPolicy", func(t *testing.T) {
		if len(ingressPolicy.Spec.PolicyTypes) != 1 || ingressPolicy.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
			t.Fatalf("expected policyTypes [Ingress], got %v", ingressPolicy.Spec.PolicyTypes)
		}
		if len(ingressPolicy.Spec.Ingress) != 1 {
			t.Fatalf("expected 1 ingress rule, got %d", len(ingressPolicy.Spec.Ingress))
		}
		rule := ingressPolicy.Spec.Ingress[0]
		if len(rule.From) != 1 {
			t.Fatalf("expected 1 from peer, got %d", len(rule.From))
		}
		from := rule.From[0]
		if from.NamespaceSelector == nil || from.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "eval-runner" {
			t.Errorf("expected ingress from namespace eval-runner, got %v", from.NamespaceSelector)
		}
		if from.PodSelector == nil || from.PodSelector.MatchLabels["app"] != "eval-runner-controller" {
			t.Errorf("expected ingress from pod app=eval-runner-controller, got %v", from.PodSelector)
		}
		expectedPorts := map[int32]bool{8080: false, 9090: false}
		for _, port := range rule.Ports {
			if port.Protocol == nil || *port.Protocol != corev1.ProtocolTCP {
				t.Errorf("expected TCP protocol, got %v", port.Protocol)
			}
			if port.Port != nil {
				expectedPorts[port.Port.IntVal] = true
			}
		}
		for portNum, found := range expectedPorts {
			if !found {
				t.Errorf("missing expected ingress port %d", portNum)
			}
		}
	})

	// Assert Egress policy
	t.Run("EgressPolicy", func(t *testing.T) {
		if len(egressPolicy.Spec.PolicyTypes) != 1 || egressPolicy.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
			t.Fatalf("expected policyTypes [Egress], got %v", egressPolicy.Spec.PolicyTypes)
		}
		if len(egressPolicy.Spec.Egress) != 2 {
			t.Fatalf("expected 2 egress rules (DNS and Git/HTTPS), got %d", len(egressPolicy.Spec.Egress))
		}

		// Rule 1: DNS
		dnsRule := egressPolicy.Spec.Egress[0]
		if len(dnsRule.To) != 1 {
			t.Fatalf("expected 1 to peer for DNS rule, got %d", len(dnsRule.To))
		}
		dnsTo := dnsRule.To[0]
		if dnsTo.NamespaceSelector == nil || dnsTo.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" {
			t.Errorf("expected DNS to namespace kube-system, got %v", dnsTo.NamespaceSelector)
		}
		if dnsTo.PodSelector == nil || dnsTo.PodSelector.MatchLabels["k8s-app"] != "kube-dns" {
			t.Errorf("expected DNS to pod k8s-app=kube-dns, got %v", dnsTo.PodSelector)
		}
		hasUDP53 := false
		hasTCP53 := false
		for _, port := range dnsRule.Ports {
			if port.Port != nil && port.Port.IntVal == 53 {
				if port.Protocol != nil && *port.Protocol == corev1.ProtocolUDP {
					hasUDP53 = true
				}
				if port.Protocol != nil && *port.Protocol == corev1.ProtocolTCP {
					hasTCP53 = true
				}
			}
		}
		if !hasUDP53 {
			t.Errorf("expected UDP port 53 in DNS egress rule")
		}
		if !hasTCP53 {
			t.Errorf("expected TCP port 53 in DNS egress rule")
		}

		// Rule 2: Git / HTTP / HTTPS
		gitRule := egressPolicy.Spec.Egress[1]
		hasIPBlock := false
		hasExcludedEvalRunner := false
		hasIntraNamespace := false

		for _, to := range gitRule.To {
			if to.IPBlock != nil {
				if to.IPBlock.CIDR == "0.0.0.0/0" {
					hasIPBlock = true
					// Check for RFC1918 exceptions
					exceptMap := make(map[string]bool)
					for _, exc := range to.IPBlock.Except {
						exceptMap[exc] = true
					}
					for _, expectedCIDR := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
						if !exceptMap[expectedCIDR] {
							t.Errorf("expected ipBlock.except to contain %s, got %v", expectedCIDR, to.IPBlock.Except)
						}
					}
				}
			}
			if to.NamespaceSelector != nil {
				for _, req := range to.NamespaceSelector.MatchExpressions {
					if req.Key == "kubernetes.io/metadata.name" && req.Operator == metav1.LabelSelectorOpNotIn {
						for _, val := range req.Values {
							if val == "eval-runner" {
								hasExcludedEvalRunner = true
							}
						}
					}
				}
			}
			if to.PodSelector != nil && to.NamespaceSelector == nil && to.IPBlock == nil {
				hasIntraNamespace = true
			}
		}

		if !hasIPBlock {
			t.Errorf("expected ipBlock 0.0.0.0/0 with RFC1918 exceptions in Git/HTTPS rule")
		}
		if !hasExcludedEvalRunner {
			t.Errorf("expected namespaceSelector with NotIn [eval-runner] in Git/HTTPS rule")
		}
		if !hasIntraNamespace {
			t.Errorf("expected intra-namespace podSelector in Git/HTTPS rule")
		}

		expectedGitPorts := map[int32]bool{80: false, 443: false, 9418: false}
		for _, port := range gitRule.Ports {
			if port.Protocol == nil || *port.Protocol != corev1.ProtocolTCP {
				t.Errorf("expected TCP protocol, got %v", port.Protocol)
			}
			if port.Port != nil {
				expectedGitPorts[port.Port.IntVal] = true
			}
		}
		for portNum, found := range expectedGitPorts {
			if !found {
				t.Errorf("missing expected egress port %d", portNum)
			}
		}
	})
}
