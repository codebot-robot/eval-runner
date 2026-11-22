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

package artifacts_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gke-labs/eval-runner/pkg/artifacts"
)

func newCache(t *testing.T) (*artifacts.ArtifactCache, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cache")
	cache, err := artifacts.NewArtifactCache(dir)
	if err != nil {
		t.Fatalf("NewArtifactCache: %v", err)
	}
	return cache, dir
}

func readAndClose(t *testing.T, file *os.File) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, file); err != nil {
		t.Fatalf("read file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
	return buf.Bytes()
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 files on disk, got %d", len(entries))
	}
}

func TestArtifactCache_HitAndMiss(t *testing.T) {
	cache, dir := newCache(t)
	content := []byte("example artifact content for cache testing")
	hash := artifacts.ArtifactHash(content)

	if file, found, err := cache.Open(hash); file != nil || found || err != nil {
		t.Fatalf("expected clean miss, got file=%v found=%v err=%v", file, found, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("directory should not exist before first write, got %v", err)
	}

	file, computedHash, err := cache.Put("", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if computedHash != hash {
		t.Fatalf("computed hash mismatch: expected %q, got %q", hash, computedHash)
	}
	if got := readAndClose(t, file); !bytes.Equal(got, content) {
		t.Fatalf("Put content mismatch: expected %q, got %q", content, got)
	}

	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("expected directory permissions 0700, got info=%v err=%v", info, err)
	}

	file, found, err := cache.Open(computedHash)
	if err != nil || !found {
		t.Fatalf("Open hit failed: found=%v err=%v", found, err)
	}
	if got := readAndClose(t, file); !bytes.Equal(got, content) {
		t.Fatalf("cached content mismatch: expected %q, got %q", content, got)
	}
}

func TestArtifactCache_Mismatch(t *testing.T) {
	cache, dir := newCache(t)
	badHash := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if file, _, err := cache.Put(badHash, bytes.NewReader([]byte("content"))); file != nil || err == nil || !strings.Contains(err.Error(), "artifact hash mismatch") {
		t.Fatalf("expected mismatch error, got file=%v err=%v", file, err)
	}
	assertEmptyDir(t, dir)
}

type failingReader struct {
	remainingBytes []byte
	failureErr     error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if len(r.remainingBytes) > 0 {
		n := copy(p, r.remainingBytes)
		r.remainingBytes = r.remainingBytes[n:]
		return n, nil
	}
	return 0, r.failureErr
}

func TestArtifactCache_MidStreamFailure(t *testing.T) {
	cache, dir := newCache(t)
	simulatedErr := errors.New("simulated stream failure")
	source := &failingReader{remainingBytes: []byte("partial content before failure"), failureErr: simulatedErr}
	if file, _, err := cache.Put("", source); file != nil || !errors.Is(err, simulatedErr) {
		t.Fatalf("expected error wrapping %v, got file=%v err=%v", simulatedErr, file, err)
	}
	assertEmptyDir(t, dir)
}

func TestArtifactCache_ConcurrentWriters(t *testing.T) {
	cache, dir := newCache(t)
	content := []byte("shared content written concurrently")
	hash := artifacts.ArtifactHash(content)

	const writerCount = 8
	var wg sync.WaitGroup
	putErrors := make([]error, writerCount)
	files := make([]*os.File, writerCount)

	for i := 0; i < writerCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			file, _, err := cache.Put(hash, bytes.NewReader(content))
			if err != nil {
				putErrors[i] = err
				return
			}
			files[i] = file
		}(i)
	}
	wg.Wait()

	for i := 0; i < writerCount; i++ {
		if err := putErrors[i]; err != nil {
			t.Fatalf("writer %d failed: %v", i, err)
		}
		if got := readAndClose(t, files[i]); !bytes.Equal(got, content) {
			t.Fatalf("writer %d content mismatch", i)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 file on disk, got %d", len(entries))
	}
}

func TestArtifactCache_InvalidHash(t *testing.T) {
	cache, _ := newCache(t)
	invalid := []string{
		"", "not-a-hash", "sha256:", "sha256:1234",
		"sha256:000000000000000000000000000000000000000000000000000000000000000G",
		"sha256:000000000000000000000000000000000000000000000000000000000000000A",
		"0000000000000000000000000000000000000000000000000000000000000000",
		"sha256:../../etc/passwd",
	}
	for _, h := range invalid {
		if file, found, err := cache.Open(h); file != nil || found || err == nil {
			t.Errorf("Open(%q): expected error and found=false, got file=%v found=%v err=%v", h, file, found, err)
		}
		if h != "" {
			if file, _, err := cache.Put(h, bytes.NewReader([]byte("content"))); file != nil || err == nil {
				t.Errorf("Put(%q): expected error, got file=%v err=%v", h, file, err)
			}
		}
	}
}
