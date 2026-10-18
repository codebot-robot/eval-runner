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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/discovery"
	"github.com/gke-labs/eval-runner/pkg/github"
	"github.com/gke-labs/eval-runner/pkg/stats"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// PairedSampleScore records paired scores for a single sample ID.
type PairedSampleScore struct {
	ID             string  `json:"id"`
	BaseScore      float64 `json:"baseScore"`
	CandidateScore float64 `json:"candidateScore"`
	Delta          float64 `json:"delta"`
}

// ComparisonTaskArtifact records paired comparison details for a single task in the artifact JSON.
type ComparisonTaskArtifact struct {
	ID            string                           `json:"id"`
	BaseRate      float64                          `json:"baseRate"`
	CandidateRate float64                          `json:"candidateRate"`
	Delta         float64                          `json:"delta"`
	DeltaCI       v1alpha1.DeltaConfidenceInterval `json:"deltaCI"`
	Pairs         int                              `json:"pairs"`
	Wins          int                              `json:"wins"`
	Losses        int                              `json:"losses"`
	Ties          int                              `json:"ties"`
	Dropped       v1alpha1.ComparisonDropped       `json:"dropped"`
	Label         string                           `json:"label"`
	PImprovement  float64                          `json:"pImprovement"`
	PairsTable    []PairedSampleScore              `json:"pairsTable,omitempty"`
}

// ComparisonArtifact is the schema stored as comparisons/<name>.json in the artifact sink.
type ComparisonArtifact struct {
	Comparison   string                   `json:"comparison"`
	Namespace    string                   `json:"namespace"`
	Baseline     string                   `json:"baseline"`
	Candidate    string                   `json:"candidate"`
	Verdict      string                   `json:"verdict"`
	PImprovement float64                  `json:"pImprovement"`
	Summary      string                   `json:"summary,omitempty"`
	Tasks        []ComparisonTaskArtifact `json:"tasks"`
	CreatedAt    time.Time                `json:"createdAt"`
}

// ComparisonReconciler reconciles a Comparison object.
type ComparisonReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	ArtifactSink        artifacts.ArtifactSink
	ControllerNamespace string
	GitHubTokenSecret   string
	GitHubClientFactory func(token string) github.Client
}

func (r *ComparisonReconciler) artifactSink() artifacts.ArtifactSink {
	if r.ArtifactSink != nil {
		return r.ArtifactSink
	}
	return &artifacts.NoopSink{}
}

func (r *ComparisonReconciler) controllerNamespace() string {
	if r.ControllerNamespace != "" {
		return r.ControllerNamespace
	}
	return discovery.DefaultControllerNamespace
}

func (r *ComparisonReconciler) gitHubTokenSecret() string {
	return r.GitHubTokenSecret
}

func (r *ComparisonReconciler) reportToGitHub(
	ctx context.Context,
	comp *v1alpha1.Comparison,
	baseRun, candRun *v1alpha1.Run,
) error {
	if comp == nil || comp.Spec.PullRequest == nil {
		return nil
	}

	secretName := r.gitHubTokenSecret()
	if secretName == "" {
		comp.Status.Reporting = &v1alpha1.ComparisonReportingStatus{
			Message: "disabled",
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var secret corev1.Secret
	key := types.NamespacedName{
		Namespace: r.controllerNamespace(),
		Name:      secretName,
	}
	if err := r.Get(ctx, key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			comp.Status.Reporting = &v1alpha1.ComparisonReportingStatus{
				Message: "disabled",
			}
			return nil
		}
		return fmt.Errorf("failed to get github token secret: %w", err)
	}

	token := strings.TrimSpace(string(secret.Data["token"]))
	if token == "" {
		comp.Status.Reporting = &v1alpha1.ComparisonReportingStatus{
			Message: "disabled",
		}
		return nil
	}

	var ghClient github.Client
	if r.GitHubClientFactory != nil {
		ghClient = r.GitHubClientFactory(token)
	} else {
		ghClient = github.NewHTTPClient(github.HTTPClientConfig{Token: token})
	}

	reporter := github.NewReporter(ghClient)
	repStatus, err := reporter.Report(ctx, comp, baseRun, candRun)
	if repStatus != nil {
		comp.Status.Reporting = repStatus
	}
	if err != nil {
		if github.IsPermanent(err) {
			if comp.Status.Reporting == nil {
				comp.Status.Reporting = &v1alpha1.ComparisonReportingStatus{}
			}
			comp.Status.Reporting.Message = err.Error()
			return nil
		}
		return err
	}

	return nil
}

func isTerminalRunPhase(phase v1alpha1.RunPhase) bool {
	return phase == v1alpha1.RunPhaseCompleted || phase == v1alpha1.RunPhaseFailed
}

// SetupWithManager sets up the controller with the Manager and watches Runs.
func (r *ComparisonReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Comparison{}).
		Watches(
			&v1alpha1.Run{},
			handler.EnqueueRequestsFromMapFunc(r.findComparisonsForRun),
		).
		Complete(r)
}

