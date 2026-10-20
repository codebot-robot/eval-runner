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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NavTab represents the current active tab in the UI header.
type NavTab string

const (
	NavTabPulls       NavTab = "pulls"
	NavTabRuns        NavTab = "runs"
	NavTabComparisons NavTab = "comparisons"
	NavTabSeries      NavTab = "series"
)

// RunListItem represents a single run row in the /runs list view.
type RunListItem struct {
	Name        string
	Namespace   string
	Phase       v1alpha1.RunPhase
	PhaseChip   string // good, bad, warn, neutral, pending
	Tasks       string // e.g. "2/2"
	Samples     int
	SampleRange string // compact, e.g. "samples 9-16"
	Rates       string
	Commit      string // short commit
	Age         string // e.g. "14m"
	Link        string // "/runs/<ns>/<name>"
}

// RunsListView is the view model for the /runs list page.
type RunsListView struct {
	ActiveTab    NavTab
	Namespace    string
	Runs         []RunListItem
	TotalCount   int
	Offset       int
	Limit        int
	HasMore      bool
	NextLink     string
	HasPrev      bool
	PrevLink     string
	ShowingRange string
}

// RunTaskRow represents a single eval task in the /runs/<ns>/<name> detail view.
type RunTaskRow struct {
	ID             string
	Phase          v1alpha1.TaskPhase
	PhaseChip      string // good, bad, warn, neutral, pending
	RateStr        string // e.g. "0.84"
	HasCI          bool
	CILeftPx       float64
	CIWidthPx      float64
	CIDotPx        float64
	CILabel        string // e.g. "[0.72, 0.95]"
	Samples        int
	Successes      int
	Method         string // e.g. "wilson", "t"
	LogPath        string // text path or URI
	SamplesLink    string // e.g. "/runs/<ns>/<name>/tasks/<task>/samples"
	Message        string
	SampleRange    string // e.g. "samples 9-16"
	SamplesDone    int
	SamplesPlanned int
	ProgressPct    float64
	ProgressText   string // e.g. "8 of 96 samples"
}

// LogURL returns a browsable web URL for the task's LogPath if it is a gs:// or http(s):// URI.
func (r RunTaskRow) LogURL() string {
	return FormatLogURL(r.LogPath)
}

// ArtifactsURL returns a browsable web URL for the run's ArtifactsPrefix if it is a gs:// or http(s):// URI.
func (v RunDetailView) ArtifactsURL() string {
	return FormatLogURL(v.ArtifactsPrefix)
}

// FormatLogURL converts a storage URI or URL to a clickable console or web link.
// For gs:// URIs, each path segment is URL-escaped to preserve characters such as #, ?, or %.
func FormatLogURL(uri string) string {
	uri = strings.TrimSpace(uri)
	if strings.HasPrefix(uri, "gs://") {
		trimmed := strings.TrimPrefix(uri, "gs://")
		isDir := strings.HasSuffix(trimmed, "/")
		segs := strings.Split(trimmed, "/")
		for i, s := range segs {
			segs[i] = url.PathEscape(s)
		}
		if isDir {
			return "https://console.cloud.google.com/storage/browser/" + strings.Join(segs, "/")
		}
		return "https://console.cloud.google.com/storage/browser/_details/" + strings.Join(segs, "/")
	}
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
		return uri
	}
	return ""
}

// RunDetailView is the view model for the /runs/<ns>/<name> detail page.
type RunDetailView struct {
	ActiveTab       NavTab
	Name            string
	Namespace       string
	Phase           v1alpha1.RunPhase
	PhaseChip       string
	RepoURL         string
	Commit          string
	CommitShort     string
	Model           string
	Age             string
	ArtifactsPrefix string
	Message         string

	// Summary
	TasksSummary     string
	TasksCompleted   int
	TasksFailed      int
	TasksTotal       int
	TotalSamples     int
	SampleRange      string // e.g. "samples 9-16"
	MeanSuccessRate  string
	SuccessRateRange string
	CompactRates     string

	Tasks []RunTaskRow
}

// ComparisonListItem represents a single comparison row in the /comparisons list view.
type ComparisonListItem struct {
	Name         string
	Namespace    string
	Verdict      v1alpha1.ComparisonVerdict
	VerdictChip  string
	PImprovement string
	Tasks        string
	BaselineRun  string
	CandidateRun string
	Age          string
	Link         string
}

// ComparisonsListView is the view model for the /comparisons list page.
type ComparisonsListView struct {
	ActiveTab    NavTab
	Namespace    string
	Comparisons  []ComparisonListItem
	TotalCount   int
	Offset       int
	Limit        int
	HasMore      bool
	NextLink     string
	HasPrev      bool
	PrevLink     string
	ShowingRange string
}

// PullRequestListItem represents a single aggregated pull request row in the /pulls list view.
type PullRequestListItem struct {
	Repository     string
	RepoHash       string
	Number         int
	GitHubURL      string
	HeadSHA        string
	HeadSHAShort   string
	Verdict        v1alpha1.ComparisonVerdict
	VerdictChip    string
	PImprovement   string
	SamplesRun     int
	Batches        int
	CompletionTime string
	ComparisonName string
	ComparisonLink string
	HistoryLink    string
	Namespace      string
}

// PullRequestsListView is the view model for the /pulls list page.
type PullRequestsListView struct {
	ActiveTab    NavTab
	Namespace    string
	Pulls        []PullRequestListItem
	TotalCount   int
	Offset       int
	Limit        int
	HasMore      bool
	NextLink     string
	HasPrev      bool
	PrevLink     string
	ShowingRange string
}

// PullRequestHistoryItem represents one Comparison in a pull request's history.
type PullRequestHistoryItem struct {
	Name           string
	Namespace      string
	HeadSHA        string
	HeadSHAShort   string
	BaseSHA        string
	BaseSHAShort   string
	Verdict        v1alpha1.ComparisonVerdict
	VerdictChip    string
	PImprovement   string
	SamplesRun     int
	Batches        int
	CompletionTime string
	Age            string
	Link           string
}

