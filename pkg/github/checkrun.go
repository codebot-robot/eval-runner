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
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/configkeys"
	"github.com/gke-labs/eval-runner/pkg/ui"
)

// CheckRunName is the standardized check run name for eval verdicts.
const CheckRunName = "eval-runner / evals"

// CheckRunState represents the payload state of a GitHub Check Run.
type CheckRunState struct {
	Name       string
	Status     string // "in_progress" or "completed"
	Conclusion string // "success", "neutral", "failure", or "" (when in_progress)
	Title      string // short state summary
	Summary    string // detailed summary text
	Text       string // technical details text (e.g. collapsed error details)
	DetailsURL string // URL of the details page
}

// ComparisonWebPath returns the web path for a Comparison:
// /{host}/{owner}/{repo}/pulls/{number} when a pull request spec is present,
// /{host}/{owner}/{repo}/comparisons/{name} when repo identity is resolved,
// or /comparisons/{namespace}/{name} as fallback.
func ComparisonWebPath(comp *v1alpha1.Comparison, ref ui.RepoRef) string {
	if comp == nil {
		return ""
	}
	if comp.Spec.PullRequest != nil && comp.Spec.PullRequest.Number > 0 {
		if ref.Host != "" {
			return ui.RepoLink(ref, fmt.Sprintf("/pulls/%d", comp.Spec.PullRequest.Number), "")
		}
	}
	if ref.Host != "" {
		return ui.RepoLink(ref, fmt.Sprintf("/comparisons/%s", comp.Name), "")
	}
	return fmt.Sprintf("/comparisons/%s/%s", comp.Namespace, comp.Name)
}

// CheckRunDetailsURL returns the URL for the check run Details link:
// {uiBaseURL}/{host}/{owner}/{repo}/pulls/{number}; falling back to the comparison page
// only when the Comparison has no pull-request spec.
func CheckRunDetailsURL(comp *v1alpha1.Comparison, uiBaseURL string, runs ...*v1alpha1.Run) string {
	if comp == nil || strings.TrimSpace(uiBaseURL) == "" {
		return ""
	}
	trimmed := strings.TrimRight(strings.TrimSpace(uiBaseURL), "/")
	ref, _ := ui.ComparisonRepoRef(comp, runs...)
	return trimmed + ComparisonWebPath(comp, ref)
}

var (
	uriRegex      = regexp.MustCompile(`(?:gs|s3|file)://[^\s)":',]+`)
	localDirRegex = regexp.MustCompile(`(?:/tmp|/private/tmp|/var/tmp)/[^\s)":',]+`)
)

// SanitizeArtifactPaths replaces known on-disk local directory paths and storage URIs
// with only the artifact name (e.g. "log.json"), preventing internal deployment details
// or /tmp/ and gs:// paths from leaking into user-facing output.
func SanitizeArtifactPaths(s string, runs ...*v1alpha1.Run) string {
	if s == "" {
		return ""
	}
	for _, run := range runs {
		if run == nil {
			continue
		}
		for _, task := range run.Status.Tasks {
			if task.LogPath != "" {
				s = strings.ReplaceAll(s, task.LogPath, path.Base(task.LogPath))
			}
		}
	}
	s = uriRegex.ReplaceAllStringFunc(s, func(m string) string {
		return path.Base(m)
	})
	s = localDirRegex.ReplaceAllStringFunc(s, func(m string) string {
		return filepath.Base(m)
	})
	return s
}

// IsScoringFailure reports whether a Comparison cannot be scored due to missing
// or unreadable artifacts (ConditionTypeArtifactsReadable is False).
func IsScoringFailure(comp *v1alpha1.Comparison) bool {
	if comp == nil {
		return false
	}
	return meta.IsStatusConditionFalse(comp.Status.Conditions, v1alpha1.ConditionTypeArtifactsReadable)
}

