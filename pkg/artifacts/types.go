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

import "context"

// RunMetadata provides metadata about the Run and Task for artifact storage.
type RunMetadata struct {
	RunName      string
	RunNamespace string
	TaskID       string
	Model        string
	Commit       string
}

// ArtifactSink defines the interface for storing Inspect evaluation logs and artifacts.
type ArtifactSink interface {
	// StoreArtifact persists the raw log bytes for a task and returns the storage path or URI.
	StoreArtifact(ctx context.Context, meta RunMetadata, filename string, data []byte) (string, error)
}
