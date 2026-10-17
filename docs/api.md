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

# API Reference: Run (v1alpha1)

**Group**: `evalrunner.gke-labs.dev`  
**Version**: `v1alpha1`  
**Kind**: `Run`  
**Scope**: Namespaced (runs reside in the `eval-runner` namespace)

> **Note**: `v1alpha1` is provisional and subject to change in future releases.

## Overview

A `Run` represents an evaluation run against a specific commit of a git repository. The eval-runner controller reconciles `Run` objects in two stages:
1. **Discovery**: Executes `.evals/list` discovery inside an untrusted, zero-privilege sandbox in the `eval-runner-sandbox` namespace and records the discovered task manifest in `status.manifest`. If `spec.model` is empty, the Run stops at `Discovered`.
2. **Sample Execution**: If `spec.model` is set, the controller executes eligible tasks from the manifest in isolated sandboxes in `eval-runner-sandbox` using Inspect, pulls out results, computes aggregate scores, and records per-task status in `status.tasks`.

## Example

```yaml
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: docs-comprehension-run
  namespace: eval-runner
spec:
  repo:
    url: https://github.com/gke-labs/eval-runner.git
    commit: abc123def456...
  model: google/gemini-3.5-flash-lite
  tasks:
    - docs/readme_comprehension
    - docs/readme_comprehension_blind
  limit: 20
status:
  phase: Completed
  message: ""
  conditions:
    - type: Discovered
      status: "True"
      reason: Discovered
      message: "Discovered 2 tasks"
  manifest:
    images: []
    tasks:
      - id: docs/readme_comprehension
        workdir: evals
        spec: docs_comprehension.py@readme_comprehension
        resources: []
      - id: docs/readme_comprehension_blind
        workdir: evals
        spec: docs_comprehension.py@readme_comprehension_blind
        resources: []
  tasks:
    - id: docs/readme_comprehension
      phase: Completed
      samples: 20
      successes: 17
      meanScore: 0.85
      logPath: ""
    - id: docs/readme_comprehension_blind
      phase: Completed
      samples: 20
      successes: 5
      meanScore: 0.25
      logPath: ""
```

## Specification (`spec`)

| Field | Type | Description |
|---|---|---|
| `repo` | `RepoSpec` | Git repository and commit to evaluate. **Required.** |
| `repo.url` | `string` | The clone URL for the repository under test. **Required.** |
| `repo.commit` | `string` | The exact git commit SHA (7 to 64 hex characters) to check out. **Required.** |
| `model` | `string` | Inspect model identifier (e.g. `mockllm/model`, `google/gemini-3.5-flash-lite`). If empty, the Run stops after discovery. |
| `tasks` | `[]string` | Optional allow-list of task IDs to run. Default: all tasks declaring `resources: []`. |
| `limit` | `*int` | Optional per-task cap on samples (passed to Inspect as `--limit`) for cost and time control. |

## Status (`status`)

