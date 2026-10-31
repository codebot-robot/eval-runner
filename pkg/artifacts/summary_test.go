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

import "testing"

func TestExpectedLogPath(t *testing.T) {
	tests := []struct {
		name string
		meta ArtifactMeta
		want string
	}{
		{
			name: "Prefix with trailing slash and hierarchical taskID",
			meta: ArtifactMeta{
				ArtifactPrefix: "gs://eval-bucket/runs/abcdef/",
				TaskID:         "docs/readme_comprehension",
			},
			want: "gs://eval-bucket/runs/abcdef/docs/readme_comprehension/inspect.json",
		},
		{
			name: "Prefix without trailing slash",
			meta: ArtifactMeta{
				ArtifactPrefix: "gs://eval-bucket/runs/abcdef",
				TaskID:         "task-1",
			},
			want: "gs://eval-bucket/runs/abcdef/task-1/inspect.json",
		},
		{
			name: "InMemory prefix",
			meta: ArtifactMeta{
				ArtifactPrefix: "mem://runs/c0ffee1/",
				TaskID:         "task-rescue",
			},
			want: "mem://runs/c0ffee1/task-rescue/inspect.json",
		},
		{
			name: "LocalDir path prefix",
			meta: ArtifactMeta{
				ArtifactPrefix: "/tmp/artifacts/runs/abcdef/",
				TaskID:         "triage/runbook_lookup",
			},
			want: "/tmp/artifacts/runs/abcdef/triage/runbook_lookup/inspect.json",
		},
		{
			name: "No prefix, with commit",
			meta: ArtifactMeta{
				Commit: "c0ffee1",
				TaskID: "task-1",
			},
			want: "runs/c0ffee1/task-1/inspect.json",
		},
		{
			name: "No prefix and no commit returns empty string",
			meta: ArtifactMeta{
				TaskID: "task-1",
			},
			want: "",
		},
		{
			name: "Custom filename specified",
			meta: ArtifactMeta{
				ArtifactPrefix: "gs://eval-bucket/runs/abcdef/",
				TaskID:         "task-1",
				Filename:       "custom_eval.json",
			},
			want: "gs://eval-bucket/runs/abcdef/task-1/custom_eval.json",
		},
		{
			name: "Empty taskID returns empty string",
			meta: ArtifactMeta{
				ArtifactPrefix: "gs://eval-bucket/runs/abcdef/",
				TaskID:         "",
			},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpectedLogPath(tc.meta)
			if got != tc.want {
				t.Errorf("ExpectedLogPath(%+v) = %q, want %q", tc.meta, got, tc.want)
			}
			aliasGot := TaskLogPath(tc.meta)
			if aliasGot != got {
				t.Errorf("TaskLogPath(%+v) = %q, want %q", tc.meta, aliasGot, got)
			}
		})
	}
}
