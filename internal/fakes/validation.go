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

package fakes

import (
	"github.com/gke-labs/eval-runner/pkg/artifacts"
)

// PathValidationTestCase defines a test case for artifact path validation.
type PathValidationTestCase struct {
	Name     string
	Meta     artifacts.RunMetadata
	Filename string
}

// ArtifactPathValidationTestCases returns the canonical table of invalid test cases
// that all artifact sinks (LocalDirSink, GCSSink, InMemorySink) must reject.
func ArtifactPathValidationTestCases() []PathValidationTestCase {
	validMeta := artifacts.RunMetadata{
		Commit: "1234567890ab",
		TaskID: "docs/readme_comprehension",
	}

	return []PathValidationTestCase{
		{
			Name: "empty commit",
			Meta: artifacts.RunMetadata{
				Commit: "",
				TaskID: "docs/readme_comprehension",
			},
			Filename: "log.json",
		},
		{
			Name: "commit with slash",
			Meta: artifacts.RunMetadata{
				Commit: "foo/bar",
				TaskID: "docs/readme_comprehension",
			},
			Filename: "log.json",
		},
		{
			Name: "commit with traversal",
			Meta: artifacts.RunMetadata{
				Commit: "../foo",
				TaskID: "docs/readme_comprehension",
			},
			Filename: "log.json",
		},
		{
			Name: "commit with backslash",
			Meta: artifacts.RunMetadata{
				Commit: "foo\\bar",
				TaskID: "docs/readme_comprehension",
			},
			Filename: "log.json",
		},
		{
			Name: "commit with null byte",
			Meta: artifacts.RunMetadata{
				Commit: "foo\x00bar",
				TaskID: "docs/readme_comprehension",
			},
			Filename: "log.json",
		},
		{
			Name: "commit with control char",
			Meta: artifacts.RunMetadata{
				Commit: "foo\nbar",
				TaskID: "docs/readme_comprehension",
			},
			Filename: "log.json",
		},
		{
			Name: "empty taskID",
			Meta: artifacts.RunMetadata{
				Commit: "1234567890ab",
				TaskID: "",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with double dot",
			Meta: artifacts.RunMetadata{
				Commit: "1234567890ab",
				TaskID: "docs/../escape",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with leading slash",
			Meta: artifacts.RunMetadata{
				Commit: "1234567890ab",
				TaskID: "/docs/readme",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with trailing slash",
			Meta: artifacts.RunMetadata{
				Commit: "1234567890ab",
				TaskID: "docs/readme/",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with backslash",
			Meta: artifacts.RunMetadata{
				Commit: "1234567890ab",
				TaskID: "docs\\readme",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with empty segment",
			Meta: artifacts.RunMetadata{
				Commit: "1234567890ab",
				TaskID: "docs//readme",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with dot segment",
			Meta: artifacts.RunMetadata{
				Commit: "1234567890ab",
				TaskID: "docs/./readme",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with null byte",
			Meta: artifacts.RunMetadata{
				Commit: "1234567890ab",
				TaskID: "docs/\x00/readme",
			},
			Filename: "log.json",
		},
		{
			Name: "taskID with control char",
			Meta: artifacts.RunMetadata{
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
