# Terminal-Bench 2.0 Example

This example demonstrates running Terminal-Bench 2.0 evaluation tasks as sandbox tasks within `eval-runner`.

Each task executes in its dedicated container sandbox using the prebuilt task image from `ghcr.io/laude-institute/terminal-bench/<name>:2.0` with the repository checked out at the Run's commit. The sandbox task executes as the container image's user (root for Terminal-Bench images) under Kubernetes baseline Pod Security Admission, bounded by per-task `timeoutSeconds`. The default sandbox task container resource limits (500m CPU, 512Mi memory) suit lightweight tasks like `regex-log`, but heavier tasks like `qemu-alpine-ssh`, `qemu-startup`, or `sqlite-with-gcov` require larger CPU and memory allocations.

Note: Terminal-Bench 2.0 container images are prebuilt for `linux/amd64` only. Cluster worker nodes must run amd64 architecture.

## Running on a Cluster

To evaluate the `terminal-bench/regex-log` task against this repository on your cluster, specify a valid 7–64 character git commit hash for `spec.repo.commit`:

```yaml
apiVersion: evalrunner.gke-labs.dev/v1alpha1
kind: Run
metadata:
  name: terminal-bench-regex-log-run
  namespace: eval-runner
spec:
  repo:
    url: https://github.com/gke-labs/eval-runner.git
    commit: 0123456789abcdef0123456789abcdef01234567
  tasks:
  - terminal-bench/regex-log
```

Apply the Run manifest:

```bash
kubectl apply -f run.yaml
```

Inspect progress and outcomes using `kubectl`:

```bash
kubectl get run terminal-bench-regex-log-run -n eval-runner -o yaml
```

## E2E Testing Notes

In `images/git-server/Dockerfile`, `sed` removes the `terminal-bench` entry from the `docs-eval.git` fixture's `.evals/list` so existing comprehension and comparison tests remain focused solely on documentation tasks. The `terminal-bench.git` e2e test fixture repository duplicates `examples/terminal-bench/` at both the repository root (to serve `.evals/list` during discovery) and nested under `examples/terminal-bench/` (to match the task command path).
