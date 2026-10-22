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
	"context"
	"fmt"
	"math"
	"net/url"
	"sort"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/stats"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RepoObservations holds raw main-branch baseline observations for a repository.
type RepoObservations struct {
	Ref           RepoRef
	Branch        string
	Cohort        string
	LinkNS        string
	TotalRuns     int
	Commits       []CommitEntry
	Observations  []EvalObservation
	ByEval        map[string][]EvalObservation
	ByCommit      map[string]map[string][]EvalObservation // taskID -> commitSHA -> observations
	Suites        map[string][]string                     // suiteName -> sorted evalIDs
	SortedSuites  []string
	MatchedSeries []seriesRun
}

// EvalScore holds computed scores and deltas for a single eval.
type EvalScore struct {
	ID          string
	Point       float64
	ScoreStr    string
	CIStr       string
	Est         stats.Estimate
	HasDelta    bool
	DeltaStr    string
	DeltaClass  string
	Delta       *stats.DeltaEstimate
	NewestTasks []stats.TaskCounts
	NewestTime  time.Time
	NewestRun   *v1alpha1.Run
	CommitSHA   string
	CommitShort string
	SamplesLink string
	RunLink     string
}

// SuiteScore holds computed pooled scores and deltas for an eval suite.
type SuiteScore struct {
	Name        string
	Point       float64
	ScoreStr    string
	CIStr       string
	Est         stats.Estimate
	HasDelta    bool
	DeltaStr    string
	DeltaClass  string
	Delta       *stats.DeltaEstimate
	EvalsCount  int
	CommitSHA   string
	CommitShort string
	NewestTime  time.Time
	NewestRun   *v1alpha1.Run
	NewestTasks []stats.TaskCounts
	Commits     []CommitEntry
	Evals       []EvalScore
}

// RepoScores holds computed suite scores and overall headline numbers.
type RepoScores struct {
	Suites       map[string]SuiteScore
	SortedSuites []string
	OverallPoint float64
	OverallScore string
	OverallCI    string
	OverallEst   stats.Estimate
	CommitSHA    string
	CommitShort  string
	NewestTime   time.Time
	NewestRun    *v1alpha1.Run
}

