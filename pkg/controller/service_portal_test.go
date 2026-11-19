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
		hosts          []string
		wantHttpHosts  []string
		wantHttpsHosts []string
	}{
		{
			name:           "empty hosts produces empty policy",
			hosts:          nil,
			wantHttpHosts:  nil,
			wantHttpsHosts: nil,
		},
		{
			name:           "only api hosts produces https rule only",
			hosts:          []string{"*.googleapis.com", "github.com"},
			wantHttpHosts:  nil,
			wantHttpsHosts: []string{"*.googleapis.com", "github.com"},
		},
		{
			name:           "package mirrors produce both http and https rules",
			hosts:          []string{"deb.debian.org", "archive.ubuntu.com"},
			wantHttpHosts:  []string{"deb.debian.org", "archive.ubuntu.com"},
			wantHttpsHosts: []string{"deb.debian.org", "archive.ubuntu.com"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			policy := BuildServicePortalEgressPolicy(tc.hosts)
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
