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
	"strings"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/ui"
)

const (
	MaxCommentBytes = 60000
	MaxIDList       = 20
)

// FormatIDList formats a slice of task IDs, truncating to maxIDs if exceeded,
// appending an explicit "... and N more" marker.
func FormatIDList(ids []string, maxIDs int) string {
	if len(ids) == 0 {
		return ""
	}
	if maxIDs <= 0 || len(ids) <= maxIDs {
		return strings.Join(ids, ", ")
	}
	truncated := ids[:maxIDs]
	remaining := len(ids) - maxIDs
	return fmt.Sprintf("%s, ... and %d more", strings.Join(truncated, ", "), remaining)
}

var idListPrefixes = []string{
	"tasks not executed by both runs: ",
	"tasks not executed: ",
	"skipped (not planned): ",
	"candidate tasks failed: ",
	"candidate task failed: ",
}

// TruncateNoteIDLists scans a notes string for task ID lists introduced by standard prefixes
// and truncates each ID list to at most MaxIDList IDs with an explicit "... and N more" marker.
// This provides defence-in-depth for older Comparisons or objects created by external writers
// whose status.message was not bounded by the controller.
func TruncateNoteIDLists(notes string) string {
	if notes == "" {
		return ""
	}

	clauses := strings.Split(notes, "; ")
	modified := false
	for i, clause := range clauses {
		for _, prefix := range idListPrefixes {
			idx := strings.Index(clause, prefix)
			if idx == -1 {
				continue
			}

			before := clause[:idx+len(prefix)]
			idListStr := clause[idx+len(prefix):]

			// If already truncated with the marker, skip
			if strings.Contains(idListStr, "... and ") {
				break
			}

			ids := strings.Split(idListStr, ", ")
			if len(ids) > MaxIDList {
				if prefix == "candidate task failed: " {
					before = clause[:idx] + "candidate tasks failed: "
				}
				clauses[i] = before + FormatIDList(ids, MaxIDList)
				modified = true
			}
			break
		}
	}

	if !modified {
		return notes
	}
	return strings.Join(clauses, "; ")
}

// EscapeCell sanitizes an untrusted string for use in Markdown tables and comment text:
// collapses newlines, HTML-escapes raw tags (<, >), and escapes Markdown formatting characters.
func EscapeCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "`", `\`+"`")
	s = strings.ReplaceAll(s, "*", `\*`)
	s = strings.ReplaceAll(s, "_", `\_`)
	s = strings.ReplaceAll(s, "[", `\[`)
	s = strings.ReplaceAll(s, "]", `\]`)
	return s
}

// ComparisonNotes extracts the single-line notes to render under the verdict in PR comments:
// the suffix after the verdict summary in status.message (e.g. skipped or not-executed tasks),
// or status.message as a whole when the verdict is INCONCLUSIVE or REGRESSED.
func ComparisonNotes(comp *v1alpha1.Comparison) string {
	if comp == nil || comp.Status.Message == "" {
		return ""
	}

	var notes string
	switch comp.Status.Verdict {
	case v1alpha1.ComparisonVerdictInconclusive, v1alpha1.ComparisonVerdictRegressed:
		notes = comp.Status.Message
	default:
		msg := comp.Status.Message
		matched := false
		for _, prefix := range []string{"IMPROVED:", "UNCHANGED:", "RUNNING:"} {
			if strings.HasPrefix(msg, prefix) {
				matched = true
				parts := strings.SplitN(msg, "; ", 2)
				if len(parts) > 1 {
					notes = parts[1]
				}
				break
			}
		}
		if !matched {
			notes = msg
		}
	}
	return TruncateNoteIDLists(notes)
}

