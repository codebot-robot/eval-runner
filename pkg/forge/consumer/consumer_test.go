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

package consumer

import (
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/forge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeStore struct {
	repos map[string]*forge.Repository
}

func (f *fakeStore) GetRepository(repo forge.RepoRef) (*forge.Repository, bool) {
	if f.repos == nil {
		return nil, false
	}
	r, ok := f.repos[strings.ToLower(repo.String())]
	return r, ok
}

func newTestScheme(t *testing.T) *runtime.Scheme {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add v1alpha1 scheme: %v", err)
	}
	return scheme
}

// TestConsumer_IsMultiRepo tests the single exported IsMultiRepo rule.
func TestConsumer_IsMultiRepo(t *testing.T) {
	if IsMultiRepo(nil) {
		t.Errorf("expected IsMultiRepo(nil) to be false")
	}
	if IsMultiRepo([]string{}) {
		t.Errorf("expected IsMultiRepo([]) to be false")
	}
	if IsMultiRepo([]string{"gke-labs/eval-runner"}) {
		t.Errorf("expected single repository to be false")
	}
	if IsMultiRepo([]string{"gke-labs/eval-runner", "gke-labs/eval-runner"}) {
		t.Errorf("expected duplicate single repository to be false")
	}
	if !IsMultiRepo([]string{"gke-labs/eval-runner", "other/repo"}) {
		t.Errorf("expected distinct repositories to be true")
	}
	if !IsMultiRepo([]string{"*"}) {
		t.Errorf("expected wildcard allow-list to be true")
	}
	if !IsMultiRepo([]string{"*", "gke-labs/eval-runner"}) {
		t.Errorf("expected wildcard with repo to be true")
	}
}