// PullRequestDetailView is the view model for the /pulls/{repohash}/{number} history page.
type PullRequestDetailView struct {
	ActiveTab   NavTab
	Namespace   string
	Repository  string
	RepoHash    string
	Number      int
	GitHubURL   string
	BackLink    string
	Comparisons []PullRequestHistoryItem
	TotalCount  int
}

// RepoHash returns a deterministic 7-character lowercase hexadecimal SHA-256 hash
// of the normalized repository name (e.g. "owner/repo").
func RepoHash(repo string) string {
	norm := strings.ToLower(strings.TrimSpace(repo))
	h := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(h[:])[:7]
}

// BuildGitHubPRURL builds a link to the GitHub pull request.
func BuildGitHubPRURL(repo string, prNum int) string {
	cleanRepo := strings.TrimPrefix(strings.TrimSpace(repo), "https://github.com/")
	cleanRepo = strings.TrimPrefix(cleanRepo, "http://github.com/")
	cleanRepo = strings.TrimSuffix(cleanRepo, ".git")
	cleanRepo = strings.Trim(cleanRepo, "/")
	return fmt.Sprintf("https://github.com/%s/pull/%d", cleanRepo, prNum)
}

// ComparisonSamplesRun returns the total evaluated samples for a Comparison.
func ComparisonSamplesRun(comp *v1alpha1.Comparison) int {
	if comp == nil {
		return 0
	}
	samplesRunTotal := 0
	totalPairs := 0
	for _, task := range comp.Status.Tasks {
		totalPairs += task.Pairs
		if task.SamplesRun > 0 {
			samplesRunTotal += task.SamplesRun
		} else {
			samplesRunTotal += task.Pairs
		}
	}
	if samplesRunTotal == 0 && totalPairs > 0 {
		samplesRunTotal = totalPairs
	}
	return samplesRunTotal
}

// FormatCompletionTime formats a Comparison completion timestamp as an age string, or "—" if not set.
func FormatCompletionTime(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return "—"
	}
	return FormatAge(*t)
}

// ComparisonTableView is the view model for rendering the comparison table sub-template.
type ComparisonTableView struct {
	Tasks       []ComparisonTaskRow
	HasTokens   bool
	HasAffinity bool
}

// ComparisonTaskRow represents a single task row in the PR-comment table.
type ComparisonTaskRow struct {
	ID            string
	BaseRate      string // e.g. "0.61"
	CandRate      string // e.g. "0.84"
	Delta         string // e.g. "+0.23"
	HasCI         bool
	CILeftPx      float64
	CIWidthPx     float64
	CIDotPx       float64
	CILabel       string // e.g. "[+0.11, +0.35]"
	StatusClass   string // good, bad, warn, neutral, pending
	Label         string // improved, regressed, unchanged, inconclusive, running
	Pairs         int
	DroppedCand   int
	DroppedBase   int
	SamplesRun    int
	Decision      string
	SuggestedNext int
	Tokens        *v1alpha1.TaskTokens
	Affinity      string // always, matched, unmatched
}

// ComparisonRows maps a Comparison resource into a slice of ComparisonTaskRow
// suitable for rendering in both HTML sub-templates and Markdown comments.
func ComparisonRows(comp *v1alpha1.Comparison) []ComparisonTaskRow {
	if comp == nil {
		return nil
	}

	rows := make([]ComparisonTaskRow, 0, len(comp.Status.Tasks))
	for _, task := range comp.Status.Tasks {
		leftPx, widthPx, dotPx, hasCI := CalculateComparisonBarGeometry(task.Delta, task.DeltaCI, 340.0)

		baseRate := "—"
		if task.BaseRate != nil {
			baseRate = fmt.Sprintf("%.2f", *task.BaseRate)
		}

		candRate := "—"
		if task.CandidateRate != nil {
			candRate = fmt.Sprintf("%.2f", *task.CandidateRate)
		}

		deltaStr := "—"
		if task.Delta != nil {
			deltaStr = fmt.Sprintf("%+.2f", *task.Delta)
		}

		ciLabel := "—"
		if task.DeltaCI != nil {
			ciLabel = fmt.Sprintf("[%+.2f, %+.2f]", task.DeltaCI.Lower, task.DeltaCI.Upper)
		}

		affinity := task.Affinity
		if affinity == "" && comp.Status.Affinity != nil {
			affinity = "always"
		}

		rows = append(rows, ComparisonTaskRow{
			ID:            task.ID,
			BaseRate:      baseRate,
			CandRate:      candRate,
			Delta:         deltaStr,
			HasCI:         hasCI,
			CILeftPx:      leftPx,
			CIWidthPx:     widthPx,
			CIDotPx:       dotPx,
			CILabel:       ciLabel,
			StatusClass:   labelStatusClass(task.Label),
			Label:         string(task.Label),
			Pairs:         task.Pairs,
			DroppedCand:   task.Dropped.Candidate,
			DroppedBase:   task.Dropped.Baseline,
			SamplesRun:    task.SamplesRun,
			Decision:      string(task.Decision),
			SuggestedNext: task.SuggestedNext,
			Tokens:        task.Tokens,
			Affinity:      affinity,
		})
	}
	return rows
}

