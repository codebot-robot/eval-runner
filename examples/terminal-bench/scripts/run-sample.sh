#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Smoke test: runs the oracle agent on the Terminal-Bench 2.0 sample dataset.
# Verifies the cluster namespace, root execution, container image pulls,
# network egress, Domain Name System resolution, and pod execution path.
#
# Usage:
#   examples/terminal-bench/scripts/run-sample.sh [extra harbor run arguments]
set -euo pipefail

script_directory=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
namespace="${NAMESPACE:-terminal-bench}"
context_flags=()
if [[ -n "${KUBE_CONTEXT:-}" ]]; then
  context_flags=(--context "${KUBE_CONTEXT}")
fi

command -v harbor >/dev/null 2>&1 || {
  echo "harbor command is not available on PATH. Please install harbor with: uv tool install harbor"
  exit 1
}

python3 -c "import k8s_agent_sandbox_harbor" 2>/dev/null || {
  echo "k8s_agent_sandbox_harbor is not installed in the active Python environment."
  echo "Please install the Agent Sandbox Harbor environment at pinned reference 75dd288fd251d22230a90d22ca0ae3523560703e:"
  echo "  pip install 'git+https://github.com/kubernetes-sigs/agent-sandbox.git@75dd288fd251d22230a90d22ca0ae3523560703e#subdirectory=clients/python/agentic-sandbox-client[async]'"
  echo "  pip install 'git+https://github.com/kubernetes-sigs/agent-sandbox.git@75dd288fd251d22230a90d22ca0ae3523560703e#subdirectory=clients/integrations/harbor'"
  exit 1
}

kubectl "${context_flags[@]}" get customresourcedefinitions sandboxclaims.extensions.agents.x-k8s.io >/dev/null 2>&1 || {
  echo "The Kubernetes cluster does not serve the Agent Sandbox extension CustomResourceDefinitions."
  exit 1
}

echo "Applying namespace and Role-Based Access Control configuration..."
kubectl "${context_flags[@]}" apply \
  -f "${script_directory}/deploy/namespace.yaml" \
  -f "${script_directory}/deploy/rbac.yaml"

if [[ "${namespace}" != "terminal-bench" ]]; then
  kubectl "${context_flags[@]}" get namespace "${namespace}" >/dev/null 2>&1 || {
    kubectl "${context_flags[@]}" create namespace "${namespace}"
  }
fi

extra_arguments=()
if [[ -n "${KUBE_CONTEXT:-}" ]]; then
  extra_arguments+=(--ek "kube_context=${KUBE_CONTEXT}")
fi

job_name="sample-$(date +%Y%m%d-%H%M%S)-$$"
jobs_directory="jobs/terminal-bench"
arguments=("$@")
for ((index = 0; index < ${#arguments[@]}; index++)); do
  case "${arguments[$index]}" in
    --job-name)
      job_name="${arguments[$((index + 1))]:-${job_name}}"
      ;;
    --job-name=*)
      job_name="${arguments[$index]#--job-name=}"
      ;;
    --jobs-dir)
      jobs_directory="${arguments[$((index + 1))]:-${jobs_directory}}"
      ;;
    --jobs-dir=*)
      jobs_directory="${arguments[$index]#--jobs-dir=}"
      ;;
  esac
done

echo "== Executing Harbor run: dataset terminal-bench-sample@2.0, oracle agent, namespace ${namespace}, job ${job_name}"
harbor run \
  -c "${script_directory}/configs/sample.yaml" \
  --job-name "${job_name}" \
  --ek "namespace=${namespace}" \
  "${extra_arguments[@]}" \
  "$@"

echo "== Checking for leftover sandbox claims in namespace ${namespace} (expecting none):"
kubectl "${context_flags[@]}" --namespace "${namespace}" get sandboxclaims 2>/dev/null || true

echo "== Report path:"
echo "${jobs_directory}/${job_name}"
