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

package syncer

import (
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/forge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestConsumer(t *testing.T) (*Consumer, client.Client) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add v1alpha1 to scheme: %v", err)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	cfg := ConsumerConfig{
		Namespace:                "test-ns",
		DefaultModel:             "mockllm/model",
		DefaultCohort:            "default",
		DefaultBatchSize:         8,
		DefaultMaxSamplesPerTask: 32,
		TrackedRepos: []RepoConfig{
			{
				Name:     "github.com/gke-labs/eval-runner",
				Branches: []string{"main"},
			},
		},
	}
	consumer := NewConsumer(cl, cfg)
	return consumer, cl
}

// TestConsumer_ReplayIdempotency verifies that processing the exact same events twice
// results in the same Runs and Comparisons without duplication or error.
func TestConsumer_ReplayIdempotency(t *testing.T) {
	ctx := t.Context()
	consumer, cl := newTestConsumer(t)
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	headSHA := "1111111111111111111111111111111111111111"
	baseSHA := "0000000000000000000000000000000000000001"

	openEvent := ChangeOpenedEvent{
		Repo: ref,
		Change: forge.ChangeRequest{
			ID:     "101",
			Number: 101,
			Title:  "feat: initial pr",
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref: "feat-branch",
				SHA: headSHA,
			},
			Base: forge.ChangeRequestBase{
				Ref: "main",
				SHA: baseSHA,
			},
		},
	}

	// 1. First event consumption
	if err := consumer.ConsumeEvent(ctx, openEvent); err != nil {
		t.Fatalf("first ConsumeEvent failed: %v", err)
	}

	// Verify candidate run, baseline run, and comparison exist
	candName := CandidateRunName(ref.String(), 101, headSHA, false)
	baseName := BaselineRunName(ref.String(), baseSHA, false)
	compName := ComparisonName(ref.String(), 101, false)

	var cand1, base1 v1alpha1.Run
	var comp1 v1alpha1.Comparison

	if err := cl.Get(ctx, types.NamespacedName{Namespace: "test-ns", Name: candName}, &cand1); err != nil {
		t.Fatalf("candidate run not found: %v", err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "test-ns", Name: baseName}, &base1); err != nil {
		t.Fatalf("baseline run not found: %v", err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "test-ns", Name: compName}, &comp1); err != nil {
		t.Fatalf("comparison not found: %v", err)
	}

	// 2. Replay the exact same event
	if err := consumer.ConsumeEvent(ctx, openEvent); err != nil {
		t.Fatalf("replayed ConsumeEvent failed: %v", err)
	}

	var cand2, base2 v1alpha1.Run
	var comp2 v1alpha1.Comparison

	if err := cl.Get(ctx, types.NamespacedName{Namespace: "test-ns", Name: candName}, &cand2); err != nil {
		t.Fatalf("candidate run not found after replay: %v", err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "test-ns", Name: baseName}, &base2); err != nil {
		t.Fatalf("baseline run not found after replay: %v", err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "test-ns", Name: compName}, &comp2); err != nil {
		t.Fatalf("comparison not found after replay: %v", err)
	}

	// Verify total count of Runs and Comparisons in the namespace: exactly 2 Runs, 1 Comparison
	var runList v1alpha1.RunList
	if err := cl.List(ctx, &runList, client.InNamespace("test-ns")); err != nil {
		t.Fatalf("list runs failed: %v", err)
	}
	if len(runList.Items) != 2 {
		t.Errorf("expected exactly 2 Runs after replay, found %d", len(runList.Items))
	}

	var compList v1alpha1.ComparisonList
	if err := cl.List(ctx, &compList, client.InNamespace("test-ns")); err != nil {
		t.Fatalf("list comparisons failed: %v", err)
	}
	if len(compList.Items) != 1 {
		t.Errorf("expected exactly 1 Comparison after replay, found %d", len(compList.Items))
	}
}