// ComparisonSummaryData holds aggregate summary, headline, and footer data for a Comparison.
type ComparisonSummaryData struct {
	Headline         string // e.g. "05ce4d8 vs a81d0c4 · P(improvement) = 0.98" or "cand vs base · P(improvement) = 0.26"
	PImprovement     string // e.g. "0.98" or "—"
	BaseRun          string // e.g. "docs-eval-base-05ce4d8"
	BaseCommit       string // full commit SHA or empty
	BaseCommitShort  string // e.g. "a81d0c4" or ""
	CandRun          string // e.g. "docs-eval-cand-05ce4d8"
	CandCommit       string // full commit SHA or empty
	CandCommitShort  string // e.g. "05ce4d8" or ""
	TotalSamples     int
	TotalPairs       int
	TotalDroppedCand int
	TotalDroppedBase int
	CostUSD          *float64
	PossibleSamples  int
	SamplesSummary   string // e.g. "96 run · 48 paired" or "$4.10 spent, 96 of 3,200 possible samples"
	BaseSummary      string // e.g. "commit a81d0c4 (docs-eval-base-05ce4d8)"
	CandSummary      string // e.g. "commit 05ce4d8 (docs-eval-cand-05ce4d8)"
	AffinityMatched  int
	HasAffinity      bool
}

// ComparisonSummary computes aggregate headline and footer strings for a Comparison.
// Optional Run resources (baseRun, candRun) can be passed to provide commit hashes
// and total sample counts. If omitted or nil, the summary falls back to the Run names
// and task counts in the Comparison spec and status.
func ComparisonSummary(comp *v1alpha1.Comparison, baseRun, candRun *v1alpha1.Run) ComparisonSummaryData {
	if comp == nil {
		return ComparisonSummaryData{
			PImprovement: "—",
		}
	}

	// Match by name if possible in case caller passed them in reverse order
	if baseRun != nil && candRun != nil {
		if comp.Spec.Baseline.Run != "" && comp.Spec.Candidate.Run != "" {
			if baseRun.Name == comp.Spec.Candidate.Run && candRun.Name == comp.Spec.Baseline.Run {
				baseRun, candRun = candRun, baseRun
			}
		}
	}

	baseCommit := ""
	candCommit := ""
	baseTotalSamples := 0
	candTotalSamples := 0

	if baseRun != nil {
		baseCommit = baseRun.Spec.Repo.Commit
		if baseRun.Status.Summary != nil {
			baseTotalSamples = baseRun.Status.Summary.TotalSamples
		}
	}

	if candRun != nil {
		candCommit = candRun.Spec.Repo.Commit
		if candRun.Status.Summary != nil {
			candTotalSamples = candRun.Status.Summary.TotalSamples
		}
	}

	totalPairs := 0
	totalDroppedCand := 0
	totalDroppedBase := 0
	for _, task := range comp.Status.Tasks {
		totalPairs += task.Pairs
		totalDroppedCand += task.Dropped.Candidate
		totalDroppedBase += task.Dropped.Baseline
	}

	totalSamples := baseTotalSamples + candTotalSamples
	if totalSamples == 0 {
		totalSamples = (totalPairs + totalDroppedCand) + (totalPairs + totalDroppedBase)
	}

	pImpStr := "—"
	if comp.Status.PImprovement != nil {
		pImpStr = fmt.Sprintf("%.2f", *comp.Status.PImprovement)
	}

	candCommitShort := ShortCommit(candCommit)
	baseCommitShort := ShortCommit(baseCommit)

	candRef := candCommitShort
	if candRef == "" && candRun != nil && candRun.Name != "" {
		candRef = candRun.Name
	}
	if candRef == "" {
		candRef = comp.Spec.Candidate.Run
	}

	baseRef := baseCommitShort
	if baseRef == "" && baseRun != nil && baseRun.Name != "" {
		baseRef = baseRun.Name
	}
	if baseRef == "" {
		baseRef = comp.Spec.Baseline.Run
	}

	headlineParts := make([]string, 0, 2)
	if candRef != "" || baseRef != "" {
		headlineParts = append(headlineParts, fmt.Sprintf("%s vs %s", candRef, baseRef))
	}
	if comp.Status.PImprovement != nil {
		headlineParts = append(headlineParts, fmt.Sprintf("P(improvement) = %.2f", *comp.Status.PImprovement))
	}
	headline := strings.Join(headlineParts, " · ")

	costUSD := comp.Status.CostUSD
	possibleSamples := comp.Status.PossibleSamples

	var spentStr string
	if costUSD != nil {
		spentStr = fmt.Sprintf("$%.2f spent", *costUSD)
	}

	samplesRunTotal := 0
	for _, task := range comp.Status.Tasks {
		if task.SamplesRun > 0 {
			samplesRunTotal += task.SamplesRun
		} else {
			samplesRunTotal += task.Pairs
		}
	}
	if samplesRunTotal == 0 && totalPairs > 0 {
		samplesRunTotal = totalPairs
	}

	var samplesStr string
	if possibleSamples > 0 {
		samplesStr = fmt.Sprintf("%d of %d possible samples", samplesRunTotal, possibleSamples)
	} else {
		samplesStr = fmt.Sprintf("%d run · %d paired", totalSamples, totalPairs)
	}
	if totalDroppedCand > 0 || totalDroppedBase > 0 {
		samplesStr += fmt.Sprintf(" · dropped: %d cand, %d base", totalDroppedCand, totalDroppedBase)
	}

	var samplesSummary string
	if spentStr != "" {
		samplesSummary = fmt.Sprintf("%s, %s", spentStr, samplesStr)
	} else {
		samplesSummary = samplesStr
	}

	baseRunName := comp.Spec.Baseline.Run
	if baseRunName == "" && baseRun != nil {
		baseRunName = baseRun.Name
	}
	candRunName := comp.Spec.Candidate.Run
	if candRunName == "" && candRun != nil {
		candRunName = candRun.Name
	}

	baseSummary := baseRunName
	if baseCommitShort != "" {
		if baseSummary != "" {
			baseSummary = fmt.Sprintf("commit %s (%s)", baseCommitShort, baseSummary)
		} else {
			baseSummary = fmt.Sprintf("commit %s", baseCommitShort)
		}
	}

	candSummary := candRunName
	if candCommitShort != "" {
		if candSummary != "" {
			candSummary = fmt.Sprintf("commit %s (%s)", candCommitShort, candSummary)
		} else {
			candSummary = fmt.Sprintf("commit %s", candCommitShort)
		}
	}

	affinityMatched := 0
	hasAffinity := false
	if comp.Status.Summary != nil {
		affinityMatched = comp.Status.Summary.AffinityMatched
	}
	if comp.Status.Affinity != nil || affinityMatched > 0 {
		hasAffinity = true
	}

	return ComparisonSummaryData{
		Headline:         headline,
		PImprovement:     pImpStr,
		BaseRun:          baseRunName,
		BaseCommit:       baseCommit,
		BaseCommitShort:  baseCommitShort,
		CandRun:          candRunName,
		CandCommit:       candCommit,
		CandCommitShort:  candCommitShort,
		TotalSamples:     totalSamples,
		TotalPairs:       totalPairs,
		TotalDroppedCand: totalDroppedCand,
		TotalDroppedBase: totalDroppedBase,
		CostUSD:          costUSD,
		PossibleSamples:  possibleSamples,
		SamplesSummary:   samplesSummary,
		BaseSummary:      baseSummary,
		CandSummary:      candSummary,
		AffinityMatched:  affinityMatched,
		HasAffinity:      hasAffinity,
	}
}

