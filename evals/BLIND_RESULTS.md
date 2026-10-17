# Blind Evaluation Results & Docs Comprehension Analysis

This document records the evaluation results of the `docs/readme_comprehension_blind` control task and documents the per-question outcomes, leakage patterns, and dataset revisions conducted to align blind performance with chance expectation.

## Motivation & Background

The repository's documentation comprehension evaluation (`docs/readme_comprehension`) tests whether an LLM can understand `eval-runner` architecture, setup, and workflows based solely on the repository documentation (`README.md` and `docs/development.md`).

To ensure the evaluation measures true reading comprehension rather than model pretraining artifacts, memorization, or educated guessing from standard conventions, a paired control task is run: `docs/readme_comprehension_blind`. The blind task feeds the identical 20 multiple-choice questions to the model without providing the documentation.

Because each question contains 4 options (A, B, C, D), pure chance expectation is **25.0%** (5 out of 20 questions).

---

## Baseline Dataset (PR #15 Initial Results)

- **Model**: `google/gemini-3.5-flash-lite`
- **With-Docs Accuracy**: 1.000 (20/20, 100.0%)
- **Blind Accuracy**: 0.450 (9/20, 45.0%)
- **Target Distribution**: A: 4 (20%), B: 7 (35%), C: 5 (25%), D: 4 (20%)

### Per-Question Breakdown (Baseline Dataset)

| Question # | Topic | Target | Blind Prediction | Outcome | Leakage / Guessing Factor |
|---|---|---|---|---|---|
| **Q1** | Primary tagline | B | A | Incorrect | Stems and choices balanced; punished ungrounded guessing. |
| **Q2** | Unit of measurement | A | B | Incorrect | Punished assumption of statistical power over success rate. |
| **Q3** | Entrypoint script (`.evals/list`) | C | B | Incorrect | Model guessed `.evals/discover` instead of `.evals/list`. |
| **Q4** | Run-level verdicts | B | B | **Correct** | Standard status terminology. |
| **Q5** | Supported eval framework | B | B | **Correct (Leak)** | Pretraining knowledge: UK AISI created Inspect (`inspect_ai`). |
| **Q6** | Open source license | C | C | **Correct (Leak)** | General convention: Google open-source projects almost universally use Apache 2.0. |
| **Q7** | Project automation tool (`ap`) | C | D | Incorrect | Model guessed `mage` instead of `ap`. |
| **Q8** | `ap generate` subcommand | B | B | **Correct (Leak)** | Stem wording leak: stem asked which command "regenerates ci-presubmits.yaml", giving away `generate`. |
| **Q9** | `ap lint` command | A | A | **Correct (Leak)** | Stem wording leak: stem asked which command "runs Go linters", giving away `lint`. |
| **Q10** | E2E enabled env var (`RUN_E2E`) | B | D | Incorrect | Model guessed `ENABLE_E2E` instead of `RUN_E2E`. |
| **Q11** | E2E cleanup env var (`SKIP_CLEANUP`) | C | C | **Correct (Leak)** | Standard Kubernetes test harness idiom. |
| **Q12** | Default cluster name | C | B | Incorrect | Model guessed `eval-runner-e2e` instead of `kind`. |
| **Q13** | Prerequisite Go version | A | B | Incorrect | Model guessed Go 1.24 instead of Go 1.27. |
| **Q14** | Kubeconfig context switch | A | B | Incorrect | Model guessed process-level KUBECONFIG override. |
| **Q15** | Inspect tasks folder | B | B | **Correct (Leak)** | Repository naming convention: `eval-runner` storing evals in `evals/`. |
| **Q16** | Run TTL (negative control) | D | D | **Correct** | Option D was the only "Not stated" option among 3 numeric choices. |
| **Q17** | Artifact storage backend (negative control) | D | A | Incorrect | Model confidently guessed GCS. |
| **Q18** | Sandbox CPU requests (negative control) | D | B | Incorrect | Model confidently guessed 1000m. |
| **Q19** | Slack webhook alerts (negative control) | B | B | **Correct** | Option B was the only "Not configured" option. |
| **Q20** | Completion token limit (negative control) | D | A | Incorrect | Model confidently guessed 4096 tokens. |

**Summary**: 11 incorrect vs 9 correct (45.0% accuracy).

---

## First Tuning Pass (PR #34 Results)

To close pretraining leaks and bring blind accuracy closer to chance:

1. **Replaced Google License Question (Q6)**:
   - *Problem*: Any LLM knows Google repos use Apache 2.0.
   - *Fix*: Replaced with a question testing the README early development status caveat (which capabilities are currently implemented in the controller vs design-stage concept).

2. **Replaced Inspect Organization Question (Q5)**:
   - *Problem*: UK AISI authorship of Inspect is general knowledge.
   - *Fix*: Replaced with a question testing the top-level README heading under which architectural principles and tenets are documented (`Core Concepts`).

3. **Eliminated Stem Leaks (Q8, Q9)**:
   - *Problem*: "regenerates" leaked `ap generate`; "runs Go linters" leaked `ap lint`.
   - *Fix*: Removed "regenerates" from Q8. Rewrote Q9 to ask about executing specific tools (`govet` and `gofmt`).

4. **Replaced CRD Manifest Tool (Q10)**:
   - *Problem*: `controller-gen` is the standard Kubernetes tooling.
   - *Fix*: Asked which task script under `dev/tasks/` generates CRDs (`./dev/tasks/generate-crds`), with balanced alternative script names (`build-crds`, `codegen`, `compile-crds`).

5. **Balanced Option Lengths & Distractors (Q14, Q15)**:
   - *Problem*: Length bias in Q14; standard directory guessing in Q15.
   - *Fix*: Equalized length across all 4 options in Q14. Replaced Q15 with setup command options.

