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

package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
)

// Reporter handles posting Comparison results to a GitHub pull request and check run.
type Reporter struct {
	Client Client
}

// NewReporter creates a new GitHub Reporter.
func NewReporter(client Client) *Reporter {
	return &Reporter{Client: client}
}

// HashContent computes an idempotency SHA-256 hash across markdown content and check run state.
func HashContent(markdown string, state CheckRunState) string {
	h := sha256.New()
	h.Write([]byte(markdown))
	h.Write([]byte{0})
	h.Write([]byte(state.Status))
	h.Write([]byte{0})
	h.Write([]byte(state.Conclusion))
	h.Write([]byte{0})
	h.Write([]byte(state.Title))
	h.Write([]byte{0})
	h.Write([]byte(state.Summary))
	return hex.EncodeToString(h.Sum(nil))
}

// Report posts or updates the Comparison's PR comment and check run.
// It is idempotent: if the content has not changed since LastPostedHash, no API calls are made.
func (r *Reporter) Report(ctx context.Context, comp *v1alpha1.Comparison, baseRun, candRun *v1alpha1.Run) (*v1alpha1.ComparisonReportingStatus, error) {
	if comp == nil || comp.Spec.PullRequest == nil {
		return nil, nil
	}

	parts := strings.Split(comp.Spec.PullRequest.Repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, &PermanentError{
			StatusCode: 400,
			Message:    fmt.Sprintf("invalid pullRequest.repository %q: expected owner/name", comp.Spec.PullRequest.Repository),
		}
	}
	owner, repo := parts[0], parts[1]

	markdown := RenderComparisonMarkdown(comp, baseRun, candRun)
	checkState := CheckRunStateForComparison(comp, baseRun, candRun)
	hash := HashContent(markdown, checkState)

	commentID := int64(0)
	checkRunID := int64(0)
	if comp.Status.Reporting != nil {
		commentID = comp.Status.Reporting.CommentID
		checkRunID = comp.Status.Reporting.CheckRunID
		if comp.Status.Reporting.LastPostedHash == hash && commentID != 0 {
			// Idempotent: content has not changed
			return comp.Status.Reporting.DeepCopy(), nil
		}
	}

	// 1. Comment upsert
	if commentID != 0 {
		err := r.Client.UpdateIssueComment(ctx, owner, repo, commentID, markdown)
		if err != nil {
			var pe *PermanentError
			if errors.As(err, &pe) && pe.StatusCode == http.StatusNotFound {
				commentID = 0
			} else {
				return nil, err
			}
		}
	}

	if commentID == 0 {
		// Search for marker in existing PR comments
		comments, err := r.Client.ListIssueComments(ctx, owner, repo, comp.Spec.PullRequest.Number)
		if err != nil {
			return nil, err
		}
		for _, c := range comments {
			if HasComparisonMarker(c.Body, comp.Namespace, comp.Name) {
				commentID = c.ID
				break
			}
		}

		if commentID != 0 {
			if err := r.Client.UpdateIssueComment(ctx, owner, repo, commentID, markdown); err != nil {
				return nil, err
			}
		} else {
			id, err := r.Client.CreateIssueComment(ctx, owner, repo, comp.Spec.PullRequest.Number, markdown)
			if err != nil {
				return nil, err
			}
			commentID = id
		}
	}

	partialStatus := &v1alpha1.ComparisonReportingStatus{
		CommentID:      commentID,
		CheckRunID:     checkRunID,
		LastPostedHash: hash,
	}

	// 2. Check run upsert
	headSHA := comp.Spec.PullRequest.HeadSHA
	if headSHA == "" && candRun != nil {
		headSHA = candRun.Spec.Repo.Commit
	}

	if headSHA != "" {
		if checkRunID != 0 {
			err := r.Client.UpdateCheckRun(ctx, owner, repo, checkRunID, checkState)
			if err != nil {
				var pe *PermanentError
				if errors.As(err, &pe) && pe.StatusCode == http.StatusNotFound {
					checkRunID = 0
				} else if errors.As(err, &pe) && pe.StatusCode == http.StatusForbidden {
					partialStatus.Message = "check runs unavailable with token auth; comment posted"
					return partialStatus, nil
				} else {
					return partialStatus, err
				}
			}
		}

		if checkRunID == 0 {
			id, err := r.Client.CreateCheckRun(ctx, owner, repo, headSHA, checkState)
			if err != nil {
				var pe *PermanentError
				if errors.As(err, &pe) && pe.StatusCode == http.StatusForbidden {
					partialStatus.Message = "check runs unavailable with token auth; comment posted"
					return partialStatus, nil
				}
				return partialStatus, err
			}
			checkRunID = id
			partialStatus.CheckRunID = checkRunID
		}
	}

	return partialStatus, nil
}
