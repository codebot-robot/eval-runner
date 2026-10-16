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

A `Run` represents an evaluation run against a specific commit of a git repository. The eval-runner controller reconciles `Run` objects by executing `.evals/list` discovery inside an untrusted, zero-privilege sandbox in the `eval-runner-sandbox` namespace and recording the discovered task manifest in `status.manifest`.

## Example

```yaml
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: support-agent-abc123
  namespace: eval-runner
spec:
  repo:
    url: git://git-server.eval-runner-sandbox.svc/support-agent.git
    commit: abc123def456...
status:
  phase: Discovered
  message: ""
  conditions:
    - type: Discovered
      status: "True"
      reason: Discovered
      message: "Discovered 3 tasks"
  manifest:
    images: []
    tasks:
      - id: triage/runbook_lookup
        workdir: evals
        spec: runbook_lookup.py@runbook_lookup
        resources: []
      - id: triage/escalation_policy
        workdir: evals
        spec: escalation.py@escalation_policy
        resources: []
      - id: triage/summary_quality
        workdir: evals
        spec: summary.py@summary_quality
        resources: []
```

## Specification (`spec`)

| Field | Type | Description |
|---|---|---|
| `repo` | `RepoSpec` | Git repository and commit to evaluate. **Required.** |
| `repo.url` | `string` | The clone URL for the repository under test. **Required.** |
| `repo.commit` | `string` | The exact git commit SHA (7 to 64 hex characters) to check out. **Required.** |

## Status (`status`)

| Field | Type | Description |
|---|---|---|
| `phase` | `string` | The current lifecycle phase of the run. See [Phases](#phases). |
| `message` | `string` | Human-readable explanation or error details (including stderr tail on failure). |
| `conditions` | `[]metav1.Condition` | Standard Kubernetes condition list. Includes condition `Discovered`. |
| `manifest` | `*Manifest` | The parsed output of `.evals/list` upon successful discovery. |
| `discoveryDeadline` | `*metav1.Time` | Overall deadline by which discovery must complete. |

### Phases

| Phase | Description |
|---|---|
| `Pending` | The Run has been created and is queued for discovery. |
| `Discovering` | A sandbox is running in `eval-runner-sandbox` performing discovery via git clone and `.evals/list`. |
| `Discovered` | Discovery succeeded. `status.manifest` contains the parsed tasks and the sandbox has been cleaned up. |
| `Failed` | Discovery failed (e.g. invalid commit, non-zero command exit, invalid manifest). Details are in `status.message`. |

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
| `tasks[].resources` | `[]ManifestResource` | Declarative resource requirements for the task. |
| `tasks[].images` | `[]string` | Names of images required by this task. |
