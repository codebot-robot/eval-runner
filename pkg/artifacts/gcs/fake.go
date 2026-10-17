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

package gcs

import (
	"context"
	"fmt"
	"sync"
)

// FakeStorageClient is an in-memory test double for StorageClient.
type FakeStorageClient struct {
	mu      sync.Mutex
	Objects map[string]FakeObject // key: "bucket/object"
	Errors  map[string]error      // key: "bucket/object" -> injected error
	Closed  bool
}

// FakeObject represents a stored object in FakeStorageClient.
type FakeObject struct {
	Bucket      string
	Object      string
	ContentType string
	Data        []byte
	Metadata    map[string]string
}

// NewFakeStorageClient creates a new FakeStorageClient.
func NewFakeStorageClient() *FakeStorageClient {
	return &FakeStorageClient{
		Objects: make(map[string]FakeObject),
		Errors:  make(map[string]error),
	}
}

// WriteObject records the object write in memory without metadata.
func (f *FakeStorageClient) WriteObject(ctx context.Context, bucket, object, contentType string, data []byte) error {
	return f.WriteObjectWithMetadata(ctx, bucket, object, contentType, data, nil)
}

// WriteObjectWithMetadata records the object write and metadata in memory.
func (f *FakeStorageClient) WriteObjectWithMetadata(ctx context.Context, bucket, object, contentType string, data []byte, metadata map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := bucket + "/" + object
	if err, ok := f.Errors[key]; ok {
		return err
	}
	// Check wildcard error for bucket
	if err, ok := f.Errors[bucket+"/*"]; ok {
		return err
	}

	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)
	var metaCopy map[string]string
	if metadata != nil {
		metaCopy = make(map[string]string, len(metadata))
		for k, v := range metadata {
			metaCopy[k] = v
		}
	}
	f.Objects[key] = FakeObject{
		Bucket:      bucket,
		Object:      object,
		ContentType: contentType,
		Data:        dataCopy,
		Metadata:    metaCopy,
	}
	return nil
}

// ReadObject retrieves the object data from memory, bounded by maxBytes.
func (f *FakeStorageClient) ReadObject(ctx context.Context, bucket, object string, maxBytes int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := bucket + "/" + object
	if err, ok := f.Errors[key]; ok {
		return nil, err
	}
	if err, ok := f.Errors[bucket+"/*"]; ok {
		return nil, err
	}

	obj, ok := f.Objects[key]
	if !ok {
		return nil, fmt.Errorf("object %s not found", key)
	}
	if maxBytes > 0 && int64(len(obj.Data)) > maxBytes {
		return nil, fmt.Errorf("object gs://%s/%s exceeds maximum size of %d bytes", bucket, object, maxBytes)
	}
	dataCopy := make([]byte, len(obj.Data))
	copy(dataCopy, obj.Data)
	return dataCopy, nil
}

// Close marks the client as closed.
func (f *FakeStorageClient) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Closed = true
	return nil
}

// GetObject retrieves a stored object by bucket and object name.
func (f *FakeStorageClient) GetObject(bucket, object string) (FakeObject, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.Objects[bucket+"/"+object]
	return obj, ok
}