// ComparisonBatchRow represents a single batch execution row in the comparison detail view.
type ComparisonBatchRow struct {
	Index        int
	Phase        v1alpha1.ComparisonPhase
	PhaseChip    string // e.g. "good", "pending", "bad", "neutral"
	SampleRange  string // e.g. "1-8" or "—"
	CandidateRun string
	BaselineRun  string
}

// ComparisonBatches extracts structured batch rows from a Comparison status.
func ComparisonBatches(comp *v1alpha1.Comparison) []ComparisonBatchRow {
	if comp == nil {
		return nil
	}
	var batchRows []ComparisonBatchRow
	for _, b := range comp.Status.Batches {
		sr := "—"
		if b.SampleRange != nil {
			sr = fmt.Sprintf("%d-%d", b.SampleRange.Start, b.SampleRange.End)
		}
		batchRows = append(batchRows, ComparisonBatchRow{
			Index:        b.Index,
			Phase:        b.Phase,
			PhaseChip:    comparisonPhaseChipClass(b.Phase),
			SampleRange:  sr,
			CandidateRun: b.CandidateRun,
			BaselineRun:  b.BaselineRun,
		})
	}
	return batchRows
}

// ComparisonActionRow represents an actionable recommendation for a comparison task.
type ComparisonActionRow struct {
	TaskID        string
	SuggestedNext int
	ShowTaskID    bool
}

// ComparisonActions returns structured action hints for inconclusive tasks in a Comparison.
func ComparisonActions(comp *v1alpha1.Comparison) []ComparisonActionRow {
	if comp == nil {
		return nil
	}
	showTaskID := len(comp.Status.Tasks) > 1
	var actions []ComparisonActionRow
	for _, task := range comp.Status.Tasks {
		if task.Label == v1alpha1.ComparisonTaskLabelInconclusive && task.SuggestedNext > 0 {
			actions = append(actions, ComparisonActionRow{
				TaskID:        task.ID,
				SuggestedNext: task.SuggestedNext,
				ShowTaskID:    showTaskID,
			})
		}
	}
	return actions
}

// ComparisonDetailView is the view model for the /comparisons/<ns>/<name> detail page.
type ComparisonDetailView struct {
	ActiveTab    NavTab
	Name         string
	Namespace    string
	Verdict      v1alpha1.ComparisonVerdict
	VerdictChip  string
	Headline     string // e.g. "3f9c2e1 vs a81d0c4 · P(improvement) = 0.98"
	PImprovement string // e.g. "0.98"
	SummaryPath  string
	Message      string

	Tasks           []ComparisonTaskRow
	HasTokens       bool
	HasAffinity     bool
	AffinityMatched int

	Batches []ComparisonBatchRow
	Actions []ComparisonActionRow

	// Summary contains aggregate comparison headline and footer data.
	Summary ComparisonSummaryData

	// Footer data
	BaseRun          string
	BaseCommit       string
	BaseCommitShort  string
	CandRun          string
	CandCommit       string
	CandCommitShort  string
	TotalSamples     int
	TotalPairs       int
	TotalDroppedCand int
	TotalDroppedBase int
	CostUSD          *float64
	PossibleSamples  int
	SamplesSummary   string

	// Histories contains per-task CI history points and inline SVG for tasks with pairs >= 2.
	Histories []ComparisonTaskHistory

	// HistoryNote is set when comparison history cannot be loaded (e.g. "artifact storage unavailable" or "history not available").
	HistoryNote string
}

// ComparisonTaskHistory holds the recomputed CI width history and SVG for a single task.
type ComparisonTaskHistory struct {
	TaskID string
	Points []ComparisonHistoryPoint
	SVG    ComparisonHistorySVG
}

// ComparisonHistoryPoint holds a single point in the CI history table beneath the chart.
type ComparisonHistoryPoint struct {
	N              int
	DeltaFormatted string
	CIFormatted    string
	CIWidth        float64
	Decision       string
	DecisionChip   string
}

// ComparisonHistorySVG holds precomputed numeric coordinates for inline SVG rendering.
type ComparisonHistorySVG struct {
	ViewBox     string
	X0          float64
	Y0          float64
	X1          float64
	Y1          float64
	MarginY     float64
	MarginLabel string
	PointsAttr  string
	Circles     []SVGCircle
	FinalLabel  SVGFinalLabel
	XAxisTicks  []SVGTick
	YAxisTicks  []SVGTick
}

// SVGCircle represents a data point circle on the SVG chart.
type SVGCircle struct {
	CX float64
	CY float64
	R  float64
}

// SVGFinalLabel represents the label positioned at the final point.
type SVGFinalLabel struct {
	X    float64
	Y    float64
	Text string
}

// SVGTick represents an axis tick with a numeric coordinate and label.
type SVGTick struct {
	X     float64
	Y     float64
	Y1    float64
	Y2    float64
	TextY float64
	Text  string
}

