#!/usr/bin/env bash
# Adapted from kubernetes-sigs/agent-sandbox examples/harbor-terminal-bench (Apache-2.0)

set -euo pipefail

namespace="${NAMESPACE:-eval-runner-sandbox}"
resource_types="sandboxclaims,sandboxwarmpools,sandboxtemplates"

for argument in "$@"; do
  case "${argument}" in
    --claims)
      resource_types="sandboxclaims"
      ;;
    --all)
      resource_types="sandboxclaims,sandboxwarmpools,sandboxtemplates"
      ;;
    -*)
      echo "Unknown flag: ${argument}" >&2
      exit 1
      ;;
    *)
      namespace="${argument}"
      ;;
  esac
done

kubectl_command=(kubectl)
if [[ -n "${KUBE_CONTEXT:-}" ]]; then
  kubectl_command+=(--context "${KUBE_CONTEXT}")
fi

"${kubectl_command[@]}" -n "${namespace}" delete "${resource_types}" \
  -l harbor.io/managed-by=k8s-agent-sandbox-harbor --ignore-not-found
