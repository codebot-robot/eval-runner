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
	"fmt"
	"strings"
)

// Documented Path Validation Rules:
//
// 1. Task IDs:
//    - May contain '/' as a hierarchical namespace separator (e.g. "docs/readme_comprehension").
//    - Must not be empty or whitespace-only.
//    - Must not start or end with a leading or trailing '/'.
//    - Must not contain path traversal elements (".."), backslashes ('\'), null bytes, or control characters.
//    - Each '/'-delimited path segment must be non-empty and must not be "." or "..".
//
// 2. Safe Components (commits, filenames, comparison names, run names, namespaces):
//    - Restricted to a safe charset without path separators.
//    - Must not be empty or whitespace-only.
//    - Must not contain '/' or '\' (and therefore no leading '/').
//    - Must not contain path traversal elements (".."), null bytes, or control characters.

// ValidateSafeComponent validates that a path component (e.g. commit, filename, comparison name)
// is non-empty, contains no slashes, backslashes, directory traversal sequences, or control characters.
func ValidateSafeComponent(name, val string) error {
	if strings.TrimSpace(val) == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if strings.Contains(val, "/") || strings.Contains(val, "\\") || strings.Contains(val, "..") || strings.ContainsRune(val, 0) {
		return fmt.Errorf("%s %q contains invalid characters or traversal elements", name, val)
	}
	for _, r := range val {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s %q contains invalid control characters", name, val)
		}
	}
	return nil
}

// ValidateTaskID validates that taskID is non-empty and adheres to task ID path rules:
// '/' is permitted as a namespace separator, but no leading/trailing slashes, empty segments,
// '.', '..', backslashes, null bytes, or control characters are allowed.
func ValidateTaskID(taskID string) error {
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("taskID must not be empty")
	}
	if strings.Contains(taskID, "\\") || strings.Contains(taskID, "..") || strings.ContainsRune(taskID, 0) {
		return fmt.Errorf("taskID %q contains invalid characters or traversal elements", taskID)
	}
	for _, r := range taskID {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("taskID %q contains invalid control characters", taskID)
		}
	}
	if strings.HasPrefix(taskID, "/") || strings.HasSuffix(taskID, "/") {
		return fmt.Errorf("taskID %q must not start or end with a slash", taskID)
	}
	parts := strings.Split(taskID, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return fmt.Errorf("taskID %q contains empty or invalid path segment %q", taskID, p)
		}
	}
	return nil
}

// ValidateArtifact validates the components of an artifact path:
// commit (or runNamespace/runName if commit is absent) and filename must be safe components,
// and taskID may contain '/' as a namespace separator.
func ValidateArtifact(meta RunMetadata, filename string) error {
	if meta.Commit != "" {
		if err := ValidateSafeComponent("commit", meta.Commit); err != nil {
			return err
		}
	} else if meta.RunNamespace != "" || meta.RunName != "" {
		if err := ValidateSafeComponent("runNamespace", meta.RunNamespace); err != nil {
			return err
		}
		if err := ValidateSafeComponent("runName", meta.RunName); err != nil {
			return err
		}
	} else {
		if err := ValidateSafeComponent("commit", meta.Commit); err != nil {
			return err
		}
	}
	if err := ValidateTaskID(meta.TaskID); err != nil {
		return err
	}
	if err := ValidateSafeComponent("filename", filename); err != nil {
		return err
	}
	return nil
}

// PathValidationTestCase defines a test case for artifact path validation.
type PathValidationTestCase struct {
	Name     string
	Meta     RunMetadata
	Filename string
}

// ArtifactPathValidationTestCases returns the canonical table of invalid test cases
// that all artifact sinks (LocalDirSink, GCSSink, InMemorySink) must reject.
func ArtifactPathValidationTestCases() []PathValidationTestCase {
	validMeta := RunMetadata{
		Commit: "1234567890ab",
		TaskID: "docs/readme_comprehension",
	}

	return []PathValidationTestCase{
		{
			Name: "empty commit",
			Meta: RunMetadata{
				Commit: "",
				TaskID: "docs/readme_comprehension",
			},
			Filename: "log.json",
		},
		{
			Name: "commit with slash",
			Meta: RunMetadata{
				Commit: "foo/bar",
				TaskID: "docs/readme_comprehension",
			},
			Filename: "log.json",
		},
		{
			Name: "commit with traversal",
			Meta: RunMetadata{
				Commit: "../foo",
				TaskID: "docs/readme_comprehension",
			},
			Filename: "log.json",
		},
		{
			Name: "commit with backslash",
			Meta: RunMetadata{
				Commit: "foo\\bar",
				TaskID: "docs/readme_comprehension",
			},
			Filename: "log.json",
		},
		{
			Name: "commit with null byte",
			Meta: RunMetadata{
				Commit: "foo\x00bar",
				TaskID: "docs/readme_comprehension",
			},
			Filename: "log.json",
		},
		{
			Name: "commit with control char",
			Meta: RunMetadata{
				Commit: "foo\nbar",
				TaskID: "docs/readme_comprehension",
			},
			Filename: "log.json",
		},
		{
			Name: "empty taskID",
			Meta: RunMetadata{
				Commit: "1234567890ab",
				TaskID: "",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with double dot",
			Meta: RunMetadata{
				Commit: "1234567890ab",
				TaskID: "docs/../escape",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with leading slash",
			Meta: RunMetadata{
				Commit: "1234567890ab",
				TaskID: "/docs/readme",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with trailing slash",
			Meta: RunMetadata{
				Commit: "1234567890ab",
				TaskID: "docs/readme/",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with backslash",
			Meta: RunMetadata{
				Commit: "1234567890ab",
				TaskID: "docs\\readme",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with empty segment",
			Meta: RunMetadata{
				Commit: "1234567890ab",
				TaskID: "docs//readme",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with dot segment",
			Meta: RunMetadata{
				Commit: "1234567890ab",
				TaskID: "docs/./readme",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with null byte",
			Meta: RunMetadata{
				Commit: "1234567890ab",
				TaskID: "docs/\x00/readme",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with control char",
			Meta: RunMetadata{
				Commit: "1234567890ab",
				TaskID: "docs/\t/readme",
			},
			Filename: "log.json",
		},
		{
			Name:     "empty filename",
			Meta:     validMeta,
			Filename: "",
		},
		{
			Name:     "filename with slash",
			Meta:     validMeta,
			Filename: "sub/log.json",
		},
		{
			Name:     "filename with double dot",
			Meta:     validMeta,
			Filename: "../log.json",
		},
		{
			Name:     "filename with backslash",
			Meta:     validMeta,
			Filename: "sub\\log.json",
		},
		{
			Name:     "filename with null byte",
			Meta:     validMeta,
			Filename: "log\x00.json",
		},
		{
			Name:     "filename with control char",
			Meta:     validMeta,
			Filename: "log\r.json",
		},
	}
}
