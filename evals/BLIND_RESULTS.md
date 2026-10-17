# Blind Evaluation Results & Docs Comprehension Analysis

This document records the evaluation results of the `docs/readme_comprehension_blind` control task and documents the per-question outcomes, leakage patterns, and dataset revisions conducted to align blind performance with chance expectation.

## Motivation & Background

The repository's documentation comprehension evaluation (`docs/readme_comprehension`) tests whether an LLM can understand `eval-runner` architecture, setup, and workflows based solely on the repository documentation (`README.md` and `docs/development.md`).

To ensure the evaluation measures true reading comprehension rather than model pretraining artifacts, memorization, or educated guessing from standard conventions, a paired control task is run: `docs/readme_comprehension_blind`. The blind task feeds the identical 20 multiple-choice questions to the model without providing the documentation.

Because each question contains 4 options (A, B, C, D), pure chance expectation is **25.0%** (5 out of 20 questions).

---

## Baseline Dataset (PR #15 Initial Results)

- **Model**: `google/gemini-3.5-flash-lite`
- **With-Docs Accuracy**: 1.000 (20/20, 100%)
- **Blind Accuracy**: 0.450 (9/20, 45.0%)
- **Target Distribution**: A: 4 (20%), B: 7 (35%), C: 5 (25%), D: 4 (20%)

### Per-Question Breakdown (Baseline Dataset)

| Question # | Topic | Target | Blind Prediction | Outcome | Leakage / Guessing Factor |
|---|---|---|---|---|---|
| **Q1** | Primary tagline | B | A | Incorrect | Stems and choices balanced; punished ungrounded guessing. |
| **Q2** | Unit of measurement | A | B | Incorrect | Punished assumption of statistical power over success rate. |
| **Q3** | Entrypoint script (`.evals/list`) | C | B | Incorrect | Model guessed `.evals/discover` instead of `.evals/list`. |
| **Q4** | Run-level verdicts | B | A | Incorrect (later B in PR text) | Standard status terminology. |
| **Q5** | Supported eval framework | B | B | **Correct (Leak)** | Pretraining knowledge: UK AISI created Inspect (`inspect_ai`). |
| **Q6** | Open source license | C | C (PR run) / A | **Correct (Leak)** | General convention: Google open-source projects almost universally use Apache 2.0. |
| **Q7** | Project automation tool (`ap`) | C | D | Incorrect | Model guessed `mage` instead of `ap`. |
| **Q8** | `ap generate` subcommand | B | B | **Correct (Leak)** | Stem wording leak: stem asked which command "regenerates ci-presubmits.yaml", giving away `generate`. |
| **Q9** | `ap lint` command | A | A | **Correct (Leak)** | Stem wording leak: stem asked which command "runs Go linters", giving away `lint`. |
| **Q10** | E2E enabled env var (`RUN_E2E`) | B | B | **Correct (Leak)** | Standard Go/Kubernetes test naming convention. |
| **Q11** | E2E cleanup env var (`SKIP_CLEANUP`) | C | C | **Correct (Leak)** | Standard Kubernetes test harness idiom. |
| **Q12** | Default cluster name | C | B | Incorrect | Model guessed `eval-runner-e2e` instead of `kind`. |
| **Q13** | Prerequisite Go version | A | B | Incorrect | Model guessed Go 1.24 instead of Go 1.27. |
| **Q14** | Kubeconfig context switch | A | A | **Correct (Leak)** | Option length bias: option A was significantly longer and more detailed than distractors. |
| **Q15** | Inspect tasks folder | B | B | **Correct (Leak)** | Repository naming convention: `eval-runner` storing evals in `evals/`. |
| **Q16** | Run TTL (negative control) | D | D | **Correct** | Option D was the only "Not stated" option among 3 numeric choices. |
| **Q17** | Artifact storage backend (negative control) | D | B / A | Incorrect | Model confidently guessed GCS / AWS. |
| **Q18** | Sandbox CPU requests (negative control) | D | B | Incorrect | Model confidently guessed 1000m. |
| **Q19** | Slack webhook alerts (negative control) | B | B | **Correct** | Option B was the only "Not configured" option. |
| **Q20** | Completion token limit (negative control) | D | D | **Correct** | Option D was the only "Not stated" option among numeric limits. |

---

## Dataset Rewriting & Mitigation Strategies

To close pretraining leaks and bring blind accuracy closer to chance:

1. **Replaced Google License Question (Q6)**:
   - *Problem*: Any LLM knows Google repos use Apache 2.0.
   - *Fix*: Replaced with a question testing the README early development status caveat (which capabilities are currently implemented in the controller vs design-stage concept).

2. **Replaced Inspect Organization Question (Q5)**:
   - *Problem*: UK AISI authorship of Inspect is general knowledge.
   - *Fix*: Replaced with a question testing the top-level README heading under which architectural principles and tenets are documented (`Core Concepts`).

