# eval-runner: functional description

**One line:** "GitHub Actions for evals." A Kubernetes-native CI system that discovers the evals in a
repository, runs them in isolated sandboxes on every pull request, uses statistics to decide which
evals and how many samples to run, and posts an `IMPROVED / REGRESSED / UNCHANGED / INCONCLUSIVE`
verdict with confidence intervals on the PR.

This document is derived from `docs/concept/project.yaml`, the eight episode scripts under
`docs/concept/videos/*/script.yaml`, and the UI mockups under `docs/concept/ui/`. It is the target
functionality for the real implementation. Where the concept material is silent, the gap is marked
**[open]** so the implementer can make a call rather than guess that the concept decided it.

> **Status:** This document describes the target product functionality and concept. For the architecture and implementation as it exists on `main` today, see [docs/architecture.md](../architecture.md). Decisions made during implementation are recorded in [docs/decisions.md](../decisions.md).

Running example used throughout the concept: repo `acme/support-agent`, PR `#418`, three evals
`triage/runbook_lookup`, `triage/escalation_policy`, `triage/summary_quality`.

---

## 1. Thesis and core model

- **An agent is a distribution, not a function.** The same input yields different outputs on each
  run, so the unit of measurement is a *success rate with a confidence interval*, never pass/fail.
- **Evals are the unit tests of agentic systems.** A PR that changes a prompt, skill, tool, model
  or fine-tune must be judged by evals, and the verdict must land on the PR next to the merge button.
- **Everything ships as a PR, and the evals decide.** Human-authored skill changes, agent-authored
  skill fixes, fine-tune activations and new evals all arrive as pull requests and are judged by the
  same gate.
- **Prompts/skills and fine-tunes are one continuum.** Both are ways to move an eval score. The
  product measures every candidate the same way: eval score against cost per call.

### Vocabulary

| Term | Meaning |
|---|---|
| **Eval / task** | A repeatable test of agent behaviour: a dataset of inputs, an environment to act in, and a scorer. Identified by an id like `triage/runbook_lookup`. |
| **Sample** | One execution of one task input against one configuration of the agent. Produces a score and a trajectory. |
| **Trajectory** | The full log of a sample: prompts, tool calls, responses, errors, tokens, cost, latency. Stored in Inspect log format. |
| **Run** | All the work eval-runner does for one commit under test (PR head vs base). *(In implementation, a `Run` represents commit × sample window; batches are new immutable Runs owned by a `Comparison` custom resource, ADR-025, ADR-026).* |
| **Base / baseline** | The merge target commit (e.g. `main @ a81d0c4`) the PR is compared against. |
| **Paired sample** | A PR sample and a base sample on the *same input*, so input difficulty cancels out of the delta. |
| **Verdict** | Run-level outcome: `IMPROVED`, `REGRESSED`, `UNCHANGED`, `INCONCLUSIVE` (plus transient `RUNNING`). |
| **Cohort** | A named group of evals tracked as its own series (e.g. `customer-reported / 2026-Q4`). |
| **Candidate / configuration** | A named agent configuration under measurement: base prompt, +skill, fine-tune, bigger model, etc. |

---

## 2. Eval discovery (`.evals/list`)

**Principle: discovery is code, not config.** eval-runner never parses a repository's eval
configuration. It executes an entrypoint inside a sandbox at the commit under test and reads back a
manifest. This makes discovery reproducible, isolated and format-agnostic.

### 2.1 The entrypoint

- Path: `.evals/list`, an executable at the repository root.
- Executed inside a sandbox checked out at the exact commit under test (both PR head and base). *(Decided in implementation: `commit` must be an exact 7–64 character hex SHA, validated by pattern `^[0-9a-fA-F]{7,64}$` to guarantee immutability.)*
- Prints a JSON manifest to stdout.

### 2.2 Manifest format (as shown in the concept)

