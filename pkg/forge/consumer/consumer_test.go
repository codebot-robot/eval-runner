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
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/forge"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
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

	// Verify comparison is retained with pr-state=closed
	if err := cl.List(t.Context(), &comps, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list comparisons: %v", err)
	}
	if len(comps.Items) != 1 {
		t.Fatalf("expected 1 comparison after close, got %d", len(comps.Items))
	}
	if comps.Items[0].Labels[LabelPRState] != StateClosed {
		t.Errorf("expected comparison label %s=%s, got %s", LabelPRState, StateClosed, comps.Items[0].Labels[LabelPRState])
	}

	// Verify candidate run for head2 and baseline run are preserved; orphaned head1 run is deleted
	var survivingRuns v1alpha1.RunList
	if err := cl.List(t.Context(), &survivingRuns, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list runs: %v", err)
	}
	var foundBaseline, foundCand2, foundCand1 bool
	for _, r := range survivingRuns.Items {
		if r.Spec.Repo.Commit == baseSHA {
			foundBaseline = true
		}
		if r.Spec.Repo.Commit == head2SHA {
			foundCand2 = true
		}
		if r.Spec.Repo.Commit == head1SHA {
			foundCand1 = true
		}
	}
	if !foundBaseline {
		t.Errorf("expected baseline run for %s to be preserved after PR close", baseSHA)
	}
	if !foundCand2 {
		t.Errorf("expected candidate run for %s to be preserved after PR close", head2SHA)
	}
	if foundCand1 {
		t.Errorf("expected orphaned candidate run for %s to be deleted after PR close", head1SHA)
	}
}

