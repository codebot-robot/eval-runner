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

package artifacts

import (
	"strings"
	"testing"
)

func TestValidateSafeComponent(t *testing.T) {
	valid := []string{
		"abc1234",
		"my-comp-1",
		"result.json",
		"eval_runner",
		"v1.0.0",
	}
	for _, v := range valid {
		if err := ValidateSafeComponent("test", v); err != nil {
			t.Errorf("Expected valid for %q, got error: %v", v, err)
		}
	}

	invalid := []struct {
		val string
		msg string
	}{
		{"", "must not be empty"},
		{"   ", "must not be empty"},
		{"a/b", "invalid characters"},
		{"a\\b", "invalid characters"},
		{"..", "invalid characters"},
		{"a..b", "invalid characters"},
		{"foo\x00bar", "invalid characters"},
		{"foo\nbar", "invalid control characters"},
		{"foo\tbar", "invalid control characters"},
		{"foo\rbar", "invalid control characters"},
	}
	for _, tc := range invalid {
		err := ValidateSafeComponent("test", tc.val)
		if err == nil {
			t.Errorf("Expected error for %q, got nil", tc.val)
		} else if !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("Expected error containing %q for %q, got: %v", tc.msg, tc.val, err)
		}
	}
}

func TestValidateTaskID(t *testing.T) {
	valid := []string{
		"task-1",
		"docs/readme_comprehension",
		"a/b/c/d",
		"benchmark_2026",
	}
	for _, v := range valid {
		if err := ValidateTaskID(v); err != nil {
			t.Errorf("Expected valid for %q, got error: %v", v, err)
		}
	}

	invalid := []string{
		"",
		"   ",
		"/leading",
		"trailing/",
		"docs//readme",
		"docs/./readme",
		"docs/../readme",
		"docs\\readme",
		"docs/\x00/readme",
		"docs/\n/readme",
	}
	for _, v := range invalid {
		if err := ValidateTaskID(v); err == nil {
			t.Errorf("Expected error for %q, got nil", v)
		}
	}
}

func TestValidateArtifact_Valid(t *testing.T) {
	meta := RunMetadata{
		Commit: "1234567890ab",
		TaskID: "docs/readme_comprehension",
	}
	if err := ValidateArtifact(meta, "log.json"); err != nil {
		t.Fatalf("Expected valid artifact to pass validation, got: %v", err)
	}
}
