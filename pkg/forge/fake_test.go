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
	"testing"
	"time"
)

func TestFakeForge_Lifecycle(t *testing.T) {
	ctx := t.Context()
	ff := NewFakeForge("github.com")

	if ff.Kind() != "github.com" {
		t.Errorf("Kind() = %q, want %q", ff.Kind(), "github.com")
	}

	ref := RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}

	// Repository
	repo := &Repository{
		Ref:           ref,
		DefaultBranch: "main",
		CloneURL:      "https://github.com/gke-labs/eval-runner.git",
	}
	ff.SetRepository(ref, repo)
	gotRepo, err := ff.Repository(ctx, ref)
	if err != nil || gotRepo.DefaultBranch != "main" {
		t.Fatalf("Repository() = %+v, err=%v", gotRepo, err)
	}

	// Branches
	branches := []Branch{
		{Name: "main", SHA: "1111111111111111111111111111111111111111"},
		{Name: "dev", SHA: "2222222222222222222222222222222222222222"},
	}
	ff.SetBranches(ref, branches)
	gotBranches, err := ff.Branches(ctx, ref)
	if err != nil || len(gotBranches) != 2 {
		t.Fatalf("Branches() = %+v, err=%v", gotBranches, err)
	}

	// ChangeRequests
	now := time.Now()
	crs := []ChangeRequest{
		{
			ID:     "42",
			Number: 42,
			Title:  "feat: something",
			Author: "contributor",
			Head: ChangeRequestHead{
				Ref:     "feat",
				SHA:     "3333333333333333333333333333333333333333",
				RepoRef: ref,
			},
			Base: ChangeRequestBase{
				Ref: "main",
				SHA: "1111111111111111111111111111111111111111",
			},
			State:     "open",
			UpdatedAt: now,
		},
	}
	ff.SetChangeRequests(ref, crs)
	gotCRs, err := ff.ChangeRequests(ctx, ref)
	if err != nil || len(gotCRs) != 1 || gotCRs[0].Number != 42 {
		t.Fatalf("ChangeRequests() = %+v, err=%v", gotCRs, err)
	}

	// Commit
	commit := &Commit{
		SHA:     "1111111111111111111111111111111111111111",
		Parents: []string{"0000000000000000000000000000000000000000"},
		Time:    now,
		Title:   "Initial commit",
	}
	ff.SetCommit(ref, commit)
	gotCommit, err := ff.Commit(ctx, ref, commit.SHA)
	if err != nil || gotCommit.Title != "Initial commit" {
		t.Fatalf("Commit() = %+v, err=%v", gotCommit, err)
	}

	// Reporter: UpsertComment
	key := "marker-key"
	body := "Report comment body"
	commentID, err := ff.UpsertComment(ctx, ref, "42", key, body)
	if err != nil {
		t.Fatalf("UpsertComment() err=%v", err)
	}
	if commentID <= 0 {
		t.Errorf("commentID = %d, want > 0", commentID)
	}
	gotBody, ok := ff.GetComment(ref, "42", key)
	if !ok || gotBody != body {
		t.Errorf("GetComment() = %q, %v, want %q, true", gotBody, ok, body)
	}
	if calls := ff.GetUpsertCalls(); len(calls) != 1 || calls[0].Key != key {
		t.Errorf("GetUpsertCalls() = %+v", calls)
	}

	// Reporter: SetStatus
	status := Status{
		State:       "success",
		Context:     "eval-runner/test",
		Description: "Passed 10/10",
		TargetURL:   "https://eval-runner.example.com",
	}
	statusID, err := ff.SetStatus(ctx, ref, commit.SHA, status)
	if err != nil {
		t.Fatalf("SetStatus() err=%v", err)
	}
	if statusID <= 0 {
		t.Errorf("statusID = %d, want > 0", statusID)
	}
	gotStatus, ok := ff.GetStatus(ref, commit.SHA, status.Context)
	if !ok || gotStatus.State != "success" {
		t.Errorf("GetStatus() = %+v, %v, want %+v, true", gotStatus, ok, status)
	}
	if calls := ff.GetSetStatusCalls(); len(calls) != 1 || calls[0].SHA != commit.SHA {
		t.Errorf("GetSetStatusCalls() = %+v", calls)
	}
}

func TestRegistry_Routing(t *testing.T) {
	ghForge := NewFakeForge("github.com")
	remoteForge := NewFakeForge("gitremote")

	reg := NewDefaultRegistry(ghForge, remoteForge)

	ghRef := RepoRef{Host: "github.com", Owner: "gke-labs", Name: "eval-runner"}
	if got := reg.ForRepo(ghRef); got != ghForge {
		t.Errorf("ForRepo(github.com) = %v, want %v", got, ghForge)
	}

	glRef := RepoRef{Host: "gitlab.com", Owner: "team", Name: "project"}
	if got := reg.ForRepo(glRef); got != remoteForge {
		t.Errorf("ForRepo(gitlab.com) = %v, want %v", got, remoteForge)
	}

	bareRef := RepoRef{Host: "git-server.local", Owner: "-", Name: "docs-eval"}
	if got := reg.ForRepo(bareRef); got != remoteForge {
		t.Errorf("ForRepo(git-server.local) = %v, want %v", got, remoteForge)
	}

	// Register custom host
	customForge := NewFakeForge("gitlab.com")
	reg.Register("gitlab.com", customForge)
	if got := reg.ForRepo(glRef); got != customForge {
		t.Errorf("ForRepo(gitlab.com after Register) = %v, want %v", got, customForge)
	}
}
