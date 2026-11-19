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

// DefaultServicePortalAllowedHTTPHosts defines the default allowed hostnames for task sandboxes over cleartext HTTP behind the service portal:
// - Debian package mirrors (deb.debian.org, security.debian.org, *.debian.org)
// - Ubuntu package mirrors (archive.ubuntu.com, security.ubuntu.com, ports.ubuntu.com, *.ubuntu.com)
var DefaultServicePortalAllowedHTTPHosts = []string{
	"deb.debian.org",
	"security.debian.org",
	"*.debian.org",
	"archive.ubuntu.com",
	"security.ubuntu.com",
	"ports.ubuntu.com",
	"*.ubuntu.com",
}

// BuildServicePortalEgressPolicy constructs an EgressPolicy proto for the given allowed HTTPS and cleartext HTTP hosts behind the service portal.
// The HTTP rule is populated with httpHosts; the HTTPS rule is populated with httpsHosts.
func BuildServicePortalEgressPolicy(httpsHosts, httpHosts []string) *portalsv1.EgressPolicy {
	if len(httpsHosts) == 0 && len(httpHosts) == 0 {
		return &portalsv1.EgressPolicy{}
	}
	var rules []*portalsv1.EgressRule
	if len(httpHosts) > 0 {
		rules = append(rules, &portalsv1.EgressRule{
			Http: &portalsv1.HttpRule{
				Hostnames: append([]string(nil), httpHosts...),
			},
		})
	}
	if len(httpsHosts) > 0 {
		rules = append(rules, &portalsv1.EgressRule{
			Https: &portalsv1.HttpsRule{
				Hostnames: append([]string(nil), httpsHosts...),
			},
		})
	}
	return &portalsv1.EgressPolicy{
		Rules: rules,
	}
}

func (r *RunReconciler) servicePortalPolicy() *portalsv1.EgressPolicy {
	r.cfgMu.RLock()
	hosts := r.ServicePortalHosts
	httpHosts := r.ServicePortalHTTPHosts
	r.cfgMu.RUnlock()
	return BuildServicePortalEgressPolicy(hosts, httpHosts)
}

func (r *RunReconciler) bindServicePortalPolicy(ctx context.Context, podUID string) error {
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
