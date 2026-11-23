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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const hashPrefix = "sha256:"

func validateHash(hash string) (string, error) {
	if !strings.HasPrefix(hash, hashPrefix) || len(hash) != len(hashPrefix)+64 {
		return "", fmt.Errorf("invalid artifact hash %q: must be %s followed by 64 lowercase hex characters", hash, hashPrefix)
	}
	hexDigest := hash[len(hashPrefix):]
	for i := 0; i < len(hexDigest); i++ {
		c := hexDigest[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return "", fmt.Errorf("invalid artifact hash %q: must be %s followed by 64 lowercase hex characters", hash, hashPrefix)
		}
	}
	return hexDigest, nil
}

// ArtifactCache manages locally stored artifact files keyed by SHA-256.
// Note: Removal of old files is not implemented yet. Temporary files left
// by a crashed process are not cleaned up either, and there is no fsync
// before the rename.
type ArtifactCache struct {
	directory string
}

// NewArtifactCache creates an ArtifactCache using the specified directory.
// The directory is not created upon construction; it is created on first write.
func NewArtifactCache(directory string) (*ArtifactCache, error) {
	if directory == "" {
		return nil, errors.New("artifact cache directory must not be empty")
	}
	return &ArtifactCache{directory: directory}, nil
}

// Open returns an open file if present under hash.
// The file holds the artifact bytes as stored in the backend (which may be compressed with gzip);
// callers decode the stream as needed. A damaged local file is not detected or re-downloaded yet.
// If the artifact is not found, it returns nil, false, nil.
// If the hash is invalid, it returns an error.
func (cache *ArtifactCache) Open(hash string) (*os.File, bool, error) {
	hexDigest, err := validateHash(hash)
	if err != nil {
		return nil, false, err
	}
	targetPath := filepath.Join(cache.directory, hexDigest)
	file, err := os.Open(targetPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("open cached artifact file %q: %w", targetPath, err)
	}
	return file, true, nil
}

// Put streams artifact content from source into a local file.
// The file holds the artifact bytes as received from the backend (which may be compressed with gzip),
// while keyed by the SHA-256 of the content as produced (decompressed if source is gzip).
// Callers decode the stream upon reading. A damaged local file is not detected or re-downloaded yet.
// While streaming, source bytes are written to disk as received and simultaneously decompressed
// and hashed in a single pass.
// If expectedHash is non-empty, it verifies that the computed SHA-256 matches.
// A source that fails or ends early, a truncated gzip, or a hash mismatch leaves nothing behind.
// It returns an open file to the cached content and the computed content hash.
func (cache *ArtifactCache) Put(expectedHash string, source io.Reader) (_ *os.File, _ string, returnedError error) {
	if expectedHash != "" {
		if _, err := validateHash(expectedHash); err != nil {
			return nil, "", err
		}
	}

	if err := os.MkdirAll(cache.directory, 0700); err != nil {
		return nil, "", fmt.Errorf("create artifact cache directory %q: %w", cache.directory, err)
	}

	temporaryFile, err := os.CreateTemp(cache.directory, "artifact-*.tmp")
	if err != nil {
		return nil, "", fmt.Errorf("create temporary cache file: %w", err)
	}
	temporaryPath := temporaryFile.Name()

	closed := false
	committed := false
	defer func() {
		if !committed {
			if !closed {
				if closeError := temporaryFile.Close(); closeError != nil {
					returnedError = errors.Join(returnedError, fmt.Errorf("close temporary cache file: %w", closeError))
				}
			}
			if removeError := os.Remove(temporaryPath); removeError != nil && !errors.Is(removeError, fs.ErrNotExist) {
				returnedError = errors.Join(returnedError, fmt.Errorf("remove temporary cache file: %w", removeError))
			}
		}
	}()

	teeReader := io.TeeReader(source, temporaryFile)
	decompressingReader, err := NewDecompressingReader(teeReader)
	if err != nil {
		return nil, "", fmt.Errorf("decompress artifact stream: %w", err)
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, decompressingReader); err != nil {
		closeError := decompressingReader.Close()
		if closeError != nil && !errors.Is(closeError, err) {
			return nil, "", errors.Join(fmt.Errorf("stream artifact to cache: %w", err), closeError)
		}
		return nil, "", fmt.Errorf("stream artifact to cache: %w", err)
	}
	if err := decompressingReader.Close(); err != nil {
		return nil, "", fmt.Errorf("close decompressing reader: %w", err)
	}

	hexDigest := hex.EncodeToString(hasher.Sum(nil))
	computedHash := hashPrefix + hexDigest
	if expectedHash != "" && expectedHash != computedHash {
		return nil, "", fmt.Errorf("artifact hash mismatch: expected %s, got %s", expectedHash, computedHash)
	}

	if err := temporaryFile.Close(); err != nil {
		closed = true
		return nil, "", fmt.Errorf("close temporary cache file: %w", err)
	}
	closed = true

	targetPath := filepath.Join(cache.directory, hexDigest)
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		return nil, "", fmt.Errorf("commit cached artifact file %q: %w", targetPath, err)
	}
	committed = true

	cachedFile, err := os.Open(targetPath)
	if err != nil {
		return nil, "", fmt.Errorf("open cached artifact file %q: %w", targetPath, err)
	}
	return cachedFile, computedHash, nil
}