| Field | Type | Description |
|---|---|---|
| `phase` | `string` | The current lifecycle phase of the run. See [Phases](#phases). |
| `message` | `string` | Human-readable explanation or error details (including stderr tail on failure). |
| `conditions` | `[]metav1.Condition` | Standard Kubernetes condition list. Includes condition `Discovered`. |
| `manifest` | `*Manifest` | The parsed output of `.evals/list` upon successful discovery. |
| `discoveryDeadline` | `*metav1.Time` | Overall deadline by which discovery must complete. |
| `executionDeadline` | `*metav1.Time` | Overall deadline by which sample execution must complete. |
| `tasks` | `[]TaskStatus` | Per-task execution results and score aggregates. See [Tasks](#taskstatus). |

### Phases

| Phase | Description |
|---|---|
| `Pending` | The Run has been created and is queued for discovery. |
| `Discovering` | A sandbox is running in `eval-runner-sandbox` performing discovery via git clone and `.evals/list`. |
| `Discovered` | Discovery succeeded. `status.manifest` contains the parsed tasks and the discovery sandbox has been cleaned up. (Terminal if `spec.model` is empty). |
| `Running` | Sample tasks are actively executing in parallel sandboxes in `eval-runner-sandbox`. |
| `Completed` | All sample tasks have finished execution and per-task aggregates are recorded in `status.tasks`. |
| `Failed` | Discovery or execution encountered a terminal error (e.g. invalid commit, non-zero command exit, invalid manifest, deadline exceeded). Details are in `status.message`. |

### TaskStatus (`status.tasks[]`)

| Field | Type | Description |
|---|---|---|
| `id` | `string` | Unique task identifier matching the manifest. |
| `phase` | `string` | Execution phase of the task (`Pending`, `Running`, `Completed`, `Failed`). |
| `samples` | `int` | Number of samples scored. |
| `successes` | `int` | Number of samples with score >= 1.0 (for binary scorers). |
| `meanScore` | `*float64` | Mean of per-sample scores. Numeric values are taken as-is (unclamped), 'C'/'CORRECT'/'YES'/'TRUE' map to 1.0, 'P'/'PARTIAL' map to 0.5, and 'I'/'INCORRECT'/'N'/'NOANSWER' map to 0.0. |
| `message` | `string` | Failure detail or bounded error information if the task failed. |
| `logPath` | `string` | Storage path or URI where the raw Inspect log was stored by the artifact sink. |

### Manifest (`status.manifest`)

| Field | Type | Description |
|---|---|---|
| `images` | `[]ManifestImage` | Container images declared by the repository under test. |
| `images[].name` | `string` | Name and tag of the image. |
| `images[].context` | `string` | Build context directory within the repository. |
| `tasks` | `[]ManifestTask` | Discovered evaluation tasks. |
| `tasks[].id` | `string` | Stable unique task identifier (e.g., `triage/runbook_lookup`). |
| `tasks[].workdir` | `string` | Optional subdirectory from which to execute the task. |
| `tasks[].spec` | `string` | Framework-specific entrypoint specification (e.g. `file.py@task_fn`). |
| `tasks[].resources` | `[]ManifestResource` | Declarative resource requirements for the task. (Tasks with non-empty resources are marked `Failed` as unsupported in this release). |
| `tasks[].images` | `[]string` | Names of images required by this task. |

## Resource and Output Limits

Because `.evals/list` and `inspect eval` execute within untrusted sandboxes, the controller enforces strict size boundaries on exec streams, manifests, and status updates. This protects the controller against memory exhaustion and prevents rejected status writes to the Kubernetes API server (etcd enforces a ~1.5 MiB object limit).

| Limit | Constant / Flag | Default | Behavior when Exceeded |
|---|---|---|---|
| Discovery stdout | `--max-stdout-bytes` | `1048576` (1 MiB) | Remaining output discarded; Run marked `Failed` with `stdout limit exceeded`. |
| Discovery stderr | `--max-stderr-bytes` | `65536` (64 KiB) | Remaining output discarded; Run marked `Failed` with `stderr limit exceeded`. |
| Manifest tasks | `MaxTasks` | `1000` | Validation fails, Run marked `Failed` with task count error. |
| Failure message length | `MaxMessageBytes` | `4096` | Truncated safely with `\n... [truncated]`. |
| Stderr tail lines | `MaxStderrLines` | `10` | Only the most recent lines of stderr are retained. |
| Stderr tail bytes | `MaxStderrTailBytes` | `3072` | Stderr tail is capped before inclusion in failure message. |
| Discovery deadline | `--discovery-deadline` | `10m` | Overall discovery deadline exceeded; Run marked `Failed` and sandbox deleted. |
| Execution deadline | `--execution-deadline` | `30m` | Overall sample execution deadline exceeded; Run marked `Failed` and sandboxes deleted. |
| Max parallel tasks | `--max-parallel-tasks` | `4` | Caps concurrent task sandboxes executing at one time. |
| Model credentials | `--model-secret` | `""` (none) | Names a Secret in `eval-runner-sandbox` whose keys are injected into sample sandboxes. |
| Sample runner image | `--sample-runner-image` | `""` (derived from controller pod image) | Container image used for sample task sandboxes (derived from controller pod image when unset). |

