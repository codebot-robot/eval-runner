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

package controller

import (
	"context"

	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/artifacts/gcs"
)

// NewArtifactSink constructs an ArtifactSink based on the provided bucket and local directory.
// If bucket is non-empty, a GCSSink is returned (with optional maxUploadBytes cap).
// If dir is non-empty, a LocalDirSink is returned.
// If neither is configured, nil is returned without error.
func NewArtifactSink(ctx context.Context, bucket, dir string, maxUploadBytes int64) (artifacts.ArtifactSink, error) {
	if bucket != "" {
		var gcsOpts []gcs.Option
		if maxUploadBytes > 0 {
			gcsOpts = append(gcsOpts, gcs.WithMaxUploadBytes(maxUploadBytes))
		}
		return gcs.New(ctx, bucket, gcsOpts...)
	}
	if dir != "" {
		return &artifacts.LocalDirSink{BaseDir: dir}, nil
	}
	return nil, nil
}
