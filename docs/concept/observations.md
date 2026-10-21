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

### 1.1 Why "Runs" Misalign with Agent Evaluation

In traditional continuous integration, a "run" is the natural unit: a test suite is deterministic and binary (pass/fail), code is checked out at a commit, every test executes once in lockstep, and the run finishes with a boolean verdict.

Agent evaluation breaks every one of these assumptions:
1. **An agent is a distribution, not a function** ([docs/concept/functionality.md §1](functionality.md)). The same prompt and inputs yield varying trajectories, so the true measurement is a success rate with a confidence interval.
2. **We run as few samples as possible** ([docs/concept/functionality.md §4](functionality.md)). Under sequential sampling, an eval that clearly passes or fails stops early ($n=8$), while a close or noisy eval gathers more evidence ($n=64$).
3. **Evals are independent**. Different evals in the same repository measure distinct capabilities (e.g. `triage/runbook_lookup` vs. `triage/escalation_policy`). They do not need to be evaluated in lockstep sample windows or synchronized execution blocks.
4. **Baseline samples are durable and reusable** (ADR-018, ADR-023, ADR-026). When a pull request is evaluated, baseline samples on the merge target commit are not re-executed; they are loaded from historical records.

Today, the `Run` custom resource serves two conflicting purposes: it is the low-level **execution container** (spawning sandboxes and managing timeouts) and the primary **entity users reason about** (a top-level Runs tab, run detail pages, run phases). A Run is treated as a monolithic event, when in reality users and statistical engines care about the **measured results of individual evals over commits, models, and harnesses**.

### 1.2 Defining an Observation

An **Observation** is an immutable measurement record for a specific 7-tuple:

$$\text{Observation} = (\text{repository}, \text{commit}, \text{eval}, \text{model}, \text{harness}, \text{sampleRange}, \text{time})$$

Each observation records:
- **Identifier coordinates**:
  - `repository`: Git repository URL or identifier (e.g. `github.com/acme/support-agent`).
  - `commit`: Exact, immutable git commit hex SHA (e.g. `3f9c2e1b4...`).
  - `eval` (or `task`): Unique task identifier (e.g. `triage/runbook_lookup`).
  - `model`: Model identifier under evaluation (e.g. `google/gemini-2.5-flash-lite`, `anthropic/claude-3-5-sonnet`, `ft/triage-v7`).
  - `harness`: Evaluation harness, scaffold, and framework version (e.g. `inspect:0.3.281`, solver settings, system prompt version).
  - `sampleRange`: 1-based inclusive dataset window evaluated (e.g. `1..16`, `17..32`).
  - `time`: Timestamp (RFC 3339) when measurement occurred.
