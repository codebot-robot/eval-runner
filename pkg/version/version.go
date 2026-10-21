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
)

// Version can be explicitly overridden, e.g. via -ldflags "-X github.com/gke-labs/eval-runner/pkg/version.Version=...".
var Version string

// Info holds VCS build details.
type Info struct {
	Revision string // 40-hex commit hash
	Time     string // commit timestamp in RFC3339 format
	Modified bool   // true if uncommitted changes were present
}

// readBuildInfoFn allows injecting build info for testing.
var readBuildInfoFn = debug.ReadBuildInfo

// Get returns the VCS information extracted from debug.ReadBuildInfo.
func Get() Info {
	info, ok := readBuildInfoFn()
	if !ok {
		return Info{}
	}
	var res Info
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			res.Revision = s.Value
		case "vcs.time":
			res.Time = s.Value
		case "vcs.modified":
			res.Modified = (s.Value == "true")
		}
	}
	return res
}

// String returns the formatted version string.
// If Version was explicitly set via ldflags, that value is returned.
// Otherwise, it extracts VCS information from debug.ReadBuildInfo().
// When a VCS revision is present, it returns the 40-hex revision,
// appending "-dirty" if the worktree had uncommitted modifications.
// If VCS revision is unavailable, it returns info.Main.Version if set and not "(devel)",
// or "devel".
func String() string {
	if Version != "" {
		return Version
	}
	info, ok := readBuildInfoFn()
	if !ok {
		return "devel"
	}
	var rev string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = (s.Value == "true")
		}
	}
	if rev != "" {
		if modified {
			return rev + "-dirty"
		}
		return rev
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}