func (r *ComparisonReconciler) findComparisonsForRun(ctx context.Context, obj client.Object) []reconcile.Request {
	run, ok := obj.(*v1alpha1.Run)
	if !ok {
		return nil
	}

	var compList v1alpha1.ComparisonList
	if err := r.List(ctx, &compList, client.InNamespace(run.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, comp := range compList.Items {
		if comp.Spec.Baseline.Run == run.Name || comp.Spec.Candidate.Run == run.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: comp.Namespace,
					Name:      comp.Name,
				},
			})
		}
	}
	return requests
}

func (r *ComparisonReconciler) determineTaskIDs(ctx context.Context, comp *v1alpha1.Comparison, baseRun, candRun *v1alpha1.Run) []string {
	if len(comp.Spec.Tasks) > 0 {
		return comp.Spec.Tasks
	}

	taskMap := make(map[string]bool)

	// Collect from candidate discovery or manifest
	if candRun.Status.Discovery != nil {
		if candRun.Status.Discovery.TaskCount > len(candRun.Status.Discovery.TaskIDs) && candRun.Status.Discovery.ManifestPath != "" {
			// Needs more than capped IDs: load the manifest by URI
			manifestBytes, err := r.artifactSink().Load(ctx, candRun.Status.Discovery.ManifestPath)
			if err != nil {
				for _, id := range candRun.Status.Discovery.TaskIDs {
					taskMap[id] = true
				}
			} else {
				m, parseErr := discovery.ParseManifest(manifestBytes)
				if parseErr != nil || m == nil {
					for _, id := range candRun.Status.Discovery.TaskIDs {
						taskMap[id] = true
					}
				} else {
					for _, t := range m.Tasks {
						taskMap[t.ID] = true
					}
				}
			}
		} else {
			for _, id := range candRun.Status.Discovery.TaskIDs {
				taskMap[id] = true
			}
		}
	}

	// Also check baseline discovery
	if baseRun.Status.Discovery != nil {
		if baseRun.Status.Discovery.TaskCount > len(baseRun.Status.Discovery.TaskIDs) && baseRun.Status.Discovery.ManifestPath != "" {
			manifestBytes, err := r.artifactSink().Load(ctx, baseRun.Status.Discovery.ManifestPath)
			if err != nil {
				for _, id := range baseRun.Status.Discovery.TaskIDs {
					taskMap[id] = true
				}
			} else {
				m, parseErr := discovery.ParseManifest(manifestBytes)
				if parseErr != nil || m == nil {
					for _, id := range baseRun.Status.Discovery.TaskIDs {
						taskMap[id] = true
					}
				} else {
					for _, t := range m.Tasks {
						taskMap[t.ID] = true
					}
				}
			}
		} else {
			for _, id := range baseRun.Status.Discovery.TaskIDs {
				taskMap[id] = true
			}
		}
	}

	for _, t := range candRun.Status.Tasks {
		taskMap[t.ID] = true
	}
	for _, t := range baseRun.Status.Tasks {
		taskMap[t.ID] = true
	}

	var taskIDs []string
	for id := range taskMap {
		taskIDs = append(taskIDs, id)
	}
	sort.Strings(taskIDs)
	return taskIDs
}

