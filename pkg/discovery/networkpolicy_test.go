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

// Package discovery hosts this NetworkPolicy test because LabelSandbox
// (evalrunner.gke-labs.dev/sandbox) is defined in this package.
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
		if len(egressPolicy.Spec.Egress) != 3 {
			t.Fatalf("expected 3 egress rules (DNS, Internet HTTP/Git, In-cluster Git fixture), got %d", len(egressPolicy.Spec.Egress))
		}

		// Verify that no egress rule contains a namespaceSelector with NotIn or an empty podSelector: {}
		for i, rule := range egressPolicy.Spec.Egress {
			for j, to := range rule.To {
				if to.NamespaceSelector != nil {
					for _, req := range to.NamespaceSelector.MatchExpressions {
						if req.Operator == metav1.LabelSelectorOpNotIn {
							t.Errorf("rule %d peer %d: namespaceSelector must not use NotIn operator", i, j)
						}
					}
				}
				if to.PodSelector != nil && len(to.PodSelector.MatchLabels) == 0 && len(to.PodSelector.MatchExpressions) == 0 {
					t.Errorf("rule %d peer %d: podSelector must not be empty {}", i, j)
				}
			}
		}

		// Rule 1: DNS
		dnsRule := egressPolicy.Spec.Egress[0]
		hasKubeSystemDNSPods := false
		hasNodeLocalIP := false
		hasCloudDNSIP := false

		for _, to := range dnsRule.To {
			if to.NamespaceSelector != nil && to.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == "kube-system" {
				if to.PodSelector != nil {
					for _, expr := range to.PodSelector.MatchExpressions {
						if expr.Key == "k8s-app" && expr.Operator == metav1.LabelSelectorOpIn {
							vals := map[string]bool{}
							for _, v := range expr.Values {
								vals[v] = true
							}
							if vals["kube-dns"] && vals["node-local-dns"] {
								hasKubeSystemDNSPods = true
							}
						}
					}
				}
			}
			if to.IPBlock != nil {
				if to.IPBlock.CIDR == "169.254.20.10/32" {
					hasNodeLocalIP = true
				}
				if to.IPBlock.CIDR == "169.254.169.254/32" {
					hasCloudDNSIP = true
				}
			}
		}

		if !hasKubeSystemDNSPods {
			t.Errorf("expected DNS to kube-system pods with k8s-app In [kube-dns, node-local-dns]")
		}
		if !hasNodeLocalIP {
			t.Errorf("expected DNS to ipBlock 169.254.20.10/32 (NodeLocal DNSCache)")
		}
		if !hasCloudDNSIP {
			t.Errorf("expected DNS to ipBlock 169.254.169.254/32 (Cloud DNS metadata data plane)")
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

		// Rule 2: Internet Git / HTTP / HTTPS
		internetRule := egressPolicy.Spec.Egress[1]
		if len(internetRule.To) != 1 {
			t.Fatalf("expected exactly 1 peer in internet rule, got %d", len(internetRule.To))
		}
		toPeer := internetRule.To[0]
		if toPeer.IPBlock == nil || toPeer.IPBlock.CIDR != "0.0.0.0/0" {
			t.Fatalf("expected ipBlock 0.0.0.0/0 in internet rule, got %v", toPeer.IPBlock)
		}
		if toPeer.NamespaceSelector != nil || toPeer.PodSelector != nil {
			t.Errorf("internet rule peer must only be an ipBlock; got namespaceSelector=%v, podSelector=%v",
				toPeer.NamespaceSelector, toPeer.PodSelector)
		}

		exceptMap := make(map[string]bool)
		for _, exc := range toPeer.IPBlock.Except {
			exceptMap[exc] = true
		}
		expectedExceptions := []string{
			"10.0.0.0/8",
			"172.16.0.0/12",
			"192.168.0.0/16",
			"100.64.0.0/10",
			"169.254.0.0/16",
		}
		for _, expectedCIDR := range expectedExceptions {
			if !exceptMap[expectedCIDR] {
				t.Errorf("expected internet rule ipBlock.except to contain %s, got %v", expectedCIDR, toPeer.IPBlock.Except)
			}
		}

		expectedInternetPorts := map[int32]bool{80: false, 443: false, 9418: false}
		for _, port := range internetRule.Ports {
			if port.Protocol == nil || *port.Protocol != corev1.ProtocolTCP {
				t.Errorf("expected TCP protocol, got %v", port.Protocol)
			}
			if port.Port != nil {
				expectedInternetPorts[port.Port.IntVal] = true
			}
		}
		for portNum, found := range expectedInternetPorts {
			if !found {
				t.Errorf("missing expected internet egress port %d", portNum)
			}
		}

		// Rule 3: In-cluster Git Fixture
		gitFixtureRule := egressPolicy.Spec.Egress[2]
		if len(gitFixtureRule.To) != 1 {
			t.Fatalf("expected exactly 1 peer in git fixture rule, got %d", len(gitFixtureRule.To))
		}
		gitPeer := gitFixtureRule.To[0]
		if gitPeer.PodSelector == nil || gitPeer.PodSelector.MatchLabels["app"] != "git-server" {
			t.Errorf("expected podSelector app=git-server in git fixture rule, got %v", gitPeer.PodSelector)
		}
		if gitPeer.NamespaceSelector != nil || gitPeer.IPBlock != nil {
			t.Errorf("git fixture rule must only use podSelector; got namespaceSelector=%v, ipBlock=%v",
				gitPeer.NamespaceSelector, gitPeer.IPBlock)
		}

		if len(gitFixtureRule.Ports) != 1 {
			t.Fatalf("expected 1 port in git fixture rule, got %d", len(gitFixtureRule.Ports))
		}
		gitPort := gitFixtureRule.Ports[0]
		if gitPort.Protocol == nil || *gitPort.Protocol != corev1.ProtocolTCP {
			t.Errorf("expected TCP protocol for git fixture, got %v", gitPort.Protocol)
		}
		if gitPort.Port == nil || gitPort.Port.IntVal != 9418 {
			t.Errorf("expected port 9418 for git fixture, got %v", gitPort.Port)
		}
	})
}
