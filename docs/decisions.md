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

# Architecture Decision Records (ADRs)

This document records the architectural and design decisions made for `eval-runner`, along with the context and rationale for each decision.

Future pull requests that introduce an architectural decision should append an entry here.

---

### ADR-001: Discovery is code, not config (`.evals/list`)
- **Date**: 2026-10-16
- **Status**: Accepted
- **Decision**: Eval tasks are discovered by executing an executable entrypoint at the repository root (`.evals/list`) inside an untrusted sandbox at the target commit, which outputs a JSON manifest to stdout.
- **Rationale**: Keeps discovery reproducible, isolated, and format-agnostic. The runner does not need language- or framework-specific AST parsers in the controller, and repositories can dynamically generate task lists or integrate any evaluation framework.

### ADR-002: API group is `evalrunner.gke-labs.dev` under `v1alpha1`
- **Date**: 2026-10-16
- **Status**: Accepted
- **Decision**: Define the Custom Resource Definition group as `evalrunner.gke-labs.dev` with version `v1alpha1`, introducing the `Run` custom resource.
- **Rationale**: Domain-namespaced under the project's GitHub organization (`gke-labs`), reserving clean namespace room for future API evolution while marking current types explicitly as alpha.

### ADR-003: Repository commit must be an exact hex SHA
- **Date**: 2026-10-16
- **Status**: Accepted
- **Decision**: `spec.repo.commit` is required and strictly validated by regex `^[0-9a-fA-F]{7,64}$` in CRD OpenAPI schema validation.
- **Rationale**: Branch and tag names are mutable and lead to non-deterministic eval runs. Requiring an exact immutable commit SHA ensures that runs are completely reproducible and that PR head and base comparisons evaluate exact, frozen states.

### ADR-004: Separate namespaces for controller and sandboxes
- **Date**: 2026-10-16
- **Status**: Accepted
- **Decision**: Run the controller and user `Run` resources in `eval-runner`, while spawning all untrusted `Sandbox` resources and pods in `eval-runner-sandbox`.
- **Rationale**: Strict isolation between the control plane and untrusted user workloads. Prevents untrusted sandbox code from discovering or interfering with controller pods or secrets, scopes controller informers and caches, and allows independent Pod Security Admission enforcement.

### ADR-005: Pin `agent-sandbox` release (`v1.0.7`) as external prerequisite
- **Date**: 2026-10-16
- **Status**: Accepted
- **Decision**: Require `sigs.k8s.io/agent-sandbox` release `v1.0.7` to be installed on the cluster as a prerequisite rather than bundling or sub-managing the sandbox controller.
- **Rationale**: Leverages standard Kubernetes SIGs infrastructure for agent sandboxing while avoiding the complexity and maintenance burden of managing custom sandbox lifecycle controllers. The controller validates that the `Sandbox` CRD is present at startup and fails fast if missing.

### ADR-006: Finalizer-based sandbox cleanup instead of `ownerReferences`
- **Date**: 2026-10-16
- **Status**: Accepted
- **Decision**: Attach a finalizer (`evalrunner.gke-labs.dev/discovery-sandbox`) to the `Run` to ensure discovery sandboxes in `eval-runner-sandbox` are deleted when a `Run` is deleted.
- **Rationale**: Kubernetes `ownerReferences` cannot cross namespace boundaries (`Run` in `eval-runner` cannot own `Sandbox` in `eval-runner-sandbox`). A finalizer prevents leaking orphaned sandbox pods if a user or CI deletes a Run mid-discovery.

### ADR-007: Strict status size discipline and exec stream limits
- **Date**: 2026-10-16
- **Status**: Accepted
- **Decision**: Impose strict size limits on sandbox exec streams (1 MiB stdout, 64 KiB stderr), manifest tasks (max 1,000), error message length (4,096 B), and stderr tail retention (10 lines / 3 KiB).
- **Rationale**: Untrusted repository entrypoints could emit megabytes of data, causing controller memory exhaustion or status write rejections against etcd's ~1.5 MiB request limit. Today `status.manifest` holds the full discovered manifest (bounded by `MaxTasks = 1000`). Enforcing limits at the stream level ensures cluster stability, and per-sample execution data will stay out of status once sample runs are implemented (#26).

### ADR-008: Project automation, manifests, and CI via `ap` (autoproject)
- **Date**: 2026-10-16
- **Status**: Accepted
- **Decision**: Standardize repository workflows (formatting, copyright header verification, CRD code generation, image builds, deployment, and presubmit CI generation) on `ap`.
- **Rationale**: Eliminates ad-hoc Makefiles, ensures identical presubmit execution between local developers and GitHub Actions, and prevents CI configuration drift.

### ADR-009: Google Gemini (`google/gemini-3.5-flash-lite`) as default evaluation model
- **Date**: 2026-10-16
- **Status**: Accepted
- **Decision**: Standardize on `google/gemini-3.5-flash-lite` as the recommended default model when running Inspect evaluation tasks with live model providers.
- **Rationale**: Provides fast latency and cost efficiency suited for frequent CI and evaluation workflows while demonstrating strong reasoning accuracy on comprehension and benchmarking tasks.

### ADR-010: Paired blind control task for documentation comprehension eval
- **Date**: 2026-10-16
- **Status**: Accepted
- **Decision**: Run a paired blind evaluation task (`docs/readme_comprehension_blind`) alongside the full comprehension task (`docs/readme_comprehension`).
- **Rationale**: Evaluates the identical question set without providing repository documentation, establishing an empirical baseline for pretraining data leakage versus genuine comprehension of the provided text.

### ADR-011: Namespaced-only RBAC and least-privilege informers
- **Date**: 2026-10-16
- **Status**: Accepted
- **Decision**: Avoid cluster-scoped RBAC (`ClusterRole`); grant controller permissions strictly through namespaced `Role` and `RoleBinding` in `eval-runner` and `eval-runner-sandbox`, and scope manager cache informers per object to their respective namespaces.
- **Rationale**: Enforces least privilege. The controller cannot read or manipulate pods, secrets, or workloads in any other cluster namespace, and cache informers do not watch cluster-wide events.