```json
{
  "images": [
    { "name": "support-agent-env:v12", "context": "hack/env" }
  ],
  "tasks": [
    {
      "id": "triage/runbook_lookup",
      "workdir": "evals",
      "spec": "runbook_lookup.py@runbook_lookup",
      "resources": [ { "type": "docker" } ]
    },
    {
      "id": "triage/escalation_policy",
      "workdir": "evals",
      "spec": "escalation.py@escalation_policy",
      "resources": [ { "type": "kind" } ],
      "images": [ "support-agent-env:v12" ]
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `images[].name`, `images[].context` | Container images eval-runner must build (from a build context path in the repo) before running tasks that reference them. |
| `tasks[].id` | Stable identifier, used everywhere in the UI, PR comment, storage paths and cohorts. |
| `tasks[].workdir` | Directory to run the task from. |
| `tasks[].spec` | Framework-specific task reference. For Inspect: `file.py@task_fn`. |
| `tasks[].resources[]` | Declarative resource needs (see §3.2). Empty list means zero privilege. |
| `tasks[].images[]` | Which built images the task needs available (e.g. pre-loaded into a kind cluster). |

**[open]** Fields not shown in the original concept mockup: manifest `version`, `cohort` / `tags` (§8), and per-task `dataset-size` (for planned "N of M possible samples" progress display up front) remain explicitly open. Sample pairing does not require sample IDs or dataset sizes to be declared in the manifest because pairing is performed dynamically by Inspect sample id (`sample.id`) from Inspect evaluation logs (#131, ADR-018), and sample windows are specified via `spec.sampleRange` (ADR-025). Explicit dataset size remains open for calculating total dataset size up front before execution.

### 2.3 Fallback when `.evals/list` is absent

```
$ test -x .evals/list || uv run inspect list tasks
evals/runbook_lookup.py@runbook_lookup
evals/escalation.py@escalation_policy
evals/summary.py@summary_quality
```

If there is no executable `.evals/list`, eval-runner falls back to Inspect's own task discovery and
synthesises an equivalent manifest (one task per listed spec, `id` derived from the spec,
no extra resources). Any other framework is supported by writing a `.evals/list` that prints its tasks.

### 2.4 Inspect compatibility

Native support for the Inspect format from the UK AI Security Institute (`inspect_ai`): tasks,
solvers, scorers and sandboxes. A task like the following must run unmodified:

```python
from inspect_ai import Task, task
from inspect_ai.dataset import json_dataset
from inspect_ai.solver import generate, use_tools
from inspect_ai.scorer import model_graded_fact

@task
def runbook_lookup():
    return Task(
        dataset=json_dataset("incidents.jsonl"),
        solver=[use_tools(runbook_search, page_oncall), generate()],
        scorer=model_graded_fact(),
        sandbox="docker",
    )
```

---

## 3. Execution: sandboxes and resources

**Principle: push the PR; the infrastructure is eval-runner's problem.** The analogy is hosted CI
runners: users stopped running their own CI fleet, and they should not run their own eval fleet.

### 3.1 Kubernetes-native execution

- eval-runner runs in a Kubernetes cluster across two namespaces: `eval-runner` (controller, webhook, UI, and `Run`/`Comparison` custom resources) and `eval-runner-sandbox` (untrusted sandboxes and pods) (ADR-004). *(The concept mockup showed `eval-ci`.)*
- **One sandbox per task running `inspect eval --limit a-b`**, not one pod per sample. Each task runs inside its own sandbox bounded by a sample range window (`spec.sampleRange` mapping to `inspect eval --limit start-end`), and the sandbox is reclaimed when the task completes (ADR-025).
- Isolation today is PSA-restricted containers plus mTLS to `sandboxd` (ADR-004, ADR-013, ADR-014, ADR-020): non-root UID 1000, drop ALL capabilities, RuntimeDefault seccomp, no ServiceAccount token, and ingress NetworkPolicy; gVisor and micro-VM isolation remain target future options.
- Images listed in the manifest: declarative resources and image building before tasks run are not started yet (§3.2).
- The controller watches `Run` and `Comparison` resources, schedules task sandboxes, collects results into the artifact sink, and drives GitHub reporting (ADR-018, ADR-021). Users "watch runs and comparisons, not pods".

### 3.2 Declarative resources

A task declares *what* it needs, not *how* to build it. eval-runner provisions it, injects a
standard environment contract into the sandbox, and tears it down afterwards.

| `resources[].type` | What eval-runner provisions | Contract injected into the sandbox |
|---|---|---|
| `kubernetes-namespace` | A fresh namespace, scoped credentials | `KUBECONFIG=/run/eval/kubeconfig` (scoped to e.g. `eval-ns-7f3a`) |
| `kind` | A throwaway kind cluster with the task's `images` pre-loaded | `KUBECONFIG=/run/eval/kubeconfig` |
| `docker` | A Docker daemon for the sample | `DOCKER_HOST=unix:///run/eval/docker.sock` |
| *(cloud project, shown as an env var)* | A leased cloud project from a pool, cleaned on release | `GOOGLE_CLOUD_PROJECT=eval-pool-0412` |
| `[]` (empty) | Nothing: zero privilege. No tokens, no daemon. | nothing |

