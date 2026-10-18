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

## Eval Authoring Guidelines

When authoring new evaluation tasks or datasets for `eval-runner`, follow these standards:

### 1. Deterministic Dataset Ordering

**Principle: Datasets must have a stable, reproducible order.**

`eval-runner` supports windowed evaluation via `spec.sampleRange {start, end}` (e.g. `sampleRange: {start: 1, end: 10}` followed by `sampleRange: {start: 11, end: 20}`). For windowed evaluation and paired comparison to work reliably:
- Tasks **must not** randomly shuffle their dataset on load.
- If shuffling is necessary for a benchmark, tasks **must shuffle with a fixed, deterministic seed** (e.g. `shuffle=seed` or fixed random seed).
- Inspect tasks must preserve consistent sample order and IDs across runs. Random, unseeded shuffling causes sample windows to map to unpredictable sample IDs, breaking paired comparisons across candidate and baseline runs and resulting in dropped samples (`DroppedCandidate` / `DroppedBaseline`).

### 2. Inspect AI Conventions

- Tasks should be decorated with `@task` from `inspect_ai`.
- Solvers and scorers should use standard Inspect components (such as `multiple_choice()`, `choice()`, `match()`, or custom scorers returning numeric scores in $[0, 1]$).
- Tasks should support model overrides (e.g. `--model mockllm/model` or `--model google/gemini-3.5-flash-lite`) to allow local testing and offline CI execution.

## Running Locally

To run the evals locally using `uv`:

```bash
# Run with mock model (offline, no API key needed):
uv run inspect eval evals/docs_comprehension.py --model mockllm/model

# Run with a sample window (e.g. samples 1 through 5):
uv run inspect eval evals/docs_comprehension.py --model mockllm/model --limit 1-5

# Run with Gemini (requires GEMINI_API_KEY):
uv run inspect eval evals/docs_comprehension.py@readme_comprehension --model google/gemini-3.5-flash-lite
```