// TestConsumer_PRCloseAndReopen verifies:
// - Closing a PR retains its Comparison with pr-state=merged (when merged);
// - Candidate seed Run is retained;
// - Orphaned seed Run is deleted;
// - A reopen flips the label back to open.
func TestConsumer_PRCloseAndReopen(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	cloneURL := "https://github.com/gke-labs/eval-runner.git"
	baseSHA := "1111111111111111111111111111111111111111"
	head1SHA := "2222222222222222222222222222222222222222"
	head2SHA := "3333333333333333333333333333333333333333"

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

	// Pre-seed baseline run via branch advanced
	err := c.ConsumeEvent(t.Context(), BranchAdvancedEvent{
		Repo:   ref,
		Branch: "main",
		NewSHA: baseSHA,
	})
	if err != nil {
		t.Fatalf("BranchAdvanced failed: %v", err)
	}

	// 1. Open PR #10 with head1SHA
	pr := forge.ChangeRequest{
		ID:     "10",
		Number: 10,
		Title:  "feat: test close and reopen",
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
	if err := c.ConsumeEvent(t.Context(), ChangeOpenedEvent{
		Repo:   ref,
		Change: pr,
	}); err != nil {
		t.Fatalf("ChangeOpened failed: %v", err)
	}

	// Verify comparison created with pr-state=open
	var comps v1alpha1.ComparisonList
	if err := cl.List(t.Context(), &comps, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list comparisons: %v", err)
	}
	if len(comps.Items) != 1 {
		t.Fatalf("expected 1 comparison, got %d", len(comps.Items))
	}
	if comps.Items[0].Labels[LabelPRState] != StateOpen {
		t.Errorf("expected comparison label %s=%s, got %s", LabelPRState, StateOpen, comps.Items[0].Labels[LabelPRState])
	}

	// 2. Update PR #10 with head2SHA
	pr.Head.SHA = head2SHA
	if err := c.ConsumeEvent(t.Context(), ChangeUpdatedEvent{
		Repo:       ref,
		OldHeadSHA: head1SHA,
		Change:     pr,
	}); err != nil {
		t.Fatalf("ChangeUpdated failed: %v", err)
	}

	// 3. Close PR #10 as merged
	pr.State = "closed"
	pr.Merged = true
	if err := c.ConsumeEvent(t.Context(), ChangeClosedEvent{
		Repo:   ref,
		Change: pr,
	}); err != nil {
		t.Fatalf("ChangeClosed failed: %v", err)
	}

	// Verify comparison is retained with pr-state=merged
	if err := cl.List(t.Context(), &comps, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list comparisons: %v", err)
	}
	if len(comps.Items) != 1 {
		t.Fatalf("expected 1 comparison retained after close, got %d", len(comps.Items))
	}
	if comps.Items[0].Labels[LabelPRState] != StateMerged {
		t.Errorf("expected comparison label %s=%s, got %s", LabelPRState, StateMerged, comps.Items[0].Labels[LabelPRState])
	}

	// Verify candidate seed Run (head2) is retained, orphaned seed Run (head1) is deleted
	var runs v1alpha1.RunList
	if err := cl.List(t.Context(), &runs, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list runs: %v", err)
	}
	var foundCand2, foundCand1 bool
	for _, r := range runs.Items {
		if r.Spec.Repo.Commit == head2SHA {
			foundCand2 = true
		}
		if r.Spec.Repo.Commit == head1SHA {
			foundCand1 = true
		}
	}
	if !foundCand2 {
		t.Errorf("expected referenced candidate run (%s) to be retained", head2SHA)
	}
	if foundCand1 {
		t.Errorf("expected orphaned seed run (%s) to be deleted", head1SHA)
	}

	// 4. Reopen PR #10
	pr.State = "open"
	pr.Merged = false
	if err := c.ConsumeEvent(t.Context(), ChangeOpenedEvent{
		Repo:   ref,
		Change: pr,
	}); err != nil {
		t.Fatalf("Reopen ChangeOpened failed: %v", err)
	}

	// Verify comparison label flipped back to open
	if err := cl.List(t.Context(), &comps, client.InNamespace("eval-runner")); err != nil {
		t.Fatalf("failed to list comparisons: %v", err)
	}
	if len(comps.Items) != 1 {
		t.Fatalf("expected 1 comparison after reopen, got %d", len(comps.Items))
	}
	if comps.Items[0].Labels[LabelPRState] != StateOpen {
		t.Errorf("expected comparison label to flip back to %s=%s after reopen, got %s",
			LabelPRState, StateOpen, comps.Items[0].Labels[LabelPRState])
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

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
}

// TestControllerRole_ConsumerVerbs parses k8s/rbac.yaml and verifies that eval-runner-controller Role
// has create, update, patch, and delete on comparisons and runs in evalrunner.gke-labs.dev.
func TestControllerRole_ConsumerVerbs(t *testing.T) {
	root := findRepoRoot(t)
	manifestPath := filepath.Join(root, "k8s", "rbac.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", manifestPath, err)
	}

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var controllerRole *rbacv1.Role
	for {
		var role rbacv1.Role
		err := decoder.Decode(&role)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to decode Role from %s: %v", manifestPath, err)
		}
		if role.Kind == "Role" && role.Name == "eval-runner-controller" && role.Namespace == "eval-runner" {
			controllerRole = &role
			break
		}
	}

	if controllerRole == nil {
		t.Fatalf("eval-runner-controller Role in namespace eval-runner not found in %s", manifestPath)
	}

	expectedVerbs := []string{"create", "update", "patch", "delete", "get", "list", "watch"}

	verbsForResource := func(resource string) map[string]bool {
		verbs := make(map[string]bool)
		for _, rule := range controllerRole.Rules {
			for _, group := range rule.APIGroups {
				if group == "evalrunner.gke-labs.dev" {
					for _, res := range rule.Resources {
						if res == resource {
							for _, v := range rule.Verbs {
								verbs[v] = true
							}
						}
					}
				}
			}
		}
		return verbs
	}

	for _, res := range []string{"comparisons", "runs"} {
		granted := verbsForResource(res)
		for _, v := range expectedVerbs {
			if !granted[v] {
				t.Errorf("eval-runner-controller Role is missing required verb %q on %s (granted: %v)", v, res, granted)
			}
		}
	}
}

