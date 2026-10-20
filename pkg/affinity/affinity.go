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

package affinity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

// Normalize normalizes a file path or glob pattern:
// - converts backslashes to forward slashes
// - strips leading "./" prefixes
// - preserves case sensitivity
func Normalize(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	for strings.HasPrefix(p, "./") || strings.HasPrefix(p, "/") {
		if strings.HasPrefix(p, "./") {
			p = strings.TrimPrefix(p, "./")
		} else if strings.HasPrefix(p, "/") {
			p = strings.TrimPrefix(p, "/")
		}
	}
	return p
}

// ValidatePattern validates that pattern is a valid glob pattern under doublestar syntax.
func ValidatePattern(pattern string) error {
	norm := Normalize(pattern)
	if !doublestar.ValidatePattern(norm) {
		return fmt.Errorf("invalid glob pattern %q", pattern)
	}
	return nil
}

// Match reports whether name matches the repository-relative pattern.
// Both pattern and name are normalized (forward slashes, stripped leading "./", case-sensitive).
func Match(pattern, name string) (bool, error) {
	normPattern := Normalize(pattern)
	normName := Normalize(name)
	return doublestar.Match(normPattern, normName)
}

// MatchAny reports whether any of the patterns matches any of the candidate file paths.
func MatchAny(patterns []string, files []string) bool {
	for _, pat := range patterns {
		normPat := Normalize(pat)
		for _, f := range files {
			normFile := Normalize(f)
			matched, err := doublestar.Match(normPat, normFile)
			if err != nil {
				continue
			}
			if matched {
				return true
			}
		}
	}
	return false
}

// DetermineAffinity computes the affinity label for a task:
// - "always" if the task declares no path patterns (empty/nil paths; workdir is not implied)
// - "matched" if changed files exceeded the cap (truncated == true)
// - "matched" if any declared pattern matches at least one changed file
// - "unmatched" if patterns were declared but none matched
func DetermineAffinity(taskPaths []string, changedFiles []string, truncated bool) string {
	if len(taskPaths) == 0 {
		return v1alpha1.ComparisonTaskAffinityAlways
	}
	if truncated {
		return v1alpha1.ComparisonTaskAffinityMatched
	}
	if MatchAny(taskPaths, changedFiles) {
		return v1alpha1.ComparisonTaskAffinityMatched
	}
	return v1alpha1.ComparisonTaskAffinityUnmatched
}

// ComputeFilesHash returns the sha256 checksum (in sha256:<hex> format) of the sorted list of changed files.
func ComputeFilesHash(files []string) string {
	sortedFiles := make([]string, len(files))
	for i, f := range files {
		sortedFiles[i] = Normalize(f)
	}
	sort.Strings(sortedFiles)
	joined := strings.Join(sortedFiles, "\n")
	h := sha256.Sum256([]byte(joined))
	return "sha256:" + hex.EncodeToString(h[:])
}