// DecisionChipClass returns the CSS chip class for a stats decision label string.
func DecisionChipClass(label string) string {
	switch label {
	case "improved":
		return "good"
	case "regressed":
		return "bad"
	case "unchanged":
		return "neutral"
	case "inconclusive":
		return "warn"
	default:
		return "pending"
	}
}

// BuildComparisonHistorySVG generates server-side numeric SVG coordinates for CI width vs n.
func BuildComparisonHistorySVG(points []ComparisonHistoryPoint, margin float64) ComparisonHistorySVG {
	const (
		viewBox = "0 0 640 220"
		x0      = 60.0
		x1      = 560.0
		y0      = 25.0
		y1      = 175.0
	)

	if len(points) == 0 {
		return ComparisonHistorySVG{ViewBox: viewBox, X0: x0, Y0: y0, X1: x1, Y1: y1}
	}

	nMax := points[len(points)-1].N
	if nMax <= 0 {
		nMax = 1
	}

	maxCIWidth := margin
	for _, pt := range points {
		if pt.CIWidth > maxCIWidth {
			maxCIWidth = pt.CIWidth
		}
	}

	yMax := maxCIWidth * 1.25
	if yMax < 0.20 {
		yMax = 0.20
	}

	marginY := y1 - (margin/yMax)*(y1-y0)
	marginLabel := fmt.Sprintf("margin ±%.2f", margin)

	var pointsAttrBuilder strings.Builder
	circles := make([]SVGCircle, len(points))

	for i, pt := range points {
		cx := x0 + (float64(pt.N)/float64(nMax))*(x1-x0)
		cy := y1 - (pt.CIWidth/yMax)*(y1-y0)
		circles[i] = SVGCircle{CX: cx, CY: cy, R: 4.0}

		if i > 0 {
			pointsAttrBuilder.WriteByte(' ')
		}
		pointsAttrBuilder.WriteString(fmt.Sprintf("%.1f,%.1f", cx, cy))
	}

	lastPt := points[len(points)-1]
	lastCircle := circles[len(circles)-1]
	finalLabelY := lastCircle.CY - 10.0
	if finalLabelY < y0+10.0 {
		finalLabelY = lastCircle.CY + 15.0
	}
	finalLabelX := lastCircle.CX
	if finalLabelX > x1-60.0 {
		finalLabelX = x1 - 60.0
	}
	finalLabel := SVGFinalLabel{
		X:    finalLabelX,
		Y:    finalLabelY,
		Text: fmt.Sprintf("n=%d (%.2f)", lastPt.N, lastPt.CIWidth),
	}

	var xTicks []SVGTick
	xTicks = append(xTicks, SVGTick{X: x0, Y1: y1, Y2: y1 + 4.0, TextY: y1 + 18.0, Text: "0"})
	if nMax >= 8 {
		midN := nMax / 2
		midX := x0 + (float64(midN)/float64(nMax))*(x1-x0)
		xTicks = append(xTicks, SVGTick{X: midX, Y1: y1, Y2: y1 + 4.0, TextY: y1 + 18.0, Text: fmt.Sprintf("%d", midN)})
	}
	xTicks = append(xTicks, SVGTick{X: x1, Y1: y1, Y2: y1 + 4.0, TextY: y1 + 18.0, Text: fmt.Sprintf("%d", nMax)})

	yTicks := []SVGTick{
		{X: x0 - 8.0, Y: y1 + 4.0, Text: "0.00"},
		{X: x0 - 8.0, Y: marginY + 4.0, Text: fmt.Sprintf("%.2f", margin)},
		{X: x0 - 8.0, Y: y0 + 4.0, Text: fmt.Sprintf("%.2f", yMax)},
	}

	return ComparisonHistorySVG{
		ViewBox:     viewBox,
		X0:          x0,
		Y0:          y0,
		X1:          x1,
		Y1:          y1,
		MarginY:     marginY,
		MarginLabel: marginLabel,
		PointsAttr:  pointsAttrBuilder.String(),
		Circles:     circles,
		FinalLabel:  finalLabel,
		XAxisTicks:  xTicks,
		YAxisTicks:  yTicks,
	}
}

// CleanRepo normalizes a repository clone URL or path to "owner/repo".
func CleanRepo(repoOrURL string) string {
	s := strings.TrimSpace(repoOrURL)
	s = strings.TrimPrefix(s, "https://github.com/")
	s = strings.TrimPrefix(s, "http://github.com/")
	s = strings.TrimPrefix(s, "git@github.com:")
	s = strings.TrimPrefix(s, "git://github.com/")
	s = strings.TrimSuffix(s, ".git")
	return strings.Trim(s, "/")
}

// SeriesListRow represents a repository and branch entry in the /series list view.
type SeriesListRow struct {
	Repository string
	RepoHash   string
	Branch     string
	RunCount   int
	Link       string
}

// SeriesListView is the view model for GET /series.
type SeriesListView struct {
	ActiveTab  NavTab
	Namespace  string
	Cohort     string
	Series     []SeriesListRow
	TotalCount int
}

// SeriesTaskLink represents a task link in the task selector.
type SeriesTaskLink struct {
	ID     string
	Link   string
	Active bool
}

// SeriesItem holds the data for one commit run in the series view.
type SeriesItem struct {
	CommitSHA      string
	CommitSHAShort string
	CommitTime     time.Time
	CommitTimeStr  string
	RunName        string
	Namespace      string
	Phase          v1alpha1.TaskPhase
	PhaseChip      string
	Samples        string
	RateStr        string
	CIStr          string
	TokensStr      string
	CostStr        string
	RunLink        string

	HasPoint    bool
	SuccessRate *float64
	CI          *v1alpha1.ConfidenceInterval
}