*(Decided in implementation: empty `resources: []` is implemented as zero-privilege sandboxes: `automountServiceAccountToken: false`, non-root user UID 1000, drop ALL capabilities, RuntimeDefault seccomp, and Pod Security Admission restricted.)*

**[open]** The exact resource type name for a leased cloud project is not given (only the env var
is shown). Resource types should be an extensible set; the four above are the launch set; declared resources and image builds are not started yet.

### 3.3 Zero-trust sandboxes

- Sandboxes are **untrusted**. A prompt-injected agent running inside one must not be able to
  rewrite history.
- Sandboxes **never hold credentials** to the results store or to GitHub.
- The **controller pulls artifacts out** of the sandbox (scores, trajectories, logs) rather than the
  sandbox pushing them anywhere.
- The only credentials in a sandbox are the narrowly scoped ones for resources it declared
  (e.g. a kubeconfig for its own throwaway namespace).

### 3.4 Run lifecycle and states

Per task, as shown in the Runs view: `queued` → `provisioning namespace` / `provisioning kind` →
`running` → `done`. Per sample pod: `queued`, `running`, `done`. A completed run reports
"N of N samples complete · all sandboxes reclaimed".

*(Decided in implementation: In the Kubernetes API, `Run.status.phase` progresses through `Pending` → `Discovering` → `Discovered` (or `Running` when a model is configured) → `Completed` or `Failed`. Because a Run evaluates a specific sample window and is immutable once terminal (ADR-025), multi-batch adaptive sampling and verdicts are tracked by the `Comparison` resource (`Running` → `Completed`, ADR-018, ADR-026). Sandboxes run per task rather than per sample pod).*

---

## 4. Statistics: adaptive, paired sampling

**Principle: bound the confidence interval, not the budget.** The naive plan (every eval × 1000
runs × every PR) is ruinous: the concept quotes `$1,400 · 3,200 samples · 6 h 40 m` against
eval-runner's `$4.10 · 96 samples · 14 min · same verdict`. Treat the problem as estimation.

### 4.1 Paired baseline comparison

