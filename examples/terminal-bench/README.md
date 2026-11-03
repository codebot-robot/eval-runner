# Terminal-Bench 2.0 on Agent Sandbox with Harbor

This example runs [Terminal-Bench 2.0](https://github.com/laude-institute/terminal-bench-2) through the [Harbor](https://github.com/harbor-framework/harbor) evaluation framework, with every benchmark trial executing inside an Agent Sandbox `SandboxClaim`.

It is adapted from upstream [examples/harbor-terminal-bench](https://github.com/kubernetes-sigs/agent-sandbox/tree/75dd288fd251d22230a90d22ca0ae3523560703e/examples/harbor-terminal-bench) in `kubernetes-sigs/agent-sandbox` at pinned ref [`75dd288fd251d22230a90d22ca0ae3523560703e`](https://github.com/kubernetes-sigs/agent-sandbox/commit/75dd288fd251d22230a90d22ca0ae3523560703e) (agent-sandbox PR 2125; not on main yet). For environment options and lifecycle details, see [clients/integrations/harbor/README.md](https://github.com/kubernetes-sigs/agent-sandbox/blob/75dd288fd251d22230a90d22ca0ae3523560703e/clients/integrations/harbor/README.md).

Trials run in the `eval-runner-sandbox` namespace, which enforces Pod Security Admission `baseline` (#489). No dedicated namespace is required.

## Prerequisites

1. Install Harbor and the Agent Sandbox Harbor integration in one isolated tool environment:

   ```bash
   uv tool install harbor --with "git+https://github.com/kubernetes-sigs/agent-sandbox.git@75dd288fd251d22230a90d22ca0ae3523560703e#subdirectory=clients/integrations/harbor"
   ```

   `uv tool install` manages dependencies in an isolated virtual environment. Do not use a separate `pip install` or test imports with a different interpreter.

2. Access to **evalrunner-dev, a kops cluster on GCE** (kubeconfig `~/.kube/kops`).

## RBAC Setup

The runner needs least-privilege permissions in `eval-runner-sandbox` to manage `SandboxTemplate`, `SandboxWarmPool`, and `SandboxClaim` resources, read `sandboxes`, list pods, and execute commands via `pods/exec`.

1. Open `deploy/rbac.yaml` and set the `RoleBinding` subject `PLACEHOLDER_USER` to the identity your kops admin kubeconfig uses (for example, `admin`).
2. Apply the role and binding:

   ```bash
   KUBECONFIG=~/.kube/kops kubectl apply -f examples/terminal-bench/deploy/rbac.yaml
   ```

## Oracle Smoke Run

The smoke test runs the reference oracle agent against the 10-task Terminal-Bench 2.0 sample. The oracle agent executes the reference solution for each task, verifying cluster DNS, public internet egress for verifier package downloads, and container exec paths.

Run the sample script using the kops kubeconfig:

```bash
KUBECONFIG=~/.kube/kops examples/terminal-bench/scripts/run-sample.sh
```

`run-sample.sh` applies `deploy/rbac.yaml`, invokes `harbor run -c examples/terminal-bench/configs/sample.yaml`, checks for leftover claims, and outputs the report directory.

To observe resources during execution:

```bash
KUBECONFIG=~/.kube/kops kubectl -n eval-runner-sandbox get sandboxclaims,sandboxtemplates,sandboxwarmpools
KUBECONFIG=~/.kube/kops kubectl -n eval-runner-sandbox get pods -w
```

If a run is interrupted, clean up any remaining resources:

```bash
KUBECONFIG=~/.kube/kops examples/terminal-bench/scripts/cleanup.sh
```

## Running Full Benchmark (tb2.yaml)

To evaluate an agent model on the full 89 tasks:

```bash
KUBECONFIG=~/.kube/kops harbor run -c examples/terminal-bench/configs/tb2.yaml -m <model>
```

If using a private Artifact Registry mirror for task images, supply `--ek image_mirror=<registry-prefix>`.

## Local Testing on kind

To run against a local `kind` cluster, clone `kubernetes-sigs/agent-sandbox` at ref `75dd288fd251d22230a90d22ca0ae3523560703e`:

```bash
# From a checkout of kubernetes-sigs/agent-sandbox at 75dd288fd251d22230a90d22ca0ae3523560703e
EXTENSIONS=true make deploy-kind
export KUBECONFIG=$PWD/bin/KUBECONFIG
```

Note: Terminal-Bench task images are `linux/amd64` only. On Apple Silicon, kind runs them under emulation: this works for a plumbing check, but QEMU nested virtualization tasks will fail and `su` may misbehave. Use `evalrunner-dev` on GCE for benchmark scores.

## Reading the Report and Expected Outcome

Harbor writes trial artifacts and `result.json` files to the output directory (default: `jobs/terminal-bench/<job-name>`).

- `task` and `trial`: Benchmark task name and trial identifier.
- `reward`: Score (1.0 indicates pass; 0 indicates fail).
- `outcome`: `pass`, `fail`, or `error: <type>`.
- `env setup s`: Time spent creating templates, pools, waiting for claims to become Ready, and probing exec readiness.
- `agent s` and `verifier s`: Execution time spent by the agent and the verifier.

An `error` row reflects an infrastructure or harness exception, while a zero reward represents a task-side evaluation failure.

### Expected Outcome

Upstream recorded 7 of 10 tasks passing with 0 infrastructure errors on the sample. The three zero-reward outcomes are task-side failures:

1. `qemu-startup` and `qemu-alpine-ssh`: The agent boots the VM, but the verifier's `apt-get install curl` encounters HTTP 404 errors from `deb.debian.org` for `bullseye-security` packages, preventing `uv` and pytest from installing. (On emulated kind nodes, QEMU also lacks nested virtualization).
2. `build-cython-ext`: 10 of 11 verifier tests pass, but the final test fails within the task image repository.

## Troubleshooting

- **Verifier network failures (`Could not resolve host` or package download errors)**: The task verifier needs cluster DNS on port 53 and public egress to download test dependencies via `curl` and `uv`. Confirm egress is permitted by network policies in `eval-runner-sandbox`.
- **Pod rejected by Pod Security Admission**: Ensure the pods run in `eval-runner-sandbox`, which is configured with PSA `baseline`.
- **Image pull delays**: First pulls of larger task images on fresh nodes can take several minutes; `ready_timeout_s` is defaulted to 600 seconds.
- **Leftover claims after termination**: Run `examples/terminal-bench/scripts/cleanup.sh` to remove leftover claims, pools, and templates.

## Limits

Harbor stream mode, Docker Compose tasks, GPU/TPU tasks, and custom network allowlists are not supported by the `k8s-agent-sandbox-harbor` integration.
