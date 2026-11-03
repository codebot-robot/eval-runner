#!/usr/bin/env bash
# Adapted from kubernetes-sigs/agent-sandbox examples/harbor-terminal-bench (Apache-2.0)

set -euo pipefail

script_directory=$(cd "$(dirname "$0")" && pwd)
example_root=$(cd "${script_directory}/.." && pwd)
namespace="${NAMESPACE:-eval-runner-sandbox}"

kubectl_command=(kubectl)
if [[ -n "${KUBE_CONTEXT:-}" ]]; then
  kubectl_command+=(--context "${KUBE_CONTEXT}")
fi

"${kubectl_command[@]}" apply -f "${example_root}/deploy/rbac.yaml"

job_name="sample-$(date +%Y%m%d-%H%M%S)-$$"
jobs_directory="jobs/terminal-bench"

arguments=("$@")
for ((index = 0; index < ${#arguments[@]}; index++)); do
  case "${arguments[$index]}" in
    --job-name)
      job_name="${arguments[$((index + 1))]:-$job_name}"
      ;;
    --job-name=*)
      job_name="${arguments[$index]#--job-name=}"
      ;;
    --jobs-dir|-o)
      jobs_directory="${arguments[$((index + 1))]:-$jobs_directory}"
      ;;
    --jobs-dir=*)
      jobs_directory="${arguments[$index]#--jobs-dir=}"
      ;;
    -o?*)
      jobs_directory="${arguments[$index]#-o}"
      ;;
  esac
done

echo "== harbor run: terminal-bench-sample@2.0, oracle agent, namespace ${namespace}, job ${job_name}"

harbor_command=(harbor run -c "${example_root}/configs/sample.yaml")
harbor_command+=(--job-name "${job_name}")
harbor_command+=(--jobs-dir "${jobs_directory}")
harbor_command+=(--ek "namespace=${namespace}")
if [[ -n "${KUBE_CONTEXT:-}" ]]; then
  harbor_command+=(--ek "kube_context=${KUBE_CONTEXT}")
fi
if [[ $# -gt 0 ]]; then
  harbor_command+=("$@")
fi

"${harbor_command[@]}"

echo "== leftovers in ${namespace} (expect none):"
"${kubectl_command[@]}" -n "${namespace}" get sandboxclaims 2>/dev/null || true

echo "== report path:"
echo "${jobs_directory}/${job_name}"