func findTaskStatus(tasks []v1alpha1.TaskStatus, taskID string) *v1alpha1.TaskStatus {
	for i := range tasks {
		if tasks[i].ID == taskID {
			return &tasks[i]
		}
	}
	return nil
}

// Reconcile handles a reconciliation request for a Comparison.
func (r *ComparisonReconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var comp v1alpha1.Comparison
	if err := r.Get(ctx, req.NamespacedName, &comp); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if comp.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	baselineRunKey := types.NamespacedName{
		Namespace: comp.Namespace,
		Name:      comp.Spec.Baseline.Run,
	}
	candidateRunKey := types.NamespacedName{
		Namespace: comp.Namespace,
		Name:      comp.Spec.Candidate.Run,
	}

	var baselineRun v1alpha1.Run
	errBase := r.Get(ctx, baselineRunKey, &baselineRun)

	var candidateRun v1alpha1.Run
	errCand := r.Get(ctx, candidateRunKey, &candidateRun)

	if apierrors.IsNotFound(errBase) || apierrors.IsNotFound(errCand) {
		var missing []string
		if apierrors.IsNotFound(errBase) {
			missing = append(missing, fmt.Sprintf("baseline run %q", comp.Spec.Baseline.Run))
		}
		if apierrors.IsNotFound(errCand) {
			missing = append(missing, fmt.Sprintf("candidate run %q", comp.Spec.Candidate.Run))
		}
		msg := fmt.Sprintf("Waiting for %s to be created", strings.Join(missing, " and "))
		return r.updateComparisonStatus(ctx, &comp, nil, nil, func(status *v1alpha1.ComparisonStatus) {
			status.Phase = v1alpha1.ComparisonPhaseRunning
			status.Verdict = v1alpha1.ComparisonVerdictRunning
			status.Summary = &v1alpha1.ComparisonSummary{Tasks: "0/0"}
			status.Message = msg
		})
	}
	if errBase != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get baseline run: %w", errBase)
	}
	if errCand != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get candidate run: %w", errCand)
	}

	// If either Run is not terminal, the Comparison is RUNNING and re-evaluates on Run updates
	isTerminalBase := isTerminalRunPhase(baselineRun.Status.Phase)
	isTerminalCand := isTerminalRunPhase(candidateRun.Status.Phase)

	if !isTerminalBase || !isTerminalCand {
		taskIDs := r.determineTaskIDs(ctx, &comp, &baselineRun, &candidateRun)
		var tasks []v1alpha1.ComparisonTaskStatus
		for _, id := range taskIDs {
			tasks = append(tasks, v1alpha1.ComparisonTaskStatus{
				ID:    id,
				Label: v1alpha1.ComparisonTaskLabelRunning,
			})
		}
		msg := "Runs are still executing"
		if !isTerminalBase && !isTerminalCand {
			msg = fmt.Sprintf("Both baseline run %q (%s) and candidate run %q (%s) are executing",
				baselineRun.Name, baselineRun.Status.Phase, candidateRun.Name, candidateRun.Status.Phase)
		} else if !isTerminalBase {
			msg = fmt.Sprintf("Baseline run %q (%s) is executing", baselineRun.Name, baselineRun.Status.Phase)
		} else {
			msg = fmt.Sprintf("Candidate run %q (%s) is executing", candidateRun.Name, candidateRun.Status.Phase)
		}

		return r.updateComparisonStatus(ctx, &comp, &baselineRun, &candidateRun, func(status *v1alpha1.ComparisonStatus) {
			status.Phase = v1alpha1.ComparisonPhaseRunning
			status.Verdict = v1alpha1.ComparisonVerdictRunning
			status.Tasks = tasks
			status.Summary = &v1alpha1.ComparisonSummary{
				Tasks: fmt.Sprintf("0/%d", len(tasks)),
			}
			status.Message = msg
		})
	}

	// Both Runs are terminal; perform pairing and comparison
	taskIDs := r.determineTaskIDs(ctx, &comp, &baselineRun, &candidateRun)
	margin := stats.DefaultEquivalenceMargin
	if comp.Spec.EquivalenceMargin != nil {
		margin = *comp.Spec.EquivalenceMargin
	}

	var taskStatuses []v1alpha1.ComparisonTaskStatus
	var reports []stats.TaskReport
	var taskArtifacts []ComparisonTaskArtifact

	for _, taskID := range taskIDs {
		candTask := findTaskStatus(candidateRun.Status.Tasks, taskID)
		baseTask := findTaskStatus(baselineRun.Status.Tasks, taskID)

		if candTask == nil || baseTask == nil ||
			candTask.Phase != v1alpha1.TaskPhaseCompleted ||
			baseTask.Phase != v1alpha1.TaskPhaseCompleted ||
			candTask.LogPath == "" || baseTask.LogPath == "" {

			taskStatuses = append(taskStatuses, v1alpha1.ComparisonTaskStatus{
				ID:    taskID,
				Label: v1alpha1.ComparisonTaskLabelInconclusive,
				Pairs: 0,
			})
			reports = append(reports, stats.TaskReport{
				ID:           taskID,
				Label:        stats.TaskLabelInconclusive,
				PImprovement: 0.5,
			})
			taskArtifacts = append(taskArtifacts, ComparisonTaskArtifact{
				ID:    taskID,
				Label: string(v1alpha1.ComparisonTaskLabelInconclusive),
			})
			continue
		}

		// Verify that log paths match the commit prefixes of the respective runs
		candPrefix := r.artifactSink().ArtifactPrefix(candidateRun.Spec.Repo.Commit)
		if candPrefix != "" && !strings.HasPrefix(candTask.LogPath, candPrefix) {
			msg := fmt.Sprintf("candidate log path %q does not match commit prefix %q", candTask.LogPath, candPrefix)
			logger.Error(nil, msg, "task", taskID)
			err := fmt.Errorf("%s", msg)
			if _, statusErr := r.updateComparisonStatus(ctx, &comp, &baselineRun, &candidateRun, func(status *v1alpha1.ComparisonStatus) {
				status.Message = msg
			}); statusErr != nil {
				return ctrl.Result{}, errors.Join(err, statusErr)
			}
			return ctrl.Result{}, err
		}

		basePrefix := r.artifactSink().ArtifactPrefix(baselineRun.Spec.Repo.Commit)
		if basePrefix != "" && !strings.HasPrefix(baseTask.LogPath, basePrefix) {
			msg := fmt.Sprintf("baseline log path %q does not match commit prefix %q", baseTask.LogPath, basePrefix)
			logger.Error(nil, msg, "task", taskID)
			err := fmt.Errorf("%s", msg)
			if _, statusErr := r.updateComparisonStatus(ctx, &comp, &baselineRun, &candidateRun, func(status *v1alpha1.ComparisonStatus) {
				status.Message = msg
			}); statusErr != nil {
				return ctrl.Result{}, errors.Join(err, statusErr)
			}
			return ctrl.Result{}, err
		}

		// Load Inspect log for candidate
		candData, err := r.artifactSink().Load(ctx, candTask.LogPath)
		if err != nil {
			msg := fmt.Sprintf("failed to load candidate log for task %q (%s): %v", taskID, candTask.LogPath, err)
			logger.Error(err, "failed to load candidate log", "task", taskID, "path", candTask.LogPath)
			if _, statusErr := r.updateComparisonStatus(ctx, &comp, &baselineRun, &candidateRun, func(status *v1alpha1.ComparisonStatus) {
				status.Message = msg
			}); statusErr != nil {
				return ctrl.Result{}, errors.Join(err, statusErr)
			}
			return ctrl.Result{}, err
		}

		// Load Inspect log for baseline
		baseData, err := r.artifactSink().Load(ctx, baseTask.LogPath)
		if err != nil {
			msg := fmt.Sprintf("failed to load baseline log for task %q (%s): %v", taskID, baseTask.LogPath, err)
			logger.Error(err, "failed to load baseline log", "task", taskID, "path", baseTask.LogPath)
			if _, statusErr := r.updateComparisonStatus(ctx, &comp, &baselineRun, &candidateRun, func(status *v1alpha1.ComparisonStatus) {
				status.Message = msg
			}); statusErr != nil {
				return ctrl.Result{}, errors.Join(err, statusErr)
			}
			return ctrl.Result{}, err
		}

		candScores, err := artifacts.ParseInspectSampleScores(candData)
		if err != nil {
			msg := fmt.Sprintf("failed to parse candidate sample scores for task %q: %v", taskID, err)
			logger.Error(err, "failed to parse candidate sample scores", "task", taskID)
			if _, statusErr := r.updateComparisonStatus(ctx, &comp, &baselineRun, &candidateRun, func(status *v1alpha1.ComparisonStatus) {
				status.Message = msg
			}); statusErr != nil {
				return ctrl.Result{}, errors.Join(err, statusErr)
			}
			return ctrl.Result{}, err
		}
		baseScores, err := artifacts.ParseInspectSampleScores(baseData)
		if err != nil {
			msg := fmt.Sprintf("failed to parse baseline sample scores for task %q: %v", taskID, err)
			logger.Error(err, "failed to parse baseline sample scores", "task", taskID)
			if _, statusErr := r.updateComparisonStatus(ctx, &comp, &baselineRun, &candidateRun, func(status *v1alpha1.ComparisonStatus) {
				status.Message = msg
			}); statusErr != nil {
				return ctrl.Result{}, errors.Join(err, statusErr)
			}
			return ctrl.Result{}, err
		}

		est := stats.ComparePaired(candScores, baseScores, 0.95)

		// When est.NumPairs == 0, decide inconclusive explicitly (otherwise Decide returns continue).
		// When NumPairs > 0, sampleCap = est.NumPairs so that unresolved tasks become inconclusive.
		var dec stats.Decision
		if est.NumPairs == 0 {
			dec = stats.Decision{
				Label:  stats.TaskLabelInconclusive,
				Stop:   true,
				Reason: "no matching sample IDs between candidate and baseline",
			}
			est.PImprovement = 0.5
		} else {
			dec = stats.Decide(est, margin, est.NumPairs, est.NumPairs, 0, 0)
		}

		taskStatus := v1alpha1.ComparisonTaskStatus{
			ID:            taskID,
			BaseRate:      &est.BaseMean,
			CandidateRate: &est.CandidateMean,
			Delta:         &est.MeanDelta,
			DeltaCI: &v1alpha1.DeltaConfidenceInterval{
				Lower: est.CI.Lower,
				Upper: est.CI.Upper,
			},
			Pairs:  est.NumPairs,
			Wins:   est.Wins,
			Losses: est.Losses,
			Ties:   est.Ties,
			Dropped: v1alpha1.ComparisonDropped{
				Candidate: est.DroppedCandidate,
				Baseline:  est.DroppedBaseline,
			},
			Label: v1alpha1.ComparisonTaskLabel(dec.Label),
		}
		taskStatuses = append(taskStatuses, taskStatus)

		reports = append(reports, stats.TaskReport{
			ID:           taskID,
			Label:        dec.Label,
			Estimate:     est,
			PImprovement: est.PImprovement,
		})

		// Build paired table for artifact
		candMap := make(map[string]float64)
		for _, s := range candScores {
			candMap[s.ID] = s.Score
		}
		baseMap := make(map[string]float64)
		for _, s := range baseScores {
			baseMap[s.ID] = s.Score
		}
		var commonIDs []string
		for id := range candMap {
			if _, ok := baseMap[id]; ok {
				commonIDs = append(commonIDs, id)
			}
		}
		sort.Strings(commonIDs)

		var pairsTable []PairedSampleScore
		for _, id := range commonIDs {
			cScore := candMap[id]
			bScore := baseMap[id]
			pairsTable = append(pairsTable, PairedSampleScore{
				ID:             id,
				BaseScore:      bScore,
				CandidateScore: cScore,
				Delta:          cScore - bScore,
			})
		}

		taskArtifacts = append(taskArtifacts, ComparisonTaskArtifact{
			ID:            taskID,
			BaseRate:      est.BaseMean,
			CandidateRate: est.CandidateMean,
			Delta:         est.MeanDelta,
			DeltaCI: v1alpha1.DeltaConfidenceInterval{
				Lower: est.CI.Lower,
				Upper: est.CI.Upper,
			},
			Pairs:  est.NumPairs,
			Wins:   est.Wins,
			Losses: est.Losses,
			Ties:   est.Ties,
			Dropped: v1alpha1.ComparisonDropped{
				Candidate: est.DroppedCandidate,
				Baseline:  est.DroppedBaseline,
			},
			Label:        string(dec.Label),
			PImprovement: est.PImprovement,
			PairsTable:   pairsTable,
		})
	}

	runVerdict := stats.AggregateVerdict(reports)
	pImp := runVerdict.PImprovement

	// Store paired table as comparisons/<name>.json in the artifact sink
	artifact := ComparisonArtifact{
		Comparison:   comp.Name,
		Namespace:    comp.Namespace,
		Baseline:     comp.Spec.Baseline.Run,
		Candidate:    comp.Spec.Candidate.Run,
		Verdict:      string(runVerdict.Verdict),
		PImprovement: runVerdict.PImprovement,
		Summary:      runVerdict.Summary,
		Tasks:        taskArtifacts,
		CreatedAt:    time.Now().UTC(),
	}

	var summaryPath string
	artJSON, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		logger.Error(err, "failed to marshal comparison artifact", "comparison", comp.Name)
	} else {
		storedPath, storeErr := r.artifactSink().StoreComparison(ctx, comp.Name, artJSON)
		if storeErr != nil {
			logger.Error(storeErr, "failed to store comparison artifact in sink", "comparison", comp.Name)
		} else {
			summaryPath = storedPath
		}
	}

	completedTasks := 0
	for _, t := range taskStatuses {
		if t.Label != v1alpha1.ComparisonTaskLabelRunning {
			completedTasks++
		}
	}

	phase := v1alpha1.ComparisonPhaseCompleted
	if runVerdict.Verdict == stats.VerdictRunning {
		phase = v1alpha1.ComparisonPhaseRunning
	}

	return r.updateComparisonStatus(ctx, &comp, &baselineRun, &candidateRun, func(status *v1alpha1.ComparisonStatus) {
		status.Phase = phase
		status.Verdict = v1alpha1.ComparisonVerdict(runVerdict.Verdict)
		status.PImprovement = &pImp
		status.Tasks = taskStatuses
		status.Summary = &v1alpha1.ComparisonSummary{
			Tasks: fmt.Sprintf("%d/%d", completedTasks, len(taskStatuses)),
		}
		status.SummaryPath = summaryPath
		status.Message = runVerdict.Summary
	})
}

