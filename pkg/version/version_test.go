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

package version

import (
	"runtime/debug"
	"testing"
)

func TestVersion_Override(t *testing.T) {
	orig := Version
	defer func() { Version = orig }()

	Version = "v1.2.3-custom"
	if got := String(); got != "v1.2.3-custom" {
		t.Errorf("expected %q, got %q", "v1.2.3-custom", got)
	}
}

func TestVersion_VCSClean(t *testing.T) {
	origFn := readBuildInfoFn
	origVer := Version
	defer func() {
		readBuildInfoFn = origFn
		Version = origVer
	}()
	Version = ""

	readBuildInfoFn = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
				{Key: "vcs.time", Value: "2026-10-21T12:00:00Z"},
				{Key: "vcs.modified", Value: "false"},
			},
		}, true
	}

	info := Get()
	if info.Revision != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("expected revision, got %q", info.Revision)
	}
	if info.Time != "2026-10-21T12:00:00Z" {
		t.Errorf("expected time, got %q", info.Time)
	}
	if info.Modified {
		t.Errorf("expected modified=false, got true")
	}

	if got := String(); got != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("expected clean revision, got %q", got)
	}
}

func TestVersion_VCSDirty(t *testing.T) {
	origFn := readBuildInfoFn
	origVer := Version
	defer func() {
		readBuildInfoFn = origFn
		Version = origVer
	}()
	Version = ""

	readBuildInfoFn = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
				{Key: "vcs.time", Value: "2026-10-21T12:00:00Z"},
				{Key: "vcs.modified", Value: "true"},
			},
		}, true
	}

	info := Get()
	if !info.Modified {
		t.Errorf("expected modified=true, got false")
	}

	if got := String(); got != "0123456789abcdef0123456789abcdef01234567-dirty" {
		t.Errorf("expected dirty revision, got %q", got)
	}
}

func TestVersion_MainModuleFallback(t *testing.T) {
	origFn := readBuildInfoFn
	origVer := Version
	defer func() {
		readBuildInfoFn = origFn
		Version = origVer
	}()
	Version = ""

	readBuildInfoFn = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{
				Version: "v0.5.0",
			},
		}, true
	}

	if got := String(); got != "v0.5.0" {
		t.Errorf("expected main module version fallback, got %q", got)
	}
}

func TestVersion_DevelFallback(t *testing.T) {
	origFn := readBuildInfoFn
	origVer := Version
	defer func() {
		readBuildInfoFn = origFn
		Version = origVer
	}()
	Version = ""

	readBuildInfoFn = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{
				Version: "(devel)",
			},
		}, true
	}

	if got := String(); got != "devel" {
		t.Errorf("expected devel fallback, got %q", got)
	}

	readBuildInfoFn = func() (*debug.BuildInfo, bool) {
		return nil, false
	}

	if got := String(); got != "devel" {
		t.Errorf("expected devel fallback when readBuildInfo is nil, got %q", got)
	}
	if info := Get(); info.Revision != "" || info.Time != "" || info.Modified {
		t.Errorf("expected empty Info when readBuildInfo is false, got %+v", info)
	}
}
