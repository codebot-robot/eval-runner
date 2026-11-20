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
	"reflect"
	"testing"
)

func TestBuildServicePortalEgressPolicy(t *testing.T) {
	testCases := []struct {
		name           string
		options        ServicePortalOptions
		wantHTTPHosts  []string
		wantHTTPSHosts []string
	}{
		{
			name:           "empty hosts produces empty policy",
			options:        ServicePortalOptions{},
			wantHTTPHosts:  nil,
			wantHTTPSHosts: nil,
		},
		{
			name: "only https hosts produces https rule only",
			options: ServicePortalOptions{
				AllowedHosts: []string{"*.googleapis.com", "github.com"},
			},
			wantHTTPHosts:  nil,
			wantHTTPSHosts: []string{"*.googleapis.com", "github.com"},
		},
		{
			name: "only http hosts produces http rule only",
			options: ServicePortalOptions{
				AllowedHTTPHosts: []string{"deb.debian.org", "archive.ubuntu.com"},
			},
			wantHTTPHosts:  []string{"deb.debian.org", "archive.ubuntu.com"},
			wantHTTPSHosts: nil,
		},
		{
			name: "both http and https hosts configure their respective rules",
			options: ServicePortalOptions{
				AllowedHosts:     []string{"*.googleapis.com", "github.com"},
				AllowedHTTPHosts: []string{"deb.debian.org", "archive.ubuntu.com"},
			},
			wantHTTPHosts:  []string{"deb.debian.org", "archive.ubuntu.com"},
			wantHTTPSHosts: []string{"*.googleapis.com", "github.com"},
		},
		{
			name: "defaults produce package mirrors for http and all default hosts for https",
			options: ServicePortalOptions{
				AllowedHosts:     DefaultServicePortalAllowedHosts,
				AllowedHTTPHosts: DefaultServicePortalAllowedHTTPHosts,
			},
			wantHTTPHosts:  DefaultServicePortalAllowedHTTPHosts,
			wantHTTPSHosts: DefaultServicePortalAllowedHosts,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			policy := BuildServicePortalEgressPolicy(tc.options)
			if err := policy.Validate(); err != nil {
				t.Fatalf("policy failed validation: %v", err)
			}
			var gotHTTPHosts []string
			var gotHTTPSHosts []string
			for _, rule := range policy.GetRules() {
				if rule.GetHttp() != nil {
					gotHTTPHosts = rule.GetHttp().GetHostnames()
				}
				if rule.GetHttps() != nil {
					gotHTTPSHosts = rule.GetHttps().GetHostnames()
				}
			}
			if !reflect.DeepEqual(gotHTTPHosts, tc.wantHTTPHosts) {
				t.Errorf("got HTTP hostnames %v, want %v", gotHTTPHosts, tc.wantHTTPHosts)
			}
			if !reflect.DeepEqual(gotHTTPSHosts, tc.wantHTTPSHosts) {
				t.Errorf("got HTTPS hostnames %v, want %v", gotHTTPSHosts, tc.wantHTTPSHosts)
			}
		})
	}
}
