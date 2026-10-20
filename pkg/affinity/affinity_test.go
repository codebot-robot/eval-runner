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
	"strings"
	"testing"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

func TestDoublestarSemantics(t *testing.T) {
	tests := []struct {
		pattern  string
		file     string
		expected bool
	}{
		// ** matches across multiple directory levels
		{"docs/**", "docs/api.md", true},
		{"docs/**", "docs/sub/folder/file.txt", true},
		{"docs/**", "docs", true},
		{"docs/**", "other/docs/file.txt", false},
		{"**/test.py", "evals/test.py", true},
		{"**/test.py", "evals/nested/sub/test.py", true},
		{"**/test.py", "test.py", true},

		// Single * does not cross path separator
		{"evals/*", "evals/docs.py", true},
		{"evals/*", "evals/sub/docs.py", false},

		// {a,b} alternative syntax
		{"{docs/**,evals/**}", "docs/README.md", true},
		{"{docs/**,evals/**}", "evals/task.py", true},
		{"{docs/**,evals/**}", "pkg/controller/run.go", false},
		{"evals/{a,b}.py", "evals/a.py", true},
		{"evals/{a,b}.py", "evals/b.py", true},
		{"evals/{a,b}.py", "evals/c.py", false},
	}

	for _, tc := range tests {
		matched, err := Match(tc.pattern, tc.file)
		if err != nil {
			t.Fatalf("unexpected error matching %q with %q: %v", tc.pattern, tc.file, err)
		}
		if matched != tc.expected {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.file, matched, tc.expected)
		}
	}
}

func TestLeadingDotSlashPrefix(t *testing.T) {
	tests := []struct {
		pattern  string
		file     string
		expected bool
	}{
		{"./docs/**", "docs/readme.md", true},
		{"docs/**", "./docs/readme.md", true},
		{"./docs/**", "./docs/readme.md", true},
		{"./README.md", "README.md", true},
		{"README.md", "./README.md", true},
		{".//docs/**", "docs/file.txt", true},
	}

	for _, tc := range tests {
		matched, err := Match(tc.pattern, tc.file)
		if err != nil {
			t.Fatalf("Match(%q, %q) error: %v", tc.pattern, tc.file, err)
		}
		if matched != tc.expected {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.file, matched, tc.expected)
		}
	}
}

func TestCaseSensitivity(t *testing.T) {
	tests := []struct {
		pattern  string
		file     string
		expected bool
	}{
		{"docs/**", "DOCS/api.md", false},
		{"docs/**", "Docs/api.md", false},
		{"README.md", "readme.md", false},
		{"README.md", "Readme.md", false},
		{"evals/*.PY", "evals/task.py", false},
		{"evals/*.py", "evals/task.py", true},
	}

	for _, tc := range tests {
		matched, err := Match(tc.pattern, tc.file)
		if err != nil {
			t.Fatalf("Match(%q, %q) error: %v", tc.pattern, tc.file, err)
		}
		if matched != tc.expected {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.file, matched, tc.expected)
		}
	}
}

func TestWorkdirNotImpliedAndEmptyPathsAlways(t *testing.T) {
	// A task with empty or nil paths has affinity "always" regardless of workdir or changed files.
	taskWithoutPaths := v1alpha1.ManifestTask{
		ID:      "evals/docs_task",
		Workdir: "evals",
		Spec:    "eval.py@task",
		Paths:   nil,
	}

	changedFilesInEval := []string{"evals/some_file.py"}
	changedFilesOutsideEval := []string{"docs/index.md"}
	emptyChangedFiles := []string{}

	if got := DetermineAffinity(taskWithoutPaths.Paths, changedFilesInEval, false); got != v1alpha1.ComparisonTaskAffinityAlways {
		t.Errorf("expected 'always' for nil paths with changed file in workdir, got %q", got)
	}
	if got := DetermineAffinity(taskWithoutPaths.Paths, changedFilesOutsideEval, false); got != v1alpha1.ComparisonTaskAffinityAlways {
		t.Errorf("expected 'always' for nil paths with changed file outside workdir, got %q", got)
	}
	if got := DetermineAffinity(taskWithoutPaths.Paths, emptyChangedFiles, false); got != v1alpha1.ComparisonTaskAffinityAlways {
		t.Errorf("expected 'always' for nil paths with no changed files, got %q", got)
	}

	taskWithEmptySlice := v1alpha1.ManifestTask{
		ID:      "evals/docs_task_empty",
		Workdir: "evals",
		Spec:    "eval.py@task",
		Paths:   []string{},
	}
	if got := DetermineAffinity(taskWithEmptySlice.Paths, changedFilesOutsideEval, false); got != v1alpha1.ComparisonTaskAffinityAlways {
		t.Errorf("expected 'always' for empty slice paths, got %q", got)
	}
}

func TestDetermineAffinity_MatchedAndUnmatched(t *testing.T) {
	taskWithPaths := []string{"docs/**", "README.md"}

	// Matched
	if got := DetermineAffinity(taskWithPaths, []string{"docs/architecture.md"}, false); got != v1alpha1.ComparisonTaskAffinityMatched {
		t.Errorf("expected 'matched', got %q", got)
	}
	if got := DetermineAffinity(taskWithPaths, []string{"README.md"}, false); got != v1alpha1.ComparisonTaskAffinityMatched {
		t.Errorf("expected 'matched', got %q", got)
	}

	// Unmatched
	if got := DetermineAffinity(taskWithPaths, []string{"pkg/controller/run.go"}, false); got != v1alpha1.ComparisonTaskAffinityUnmatched {
		t.Errorf("expected 'unmatched', got %q", got)
	}
	if got := DetermineAffinity(taskWithPaths, []string{}, false); got != v1alpha1.ComparisonTaskAffinityUnmatched {
		t.Errorf("expected 'unmatched' for empty changed files, got %q", got)
	}

	// Truncated (cap exceeded): all tasks with paths are treated as matched
	if got := DetermineAffinity(taskWithPaths, []string{"unrelated.txt"}, true); got != v1alpha1.ComparisonTaskAffinityMatched {
		t.Errorf("expected 'matched' when truncated == true, got %q", got)
	}
	// Task with empty paths remains "always" even when truncated
	if got := DetermineAffinity(nil, []string{"unrelated.txt"}, true); got != v1alpha1.ComparisonTaskAffinityAlways {
		t.Errorf("expected 'always' for empty paths when truncated == true, got %q", got)
	}
}

func TestValidatePattern(t *testing.T) {
	if err := ValidatePattern("docs/**"); err != nil {
		t.Errorf("expected valid pattern, got %v", err)
	}
	if err := ValidatePattern("{docs/**,evals/**}"); err != nil {
		t.Errorf("expected valid pattern, got %v", err)
	}
	if err := ValidatePattern("[a-z"); err == nil {
		t.Errorf("expected error for unclosed bracket pattern")
	}
}

func TestComputeFilesHash(t *testing.T) {
	files1 := []string{"docs/api.md", "README.md", "pkg/controller/run.go"}
	files2 := []string{"README.md", "pkg/controller/run.go", "docs/api.md"} // different order

	hash1 := ComputeFilesHash(files1)
	hash2 := ComputeFilesHash(files2)

	if !strings.HasPrefix(hash1, "sha256:") {
		t.Errorf("expected sha256 prefix, got %q", hash1)
	}
	if hash1 != hash2 {
		t.Errorf("expected order-independent hash equality, got %q vs %q", hash1, hash2)
	}
}
