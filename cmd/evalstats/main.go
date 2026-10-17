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

// Package main provides a lightweight CLI for computing eval statistics
// from JSONL streams of sample evaluations.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/stats"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

// EvalRecord represents one record in the input JSONL.
type EvalRecord struct {
	Task           string   `json:"task"`
	SampleID       string   `json:"sample_id"`
	BaseScore      *float64 `json:"base_score,omitempty"`
	PRScore        *float64 `json:"pr_score,omitempty"`
	CandidateScore *float64 `json:"candidate_score,omitempty"`
}

// TaskSummary holds computed statistics for output formatting.
type TaskSummary struct {
	Task     string               `json:"task"`
	BaseMean float64              `json:"base_mean"`
	PRMean   float64              `json:"pr_mean"`
	Delta    float64              `json:"delta"`
	CILow    float64              `json:"ci_lower"`
	CIHigh   float64              `json:"ci_upper"`
	Decision stats.Decision       `json:"decision"`
	Estimate stats.PairedEstimate `json:"estimate"`
}

// RunOutput encapsulates the full CLI output in JSON mode.
type RunOutput struct {
	Tasks   []TaskSummary    `json:"tasks"`
	Verdict stats.RunVerdict `json:"verdict"`
}

// SingleRunTaskSummary holds per-task stats for JSON output from a Run object.
type SingleRunTaskSummary struct {
	Task        string                       `json:"task"`
	Phase       string                       `json:"phase"`
	Samples     int                          `json:"samples"`
	Successes   int                          `json:"successes"`
	SuccessRate *float64                     `json:"successRate,omitempty"`
	CI          *v1alpha1.ConfidenceInterval `json:"ci,omitempty"`
}

// SingleRunOutput encapsulates the Run output in JSON mode.
type SingleRunOutput struct {
	Run     string                 `json:"run"`
	Phase   string                 `json:"phase"`
	Tasks   []SingleRunTaskSummary `json:"tasks"`
	Summary *v1alpha1.RunSummary   `json:"summary,omitempty"`
}

