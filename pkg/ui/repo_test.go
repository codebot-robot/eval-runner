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
	"testing"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/forge/consumer"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRunBelongsToRepo(t *testing.T) {
	targetRef := RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	forkRef := RepoRef{Host: "github.com", Owner: "codebot-robot", Name: "eval-runner"}
	otherRef := RepoRef{Host: "github.com", Owner: "other-org", Name: "other-repo"}

	t.Run("nil run or zero ref", func(t *testing.T) {
		if runBelongsToRepo(nil, targetRef) {
			t.Errorf("expected false for nil run")
		}
		run := &v1alpha1.Run{}
		if runBelongsToRepo(run, RepoRef{}) {
			t.Errorf("expected false for zero RepoRef")
		}
	})

	t.Run("label match", func(t *testing.T) {
		// Fork candidate run with target-repo label matching target repository
		run := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pr-391-cand",
				Labels: map[string]string{
					consumer.LabelRepo:       "codebot-robot_eval-runner",
					consumer.LabelTargetRepo: "gke-labs_eval-runner",
				},
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					URL: "https://github.com/codebot-robot/eval-runner.git",
				},
			},
		}

		if !runBelongsToRepo(run, targetRef) {
			t.Errorf("expected run with target-repo label to match target repository")
		}
		// Also still matches fork repository via runRepoRef
		if !runBelongsToRepo(run, forkRef) {
			t.Errorf("expected run with fork source repo to match fork repository")
		}
		// Does not match unrelated repository
		if runBelongsToRepo(run, otherRef) {
			t.Errorf("expected run to not match unrelated repository")
		}
	})

	t.Run("source match", func(t *testing.T) {
		// Run without target-repo label, but source repo matches target repository
		run := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name: "base-run",
				Labels: map[string]string{
					consumer.LabelRepo: "gke-labs_eval-runner",
				},
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					URL: "https://github.com/gke-labs/eval-runner.git",
				},
			},
		}

		if !runBelongsToRepo(run, targetRef) {
			t.Errorf("expected run matching source repo to match target repository")
		}
		if runBelongsToRepo(run, forkRef) {
			t.Errorf("expected run not to match fork repository")
		}
	})

	t.Run("Comparison fallback", func(t *testing.T) {
		// Pre-change run: no target-repo label, source repo points to fork
		run := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name: "legacy-cand-run",
				Labels: map[string]string{
					consumer.LabelRepo: "codebot-robot_eval-runner",
				},
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					URL: "https://github.com/codebot-robot/eval-runner.git",
				},
			},
		}

		// Comparison in the namespace referencing the run as candidate for target repo
		comp := v1alpha1.Comparison{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pr-391",
			},
			Spec: v1alpha1.ComparisonSpec{
				PullRequest: &v1alpha1.PullRequestSpec{
					Repository: "gke-labs/eval-runner",
					Number:     391,
				},
				Candidate: v1alpha1.ComparisonRunRef{
					Run: "legacy-cand-run",
				},
				Baseline: v1alpha1.ComparisonRunRef{
					Run: "legacy-base-run",
				},
			},
		}

		// Without comps, it does not match targetRef
		if runBelongsToRepo(run, targetRef) {
			t.Errorf("expected false without comparison fallback")
		}
		// With comps, fallback resolves it to targetRef
		if !runBelongsToRepo(run, targetRef, comp) {
			t.Errorf("expected true via Comparison candidate fallback")
		}

		// Also verify baseline fallback
		baseRun := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name: "legacy-base-run",
			},
		}
		if !runBelongsToRepo(baseRun, targetRef, comp) {
			t.Errorf("expected true via Comparison baseline fallback")
		}

		// Also verify batch run with comparison label fallback
		batchRun := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name: "legacy-cand-b2",
				Labels: map[string]string{
					"evalrunner.gke-labs.dev/comparison": "pr-391",
				},
			},
		}
		if !runBelongsToRepo(batchRun, targetRef, comp) {
			t.Errorf("expected true via Comparison parent label fallback")
		}
	})

	t.Run("no match", func(t *testing.T) {
		// Run from unrelated repo with no target-repo and unrelated comparison
		run := &v1alpha1.Run{
			ObjectMeta: metav1.ObjectMeta{
				Name: "unrelated-run",
				Labels: map[string]string{
					consumer.LabelRepo: "unrelated_repo",
				},
			},
			Spec: v1alpha1.RunSpec{
				Repo: v1alpha1.RepoSpec{
					URL: "https://github.com/unrelated/repo.git",
				},
			},
		}

		comp := v1alpha1.Comparison{
			ObjectMeta: metav1.ObjectMeta{
				Name: "other-comp",
			},
			Spec: v1alpha1.ComparisonSpec{
				PullRequest: &v1alpha1.PullRequestSpec{
					Repository: "other-org/other-repo",
					Number:     10,
				},
				Candidate: v1alpha1.ComparisonRunRef{
					Run: "unrelated-run",
				},
			},
		}

		if runBelongsToRepo(run, targetRef, comp) {
			t.Errorf("expected false for completely unrelated run and comparison")
		}
	})
}
