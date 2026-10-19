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

package ui

import (
	"sync"
	"time"

	"github.com/gke-labs/eval-runner/pkg/artifacts"
)

// logCacheEntry holds the cached parsed samples and error for a specific LogPath.
type logCacheEntry struct {
	key       string
	dataLen   int64
	samples   []artifacts.ParsedInspectSample
	parseErr  error
	createdAt time.Time
	prev      *logCacheEntry
	next      *logCacheEntry
}

// logLRUCache is an in-process, thread-safe LRU cache bounded by total byte size and TTL.
type logLRUCache struct {
	mu         sync.Mutex
	maxBytes   int64
	totalBytes int64
	ttl        time.Duration
	entries    map[string]*logCacheEntry
	head       *logCacheEntry // MRU
	tail       *logCacheEntry // LRU
}

func newLogLRUCache(maxBytes int64, ttl time.Duration) *logLRUCache {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024 * 1024 // 64 MiB default
	}
	if ttl <= 0 {
		ttl = 1 * time.Hour // 1 hour default
	}
	return &logLRUCache{
		maxBytes: maxBytes,
		ttl:      ttl,
		entries:  make(map[string]*logCacheEntry),
	}
}

// get retrieves cached samples if present and not expired, moving the entry to the head.
func (c *logLRUCache) get(key string) ([]artifacts.ParsedInspectSample, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil, nil, false
	}

	if time.Since(entry.createdAt) > c.ttl {
		c.removeEntry(entry)
		return nil, nil, false
	}

	c.moveToHead(entry)
	return entry.samples, entry.parseErr, true
}

// set adds or replaces a cache entry, evicting the least recently used entries if maxBytes is exceeded.
func (c *logLRUCache) set(key string, dataLen int64, samples []artifacts.ParsedInspectSample, parseErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, ok := c.entries[key]; ok {
		c.removeEntry(existing)
	}

	if dataLen > c.maxBytes {
		return // single entry larger than cache capacity is not cached
	}

	for c.totalBytes+dataLen > c.maxBytes && c.tail != nil {
		c.removeEntry(c.tail)
	}

	entry := &logCacheEntry{
		key:       key,
		dataLen:   dataLen,
		samples:   samples,
		parseErr:  parseErr,
		createdAt: time.Now(),
	}

	c.entries[key] = entry
	c.insertAtHead(entry)
	c.totalBytes += dataLen
}

func (c *logLRUCache) moveToHead(e *logCacheEntry) {
	if c.head == e {
		return
	}
	c.detach(e)
	c.insertAtHead(e)
}

func (c *logLRUCache) insertAtHead(e *logCacheEntry) {
	e.prev = nil
	e.next = c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

func (c *logLRUCache) detach(e *logCacheEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	e.prev = nil
	e.next = nil
}

func (c *logLRUCache) removeEntry(e *logCacheEntry) {
	delete(c.entries, e.key)
	c.detach(e)
	c.totalBytes -= e.dataLen
}
