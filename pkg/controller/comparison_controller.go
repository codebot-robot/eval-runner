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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/discovery"
	"github.com/gke-labs/eval-runner/pkg/github"
	"github.com/gke-labs/eval-runner/pkg/stats"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// LabelBatchKey identifies a baseline batch evaluation by its deterministic hash.
	LabelBatchKey = "evalrunner.gke-labs.dev/batch-key"
	// LabelComparison references the parent Comparison resource.
	LabelComparison = "evalrunner.gke-labs.dev/comparison"
	// LabelBatch is the 1-based index of the evaluation batch.
	LabelBatch = "evalrunner.gke-labs.dev/batch"
	// LabelRole indicates whether a batch run is candidate or baseline.
	LabelRole = "evalrunner.gke-labs.dev/role"
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
	ID              string                           `json:"id"`
	BaseRate        float64                          `json:"baseRate"`
	CandidateRate   float64                          `json:"candidateRate"`
	Delta           float64                          `json:"delta"`
	DeltaCI         v1alpha1.DeltaConfidenceInterval `json:"deltaCI"`
	Pairs           int                              `json:"pairs"`
	Wins            int                              `json:"wins"`
	Losses          int                              `json:"losses"`
	Ties            int                              `json:"ties"`
	Dropped         v1alpha1.ComparisonDropped       `json:"dropped"`
	Label           string                           `json:"label"`
	PImprovement    float64                          `json:"pImprovement"`
	SamplesRun      int                              `json:"samplesRun,omitempty"`
	Samples         int                              `json:"samples,omitempty"`
	Decision        string                           `json:"decision,omitempty"`
	SuggestedNext   int                              `json:"suggestedNext,omitempty"`
	Tokens          *int                             `json:"tokens,omitempty"`
	CandidateTokens *int                             `json:"candidateTokens,omitempty"`
	BaseTokens      *int                             `json:"baseTokens,omitempty"`
	PairsTable      []PairedSampleScore              `json:"pairsTable,omitempty"`
}

// ComparisonArtifact is the schema stored as comparisons/<name>.json in the artifact sink.
type ComparisonArtifact struct {
	Comparison   string                           `json:"comparison"`
	Namespace    string                           `json:"namespace"`
	Baseline     string                           `json:"baseline"`
	Candidate    string                           `json:"candidate"`
	Verdict      string                           `json:"verdict"`
	PImprovement float64                          `json:"pImprovement"`
	Summary      string                           `json:"summary,omitempty"`
	Tasks        []ComparisonTaskArtifact         `json:"tasks"`
	Batches      []v1alpha1.ComparisonBatchStatus `json:"batches,omitempty"`
	CreatedAt    time.Time                        `json:"createdAt"`
}

// GitHubCredentials holds credentials extracted from a Secret for factory injection.
type GitHubCredentials struct {
	Token          string
	AppID          string
	InstallationID string
	PrivateKey     []byte
	AppSlug        string
	AppAuth        *github.AppTokenProvider
}

// ComparisonReconciler reconciles a Comparison object.
type ComparisonReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	ArtifactSink        artifacts.ArtifactSink
	ControllerNamespace string
	GitHubTokenSecret   string
	GitHubAppSecret     string
	UIBaseURL           string
	GitHubBaseURL       string
	GitHubClientFactory func(creds GitHubCredentials) github.Client

	cachedAppMu   sync.Mutex
	cachedAppKey  string
	cachedAppAuth *github.AppTokenProvider
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

func (r *ComparisonReconciler) getOrCreateAppAuth(secret *corev1.Secret, appID, installationID, appSlug string, privKey []byte) (*github.AppTokenProvider, error) {
	cacheKey := fmt.Sprintf("%s/%s:%s", secret.Namespace, secret.Name, secret.ResourceVersion)
	if secret.ResourceVersion == "" {
		h := sha256.Sum256(append([]byte(appID+":"+installationID+":"), privKey...))
		cacheKey = fmt.Sprintf("%s/%s:%x", secret.Namespace, secret.Name, h)
	}

	r.cachedAppMu.Lock()
	defer r.cachedAppMu.Unlock()

	if r.cachedAppKey == cacheKey && r.cachedAppAuth != nil {
		return r.cachedAppAuth, nil
	}

	appAuth, err := github.NewAppTokenProvider(github.AppConfig{
		AppID:          appID,
		InstallationID: installationID,
		PrivateKeyPEM:  privKey,
		AppSlug:        appSlug,
		BaseURL:        r.GitHubBaseURL,
	})
	if err != nil {
		return nil, err
	}

	r.cachedAppKey = cacheKey
	r.cachedAppAuth = appAuth
	return appAuth, nil
}