func (r *ComparisonReconciler) updateComparisonStatus(
	ctx context.Context,
	comp *v1alpha1.Comparison,
	baseRun, candRun *v1alpha1.Run,
	mutate func(*v1alpha1.ComparisonStatus),
) (ctrl.Result, error) {
	key := types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}

	// 1. Fetch latest object and apply in-memory status mutations
	var latest v1alpha1.Comparison
	if err := r.Get(ctx, key, &latest); err != nil {
		return ctrl.Result{}, err
	}
	mutate(&latest.Status)

	// 2. Perform GitHub reporting ONCE outside the RetryOnConflict loop
	var reportErr error
	if latest.Spec.PullRequest != nil {
		if err := r.reportToGitHub(ctx, &latest, baseRun, candRun); err != nil {
			reportErr = err
		}
	}
	reportingStatus := latest.Status.Reporting.DeepCopy()

	// 3. Persist status with conflict retry
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.Comparison
		if err := r.Get(ctx, key, &current); err != nil {
			return err
		}
		mutate(&current.Status)
		if latest.Spec.PullRequest != nil {
			current.Status.Reporting = reportingStatus.DeepCopy()
		}
		return r.Status().Update(ctx, &current)
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update Comparison status: %w", err)
	}
	if reportErr != nil {
		return ctrl.Result{}, reportErr
	}
	return ctrl.Result{}, nil
}
