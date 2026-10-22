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

package controller

import (
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/forge"
	"github.com/gke-labs/eval-runner/pkg/forge/syncer"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestController_Consumer_LifecycleAndReplay verifies the consumer within the controller
// handles open, update, close, branch advance, baseline reuse, and replay idempotency.
func TestController_Consumer_LifecycleAndReplay(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add v1alpha1 to scheme: %v", err)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	ns := "eval-runner"

	cfg := syncer.ConsumerConfig{
		Namespace:                ns,
		DefaultModel:             "mockllm/model",
		DefaultCohort:            "default",
		DefaultBatchSize:         8,
		DefaultMaxSamplesPerTask: 32,
		TrackedRepos: []syncer.RepoConfig{
			{
				Name:     "gke-labs/eval-runner",
				Branches: []string{"main"},
			},
		},
	}
	consumer := syncer.NewConsumer(cl, cfg)
	ctx := t.Context()
	ref := forge.RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	baseSHA := "0000000000000000000000000000000000000001"
	headSHA1 := "1111111111111111111111111111111111111111"
	headSHA2 := "2222222222222222222222222222222222222222"

	// 1. BranchAdvancedEvent for main branch warms the baseline cache
	pushEvent := syncer.BranchAdvancedEvent{
		Repo:   ref,
		Branch: "main",
		NewSHA: baseSHA,
		Commit: &forge.Commit{
			SHA:   baseSHA,
			Time:  time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC),
			Title: "feat: baseline commit",
		},
	}
	if err := consumer.ConsumeEvent(ctx, pushEvent); err != nil {
		t.Fatalf("BranchAdvancedEvent failed: %v", err)
	}

	baseName := syncer.BaselineRunName("gke-labs/eval-runner", baseSHA, false)
	var baseRun v1alpha1.Run
	if err := cl.Get(ctx, types.NamespacedName{Namespace: ns, Name: baseName}, &baseRun); err != nil {
		t.Fatalf("baseline run not found: %v", err)
	}
	// Mark baseline run as Completed to test cache reuse
	baseRun.Status.Phase = v1alpha1.RunPhaseCompleted
	if err := cl.Update(ctx, &baseRun); err != nil {
		t.Fatalf("failed to mark baseline run completed: %v", err)
	}

	// 2. ChangeOpenedEvent for PR #42
	openEvent := syncer.ChangeOpenedEvent{
		Repo: ref,
		Change: forge.ChangeRequest{
			ID:     "42",
			Number: 42,
			Title:  "feat: sample pr",
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref: "feature-branch",
				SHA: headSHA1,
			},
			Base: forge.ChangeRequestBase{
				Ref: "main",
				SHA: baseSHA,
			},
		},
	}
	if err := consumer.ConsumeEvent(ctx, openEvent); err != nil {
		t.Fatalf("ChangeOpenedEvent failed: %v", err)
	}

	compName := syncer.ComparisonName("gke-labs/eval-runner", 42, false)
	var comp v1alpha1.Comparison
	if err := cl.Get(ctx, types.NamespacedName{Namespace: ns, Name: compName}, &comp); err != nil {
		t.Fatalf("Comparison not found: %v", err)
	}
	candName1 := syncer.CandidateRunName("gke-labs/eval-runner", 42, headSHA1, false)
	if comp.Spec.Candidate.Run != candName1 {
		t.Errorf("expected comparison candidate run %s, got %s", candName1, comp.Spec.Candidate.Run)
	}
	if comp.Spec.Baseline.Run != baseName {
		t.Errorf("expected comparison baseline run %s (reused), got %s", baseName, comp.Spec.Baseline.Run)
	}

	// 3. ChangeUpdatedEvent for PR #42 with headSHA2
	updateEvent := syncer.ChangeUpdatedEvent{
		Repo:       ref,
		OldHeadSHA: headSHA1,
		Change: forge.ChangeRequest{
			ID:     "42",
			Number: 42,
			Title:  "feat: sample pr updated",
			State:  "open",
			Head: forge.ChangeRequestHead{
				Ref: "feature-branch",
				SHA: headSHA2,
			},
			Base: forge.ChangeRequestBase{
				Ref: "main",
				SHA: baseSHA,
			},
		},
	}
	if err := consumer.ConsumeEvent(ctx, updateEvent); err != nil {
		t.Fatalf("ChangeUpdatedEvent failed: %v", err)
	}

	if err := cl.Get(ctx, types.NamespacedName{Namespace: ns, Name: compName}, &comp); err != nil {
		t.Fatalf("Comparison not found after update: %v", err)
	}
	candName2 := syncer.CandidateRunName("gke-labs/eval-runner", 42, headSHA2, false)
	if comp.Spec.Candidate.Run != candName2 {
		t.Errorf("expected updated comparison candidate run %s, got %s", candName2, comp.Spec.Candidate.Run)
	}

	// 4. Replay update event -> idempotent
	if err := consumer.ConsumeEvent(ctx, updateEvent); err != nil {
		t.Fatalf("replayed ChangeUpdatedEvent failed: %v", err)
	}

	// 5. ChangeClosedEvent for PR #42 cleans up Comparison and candidate Run
	closeEvent := syncer.ChangeClosedEvent{
		Repo: ref,
		Change: forge.ChangeRequest{
			ID:     "42",
			Number: 42,
			State:  "closed",
		},
	}
	if err := consumer.ConsumeEvent(ctx, closeEvent); err != nil {
		t.Fatalf("ChangeClosedEvent failed: %v", err)
	}

	var compAfter v1alpha1.Comparison
	if err := cl.Get(ctx, types.NamespacedName{Namespace: ns, Name: compName}, &compAfter); err == nil {
		t.Errorf("expected Comparison to be deleted after PR closed")
	}

	// Completed baseline run is preserved
	if err := cl.Get(ctx, types.NamespacedName{Namespace: ns, Name: baseName}, &baseRun); err != nil {
		t.Errorf("expected baseline run %s to be preserved, got error: %v", baseName, err)
	}

	// Replay close event -> idempotent
	if err := consumer.ConsumeEvent(ctx, closeEvent); err != nil {
		t.Fatalf("replayed ChangeClosedEvent failed: %v", err)
	}
}