// SeriesDetailView is the view model for GET /series/{repohash}/{branch}.
type SeriesDetailView struct {
	ActiveTab             NavTab
	Namespace             string
	Repository            string
	RepoHash              string
	Branch                string
	SelectedTask          string
	Tasks                 []SeriesTaskLink
	N                     int
	Cohort                string
	OrderedByCreationTime bool
	OrderedNote           string
	SVG                   SeriesSVG
	Items                 []SeriesItem
	TotalRuns             int
	BackLink              string
}

// SeriesSVG holds precomputed numeric coordinates for inline SVG rendering of a task series.
type SeriesSVG struct {
	ViewBox    string
	X0         float64
	Y0         float64
	X1         float64
	Y1         float64
	Polylines  []string    // list of "x1,y1 x2,y2 ..." for each contiguous polyline segment
	Polygons   []string    // list of "x0,yu0 ... xk,yuk xk,ylk ... x0,yl0" for shaded CI band
	Circles    []SVGCircle // data points
	XAxisTicks []SVGTick   // ticks for commits along X-axis
	YAxisTicks []SVGTick   // ticks for success rate along Y-axis
}

// BuildSeriesSVG generates server-side numeric SVG coordinates for success rate vs commit series.
func BuildSeriesSVG(items []SeriesItem) SeriesSVG {
	const (
		viewBox = "0 0 640 220"
		x0      = 60.0
		x1      = 580.0
		y0      = 25.0
		y1      = 175.0
	)

	yTicks := []SVGTick{
		{X: x0 - 8.0, Y: y1 + 4.0, Y1: y1, Text: "0.00"},
		{X: x0 - 8.0, Y: (y0+y1)/2.0 + 4.0, Y1: (y0 + y1) / 2.0, Text: "0.50"},
		{X: x0 - 8.0, Y: y0 + 4.0, Y1: y0, Text: "1.00"},
	}

	if len(items) == 0 {
		return SeriesSVG{
			ViewBox:    viewBox,
			X0:         x0,
			Y0:         y0,
			X1:         x1,
			Y1:         y1,
			YAxisTicks: yTicks,
		}
	}

	N := len(items)
	var xTicks []SVGTick
	if N <= 8 {
		for i, it := range items {
			cx := x0
			if N > 1 {
				cx = x0 + (float64(i)/float64(N-1))*(x1-x0)
			} else {
				cx = (x0 + x1) / 2.0
			}
			xTicks = append(xTicks, SVGTick{X: cx, Y1: y1, Y2: y1 + 4.0, TextY: y1 + 18.0, Text: it.CommitSHAShort})
		}
	} else {
		cx0 := x0
		cxMid := x0 + (float64(N/2)/float64(N-1))*(x1-x0)
		cxLast := x1
		xTicks = append(xTicks, SVGTick{X: cx0, Y1: y1, Y2: y1 + 4.0, TextY: y1 + 18.0, Text: items[0].CommitSHAShort})
		xTicks = append(xTicks, SVGTick{X: cxMid, Y1: y1, Y2: y1 + 4.0, TextY: y1 + 18.0, Text: items[N/2].CommitSHAShort})
		xTicks = append(xTicks, SVGTick{X: cxLast, Y1: y1, Y2: y1 + 4.0, TextY: y1 + 18.0, Text: items[N-1].CommitSHAShort})
	}

	var polylines []string
	var polygons []string
	var circles []SVGCircle

	type pt2D struct {
		x float64
		y float64
	}
	type ci2D struct {
		x      float64
		yUpper float64
		yLower float64
	}

	var curLine []pt2D
	var curCI []ci2D

	flushLine := func() {
		if len(curLine) >= 2 {
			var sb strings.Builder
			for j, p := range curLine {
				if j > 0 {
					sb.WriteByte(' ')
				}
				sb.WriteString(fmt.Sprintf("%.1f,%.1f", p.x, p.y))
			}
			polylines = append(polylines, sb.String())
		}
		curLine = nil
	}

	flushCI := func() {
		if len(curCI) >= 2 {
			var sb strings.Builder
			for j, p := range curCI {
				if j > 0 {
					sb.WriteByte(' ')
				}
				sb.WriteString(fmt.Sprintf("%.1f,%.1f", p.x, p.yUpper))
			}
			for j := len(curCI) - 1; j >= 0; j-- {
				sb.WriteByte(' ')
				sb.WriteString(fmt.Sprintf("%.1f,%.1f", curCI[j].x, curCI[j].yLower))
			}
			polygons = append(polygons, sb.String())
		} else if len(curCI) == 1 {
			p := curCI[0]
			poly := fmt.Sprintf("%.1f,%.1f %.1f,%.1f %.1f,%.1f %.1f,%.1f",
				p.x-2.0, p.yUpper,
				p.x+2.0, p.yUpper,
				p.x+2.0, p.yLower,
				p.x-2.0, p.yLower,
			)
			polygons = append(polygons, poly)
		}
		curCI = nil
	}

	for i, it := range items {
		cx := x0
		if N > 1 {
			cx = x0 + (float64(i)/float64(N-1))*(x1-x0)
		} else {
			cx = (x0 + x1) / 2.0
		}

		if !it.HasPoint || it.SuccessRate == nil {
			flushLine()
			flushCI()
			continue
		}

		rate := *it.SuccessRate
		if rate < 0.0 {
			rate = 0.0
		} else if rate > 1.0 {
			rate = 1.0
		}
		cy := y1 - rate*(y1-y0)

		circles = append(circles, SVGCircle{CX: cx, CY: cy, R: 4.0})
		curLine = append(curLine, pt2D{x: cx, y: cy})

		if it.CI != nil {
			lo := it.CI.Lower
			if lo < 0.0 {
				lo = 0.0
			} else if lo > 1.0 {
				lo = 1.0
			}
			hi := it.CI.Upper
			if hi < 0.0 {
				hi = 0.0
			} else if hi > 1.0 {
				hi = 1.0
			}
			yUpper := y1 - hi*(y1-y0)
			yLower := y1 - lo*(y1-y0)
			curCI = append(curCI, ci2D{x: cx, yUpper: yUpper, yLower: yLower})
		} else {
			flushCI()
		}
	}

	flushLine()
	flushCI()

	return SeriesSVG{
		ViewBox:    viewBox,
		X0:         x0,
		Y0:         y0,
		X1:         x1,
		Y1:         y1,
		Polylines:  polylines,
		Polygons:   polygons,
		Circles:    circles,
		XAxisTicks: xTicks,
		YAxisTicks: yTicks,
	}
}

