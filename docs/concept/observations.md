<!--
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Concept: Observations as the Unit of Measurement and Product Navigation

> **Context:** On 2026-10-21, Justin raised a fundamental question regarding the conceptual model of `eval-runner`:
>
> *"I am uneasy about 'runs' being a central unit of observation, because I think the whole point is that we don't do runs, we just run as few individual evals as we can to try to understand how we are doing."*
>
> This document proposes transitioning the product mental model, UI navigation, and scheduling semantics from monolithic **Runs** to granular **Observations**.
>
> - **Product concept & target functionality:** [docs/concept/functionality.md](functionality.md)
> - **Current architecture & implementation:** [docs/architecture.md](../architecture.md)
> - **Decision record:** [docs/decisions.md](../decisions.md) (ADR-032)

---

## 1. The Observation Model

### 1.1 Motivation

Traditional continuous integration evaluates deterministic suites in lockstep. In contrast, an agent is a distribution, not a function ([docs/concept/functionality.md §1](functionality.md)): performance is measured as a success rate with uncertainty intervals, evaluated via sequential sampling that halts as soon as a decision is statistically clear ([docs/concept/functionality.md §4](functionality.md)).

Today, `Run` serves dual roles as an execution batch primitive and the primary navigation entity. In reality, an evaluation suite (all evals declared in a repository, with named cohorts representing subsets) consists of independent evals with durable, reusable baseline observations across commits, models, and harnesses.

### 1.2 Defining an Observation

An **Observation** is an immutable measurement record for a 7-tuple:

`Observation = (repository, commit, eval, model, harness, sampleRange, time)`

