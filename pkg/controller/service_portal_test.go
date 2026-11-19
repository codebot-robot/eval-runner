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
		httpsHosts     []string
		httpHosts      []string
		wantHttpHosts  []string
		wantHttpsHosts []string
	}{
		{
			name:           "empty hosts produces empty policy",
			httpsHosts:     nil,
			httpHosts:      nil,
			wantHttpHosts:  nil,
			wantHttpsHosts: nil,
		},
		{
			name:           "only https hosts produces https rule only",
			httpsHosts:     []string{"*.googleapis.com", "github.com"},
			httpHosts:      nil,
			wantHttpHosts:  nil,
			wantHttpsHosts: []string{"*.googleapis.com", "github.com"},
		},
		{
			name:           "only http hosts produces http rule only",
			httpsHosts:     nil,
			httpHosts:      []string{"deb.debian.org", "archive.ubuntu.com"},
			wantHttpHosts:  []string{"deb.debian.org", "archive.ubuntu.com"},
			wantHttpsHosts: nil,
		},
		{
			name:           "both http and https hosts configure their respective rules",
			httpsHosts:     []string{"*.googleapis.com", "github.com"},
			httpHosts:      []string{"deb.debian.org", "archive.ubuntu.com"},
			wantHttpHosts:  []string{"deb.debian.org", "archive.ubuntu.com"},
			wantHttpsHosts: []string{"*.googleapis.com", "github.com"},
		},
		{
			name:           "defaults produce package mirrors for http and all default hosts for https",
			httpsHosts:     DefaultServicePortalAllowedHosts,
			httpHosts:      DefaultServicePortalAllowedHTTPHosts,
			wantHttpHosts:  DefaultServicePortalAllowedHTTPHosts,
			wantHttpsHosts: DefaultServicePortalAllowedHosts,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			policy := BuildServicePortalEgressPolicy(tc.httpsHosts, tc.httpHosts)
			if err := policy.Validate(); err != nil {
				t.Fatalf("policy failed validation: %v", err)
			}
			var gotHttpHosts []string
			var gotHttpsHosts []string
			for _, rule := range policy.GetRules() {
				if rule.GetHttp() != nil {
					gotHttpHosts = rule.GetHttp().GetHostnames()
				}
				if rule.GetHttps() != nil {
					gotHttpsHosts = rule.GetHttps().GetHostnames()
				}
			}
			if !reflect.DeepEqual(gotHttpHosts, tc.wantHttpHosts) {
				t.Errorf("got HTTP hostnames %v, want %v", gotHttpHosts, tc.wantHttpHosts)
			}
			if !reflect.DeepEqual(gotHttpsHosts, tc.wantHttpsHosts) {
				t.Errorf("got HTTPS hostnames %v, want %v", gotHttpsHosts, tc.wantHttpsHosts)
			}
		})
	}
}
