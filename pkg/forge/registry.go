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

package forge

import (
	"strings"
	"sync"
)

// Registry maps repository references to the appropriate Forge implementation by host.
type Registry struct {
	mu           sync.RWMutex
	defaultForge Forge
	forges       map[string]Forge
}

// NewRegistry creates a new Registry with an optional fallback forge.
func NewRegistry(defaultForge Forge) *Registry {
	return &Registry{
		defaultForge: defaultForge,
		forges:       make(map[string]Forge),
	}
}

// NewDefaultRegistry creates a Registry configured with github.com mapped to githubForge,
// falling back to gitRemoteForge for any other host.
func NewDefaultRegistry(githubForge, gitRemoteForge Forge) *Registry {
	reg := NewRegistry(gitRemoteForge)
	if githubForge != nil {
		reg.Register("github.com", githubForge)
	}
	return reg
}

// Register registers a forge for a specific host name (e.g. "github.com").
// Matching is case-insensitive.
func (r *Registry) Register(host string, f Forge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forges[strings.ToLower(strings.TrimSpace(host))] = f
}

// SetDefault sets the fallback forge when no host-specific forge matches.
func (r *Registry) SetDefault(f Forge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultForge = f
}

// ForRepo returns the Forge responsible for ref.
// If ref.Host matches a registered host, that forge is returned.
// Otherwise, the default forge is returned.
func (r *Registry) ForRepo(ref RepoRef) Forge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if f, ok := r.forges[strings.ToLower(strings.TrimSpace(ref.Host))]; ok {
		return f
	}
	return r.defaultForge
}
