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

# Statistics in eval-runner: Adaptive, Paired Sampling

**Principle: Bound the confidence interval, not the budget.**

An agent is a distribution, not a function: evaluating an agent on the same prompt yields different trajectories and scores across runs. Naive CI setups (e.g. 1,000 runs × every eval × every PR) are economically ruinous ($1,400+ and hours of execution per PR). `eval-runner` treats evaluation as an adaptive statistical estimation problem: it runs paired samples until the confidence interval on the delta either clears zero or fits entirely within an equivalence margin, reaching the exact same verdict for a fraction of the cost ($4.10 and 14 minutes).

The statistical core is implemented as a pure Go package in `pkg/stats` with no cluster or third-party dependencies.

In the `eval-runner` controller, each completed task on a `Run` reports its point estimate in `status.tasks[].successRate` and its 95% confidence interval in `status.tasks[].successRateCI: {lower, upper, confidence: 0.95, method: "wilson"|"t"}` (see [API Reference: Run](api.md)). Aggregate run counts, total samples, and the range of task success rates are recorded in `status.summary`.

---

## 1. Single-Eval Success Rate & Confidence Intervals

### 1.1 Proportion: Wilson Score Interval

For binary success/failure outcomes (0 or 1), `Proportion(successes, n int, confidence float64) Estimate` computes the point estimate $p = x/n$ and the **Wilson score confidence interval**.

#### Why Wilson Over Wald
The textbook Wald interval is:
$$p \pm z \sqrt{\frac{p(1-p)}{n}}$$

While ubiquitous in introductory textbooks, the Wald interval is disastrous for LLM evals:
1. **Severe under-coverage at small $n$**: For sample sizes common in early eval phases ($n=10 \dots 40$), actual coverage is often far below 95% (frequently dropping below 80%).
2. **Degeneracy at boundaries**: When $x=0$ or $x=n$, the sample variance $\sqrt{p(1-p)/n}$ evaluates to 0, producing an absurd zero-width interval $[0, 0]$ or $[1, 1]$.
3. **Out-of-bounds intervals**: Wald intervals routinely produce lower bounds $< 0$ or upper bounds $> 1$.

The Wilson score interval inverts the score test under the binomial distribution:
$$\text{Center} = \frac{p + \frac{z^2}{2n}}{1 + \frac{z^2}{n}}, \quad \text{Margin} = \frac{z}{1 + \frac{z^2}{n}} \sqrt{\frac{p(1-p)}{n} + \frac{z^2}{4n^2}}$$
$$\text{CI} = \left[\max\left(0, \text{Center} - \text{Margin}\right), \; \min\left(1, \text{Center} + \text{Margin}\right)\right]$$

For example, at 95% confidence ($z \approx 1.96$):
- $7 / 10$ successes $\implies [0.397, \; 0.892]$ (point: $0.70$)
- $0 / 10$ successes $\implies [0.000, \; 0.278]$ (bounded, non-zero uncertainty)
- $10 / 10$ successes $\implies [0.722, \; 1.000]$ (bounded)

Empirical simulation confirms that Wilson maintains ~95% coverage across small and moderate $n$.

### 1.2 Mean: Student's t-Interval for Partial Credit

Inspect scorers frequently emit continuous partial credit scores in $[0, 1]$ (e.g. model-graded fact scores). For non-binary scores, `Mean(scores []float64, confidence float64) Estimate` computes the sample mean and Student's t-distribution confidence interval:
$$\bar{x} \pm t^*_{n-1} \cdot \frac{s}{\sqrt{n}}$$

The controller selects the interval method dynamically based on parsed scores: if every parsed sample score is strictly 0.0 or 1.0 (or if results fallback is used with no individual sample scores), `wilson` is selected; if any sample receives a fractional score, `t` is selected. Note that a partial-credit scorer that happened to emit only 0.0 and 1.0 on a given run will therefore be labelled `wilson`.