func main() {
	margin := flag.Float64("margin", 0.05, "Equivalence margin for stopping rule (e.g. 0.05 for ±0.05)")
	conf := flag.Float64("confidence", 0.95, "Confidence level (e.g. 0.95 for 95% CI)")
	sampleCap := flag.Int("sample-cap", 0, "Sample cap (0 for unlimited)")
	budget := flag.Float64("budget", 0.0, "Spend budget in dollars (0 for unlimited)")
	spent := flag.Float64("spent", 0.0, "Current spend in dollars")
	final := flag.Bool("final", false, "Treat input as complete run: sets sample cap to samples run so unresolved tasks become inconclusive rather than continue")
	jsonOut := flag.Bool("json", false, "Output results in JSON format")
	fromRun := flag.String("from-run", "", "Read Run by name from Kubernetes cluster and print per-task statistics")
	namespace := flag.String("namespace", "eval-runner", "Kubernetes namespace for -from-run (defaults to eval-runner)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: evalstats [flags] [file.jsonl]\n\n")
		fmt.Fprintf(os.Stderr, "Reads JSONL lines of {task, sample_id, base_score, pr_score} and outputs\n")
		fmt.Fprintf(os.Stderr, "per-task paired statistics and the run-level verdict.\n\n")
		fmt.Fprintf(os.Stderr, "Alternatively, use -from-run <name> to inspect a Run from the cluster.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if *fromRun != "" {
		cfg, err := config.GetConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting Kubernetes config: %v\n", err)
			os.Exit(1)
		}

		scheme := runtime.NewScheme()
		utilruntime.Must(clientgoscheme.AddToScheme(scheme))
		utilruntime.Must(v1alpha1.AddToScheme(scheme))

		cl, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating Kubernetes client: %v\n", err)
			os.Exit(1)
		}

		runNS := *namespace
		runName := *fromRun
		if strings.Contains(runName, "/") {
			parts := strings.SplitN(runName, "/", 2)
			runNS = parts[0]
			runName = parts[1]
		}

		var run v1alpha1.Run
		if err := cl.Get(context.Background(), types.NamespacedName{Namespace: runNS, Name: runName}, &run); err != nil {
			fmt.Fprintf(os.Stderr, "Error getting Run %s/%s: %v\n", runNS, runName, err)
			os.Exit(1)
		}

		if *jsonOut {
			if err := PrintRunJSON(os.Stdout, &run); err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding JSON output: %v\n", err)
				os.Exit(1)
			}
			return
		}

		if err := PrintRunTable(os.Stdout, &run); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
			os.Exit(1)
		}
		return
	}

	var reader io.Reader = os.Stdin
	if flag.NArg() > 0 && flag.Arg(0) != "-" {
		f, err := os.Open(flag.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening file %s: %v\n", flag.Arg(0), err)
			os.Exit(1)
		}
		defer func() {
			if closeErr := f.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: error closing %s: %v\n", flag.Arg(0), closeErr)
			}
		}()
		reader = f
	}

	tasksCand := make(map[string][]stats.SampleScore)
	tasksBase := make(map[string][]stats.SampleScore)

	scanner := bufio.NewScanner(reader)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		var rec EvalRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse JSON at line %d: %v\n", lineNum, err)
			continue
		}

		taskID := rec.Task
		if taskID == "" {
			taskID = "default"
		}

		prScore := rec.PRScore
		if prScore == nil {
			prScore = rec.CandidateScore
		}

		if prScore != nil {
			tasksCand[taskID] = append(tasksCand[taskID], stats.SampleScore{
				ID:    rec.SampleID,
				Score: *prScore,
			})
		}

		if rec.BaseScore != nil {
			tasksBase[taskID] = append(tasksBase[taskID], stats.SampleScore{
				ID:    rec.SampleID,
				Score: *rec.BaseScore,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
		os.Exit(1)
	}

	// Gather all unique task names
	taskNamesMap := make(map[string]struct{})
	for t := range tasksCand {
		taskNamesMap[t] = struct{}{}
	}
	for t := range tasksBase {
		taskNamesMap[t] = struct{}{}
	}

	var taskNames []string
	for t := range taskNamesMap {
		taskNames = append(taskNames, t)
	}
	sort.Strings(taskNames)

	var summaries []TaskSummary
	var reports []stats.TaskReport

	for _, name := range taskNames {
		cand := tasksCand[name]
		base := tasksBase[name]

		est := stats.ComparePaired(cand, base, *conf)
		capToUse := *sampleCap
		if *final && capToUse == 0 {
			capToUse = est.NumPairs
		}
		dec := stats.Decide(est, *margin, est.NumPairs, capToUse, *spent, *budget)

		summaries = append(summaries, TaskSummary{
			Task:     name,
			BaseMean: est.BaseMean,
			PRMean:   est.CandidateMean,
			Delta:    est.MeanDelta,
			CILow:    est.CI.Lower,
			CIHigh:   est.CI.Upper,
			Decision: dec,
			Estimate: est,
		})

		reports = append(reports, stats.TaskReport{
			ID:           name,
			Label:        dec.Label,
			Estimate:     est,
			PImprovement: est.PImprovement,
		})
	}

	verdict := stats.AggregateVerdict(reports)

	if *jsonOut {
		out := RunOutput{
			Tasks:   summaries,
			Verdict: verdict,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding JSON output: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Text table output
	if err := printTable(summaries); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("VERDICT: %s · P(improvement) = %.2f\n", verdict.Verdict, verdict.PImprovement)
	if verdict.Summary != "" {
		fmt.Printf("Summary: %s\n", verdict.Summary)
	}
}

func printTable(summaries []TaskSummary) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(w, "TASK\tBASE\tTHIS PR\tΔ (95% CI)\tLABEL\tN\tDROPPED"); err != nil {
		return err
	}
	for _, s := range summaries {
		deltaStr := fmt.Sprintf("%+.2f [%+.2f, %+.2f]", s.Delta, s.CILow, s.CIHigh)
		droppedStr := fmt.Sprintf("%d/%d", s.Estimate.DroppedCandidate, s.Estimate.DroppedBaseline)
		if _, err := fmt.Fprintf(w, "%s\t%.2f\t%.2f\t%s\t%s\t%d\t%s\n",
			s.Task,
			s.BaseMean,
			s.PRMean,
			deltaStr,
			s.Decision.Label,
			s.Estimate.NumPairs,
			droppedStr,
		); err != nil {
			return err
		}
	}
	return w.Flush()
}

// PrintRunTable writes a formatted table of tasks with success rates and confidence intervals from a Run.
func PrintRunTable(w io.Writer, run *v1alpha1.Run) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "TASK\tRATE (95% CI)\tN\tMETHOD"); err != nil {
		return err
	}

	for _, t := range run.Status.Tasks {
		var rateCI string
		method := "-"

		if t.SuccessRate != nil {
			if t.SuccessRateCI != nil {
				rateCI = fmt.Sprintf("%.2f [%.2f, %.2f]", *t.SuccessRate, t.SuccessRateCI.Lower, t.SuccessRateCI.Upper)
				method = t.SuccessRateCI.Method
			} else {
				rateCI = fmt.Sprintf("%.2f", *t.SuccessRate)
			}
		} else if t.Phase == v1alpha1.TaskPhaseCompleted && t.Samples > 0 {
			est := stats.Proportion(t.Successes, t.Samples, 0.95)
			rateCI = fmt.Sprintf("%.2f [%.2f, %.2f]", est.Point, est.CI.Lower, est.CI.Upper)
			method = "wilson"
		} else if t.Phase == v1alpha1.TaskPhaseFailed {
			rateCI = "failed"
		} else if t.Phase != "" {
			rateCI = strings.ToLower(string(t.Phase))
		} else {
			rateCI = "-"
		}

		if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n",
			t.ID,
			rateCI,
			t.Samples,
			method,
		); err != nil {
			return err
		}
	}

	if err := tw.Flush(); err != nil {
		return err
	}

	if run.Status.Summary != nil {
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "Tasks: %d completed, %d failed (%d total) · Total samples: %d\n",
			run.Status.Summary.TasksCompleted,
			run.Status.Summary.TasksFailed,
			run.Status.Summary.TasksTotal,
			run.Status.Summary.TotalSamples,
		); err != nil {
			return err
		}
		if run.Status.Summary.MeanSuccessRate != nil && run.Status.Summary.SuccessRateRange != nil {
			if _, err := fmt.Fprintf(w, "Mean success rate: %.2f (range %.2f-%.2f)\n",
				*run.Status.Summary.MeanSuccessRate,
				run.Status.Summary.SuccessRateRange.Min,
				run.Status.Summary.SuccessRateRange.Max,
			); err != nil {
				return err
			}
		}
	}

	return nil
}

// PrintRunJSON writes JSON output for a Run.
func PrintRunJSON(w io.Writer, run *v1alpha1.Run) error {
	var taskSummaries []SingleRunTaskSummary
	for _, t := range run.Status.Tasks {
		taskSummaries = append(taskSummaries, SingleRunTaskSummary{
			Task:        t.ID,
			Phase:       string(t.Phase),
			Samples:     t.Samples,
			Successes:   t.Successes,
			SuccessRate: t.SuccessRate,
			CI:          t.SuccessRateCI,
		})
	}
	out := SingleRunOutput{
		Run:     run.Name,
		Phase:   string(run.Status.Phase),
		Tasks:   taskSummaries,
		Summary: run.Status.Summary,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
