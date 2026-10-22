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

package gitremote

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"
)

var _ forge.Forge = (*Forge)(nil)

// RefLister queries references from a remote Git URL.
type RefLister func(ctx context.Context, url string) ([]*plumbing.Reference, error)

// URLResolver converts a RepoRef to a clone URL.
type URLResolver func(ref forge.RepoRef) string

// Forge implements forge.Forge backed by pure-Go remote ref advertisement via go-git.
// It executes no subcommands, requires no git binary, and performs no clones or working tree checkouts.
type Forge struct {
	lister   RefLister
	resolver URLResolver
}

// Option configures a gitremote Forge.
type Option func(*options)

type options struct {
	lister   RefLister
	resolver URLResolver
	baseURL  string
}

// WithRefLister sets a custom ref lister (useful for unit tests without network).
func WithRefLister(lister RefLister) Option {
	return func(o *options) {
		o.lister = lister
	}
}

// WithURLResolver sets a custom repository URL resolver function.
func WithURLResolver(resolver URLResolver) Option {
	return func(o *options) {
		o.resolver = resolver
	}
}

// WithBaseURL sets a base URL prefix for repositories.
func WithBaseURL(baseURL string) Option {
	return func(o *options) {
		o.baseURL = strings.TrimRight(baseURL, "/")
	}
}

func defaultRefLister(ctx context.Context, url string) ([]*plumbing.Reference, error) {
	rem := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})
	return rem.ListContext(ctx, &git.ListOptions{})
}

func defaultURLResolver(ref forge.RepoRef) string {
	if strings.EqualFold(ref.Host, "github.com") {
		return fmt.Sprintf("https://github.com/%s/%s.git", ref.Owner, ref.Name)
	}
	if ref.Owner == "-" {
		return fmt.Sprintf("https://%s/%s.git", ref.Host, ref.Name)
	}
	return fmt.Sprintf("https://%s/%s/%s.git", ref.Host, ref.Owner, ref.Name)
}

// New creates a new gitremote Forge.
func New(opts ...Option) *Forge {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	lister := o.lister
	if lister == nil {
		lister = defaultRefLister
	}

	resolver := o.resolver
	if resolver == nil {
		if o.baseURL != "" {
			resolver = func(ref forge.RepoRef) string {
				if ref.Owner == "-" {
					return fmt.Sprintf("%s/%s.git", o.baseURL, ref.Name)
				}
				return fmt.Sprintf("%s/%s/%s.git", o.baseURL, ref.Owner, ref.Name)
			}
		} else {
			resolver = defaultURLResolver
		}
	}

	return &Forge{
		lister:   lister,
		resolver: resolver,
	}
}

// Kind returns "gitremote".
func (f *Forge) Kind() string {
	return "gitremote"
}

func (f *Forge) listRefs(ctx context.Context, ref forge.RepoRef) ([]*plumbing.Reference, string, error) {
	if !ref.IsValid() {
		return nil, "", fmt.Errorf("invalid repository reference: %v", ref)
	}
	cloneURL := f.resolver(ref)

	// Default to 30s timeout if context has no deadline
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	refs, err := f.lister(ctx, cloneURL)
	if err != nil {
		if errors.Is(err, transport.ErrRepositoryNotFound) || strings.Contains(strings.ToLower(err.Error()), "repository not found") {
			return nil, "", fmt.Errorf("%w: %v", forge.ErrNotFound, err)
		}
		return nil, "", err
	}
	return refs, cloneURL, nil
}

// Repository returns metadata for ref, discovering the default branch via symbolic HEAD.
func (f *Forge) Repository(ctx context.Context, ref forge.RepoRef) (*forge.Repository, error) {
	refs, cloneURL, err := f.listRefs(ctx, ref)
	if err != nil {
		return nil, err
	}

	defaultBranch := ExtractSymbolicHEAD(refs)
	return &forge.Repository{
		Ref:           ref,
		DefaultBranch: defaultBranch,
		CloneURL:      cloneURL,
	}, nil
}

// Branches lists branch heads (name -> commit sha) via remote ref advertisement.
func (f *Forge) Branches(ctx context.Context, ref forge.RepoRef) ([]forge.Branch, error) {
	refs, _, err := f.listRefs(ctx, ref)
	if err != nil {
		return nil, err
	}

	return ExtractBranches(refs), nil
}