// TestConsumer_ChangeUpdatedAndClosed verifies update (synchronize) and closure lifecycle.
func TestConsumer_ChangeUpdatedAndClosed(t *testing.T) {
	ctx := t.Context()
	consumer, cl := newTestConsumer(t)
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	sha1 := "1111111111111111111111111111111111111111"
	sha2 := "2222222222222222222222222222222222222222"
	baseSHA := "0000000000000000000000000000000000000001"

	// 1. Open
	openEvent := ChangeOpenedEvent{
		Repo: ref,
		Change: forge.ChangeRequest{
			ID:     "202",
			Number: 202,
			Title:  "feat: pr",
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref: "feat",
				SHA: sha1,
			},
			Base: forge.ChangeRequestBase{
				Ref: "main",
				SHA: baseSHA,
			},
		},
	}
	if err := consumer.ConsumeEvent(ctx, openEvent); err != nil {
		t.Fatalf("open event failed: %v", err)
	}

	// 2. Update with new head sha2
	updateEvent := ChangeUpdatedEvent{
		Repo:       ref,
		OldHeadSHA: sha1,
		Change: forge.ChangeRequest{
			ID:     "202",
			Number: 202,
			Title:  "feat: pr updated",
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref: "feat",
				SHA: sha2,
			},
			Base: forge.ChangeRequestBase{
				Ref: "main",
				SHA: baseSHA,
			},
		},
	}
	if err := consumer.ConsumeEvent(ctx, updateEvent); err != nil {
		t.Fatalf("update event failed: %v", err)
	}

	// Verify Comparison now points to new candidate run
	compName := ComparisonName(ref.String(), 202, false)
	var comp v1alpha1.Comparison
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "test-ns", Name: compName}, &comp); err != nil {
		t.Fatalf("comparison get failed: %v", err)
	}
	newCandName := CandidateRunName(ref.String(), 202, sha2, false)
	if comp.Spec.Candidate.Run != newCandName {
		t.Errorf("expected comparison candidate run %s, got %s", newCandName, comp.Spec.Candidate.Run)
	}

	// 3. Close PR
	closeEvent := ChangeClosedEvent{
		Repo: ref,
		Change: forge.ChangeRequest{
			ID:     "202",
			Number: 202,
			State:  "closed",
		},
	}
	if err := consumer.ConsumeEvent(ctx, closeEvent); err != nil {
		t.Fatalf("close event failed: %v", err)
	}

	// Verify comparison is deleted
	var compAfter v1alpha1.Comparison
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "test-ns", Name: compName}, &compAfter); err == nil {
		t.Errorf("expected comparison to be deleted after PR closed")
	}

	// Verify baseline run is preserved
	baseName := BaselineRunName(ref.String(), baseSHA, false)
	var baseRun v1alpha1.Run
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "test-ns", Name: baseName}, &baseRun); err != nil {
		t.Errorf("expected baseline run to be preserved, got error: %v", err)
	}

	// Replay close event -> no error
	if err := consumer.ConsumeEvent(ctx, closeEvent); err != nil {
		t.Fatalf("replayed close event failed: %v", err)
	}
}

// TestConsumer_BranchAdvanced_WarmBaseline verifies that branch advanced event warms the baseline cache.
func TestConsumer_BranchAdvanced_WarmBaseline(t *testing.T) {
	ctx := t.Context()
	consumer, cl := newTestConsumer(t)
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	commitTime := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	mainSHA := "3333333333333333333333333333333333333333"

	baEvent := BranchAdvancedEvent{
		Repo:   ref,
		Branch: "main",
		NewSHA: mainSHA,
		Commit: &forge.Commit{
			SHA:   mainSHA,
			Time:  commitTime,
			Title: "main push commit",
		},
	}

	if err := consumer.ConsumeEvent(ctx, baEvent); err != nil {
		t.Fatalf("BranchAdvancedEvent failed: %v", err)
	}

	baseName := BaselineRunName(ref.String(), mainSHA, false)
	var warmRun v1alpha1.Run
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "test-ns", Name: baseName}, &warmRun); err != nil {
		t.Fatalf("expected warm baseline run to exist: %v", err)
	}
	if warmRun.Annotations[AnnotationCommitTime] != commitTime.Format(time.RFC3339) {
		t.Errorf("expected commit time annotation, got %q", warmRun.Annotations[AnnotationCommitTime])
	}

	// Replay branch advanced -> idempotent
	if err := consumer.ConsumeEvent(ctx, baEvent); err != nil {
		t.Fatalf("replaying BranchAdvancedEvent failed: %v", err)
	}
}