// collectRepoObservations lists runs in namespace and builds RepoObservations for the repository and branch.
// Enforces at most one List of Runs per request.
func (s *Server) collectRepoObservations(ctx context.Context, ns string, ref RepoRef, branch string, cohort ...string) (*RepoObservations, error) {
	var allRuns v1alpha1.RunList
	if err := s.readerFor(ns).List(ctx, &allRuns, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	cFilter := ""
	if len(cohort) > 0 {
		cFilter = cohort[0]
	}
	obs := buildRepoObservations(allRuns.Items, ref, branch, cFilter, s.linkNamespace(ns), s.hasArtifactStorage())
	return obs, nil
}

// buildRepoObservations processes a list of runs into structured RepoObservations.
func buildRepoObservations(runs []v1alpha1.Run, ref RepoRef, branch, cohort string, linkNS string, hasArtifactStorage bool) *RepoObservations {
	var totalRuns int
	for i := range runs {
		if rRef, ok := runRepoRef(&runs[i]); ok && rRef.Equal(ref) {
			totalRuns++
		}
	}

	matched := collectSeries(runs, ref, branch, cohort)

	var orderedCommits []CommitEntry
	seenCommits := make(map[string]bool)
	for _, sr := range matched {
		c := runCommitSHA(sr.run)
		if c == "" {
			continue
		}
		if !seenCommits[c] {
			seenCommits[c] = true
			orderedCommits = append(orderedCommits, CommitEntry{
				SHA:        c,
				CommitTime: sr.commitTime,
			})
		}
	}

	byCommit := make(map[string]map[string][]EvalObservation)
	byEval := make(map[string][]EvalObservation)
	suitesMap := make(map[string]map[string]bool)
	var allObs []EvalObservation

	for _, sr := range matched {
		run := sr.run
		commit := runCommitSHA(run)
		if commit == "" {
			continue
		}
		for idx := range run.Status.Tasks {
			t := &run.Status.Tasks[idx]
			if t.ID == "" {
				continue
			}
			if t.Phase == v1alpha1.TaskPhaseCompleted && t.Samples > 0 {
				samples := t.Samples
				succ := t.Successes
				if succ == 0 && t.SuccessRate != nil && *t.SuccessRate > 0 {
					succ = int(math.Round(*t.SuccessRate * float64(samples)))
				}
				est := stats.PooledWilsonFromTotals(succ, samples, 0.95)
				scoreStr := fmt.Sprintf("%.2f", est.Point)
				ciStr := fmt.Sprintf("[%.2f, %.2f]", est.CI.Lower, est.CI.Upper)

				samplesLink := ""
				if t.LogPath != "" && hasArtifactStorage {
					samplesLink = repoLink(ref, fmt.Sprintf("/runs/%s/tasks/%s/samples", run.Name, url.PathEscape(t.ID)), linkNS)
				}

				obs := EvalObservation{
					RunName:     run.Name,
					Run:         run,
					TaskID:      t.ID,
					CommitSHA:   commit,
					CommitTime:  sr.commitTime,
					Model:       run.Spec.Model,
					Harness:     run.Labels["evalrunner.gke-labs.dev/harness"],
					Samples:     samples,
					Successes:   succ,
					SuccessRate: t.SuccessRate,
					ScoreStr:    scoreStr,
					CIStr:       ciStr,
					Est:         est,
					SamplesLink: samplesLink,
				}

				if byCommit[t.ID] == nil {
					byCommit[t.ID] = make(map[string][]EvalObservation)
				}
				byCommit[t.ID][commit] = append(byCommit[t.ID][commit], obs)
				byEval[t.ID] = append(byEval[t.ID], obs)
				allObs = append(allObs, obs)

				suite := SuiteOf(t.ID)
				if suitesMap[suite] == nil {
					suitesMap[suite] = make(map[string]bool)
				}
				suitesMap[suite][t.ID] = true
			}
		}
	}

	var sortedSuites []string
	suites := make(map[string][]string)
	for sName, evalSet := range suitesMap {
		sortedSuites = append(sortedSuites, sName)
		var eIDs []string
		for eID := range evalSet {
			eIDs = append(eIDs, eID)
		}
		sort.Strings(eIDs)
		suites[sName] = eIDs
	}
	sort.Strings(sortedSuites)

	return &RepoObservations{
		Ref:           ref,
		Branch:        branch,
		Cohort:        cohort,
		LinkNS:        linkNS,
		TotalRuns:     totalRuns,
		Commits:       orderedCommits,
		Observations:  allObs,
		ByEval:        byEval,
		ByCommit:      byCommit,
		Suites:        suites,
		SortedSuites:  sortedSuites,
		MatchedSeries: matched,
	}
}

// suiteScores computes pooled scores, Wilson intervals, and deltas across all suites and evals.
func suiteScores(obs *RepoObservations) RepoScores {
	suitesResult := make(map[string]SuiteScore)
	var sortedSuiteNames []string

	for _, sName := range obs.SortedSuites {
		evalIDsInSuite := obs.Suites[sName]

		var evalScoresInSuite []EvalScore

		for _, eID := range evalIDsInSuite {
			var evalCommits []CommitEntry
			for _, c := range obs.Commits {
				if len(obs.ByCommit[eID][c.SHA]) > 0 {
					evalCommits = append(evalCommits, c)
				}
			}

			if len(evalCommits) == 0 {
				continue
			}

			newestCommit := evalCommits[len(evalCommits)-1]
			newestObs := obs.ByCommit[eID][newestCommit.SHA]

			var newestTasks []stats.TaskCounts
			var latestRun *v1alpha1.Run
			var newestTime time.Time
			for _, o := range newestObs {
				newestTasks = append(newestTasks, stats.TaskCounts{
					Successes: o.Successes,
					Samples:   o.Samples,
				})
				latestRun = o.Run
				if o.CommitTime.After(newestTime) {
					newestTime = o.CommitTime
				}
			}
			if newestTime.IsZero() && latestRun != nil {
				newestTime, _ = runCommitTime(latestRun)
				if newestTime.IsZero() {
					newestTime = latestRun.CreationTimestamp.Time
				}
			}

			evalEst := stats.PooledWilson(newestTasks, 0.95)
			evalScoreStr := fmt.Sprintf("%.2f", evalEst.Point)
			evalCIStr := fmt.Sprintf("[%.2f, %.2f]", evalEst.CI.Lower, evalEst.CI.Upper)

			evalDeltaStr := "—"
			evalDeltaClass := ""
			evalHasDelta := false
			var evalDelta *stats.DeltaEstimate
			if len(evalCommits) > 1 {
				prevCommit := evalCommits[len(evalCommits)-2]
				prevObs := obs.ByCommit[eID][prevCommit.SHA]
				var prevTasks []stats.TaskCounts
				for _, o := range prevObs {
					prevTasks = append(prevTasks, stats.TaskCounts{
						Successes: o.Successes,
						Samples:   o.Samples,
					})
				}
				prevEst := stats.PooledWilson(prevTasks, 0.95)
				delta := stats.DifferenceUnpaired(evalEst, prevEst, 0.95)
				evalDelta = &delta
				evalDeltaStr = FormatDeltaCI(delta.Delta, delta.CI.Lower, delta.CI.Upper)
				evalDeltaClass = DeltaChipClass(delta.CI)
				evalHasDelta = true
			}

			evalRunLink := ""
			evalSamplesLink := ""
			if latestRun != nil {
				evalRunLink = runLink(obs.Ref, latestRun.Name, obs.LinkNS, latestRun.Namespace)
				for _, o := range newestObs {
					if o.SamplesLink != "" {
						evalSamplesLink = o.SamplesLink
						break
					}
				}
			}

			evalScoresInSuite = append(evalScoresInSuite, EvalScore{
				ID:          eID,
				Point:       evalEst.Point,
				ScoreStr:    evalScoreStr,
				CIStr:       evalCIStr,
				Est:         evalEst,
				HasDelta:    evalHasDelta,
				DeltaStr:    evalDeltaStr,
				DeltaClass:  evalDeltaClass,
				Delta:       evalDelta,
				NewestTasks: newestTasks,
				NewestTime:  newestTime,
				NewestRun:   latestRun,
				CommitSHA:   newestCommit.SHA,
				CommitShort: ShortCommit(newestCommit.SHA),
				SamplesLink: evalSamplesLink,
				RunLink:     evalRunLink,
			})
		}

		if len(evalScoresInSuite) == 0 {
			continue
		}

		// Suite score at main = mean of its evals' scores with interval from pooled samples
		var sumEvalScores float64
		var suiteTasks []stats.TaskCounts
		var newestSuiteTime time.Time
		var newestSuiteRun *v1alpha1.Run
		for _, es := range evalScoresInSuite {
			sumEvalScores += es.Point
			suiteTasks = append(suiteTasks, es.NewestTasks...)
			if es.NewestTime.After(newestSuiteTime) {
				newestSuiteTime = es.NewestTime
				newestSuiteRun = es.NewestRun
			}
		}
		suiteScorePoint := sumEvalScores / float64(len(evalScoresInSuite))
		suitePooledEst := stats.PooledWilson(suiteTasks, 0.95)
		suiteScoreStr := fmt.Sprintf("%.2f", suiteScorePoint)
		suiteCIStr := fmt.Sprintf("[%.2f, %.2f]", suitePooledEst.CI.Lower, suitePooledEst.CI.Upper)

		// Suite commits in chronological order
		var suiteCommits []CommitEntry
		for _, c := range obs.Commits {
			hasObs := false
			for _, eID := range evalIDsInSuite {
				if len(obs.ByCommit[eID][c.SHA]) > 0 {
					hasObs = true
					break
				}
			}
			if hasObs {
				suiteCommits = append(suiteCommits, c)
			}
		}

		// Suite commit is the newest commit where observations for this suite exist
		suiteCommitSHA := ""
		suiteCommitShort := ""
		if len(suiteCommits) > 0 {
			suiteCommitSHA = suiteCommits[len(suiteCommits)-1].SHA
			suiteCommitShort = ShortCommit(suiteCommitSHA)
		}

		suiteDeltaStr := "—"
		suiteDeltaClass := ""
		suiteHasDelta := false
		var suiteDelta *stats.DeltaEstimate
		if len(suiteCommits) > 1 {
			prevCommit := suiteCommits[len(suiteCommits)-2]
			var prevEvalScores []float64
			var prevSuiteTasks []stats.TaskCounts
			for _, eID := range evalIDsInSuite {
				eObs := obs.ByCommit[eID][prevCommit.SHA]
				if len(eObs) > 0 {
					var eTasks []stats.TaskCounts
					for _, o := range eObs {
						eTasks = append(eTasks, stats.TaskCounts{Successes: o.Successes, Samples: o.Samples})
						prevSuiteTasks = append(prevSuiteTasks, stats.TaskCounts{Successes: o.Successes, Samples: o.Samples})
					}
					eEst := stats.PooledWilson(eTasks, 0.95)
					prevEvalScores = append(prevEvalScores, eEst.Point)
				}
			}
			if len(prevEvalScores) > 0 {
				var prevSum float64
				for _, s := range prevEvalScores {
					prevSum += s
				}
				prevSuitePoint := prevSum / float64(len(prevEvalScores))
				prevSuitePooled := stats.PooledWilson(prevSuiteTasks, 0.95)

				curSuiteEst := stats.Estimate{
					Point:  suiteScorePoint,
					CI:     suitePooledEst.CI,
					N:      suitePooledEst.N,
					Method: "pooled-wilson",
				}
				prevSuiteEst := stats.Estimate{
					Point:  prevSuitePoint,
					CI:     prevSuitePooled.CI,
					N:      prevSuitePooled.N,
					Method: "pooled-wilson",
				}
				delta := stats.DifferenceUnpaired(curSuiteEst, prevSuiteEst, 0.95)
				suiteDelta = &delta
				suiteDeltaStr = FormatDeltaCI(delta.Delta, delta.CI.Lower, delta.CI.Upper)
				suiteDeltaClass = DeltaChipClass(delta.CI)
				suiteHasDelta = true
			}
		}

		sScore := SuiteScore{
			Name:        sName,
			Point:       suiteScorePoint,
			ScoreStr:    suiteScoreStr,
			CIStr:       suiteCIStr,
			Est:         suitePooledEst,
			HasDelta:    suiteHasDelta,
			DeltaStr:    suiteDeltaStr,
			DeltaClass:  suiteDeltaClass,
			Delta:       suiteDelta,
			EvalsCount:  len(evalScoresInSuite),
			CommitSHA:   suiteCommitSHA,
			CommitShort: suiteCommitShort,
			NewestTime:  newestSuiteTime,
			NewestRun:   newestSuiteRun,
			NewestTasks: suiteTasks,
			Commits:     suiteCommits,
			Evals:       evalScoresInSuite,
		}
		suitesResult[sName] = sScore
		sortedSuiteNames = append(sortedSuiteNames, sName)
	}

	// Overall headline score across all suites
	var overallPoint float64
	var allTasks []stats.TaskCounts
	for _, sName := range sortedSuiteNames {
		sr := suitesResult[sName]
		overallPoint += sr.Point
		allTasks = append(allTasks, sr.NewestTasks...)
	}
	if len(sortedSuiteNames) > 0 {
		overallPoint /= float64(len(sortedSuiteNames))
	}
	overallPooled := stats.PooledWilson(allTasks, 0.95)
	overallScoreStr := fmt.Sprintf("%.2f", overallPoint)
	overallCIStr := fmt.Sprintf("[%.2f, %.2f]", overallPooled.CI.Lower, overallPooled.CI.Upper)

	// Derive headline commit and time from newest commit that actually has observations
	var newestOverallCommitSHA string
	var newestOverallTime time.Time
	var newestOverallRun *v1alpha1.Run
	for i := len(obs.Commits) - 1; i >= 0; i-- {
		c := obs.Commits[i]
		hasAnyObs := false
		for _, eList := range obs.Suites {
			for _, eID := range eList {
				if len(obs.ByCommit[eID][c.SHA]) > 0 {
					hasAnyObs = true
					break
				}
			}
			if hasAnyObs {
				break
			}
		}
		if hasAnyObs {
			newestOverallCommitSHA = c.SHA
			newestOverallTime = c.CommitTime
			for _, sr := range obs.MatchedSeries {
				if runCommitSHA(sr.run) == c.SHA {
					newestOverallRun = sr.run
					break
				}
			}
			break
		}
	}

	return RepoScores{
		Suites:       suitesResult,
		SortedSuites: sortedSuiteNames,
		OverallPoint: overallPoint,
		OverallScore: overallScoreStr,
		OverallCI:    overallCIStr,
		OverallEst:   overallPooled,
		CommitSHA:    newestOverallCommitSHA,
		CommitShort:  ShortCommit(newestOverallCommitSHA),
		NewestTime:   newestOverallTime,
		NewestRun:    newestOverallRun,
	}
}

// EvalSparklineItems generates series items for an eval over the given sparkline commits.
func (obs *RepoObservations) EvalSparklineItems(evalID string, sparklineCommits []CommitEntry) []SeriesItem {
	var items []SeriesItem
	for _, sc := range sparklineCommits {
		obsList := obs.ByCommit[evalID][sc.SHA]
		sItem := SeriesItem{
			CommitSHA:      sc.SHA,
			CommitSHAShort: ShortCommit(sc.SHA),
			CommitTime:     sc.CommitTime,
		}
		if len(obsList) > 0 {
			var cTasks []stats.TaskCounts
			for _, o := range obsList {
				cTasks = append(cTasks, stats.TaskCounts{Successes: o.Successes, Samples: o.Samples})
			}
			cEst := stats.PooledWilson(cTasks, 0.95)
			p := cEst.Point
			ci := v1alpha1.ConfidenceInterval{
				Lower:      cEst.CI.Lower,
				Upper:      cEst.CI.Upper,
				Confidence: cEst.Confidence,
				Method:     cEst.Method,
			}
			sItem.HasPoint = true
			sItem.SuccessRate = &p
			sItem.CI = &ci
			sItem.ObsCount = len(obsList)
		}
		items = append(items, sItem)
	}
	return items
}

// SuiteSparklineItems generates series items for an eval suite over the given sparkline commits.
func (obs *RepoObservations) SuiteSparklineItems(suiteName string, sparklineCommits []CommitEntry) []SeriesItem {
	evalIDs := obs.Suites[suiteName]
	var items []SeriesItem
	for _, sc := range sparklineCommits {
		sItem := SeriesItem{
			CommitSHA:      sc.SHA,
			CommitSHAShort: ShortCommit(sc.SHA),
			CommitTime:     sc.CommitTime,
		}
		var cEvalsScores []float64
		var cSuiteTasks []stats.TaskCounts
		obsInSuite := 0
		for _, eID := range evalIDs {
			obsList := obs.ByCommit[eID][sc.SHA]
			if len(obsList) > 0 {
				obsInSuite += len(obsList)
				var eTasks []stats.TaskCounts
				for _, o := range obsList {
					eTasks = append(eTasks, stats.TaskCounts{Successes: o.Successes, Samples: o.Samples})
					cSuiteTasks = append(cSuiteTasks, stats.TaskCounts{Successes: o.Successes, Samples: o.Samples})
				}
				eEst := stats.PooledWilson(eTasks, 0.95)
				cEvalsScores = append(cEvalsScores, eEst.Point)
			}
		}
		if len(cEvalsScores) > 0 {
			sum := 0.0
			for _, s := range cEvalsScores {
				sum += s
			}
			meanScore := sum / float64(len(cEvalsScores))
			cEst := stats.PooledWilson(cSuiteTasks, 0.95)
			ci := v1alpha1.ConfidenceInterval{
				Lower:      cEst.CI.Lower,
				Upper:      cEst.CI.Upper,
				Confidence: cEst.Confidence,
				Method:     cEst.Method,
			}
			sItem.HasPoint = true
			sItem.SuccessRate = &meanScore
			sItem.CI = &ci
			sItem.ObsCount = obsInSuite
		}
		items = append(items, sItem)
	}
	return items
}