#### Caveats for Tiny $n$
- **$n = 1$**: Sample variance is undefined ($df = 0$). The function returns point estimate $x_0$ with an uninformative interval $[0.0, 1.0]$.
- **Small $n$ ($n = 2, 3$)**: The critical value $t^*$ is very large ($t^* = 12.71$ for $df=1$, $t^* = 4.30$ for $df=2$), reflecting genuine uncertainty when estimating variance from very few observations.
- **Support bounds**: Because eval scores are bounded in $[0, 1]$, intervals are clamped to $[0.0, 1.0]$.

---

## 2. Paired Baseline Comparison (PR vs Base)

### 2.1 Why Paired Sampling

Inputs vary widely in inherent difficulty: some questions are easy for all agent configurations, while others are difficult. If candidate samples and baseline samples are drawn independently, input variance contaminates the estimated effect size.

`eval-runner` runs **paired samples on identical sample IDs**. For each sample $i$:
$$d_i = \text{score}_{\text{candidate}, i} - \text{score}_{\text{baseline}, i}$$

By taking the difference on the same input, input difficulty cancels out:
$$\text{Var}(\bar{d}) = \frac{\text{Var}(X_{\text{cand}}) + \text{Var}(X_{\text{base}}) - 2\text{Cov}(X_{\text{cand}}, X_{\text{base}})}{n}$$
When candidate and baseline performances are positively correlated on the same benchmark inputs ($\text{Cov} > 0$), paired sampling drastically reduces sample variance, requiring far fewer samples to establish statistical significance.

### 2.2 Handling Unpaired Samples

Samples with missing pairs (e.g. candidate evaluated on an ID not in baseline, or vice-versa) are explicitly counted and reported in `DroppedCandidate` and `DroppedBaseline`. They are never silently ignored or imputed.

### 2.3 Confidence Interval on Paired Delta

`pkg/stats` provides both:
1. **Bootstrap Percentile CI (Default)**: Re-samples paired differences $B=2,000$ times with a fixed seed (`DefaultSeed = 42`) and takes empirical quantiles $(\alpha/2, 1 - \alpha/2)$.
   - *Rationale*: Paired differences in binary evals are discrete trinary values $\{-1, 0, +1\}$ (PR lost, tied, or won) or skewed partial credit differences. Percentile bootstrap makes no normality assumption and respects skewness and bounded differences.
   - *Degenerate intervals at small $n$*: If all paired differences in a sample are identical (for example, 1 win, or 2 ties), all bootstrap resamples produce the exact same mean, yielding a zero-width interval $[d, d]$. The stopping rule in `Decide` guards against premature termination by requiring a minimum sample count (`MinPairsToDecide = 8`) before CI-based stopping rules can fire.
2. **Normal Approximation CI**: Classical $\bar{d} \pm z \cdot (s_d / \sqrt{n})$ reported alongside for direct analytical comparison.

### 2.4 Posterior P(improvement)

`PImprovement` is the posterior probability that the true delta is positive ($P(\Delta > 0)$).

`eval-runner` implements **Rubin's Bayesian Bootstrap** (1981):
- Rather than integer resampling, Dirichlet weights $(w_1, \dots, w_n) \sim \text{Dirichlet}(1, \dots, 1)$ are drawn across observations by sampling standard exponentials $e_i = -\ln(U_i)$.
- For each draw $b$, the weighted mean $\mu^*_b = \sum_{i=1}^n w_i d_i$ is computed.
- $P(\text{improvement}) = \frac{1}{B} \sum_{b=1}^B \mathbb{I}(\mu^*_b > 0)$.

*Rationale*: Unlike Beta-Binomial models that collapse differences into win/loss/tie buckets, the Bayesian Bootstrap operates directly on continuous differences, seamlessly handling Inspect partial credit while preserving exact Bayesian posterior interpretation.

---

## 3. Stopping Rule and Task Labels

`Decide(est PairedEstimate, margin float64, samplesRun, sampleCap int, spent, budget float64) Decision` implements the stopping rule from §4.2:

```
                          [ Paired 95% CI on Δ ]
                                    │
           ┌────────────────────────┼────────────────────────┐
           ▼                        ▼                        ▼
     Excludes Zero?        Inside ±margin (0.05)?    Budget or Cap Hit?
    (requires n ≥ 8)          (requires n ≥ 8)               │
     ┌─────┴─────┐                  │                        │
     ▼           ▼                  ▼                        ▼
  [> 0]       [< 0]            [unchanged]             [inconclusive]
improved    regressed             (stop)                   (stop)
 (stop)      (stop)
                                    │
                             (None of above)
                                    ▼
                                [continue]
                     (suggest next batch size & N_more)
```

1. **CI-based stopping rules (requires $n \ge \text{MinPairsToDecide} = 8$)**:
   - **CI excludes zero**:
     - $\text{CI}_{\text{lower}} > 0 \implies \mathbf{improved}$ (stop)
     - $\text{CI}_{\text{upper}} < 0 \implies \mathbf{regressed}$ (stop)
   - **CI entirely within equivalence margin** $[-\text{margin}, +\text{margin}]$ (default $\pm 0.05$):
     - $-\text{margin} \le \text{CI}_{\text{lower}} \le \text{CI}_{\text{upper}} \le +\text{margin} \implies \mathbf{unchanged}$ (stop)
2. **Sample cap or budget reached**:
   - $\text{samplesRun} \ge \text{sampleCap}$ or $\text{spent} \ge \text{budget} \implies \mathbf{inconclusive}$ (stop)
3. **Otherwise (or if $n < 8$)**:
   - $\mathbf{continue}$ sampling. Returns suggested `NextBatchSize` (e.g. 8, 16, 24, 32) and `EstimatedSamples` to reach resolution.

### Sample Size Estimation Formula
The CI half-width narrows as $W \propto 1/\sqrt{n}$. To reach target resolution $W_{\text{target}} = \max(|\bar{d}|, \text{margin})$:
$$N_{\text{total}} \approx n_{\text{current}} \cdot \left(\frac{W_{\text{current}}}{W_{\text{target}}}\right)^2, \quad N_{\text{more}} = N_{\text{total}} - n_{\text{current}}$$

This powers the UI action: `▶ run 32 more samples (~$1.40)`.

---

## 4. Run-Level Verdict & Decisions for [Open] Items

### 4.1 Verdict Semantics Table (§4.4)

| Verdict | Condition | Meaning |
|---|---|---|
| `RUNNING` | Any task is running/queued/continue | Sampling in progress. Transient headline while tasks run. |
| `REGRESSED` | At least one task regressed (and none running) | Run failed: PR degraded agent behavior. |
| `INCONCLUSIVE` | No regressions, but at least one task is inconclusive | Uncertainty remains; more samples recommended. |
| `IMPROVED` | At least one task improved, all others unchanged | Net behavioral improvement. |
| `UNCHANGED` | All tasks unchanged | Equivalent performance. |

### 4.2 Architectural Choices for [Open] Concept Items

1. **Default CI for Paired Differences**:
   - *Choice*: Non-parametric **Bootstrap Percentile CI** as default (`est.CI`), with **Normal Approximation** retained as `est.NormalCI`.
   - *Reason*: Avoids assuming normality for discrete differences $\{-1, 0, 1\}$.
2. **P(improvement) Model**:
   - *Choice*: **Bayesian Bootstrap** (Dirichlet resampling).
   - *Reason*: Works uniformly across binary scores and continuous partial-credit scores without discretization.
3. **RUNNING vs REGRESSED Precedence**:
   - *Choice*: `RUNNING` ranks first in precedence. While any task is running, queued, or continuing, the run-level headline is `RUNNING`, with individual task status (including an early `regressed`) visible in the per-task table.
   - *Reason*: Avoids flipping the run headline back and forth while batches are actively in flight.
