<!-- eval-runner:comparison:eval-runner/docs-eval-batches-tokens -->

## RUNNING

Running batch 2 (1 tasks continuing)

| eval | base | this PR | Δ (95% CI) | label | samples | tokens |
| --- | --- | --- | --- | --- | --- | --- |
| docs/readme\_comprehension | 0.60 | 0.90 | +0.30 [+0.10, +0.50] | improved | 16 | 800 |
| docs/readme\_comprehension\_blind | 0.20 | 0.15 | -0.05 [-0.15, +0.00] | inconclusive | 16 | — |
| docs/readme\_summary | — | — | — | running | — | — |

64 run · 32 paired · paired against base commit a81d0c4 · `/comparisons/eval-runner/docs-eval-batches-tokens` · gs://eval-bucket/comparisons/docs-eval-batches-tokens.json

- run 16 more samples (docs/readme\_comprehension\_blind)
