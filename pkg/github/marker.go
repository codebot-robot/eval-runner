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

package github

import (
	"fmt"
	"strings"
)

// ComparisonMarker returns the hidden HTML comment marker used to identify
// and update an existing pull request comment for a Comparison in-place.
func ComparisonMarker(namespace, name string) string {
	return fmt.Sprintf("<!-- eval-runner:comparison:%s/%s -->", namespace, name)
}

// HasComparisonMarker reports whether body contains the hidden marker for the given Comparison.
// It verifies the marker is on its own line or at the beginning of the comment body to avoid
// false matches on escaped or embedded text.
func HasComparisonMarker(body, namespace, name string) bool {
	marker := ComparisonMarker(namespace, name)
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, marker) {
		rest := trimmed[len(marker):]
		if len(rest) == 0 || rest[0] == '\r' || rest[0] == '\n' || rest[0] == ' ' {
			return true
		}
	}
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == marker {
			return true
		}
	}
	return false
}