6. **Target Balancing & Negative Control Dispersal**:
   - Rebalanced target letters so all four choices (`A`, `B`, `C`, `D`) appear exactly **5 times each** (25.0% uniform distribution).
   - Distributed negative control correct answers across multiple letter positions (A, C, D) rather than defaulting to D.

7. **Evidence Drift Guard**:
   - Added an `evidence` field to every sample containing an exact, non-empty substring from the injected documentation.
   - Integrated unit checks into `dev/tasks/test-evals` verifying that every question's basis is intact.

**First Pass Summary**: Blind accuracy dropped from 45.0% (9/20) to 35.0% (7/20), with with-docs remaining 20/20 (100.0%).

---

## Second Tuning Pass (PR #35 Results)

The review of PR #34 identified three remaining soft leaks and a gap in negative control drift protection:

1. **`absent` Guard for Negative Controls**:
   - *Problem*: The `evidence` guard ensures referenced sections exist, but negative controls ("not stated") break when a previously omitted fact is added to the documentation (as occurred in #15 when namespaces were documented).
   - *Fix*: Added an optional `absent` list of substrings per sample that must NOT appear anywhere in the injected documentation. Verified by `evals/test_drift.py` and run via `dev/tasks/test-evals`. Populated for all 5 negative controls (Q16–Q20).

2. **Rewritten Soft Leaks (Q11, Q12, Q15)**:
   - **Q11 (`SKIP_CLEANUP`)**: The variable name in the stem gave away the semantic meaning ("skips cleanup"). Rewritten to ask for the default kind cluster name when `KIND_CLUSTER_NAME` is unset (`kind`), where models guess `eval-runner-e2e`.
   - **Q12 (Sandbox container name)**: The stem previously asked about the controller's namespace (which overstated what the docs said and `eval-runner` was guessable). Rewritten to ask for the container name when viewing discovery sandbox logs (`agent`), where models guess `discovery` or `sandbox`.
   - **Q15 (Tool installation directory)**: `uv sync` is universally known in Python uv projects. Rewritten to ask into which directory `dev/tasks/test-e2e` installs pinned tools (`.build/bin/`), where models guess `.tools/bin/` or `bin/`.

3. **Public API / Isolated Model Resolution**:
   - Documented and isolated the `inspect_ai.model._model.active_model` lookup in `_resolve_model()` with explicit rationale: Inspect AI requires inspecting the active model at task instantiation to respect `--model` CLI overrides (e.g. for offline mock CI).

### Current Retuned Dataset Results

- **Model**: `google/gemini-3.5-flash-lite`
- **Target Distribution**: A: 5 (25.0%), B: 5 (25.0%), C: 5 (25.0%), D: 5 (25.0%)
- **With-Docs Accuracy**: **1.000** (20/20, 100.0%)
- **Blind Accuracy**: **0.250** (5/20, 25.0%) — **exactly matches pure chance expectation (5/20)**.

### Per-Question Breakdown (Current Dataset)

| Question # | Topic | Target | Blind Prediction | Outcome | Notes |
|---|---|---|---|---|---|
| **Q1** | Primary tagline | B | A | Incorrect | Correctly punished guessing "CI for agents". |
| **Q2** | Unit of measurement | A | B | Incorrect | Punished assumption of statistical power over success rate. |
| **Q3** | `.evals/list` output format | D | D | **Correct** | Chance guess (JSON manifest). |
| **Q4** | Run-level verdicts | C | A | Incorrect | Punished guessing "IMPROVEMENT, REGRESSION...". |
| **Q5** | Top-level README heading | B | C | Incorrect | Guessed "Design Tenets" instead of "Core Concepts". |
| **Q6** | Controller implementation status | D | D | **Correct** | Chance guess on early development caveat. |
| **Q7** | agent-sandbox pinned release | A | B | Incorrect | Guessed v0.9.4 instead of v1.0.7. |
| **Q8** | `ap generate` tasks | C | B | Incorrect | Guessed `ap sync` instead of `ap generate`. |
| **Q9** | `ap lint` tools | A | C | Incorrect | Guessed `ap check` instead of `ap lint`. |
| **Q10** | CRD generator script | B | C | Incorrect | Guessed `codegen` instead of `generate-crds`. |
| **Q11** | Default kind cluster name | C | B | Incorrect | Guessed `eval-runner-e2e` instead of `kind`. |
| **Q12** | Discovery sandbox container name | B | D | Incorrect | Guessed `discovery` instead of `agent`. |
| **Q13** | Prerequisite Go version | D | A | Incorrect | Guessed Go 1.24 instead of Go 1.27. |
| **Q14** | Kubeconfig context alteration | A | B | Incorrect | Length bias eliminated; guessed KUBECONFIG override. |
| **Q15** | Pinned tools install directory | B | D | Incorrect | Guessed `.tools/bin/` instead of `.build/bin/`. |
| **Q16** | Run TTL (negative control) | C | C | **Correct** | Negative control target at C correctly identified. |
| **Q17** | Storage backend (negative control) | D | A | Incorrect | Confidently guessed S3 bucket. |
| **Q18** | Sandbox CPU requests (negative control) | A | B | Incorrect | Confidently guessed 500m CPU. |
| **Q19** | Slack alerts (negative control) | D | D | **Correct** | Negative control target at D correctly identified. |
| **Q20** | Token limit (negative control) | C | C | **Correct** | Negative control target at C correctly identified. |

**Summary**: 15 incorrect vs 5 correct (25.0% accuracy, 5/20). With docs, accuracy is 20/20 (100.0%), demonstrating perfect discrimination between reading comprehension and ungrounded guessing.