// ScoringFailureDetails extracts the user-facing headline and sanitized technical error
// when scoring fails for a Comparison.
func ScoringFailureDetails(comp *v1alpha1.Comparison, baseRun, candRun *v1alpha1.Run) (headline string, techError string) {
	if comp == nil {
		return "", ""
	}
	msgLower := strings.ToLower(comp.Status.Message)
	isCand := strings.HasPrefix(msgLower, "failed to load candidate") ||
		strings.HasPrefix(msgLower, "candidate log path") ||
		strings.HasPrefix(msgLower, "failed to parse candidate")

	role := "Baseline"
	target := "baseline"
	if isCand {
		role = "Candidate"
		target = "candidate"
	}

	var shortSHA string
	if !isCand {
		if comp.Spec.PullRequest != nil && comp.Spec.PullRequest.BaseSHA != "" {
			shortSHA = comp.Spec.PullRequest.BaseSHA
		} else if baseRun != nil && baseRun.Spec.Repo.Commit != "" {
			shortSHA = baseRun.Spec.Repo.Commit
		}
	} else {
		if comp.Spec.PullRequest != nil && comp.Spec.PullRequest.HeadSHA != "" {
			shortSHA = comp.Spec.PullRequest.HeadSHA
		} else if candRun != nil && candRun.Spec.Repo.Commit != "" {
			shortSHA = candRun.Spec.Repo.Commit
		}
	}
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}

	if shortSHA != "" {
		headline = fmt.Sprintf("%s results for `%s` are unavailable; eval-runner will need to re-run the %s.", role, shortSHA, target)
	} else {
		headline = fmt.Sprintf("%s results are unavailable; eval-runner will need to re-run the %s.", role, target)
	}

	techError = SanitizeArtifactPaths(comp.Status.Message, baseRun, candRun)
	return headline, techError
}

// PlannedSamples returns the planned total samples for a Run, computed as limit x tasks.
// If limit or task count cannot be determined, it returns 0.
func PlannedSamples(run *v1alpha1.Run) int {
	if run == nil {
		return 0
	}
	limit := 0
	if run.Spec.Limit != nil && *run.Spec.Limit > 0 {
		limit = *run.Spec.Limit
	}
	if limit <= 0 {
		return 0
	}
	tasks := 0
	if len(run.Spec.Tasks) > 0 {
		tasks = len(run.Spec.Tasks)
	} else if run.Status.Discovery != nil && run.Status.Discovery.EligibleTaskCount > 0 {
		tasks = run.Status.Discovery.EligibleTaskCount
	} else if len(run.Status.Tasks) > 0 {
		tasks = len(run.Status.Tasks)
	}
	return limit * tasks
}

