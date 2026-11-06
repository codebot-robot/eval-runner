#!/bin/bash
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

set -o errexit
set -o nounset
set -o pipefail

unset GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM || true

PINNED_COMMIT="7e917f35c281188532772312d4ad91ca9274febc"
REPOSITORY_URL="https://github.com/laude-institute/terminal-bench-2-0-sample.git"

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tasks_directory="${script_directory}/tasks"

temporary_directory="$(mktemp -d)"
trap 'rm -rf "${temporary_directory}"' EXIT

git clone --quiet "${REPOSITORY_URL}" "${temporary_directory}/checkout"
git -C "${temporary_directory}/checkout" checkout --quiet "${PINNED_COMMIT}"

mkdir -p "${tasks_directory}"
printf "%s\n" "${PINNED_COMMIT}" > "${tasks_directory}/PINNED_COMMIT"

for sample_task_directory in "${temporary_directory}/checkout/sample"/*; do
  [ -d "${sample_task_directory}" ] || continue
  task_name="$(basename "${sample_task_directory}")"
  destination_task_directory="${tasks_directory}/${task_name}"

  rm -rf "${destination_task_directory}"
  mkdir -p "${destination_task_directory}"

  cp "${sample_task_directory}/task.toml" "${destination_task_directory}/task.toml"
  cp -r "${sample_task_directory}/solution" "${destination_task_directory}/solution"
  cp -r "${sample_task_directory}/tests" "${destination_task_directory}/tests"

  working_directory="$(awk '/^WORKDIR/ { workdir = $2 } END { print workdir }' "${sample_task_directory}/environment/Dockerfile")"
  if [ -z "${working_directory}" ]; then
    working_directory="/"
  fi
  printf "%s\n" "${working_directory}" > "${destination_task_directory}/WORKDIR"
done

echo "Vendored sample tasks at pinned commit ${PINNED_COMMIT} into ${tasks_directory}"