// NewComparisonDetailView constructs a ComparisonDetailView from a Comparison and optional Runs.
func NewComparisonDetailView(comp *v1alpha1.Comparison, baseRun, candRun *v1alpha1.Run) ComparisonDetailView {
	if comp == nil {
		return ComparisonDetailView{}
	}
	summary := ComparisonSummary(comp, baseRun, candRun)
	taskRows := ComparisonRows(comp)

	hasTokens := false
	hasAffinity := false
	for _, tr := range taskRows {
		if tr.Tokens != nil {
			hasTokens = true
		}
		if tr.Affinity != "" {
			hasAffinity = true
		}
	}

	affinityMatched := 0
	if comp.Status.Summary != nil {
		affinityMatched = comp.Status.Summary.AffinityMatched
	}

	return ComparisonDetailView{
		ActiveTab:        NavTabComparisons,
		Name:             comp.Name,
		Namespace:        comp.Namespace,
		Verdict:          comp.Status.Verdict,
		VerdictChip:      verdictChipClass(comp.Status.Verdict),
		Headline:         summary.Headline,
		PImprovement:     summary.PImprovement,
		SummaryPath:      comp.Status.SummaryPath,
		Message:          comp.Status.Message,
		Tasks:            taskRows,
		HasTokens:        hasTokens,
		HasAffinity:      hasAffinity,
		AffinityMatched:  affinityMatched,
		Batches:          ComparisonBatches(comp),
		Actions:          ComparisonActions(comp),
		Summary:          summary,
		BaseRun:          summary.BaseRun,
		BaseCommit:       summary.BaseCommit,
		BaseCommitShort:  summary.BaseCommitShort,
		CandRun:          summary.CandRun,
		CandCommit:       summary.CandCommit,
		CandCommitShort:  summary.CandCommitShort,
		TotalSamples:     summary.TotalSamples,
		TotalPairs:       summary.TotalPairs,
		TotalDroppedCand: summary.TotalDroppedCand,
		TotalDroppedBase: summary.TotalDroppedBase,
		CostUSD:          summary.CostUSD,
		PossibleSamples:  summary.PossibleSamples,
		SamplesSummary:   summary.SamplesSummary,
	}
}

// Helpers for CSS status classes

func comparisonPhaseChipClass(phase v1alpha1.ComparisonPhase) string {
	switch phase {
	case v1alpha1.ComparisonPhaseCompleted:
		return "good"
	case v1alpha1.ComparisonPhaseRunning:
		return "pending"
	default:
		return "neutral"
	}
}

func phaseChipClass(phase v1alpha1.RunPhase) string {
	switch phase {
	case v1alpha1.RunPhaseCompleted:
		return "good"
	case v1alpha1.RunPhaseRunning:
		return "pending"
	case v1alpha1.RunPhasePending, v1alpha1.RunPhaseDiscovering, v1alpha1.RunPhaseDiscovered:
		return "warn"
	case v1alpha1.RunPhaseFailed:
		return "bad"
	default:
		return "neutral"
	}
}

func taskPhaseChipClass(phase v1alpha1.TaskPhase) string {
	switch phase {
	case v1alpha1.TaskPhaseCompleted:
		return "good"
	case v1alpha1.TaskPhaseRunning:
		return "pending"
	case v1alpha1.TaskPhasePending:
		return "neutral"
	case v1alpha1.TaskPhaseFailed:
		return "bad"
	default:
		return "neutral"
	}
}

func verdictChipClass(verdict v1alpha1.ComparisonVerdict) string {
	switch verdict {
	case v1alpha1.ComparisonVerdictImproved:
		return "good"
	case v1alpha1.ComparisonVerdictRegressed:
		return "bad"
	case v1alpha1.ComparisonVerdictUnchanged:
		return "neutral"
	case v1alpha1.ComparisonVerdictInconclusive:
		return "warn"
	case v1alpha1.ComparisonVerdictRunning:
		return "pending"
	default:
		return "neutral"
	}
}

