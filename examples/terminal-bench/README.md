# Terminal-Bench 2.0 on Agent Sandbox with Harbor

This example demonstrates running [Terminal-Bench 2.0](https://github.com/laude-institute/terminal-bench-2) through the [Harbor](https://github.com/harbor-framework/harbor) evaluation framework inside Kubernetes sandboxes using Agent Sandbox. It is adapted from upstream `kubernetes-sigs/agent-sandbox` pull request 2126.

This slice is deliberately manual and controller-free: it verifies the cluster configuration—a root-capable namespace, container image pulls from GitHub Container Registry (`ghcr.io`), Domain Name System resolution and public network egress for verifiers, and the execution path—before implementing controller automation. Over time, these benchmark evaluations will transition to dedicated repositories; for now, they reside under `examples/terminal-bench/`.

Our existing `eval-runner-sandbox` namespace enforces Pod Security Admission `restricted` and must not be loosened. Terminal-Bench tasks execute as root to install packages and configure environments, so this example deploys into a dedicated `terminal-bench` namespace configured with Pod Security Admission `baseline`.

## Architecture and Execution Lifecycle

During an evaluation run, Harbor coordinates trial execution from the local machine or runner through the `k8s_agent_sandbox_harbor` environment:

1. **Preflight**: Harbor verifies that the target cluster serves the Agent Sandbox extension CustomResourceDefinitions (`extensions.agents.x-k8s.io`).
2. **Environment Startup**: The environment generates a pod specification for the task container image, creates matching `SandboxTemplate` and `SandboxWarmPool` resources (reused across trials sharing the same image), and submits a `SandboxClaim`.
3. **Execution**: Harbor sets up standard directories (`/logs/agent`, `/logs/verifier`, `/logs/artifacts`, `/tests`, `/solution`) and transfers solution or agent files. Commands are executed inside the container through Kubernetes pod execution websockets.
4. **Verification**: Harbor transfers test scripts to `/tests` and runs the verification suite. Verifier scripts install dependencies using `curl` and `uv` and run `pytest`, requiring Domain Name System resolution and outbound internet connectivity.
5. **Teardown**: Harbor deletes the `SandboxClaim`, terminating the backing sandbox pod.

## Prerequisites

- **Python**: Version 3.12 or newer.
- **Harbor**: Install Harbor using `uv`:
  ```bash
  uv tool install harbor
  ```
- **Agent Sandbox Harbor Environment**: Install the integration package from `kubernetes-sigs/agent-sandbox` at the pinned reference (pull request 2125, head commit `75dd288fd251d22230a90d22ca0ae3523560703e`):
  ```bash
  pip install "git+https://github.com/kubernetes-sigs/agent-sandbox.git@75dd288fd251d22230a90d22ca0ae3523560703e#subdirectory=clients/python/agentic-sandbox-client[async]"
  pip install "git+https://github.com/kubernetes-sigs/agent-sandbox.git@75dd288fd251d22230a90d22ca0ae3523560703e#subdirectory=clients/integrations/harbor"
  ```
- **Cluster Access**: A valid `kubeconfig` pointing to either our development cluster (`evalrunner-dev` on Google Kubernetes Engine) or a local `kind` development cluster.

## Walk-Through on kind

To run the smoke test locally on a `kind` cluster:

1. Create a `kind` cluster with Agent Sandbox and extension controllers installed:
   ```bash
   EXTENSIONS=true make deploy-kind
   export KUBECONFIG="${PWD}/bin/KUBECONFIG"
   ```
2. Execute the smoke test script:
   ```bash
   examples/terminal-bench/scripts/run-sample.sh
   ```

The script ensures the `terminal-bench` namespace and Role-Based Access Control role are applied, runs the oracle agent across the 10-task sample dataset, checks for leftover sandbox claims, and prints the report directory path.

## Walk-Through on Google Kubernetes Engine

To run against our development cluster (`evalrunner-dev`):

1. **Cluster Setup**: Ensure the `evalrunner-dev` cluster has Agent Sandbox and extension controllers installed. For faster container startup, enable Image Streaming on the node pool.
2. **Namespace and Role-Based Access Control**:
   Apply the manifests:
   ```bash
   kubectl apply -f examples/terminal-bench/deploy/namespace.yaml \
     -f examples/terminal-bench/deploy/rbac.yaml
   ```
   Bind the runner role to your user or service account identity:
   ```bash
   kubectl --namespace terminal-bench create rolebinding harbor-runner \
     --role=harbor-runner \
     --user="$(gcloud config get-value account)"
   ```
3. **Artifact Registry Image Mirror (Optional)**:
   External registries rate-limit anonymous pulls during large concurrent runs. You can mirror task container images into Google Cloud Artifact Registry and configure the mirror placeholder in `configs/sample.yaml` or `configs/tb2.yaml`:
   ```yaml
   image_mirror: "us-central1-docker.pkg.dev/<PROJECT_ID>/terminal-bench-mirror"
   ```
4. **Run the Smoke Test**:
   ```bash
   examples/terminal-bench/scripts/run-sample.sh
   ```
5. **Run the Full Benchmark with a Model**:
   ```bash
   harbor run -c examples/terminal-bench/configs/tb2.yaml \
     --model anthropic/claude-sonnet-4-5 \
     --ek image_mirror=us-central1-docker.pkg.dev/<PROJECT_ID>/terminal-bench-mirror
   ```

## Reading the Report and Expected Outcome

The report path is printed upon completion (for example, `jobs/terminal-bench/<job_name>`). Each trial directory contains `result.json` with reward scores, timing details, and execution logs.

### Expected Outcome

On the 10-task sample dataset (`terminal-bench-sample@2.0`), upstream recorded 7 of 10 tasks passing with 0 infrastructure errors.

An infrastructure error indicates a cluster, network, or harness failure (such as an exception during sandbox creation or execution). In contrast, a reward of 0 with 0 infrastructure errors indicates a task-level failure rather than a cluster defect.

The three zero-reward tasks fail due to task-side issues:

- `qemu-startup`: The oracle agent boots the virtual machine successfully, but the verifier's package installation command (`apt-get install curl`) fails due to HTTP 404 errors from `deb.debian.org` package mirrors for older Debian `bullseye-security` archives. Consequently, `uv` and `pytest` cannot install.
- `qemu-alpine-ssh`: Fails for the identical Debian package mirror issue during verifier initialization.
- `build-cython-ext`: 10 of 11 verifier assertions pass; the final assertion runs the internal test suite of the target repository, which exits with status code 1 inside the container image.

Achieving 7 of 10 passing tasks with 0 infrastructure errors confirms that root execution, image pulling, Domain Name System resolution, public network egress, and pod execution are fully operational on the cluster.

## Cleanup

If an evaluation run is interrupted or cancelled, remove leftover sandbox resources using the cleanup script:

```bash
examples/terminal-bench/scripts/cleanup.sh terminal-bench
```

This script deletes all `SandboxClaim`, `SandboxWarmPool`, and `SandboxTemplate` resources labeled with `harbor.io/managed-by`.

## Troubleshooting

- **Verifier Network or Domain Name System Failures**: If verifier commands report `Could not resolve host` or package download timeouts, verify that cluster Domain Name System pods are operational and that NetworkPolicies permit egress traffic on UDP and TCP port 53 as well as public HTTPS ports.
- **Pod Security Admission Rejections**: Task images require root execution. Ensure tasks run in the `terminal-bench` namespace (`baseline` profile) and not in `eval-runner-sandbox` (`restricted` profile).
- **Image Pull Latency**: Large container images (up to 1.3 gigabytes) may take several minutes on cold nodes. Increase `ready_timeout_s` in configuration or configure an Artifact Registry mirror.
- **Leftover Resources**: Execute `examples/terminal-bench/scripts/cleanup.sh` to remove orphaned sandbox objects.

## Limits

- **Unsupported Features**: Harbor streaming mode, Docker Compose tasks, Graphics Processing Unit (GPU) acceleration, Tensor Processing Unit (TPU) acceleration, and custom network allowlists are not supported by the Agent Sandbox integration.
- **Prebuilt Images**: Tasks must specify a prebuilt container image; dynamic image builds from Dockerfiles at trial runtime are not supported.