- **Coordinates**: `repository` (clone URL), `commit` (exact hex SHA), `eval` (task ID), `model` (e.g. `google/gemini-2.5-flash-lite`), `harness` (framework/version, e.g. `inspect:0.3.281`), `sampleRange` (1-based window `1..16`), and `time` (RFC 3339 timestamp).
- **Outcomes**: Score (`successRate` or `meanScore`), uncertainty interval (`successRateCI` via Wilson or Student's t), and scored sample count ($n$).
- **Cost, tokens, latency**: Monetary spend (`costUSD`, `costSource`), tokens (`input`, `output`, `total`, `reasoning`, `cacheRead`), and latency percentiles (`p50`, `p95`).
- **Provenance and artifacts**: Artifact sink URI to raw Inspect log (`logPath`), derived summary path (`artifacts.TaskSummaryURI(logPath)`), and batch execution references.

### 1.3 Mapping Today's Constructs onto Observations

Entries in `Run.status.tasks[]` already function as observations, while `Run` is merely an ephemeral scheduling batch:

| Observation Dimension | `Run` / `TaskStatus` Source | Implementation Notes |
|---|---|---|
| `repository` | `Run.spec.repo.url` | Repository git clone URL. |
| `commit` | `Run.spec.repo.commit` | Exact hex commit SHA. |
| `eval` | `Run.status.tasks[].id` | Task identifier (e.g. `docs/readme_comprehension`). |
| `model` | `Run.spec.model` | Set at Run level, not per task. |
| `harness` | *(none today)* | Implicit runtime environment (e.g. Inspect version in sandbox image). |
| `sampleRange` | `Run.status.tasks[].sampleRange` | Nil when `spec.limit` or neither is set; nil represents window `1..spec.limit` or `1..tasks[].samples`. |
| `time` | `Run.status.tasks[].startedAt` | Timestamp when task transitioned to Running; no per-task completion timestamp exists. |
| `score` | `status.tasks[].successRate` / `meanScore` | Point estimate: success rate for binary scorers, mean score for continuous scorers. |
| `interval` | `status.tasks[].successRateCI` | Struct `{lower, upper, confidence, method}` (Wilson or Student-t). |
| `sampleCount` | `status.tasks[].samples` | Number of evaluated and scored samples ($n$). |
| `cost` | `status.tasks[].costUSD` + `costSource` | Estimated spend in USD and price provenance (`configmap` or `default`). |
| `tokens` | `status.tasks[].tokens` | Struct `{input, output, total, reasoning, cacheRead}`. |
| `latency` | `status.tasks[].latency` | Struct `{p50, p95}` in seconds. |
| `logPath` | `status.tasks[].logPath` | Inspect log URI in artifact sink. |
| `summaryPath` | *(derived)* | Derived dynamically via `artifacts.TaskSummaryURI(logPath)`, not stored on status. |
| `cohort` | `status.tasks[].cohort` | Manifest-declared cohort grouping (ADR-030). |

For Comparisons:
- **Decision**: Recorded per task on `ComparisonTaskStatus`: `{baseRate, candidateRate, delta, deltaCI, pImprovement, pairs, decision}` (with `label`, `wins`, `losses`, `ties`, `dropped`).
- **Two Sets of Observations**: The candidate and baseline Run pairs listed in `Comparison.status.batches[]` (`candidateRun`, `baselineRun`, `sampleRange`).
- **Sample Pairing**: Paired strictly by sample ID within a task (`pkg/stats/paired.go:38`).
- **Baseline Reuse**: Baseline matching uses `findCompletedBaselineRun(url, commit, model, tasks, sampleRange)` in `pkg/controller/comparison_controller.go:930`, which is already the observation key minus harness.

---

## 2. What the User Navigates

### 2.1 Navigation Hierarchy

On `main`, `/` is the repository index (`handleRepoIndex`), repository pages are scoped under `/{host}/{owner}/{repo}` with tabs for Overview, Runs, Comparisons, Pull Requests (`.../pulls`), and Series (`.../series`, ADR-030). Pull requests are routed at `/{host}/{owner}/{repo}/pulls/{number}` (and `/pulls/{repohash}/{number}`).

The observation model restructures navigation:
1. **Repository Overview** (`/{host}/{owner}/{repo}`): Health of the default branch and open pull requests on the forge (GitHub first).
2. **Suite & Cohorts View** (`/{host}/{owner}/{repo}/series`): Multi-task progression over commits grouped by cohort (e.g. `default`, `customer-reported`).
3. **Eval Page** (`/{host}/{owner}/{repo}/evals/{task}`): Today's Series/cohorts view (`/series/{repohash}/{branch}`, ADR-030) re-scoped to a single eval. Displays task metadata (spec, path affinity globs, dataset size) and score progression over commits with confidence intervals.
4. **Observations**: Ticks along the commit trendline displaying commit SHA, model, harness, sample range, score with interval, cost, and delta against baseline.
5. **Samples / Trajectory**: Inspect trajectory viewer ([docs/concept/functionality.md §6](functionality.md)) showing prompts, tool calls, and scores.

### 2.2 Where Runs and Comparisons Still Appear

- **Runs as Provenance**: Internal execution records. An observation indicates its origin (e.g. `Observed via batch run pr-418-b2-cand at 2026-10-21 14:32 UTC`).
- **Comparisons as Pull Request Decisions**: Comparisons aggregate decisions across observed evals for a change, surfaced on pull requests on the forge (GitHub first) and at `/{host}/{owner}/{repo}/pulls/{number}` or `/comparisons/{namespace}/{name}`.

### 2.3 Navigation Recommendation

**Recommendation:** Demote the repository-level "Runs" tab to a secondary **"Activity"** tab under repository navigation. Users focus on eval progression and pull request merge decisions rather than batch execution IDs.

### 2.4 Backwards Compatibility

Demoting Runs from primary navigation preserves existing integrations:

| Surface | Impact If Runs Disappear | Mitigation |
|---|---|---|
| **PR Comment Trajectory Link** | PR comment footer links to `[trajectories](<uiBaseURL>/runs/{ns}/{candRun}/tasks)` (`pkg/github/renderer.go:366`). | Retain `/runs/{ns}/{name}/tasks` permanently; render sample transcripts directly. |
| **PR Comment Web Link** | Header links `[web view](<uiBaseURL>/comparisons/{ns}/{name})` (`renderer.go:336-339`). | Retain `/comparisons/{ns}/{name}` permanently. |
| **Repo-Scoped Run Routes** | `/{host}/{owner}/{repo}/runs/{name}` and `.../runs/{name}/tasks/{rest...}`. | Maintain routes; display execution metadata with a banner linking to the canonical eval page (e.g. `` `triage/runbook_lookup` ``). |
| **Legacy Global Routes** | `/runs/{namespace}/{name}` and `/runs/{namespace}/{name}/tasks/{rest...}`. | Retain legacy global routes as deep links. |
| **CLI / Operator Tools** | `kubectl get runs` and `evalstats -from-run <name>` / `evalstats -from-comparison <name>`. | Unchanged; `Run` and `Comparison` CRDs remain the cluster execution primitives. |

---

## 3. Scheduling Implication ("Run as Few Evals as We Can")

### 3.1 Per-Eval Independent Scheduling

The paired batch loop already builds subsequent batches from undecided tasks only (`continueTasks` in `pkg/controller/comparison_controller.go:2448-2530`), and `stats.Decide(...)` already takes current spend and budget (`spec.budgetUSD` / `status.budget`).

The observation model introduces three key capabilities:
1. **Per-Eval Sample Windows**: Today, all continuing tasks share identical sample ranges (`SampleRange{Start, End}`). Under per-eval scheduling, task A can receive 8 samples while task B receives 16 based on variance and stopping distance.
2. **Main-Branch Targets Without PRs**: Evaluates commits directly against confidence targets (e.g. interval width $\le 0.05$) or sample caps without requiring a baseline Comparison.
3. **Model and Harness Coordinates**: First-class indexing by model and harness.

Integration with existing mechanisms:
- **Sequential Stopping Rules**: Evaluated per task via `stats.Decide`.
- **Baseline Reuse**: Loaded from artifact storage via `findCompletedBaselineRun` (extended with harness).
- **Cohorts & Affinity**: Manifest cohorts group evals; path affinity (ADR-027) zeroes needed samples for untouched evals in `prune` mode.

### 3.2 Scheduling API Options

| Option | Architecture | Trade-Offs |
|---|---|---|
| **Option A: Suite Intent CRD with Per-Eval Targets** | Extend `Comparison` (or an `Evaluation` CRD) with per-eval targets (`spec.evals[].targetCI`, `spec.evals[].maxSamples`). | Bounded etcd footprint (one CR per PR); retains array management in spec/status. |
| **Option B: CRD-per-Eval (`EvalRequest`)** | Declarative CR per eval per commit (`spec: { repo, commit, eval, model, targetCI }`). | Native per-eval lifecycle; severe etcd and apiserver watch explosion across hundreds of evals. |
| **Option C: Two-Tier Architecture (Intent + Execution Units)** | High-level `Evaluation` CR declares suite goals; controller reconciles observation cache and schedules atomic execution runs for missing sample deltas. | Protects etcd scalability while achieving independent per-eval scheduling; requires controller observation cache. |

**Recommendation:** Adopt **Option C**, evolving `Comparison` with per-eval stopping independence while keeping `Run` as the internal execution batch unit.

---

## 4. Breakdown Dimensions (Model and Harness as First-Class)

### 4.1 Motivation

An observation is conditional on both the model (prompt/weights) and the harness (Inspect framework version, runtime scaffold, solver settings). Today, `Run.spec.model` is an optional string, while `api/v1alpha1` and `pkg/discovery` have no field for harness; the only runtime fact is the Inspect version in the sandbox image.

### 4.2 Recommended Fields and Labels

- **CRD Fields**: `spec.model` (string) and `spec.harness` (struct `{framework, version, variant}`, compact shorthand `inspect:0.3.281`).
- **Runtime Auto-Detection**: Because manifests do not declare harnesses today, auto-detect the Inspect version from the sandbox container runtime as the zero-config default.
- **Kubernetes Labels**: `evalrunner.gke-labs.dev/model`, `evalrunner.gke-labs.dev/harness`, `evalrunner.gke-labs.dev/task`, `evalrunner.gke-labs.dev/commit`. Label values cap at 63 characters and disallow slashes, requiring sanitized slugs (e.g. `google/gemini-2.5-flash-lite` -> `gemini-2.5-flash-lite`).
- **Artifact Metadata**: Persist `repository, commit, eval, model, harness, sampleRange, score, scoreCI, tokens, costUSD, timestamp` in `summary.json` to power the Continuum view ([docs/concept/functionality.md §10](functionality.md)).

### 4.3 UI Implementation Status: Dimension Breakdown

In the eval suite detail page (`/{host}/{owner}/{repo}/evals/{suite}`), the trendline breakdown is implemented as a unified function `LinesPerDimension(obs, dim, commits)`. Because runs currently do not carry the `evalrunner.gke-labs.dev/harness` label, only the `spec.model` breakdown is currently live. When the harness label is populated on Runs in the future, the harness breakdown can be displayed using the exact same dimension pipeline.

---

## 5. Open Questions for Justin

### Question 1: Should `Run` remain a user-facing CRD or become an internal execution detail?
- **Options**: (A) User-facing CRD. (B) Internal execution batch CRD, with `Comparison`/`Evaluation` and observations elevated in UI/docs. (C) Deprecate `Run` entirely.
- **Recommendation:** **Option B**. Preserves controller stability and `kubectl` workflows while aligning the user experience with observations.

### Question 2: Granularity of the scheduling CRD: suite-level intent vs. CRD-per-eval?
- **Options**: (A) Suite-level intent with per-eval targets. (B) One CRD per eval per commit (`EvalRequest`). (C) Two-tier architecture (suite intent + execution units).
- **Recommendation:** **Option C** (two-tier architecture). Protects etcd from object explosion while allowing per-eval independent sampling.

### Question 3: When does listing Runs in the UI stop scaling, and how should observations be indexed?
- **Context**: Today the Series view lists Runs directly from the Kubernetes API (ADR-030 item 4). As runs accumulate, apiserver list latency and memory grow.
- **Options**: (A) Continue listing Runs from apiserver. (B) Queryable observation index in the artifact sink (e.g. SQLite cache in `eval-runner-ui` or index JSON in bucket). (C) Full bucket scans on page load.
- **Recommendation:** **Option B**. Continue using apiserver listing for current volumes, transitioning to an observation index in the sink once listing latency degrades.

### Question 4: How are harnesses declared and matched during baseline pairing?
- **Options**: (A) Implicit; no pairing check. (B) Declared in manifest. (C) Auto-detected from runtime Inspect version.
- **Recommendation:** **Option C** (auto-detection) with optional manifest declaration. Strictly enforce identical harness matching during baseline pairing.

### Question 5: What should be the repository-level landing view in the Web UI?
- **Context**: Today `/{host}/{owner}/{repo}` renders the Overview page with tabs for Overview, Runs, Comparisons, Pull Requests, and Series.
- **Options**: (A) Keep Overview as repository landing page. (B) Default to Pull Requests. (C) Default to Series / Evals.
- **Recommendation:** **Option A**. Keep Overview as the repository landing page, feature Pull Requests and Series prominently, and demote "Runs" to "Activity".