func labelStatusClass(label v1alpha1.ComparisonTaskLabel) string {
	switch label {
	case v1alpha1.ComparisonTaskLabelImproved:
		return "good"
	case v1alpha1.ComparisonTaskLabelRegressed:
		return "bad"
	case v1alpha1.ComparisonTaskLabelUnchanged:
		return "neutral"
	case v1alpha1.ComparisonTaskLabelInconclusive:
		return "warn"
	case v1alpha1.ComparisonTaskLabelRunning, v1alpha1.ComparisonTaskLabelContinue:
		return "pending"
	case v1alpha1.ComparisonTaskLabelSkipped:
		return "neutral"
	default:
		return "neutral"
	}
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// CalculateComparisonBarGeometry calculates the geometry for the comparison CI bar.
// Bar width is 340px, with the zero mark at the center (170px).
// Scale is 340px / 1.0 = 340px per unit difference (range [-0.5, +0.5] spans the 340px bar).
func CalculateComparisonBarGeometry(delta *float64, ci *v1alpha1.DeltaConfidenceInterval, barWidth float64) (leftPx, widthPx, dotPx float64, hasCI bool) {
	if ci == nil {
		return 0, 0, 0, false
	}

	zeroPx := barWidth / 2.0
	scale := barWidth / 1.0 // 340 px per unit delta

	lo := ci.Lower
	hi := ci.Upper
	d := 0.0
	if delta != nil {
		d = *delta
	} else {
		d = (lo + hi) / 2.0
	}

	rawLeft := zeroPx + lo*scale
	rawRight := zeroPx + hi*scale

	leftPx = clamp(rawLeft, 0, barWidth)
	rightPx := clamp(rawRight, 0, barWidth)
	if rightPx < leftPx {
		rightPx = leftPx
	}
	widthPx = math.Max(rightPx-leftPx, 2.0) // at least 2px to be visible

	// Point estimate dot is 14px wide, centered on zeroPx + d*scale
	rawDot := zeroPx + d*scale - 7.0
	dotPx = clamp(rawDot, 0, barWidth-14.0)

	return leftPx, widthPx, dotPx, true
}

// CalculateRunBarGeometry calculates the geometry for a Run's single-task CI bar against a 0-1 axis.
// Axis width is barWidth (default 240px). Axis goes from 0.0 (left=0) to 1.0 (right=barWidth).
func CalculateRunBarGeometry(rate *float64, meanScore *float64, ci *v1alpha1.ConfidenceInterval, barWidth float64) (leftPx, widthPx, dotPx float64, hasCI bool) {
	if ci == nil {
		return 0, 0, 0, false
	}

	lo := clamp(ci.Lower, 0.0, 1.0)
	hi := clamp(ci.Upper, 0.0, 1.0)
	if hi < lo {
		hi = lo
	}

	pt := 0.0
	if rate != nil {
		pt = clamp(*rate, 0.0, 1.0)
	} else if meanScore != nil {
		pt = clamp(*meanScore, 0.0, 1.0)
	} else {
		pt = (lo + hi) / 2.0
	}

	leftPx = lo * barWidth
	rightPx := hi * barWidth
	widthPx = math.Max(rightPx-leftPx, 2.0)

	// Point estimate dot is 14px wide, centered on pt * barWidth
	rawDot := pt*barWidth - 7.0
	dotPx = clamp(rawDot, 0, barWidth-14.0)

	return leftPx, widthPx, dotPx, true
}

func FormatAge(t metav1.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t.Time)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func ShortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// TaskSampleRow represents an individual sample row in the task samples list.
type TaskSampleRow struct {
	ID            string
	EscapedID     string
	Score         string
	Status        string // "pass", "fail", "unknown"
	StatusClass   string // "good", "bad", "neutral"
	InputPreview  string
	OutputPreview string
	LatencyStr    string
	TokensStr     string
	Link          string
}

// TaskSamplesListView is the view model for GET /runs/{ns}/{name}/tasks/{task}/samples.
type TaskSamplesListView struct {
	ActiveTab        NavTab
	Namespace        string
	RunName          string
	TaskID           string
	EscapedTaskID    string
	Commit           string
	Model            string
	LogPath          string
	Samples          []TaskSampleRow
	TotalSamples     int
	Page             int
	PageSize         int
	TotalPages       int
	ShowingStart     int
	ShowingEnd       int
	HasPrev          bool
	PrevLink         string
	HasNext          bool
	NextLink         string
	IsTruncatedLog   bool
	TruncatedMessage string
}

// TranscriptRowView represents a single rendered transcript entry row.
type TranscriptRowView struct {
	Kind    string // "user", "assistant", "tool_call", "tool", "system"
	Class   string // "", "tool", "bad"
	Content string
}

// SampleTranscriptView is the view model for GET /runs/{ns}/{name}/tasks/{task}/samples/{sample}.
type SampleTranscriptView struct {
	ActiveTab              NavTab
	Namespace              string
	RunName                string
	TaskID                 string
	EscapedTaskID          string
	SampleID               string
	EscapedSampleID        string
	Score                  string
	Status                 string // "pass", "fail", "unknown"
	StatusUpper            string // "PASS", "FAIL", "UNKNOWN"
	StatusClass            string // "good", "bad", "neutral"
	LatencyStr             string
	TokensStr              string
	LogPath                string
	Rows                   []TranscriptRowView
	TotalEvents            int
	IsEventsTruncated      bool
	EventsTruncatedMessage string
	IsTruncatedLog         bool
	TruncatedMessage       string
}

// TruncateRunes truncates string s to maxRunes, appending "..." if truncated.
func TruncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// TruncateMessageBytes truncates string s to maxBytes on a valid UTF-8 rune boundary,
// appending a truncation marker if truncated.
func TruncateMessageBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return "... [content truncated]"
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes] + "\n... [content truncated]"
}

// FormatTokens formats an integer token count with commas, e.g. 41200 -> "41,200", or "—" if <= 0.
func FormatTokens(tokens int) string {
	if tokens <= 0 {
		return "—"
	}
	in := strconv.Itoa(tokens)
	if len(in) <= 3 {
		return in
	}
	var out []byte
	rem := len(in) % 3
	if rem > 0 {
		out = append(out, in[:rem]...)
		if len(in) > rem {
			out = append(out, ',')
		}
	}
	for i := rem; i < len(in); i += 3 {
		out = append(out, in[i:i+3]...)
		if i+3 < len(in) {
			out = append(out, ',')
		}
	}
	return string(out)
}

// FormatLatency formats a float pointer latency in seconds, e.g. 3.0 -> "3.0s", or "—" if nil.
func FormatLatency(sec *float64) string {
	if sec == nil {
		return "—"
	}
	return fmt.Sprintf("%.1fs", *sec)
}

// BuildTaskSampleLink builds a path-escaped URL to a sample transcript page.
func BuildTaskSampleLink(ns, runName, taskID, sampleID string) string {
	return fmt.Sprintf("/runs/%s/%s/tasks/%s/samples/%s",
		ns,
		runName,
		url.PathEscape(taskID),
		url.PathEscape(sampleID),
	)
}

// BuildTaskSamplesListLink builds a path-escaped URL to a task samples list page.
func BuildTaskSamplesListLink(ns, runName, taskID string) string {
	return fmt.Sprintf("/runs/%s/%s/tasks/%s/samples",
		ns,
		runName,
		url.PathEscape(taskID),
	)
}