// ChangeRequests lists change requests from refs/pull/N/head for github.com remotes.
func (f *Forge) ChangeRequests(ctx context.Context, ref forge.RepoRef) ([]forge.ChangeRequest, error) {
	// gitremote only serves change requests for github remotes with refs/pull/
	if !strings.EqualFold(ref.Host, "github.com") {
		return nil, fmt.Errorf("gitremote: change requests not supported for host %q: %w", ref.Host, forge.ErrUnsupported)
	}

	refs, _, err := f.listRefs(ctx, ref)
	if err != nil {
		return nil, err
	}

	defaultBranch := ExtractSymbolicHEAD(refs)
	return ExtractChangeRequests(refs, ref, defaultBranch), nil
}

// Commit returns forge.ErrUnsupported because remote ref advertisement does not transfer commit objects.
func (f *Forge) Commit(ctx context.Context, ref forge.RepoRef, sha string) (*forge.Commit, error) {
	return nil, fmt.Errorf("gitremote: commit metadata not supported via remote ref advertisement: %w", forge.ErrUnsupported)
}

// ExtractSymbolicHEAD extracts the default branch name from the symbolic HEAD reference.
func ExtractSymbolicHEAD(refs []*plumbing.Reference) string {
	for _, r := range refs {
		if r.Name() == plumbing.HEAD && r.Type() == plumbing.SymbolicReference {
			target := r.Target().String()
			branch := strings.TrimPrefix(target, "refs/heads/")
			if branch != "" && branch != target {
				return branch
			}
			return r.Target().Short()
		}
	}
	return ""
}

// ExtractBranches extracts branch heads from reference advertisements, ignoring tags and HEAD.
func ExtractBranches(refs []*plumbing.Reference) []forge.Branch {
	var branches []forge.Branch
	for _, r := range refs {
		if r.Name().IsTag() || r.Name() == plumbing.HEAD {
			continue
		}
		if r.Name().IsBranch() {
			branches = append(branches, forge.Branch{
				Name: r.Name().Short(),
				SHA:  r.Hash().String(),
			})
		}
	}
	sort.Slice(branches, func(i, j int) bool {
		return branches[i].Name < branches[j].Name
	})
	return branches
}

// ExtractChangeRequests extracts ChangeRequests from refs/pull/N/head and refs/pull/N/merge.
// Reports State: "unknown" per ADR-033 because GitHub retains head refs indefinitely.
func ExtractChangeRequests(refs []*plumbing.Reference, ref forge.RepoRef, defaultBranch string) []forge.ChangeRequest {
	type pullAcc struct {
		headSHA  string
		mergeSHA string
	}
	pulls := make(map[int]*pullAcc)

	for _, r := range refs {
		name := r.Name().String()
		if !strings.HasPrefix(name, "refs/pull/") {
			continue
		}
		trimmed := strings.TrimPrefix(name, "refs/pull/")
		parts := strings.Split(trimmed, "/")
		if len(parts) == 2 {
			num, err := strconv.Atoi(parts[0])
			if err != nil {
				continue
			}
			acc, ok := pulls[num]
			if !ok {
				acc = &pullAcc{}
				pulls[num] = acc
			}
			switch parts[1] {
			case "head":
				acc.headSHA = r.Hash().String()
			case "merge":
				acc.mergeSHA = r.Hash().String()
			}
		}
	}

	var crs []forge.ChangeRequest
	for num, acc := range pulls {
		// Only include change requests that have a head ref
		if acc.headSHA == "" {
			continue
		}
		crs = append(crs, forge.ChangeRequest{
			ID:     forge.ChangeRequestID(strconv.Itoa(num)),
			Number: num,
			Title:  fmt.Sprintf("Pull request #%d", num),
			Head: forge.ChangeRequestHead{
				Ref:     fmt.Sprintf("refs/pull/%d/head", num),
				SHA:     acc.headSHA,
				RepoRef: ref,
			},
			Base: forge.ChangeRequestBase{
				Ref: defaultBranch,
			},
			State:    "unknown",
			MergeSHA: acc.mergeSHA,
		})
	}

	sort.Slice(crs, func(i, j int) bool {
		return crs[i].Number < crs[j].Number
	})
	return crs
}