3. **Eliminated Stem Leaks (Q8, Q9)**:
   - *Problem*: "regenerates" leaked `ap generate`; "runs Go linters" leaked `ap lint`.
   - *Fix*: Removed "regenerates" from Q8 ("Which ap subcommand formats code, stamps copyright headers, and updates CI presubmit scripts?"). Rewrote Q9 to ask about executing specific tools: "According to docs/development.md, which command executes govet and gofmt via project automation?".

4. **Replaced CRD Manifest Tool (Q10)**:
   - *Problem*: `controller-gen` is the standard Kubernetes tooling.
   - *Fix*: Asked which task script under `dev/tasks/` generates CRDs (`./dev/tasks/generate-crds`), with balanced alternative script names (`build-crds`, `codegen`, `compile-crds`).

5. **Balanced Option Lengths & Distractors (Q14, Q15)**:
   - *Problem*: Length bias in Q14; standard directory guessing in Q15.
   - *Fix*: Equalized length across all 4 options in Q14. Replaced Q15 with the setup command (`uv sync` vs `uv pip install`, `uv install`, `uv lock --install`).

6. **Target Balancing & Negative Control Dispersal**:
   - Rebalanced target letters so all four choices (`A`, `B`, `C`, `D`) appear exactly **5 times each** (25.0% uniform distribution).
   - Distributed negative control correct answers across multiple letter positions (A, C, D) rather than defaulting to D.

7. **Evidence Drift Guard**:
   - Added an `evidence` field to every sample containing an exact, non-empty substring from the injected documentation.
   - Integrated unit checks into `dev/tasks/test-evals` verifying that every question's basis is intact.

---

## Rewritten Dataset Results

- **Model**: `google/gemini-3.5-flash-lite`
- **Target Distribution**: A: 5 (25%), B: 5 (25%), C: 5 (25%), D: 5 (25%)
- **With-Docs Accuracy**: **1.000** (20/20, 100.0%)
- **Blind Accuracy**: **0.350** (7/20, 35.0%) — reduced by 10 percentage points from 45% and within ~1 question of 25% chance expectation (5/20).

### Per-Question Breakdown (Rewritten Dataset)

| Question # | Topic | Target | Blind Prediction | Outcome | Notes |
|---|---|---|---|---|---|
| **Q1** | Primary tagline | B | A | Incorrect | Correctly punished guessing "CI for agents". |
| **Q2** | Unit of measurement | A | A | Correct | Chance guess. |
| **Q3** | `.evals/list` output format | D | D | Correct | Guessed JSON manifest. |
| **Q4** | Run-level verdicts | C | A | Incorrect | Correctly punished guessing "IMPROVEMENT, REGRESSION...". |
| **Q5** | Top-level README heading | B | C | Incorrect | Guessed "Design Tenets" instead of "Core Concepts". |
| **Q6** | Implemented controller status | D | B | Incorrect | Guessed micro-VMs instead of discovery sandbox. |
| **Q7** | agent-sandbox pinned release | A | B | Incorrect | Guessed v0.9.4 instead of v1.0.7. |
| **Q8** | `ap generate` tasks | C | D | Incorrect | Guessed `ap codestyle` instead of `ap generate`. |
| **Q9** | `ap lint` tools | A | C | Incorrect | Guessed `ap check` instead of `ap lint`. |
| **Q10** | CRD generator script | B | C | Incorrect | Guessed `codegen` instead of `generate-crds`. |
| **Q11** | `SKIP_CLEANUP=1` effect | C | C | Correct | Standard harness semantics. |
| **Q12** | Controller deployment namespace | B | B | Correct | Guessed `eval-runner` namespace. |
| **Q13** | Prerequisite Go version | D | A | Incorrect | Guessed Go 1.24 instead of Go 1.27. |
| **Q14** | Kubeconfig context alteration | A | B | Incorrect | Length bias eliminated; guessed KUBECONFIG override. |
| **Q15** | `uv sync` setup command | B | B | Correct | Guessed `uv sync`. |
| **Q16** | Run TTL (negative control) | C | C | Correct | Negative control target at C correctly identified. |
| **Q17** | Storage backend (negative control) | D | A | Incorrect | Confidently guessed S3 bucket. |
| **Q18** | Sandbox CPU requests (negative control) | A | C | Incorrect | Confidently guessed 1000m CPU. |
| **Q19** | Slack alerts (negative control) | D | D | Correct | Negative control target at D correctly identified. |
| **Q20** | Token limit (negative control) | C | A | Incorrect | Confidently guessed 4096 tokens. |

**Summary**: 13 incorrect vs 7 correct (35% accuracy). With docs, accuracy is 20/20 (100%), demonstrating clear discrimination between reader comprehension and blind guessing.
