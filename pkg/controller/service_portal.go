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
	"strings"

	portalsv1 "github.com/gke-labs/service-portals/pkg/portals/v1"
	"k8s.io/apimachinery/pkg/types"
)

// DefaultServicePortalAllowedHosts defines the default allowed hostnames for task sandboxes behind the service portal:
// - Model APIs (*.googleapis.com)
// - Git hosts (github.com, *.github.com, *.githubusercontent.com)
// - Python package repositories (pypi.org, *.pypi.org, files.pythonhosted.org, *.pythonhosted.org)
// - Tool and package distributors for Terminal-Bench tasks (astral.sh, *.astral.sh, deb.debian.org, security.debian.org, *.debian.org, archive.ubuntu.com, security.ubuntu.com, ports.ubuntu.com, *.ubuntu.com)
var DefaultServicePortalAllowedHosts = []string{
	"*.googleapis.com",
	"github.com",
	"*.github.com",
	"*.githubusercontent.com",
	"pypi.org",
	"*.pypi.org",
	"files.pythonhosted.org",
	"*.pythonhosted.org",
	"astral.sh",
	"*.astral.sh",
	"deb.debian.org",
	"security.debian.org",
	"*.debian.org",
	"archive.ubuntu.com",
	"security.ubuntu.com",
	"ports.ubuntu.com",
	"*.ubuntu.com",
}

func isPackageMirrorHost(host string) bool {
	return host == "deb.debian.org" ||
		host == "security.debian.org" ||
		host == "*.debian.org" ||
		host == "archive.ubuntu.com" ||
		host == "security.ubuntu.com" ||
		host == "ports.ubuntu.com" ||
		host == "*.ubuntu.com" ||
		strings.HasSuffix(host, ".debian.org") ||
		strings.HasSuffix(host, ".ubuntu.com")
}

// BuildServicePortalEgressPolicy constructs an EgressPolicy proto for the given allowed hosts.
// It creates an HTTPS allow rule for all hostnames and an HTTP allow rule only for package mirrors.
func BuildServicePortalEgressPolicy(hosts []string) *portalsv1.EgressPolicy {
	var cleanHosts []string
	var httpHosts []string
	seen := make(map[string]bool)
	for _, host := range hosts {
		trimmed := strings.TrimSpace(host)
		if trimmed != "" && !seen[trimmed] {
			seen[trimmed] = true
			cleanHosts = append(cleanHosts, trimmed)
			if isPackageMirrorHost(trimmed) {
				httpHosts = append(httpHosts, trimmed)
			}
		}
	}
	if len(cleanHosts) == 0 {
		return &portalsv1.EgressPolicy{}
	}
	var rules []*portalsv1.EgressRule
	if len(httpHosts) > 0 {
		rules = append(rules, &portalsv1.EgressRule{
			Http: &portalsv1.HttpRule{
				Hostnames: httpHosts,
			},
		})
	}
	rules = append(rules, &portalsv1.EgressRule{
		Https: &portalsv1.HttpsRule{
			Hostnames: append([]string(nil), cleanHosts...),
		},
	})
	return &portalsv1.EgressPolicy{
		Rules: rules,
	}
}

func (r *RunReconciler) servicePortalConfigured() bool {
	return r.ServicePortalClient != nil
}

func (r *RunReconciler) servicePortalPolicy() *portalsv1.EgressPolicy {
	r.cfgMu.RLock()
	hosts := r.ServicePortalAllowedHosts
	r.cfgMu.RUnlock()
	return BuildServicePortalEgressPolicy(hosts)
}

func (r *RunReconciler) bindServicePortalPolicy(ctx context.Context, podUID string) error {
	if podUID == "" {
		return errors.New("service portal bind failed: pod UID is empty")
	}
	policy := r.servicePortalPolicy()
	if err := r.ServicePortalClient.Bind(ctx, podUID, policy, nil); err != nil {
		return fmt.Errorf("service portal bind failed: %w", err)
	}
	return nil
}

func (r *RunReconciler) isServicePortalPodBound(uid types.UID) bool {
	r.boundPodsMu.RLock()
	defer r.boundPodsMu.RUnlock()
	return r.boundServicePortalPods != nil && r.boundServicePortalPods[uid]
}

func (r *RunReconciler) markServicePortalPodBound(uid types.UID) {
	r.boundPodsMu.Lock()
	defer r.boundPodsMu.Unlock()
	if r.boundServicePortalPods == nil {
		r.boundServicePortalPods = make(map[types.UID]bool)
	}
	r.boundServicePortalPods[uid] = true
}

func (r *RunReconciler) unmarkServicePortalPodBound(uid types.UID) {
	r.boundPodsMu.Lock()
	defer r.boundPodsMu.Unlock()
	delete(r.boundServicePortalPods, uid)
}