// taskLabelCounts builds the comma-separated per-label count string:
// "N improved, M unchanged, K regressed, J inconclusive" from comp.Status.Tasks,
// omitting zero buckets. Returns "" if comp is nil, tasks is empty, or all counts are zero.
func taskLabelCounts(comp *v1alpha1.Comparison) string {
	if comp == nil || len(comp.Status.Tasks) == 0 {
		return ""
	}
	var numImproved, numUnchanged, numRegressed, numInconclusive int
	for _, task := range comp.Status.Tasks {
		switch task.Label {
		case v1alpha1.ComparisonTaskLabelImproved:
			numImproved++
		case v1alpha1.ComparisonTaskLabelUnchanged:
			numUnchanged++
		case v1alpha1.ComparisonTaskLabelRegressed:
			numRegressed++
		case v1alpha1.ComparisonTaskLabelInconclusive:
			numInconclusive++
		}
	}
	var parts []string
	if numImproved > 0 {
		parts = append(parts, fmt.Sprintf("%d improved", numImproved))
	}
	if numUnchanged > 0 {
		parts = append(parts, fmt.Sprintf("%d unchanged", numUnchanged))
	}
	if numRegressed > 0 {
		parts = append(parts, fmt.Sprintf("%d regressed", numRegressed))
	}
	if numInconclusive > 0 {
		parts = append(parts, fmt.Sprintf("%d inconclusive", numInconclusive))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

// CheckRunStateForComparison maps a Comparison and its candidate/baseline Runs
// to the CheckRunState for the "eval-runner / evals" check run across all 5 verdicts.
func CheckRunStateForComparison(comp *v1alpha1.Comparison, baseRun, candRun *v1alpha1.Run, inconclusivePolicy string, uiBaseURL string) CheckRunState {
	state := CheckRunState{
		Name: CheckRunName,
	}
	if comp == nil {
		state.Status = "in_progress"
		state.Title = "running"
		state.Summary = "running"
		return state
	}

	policy := configkeys.InconclusivePolicyNeutral
	if inconclusivePolicy != "" {
		policy = strings.ToLower(strings.TrimSpace(inconclusivePolicy))
	}

	if IsScoringFailure(comp) {
		headline, techErr := ScoringFailureDetails(comp, baseRun, candRun)
		state.Status = "completed"
		if policy == configkeys.InconclusivePolicyFailure {
			state.Conclusion = "failure"
		} else {
			state.Conclusion = "neutral"
		}
		state.Title = headline
		state.Summary = headline
		state.Text = techErr
		state.DetailsURL = CheckRunDetailsURL(comp, uiBaseURL, baseRun, candRun)
		return state
	}

	counts := taskLabelCounts(comp)

	switch comp.Status.Verdict {
	case v1alpha1.ComparisonVerdictImproved:
		state.Status = "completed"
		state.Conclusion = "success"
		if counts != "" {
			state.Title = fmt.Sprintf("%s: %s", comp.Status.Verdict, counts)
		} else if comp.Status.PImprovement != nil {
			state.Title = fmt.Sprintf("IMPROVED · P(improvement) = %.2f", *comp.Status.PImprovement)
		} else {
			state.Title = "IMPROVED"
		}
		state.Summary = state.Title

	case v1alpha1.ComparisonVerdictUnchanged:
		state.Status = "completed"
		state.Conclusion = "success"
		if counts != "" {
			state.Title = fmt.Sprintf("%s: %s", comp.Status.Verdict, counts)
		} else {
			state.Title = "UNCHANGED"
		}
		state.Summary = state.Title

	case v1alpha1.ComparisonVerdictInconclusive:
		state.Status = "completed"
		if policy == configkeys.InconclusivePolicyFailure {
			state.Conclusion = "failure"
		} else {
			state.Conclusion = "neutral"
		}
		if counts != "" {
			state.Title = fmt.Sprintf("%s: %s", comp.Status.Verdict, counts)
		} else {
			state.Title = "INCONCLUSIVE · more samples recommended"
		}
		state.Summary = state.Title

	case v1alpha1.ComparisonVerdictRegressed:
		state.Status = "completed"
		state.Conclusion = "failure"
		if counts != "" {
			state.Title = fmt.Sprintf("%s: %s", comp.Status.Verdict, counts)
		} else {
			state.Title = "REGRESSED"
		}
		state.Summary = state.Title

	case v1alpha1.ComparisonVerdictRunning:
		fallthrough
	default:
		state.Status = "in_progress"
		state.Conclusion = ""

		// Calculate N (completed samples) and M (total planned samples: limit x tasks)
		completed := 0
		total := 0
		if candRun != nil {
			if candRun.Status.Summary != nil {
				completed += candRun.Status.Summary.TotalSamples
			}
			total += PlannedSamples(candRun)
		}
		if baseRun != nil {
			if baseRun.Status.Summary != nil {
				completed += baseRun.Status.Summary.TotalSamples
			}
			total += PlannedSamples(baseRun)
		}

		if total > 0 {
			state.Title = fmt.Sprintf("running · %d of %d samples", completed, total)
		} else if completed > 0 {
			state.Title = fmt.Sprintf("running · %d samples", completed)
		} else {
			state.Title = "running"
		}
		state.Summary = state.Title
	}

	if comp.Status.Message != "" {
		state.Summary = EscapeCell(comp.Status.Message)
	}

	var budgetLine string
	if comp.Status.Budget != nil && comp.Status.Budget.LimitUSD != nil && *comp.Status.Budget.LimitUSD > 0 {
		spent := 0.0
		if comp.Status.Budget.SpentUSD != nil {
			spent = *comp.Status.Budget.SpentUSD
		} else if comp.Status.CostUSD != nil {
			spent = *comp.Status.CostUSD
		}
		budgetLine = fmt.Sprintf("budget $%.2f / $%.2f", spent, *comp.Status.Budget.LimitUSD)
		if !comp.Status.Budget.Enforced {
			budgetLine += " (not enforced)"
		}
	} else if comp.Spec.BudgetUSD != nil && *comp.Spec.BudgetUSD > 0 {
		spent := 0.0
		if comp.Status.CostUSD != nil {
			spent = *comp.Status.CostUSD
		}
		budgetLine = fmt.Sprintf("budget $%.2f / $%.2f", spent, *comp.Spec.BudgetUSD)
	}

	if budgetLine != "" {
		if state.Summary != "" {
			state.Summary = state.Summary + "\n\n" + budgetLine
		} else {
			state.Summary = budgetLine
		}
	}

	lines := ActionHintLines(comp)
	if len(lines) > 0 {
		if state.Summary != "" {
			state.Summary = state.Summary + "\n\n" + strings.Join(lines, "\n")
		} else {
			state.Summary = strings.Join(lines, "\n")
		}
	}

	state.DetailsURL = CheckRunDetailsURL(comp, uiBaseURL, baseRun, candRun)

	return state
}
