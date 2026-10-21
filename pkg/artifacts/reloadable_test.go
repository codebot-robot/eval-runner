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
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type mockSink struct {
	ArtifactSink
	bucket         string
	baseDir        string
	closed         atomic.Bool
	inFlightBlock  chan struct{}
	inFlightActive atomic.Int32
	storeCount     atomic.Int32
}

func (m *mockSink) StoreArtifact(ctx context.Context, meta RunMetadata, filename string, data []byte) (string, error) {
	m.inFlightActive.Add(1)
	defer m.inFlightActive.Add(-1)
	m.storeCount.Add(1)
	if m.inFlightBlock != nil {
		<-m.inFlightBlock
	}
	return "stored-path", nil
}

func (m *mockSink) Load(ctx context.Context, path string) ([]byte, error) {
	m.inFlightActive.Add(1)
	defer m.inFlightActive.Add(-1)
	if m.inFlightBlock != nil {
		<-m.inFlightBlock
	}
	return []byte("loaded-data"), nil
}

func (m *mockSink) ArtifactPrefix(commit string) string {
	if m.bucket != "" {
		return "gs://" + m.bucket + "/" + commit
	}
	if m.baseDir != "" {
		return m.baseDir + "/" + commit
	}
	return ""
}

func (m *mockSink) Bucket() string {
	return m.bucket
}

func (m *mockSink) BaseDir() string {
	return m.baseDir
}

func (m *mockSink) Close() error {
	m.closed.Store(true)
	return nil
}

func TestReloadableSink_InFlightUploadsFinishBeforeClose(t *testing.T) {
	ctx := t.Context()

	sink1 := &mockSink{
		bucket:        "bucket-1",
		inFlightBlock: make(chan struct{}),
	}
	reloadable := NewReloadableSink(sink1)

	// Start an in-flight upload on sink1 in a goroutine
	uploadStarted := make(chan struct{})
	uploadDone := make(chan struct{})
	go func() {
		close(uploadStarted)
		_, err := reloadable.StoreArtifact(ctx, RunMetadata{}, "file.json", []byte("test"))
		if err != nil {
			t.Errorf("unexpected error from StoreArtifact: %v", err)
		}
		close(uploadDone)
	}()

	<-uploadStarted
	// Give the goroutine a moment to enter StoreArtifact
	time.Sleep(20 * time.Millisecond)

	sink2 := &mockSink{bucket: "bucket-2"}
	// Swap to sink2
	reloadable.Swap(sink2)

	// Verify sink2 is current
	if reloadable.Current() != sink2 {
		t.Fatalf("expected sink2 to be current")
	}

	// Verify sink1 is NOT yet closed because the in-flight upload is blocked
	if sink1.closed.Load() {
		t.Fatalf("expected sink1 to NOT be closed while in-flight upload is active")
	}

	// Start a second upload on the new sink - should not block on sink1's channel
	_, err := reloadable.StoreArtifact(ctx, RunMetadata{}, "file2.json", []byte("test2"))
	if err != nil {
		t.Fatalf("StoreArtifact on sink2 failed: %v", err)
	}
	if sink2.storeCount.Load() != 1 {
		t.Fatalf("expected storeCount 1 on sink2, got %d", sink2.storeCount.Load())
	}

	// Now unblock the in-flight upload on sink1
	close(sink1.inFlightBlock)
	<-uploadDone

	// Give a moment for close goroutine to finish
	deadline := time.Now().Add(2 * time.Second)
	for !sink1.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if !sink1.closed.Load() {
		t.Fatalf("expected sink1 to be closed after in-flight upload finished")
	}
	if sink2.closed.Load() {
		t.Fatalf("sink2 should still be open")
	}

	// Finally, close reloadable sink
	if err := reloadable.Close(); err != nil {
		t.Fatalf("reloadable.Close failed: %v", err)
	}
	if !sink2.closed.Load() {
		t.Fatalf("expected sink2 to be closed after reloadable.Close")
	}
}

func TestReloadableSink_DelegationAndPrefix(t *testing.T) {
	sink := &mockSink{bucket: "my-bucket", baseDir: "/tmp/data"}
	reloadable := NewReloadableSink(sink)

	if reloadable.Bucket() != "my-bucket" {
		t.Errorf("expected bucket my-bucket, got %q", reloadable.Bucket())
	}
	if reloadable.BaseDir() != "/tmp/data" {
		t.Errorf("expected baseDir /tmp/data, got %q", reloadable.BaseDir())
	}
	if reloadable.ArtifactPrefix("commit1") != "gs://my-bucket/commit1" {
		t.Errorf("expected prefix gs://my-bucket/commit1, got %q", reloadable.ArtifactPrefix("commit1"))
	}

	reloadable.SwapWait(&mockSink{baseDir: "/var/log"})
	if reloadable.ArtifactPrefix("commit1") != "/var/log/commit1" {
		t.Errorf("expected prefix /var/log/commit1, got %q", reloadable.ArtifactPrefix("commit1"))
	}
	if !sink.closed.Load() {
		t.Errorf("expected initial sink to be closed after SwapWait")
	}
}