4. **Precedence for Mixed `improved` + `inconclusive`**:
   - *Choice*: Overall verdict is $\mathbf{INCONCLUSIVE}$.
   - *Reason*: Conservative safety gate. If an eval is inconclusive because it ran out of sample budget, it could conceal an undetected regression. A PR cannot be certified as an unconditional improvement while another eval remains unresolved.
5. **No Hard-Coded Probabilities in Verdict(labels)**:
   - *Choice*: `Verdict(labels)` evaluates categorical labels only and sets `PImprovement = 0.0`. Callers requiring posterior probabilities must use `AggregateVerdict`, preventing placeholder probabilities from being displayed on PRs.
6. **Run-Level Headline P(improvement) Aggregation & Label Floors**:
   - *Choice*:
     $$P(\text{run improvement}) = \min\left(\max_{k} P(\text{imp}_k), \; \min_{k} P(\text{not\_reg}_k)\right)$$
   - *Per-task $P(\text{not\_reg})$ definition and label floors*:
     - For tasks labelled `improved`: floor of $0.999$.
     - For tasks labelled `unchanged`: floor of $0.975$ (reflecting statistical equivalence within the margin).
     - For all other tasks (`regressed`, `inconclusive`, `running`): $P(\text{not\_reg}) = P(\text{imp})$.
   - *Headline value rules*:
     - If the aggregate verdict is `REGRESSED`, $P(\text{run improvement}) = \min_k P(\text{not\_reg}_k)$.
     - If `UNCHANGED`, headline $P(\text{run improvement}) = 0.50$.
     - Otherwise, $P(\text{run improvement}) = \min(\max_k P(\text{imp}_k), \min_k P(\text{not\_reg}_k))$.

---

## 5. Worked Example: `acme/support-agent` PR #418

This worked example traces the numbers from `docs/concept/functionality.md` and the UI mockups in `docs/concept/ui/budget.html` and `docs/concept/ui/pr_comment.html`.

### 5.1 Per-Task Results

| Task | Base | PR | Paired $\Delta$ (95% CI) | Decision Label | $n$ | $P(\text{improvement})$ |
|---|---|---|---|---|---|---|
| `triage/runbook_lookup` | 0.61 | 0.84 | $+0.23 \; [+0.11, +0.35]$ | `improved` | 48 | 0.99 |
| `triage/escalation_policy` | 0.88 | 0.90 | $+0.02 \; [-0.05, +0.09]$ | `unchanged` | 32 | 0.55 |
| `triage/summary_quality` | 0.72 | 0.75 | $+0.03 \; [-0.08, +0.14]$ | `inconclusive` | 16 | 0.60 |

### 5.2 Adaptive Narrowing for `triage/runbook_lookup` (`budget.html`)

As paired samples accumulate, the confidence interval narrows systematically:

- $n=4$: $[-0.42, +0.78]$ (width 1.20) $\implies$ `continue`
- $n=8$: $[-0.21, +0.60]$ (width 0.81) $\implies$ `continue`
- $n=16$: $[-0.06, +0.48]$ (width 0.54) $\implies$ `continue`
- $n=24$: $[0.00, +0.43]$ (width 0.43) $\implies$ `continue`
- $n=48$: $[+0.11, +0.35]$ (width 0.24) $\implies$ CI clear of zero: `improved`, stop!
- (If extended to $n=96$: $[+0.15, +0.31]$, width 0.16)

### 5.3 Run-Level Headline

- All tasks evaluated conclusively (or with summary quality resolved as unchanged):
  - $\max_k P(\text{imp}_k) = 0.99$ (`triage/runbook_lookup`)
  - $P(\text{not\_reg})$ for `triage/escalation_policy` (unchanged) has floor $0.975$ (yielding $0.9750$, displayed rounded as $0.98$ in UI)
  - Headline $P(\text{improvement}) = \min(0.99, 0.975) = \mathbf{0.975}$ ($\approx 0.98$)
- Posted PR Comment headline:
  `IMPROVED · 3f9c2e1 vs main · P(improvement) = 0.98`