// RenderComparisonMarkdown renders a GitHub pull request comment for a Comparison,
// using ui.ComparisonRows and ui.ComparisonSummary so the comment matches the web view.
// If uiBaseURL is provided, the web-view link is rendered as an absolute Markdown link;
// otherwise it renders as code text.
// The rendered body is guaranteed not to exceed maxCommentBytes (60000 bytes).
func RenderComparisonMarkdown(comp *v1alpha1.Comparison, baseRun, candRun *v1alpha1.Run, uiBaseURL string) string {
	if comp == nil {
		return ""
	}

	var header strings.Builder

	// 1. Hidden marker comment
	header.WriteString(ComparisonMarker(comp.Namespace, comp.Name))
	header.WriteString("\n\n")

	// 2. Verdict line
	verdict := string(comp.Status.Verdict)
	if verdict == "" {
		verdict = string(v1alpha1.ComparisonVerdictRunning)
	}
	if comp.Status.PImprovement != nil {
		header.WriteString(fmt.Sprintf("## %s · P(improvement) = %.2f\n\n", verdict, *comp.Status.PImprovement))
	} else {
		header.WriteString(fmt.Sprintf("## %s\n\n", verdict))
	}

	// Notes under the verdict line
	if notes := ComparisonNotes(comp); notes != "" {
		header.WriteString(EscapeCell(notes))
		header.WriteString("\n\n")
	}

	headerStr := header.String()

	// 3. Table
	hasTokens := false
	rows := ui.ComparisonRows(comp)
	for _, row := range rows {
		if row.Tokens != nil {
			hasTokens = true
			break
		}
	}

	var tableHeader string
	if hasTokens {
		tableHeader = "| eval | base | this PR | Δ (95% CI) | label | samples | tokens |\n| --- | --- | --- | --- | --- | --- | --- |\n"
	} else {
		tableHeader = "| eval | base | this PR | Δ (95% CI) | label | samples |\n| --- | --- | --- | --- | --- | --- |\n"
	}

	rowStrings := make([]string, len(rows))
	for i, row := range rows {
		escapedID := EscapeCell(row.ID)
		deltaStr := "—"
		if row.Delta != "—" && row.CILabel != "—" {
			deltaStr = fmt.Sprintf("%s %s", row.Delta, row.CILabel)
		} else if row.Delta != "—" {
			deltaStr = row.Delta
		}

		samplesStr := "—"
		if row.SamplesRun > 0 {
			samplesStr = fmt.Sprintf("%d", row.SamplesRun)
		} else if row.Pairs > 0 {
			samplesStr = fmt.Sprintf("%d", row.Pairs)
		}

		if hasTokens {
			tokensStr := "—"
			if row.Tokens != nil {
				tokensStr = fmt.Sprintf("%d", row.Tokens.Total)
			}
			rowStrings[i] = fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s |\n",
				escapedID,
				row.BaseRate,
				row.CandRate,
				deltaStr,
				row.Label,
				samplesStr,
				tokensStr,
			)
		} else {
			rowStrings[i] = fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n",
				escapedID,
				row.BaseRate,
				row.CandRate,
				deltaStr,
				row.Label,
				samplesStr,
			)
		}
	}

	// 4. Footer & Actions
	var footerBuilder strings.Builder
	summary := ui.ComparisonSummary(comp, baseRun, candRun)
	samplesStr := summary.SamplesSummary
	if samplesStr == "" {
		samplesStr = fmt.Sprintf("%d run · %d paired", summary.TotalSamples, summary.TotalPairs)
	}

	pairedAgainstStr := ""
	if summary.BaseCommitShort != "" {
		pairedAgainstStr = fmt.Sprintf("paired against base commit %s", EscapeCell(summary.BaseCommitShort))
	} else if summary.BaseRun != "" {
		pairedAgainstStr = fmt.Sprintf("paired against base commit %s", EscapeCell(summary.BaseRun))
	} else {
		pairedAgainstStr = "paired against base"
	}

	webPath := fmt.Sprintf("/comparisons/%s/%s", comp.Namespace, comp.Name)
	linkStr := fmt.Sprintf("`%s`", webPath)
	if strings.TrimSpace(uiBaseURL) != "" {
		baseURL := strings.TrimRight(strings.TrimSpace(uiBaseURL), "/")
		linkStr = fmt.Sprintf("[web view](%s%s)", baseURL, webPath)
	}

	footerParts := []string{samplesStr, pairedAgainstStr, linkStr}
	if comp.Status.SummaryPath != "" {
		footerParts = append(footerParts, EscapeCell(comp.Status.SummaryPath))
	}

	footerBuilder.WriteString(strings.Join(footerParts, " · "))
	footerBuilder.WriteString("\n")

	// 5. Actions (e.g. comment `/eval more <n>` to run N more samples for inconclusive tasks)
	if comp != nil {
		actions := ui.ComparisonActions(comp)
		if len(actions) > 0 {
			var lines []string
			actionCount := len(actions)
			limit := actionCount
			if limit > MaxIDList {
				limit = MaxIDList
			}
			for i := 0; i < limit; i++ {
				action := actions[i]
				if action.ShowTaskID && action.TaskID != "" {
					lines = append(lines, fmt.Sprintf("- comment `/eval more %d` to run %d more samples (%s)", action.SuggestedNext, action.SuggestedNext, EscapeCell(action.TaskID)))
				} else {
					lines = append(lines, fmt.Sprintf("- comment `/eval more %d` to run %d more samples", action.SuggestedNext, action.SuggestedNext))
				}
			}
			if actionCount > MaxIDList {
				remaining := actionCount - MaxIDList
				taskWord := "tasks"
				if remaining == 1 {
					taskWord = "task"
				}
				lines = append(lines, fmt.Sprintf("- ... and %d more inconclusive %s (see the Comparison view)", remaining, taskWord))
			}
			footerBuilder.WriteString("\n")
			footerBuilder.WriteString(strings.Join(lines, "\n"))
			footerBuilder.WriteString("\n")
		}
	}
	footerAndActionsStr := footerBuilder.String()

	// Assemble full comment
	var fullSB strings.Builder
	fullSB.WriteString(headerStr)
	fullSB.WriteString(tableHeader)
	for _, rStr := range rowStrings {
		fullSB.WriteString(rStr)
	}
	fullSB.WriteString("\n")
	fullSB.WriteString(footerAndActionsStr)
	fullComment := fullSB.String()

	if len(fullComment) <= MaxCommentBytes {
		return fullComment
	}

	// Body exceeds MaxCommentBytes; drop table rows from the bottom while adding
	// an explicit marker separated by blank lines from table and footer.
	prefixLens := make([]int, len(rowStrings)+1)
	for i := 0; i < len(rowStrings); i++ {
		prefixLens[i+1] = prefixLens[i] + len(rowStrings[i])
	}

	baseLen := len(headerStr) + len(tableHeader) + 1 + 1 + len(footerAndActionsStr) // 1 for \n after table, 1 for \n after marker
	k := 0
	for candidates := len(rowStrings) - 1; candidates >= 0; candidates-- {
		dropped := len(rowStrings) - candidates
		taskWord := "tasks"
		if dropped == 1 {
			taskWord = "task"
		}
		markerLine := fmt.Sprintf("... %d more %s (see the Comparison view)\n", dropped, taskWord)
		if baseLen+prefixLens[candidates]+len(markerLine) <= MaxCommentBytes {
			k = candidates
			break
		}
	}

	dropped := len(rowStrings) - k
	taskWord := "tasks"
	if dropped == 1 {
		taskWord = "task"
	}

	var sb strings.Builder
	sb.WriteString(headerStr)
	sb.WriteString(tableHeader)
	for i := 0; i < k; i++ {
		sb.WriteString(rowStrings[i])
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("... %d more %s (see the Comparison view)\n", dropped, taskWord))
	sb.WriteString("\n")
	sb.WriteString(footerAndActionsStr)
	return sb.String()
}