func (r *ComparisonReconciler) reportToGitHub(
	ctx context.Context,
	comp *v1alpha1.Comparison,
	baseRun, candRun *v1alpha1.Run,
) error {
	if comp == nil || comp.Spec.PullRequest == nil {
		return nil
	}

	secretNames := []string{}
	if r.GitHubAppSecret != "" {
		secretNames = append(secretNames, r.GitHubAppSecret)
	}
	if r.gitHubTokenSecret() != "" && r.gitHubTokenSecret() != r.GitHubAppSecret {
		secretNames = append(secretNames, r.gitHubTokenSecret())
	}

	if len(secretNames) == 0 {
		comp.Status.Reporting = &v1alpha1.ComparisonReportingStatus{
			Message: "disabled",
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var ghClient github.Client
	for _, secretName := range secretNames {
		var secret corev1.Secret
		key := types.NamespacedName{
			Namespace: r.controllerNamespace(),
			Name:      secretName,
		}
		if err := r.Get(ctx, key, &secret); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("failed to get github credentials secret %q: %w", secretName, err)
		}

		appID := strings.TrimSpace(string(secret.Data["app-id"]))
		if appID == "" {
			appID = strings.TrimSpace(string(secret.Data["app_id"]))
		}
		installationID := strings.TrimSpace(string(secret.Data["installation-id"]))
		if installationID == "" {
			installationID = strings.TrimSpace(string(secret.Data["installation_id"]))
		}
		privKey := secret.Data["private-key"]
		if len(privKey) == 0 {
			privKey = secret.Data["private_key"]
		}
		appSlug := strings.TrimSpace(string(secret.Data["app-slug"]))
		if appSlug == "" {
			appSlug = strings.TrimSpace(string(secret.Data["app_slug"]))
		}
		token := strings.TrimSpace(string(secret.Data["token"]))

		// App credentials are preferred over token
		if appID != "" && installationID != "" && len(privKey) > 0 {
			appAuth, err := r.getOrCreateAppAuth(&secret, appID, installationID, appSlug, privKey)
			if err != nil {
				comp.Status.Reporting = &v1alpha1.ComparisonReportingStatus{
					Message: fmt.Sprintf("invalid app credentials: %v", err),
				}
				return nil
			}

			creds := GitHubCredentials{
				AppID:          appID,
				InstallationID: installationID,
				PrivateKey:     privKey,
				AppSlug:        appSlug,
				AppAuth:        appAuth,
			}
			if r.GitHubClientFactory != nil {
				ghClient = r.GitHubClientFactory(creds)
			} else {
				ghClient = github.NewHTTPClient(github.HTTPClientConfig{
					AppAuth: appAuth,
					BaseURL: r.GitHubBaseURL,
				})
			}
			break
		} else if token != "" {
			creds := GitHubCredentials{
				Token: token,
			}
			if r.GitHubClientFactory != nil {
				ghClient = r.GitHubClientFactory(creds)
			} else {
				ghClient = github.NewHTTPClient(github.HTTPClientConfig{
					Token:   token,
					BaseURL: r.GitHubBaseURL,
				})
			}
			break
		}
	}

	if ghClient == nil {
		comp.Status.Reporting = &v1alpha1.ComparisonReportingStatus{
			Message: "disabled",
		}
		return nil
	}

	reporter := github.NewReporter(ghClient, github.ReporterOptions{
		UIBaseURL: r.UIBaseURL,
	})
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

func toComparisonTaskLabel(label stats.TaskLabel) v1alpha1.ComparisonTaskLabel {
	if label == stats.TaskLabelContinue {
		return v1alpha1.ComparisonTaskLabelRunning
	}
	return v1alpha1.ComparisonTaskLabel(label)
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
		matched := false
		if comp.Spec.Baseline.Run == run.Name || comp.Spec.Candidate.Run == run.Name {
			matched = true
		}
		if !matched {
			for _, b := range comp.Status.Batches {
				if b.CandidateRun == run.Name || b.BaselineRun == run.Name {
					matched = true
					break
				}
			}
		}
		if !matched {
			for _, ref := range run.OwnerReferences {
				if ref.Kind == "Comparison" && ref.Name == comp.Name {
					matched = true
					break
				}
			}
		}
		if matched {
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

// ComputeBatchKey calculates a deterministic SHA-256 hash identifying a baseline batch evaluation:
// repo URL, commit, model, sorted task IDs, and sample window.
func ComputeBatchKey(repoURL, commit, model string, tasks []string, window v1alpha1.SampleRange) string {
	sortedTasks := make([]string, len(tasks))
	copy(sortedTasks, tasks)
	sort.Strings(sortedTasks)
	raw := fmt.Sprintf("%s\n%s\n%s\n%s\n%d-%d", repoURL, commit, model, strings.Join(sortedTasks, ","), window.Start, window.End)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", h)[:32]
}

func sameTaskSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa := make([]string, len(a))
	copy(sa, a)
	sort.Strings(sa)
	sb := make([]string, len(b))
	copy(sb, b)
	sort.Strings(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

func (r *ComparisonReconciler) findCompletedBaselineRun(ctx context.Context, namespace, repoURL, commit, model string, tasks []string, window v1alpha1.SampleRange) (*v1alpha1.Run, error) {
	batchKey := ComputeBatchKey(repoURL, commit, model, tasks, window)

	// 1. Label selector search
	var runList v1alpha1.RunList
	if err := r.List(ctx, &runList, client.InNamespace(namespace), client.MatchingLabels{
		LabelBatchKey: batchKey,
	}); err != nil {
		return nil, fmt.Errorf("failed to list runs by batch-key: %w", err)
	}
	for _, run := range runList.Items {
		if run.Status.Phase == v1alpha1.RunPhaseCompleted && run.DeletionTimestamp == nil {
			return &run, nil
		}
	}

	// 2. Spec-matching fallback across completed runs in namespace
	if err := r.List(ctx, &runList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list runs for baseline matching: %w", err)
	}
	for _, run := range runList.Items {
		if run.Status.Phase != v1alpha1.RunPhaseCompleted || run.DeletionTimestamp != nil {
			continue
		}
		if run.Spec.Repo.URL == repoURL && run.Spec.Repo.Commit == commit && run.Spec.Model == model {
			effRange := EffectiveSampleRange(run.Spec)
			if effRange != nil && effRange.Start == window.Start && effRange.End == window.End {
				if sameTaskSet(run.Spec.Tasks, tasks) {
					return &run, nil
				}
			}
		}
	}

	return nil, nil
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

	// Initialize batches if not already recorded in status
	batches := make([]v1alpha1.ComparisonBatchStatus, len(comp.Status.Batches))
	copy(batches, comp.Status.Batches)

	if len(batches) == 0 {
		var b1Range v1alpha1.SampleRange
		if eff := EffectiveSampleRange(candidateRun.Spec); eff != nil {
			b1Range = *eff
		} else if eff := EffectiveSampleRange(baselineRun.Spec); eff != nil {
			b1Range = *eff
		} else if len(candidateRun.Status.Tasks) > 0 && candidateRun.Status.Tasks[0].SampleRange != nil {
			b1Range = *candidateRun.Status.Tasks[0].SampleRange
		} else if len(baselineRun.Status.Tasks) > 0 && baselineRun.Status.Tasks[0].SampleRange != nil {
			b1Range = *baselineRun.Status.Tasks[0].SampleRange
		} else if comp.Spec.Sampling != nil {
			initial := comp.Spec.Sampling.EffectiveInitialBatch()
			b1Range = v1alpha1.SampleRange{Start: 1, End: initial}
		} else if len(candidateRun.Status.Tasks) > 0 && candidateRun.Status.Tasks[0].Samples > 0 {
			b1Range = v1alpha1.SampleRange{Start: 1, End: candidateRun.Status.Tasks[0].Samples}
		} else {
			initial := comp.Spec.Sampling.EffectiveInitialBatch()
			b1Range = v1alpha1.SampleRange{Start: 1, End: initial}
		}

		batches = append(batches, v1alpha1.ComparisonBatchStatus{
			Index:        1,
			SampleRange:  b1Range,
			CandidateRun: comp.Spec.Candidate.Run,
			BaselineRun:  comp.Spec.Baseline.Run,
			Phase:        v1alpha1.ComparisonBatchPhaseRunning,
		})
	}

	// Fetch all batch Runs and determine terminal phases
	type batchRunPair struct {
		candRun *v1alpha1.Run
		baseRun *v1alpha1.Run
	}
	batchRuns := make([]batchRunPair, len(batches))
	allBatchesTerminal := true

	for i := range batches {
		var bCand, bBase v1alpha1.Run
		cErr := r.Get(ctx, types.NamespacedName{Namespace: comp.Namespace, Name: batches[i].CandidateRun}, &bCand)
		bErr := r.Get(ctx, types.NamespacedName{Namespace: comp.Namespace, Name: batches[i].BaselineRun}, &bBase)

		if cErr != nil || bErr != nil {
			batches[i].Phase = v1alpha1.ComparisonBatchPhaseRunning
			allBatchesTerminal = false
			continue
		}

		batchRuns[i] = batchRunPair{candRun: &bCand, baseRun: &bBase}

		termC := isTerminalRunPhase(bCand.Status.Phase)
		termB := isTerminalRunPhase(bBase.Status.Phase)

		if !termC || !termB {
			batches[i].Phase = v1alpha1.ComparisonBatchPhaseRunning
			allBatchesTerminal = false
		} else if bCand.Status.Phase == v1alpha1.RunPhaseFailed || bBase.Status.Phase == v1alpha1.RunPhaseFailed {
			batches[i].Phase = v1alpha1.ComparisonBatchPhaseFailed
		} else {
			batches[i].Phase = v1alpha1.ComparisonBatchPhaseCompleted
		}
	}

	// If any batch is still executing, comparison is Running
	if !allBatchesTerminal {
		taskIDs := r.determineTaskIDs(ctx, &comp, &baselineRun, &candidateRun)
		var tasks []v1alpha1.ComparisonTaskStatus
		for _, id := range taskIDs {
			tasks = append(tasks, v1alpha1.ComparisonTaskStatus{
				ID:    id,
				Label: v1alpha1.ComparisonTaskLabelRunning,
			})
		}
		msg := "Runs are still executing"
		return r.updateComparisonStatus(ctx, &comp, &baselineRun, &candidateRun, func(status *v1alpha1.ComparisonStatus) {
			status.Phase = v1alpha1.ComparisonPhaseRunning
			status.Verdict = v1alpha1.ComparisonVerdictRunning
			status.Batches = batches
			status.Tasks = tasks
			status.Summary = &v1alpha1.ComparisonSummary{
				Tasks: fmt.Sprintf("0/%d", len(tasks)),
			}
			status.Message = msg
		})
	}

	// All current batch Runs are terminal; load scores from every batch
	taskIDs := r.determineTaskIDs(ctx, &comp, &baselineRun, &candidateRun)
	margin := stats.DefaultEquivalenceMargin
	if comp.Spec.EquivalenceMargin != nil {
		margin = *comp.Spec.EquivalenceMargin
	}
	maxSamples := comp.Spec.Sampling.EffectiveMaxSamplesPerTask()
	shouldBatch := comp.Spec.Sampling != nil || comp.Spec.ExtraSamples > 0 || len(comp.Status.Batches) > 1

	type taskEval struct {
		taskID        string
		candScores    []stats.SampleScore
		baseScores    []stats.SampleScore
		tokens        *int
		candTokens    *int
		baseTokens    *int
		exhausted     bool
		failed        bool
		est           stats.PairedEstimate
		dec           stats.Decision
		suggestedNext int
		samplesRun    int
	}

	var taskEvals []taskEval

	for _, taskID := range taskIDs {
		var allCandScores []stats.SampleScore
		var allBaseScores []stats.SampleScore
		var totalTokens *int
		var totalCandTokens *int
		var totalBaseTokens *int
		taskExhausted := false
		taskFailed := false

		for batchIdx, pair := range batchRuns {
			if pair.candRun == nil || pair.baseRun == nil {
				continue
			}
			candTask := findTaskStatus(pair.candRun.Status.Tasks, taskID)
			baseTask := findTaskStatus(pair.baseRun.Status.Tasks, taskID)

			if candTask == nil && baseTask == nil {
				// Task was not part of this batch
				continue
			}

			// Check for zero-sample failure or "no scored samples" exhaustion signal
			if (candTask != nil && candTask.Phase == v1alpha1.TaskPhaseFailed && (candTask.Samples == 0 || strings.Contains(candTask.Message, "no scored samples"))) ||
				(baseTask != nil && baseTask.Phase == v1alpha1.TaskPhaseFailed && (baseTask.Samples == 0 || strings.Contains(baseTask.Message, "no scored samples"))) {
				taskExhausted = true
				continue
			}

			if candTask == nil || baseTask == nil ||
				candTask.Phase != v1alpha1.TaskPhaseCompleted ||
				baseTask.Phase != v1alpha1.TaskPhaseCompleted ||
				candTask.LogPath == "" || baseTask.LogPath == "" {
				taskFailed = true
				continue
			}

			// Verify log paths match commit prefixes
			candPrefix := r.artifactSink().ArtifactPrefix(pair.candRun.Spec.Repo.Commit)
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

			basePrefix := r.artifactSink().ArtifactPrefix(pair.baseRun.Spec.Repo.Commit)
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

			// Load candidate log
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

			// Load baseline log
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

			// Check window length for dataset exhaustion
			batchRange := EffectiveSampleRange(pair.candRun.Spec)
			if batchRange == nil {
				batchRange = candTask.SampleRange
			}
			if batchRange == nil && batchIdx < len(batches) {
				batchRange = &batches[batchIdx].SampleRange
			}

			if EffectiveSampleRange(pair.candRun.Spec) == nil && EffectiveSampleRange(pair.baseRun.Spec) == nil &&
				candTask.SampleRange == nil && comp.Spec.Sampling == nil && batchIdx == 0 {
				taskExhausted = true
			} else if batchRange != nil {
				winLen := batchRange.End - batchRange.Start + 1
				if len(candScores) < winLen || len(baseScores) < winLen {
					taskExhausted = true
				}
			}

			allCandScores = append(allCandScores, candScores...)
			allBaseScores = append(allBaseScores, baseScores...)

			if tok, err := artifacts.ExtractInspectTokens(candData); err != nil {
				logger.V(1).Info("failed to extract candidate tokens", "task", taskID, "error", err)
			} else if tok != nil {
				if totalCandTokens == nil {
					t := *tok
					totalCandTokens = &t
				} else {
					*totalCandTokens += *tok
				}
				if totalTokens == nil {
					t := *tok
					totalTokens = &t
				} else {
					*totalTokens += *tok
				}
			}

			if tok, err := artifacts.ExtractInspectTokens(baseData); err != nil {
				logger.V(1).Info("failed to extract baseline tokens", "task", taskID, "error", err)
			} else if tok != nil {
				if totalBaseTokens == nil {
					t := *tok
					totalBaseTokens = &t
				} else {
					*totalBaseTokens += *tok
				}
				if totalTokens == nil {
					t := *tok
					totalTokens = &t
				} else {
					*totalTokens += *tok
				}
			}
		}

		est := stats.ComparePaired(allCandScores, allBaseScores, 0.95)
		samplesRun := est.NumPairs
		if samplesRun == 0 && len(allCandScores) > 0 {
			samplesRun = len(allCandScores)
		}

		var dec stats.Decision
		if est.NumPairs == 0 {
			dec = stats.Decision{
				Label:  stats.TaskLabelInconclusive,
				Stop:   true,
				Reason: "no matching sample IDs between candidate and baseline",
			}
			est.PImprovement = 0.5
		} else if !shouldBatch {
			dec = stats.Decide(est, margin, samplesRun, samplesRun, 0, 0)
		} else if taskExhausted {
			dec = stats.Decide(est, margin, samplesRun, maxSamples, 0, 0)
			if dec.Label == stats.TaskLabelContinue {
				dec.Label = stats.TaskLabelInconclusive
				dec.Stop = true
				dec.Reason = "dataset exhausted"
			}
		} else {
			dec = stats.Decide(est, margin, samplesRun, maxSamples, 0, 0)
		}

		suggestedNext := 0
		if dec.EstimatedSamples > 0 {
			suggestedNext = dec.EstimatedSamples
		} else if dec.NextBatchSize > 0 {
			suggestedNext = dec.NextBatchSize
		}

		taskEvals = append(taskEvals, taskEval{
			taskID:        taskID,
			candScores:    allCandScores,
			baseScores:    allBaseScores,
			tokens:        totalTokens,
			candTokens:    totalCandTokens,
			baseTokens:    totalBaseTokens,
			exhausted:     taskExhausted,
			failed:        taskFailed,
			est:           est,
			dec:           dec,
			suggestedNext: suggestedNext,
			samplesRun:    samplesRun,
		})
	}

	// Check if any tasks need to continue sampling
	var continueTasks []string
	for _, te := range taskEvals {
		if te.dec.Label == stats.TaskLabelContinue && !te.exhausted && te.samplesRun < maxSamples {
			continueTasks = append(continueTasks, te.taskID)
		}
	}

	// Schedule next batch if any tasks continue
	if len(continueTasks) > 0 {
		nextBatchIndex := len(batches) + 1
		lastBatch := batches[len(batches)-1]
		nextStart := lastBatch.SampleRange.End + 1
		batchSize := comp.Spec.Sampling.EffectiveBatchSize()
		nextEnd := nextStart + batchSize - 1
		if nextEnd > maxSamples {
			nextEnd = maxSamples
		}

		if nextStart <= nextEnd {
			candCloneName := fmt.Sprintf("%s-b%d-cand", comp.Name, nextBatchIndex)
			baseRunName := fmt.Sprintf("%s-b%d-base", comp.Name, nextBatchIndex)

			batchRange := v1alpha1.SampleRange{Start: nextStart, End: nextEnd}
			batchKey := ComputeBatchKey(baselineRun.Spec.Repo.URL, baselineRun.Spec.Repo.Commit, baselineRun.Spec.Model, continueTasks, batchRange)

			// Baseline reuse lookup
			existingBase, err := r.findCompletedBaselineRun(ctx, comp.Namespace, baselineRun.Spec.Repo.URL, baselineRun.Spec.Repo.Commit, baselineRun.Spec.Model, continueTasks, batchRange)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("failed looking up baseline run for reuse: %w", err)
			}
			if existingBase != nil {
				baseRunName = existingBase.Name
				logger.Info("reusing completed baseline run for batch", "comparison", comp.Name, "batch", nextBatchIndex, "run", baseRunName, "batchKey", batchKey)
			} else {
				baseClone := &v1alpha1.Run{
					ObjectMeta: metav1.ObjectMeta{
						Name:      baseRunName,
						Namespace: comp.Namespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         v1alpha1.GroupVersion.String(),
								Kind:               "Comparison",
								Name:               comp.Name,
								UID:                comp.UID,
								Controller:         ptr.To(true),
								BlockOwnerDeletion: ptr.To(true),
							},
						},
						Labels: map[string]string{
							LabelBatchKey:   batchKey,
							LabelComparison: comp.Name,
							LabelBatch:      strconv.Itoa(nextBatchIndex),
							LabelRole:       "baseline",
						},
					},
					Spec: v1alpha1.RunSpec{
						Repo:        baselineRun.Spec.Repo,
						Model:       baselineRun.Spec.Model,
						Tasks:       continueTasks,
						SampleRange: &v1alpha1.SampleRange{Start: nextStart, End: nextEnd},
					},
				}
				if err := r.Create(ctx, baseClone); err != nil && !apierrors.IsAlreadyExists(err) {
					return ctrl.Result{}, fmt.Errorf("failed to create baseline batch run: %w", err)
				}
			}

			candClone := &v1alpha1.Run{
				ObjectMeta: metav1.ObjectMeta{
					Name:      candCloneName,
					Namespace: comp.Namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         v1alpha1.GroupVersion.String(),
							Kind:               "Comparison",
							Name:               comp.Name,
							UID:                comp.UID,
							Controller:         ptr.To(true),
							BlockOwnerDeletion: ptr.To(true),
						},
					},
					Labels: map[string]string{
						LabelComparison: comp.Name,
						LabelBatch:      strconv.Itoa(nextBatchIndex),
						LabelRole:       "candidate",
					},
				},
				Spec: v1alpha1.RunSpec{
					Repo:        candidateRun.Spec.Repo,
					Model:       candidateRun.Spec.Model,
					Tasks:       continueTasks,
					SampleRange: &v1alpha1.SampleRange{Start: nextStart, End: nextEnd},
				},
			}
			if err := r.Create(ctx, candClone); err != nil && !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, fmt.Errorf("failed to create candidate batch run: %w", err)
			}

			batches = append(batches, v1alpha1.ComparisonBatchStatus{
				Index:        nextBatchIndex,
				SampleRange:  batchRange,
				CandidateRun: candCloneName,
				BaselineRun:  baseRunName,
				Phase:        v1alpha1.ComparisonBatchPhaseRunning,
			})

			var partialStatuses []v1alpha1.ComparisonTaskStatus
			for _, te := range taskEvals {
				partialStatuses = append(partialStatuses, v1alpha1.ComparisonTaskStatus{
					ID:            te.taskID,
					BaseRate:      &te.est.BaseMean,
					CandidateRate: &te.est.CandidateMean,
					Delta:         &te.est.MeanDelta,
					DeltaCI: &v1alpha1.DeltaConfidenceInterval{
						Lower: te.est.CI.Lower,
						Upper: te.est.CI.Upper,
					},
					Pairs:  te.est.NumPairs,
					Wins:   te.est.Wins,
					Losses: te.est.Losses,
					Ties:   te.est.Ties,
					Dropped: v1alpha1.ComparisonDropped{
						Candidate: te.est.DroppedCandidate,
						Baseline:  te.est.DroppedBaseline,
					},
					Label:         toComparisonTaskLabel(te.dec.Label),
					SamplesRun:    te.samplesRun,
					Decision:      string(te.dec.Label),
					SuggestedNext: te.suggestedNext,
					Samples:       te.samplesRun,
					Tokens:        te.tokens,
				})
			}

			return r.updateComparisonStatus(ctx, &comp, &baselineRun, &candidateRun, func(status *v1alpha1.ComparisonStatus) {
				status.Phase = v1alpha1.ComparisonPhaseRunning
				status.Verdict = v1alpha1.ComparisonVerdictRunning
				status.Batches = batches
				status.Tasks = partialStatuses
				status.Summary = &v1alpha1.ComparisonSummary{
					Tasks: fmt.Sprintf("%d/%d", len(taskIDs)-len(continueTasks), len(taskIDs)),
				}
				status.Message = fmt.Sprintf("Running batch %d (%d tasks continuing)", nextBatchIndex, len(continueTasks))
			})
		}
	}

	// Check extraSamples if all tasks have stopped
	extraRequested := comp.Spec.ExtraSamples > comp.Status.ExtraSamplesApplied
	if extraRequested {
		var extraTasks []string
		for _, te := range taskEvals {
			if !te.exhausted {
				extraTasks = append(extraTasks, te.taskID)
			}
		}

		if len(extraTasks) > 0 {
			nextBatchIndex := len(batches) + 1
			lastBatch := batches[len(batches)-1]
			nextStart := lastBatch.SampleRange.End + 1
			batchSize := comp.Spec.Sampling.EffectiveBatchSize()
			extraRemaining := comp.Spec.ExtraSamples - comp.Status.ExtraSamplesApplied
			if extraRemaining < batchSize {
				batchSize = extraRemaining
			}
			nextEnd := nextStart + batchSize - 1
			newExtraApplied := comp.Spec.ExtraSamples

			candCloneName := fmt.Sprintf("%s-b%d-cand", comp.Name, nextBatchIndex)
			baseRunName := fmt.Sprintf("%s-b%d-base", comp.Name, nextBatchIndex)

			batchRange := v1alpha1.SampleRange{Start: nextStart, End: nextEnd}
			batchKey := ComputeBatchKey(baselineRun.Spec.Repo.URL, baselineRun.Spec.Repo.Commit, baselineRun.Spec.Model, extraTasks, batchRange)

			existingBase, err := r.findCompletedBaselineRun(ctx, comp.Namespace, baselineRun.Spec.Repo.URL, baselineRun.Spec.Repo.Commit, baselineRun.Spec.Model, extraTasks, batchRange)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("failed looking up baseline run for extra batch reuse: %w", err)
			}
			if existingBase != nil {
				baseRunName = existingBase.Name
				logger.Info("reusing completed baseline run for extra batch", "comparison", comp.Name, "batch", nextBatchIndex, "run", baseRunName, "batchKey", batchKey)
			} else {
				baseClone := &v1alpha1.Run{
					ObjectMeta: metav1.ObjectMeta{
						Name:      baseRunName,
						Namespace: comp.Namespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         v1alpha1.GroupVersion.String(),
								Kind:               "Comparison",
								Name:               comp.Name,
								UID:                comp.UID,
								Controller:         ptr.To(true),
								BlockOwnerDeletion: ptr.To(true),
							},
						},
						Labels: map[string]string{
							LabelBatchKey:   batchKey,
							LabelComparison: comp.Name,
							LabelBatch:      strconv.Itoa(nextBatchIndex),
							LabelRole:       "baseline",
						},
					},
					Spec: v1alpha1.RunSpec{
						Repo:        baselineRun.Spec.Repo,
						Model:       baselineRun.Spec.Model,
						Tasks:       extraTasks,
						SampleRange: &v1alpha1.SampleRange{Start: nextStart, End: nextEnd},
					},
				}
				if err := r.Create(ctx, baseClone); err != nil && !apierrors.IsAlreadyExists(err) {
					return ctrl.Result{}, fmt.Errorf("failed to create baseline extra batch run: %w", err)
				}
			}

			candClone := &v1alpha1.Run{
				ObjectMeta: metav1.ObjectMeta{
					Name:      candCloneName,
					Namespace: comp.Namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         v1alpha1.GroupVersion.String(),
							Kind:               "Comparison",
							Name:               comp.Name,
							UID:                comp.UID,
							Controller:         ptr.To(true),
							BlockOwnerDeletion: ptr.To(true),
						},
					},
					Labels: map[string]string{
						LabelComparison: comp.Name,
						LabelBatch:      strconv.Itoa(nextBatchIndex),
						LabelRole:       "candidate",
					},
				},
				Spec: v1alpha1.RunSpec{
					Repo:        candidateRun.Spec.Repo,
					Model:       candidateRun.Spec.Model,
					Tasks:       extraTasks,
					SampleRange: &v1alpha1.SampleRange{Start: nextStart, End: nextEnd},
				},
			}
			if err := r.Create(ctx, candClone); err != nil && !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, fmt.Errorf("failed to create candidate extra batch run: %w", err)
			}

			batches = append(batches, v1alpha1.ComparisonBatchStatus{
				Index:        nextBatchIndex,
				SampleRange:  batchRange,
				CandidateRun: candCloneName,
				BaselineRun:  baseRunName,
				Phase:        v1alpha1.ComparisonBatchPhaseRunning,
			})

			var partialStatuses []v1alpha1.ComparisonTaskStatus
			for _, te := range taskEvals {
				partialStatuses = append(partialStatuses, v1alpha1.ComparisonTaskStatus{
					ID:            te.taskID,
					BaseRate:      &te.est.BaseMean,
					CandidateRate: &te.est.CandidateMean,
					Delta:         &te.est.MeanDelta,
					DeltaCI: &v1alpha1.DeltaConfidenceInterval{
						Lower: te.est.CI.Lower,
						Upper: te.est.CI.Upper,
					},
					Pairs:  te.est.NumPairs,
					Wins:   te.est.Wins,
					Losses: te.est.Losses,
					Ties:   te.est.Ties,
					Dropped: v1alpha1.ComparisonDropped{
						Candidate: te.est.DroppedCandidate,
						Baseline:  te.est.DroppedBaseline,
					},
					Label:         toComparisonTaskLabel(te.dec.Label),
					SamplesRun:    te.samplesRun,
					Decision:      string(te.dec.Label),
					SuggestedNext: te.suggestedNext,
					Samples:       te.samplesRun,
					Tokens:        te.tokens,
				})
			}

			return r.updateComparisonStatus(ctx, &comp, &baselineRun, &candidateRun, func(status *v1alpha1.ComparisonStatus) {
				status.Phase = v1alpha1.ComparisonPhaseRunning
				status.Verdict = v1alpha1.ComparisonVerdictRunning
				status.Batches = batches
				status.ExtraSamplesApplied = newExtraApplied
				status.Tasks = partialStatuses
				status.Summary = &v1alpha1.ComparisonSummary{
					Tasks: fmt.Sprintf("%d/%d", len(taskIDs), len(taskIDs)),
				}
				status.Message = fmt.Sprintf("Running extra batch %d (%d samples requested)", nextBatchIndex, batchSize)
			})
		}
	}

	// All tasks are decided and no extra samples needed; finalize comparison
	var taskStatuses []v1alpha1.ComparisonTaskStatus
	var reports []stats.TaskReport
	var taskArtifacts []ComparisonTaskArtifact

	for _, te := range taskEvals {
		taskStatus := v1alpha1.ComparisonTaskStatus{
			ID:            te.taskID,
			BaseRate:      &te.est.BaseMean,
			CandidateRate: &te.est.CandidateMean,
			Delta:         &te.est.MeanDelta,
			DeltaCI: &v1alpha1.DeltaConfidenceInterval{
				Lower: te.est.CI.Lower,
				Upper: te.est.CI.Upper,
			},
			Pairs:  te.est.NumPairs,
			Wins:   te.est.Wins,
			Losses: te.est.Losses,
			Ties:   te.est.Ties,
			Dropped: v1alpha1.ComparisonDropped{
				Candidate: te.est.DroppedCandidate,
				Baseline:  te.est.DroppedBaseline,
			},
			Label:         toComparisonTaskLabel(te.dec.Label),
			SamplesRun:    te.samplesRun,
			Decision:      string(te.dec.Label),
			SuggestedNext: te.suggestedNext,
			Samples:       te.samplesRun,
			Tokens:        te.tokens,
		}
		taskStatuses = append(taskStatuses, taskStatus)

		reports = append(reports, stats.TaskReport{
			ID:           te.taskID,
			Label:        te.dec.Label,
			Estimate:     te.est,
			PImprovement: te.est.PImprovement,
		})

		// Build paired table for artifact
		candMap := make(map[string]float64)
		for _, s := range te.candScores {
			candMap[s.ID] = s.Score
		}
		baseMap := make(map[string]float64)
		for _, s := range te.baseScores {
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
			ID:            te.taskID,
			BaseRate:      te.est.BaseMean,
			CandidateRate: te.est.CandidateMean,
			Delta:         te.est.MeanDelta,
			DeltaCI: v1alpha1.DeltaConfidenceInterval{
				Lower: te.est.CI.Lower,
				Upper: te.est.CI.Upper,
			},
			Pairs:  te.est.NumPairs,
			Wins:   te.est.Wins,
			Losses: te.est.Losses,
			Ties:   te.est.Ties,
			Dropped: v1alpha1.ComparisonDropped{
				Candidate: te.est.DroppedCandidate,
				Baseline:  te.est.DroppedBaseline,
			},
			Label:           string(te.dec.Label),
			PImprovement:    te.est.PImprovement,
			SamplesRun:      te.samplesRun,
			Samples:         te.samplesRun,
			Decision:        string(te.dec.Label),
			SuggestedNext:   te.suggestedNext,
			Tokens:          te.tokens,
			CandidateTokens: te.candTokens,
			BaseTokens:      te.baseTokens,
			PairsTable:      pairsTable,
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
		Batches:      batches,
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
		status.Batches = batches
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
