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

# Development Guide

This document describes how to set up your local development environment, deploy eval-runner, and run tests.

## Prerequisites

- **Go**: 1.27 or newer
- **Docker**: Docker daemon running (locally or via `DOCKER_HOST`). (The `buildx` plugin is only required if building multi-arch images with `ap`.)
- **Git**
- **Kubernetes cluster with agent-sandbox**: `agent-sandbox` is a prerequisite for running `eval-runner`. On any cluster where `eval-runner` is deployed, install the pinned `v1.0.7` release:

  ```bash
  kubectl apply -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v1.0.7/sandbox.yaml
  ```

  The controller will verify that the `Sandbox` CRD (`agents.x-k8s.io/v1beta1`) is present upon startup and fail fast with an error message if it is missing.

### Upgrading the `sandboxd` Image

`eval-runner` injects `sandboxd` into discovery sandboxes via an initContainer using the image reference pinned by digest in `pkg/discovery/sandbox.go` (`SandboxdImage` constant).

To upgrade or repin the `sandboxd` image:
1. Locate the target release or staging image from [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox).
2. Inspect the multi-architecture manifest list using `docker buildx imagetools inspect <image-ref>` to verify available platforms and obtain its exact `@sha256:` digest.
3. Update `SandboxdImage` in `pkg/discovery/sandbox.go` with the pinned digest reference.
4. Run `ap test` and `ap e2e` to verify.

Tools like `kind` and `kubectl` do not need to be installed beforehand for testing; test scripts can download and manage them automatically under `.build/bin/`.

## Project Automation (`ap`)