- Every PR sample is paired with a baseline sample **on the same input** (paired by Inspect sample id, ADR-018, #131).
- The estimand per task is the **delta in success rate** (PR minus base), with a 95% confidence
  interval computed on the paired differences (bootstrap percentile CI by default, normal approximation as fallback; P(improvement) via Rubin's Bayesian Bootstrap, ADR-018, ADR-019, [docs/statistics.md](../statistics.md)), so per-input difficulty variance cancels.
- Baseline samples are cached and reused across PRs sharing the same base commit
  ("paired against base commit a81d0c4"). Cache invalidation policy when the base moves or batches extend is decided in ADR-023 and ADR-026: baseline Runs and batches are identified by a deterministic batch key covering repo URL, commit SHA, model, task set, and sample window (`sampleRange`). When the merge base moves or configuration changes, a fresh commit SHA or batch key is evaluated, automatically invalidating and replacing the baseline without mutating historical Runs.

### 4.2 Stopping rule

Sampling for a task continues in batches until one of:

1. The 95% CI on Δ **excludes zero** (with minimum $n \ge 8$ pairs) → `improved` (CI entirely above 0) or `regressed` (entirely below).
2. The CI is **entirely within the equivalence margin** (default $\pm 0.05$) → `unchanged`.
3. The per-run budget or sample cap is reached before either → `inconclusive`.

The budget mockup shows the CI narrowing as paired samples accumulate
(n=4: [−0.42, +0.78] … n=24: [0.00, +0.43] … n=96: [+0.15, +0.31] → "CI clear of zero: IMPROVED, stop").

*(Decided in implementation: `pkg/stats.Decide` implements this stopping rule, requiring `MinPairsToDecide = 8` before CI stopping rules can fire; see ADR-018, ADR-026, and [docs/statistics.md](../statistics.md) §3).*

### 4.3 Task selection (ROI-driven)

eval-runner chooses *which* evals to run and in what order, prioritising:

- **Changed-file affinity**: evals that touch what the PR changed (e.g. a change under
  `docs/**` prioritises documentation evals). Declared via per-task repository-relative `paths`
  globs in `.evals/list` (`ManifestTask.paths`). When evaluating pull requests, the controller fetches
  changed files (`pulls/{n}/files`, capped at 300) and assigns per-task affinity (`always`, `matched`, `unmatched`).
  Under `affinityMode=order` (default), matched and always tasks are ordered first in subsequent batches;
  under `affinityMode=prune`, unmatched tasks are dropped and reported as skipped (see ADR-027).
- **Remaining uncertainty**: evals whose current CI on Δ is still wide get more samples;
  evals already conclusive get none. (Implemented via adaptive batch sampling in ADR-026, where decided tasks stop sampling and are excluded from subsequent batches).

Sample counts therefore differ per task within a run (e.g. 48 / 32 / 16).

### 4.4 Verdict semantics

Per-task label: `improved`, `regressed`, `unchanged`, `inconclusive` (plus `running`, `continue`).

Run-level verdict, posted on the PR:

| Verdict | Meaning |
|---|---|
| `IMPROVED` | At least one task improved, all other tasks unchanged. |
| `REGRESSED` | At least one task regressed (and none running). |
| `UNCHANGED` | All tasks unchanged (statistically equivalent within margin). |
| `INCONCLUSIVE` | No regressions found but at least one task is still inconclusive (or mixed `improved` + `inconclusive`); more samples recommended. |
| `RUNNING` | Sampling in progress. |

Every verdict is accompanied by **P(improvement)**, a single posterior probability that the PR is a
net improvement (e.g. `P(improvement) = 0.98`).

*(Decided in implementation: ADR-018, ADR-019, ADR-026, issue #141, and [docs/statistics.md](../statistics.md) §4):*
- **Verdict Aggregation Rule**: `RUNNING` while any task is running/queued/continue; `REGRESSED` if any task regressed; `INCONCLUSIVE` if any task is inconclusive (or mixed `improved` + `inconclusive`); `IMPROVED` if at least one task improved and all other tasks unchanged; `UNCHANGED` if all tasks unchanged. Headline $P(\text{improvement}) = \min(\max_k P(\text{imp}_k), \min_k P(\text{not\_reg}_k))$ with label floors ($0.999$ for `improved`, $0.975$ for `unchanged`).
- **Tie Handling (ADR-019)**: Per-task $P(\text{improvement}) = P(\Delta > 0) + 0.5 \cdot P(\Delta = 0)$ (ties split evenly). Computed via Rubin's Bayesian Bootstrap regularised with a uniform prior ($+1, -1$ pseudo-observations). When all tasks are `unchanged`, headline $P = 0.50$. When a comparison contains only `unchanged` and `inconclusive` tasks, headline $P$ is the arithmetic mean across tasks capped by $\min_k P(\text{not\_reg}_k)$, avoiding bias while preventing risk dilution.
- **Candidate-Failure Cap (Issue #141)**: If a task fails on the candidate side while the baseline succeeded (or was not planned), the candidate task failure caps any `UNCHANGED` or `IMPROVED` verdict to `INCONCLUSIVE` with $P(\text{improvement})$ capped at $0.50$ and the failure explanation recorded in `status.message`.

### 4.5 Buying more samples

An `INCONCLUSIVE` result offers an action that purchases exactly the samples still needed, with a
cost estimate: `▶ run 32 more samples (~$1.40)`.

*(Decided in implementation: In the concept mockup, triggering the action "extends the same run". In the implementation, "more samples extends the same run" is implemented as batches that are new immutable Runs owned by a Comparison (ADR-025, ADR-026). A Run is commit × sample window (`sampleRange`); terminal Runs remain strictly immutable. When more samples are needed—either automatically via adaptive batch sampling (`spec.sampling`) or requested via `spec.extraSamples`—the `Comparison` controller schedules new immutable Run pairs evaluating subsequent sample windows (`[highestPosition + 1, nextEnd]`) for undecided tasks, concatenates sample scores, and evaluates the stopping rule. PR comments display `- comment `/eval more <n>` to run <n> more samples` for inconclusive tasks with remaining dataset samples using `suggestedNext` from `stats.Decide`. Reviewers can comment `/eval more <n>` directly on the PR, which increments `spec.extraSamples` on the Comparison and triggers further batches; see ADR-026 amendment, #151).*

### 4.6 Cost and latency accounting

Each sample records tokens, inference cost and latency. Runs report total samples and spend
(`96 of 3,200 possible samples run · $4.10`), and comparisons report **cost per sample** and
**p50 latency** deltas (`cost per sample −61% · p50 latency −38%`). These feed the continuum view (§9).

*(Decided in implementation: Model token usage (`input`, `output`, `total`) is parsed from Inspect evaluation logs via `ExtractInspectTokens`, accumulated per task across evaluated runs, and recorded in `Run.status.tasks[].tokens`, `Comparison.status.tasks[].tokens`, and written to `summary.json`; see ADR-026).*

---

## 5. GitHub integration (the PR surface)

This is the product moment: the verdict lands on the PR, next to the merge button.

### 5.1 Trigger

A run starts on every pull request (open / push). The PR head is compared with the merge target.

*(Decided in implementation: The `eval-runner-webhook` service handles GitHub `pull_request` (opened, synchronize, reopened), `push`, and `issue_comment` (`/eval more <n>`) events, creates candidate/baseline Runs and Comparisons configured for adaptive paired sampling (`defaultBatchSize`, `defaultMaxSamplesPerTask`) with baseline cache reuse, and increments `spec.extraSamples` upon reviewer comment command; see ADR-023, ADR-026 amendment).*

### 5.2 Check run

A GitHub check named `eval-runner / evals` on `headSHA` whose description carries the state.

*(Decided in implementation: ADR-021, ADR-024):*
- State and conclusion mapping:
  - `RUNNING`: status `in_progress`, conclusion `""`, description `running · N of M samples` (where M is planned samples across runs)
  - `IMPROVED`: status `completed`, conclusion `success`, description `IMPROVED · P(improvement) = 0.98`
  - `UNCHANGED`: status `completed`, conclusion `success`, description `UNCHANGED`
  - `INCONCLUSIVE`: status `completed`, conclusion `neutral`, description `INCONCLUSIVE · more samples recommended` (neutral avoids blocking PR merges by default)
  - `REGRESSED`: status `completed`, conclusion `failure`, description `REGRESSED`
- Check run descriptions differ slightly from the mockup as listed above. Authentication supports GitHub App (preferred) and PAT fallback stored in the controller namespace.

### 5.3 PR comment (mockup: `ui/pr_comment.html`)

A single bot comment, updated in place as the run progresses, containing:

1. **Verdict chip** + headline: `3f9c2e1 vs main · P(improvement) = 0.98`
   (or `· sampling…`, `· more samples recommended`).
2. **Per-eval table**: `eval | base | this PR | Δ (95% CI) | label | runs`, with a horizontal
   CI bar per row drawn against a zero line, coloured by label.
3. **Footer**: samples run vs possible and spend; `paired against base commit <sha>`;
   link `trajectories ↗`; optional extra lines such as cost/latency deltas, the
   "run N more samples" action, or provenance (`authored by trajectory-miner · reviewed by you`).

*(Decided in implementation: Single bot comment updated in place using a hidden HTML marker `<!-- eval-runner:comparison:<ns>/<name> -->` and rendered via `pkg/github.RenderComparisonMarkdown`; see ADR-021).*

### 5.4 PRs eval-runner opens itself

eval-runner (or agents it hosts) author PRs in the same repo, which are then judged like any other:

- `miner/...` branches from the trajectory miner (§7), e.g.
  `skills(triage): consult the runbook before paging on-call`, with an evidence line
  `Opened by trajectory-miner · evidence: 14 of 21 failing samples in triage/runbook_lookup`.
- `eval-runner/activate-<finetune>` branches (§8), e.g. `agent: activate fine-tune triage-v7`,
  a one-line change to `agent.yaml` switching `model: gemini-2.5-pro` to
  `model: ft/triage-v7`, with the note `merging switches production traffic to triage-v7`.
- New evals submitted from the wild (§9), tagged with a cohort.

---

## 6. Trajectory storage and viewer

**Principle: open format, your storage.** A score says the agent failed; the trajectory says how.

- Every sample's full trajectory (every prompt, tool call, error, token) is stored in the
  **Inspect log format**, as JSON, in the **user's own bucket**. Nothing proprietary; anything that
  can read JSON can read it.
- Artifacts are written by the controller to the configured `ArtifactSink` (GCS, local directory in Kind, or in-memory), never by untrusted sandboxes (§3.3, ADR-018, ADR-022).
- **Implemented artifact layout**:
  - `runs/<commit>/manifest.json`: raw discovered task manifest from `.evals/list` (ADR-022)
  - `runs/<commit>/<task>/<inspect-log>.json`: full Inspect evaluation log with sample trajectories
  - `runs/<commit>/<task>/summary.json`: task aggregates, sample range, and token usage (ADR-025, ADR-026)
  - `comparisons/<name>.json`: paired sample comparisons, deltas, confidence intervals, and aggregate verdict (ADR-018)

### Trajectory viewer (mockup: `ui/trajectory.html`)

*(Status: Not started. Mockup exists under `docs/concept/ui/trajectory.html`.)*

- Left: sample list for `<task> · base <sha> · N samples`, each row with pass/fail dot, sample id,
  turn count; selectable.
- Right: header `sample-019 · FAIL · 13 turns · 41,200 tokens`, storage path, and an event stream
  with typed rows: `user`, `tool_call` (highlighted), `assistant` (highlighted red when it is the
  failing turn), and `miner` notes (teal) attached by the trajectory miner, e.g.
  "Pattern seen in 14 / 21 failing samples: never calls runbook_search before page_oncall."

---

## 7. Trajectory mining (agents improving agents)

Because trajectories are open, a coding agent can read them.

1. Point a miner agent at the **failing trajectories** of a task.
2. It **clusters** them and identifies the shared failure pattern.
3. It **annotates** the trajectories with its finding (the `miner` notes above).
4. It **writes the fix as a skill** (e.g. edits `skills/triage/SKILL.md` to add "check the runbook
   index before escalating") and **opens a PR** with the evidence count in the description.
5. The evals judge the PR like any other; the result is "agents improving agents, with a number
   attached" (`P(improvement) = 0.99`).

**[open]** Trigger (on demand from the UI, on a schedule, or after every regressed run) and which
coding agent runs the miner remain explicitly open. (Trajectory mining is not started yet; the concept treats the miner as a pluggable agent that only needs read
access to the bucket and permission to open PRs).

---

## 8. Fine-tunes (closing the loop with OpenRL)

Every scored trajectory is a labelled training example.

1. eval-runner hands scored trajectories to **OpenRL** and trains a fine-tune
   (e.g. overnight: `triage-v7`, 2,140 trajectories, base model `gemini-2.5-pro`, $38 compute).
2. Before anyone sees it, the fine-tune is **measured on the same paired samples as the last PR**
   so the comparison is apples to apples.
3. The user receives a **notification** (mockup: `ui/notification.html`):
   "New fine-tune available: triage-v7", with a table
   `eval | current (prompt + skills) | triage-v7 | Δ (95% CI) | cost / sample`, and actions
   **Open PR to activate triage-v7** and **Compare trajectories**.
4. Activation is a **one-line PR** changing the `model:` line in the agent config. The evals run
   again on the real configuration, and the PR comment reports both score deltas and
   `cost per sample −61% · p50 latency −38%`.
5. "Training is just another way to move the number."

**[open]** Scheduling policy (nightly, on N new trajectories, manual), which trajectories are
selected (all scored, only base-branch, filtered by score), and the fine-tune registry naming
(`ft/<name>`) remain explicitly open. (Fine-tunes training loop and continuum measurement are not started yet). The **Fine-tunes** nav entry in the web app lists trained candidates and their
measurements.

---

## 9. Evals from the wild and cohorts

### 9.1 "Don't file a bug. Send the eval." (mockup: `ui/bugreport.html`)

When a user hits a real failure, an agent turns their trajectory into an eval instead of a bug report:

- **Left pane**: what happened (the user's real trajectory) with PII highlighted for redaction
  (person, company, order id).
- **Right pane**: what is sent, an anonymised eval:
  - a task id (`checkout_502_eu_region`),
  - the input with placeholders (`{{person}}`, `{{company}}`, `{{time}}`, `{{order_id}}`),
  - the **environment captured** (e.g. "runbook index containing RB-204"),
  - a **scorer** for what should have happened ("resolves via runbook, does not page on-call"),
  - a **cohort** tag (`customer-reported / 2026-Q4`).
- Actions: **Submit eval to <org>/<repo>** (opens a PR adding the eval), **Preview redactions**.
- Guarantee: "no raw customer data leaves your environment", i.e. anonymisation happens
  client-side before submission.

### 9.2 Cohorts (mockup: `ui/cohort.html`)

Real-world evals make the suite harder, so the combined average drops, and a dropping average looks
exactly like a regression. eval-runner tracks **cohorts** so suite expansion and real regression are
separate numbers:

- Each eval belongs to a cohort (default: the original suite). **[open]** Declared in the manifest
  or by path convention remains explicitly open. (Cohorts tracking is not started yet).
- The **Cohorts** view charts, for the main branch, **mean score per cohort over time** (weekly,
  95% CI shaded): original suite, each added cohort, and all evals combined, with an annotation at
  the point a cohort was added ("11 customer-reported evals added").
- Reading: "original suite 0.86 · no regression", "new cohort 0.68 · improving", "combined 0.81".
- This implies eval-runner also runs evals on the **main branch after merges** (not only PRs) to
  produce the time series.

---

## 10. The continuum view (mockup: `ui/continuum.html`)

Prompts and skills are a cheap, fast form of fine-tuning; weights are a deeper, slower, cheaper-to-run
one. They are one lever, so every candidate is plotted on the same axes:

- **x: cost per sample (inference)**, **y: eval score (mean, 95% CI)**, for the suite.
- Candidates shown: base prompt, + skill, + skill + 2 more skills, fine-tune `triage-v7`,
  fine-tune v7 + skill, bigger base model. A dashed **frontier** connects the non-dominated points.
- Every point is measured by the same evals on the same inputs, so the user can "pick the point on
  the frontier you can afford".

Each candidate is a named configuration with a measurement record (score per task with CI, cost per
sample, latency), produced by the same run machinery.

---

## 11. Web application surfaces

Mockup `ui/runs.html` shows the app shell. Left nav: **Pull requests · Runs · Trajectories ·
Cohorts · Fine-tunes**.

*(Decided in implementation: A lightweight dashboard web UI (`cmd/eval-runner-ui`, deployed as `eval-runner-ui`) is implemented and serves HTML views for `Runs` and `Comparisons`. The trajectory viewer, budget/sampling detail chart, cohorts, fine-tunes, and PR surface in the web UI remain target future surfaces; mockups exist under `docs/concept/ui/`).*

| Surface | Mockup | Content |
|---|---|---|
| Pull requests | (via `pr_comment.html` / `pr_diff.html`) | List of PRs with their current verdict; links to the run. |
| Runs | `runs.html` | Heading `<repo> #<pr> · <sha>`; meta `namespace eval-ci · isolation: gvisor · 8 of 96 samples complete`; per task: id, resource chips (or "no credentials"), a pod grid (queued / running / done), status text; a cluster utilisation honeycomb; footer note on the security model. |
| Budget / sampling detail | `budget.html` | "How many samples until we know?": naive vs adaptive cost tiles; chart of Δ with 95% CI vs paired samples run, equivalence band, stop annotation. |
| Trajectories | `trajectory.html` | Sample list + event viewer with miner annotations (§6). |
| Cohorts | `cohort.html` | Per-cohort score over time (§9.2). |
| Fine-tunes | `notification.html`, `continuum.html` | Available fine-tunes with measured comparison and activate action; continuum scatter (§8, §10). |
| Bug report → eval | `bugreport.html` | Anonymised eval submission dialog (§9.1). Likely surfaced in the agent's own product, calling eval-runner. |

The remaining mockups (`title.html`, `caption.html`, `endcard.html`, `code.html`, `base.css`) are
presentation chrome for the video series and carry no product functionality beyond the brand tokens
(indigo `#4F46E5`, teal `#14B8A6`, ink `#111827`, muted `#6B7280`) and the status palette
(`good / bad / warn / neutral / pending`).

---

## 12. Components (architecture and status)

The system is composed of the following components:

1. **Controller (`cmd/eval-runner`)**: Reconciles `Run` (discovery in zero-privilege sandboxes and task sample execution) and `Comparison` (sample pairing by Inspect sample id, adaptive batch extension, stopping rule evaluation, and GitHub PR reporting via in-place comments and check runs). *(Implemented; image `eval-runner`).*
2. **Webhook Service (`cmd/eval-runner-webhook`)**: Ingress receiver for GitHub `pull_request` and `push` webhooks; validates HMAC signatures, enforces allow-lists, and creates candidate/baseline Runs and Comparisons with baseline cache reuse. *(Implemented; image `eval-runner-webhook`).*
3. **Web UI (`cmd/eval-runner-ui`)**: Web dashboard serving HTML views of `Runs` and `Comparisons`. *(Implemented; image `eval-runner-ui`).*
4. **CLI Tool (`cmd/evalstats`)**: Terminal inspection of cluster Runs and Comparisons matching PR-comment table layouts. *(Implemented).*
5. **Sandboxes & Runtime (`eval-runner-sandbox`)**: Isolated pods running `sandboxd` with mutual TLS (mTLS) and PSA-restricted profile, executing discovery (`.evals/list`) and task sample batches (`inspect eval --limit a-b`). *(Implemented).*
6. **Artifact Store (`ArtifactSink`)**: Cloud (GCS via Workload Identity) or local/in-memory sink holding `runs/<commit>/manifest.json`, `runs/<commit>/<task>/<inspect-log>.json`, `runs/<commit>/<task>/summary.json`, and `comparisons/<name>.json`. *(Implemented).*
7. **Trajectory Miner**: Pluggable coding agent that clusters failures and authors skill-fix PRs. *(Target future; not started).*
8. **Fine-Tune Pipeline**: OpenRL training loop, candidate registry (`ft/<name>`), continuum measurement, activation PRs. *(Target future; not started).*
9. **Eval-from-the-Wild Submitter**: Client-side anonymisation and PR submission of production failures. *(Target future; not started).*

---

## 13. Suggested build order

The episodes are ordered from core to periphery and make a reasonable milestone sequence:

1. **Discovery + execution** (§2, §3): `.evals/list`, Inspect fallback, sandbox pods, declarative
   resources, artifact pull-out, Runs view.
2. **Statistics + PR surface** (§4, §5): paired sampling, stopping rule, verdicts, P(improvement),
   check run and PR comment, "run more samples".
3. **Trajectories** (§6): bucket layout, viewer.
4. **Cohorts + main-branch time series** (§9.2).
5. **Miner** (§7), **fine-tunes** (§8), **evals from the wild** (§9.1), **continuum** (§10).

---

## 14. Non-goals (as implied by the concept)

- eval-runner does not define its own eval format. It runs what the repository declares and is
  natively compatible with Inspect.
- It does not store trajectories in a proprietary store. The user's bucket, open format.
- It does not auto-merge. Every change, including its own, is a PR a human decides on
  ("Merge, or don't. Your call, with evidence.").