- **Measured outcomes**:
  - `score`: Point estimate (mean success rate $\hat{p} \in [0, 1]$ or continuous score).
  - `interval`: Uncertainty quantification (e.g. 95% Wilson score interval $[\text{lower}, \text{upper}]$ for binary tasks, or Student's t-interval for continuous scores).
  - `sampleCount`: Number of evaluated samples ($n$).
- **Cost & latency**:
  - `tokens`: Breakdown of token consumption (`input`, `output`, `total`).
  - `cost`: Monetary spend in USD based on provider pricing.
  - `latency`: Duration and percentiles (`p50`, `p95`).
- **Provenance & artifacts**:
  - `logPath`: Pointer to the raw Inspect evaluation log in the artifact sink (`runs/<commit>/<task>/<inspect-log>.json`).
  - `summaryPath`: Pointer to the structured `summary.json`.
  - `executionRef`: Provenance link to the batch execution job/pod that produced it.

### 1.3 Mapping Today's Constructs onto Observations

Under this model, existing `eval-runner` primitives naturally fall into place:

```
+---------------------------------------------------------------------------------------+
| Run pr-418-b1-cand (Execution Batch)                                                  |
| Commit: 3f9c2e1 | Model: gemini-2.5-flash | Window: 1..8                             |
|                                                                                       |
|   +-------------------------------------------------------------------------------+   |
|   | status.tasks[0]: triage/runbook_lookup                                        |   |
|   | -> Observation (3f9c2e1, triage/runbook_lookup, gemini-2.5-flash, 1..8)      |   |
|   +-------------------------------------------------------------------------------+   |
|   | status.tasks[1]: triage/escalation_policy                                     |   |
|   | -> Observation (3f9c2e1, triage/escalation_policy, gemini-2.5-flash, 1..8)    |   |
|   +-------------------------------------------------------------------------------+   |
+---------------------------------------------------------------------------------------+
```

1. **`Run.status.tasks[]` entries ARE observations**:
   A `Run` is simply a scheduled batch of observations that happened to execute together in the cluster. Because terminal Runs are strictly immutable (ADR-025), each completed task in `Run.status.tasks[]` is already an immutable observation record.
2. **`Comparison` is a decision over two sets of observations**:
   A `Comparison` does not execute code; it evaluates statistical decisions. It takes a set of candidate observations $\mathcal{O}_{\text{cand}}$ and a set of baseline observations $\mathcal{O}_{\text{base}}$ sharing identical `(eval, model, harness, sampleId)` inputs. It pairs sample trajectories, computes paired difference $\Delta$ with a 95% bootstrap confidence interval, applies the stopping rule (`improved`, `regressed`, `unchanged`, `inconclusive`), and aggregates the overall verdict with $P(\text{improvement})$ (ADR-018, ADR-019).

---

## 2. What the User Navigates

### 2.1 The Observation-Centric Navigation Hierarchy

Instead of confronting users with a flat table of scheduler jobs (`pr-418-3f9c2e1-b1`, `pr-418-3f9c2e1-b2`, `base-a81d0c4`), the web UI and CLI navigate by evaluation entities:

```
1. Repository / Overview (PRs & main branch status)
   │
   └── 2. Eval Suite / Cohorts (triage/*, customer-reported-q4, default)
          │
          └── 3. Eval / Task (e.g. triage/runbook_lookup)
                 │
                 └── 4. Observations (ticks along the commit/model trendline)
                        │
                        └── 5. Samples / Trajectory Viewer (prompts, tool calls, errors)
```

1. **Repository / Overview**:
   - Status of active pull requests (verdict, $P(\text{improvement})$, cost) and default branch health.
2. **Eval Suite / Cohorts**:
   - High-level capability groups. Cohorts ([docs/concept/functionality.md §9.2](functionality.md), ADR-030) allow tracking suite expansion over time without masking regressions.
3. **Eval (Task)**:
   - The central product page (e.g. `/evals/triage/runbook_lookup`).
   - Shows task metadata: definition file (`runbook_lookup.py@runbook_lookup`), declared path affinity globs (`paths: ["docs/**", "evals/**"]`), total dataset sample count, and required resources.
   - Shows the **progression trendline**: score over commits with 95% confidence intervals, broken down by model or harness.
4. **Observations (Ticks on the Trendline)**:
   - Each point on the trendline is an **observation**.
   - Hovering or clicking an observation displays: commit SHA, model, harness, sample range (`1..16`), score ($0.875$ [0.640, 0.965]), token usage, inference cost, and comparison delta against baseline.
5. **Samples / Trajectory Viewer**:
   - Clicking an observation drills into the Inspect trajectory viewer ([docs/concept/functionality.md §6](functionality.md)).
   - Shows the list of evaluated samples with pass/fail badges, turn counts, tool calls, prompt transcripts, and trajectory miner annotations.

### 2.2 Where Runs and Comparisons Still Appear

Runs and Comparisons remain essential, but their conceptual roles are clarified:

- **Runs as Provenance**:
  Runs are execution engine details. When viewing an observation, a provenance badge indicates how it was produced:
  `Observed via batch run pr-418-b2-cand (pod sb-task-runbook-lookup-7f8a) at 2026-10-21 14:32 UTC`.
- **Comparisons as Pull Request Decisions**:
  A pull request introduces a code change that affects multiple evals. The PR comment and GitHub check run link directly to a **Comparison / PR Summary** page (e.g. `/pulls/acme/support-agent/418`). The Comparison aggregates the decisions across all observed evals for that PR, answering: *"Should this PR be merged?"*

### 2.3 Recommendation on Navigation and the Runs Tab

**Recommendation:** Demote the top-level "Runs" tab to a secondary **"Activity"** or **"Executions"** view under system/admin navigation.

Engineers using modern CI do not navigate by raw container job IDs; they navigate by commit, pull request, or test suite. Similarly, AI engineers want to evaluate agent behavior over time and make merge decisions on PRs, not manage Kubernetes batch executions.

### 2.4 Backwards Compatibility: What Breaks and How to Mitigate

Demoting Runs from primary navigation must not break existing integrations:

| Surface | What Breaks If Runs Disappear | Mitigation |
|---|---|---|
| **PR Comment Links** | Existing PR comments contain links formatted as `[web view](<uiBaseURL>/comparisons/<ns>/<name>)` or legacy `/runs/{ns}/{name}`. | All `/comparisons/{ns}/{name}` and `/runs/{ns}/{name}` URLs remain permanently valid as deep links. The PR comment continues linking to the Comparison view. |
| **kubectl Workflows** | Cluster operators use `kubectl get runs`, `kubectl describe run <name>`, and `evalstats -from-run <name>`. | The `Run` CRD remains fully operational in the Kubernetes API. The observation model changes product information architecture, not the underlying Kubernetes resource schema. |
| **Legacy Web URLs** | Browser bookmarks, documentation links, and scripts targeting `/runs/{ns}/{name}` or `/runs/{ns}/{name}/tasks/{rest...}`. | Maintain legacy routes. The Run detail page displays an execution summary with an informational banner: *"Viewing execution batch `<name>`. View these observations on the [triage/runbook_lookup](/evals/triage/runbook_lookup) eval page."* |

---

## 3. Scheduling Implication ("Run as Few Evals as We Can")

### 3.1 Per-Eval Independent Scheduling

In the current batch-oriented implementation (ADR-025, ADR-026), the controller evaluates tasks in synchronized batches: Batch 1 schedules sample range `1..8` across tasks A, B, and C. If Task A reaches statistical significance ($95\%\ \text{CI}$ clear of zero) at $n=8$, while Task B remains inconclusive and requires $n=32$, Batch 2 must be spawned with an explicit task filter `spec.tasks: [B]`.

When observations become the central unit, "run as few evals as we can" operates per-eval:

$$\text{NeededSamples}(E, C, M) = f\left(\text{ExistingObservations}(E, C, M), \text{BaselineObservations}(E, C_{\text{base}}, M), \text{StoppingRule}\right)$$

For each eval $E$ on candidate commit $C$:
1. The controller inspects existing observations for $(E, C, M)$.
2. If paired with a baseline, it computes paired delta $\Delta$ and evaluates `stats.Decide`.
3. If the stopping rule fires (`improved`, `regressed`, or `unchanged` within equivalence margin $\pm 0.05$), $\text{NeededSamples} = 0$.
4. If undecided, it determines the next minimal sample window (e.g. samples `9..16`) for that eval alone, bounded by budget or `maxSamplesPerTask`.
5. Evals that converge quickly consume zero additional compute, while undecided evals gather samples independently.

```
Commit 3f9c2e1 (Candidate) vs. a81d0c4 (Base)
Eval 1: triage/runbook_lookup   ──[ 8 samples ]──> IMPROVED    (Stop: 0 samples needed)
Eval 2: triage/escalation_policy ──[ 8 samples ]──[ 8 samples ]──> UNCHANGED (Stop)
Eval 3: triage/summary_quality   ──[ 8 samples ]──[ 8 samples ]──[ 16 samples ]──> INCONCLUSIVE (Cap)
```

### 3.2 Integration with Existing Capabilities

- **Sequential Sampling & Stopping Rules** ([docs/concept/functionality.md §4.2](functionality.md), ADR-018):
  `stats.Decide` and Bayesian bootstrap stopping rules operate directly on pairs of sample observations.
- **Baseline Reuse** ([docs/concept/functionality.md §4.1](functionality.md), ADR-023, ADR-026):
  Observations for $(E, C_{\text{base}}, M, H, \text{window})$ are stored permanently in the artifact sink. Any future candidate observation matching that window pairs with the existing baseline observation without re-running compute.
- **Cohorts** ([docs/concept/functionality.md §9.2](functionality.md), ADR-030):
  A cohort is a grouping attribute on an eval (`tasks[].cohort`). The Cohorts view aggregates observations across evals sharing the cohort tag.
- **Changed-File Task Affinity** (ADR-027):
  Path globs determine whether an eval is sensitive to files changed in a PR. If an eval's affinity is `unmatched` and the cluster operates in `prune` mode, needed samples are immediately 0.

### 3.3 Scheduling API Options & Trade-Offs

How should a per-eval scheduling API be expressed in Kubernetes?

| Option | Architecture | Trade-Offs |
|---|---|---|
| **Option A: Coarse Goal CRD with Per-Eval Targets** | Keep `Comparison` (or a new `SuiteEvaluation` CRD) as the top-level intent, declaring per-eval policies:<br>`spec.evals[].id`<br>`spec.evals[].targetCI: 0.05`<br>`spec.evals[].maxSamples: 64`<br>Controller coordinates execution internally. | **Pros**: Bounded etcd footprint (one CR per PR); familiar controller pattern; controller can coalesce independent eval needs into shared sandbox pods where advantageous.<br>**Cons**: Array management in CRD spec/status; retains some batch coupling in status reporting. |
| **Option B: Fine-Grained CRD-per-Eval (`EvalRequest` / `ObservationRequest`)** | Create an individual CRD for every eval under test:<br>`apiVersion: evalrunner.gke-labs.dev/v1alpha1`<br>`kind: EvalRequest`<br>`spec: { repo, commit, eval, model, targetCI }` | **Pros**: Native Kubernetes lifecycle, events, and status per eval; completely decoupled execution.<br>**Cons**: **CRD explosion**. A repository with 200 evals evaluated on 10 PRs creates 2,000 CRDs, inflating etcd memory and apiserver watch streams; high overhead for short-lived tasks. |
| **Option C: Two-Tier Architecture (Declarative Intent + Atomic Execution Units)** | High-level declarative intent (`Comparison` or `PullRequestEvaluation`) specifies PR targets. The controller reconciles against the observation store and schedules lightweight, immutable `ObservationJob`s or task sandboxes strictly for the missing sample delta. | **Pros**: Best of both worlds: protects etcd scalability while achieving true per-eval independent execution; clear separation between product intent and execution mechanics.<br>**Cons**: Controller must maintain an in-memory observation cache or query the artifact sink efficiently. |

**Recommendation:** Adopt **Option C** as the target architecture, using an evolutionary path: enhance `Comparison` with per-eval stopping independence, while retaining `Run` as an internal execution mechanism until task-level sandboxing can be driven directly.

---

## 4. Breakdown Dimensions (Model and Harness as First-Class)

### 4.1 Why Model and Harness Must Be First-Class

An observation of an agent is not defined by repository code alone:
- **Model**: The foundation model (`google/gemini-2.5-flash-lite`, `anthropic/claude-3-5-sonnet`) or fine-tune (`ft/triage-v7`). A prompt change that improves Gemini may regress Claude.
- **Harness**: The evaluation framework (`inspect_ai:0.3.281`), agent runtime scaffold, system prompt wrapper, tool definitions, and temperature/seed settings. If the harness changes (e.g. Inspect bumps its tool-calling format or prompts are wrapped differently), observations across harnesses cannot be naively paired.

Today, `Run.spec.model` exists as an optional string, but `harness` has no dedicated field or schema representation.

### 4.2 Recommended Fields and Labels

To make model and harness first-class dimensions across the system:

1. **CRD Specifications (`RunSpec`, `ComparisonSpec`, future `EvaluationSpec`)**:
   ```yaml
   spec:
     model: "google/gemini-2.5-flash-lite"
     harness:
       framework: "inspect"
       version: "0.3.281"
       variant: "default"
   ```
   *Compact string shorthand*: `harness: "inspect:0.3.281"`.
2. **Kubernetes Labels** (for fast informer filtering and indexing):
   - `evalrunner.gke-labs.dev/model`: Sanitized model slug (e.g. `gemini-2.5-flash-lite`).
   - `evalrunner.gke-labs.dev/harness`: Sanitized harness slug (e.g. `inspect-0.3.281`).
   - `evalrunner.gke-labs.dev/task`: Sanitized task identifier.
   - `evalrunner.gke-labs.dev/commit`: Short commit hex SHA.
3. **Artifact Metadata (`summary.json` & Inspect Logs)**:
   Every artifact written to the sink must include the complete identification block:
   ```json
   {
     "repository": "github.com/acme/support-agent",
     "commit": "3f9c2e1b4a0f",
     "eval": "triage/runbook_lookup",
     "model": "google/gemini-2.5-flash-lite",
     "harness": "inspect:0.3.281",
     "sampleRange": { "start": 1, "end": 16 },
     "score": 0.875,
     "scoreCI": { "lower": 0.640, "upper": 0.965, "confidence": 0.95, "method": "wilson" },
     "tokens": { "input": 14200, "output": 2100, "total": 16300 },
     "costUSD": 0.0072,
     "timestamp": "2026-10-21T14:32:00Z"
   }
   ```
   This metadata enables the **Continuum View** ([docs/concept/functionality.md §10](functionality.md)) to plot eval score against cost-per-sample across different models and harnesses for identical commits.

---

## 5. Open Questions for Justin

The following five strategic questions require decision. Recommendations are provided for each:

### Question 1: Should `Run` remain a user-facing CRD or become an internal execution detail?
- **Options**:
  - A. Retain `Run` as a primary user-facing CRD in documentation, CLI, and API.
  - B. Keep `Run` as a lower-level orchestration CRD (used by the controller, operators, and debugging), but elevate `Comparison` / `Evaluation` and `Observation` in the documentation and UI.
  - C. Deprecate and eliminate the `Run` CRD entirely in favor of task-level execution pods.
- **Recommendation:** **Option B**. Retaining `Run` as an internal execution primitive avoids breaking existing cluster controllers and `kubectl` workflows, while shifting user-facing documentation and the web UI to reason about observations and pull request evaluations.

### Question 2: Granularity of the scheduling CRD: suite-level intent vs. CRD-per-eval?
- **Options**:
  - A. Suite-level declarative CRD with per-eval target policies (Option A / Option C in §3.3).
  - B. One Kubernetes Custom Resource per eval per commit (`EvalRequest`).
- **Recommendation:** **Option A**. Creating a Custom Resource per eval per commit threatens etcd stability and watch scalability in repositories with hundreds of evals. A suite-level intent resource with per-eval independent execution preserves Kubernetes control plane health while giving users per-eval stopping efficiency.

### Question 3: How should the observation index be stored and queried?
- **Options**:
  - A. Full scan of object storage (`runs/<commit>/<task>/summary.json`) on every UI query.
  - B. Maintain a lightweight, queryable observation index (e.g. SQLite database or append-only index file `observations/<repo>/index.json` in the artifact bucket).
  - C. Persist all observation summaries in Kubernetes etcd custom resources.
- **Recommendation:** **Option B**. Scanning thousands of GCS objects on page loads is too slow, and storing all historical observations in etcd violates ADR-007 and ADR-022 status discipline. A lightweight index in the artifact sink (cached in memory by `eval-runner-ui`) allows rendering multi-commit trendlines in milliseconds.

### Question 4: How are harnesses declared and matched during baseline pairing?
- **Options**:
  - A. Harness is implicit from repository code and environment; no pairing enforcement.
  - B. Harness is declared in the `.evals/list` manifest and required to match exactly when pairing samples.
  - C. Harness is auto-detected from the sandbox runtime (e.g. Inspect version) and recorded as metadata.
- **Recommendation:** **Option B + C**. Tasks should declare their default harness in `.evals/list` with runtime auto-detection fallback. Comparisons must strictly require matching harness versions when pairing samples, because changes in evaluation harnesses alter the underlying measurement instrument.

### Question 5: What should be the primary landing page in the Web UI?
- **Options**:
  - A. Keep `/runs` as the default home page.
  - B. Make `/pulls` (Pull Requests Overview) the home page for active development, and `/evals` (Eval Suites & Cohorts) the home page for repository progression.
- **Recommendation:** **Option B**. AI engineers want to know: "How are my PRs doing?" and "How is our agent progressing on its benchmark suites?" The top-level navigation should feature **Pull Requests**, **Eval Suites / Cohorts**, and **Continuum**, demoting "Runs" to an "Activity" view.