---

## 6. CLI Tool: `cmd/evalstats`

A minimal CLI tool `cmd/evalstats` allows testing statistical computations against JSONL data streams before controller wiring lands.

### Usage

```bash
# Read JSONL from file
go run ./cmd/evalstats path/to/scores.jsonl

# Read from stdin
cat scores.jsonl | go run ./cmd/evalstats

# Treat static file as a complete run (-final marks unresolved tasks as inconclusive rather than continue)
go run ./cmd/evalstats -final path/to/scores.jsonl

# JSON output mode with custom margin
cat scores.jsonl | go run ./cmd/evalstats -margin 0.05 -json

# Read and inspect a Run directly from the Kubernetes cluster
go run ./cmd/evalstats -from-run docs-comprehension-run

# Specify namespace (defaults to eval-runner)
go run ./cmd/evalstats -from-run docs-comprehension-run -namespace eval-runner

# Output cluster Run statistics as JSON
go run ./cmd/evalstats -from-run docs-comprehension-run -json

# Read and inspect a Comparison from the Kubernetes cluster (PR-comment mockup format)
go run ./cmd/evalstats -from-comparison pr-418-eval

# Output cluster Comparison statistics as JSON
go run ./cmd/evalstats -from-comparison pr-418-eval -json
```

### Input Format (JSONL)

Each line is a JSON object with `{task, sample_id, base_score, pr_score}`:

```json
{"task": "triage/runbook_lookup", "sample_id": "incident-101", "base_score": 0.0, "pr_score": 1.0}
{"task": "triage/runbook_lookup", "sample_id": "incident-102", "base_score": 1.0, "pr_score": 1.0}
{"task": "triage/escalation_policy", "sample_id": "policy-001", "base_score": 1.0, "pr_score": 1.0}
```

### Output Example (JSONL Mode)

```
TASK                       BASE   THIS PR   Δ (95% CI)             LABEL        N    DROPPED
triage/escalation_policy   0.88   0.90      +0.02 [-0.05, +0.09]   unchanged    32   0/0
triage/runbook_lookup      0.61   0.84      +0.23 [+0.11, +0.35]   improved     48   0/0

VERDICT: IMPROVED · P(improvement) = 0.98
Summary: IMPROVED: 1 task(s) improved, 1 unchanged
```

### Output Example (`-from-run` Mode)

```
TASK                             RATE (95% CI)        N    METHOD
docs/readme_comprehension        1.00 [0.84, 1.00]    20   wilson
docs/readme_comprehension_blind  0.15 [0.05, 0.36]    20   wilson

Tasks: 2 completed, 0 failed (2 total) · Total samples: 40
Mean success rate: 0.58 (range 0.15-1.00)
```

### Output Example (`-from-comparison` Mode)

This output directly mirrors the PR comment mockup table layout:

```text
eval                       base   this PR   Δ (95% CI)             label        runs
triage/runbook_lookup      0.61   0.84      +0.23 [+0.11, +0.35]   improved     48
triage/escalation_policy   0.88   0.90      +0.02 [-0.05, +0.09]   unchanged    32
triage/summary_quality     0.72   0.75      +0.03 [-0.08, +0.14]   inconclusive 16

Verdict: IMPROVED · P(improvement) = 0.98
Summary: IMPROVED: 1 task(s) improved, 1 task(s) unchanged, 1 task(s) inconclusive; net improvement probable
```

For two deterministic runs at the same commit (e.g. mockllm or identical Gemini runs):

```text
eval                             base   this PR   Δ (95% CI)             label       runs
docs/readme_comprehension        1.00   1.00      +0.00 [+0.00, +0.00]   unchanged   20
docs/readme_comprehension_blind  0.15   0.15      +0.00 [+0.00, +0.00]   unchanged   20

Verdict: UNCHANGED · P(improvement) = 0.50
Summary: UNCHANGED: all 2 task(s) statistically equivalent within margin ±0.05
```

