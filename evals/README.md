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

# Evaluation Tasks (`evals/`)

This directory contains evaluation tasks for `eval-runner`, authored using the [UK AISI Inspect](https://inspect.ai-safety-institute.org.uk/) (`inspect_ai`) evaluation framework.

## Available Tasks

- **`docs/readme_comprehension`**: Evaluates reader and LLM comprehension of `eval-runner`'s documentation (`README.md` and `docs/development.md`) across multiple-choice questions defined in `datasets/readme_questions.jsonl`.
- **`docs/readme_comprehension_blind`**: A blind control task evaluating answers without providing the documentation in context, testing for pretraining leakage.

## Dataset Format

Questions are stored in `datasets/readme_questions.jsonl`. Each line is a JSON record with the following schema:

```json
{
  "id": "readme-tagline",
  "input": "What is eval-runner's primary tagline in README.md?",
  "choices": [
    "CI for agents: automated benchmarking of autonomous workflows.",
    "CI for evals: efficient statistical estimation of agentic goodness.",
    "CI for LLMs: continuous integration for probabilistic models.",
    "CI for benchmarks: scalable distribution testing for AI agents."
  ],
  "target": "B",
  "source": "README.md: eval-runner",
  "evidence": "CI for evals: efficient statistical estimation of agentic goodness."
}
```

Fields:
- **`id`**: Unique, stable string identifier for the sample (e.g. `readme-tagline`, `dev-kind-cluster`).
- **`input`**: The multiple-choice question prompt.
- **`choices`**: List of 4 multiple-choice options (`A`, `B`, `C`, `D`).
- **`target`**: The correct option letter (`A`, `B`, `C`, or `D`).
- **`source`**: The document and section where the answer is documented.
- **`evidence`**: A substring in the repository documentation verifying the answer. Verified by `test_drift.py` to prevent doc drift.
- **`absent`**: Optional list of substrings that must NOT appear in the repository documentation. Used for negative controls ("The documents do not state this.") to guard against spurious documentation changes.

## Eval Authoring Guidelines

When authoring new evaluation tasks or datasets for `eval-runner`, follow these standards:

### 1. Explicit Sample IDs (Required)

All evaluation tasks **must** define explicit, stable sample IDs (e.g. mapping `id="id"` in Inspect's `FieldSpec`).

**Positional IDs are a fallback, not a design.**
- If a dataset omits `id`, Inspect defaults to assigning positional indices (`1, 2, ...`).
- When `eval-runner` compares candidate and baseline runs, the Comparison controller pairs samples by Inspect sample ID.
- If a pull request inserts, deletes, or reorders a question, every subsequent sample silently shifts ID. Candidate sample 5 gets paired with baseline sample 5 (which was previously question 4), yielding a confident but completely erroneous paired estimate rather than dropping unmatched samples.
- Explicit IDs ensure that when samples are added or modified across commits, unmatched samples are surfaced as dropped/unpaired, preserving statistical integrity.

**Invariant: IDs are permanent.**
- Never rename or renumber an existing ID.
- A question whose text, choices, or target changes materially gets a *new* ID (the old one retires). Otherwise, comparisons across commits will pair two different questions under the same key, reintroducing the exact mispairing failure mode explicit IDs exist to prevent.

### 2. Deterministic Dataset Ordering

**Principle: Datasets must have a stable, reproducible order.**

`eval-runner` supports windowed evaluation via `spec.sampleRange {start, end}` (e.g. `sampleRange: {start: 1, end: 10}` followed by `sampleRange: {start: 11, end: 20}`). For windowed evaluation and paired comparison to work reliably:
- Tasks **must not** randomly shuffle their dataset on load.
- If shuffling is necessary for a benchmark, tasks **must shuffle with a fixed, deterministic seed** (e.g. `shuffle=seed` or fixed random seed).
- Inspect tasks must preserve consistent sample order and IDs across runs. Random, unseeded shuffling causes sample windows to map to unpredictable sample IDs, breaking paired comparisons across candidate and baseline runs and resulting in dropped samples (`DroppedCandidate` / `DroppedBaseline`).
- Always **append** new questions to the end of the dataset file (such as `datasets/readme_questions.jsonl`). Do not reorder or insert rows into the middle of the dataset, as that shifts which samples fall into positional windows. Explicit sample IDs protect pairing across edits, while stable ordering protects the consistency of sampling windows.

### 3. Inspect AI Conventions

- Tasks should be decorated with `@task` from `inspect_ai`.
- Solvers and scorers should use standard Inspect components (such as `multiple_choice()`, `choice()`, `match()`, or custom scorers returning numeric scores in $[0, 1]$).
- Tasks should support model overrides (e.g. `--model mockllm/model` or `--model google/gemini-3.5-flash-lite`) to allow local testing and offline CI execution.

### 4. Drift Guards and Validation

When adding or modifying questions, verify that the dataset passes validation:

```bash
# Run unit tests and drift guards
uv run --locked python -m unittest test_drift.py

# Run the complete evals verification task
./dev/tasks/test-evals
```

## Running Locally

To run the evals locally using `uv`:

```bash
# Run with mock model (offline, no API key needed):
uv run inspect eval evals/docs_comprehension.py --model mockllm/model

# Run with a sample window (e.g. samples 1 through 5):
uv run inspect eval evals/docs_comprehension.py --model mockllm/model --limit 1-5

# Run with Gemini (requires GEMINI_API_KEY):
uv run inspect eval evals/docs_comprehension.py@readme_comprehension --model google/gemini-3.5-flash-lite

# Run blind control task:
uv run inspect eval evals/docs_comprehension.py@readme_comprehension_blind --model google/gemini-3.5-flash-lite
```
