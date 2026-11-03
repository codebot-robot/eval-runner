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

# Removes sandbox claims, sandbox warm pools, and sandbox templates created by
# Harbor, selected by the harbor.io/managed-by label.
#
# Usage:
#   examples/terminal-bench/scripts/cleanup.sh [namespace]
set -euo pipefail

namespace="${1:-${NAMESPACE:-terminal-bench}}"
context_flags=()
if [[ -n "${KUBE_CONTEXT:-}" ]]; then
  context_flags=(--context "${KUBE_CONTEXT}")
fi

echo "Removing Harbor sandbox claims in namespace ${namespace}..."
kubectl "${context_flags[@]}" delete sandboxclaims \
  --namespace "${namespace}" \
  --selector "harbor.io/managed-by" \
  --ignore-not-found

echo "Removing Harbor sandbox warm pools in namespace ${namespace}..."
kubectl "${context_flags[@]}" delete sandboxwarmpools \
  --namespace "${namespace}" \
  --selector "harbor.io/managed-by" \
  --ignore-not-found

echo "Removing Harbor sandbox templates in namespace ${namespace}..."
kubectl "${context_flags[@]}" delete sandboxtemplates \
  --namespace "${namespace}" \
  --selector "harbor.io/managed-by" \
  --ignore-not-found