// TestConsumer_BaselineCacheReuse verifies that an existing completed baseline run is reused.
func TestConsumer_BaselineCacheReuse(t *testing.T) {
	ctx := t.Context()
	consumer, cl := newTestConsumer(t)
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	baseSHA := "4444444444444444444444444444444444444444"
	headSHA := "5555555555555555555555555555555555555555"
	cloneURL := BuildCloneURL(ref.Owner, ref.Name)

	// Pre-seed completed baseline run
	existingBaseName := "base-preexisting"
	existingBaseRun := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      existingBaseName,
			Namespace: "test-ns",
			Labels: map[string]string{
				LabelHeadSHA: baseSHA,
			},
		},
		Spec: v1alpha1.RunSpec{
			Repo: v1alpha1.RepoSpec{
				URL:    cloneURL,
				Commit: baseSHA,
			},
			Model: "mockllm/model",
		},
		Status: v1alpha1.RunStatus{
			Phase: v1alpha1.RunPhaseCompleted,
		},
	}
	if err := cl.Create(ctx, existingBaseRun); err != nil {
		t.Fatalf("failed to seed completed baseline run: %v", err)
	}

	openEvent := ChangeOpenedEvent{
		Repo: ref,
		Change: forge.ChangeRequest{
			ID:     "303",
			Number: 303,
			Title:  "feat: reuse baseline",
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref: "feat",
				SHA: headSHA,
			},
			Base: forge.ChangeRequestBase{
				Ref: "main",
				SHA: baseSHA,
			},
		},
	}

	if err := consumer.ConsumeEvent(ctx, openEvent); err != nil {
		t.Fatalf("open event failed: %v", err)
	}

	compName := ComparisonName(ref.String(), 303, false)
	var comp v1alpha1.Comparison
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "test-ns", Name: compName}, &comp); err != nil {
		t.Fatalf("comparison not found: %v", err)
	}

	// Verify Comparison reused the existing completed baseline run
	if comp.Spec.Baseline.Run != existingBaseName {
		t.Errorf("expected comparison to reuse %s, got %s", existingBaseName, comp.Spec.Baseline.Run)
	}
}

type fakeRepoStore struct {
	repos map[string]*forge.Repository
}

func (f *fakeRepoStore) GetRepository(repo forge.RepoRef) (*forge.Repository, bool) {
	r, ok := f.repos[repo.String()]
	return r, ok
}

// TestConsumer_EmptyDefaultModel_SkipsCreation verifies that when DefaultModel is empty,
// the consumer logs and creates no Runs or Comparisons.
func TestConsumer_EmptyDefaultModel_SkipsCreation(t *testing.T) {
	ctx := t.Context()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme failed: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	consumer := NewConsumer(cl, ConsumerConfig{
		Namespace:    "test-ns",
		DefaultModel: "", // empty!
	})
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	event := ChangeOpenedEvent{
		Repo: ref,
		Change: forge.ChangeRequest{
			Number: 99,
			State:  "open",
			Head:   forge.ChangeRequestHead{SHA: "1111111111111111111111111111111111111111"},
			Base:   forge.ChangeRequestBase{SHA: "0000000000000000000000000000000000000001"},
		},
	}

	if err := consumer.ConsumeEvent(ctx, event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var runList v1alpha1.RunList
	if err := cl.List(ctx, &runList, client.InNamespace("test-ns")); err != nil {
		t.Fatalf("list runs failed: %v", err)
	}
	if len(runList.Items) != 0 {
		t.Errorf("expected 0 runs when DefaultModel is empty, got %d", len(runList.Items))
	}
}

// TestConsumer_CloneURL_FromStore verifies that the consumer reads CloneURL from the store.
func TestConsumer_CloneURL_FromStore(t *testing.T) {
	ctx := t.Context()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme failed: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	ref := forge.RepoRef{Host: "git-server.svc", Owner: "evals", Name: "custom-repo"}
	storeCloneURL := "git://git-server.svc/evals/custom-repo.git"

	repoStore := &fakeRepoStore{
		repos: map[string]*forge.Repository{
			ref.String(): {
				Ref:      ref,
				CloneURL: storeCloneURL,
			},
		},
	}

	consumer := NewConsumer(cl, ConsumerConfig{
		Namespace:    "test-ns",
		DefaultModel: "mockllm/model",
		Store:        repoStore,
	})

	baEvent := BranchAdvancedEvent{
		Repo:   ref,
		Branch: "main",
		NewSHA: "aaaaaaa111111111111111111111111111111111",
	}

	if err := consumer.ConsumeEvent(ctx, baEvent); err != nil {
		t.Fatalf("BranchAdvancedEvent failed: %v", err)
	}

	baseName := BaselineRunName(ref.String(), "aaaaaaa111111111111111111111111111111111", false)
	var run v1alpha1.Run
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "test-ns", Name: baseName}, &run); err != nil {
		t.Fatalf("run not found: %v", err)
	}

	if run.Spec.Repo.URL != storeCloneURL {
		t.Errorf("expected clone URL %q from store, got %q", storeCloneURL, run.Spec.Repo.URL)
	}
}