// TestConsumer_EmptyDefaultModel_SkipsCreation verifies that when defaultModel is unset,
// the consumer logs once and creates nothing (0 Runs, 0 Comparisons).
func TestConsumer_EmptyDefaultModel_SkipsCreation(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	c := New(cl, Config{
		Namespace:    "eval-runner",
		DefaultModel: "", // unset
	})

	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	// 1. BranchAdvancedEvent
	err := c.ConsumeEvent(t.Context(), BranchAdvancedEvent{
		Repo:   ref,
		Branch: "main",
		NewSHA: "1111111111111111111111111111111111111111",
	})
	if err != nil {
		t.Fatalf("unexpected error on BranchAdvanced: %v", err)
	}

	// 2. ChangeOpenedEvent
	err = c.ConsumeEvent(t.Context(), ChangeOpenedEvent{
		Repo: ref,
		Change: forge.ChangeRequest{
			Number: 42,
			Head: forge.ChangeRequestHead{
				SHA: "2222222222222222222222222222222222222222",
			},
			Base: forge.ChangeRequestBase{
				SHA: "1111111111111111111111111111111111111111",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error on ChangeOpened: %v", err)
	}

	var runs v1alpha1.RunList
	if err := cl.List(t.Context(), &runs, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list runs: %v", err)
	}
	if len(runs.Items) != 0 {
		t.Errorf("expected 0 runs created when defaultModel is empty, got %d", len(runs.Items))
	}

	var comps v1alpha1.ComparisonList
	if err := cl.List(t.Context(), &comps, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list comparisons: %v", err)
	}
	if len(comps.Items) != 0 {
		t.Errorf("expected 0 comparisons created when defaultModel is empty, got %d", len(comps.Items))
	}
}

// TestConsumer_CloneURL_FromStore verifies clone URLs come from stored Repository.CloneURL.
func TestConsumer_CloneURL_FromStore(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	ref := forge.RepoRef{Host: "custom.git.internal", Owner: "myorg", Name: "myrepo"}
	expectedURL := "git://custom.git.internal/myorg/myrepo.git"

	store := &fakeStore{
		repos: map[string]*forge.Repository{
			strings.ToLower(ref.String()): {
				Ref:           ref,
				DefaultBranch: "main",
				CloneURL:      expectedURL,
			},
		},
	}

	c := New(cl, Config{
		Namespace:    "eval-runner",
		DefaultModel: "test-model",
		Store:        store,
	})

	// Push to main branch
	err := c.ConsumeEvent(t.Context(), BranchAdvancedEvent{
		Repo:   ref,
		Branch: "main",
		NewSHA: "1234567890abcdef1234567890abcdef12345678",
	})
	if err != nil {
		t.Fatalf("ConsumeEvent failed: %v", err)
	}

	var runs v1alpha1.RunList
	if err := cl.List(t.Context(), &runs, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list runs: %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs.Items))
	}

	run := runs.Items[0]
	if run.Spec.Repo.URL != expectedURL {
		t.Errorf("expected clone URL %q from store, got %q", expectedURL, run.Spec.Repo.URL)
	}
}

// TestConsumer_LifecycleAndReplay verifies open, update, close, and idempotent replay.
func TestConsumer_LifecycleAndReplay(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	cloneURL := "https://github.com/gke-labs/eval-runner.git"

	store := &fakeStore{
		repos: map[string]*forge.Repository{
			strings.ToLower(ref.String()): {
				Ref:      ref,
				CloneURL: cloneURL,
			},
		},
	}

	c := New(cl, Config{
		Namespace:        "eval-runner",
		DefaultModel:     "mockllm/model",
		DefaultBatchSize: 8,
		TrackedRepos: []RepoConfig{
			{Name: "gke-labs/eval-runner", Ref: ref, Branches: []string{"main"}},
		},
		Store: store,
	})

	baseSHA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	head1SHA := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	head2SHA := "cccccccccccccccccccccccccccccccccccccccc"

	// 1. BranchAdvancedEvent on main
	err := c.ConsumeEvent(t.Context(), BranchAdvancedEvent{
		Repo:       ref,
		Branch:     "main",
		NewSHA:     baseSHA,
		CommitTime: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("BranchAdvanced failed: %v", err)
	}

	// 2. Replay BranchAdvanced: must be idempotent
	err = c.ConsumeEvent(t.Context(), BranchAdvancedEvent{
		Repo:       ref,
		Branch:     "main",
		NewSHA:     baseSHA,
		CommitTime: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("Replay BranchAdvanced failed: %v", err)
	}

	var runs v1alpha1.RunList
	if err := cl.List(t.Context(), &runs, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list runs: %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("expected exactly 1 baseline run after replay, got %d", len(runs.Items))
	}

	// 3. ChangeOpenedEvent for PR #7
	pr := forge.ChangeRequest{
		ID:     "7",
		Number: 7,
		Title:  "feat: cool PR",
		State:  "open",
		Head: forge.ChangeRequestHead{
			Ref: "feature-branch",
			SHA: head1SHA,
		},
		Base: forge.ChangeRequestBase{
			Ref: "main",
			SHA: baseSHA,
		},
	}
	err = c.ConsumeEvent(t.Context(), ChangeOpenedEvent{
		Repo:   ref,
		Change: pr,
	})
	if err != nil {
		t.Fatalf("ChangeOpened failed: %v", err)
	}

	// Replay ChangeOpened: must be idempotent
	err = c.ConsumeEvent(t.Context(), ChangeOpenedEvent{
		Repo:   ref,
		Change: pr,
	})
	if err != nil {
		t.Fatalf("Replay ChangeOpened failed: %v", err)
	}

	var comps v1alpha1.ComparisonList
	if err := cl.List(t.Context(), &comps, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list comparisons: %v", err)
	}
	if len(comps.Items) != 1 {
		t.Fatalf("expected 1 comparison after PR opened + replay, got %d", len(comps.Items))
	}

	// 4. ChangeUpdatedEvent for PR #7 (new commit)
	pr.Head.SHA = head2SHA
	err = c.ConsumeEvent(t.Context(), ChangeUpdatedEvent{
		Repo:       ref,
		OldHeadSHA: head1SHA,
		Change:     pr,
	})
	if err != nil {
		t.Fatalf("ChangeUpdated failed: %v", err)
	}

	// Replay ChangeUpdated: must be idempotent
	err = c.ConsumeEvent(t.Context(), ChangeUpdatedEvent{
		Repo:       ref,
		OldHeadSHA: head1SHA,
		Change:     pr,
	})
	if err != nil {
		t.Fatalf("Replay ChangeUpdated failed: %v", err)
	}

	if err := cl.List(t.Context(), &comps, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list comparisons: %v", err)
	}
	if len(comps.Items) != 1 {
		t.Fatalf("expected still 1 comparison after update, got %d", len(comps.Items))
	}
	if comps.Items[0].Spec.PullRequest.HeadSHA != head2SHA {
		t.Errorf("expected comparison head SHA %s, got %s", head2SHA, comps.Items[0].Spec.PullRequest.HeadSHA)
	}

	// 5. ChangeClosedEvent for PR #7
	pr.State = "closed"
	err = c.ConsumeEvent(t.Context(), ChangeClosedEvent{
		Repo:   ref,
		Change: pr,
	})
	if err != nil {
		t.Fatalf("ChangeClosed failed: %v", err)
	}

	// Verify comparison deleted
	if err := cl.List(t.Context(), &comps, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list comparisons: %v", err)
	}
	if len(comps.Items) != 0 {
		t.Fatalf("expected 0 comparisons after close, got %d", len(comps.Items))
	}

	// Verify baseline run preserved
	var survivingRuns v1alpha1.RunList
	if err := cl.List(t.Context(), &survivingRuns, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list runs: %v", err)
	}
	var foundBaseline bool
	for _, r := range survivingRuns.Items {
		if r.Spec.Repo.Commit == baseSHA {
			foundBaseline = true
		}
	}
	if !foundBaseline {
		t.Errorf("expected baseline run for %s to be preserved after PR close", baseSHA)
	}
}

// TestConsumer_BaselineReuse verifies that an existing Completed baseline Run is reused.
func TestConsumer_BaselineReuse(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	baseSHA := "1111111111111111111111111111111111111111"

	// Pre-seed completed baseline run
	completedBaseRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "custom-completed-base-run",
			Namespace: "eval-runner",
			Labels: map[string]string{
				LabelHeadSHA: baseSHA,
				LabelRepo:    "gke-labs_eval-runner",
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    "https://github.com/gke-labs/eval-runner.git",
				Commit: baseSHA,
			},
			Model: "mockllm/model",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
		},
	}
	if err := cl.Create(t.Context(), completedBaseRun); err != nil {
		t.Fatalf("failed to create pre-seeded baseline run: %v", err)
	}

	c := New(cl, Config{
		Namespace:    "eval-runner",
		DefaultModel: "mockllm/model",
	})

	// Open PR against that base SHA
	err := c.ConsumeEvent(t.Context(), ChangeOpenedEvent{
		Repo: ref,
		Change: forge.ChangeRequest{
			Number: 10,
			Head: forge.ChangeRequestHead{
				SHA: "2222222222222222222222222222222222222222",
			},
			Base: forge.ChangeRequestBase{
				SHA: baseSHA,
			},
		},
		CandidateCloneURL: "https://github.com/gke-labs/eval-runner.git",
		BaselineCloneURL:  "https://github.com/gke-labs/eval-runner.git",
	})
	if err != nil {
		t.Fatalf("ConsumeEvent failed: %v", err)
	}

	comp, err := FindComparisonForPR(t.Context(), cl, "eval-runner", "gke-labs/eval-runner", 10)
	if err != nil || comp == nil {
		t.Fatalf("failed to find comparison for PR #10: %v", err)
	}

	if comp.Spec.Baseline.Run != "custom-completed-base-run" {
		t.Errorf("expected comparison to reuse completed baseline run %q, got %q",
			"custom-completed-base-run", comp.Spec.Baseline.Run)
	}
}

// TestConsumer_MissingCloneURL_Fails verifies that when neither store nor hint has a clone URL,
// ConsumeEvent returns an error without synthesizing hostnames.
func TestConsumer_MissingCloneURL_Fails(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	c := New(cl, Config{
		Namespace:    "eval-runner",
		DefaultModel: "mockllm/model",
		Store:        nil,
	})

	err := c.ConsumeEvent(t.Context(), BranchAdvancedEvent{
		Repo:   ref,
		Branch: "main",
		NewSHA: "1111111111111111111111111111111111111111",
	})
	if err == nil {
		t.Fatalf("expected error when clone URL is missing from both store and hint, got nil")
	}
	if !strings.Contains(err.Error(), "no clone URL available for repository") {
		t.Errorf("unexpected error message: %v", err)
	}
}
