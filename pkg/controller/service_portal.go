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

	"github.com/gke-labs/eval-runner/pkg/discovery"
	portalsv1 "github.com/gke-labs/service-portals/pkg/portals/v1"
	corev1 "k8s.io/api/core/v1"
)

// ServicePortalClient binds egress policies to sandbox pods via the Service Portal Controller.
type ServicePortalClient interface {
	Bind(ctx context.Context, podUID string, policy *portalsv1.EgressPolicy, secrets map[string][]byte) error
}

// BuildServicePortalEgressPolicy constructs an EgressPolicy proto for the given allowed hosts.
// It creates HTTP and HTTPS allow rules for the specified hostnames without duplicates.
func BuildServicePortalEgressPolicy(hosts []string) *portalsv1.EgressPolicy {
	var cleanHosts []string
	seen := make(map[string]bool)
	for _, h := range hosts {
		trimmed := strings.TrimSpace(h)
		if trimmed != "" && !seen[trimmed] {
			seen[trimmed] = true
			cleanHosts = append(cleanHosts, trimmed)
		}
	}
	if len(cleanHosts) == 0 {
		return &portalsv1.EgressPolicy{}
	}
	return &portalsv1.EgressPolicy{
		Rules: []*portalsv1.EgressRule{
			{
				Http: &portalsv1.HttpRule{
					Hostnames: append([]string(nil), cleanHosts...),
				},
			},
			{
				Https: &portalsv1.HttpsRule{
					Hostnames: append([]string(nil), cleanHosts...),
				},
			},
		},
	}
}

func (r *RunReconciler) servicePortalConfigured() bool {
	return r.ServicePortalClient != nil
}

func (r *RunReconciler) servicePortalPolicy() *portalsv1.EgressPolicy {
	r.cfgMu.RLock()
	hosts := r.SandboxEgressHosts
	r.cfgMu.RUnlock()
	if len(hosts) == 0 {
		hosts = discovery.DefaultSandboxEgressHosts
	}
	return BuildServicePortalEgressPolicy(hosts)
}

func (r *RunReconciler) bindServicePortalPolicy(ctx context.Context, pod *corev1.Pod) error {
	if r.ServicePortalClient == nil {
		return nil
	}
	if pod == nil || pod.UID == "" {
		return errors.New("service portal bind failed: pod UID is empty")
	}
	policy := r.servicePortalPolicy()
	if err := r.ServicePortalClient.Bind(ctx, string(pod.UID), policy, nil); err != nil {
		return fmt.Errorf("service portal bind failed: %w", err)
	}
	return nil
}
