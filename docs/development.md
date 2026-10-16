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

This document describes how to set up your local development environment and run tests for `eval-runner`.

## Prerequisites

- **Go**: 1.27 or newer
- **Docker**: Docker daemon running (locally or via `DOCKER_HOST`)
- **Git**

Tools like `kind` and `kubectl` do not need to be installed beforehand; the test scripts can download and manage them automatically.

## Project Automation (`ap`)

This repository uses [`ap` (autoproject)](https://github.com/gke-labs/gke-labs-infra/blob/main/ap/llms.txt) for project automation and CI generation.

Common commands:

- `ap generate`: Formats code, stamps copyright headers, updates CI presubmit scripts (`dev/ci/presubmits/`), and regenerates `.github/workflows/ci-presubmits.yaml`.
- `ap lint`: Runs Go linters (`govet`, `gofmt`, etc.).
- `ap test`: Runs unit tests (`go test`).
- `ap build`: Builds container images defined under `images/`.
- `ap e2e`: Discovers and executes all end-to-end tests.

## Running Unit Tests

Run all unit tests with:

```bash
go test ./...
```

E2E tests will automatically be skipped unless `RUN_E2E=1` is provided.

## Running End-to-End (E2E) Tests

E2E tests live in `tests/e2e/`. They test eval-runner against a real Kubernetes cluster using `kind` and `kubernetes-sigs/agent-sandbox`.

### Option 1: Automated via `ap e2e` (Recommended)

Run:

```bash
ap e2e
```

(or `go run github.com/gke-labs/gke-labs-infra/ap@latest e2e .` or `./dev/ci/presubmits/ap-e2e`)

This provides a one-command run with no pre-installed `kind` or `kubectl`:
1. Executes `dev/tasks/test-e2e` to download and install pinned versions of `kind` and `kubectl` into `.build/bin/`.
2. Automatically discovers and executes all Go E2E tests under `tests/e2e/` with `RUN_E2E=1`.

### Option 2: Running via `go test` directly

If `kind` and `kubectl` are already installed on your system `PATH` (or after running `./dev/tasks/test-e2e` once to populate `.build/bin`):

```bash
RUN_E2E=1 go test -v -count=1 ./tests/e2e/...
```

To run a specific test:

```bash
RUN_E2E=1 go test -v -count=1 ./tests/e2e/... -run TestEvalDiscovery
```

> **Note on Kubeconfig Context:** When running tests against a cluster, the harness switches the active kubectl context using `kubectl config use-context kind-<clusterName>`, which updates the current context in your kubeconfig (`~/.kube/config`).

## Environment Variables

The E2E test harness recognizes several environment variables:

| Variable | Default | Description |
|---|---|---|
| `RUN_E2E` | _(unset)_ | When set to `1` (or any non-empty value), enables running the E2E test suite. When unset, E2E tests are skipped. |
| `KIND_CLUSTER_NAME` | `kind` | Name of the kind cluster to create or use. If a cluster with this name already exists, the test reuses it without re-creating it or deleting it afterwards. |
| `SKIP_CLEANUP` | _(unset)_ | When set to `1` (or any non-empty value), preserves the kind cluster and test namespaces created by the test instead of deleting them when the test completes. This is useful for manual debugging, e.g. inspecting pods and resources with `kubectl`. |