// TestConsumer_ForkPullRequest_CloneURLs verifies that candidate Runs for fork PRs
// clone the fork repository (carrying CloneURL from the head side of the change request),
// while the baseline Run clones from the base repository.
func TestConsumer_ForkPullRequest_CloneURLs(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	baseRef := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	forkRef := forge.RepoRef{Host: "github.com", Owner: "codebot-robot", Name: "eval-runner"}
	baseURL := "https://github.com/gke-labs/eval-runner.git"
	forkURL := "https://github.com/codebot-robot/eval-runner.git"

	store := &fakeStore{
		repos: map[string]*forge.Repository{
			strings.ToLower(baseRef.String()): {
				Ref:           baseRef,
				DefaultBranch: "main",
				CloneURL:      baseURL,
			},
		},
	}

	c := New(cl, Config{
		Namespace:        "eval-runner",
		DefaultModel:     "mockllm/model",
		DefaultBatchSize: 8,
		TrackedRepos: []RepoConfig{
			{Name: "gke-labs/eval-runner", Ref: baseRef, Branches: []string{"main"}},
		},
		Store: store,
	})

	baseSHA := "1111111111111111111111111111111111111111"
	forkSHA1 := "2222222222222222222222222222222222222222"
	forkSHA2 := "3333333333333333333333333333333333333333"

	// 1. ChangeOpenedEvent for fork PR #363
	pr := forge.ChangeRequest{
		ID:     "363",
		Number: 363,
		Title:  "fix: fork PR bugfix",
		State:  "open",
		Head: forge.ChangeRequestHead{
			Ref:      "my-branch",
			SHA:      forkSHA1,
			RepoRef:  forkRef,
			CloneURL: forkURL,
		},
		Base: forge.ChangeRequestBase{
			Ref: "main",
			SHA: baseSHA,
		},
	}

	err := c.ConsumeEvent(t.Context(), ChangeOpenedEvent{
		Repo:   baseRef,
		Change: pr,
	})
	if err != nil {
		t.Fatalf("ChangeOpened failed: %v", err)
	}

	// Verify Candidate Run has the fork's clone URL and commit
	candRunName := CandidateRunName("gke-labs/eval-runner", 363, forkSHA1, false)
	var candRun v1alpha1.Run
	if err := cl.Get(t.Context(), client.ObjectKey{Namespace: "eval-runner", Name: candRunName}, &candRun); err != nil {
		t.Fatalf("failed to get candidate run %s: %v", candRunName, err)
	}
	if candRun.Spec.Repo.URL != forkURL {
		t.Errorf("candidate run spec.repo.url = %q, want fork URL %q", candRun.Spec.Repo.URL, forkURL)
	}
	if candRun.Spec.Repo.Commit != forkSHA1 {
		t.Errorf("candidate run spec.repo.commit = %q, want %q", candRun.Spec.Repo.Commit, forkSHA1)
	}
	if candRun.Labels[LabelRepo] != SanitizeLabelValue("codebot-robot/eval-runner") {
		t.Errorf("candidate run repo label = %q, want %q", candRun.Labels[LabelRepo], SanitizeLabelValue("codebot-robot/eval-runner"))
	}

	// Verify Baseline Run has the base repo's clone URL and commit
	baseRunName := BaselineRunName("gke-labs/eval-runner", baseSHA, false)
	var baseRun v1alpha1.Run
	if err := cl.Get(t.Context(), client.ObjectKey{Namespace: "eval-runner", Name: baseRunName}, &baseRun); err != nil {
		t.Fatalf("failed to get baseline run %s: %v", baseRunName, err)
	}
	if baseRun.Spec.Repo.URL != baseURL {
		t.Errorf("baseline run spec.repo.url = %q, want base URL %q", baseRun.Spec.Repo.URL, baseURL)
	}
	if baseRun.Spec.Repo.Commit != baseSHA {
		t.Errorf("baseline run spec.repo.commit = %q, want %q", baseRun.Spec.Repo.Commit, baseSHA)
	}
	if baseRun.Labels[LabelRepo] != SanitizeLabelValue("gke-labs/eval-runner") {
		t.Errorf("baseline run repo label = %q, want %q", baseRun.Labels[LabelRepo], SanitizeLabelValue("gke-labs/eval-runner"))
	}

	// Verify Comparison references candidate and baseline runs
	comp, err := FindComparisonForPR(t.Context(), cl, "eval-runner", "gke-labs/eval-runner", 363)
	if err != nil || comp == nil {
		t.Fatalf("failed to find comparison for PR #363: %v", err)
	}
	if comp.Spec.Candidate.Run != candRunName {
		t.Errorf("comparison candidate run = %q, want %q", comp.Spec.Candidate.Run, candRunName)
	}
	if comp.Spec.Baseline.Run != baseRunName {
		t.Errorf("comparison baseline run = %q, want %q", comp.Spec.Baseline.Run, baseRunName)
	}

	// 2. ChangeUpdatedEvent (new commit pushed to fork)
	pr.Head.SHA = forkSHA2
	err = c.ConsumeEvent(t.Context(), ChangeUpdatedEvent{
		Repo:       baseRef,
		OldHeadSHA: forkSHA1,
		Change:     pr,
	})
	if err != nil {
		t.Fatalf("ChangeUpdated failed: %v", err)
	}

	cand2RunName := CandidateRunName("gke-labs/eval-runner", 363, forkSHA2, false)
	var cand2Run v1alpha1.Run
	if err := cl.Get(t.Context(), client.ObjectKey{Namespace: "eval-runner", Name: cand2RunName}, &cand2Run); err != nil {
		t.Fatalf("failed to get updated candidate run %s: %v", cand2RunName, err)
	}
	if cand2Run.Spec.Repo.URL != forkURL {
		t.Errorf("updated candidate run spec.repo.url = %q, want fork URL %q", cand2Run.Spec.Repo.URL, forkURL)
	}
	if cand2Run.Spec.Repo.Commit != forkSHA2 {
		t.Errorf("updated candidate run spec.repo.commit = %q, want %q", cand2Run.Spec.Repo.Commit, forkSHA2)
	}
}
