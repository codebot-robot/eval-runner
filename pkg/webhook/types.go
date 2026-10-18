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

package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

const (
	// LabelPR tracks the pull request number on created Runs and Comparisons.
	LabelPR = "evalrunner.gke-labs.dev/pr"

	// LabelRepo tracks the repository identifier on created Runs and Comparisons.
	LabelRepo = "evalrunner.gke-labs.dev/repo"

	// LabelHeadSHA tracks the git commit SHA on created Runs and Comparisons.
	LabelHeadSHA = "evalrunner.gke-labs.dev/head-sha"

	// HeaderHubSignature256 is the GitHub webhook HMAC-SHA256 signature header.
	HeaderHubSignature256 = "X-Hub-Signature-256"

	// HeaderGitHubEvent is the GitHub webhook event type header.
	HeaderGitHubEvent = "X-GitHub-Event"

	// HeaderGitHubDelivery is the GitHub webhook delivery UUID header.
	HeaderGitHubDelivery = "X-GitHub-Delivery"

	// EventPullRequest is the GitHub event name for pull requests.
	EventPullRequest = "pull_request"

	// EventPush is the GitHub event name for git pushes.
	EventPush = "push"

	// EventPing is the GitHub event name for webhook ping.
	EventPing = "ping"

	// ActionOpened is the pull_request action when a PR is opened.
	ActionOpened = "opened"

	// ActionSynchronize is the pull_request action when new commits are pushed to a PR.
	ActionSynchronize = "synchronize"

	// ActionReopened is the pull_request action when a closed PR is reopened.
	ActionReopened = "reopened"
)

// GitHubRepository represents repository metadata in GitHub webhook payloads.
type GitHubRepository struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"` // "owner/name"
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// GitHubCommitRef represents a git commit reference (head or base) in a pull request.
type GitHubCommitRef struct {
	SHA  string `json:"sha"`
	Ref  string `json:"ref"`
	Repo struct {
		CloneURL string `json:"clone_url"`
		FullName string `json:"full_name"`
	} `json:"repo"`
}

// GitHubPullRequestData represents pull request details in GitHub webhook payloads.
type GitHubPullRequestData struct {
	Number int             `json:"number"`
	State  string          `json:"state"`
	Head   GitHubCommitRef `json:"head"`
	Base   GitHubCommitRef `json:"base"`
}

// GitHubPullRequestEvent represents the payload of a GitHub pull_request webhook event.
type GitHubPullRequestEvent struct {
	Action      string                `json:"action"`
	Number      int                   `json:"number"`
	PullRequest GitHubPullRequestData `json:"pull_request"`
	Repository  GitHubRepository      `json:"repository"`
}

// GitHubPushEvent represents the payload of a GitHub push webhook event.
type GitHubPushEvent struct {
	Ref        string           `json:"ref"`    // e.g. "refs/heads/main"
	Before     string           `json:"before"` // previous commit SHA
	After      string           `json:"after"`  // new commit SHA
	Deleted    bool             `json:"deleted"`
	Repository GitHubRepository `json:"repository"`
	HeadCommit *struct {
		ID string `json:"id"`
	} `json:"head_commit"`
}

// ShortSHA returns the first 7 characters of a git commit SHA in lowercase.
// If the SHA is shorter than 7 characters, it is returned trimmed and in lowercase.
func ShortSHA(sha string) string {
	s := strings.ToLower(strings.TrimSpace(sha))
	if len(s) <= 7 {
		return s
	}
	return s[:7]
}

// SanitizeLabelValue converts an arbitrary string (such as "owner/name") into a valid
// Kubernetes label value:
// - Max 63 characters
// - Characters must be [a-zA-Z0-9_.-]
// - Must start and end with an alphanumeric character ([a-zA-Z0-9])
func SanitizeLabelValue(val string) string {
	val = strings.TrimSpace(val)
	if val == "" {
		return ""
	}

	var sb strings.Builder
	for _, r := range val {
		switch {
		case r == '/':
			sb.WriteRune('_')
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}

	s := sb.String()
	// Trim non-alphanumeric from edges
	s = strings.TrimFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	if len(s) > 63 {
		s = s[:63]
		s = strings.TrimFunc(s, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
	}
	return s
}

// VerifySignature verifies that the payload matches the X-Hub-Signature-256 header
// using HMAC-SHA256 and constant-time comparison.
func VerifySignature(payload, secret []byte, signatureHeader string) bool {
	if len(secret) == 0 || signatureHeader == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return false
	}
	hexSig := strings.TrimPrefix(signatureHeader, prefix)
	expectedMAC, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	actualMAC := mac.Sum(nil)

	return hmac.Equal(actualMAC, expectedMAC)
}