This repository uses [`ap` (autoproject)](https://github.com/gke-labs/gke-labs-infra/blob/main/ap/llms.txt) for project automation, manifests, and CI generation.

Common commands:

- `ap generate`: Formats code, stamps copyright headers, runs CRD generation tasks, updates CI presubmit scripts (`dev/ci/presubmits/`), and regenerates `.github/workflows/ci-presubmits.yaml`.
- `ap lint`: Runs Go linters (`govet`, `gofmt`, etc.).
- `ap test`: Runs unit tests (`go test`).
- `ap build`: Builds container images defined under `images/`.
- `ap deploy`: Builds and deploys images and Kubernetes manifests from `k8s/` into the active cluster.
- `ap e2e`: Discovers and executes all end-to-end tests.

## Generating CRDs and DeepCopy

CRD manifests under `k8s/` and Go `runtime.Object` deepcopy methods are generated using `controller-gen`.

Run:

```bash
./dev/tasks/generate-crds
```

This task is also invoked automatically whenever `ap generate` is executed.

## Deploying to a Cluster (`ap deploy`)

To deploy `eval-runner` into a target Kubernetes cluster (with `agent-sandbox` installed), deploys must use a unique image tag rather than `latest`. Deploying with `latest` leaves the Deployment manifest byte-identical when updating an existing installation, causing `kubectl apply` to report `unchanged` and leaving the cluster silently running the previous digest.

The recommended way to deploy is using the `./dev/tasks/deploy` wrapper, which automatically sets `IMAGE_TAG` from git (short SHA, plus a `-dirty` suffix when the working tree is dirty), invokes `ap` at the repository-pinned version via `go run` (never from `PATH`), verifies that the pushed multi-platform index contains `linux/amd64`, and applies the manifests:

```bash
IMAGE_PREFIX=<registry> ./dev/tasks/deploy
```

For example, on Google Kubernetes Engine (GKE):

```bash
IMAGE_PREFIX=gcr.io/my-gke-project ./dev/tasks/deploy
```

Alternatively, invoke `ap deploy` directly by setting a unique tag:

```bash
IMAGE_TAG=$(git rev-parse --short HEAD) IMAGE_PREFIX=<registry> ap deploy
```

(`ap` honours `IMAGE_TAG`.)

`./dev/tasks/deploy` will:
1. Build the `eval-runner` container image (for `linux/amd64` and `linux/arm64`, pinned in `.ap/images.yaml`).
2. Tag and push the multi-platform image index to `<IMAGE_PREFIX>/eval-runner:<IMAGE_TAG>`.
3. Inspect the pushed index with `docker buildx imagetools inspect` and verify it contains `linux/amd64`, failing immediately before applying manifests if absent.
4. Apply the manifests in `k8s/` in order (`00-namespaces.yaml`, `evalrunner.gke-labs.dev_runs.yaml`, `rbac.yaml`, `sandbox-networkpolicy.yaml`, `runner-deployment.yaml`).
5. Substitute the `eval-runner` image reference in the deployment with the full pushed image reference (`<IMAGE_PREFIX>/eval-runner:<IMAGE_TAG>`).
6. Wait for the CRD to be established and the deployment to become available.

### Verifying the Running Digest

To verify which image digest is actively running in the cluster:

```bash
kubectl -n eval-runner get pods -o jsonpath='{.items[*].status.containerStatuses[*].imageID}'
```

### Recovery: Manual Rollout Restart

If a cluster was previously deployed with `latest` (or an unchanged tag) and did not roll automatically, restart the deployment to force Kubernetes to pull and start updated pods:

```bash
kubectl -n eval-runner rollout restart deployment/eval-runner-controller
```

## Creating a Run by Hand

Once the controller is deployed, you can create a `Run` resource targeting any git repository:

```bash
kubectl apply -f - <<EOF
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: test-run
  namespace: eval-runner
spec:
  repo:
    url: https://github.com/my-org/my-repo.git
    commit: abc1234def5678...
EOF
```

### Inspecting Runs

View the status and phase of runs:

```bash
kubectl get runs -n eval-runner
```

View detailed status, conditions, and the discovered manifest:

```bash
kubectl get run test-run -n eval-runner -o yaml
```

### Inspecting Sandboxes in `eval-runner-sandbox`

During discovery, the controller creates an isolated sandbox in the `eval-runner-sandbox` namespace:

```bash
# View active sandboxes
kubectl get sandboxes -n eval-runner-sandbox

# View backing discovery pods
kubectl get pods -n eval-runner-sandbox

# View logs from a discovery container
kubectl logs -n eval-runner-sandbox discovery-test-run -c agent
```

Once discovery completes (or fails), the sandbox is automatically deleted.

### Running Sample Evaluations with a Real Model (e.g. Gemini on GKE)

To execute real evals against a repository, provide `spec.model` on the `Run`. For cloud models (such as Gemini), inject provider credentials using a Kubernetes Secret in the sandbox namespace:

1. **Ensure the Model Secret exists in `eval-runner-sandbox`:**

   Our GKE dev cluster uses `eval-runner-sandbox/model-credentials` containing key `GEMINI_API_KEY`. If creating it from scratch:

   ```bash
   kubectl -n eval-runner-sandbox create secret generic model-credentials \
     --from-literal=GEMINI_API_KEY="${GEMINI_API_KEY}"
   ```

2. **Configure the Controller:**

   Configure the eval-runner controller deployment with `--model-secret=model-credentials` using a JSON patch on the container args:

   ```bash
   kubectl -n eval-runner patch deployment eval-runner-controller --type=json \
     -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--model-secret=model-credentials"}]'
   ```

   Alternatively, add `- --model-secret=model-credentials` under `args` in `k8s/runner-deployment.yaml` and deploy via `ap deploy`.

   *(Note: This is an interim credential injection mechanism; future architecture targets zero-credential sandboxes via a model gateway.)*

3. **Create the Run:**

   ```bash
   kubectl apply -f - <<EOF
   apiVersion: evalrunner.gke-labs.dev/v1alpha1
   kind: Run
   metadata:
     name: docs-comprehension-gemini
     namespace: eval-runner
   spec:
     repo:
       url: https://github.com/gke-labs/eval-runner.git
       commit: $(git rev-parse HEAD)
     model: google/gemini-3.5-flash-lite
     tasks:
       - docs/readme_comprehension
       - docs/readme_comprehension_blind
     limit: 10
   EOF
   ```

4. **Watch and View Results:**

   Watch the run progress from `Pending` -> `Discovering` -> `Running` -> `Completed`:

   ```bash
   kubectl get run docs-comprehension-gemini -n eval-runner -w
   ```

   View per-task scores and aggregates once completed:

   ```bash
   kubectl get run docs-comprehension-gemini -n eval-runner -o yaml
   ```

#### Provider Dependencies and Custom Images

The default sample runner sandbox image (`images/sample-runner`) includes Inspect AI alongside pinned provider dependencies for common model providers out of the box:
- `google-genai` (for Gemini models, our default)
- `openai`
- `anthropic`

For evaluations requiring other model providers or custom environments, a repository can declare custom images in `.evals/list` using the manifest's top-level `images[]` declarations and `tasks[].images[]` references (see concept §2.2 in `docs/concept/functionality.md`).

## Running Unit Tests

Run all unit tests with:

```bash
go test ./...
```

E2E tests will automatically be skipped unless `RUN_E2E=1` is provided.

## Running End-to-End (E2E) Tests

E2E tests live in `tests/e2e/`. They test eval-runner against a real Kubernetes cluster using `kind`, `kubernetes-sigs/agent-sandbox`, and in-cluster git server fixtures.

### Option 1: Automated via `ap e2e` (Recommended)

Run:

```bash
ap e2e
```

(or `go run github.com/gke-labs/gke-labs-infra/ap@latest e2e .` or `./dev/ci/presubmits/ap-e2e`)

This provides a one-command run with no pre-installed `kind` or `kubectl`:
1. Executes `dev/tasks/test-e2e` to download and install pinned versions of `kind` and `kubectl` into `.build/bin/`.
2. Automatically discovers and executes all Go E2E tests under `tests/e2e/` with `RUN_E2E=1`, running `TestRunDiscovery`.

### Option 2: Running via `go test` directly

If `kind` and `kubectl` are already installed on your system `PATH` (or after running `./dev/tasks/test-e2e` once to populate `.build/bin`):

```bash
RUN_E2E=1 go test -v -count=1 ./tests/e2e/...
```

To run a specific test:

```bash
RUN_E2E=1 go test -v -count=1 ./tests/e2e/... -run TestRunDiscovery
```

> **Note on Kubeconfig Context:** When running tests against a cluster, the harness switches the active kubectl context using `kubectl config use-context kind-<clusterName>`, which updates the current context in your kubeconfig (`~/.kube/config`).

## Environment Variables

The E2E test harness recognizes several environment variables:

| Variable | Default | Description |
|---|---|---|
| `RUN_E2E` | _(unset)_ | When set to `1` (or any non-empty value), enables running the E2E test suite. When unset, E2E tests are skipped. |
| `KIND_CLUSTER_NAME` | `kind` | Name of the kind cluster to create or use. If a cluster with this name already exists, the test reuses it without re-creating it or deleting it afterwards. |
| `SKIP_CLEANUP` | _(unset)_ | When set to `1` (or any non-empty value), preserves the kind cluster and test namespaces created by the test instead of deleting them when the test completes. This is useful for manual debugging, e.g. inspecting pods and resources with `kubectl`. |

## Evals

We dogfood our own evals to measure whether our documentation is understandable to a reader. An Inspect task feeds the documentation (`README.md` and `docs/development.md`) to a model and asks multiple-choice comprehension questions whose answers can only be found in the docs.

The eval is managed with [`uv`](https://docs.astral.sh/uv/) under the `evals/` directory.

### Setup

Install `uv` if not already installed, then install Python dependencies in `evals/`:

```bash
cd evals
uv sync
```

### Running Locally

To run the comprehension eval with a real model, set your provider API key in the environment (e.g. `GEMINI_API_KEY` for Google Gemini models). The default model across code and configuration is `google/gemini-3.5-flash-lite`:

```bash
cd evals
uv run inspect eval docs_comprehension.py@readme_comprehension --model google/gemini-3.5-flash-lite
```

(Note: `--model` can also be omitted when `GEMINI_API_KEY` is set; the task falls back to `DEFAULT_MODEL = "google/gemini-3.5-flash-lite"`.)

To run the blind control task (which omits the documents to check for pretraining leakage):

```bash
cd evals
uv run inspect eval docs_comprehension.py@readme_comprehension_blind --model google/gemini-3.5-flash-lite
```

To run offline without requiring an API key (using Inspect's built-in `mockllm/model` provider):

```bash
cd evals
uv run inspect eval docs_comprehension.py --model mockllm/model
```

### Viewing Results (`inspect view`)

Inspect includes an interactive web UI for inspecting sample logs, evaluation scores, prompts, and model responses:

```bash
cd evals
uv run inspect view
```

This launches a local web server (usually at `http://localhost:7575`) displaying evaluation runs stored in `evals/logs/`.

### How to Add a Question

Questions live in `evals/datasets/readme_questions.jsonl`. Each line is a JSON record with the following schema:

```json
{
  "input": "Which command runs unit tests in the repository?",
  "choices": [
    "go test ./...",
    "npm test",
    "cargo test",
    "The documents do not state this."
  ],
  "target": "A",
  "source": "docs/development.md: Running Unit Tests",
  "evidence": "go test ./..."
}
```

Guidelines for writing questions:
- **`input`**: A clear question testing comprehension of facts stated in `README.md` or `docs/development.md`.
- **`choices`**: Exactly 4 options (`A`, `B`, `C`, `D`).
- **`target`**: The letter of the correct answer (`A`, `B`, `C`, or `D`).
- **`source`**: The document and section where the answer is found (e.g., `README.md: Core Concepts`, `docs/development.md: Environment Variables`).
- **`evidence`**: A short, non-empty substring from the injected documentation that must be present (verified by `test-evals` in CI as a drift guard).
- **`absent`**: An optional list of non-empty substrings that must NOT appear in the injected documentation (verified by `test-evals` in CI to ensure negative controls do not break when previously omitted facts are added to the documentation).
- **Guard against leakage**: Ask about repo-specific details (names of tools, commands, environment variables, verdicts) rather than general Kubernetes or software engineering facts.
- **Negative controls ("Not stated")**: Include questions about plausible facts that are intentionally omitted from our documentation, with the correct target being "The documents do not state this." (or similar). This punishes models that guess confidently.

