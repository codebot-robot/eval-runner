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

task_name="${1:-}"
mode="${2:-oracle}"

if [ -z "${task_name}" ]; then
  echo "Usage: run-task.sh <task-name> [oracle|agent]" >&2
  exit 1
fi

LOGS_DIRECTORY="${LOGS_DIRECTORY:-/logs}"
OUT_DIRECTORY="${OUT_DIRECTORY:-/out}"
SOLUTION_DIRECTORY="${SOLUTION_DIRECTORY:-/solution}"
TESTS_DIRECTORY="${TESTS_DIRECTORY:-/tests}"

# Locate the task directory
if [ -d "examples/terminal-bench/tasks/${task_name}" ]; then
  task_directory="examples/terminal-bench/tasks/${task_name}"
elif [ -d "tasks/${task_name}" ]; then
  task_directory="tasks/${task_name}"
else
  script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  task_directory="${script_directory}/tasks/${task_name}"
fi

mkdir -p "${LOGS_DIRECTORY}/verifier" "${OUT_DIRECTORY}/logs"

if [ ! -d "${task_directory}" ]; then
  cat <<EOF > "${OUT_DIRECTORY}/results.json"
{
  "schemaVersion": 1,
  "samples": [
    {
      "id": "${task_name}",
      "passed": false,
      "score": 0.0,
      "message": "task directory not found: ${task_name}"
    }
  ]
}
EOF
  exit 0
fi

# Copy vendored solution and tests into place
mkdir -p "${SOLUTION_DIRECTORY}" "${TESTS_DIRECTORY}"
if [ -d "${task_directory}/solution" ]; then
  rm -rf "${SOLUTION_DIRECTORY:?}"/*
  cp -a "${task_directory}/solution/." "${SOLUTION_DIRECTORY}/"
  chmod -R +x "${SOLUTION_DIRECTORY}" 2>/dev/null || true
fi

if [ -d "${task_directory}/tests" ]; then
  rm -rf "${TESTS_DIRECTORY:?}"/*
  cp -a "${task_directory}/tests/." "${TESTS_DIRECTORY}/"
  chmod -R +x "${TESTS_DIRECTORY}" 2>/dev/null || true
fi

# Change directory to the task's recorded WORKDIR
working_directory="/"
if [ -f "${task_directory}/WORKDIR" ]; then
  working_directory="$(cat "${task_directory}/WORKDIR" | tr -d '[:space:]')"
fi
if [ -n "${working_directory}" ]; then
  mkdir -p "${working_directory}"
  cd "${working_directory}"
fi

# Run solve.sh in oracle mode
if [ "${mode}" = "oracle" ] && [ -f "${SOLUTION_DIRECTORY}/solve.sh" ]; then
  bash "${SOLUTION_DIRECTORY}/solve.sh" > "${OUT_DIRECTORY}/logs/solve.log" 2>&1 || true
fi

# Run verifier test.sh
verifier_log="${OUT_DIRECTORY}/logs/verifier.log"
if [ -f "${TESTS_DIRECTORY}/test.sh" ]; then
  bash "${TESTS_DIRECTORY}/test.sh" > "${verifier_log}" 2>&1 || true
else
  printf "test script not found at %s/test.sh\n" "${TESTS_DIRECTORY}" > "${verifier_log}"
fi

# Read reward and construct result sample
reward_file="${LOGS_DIRECTORY}/verifier/reward.txt"
passed="false"
score="0.0"
message=""

if [ ! -f "${reward_file}" ]; then
  message="verifier did not write reward.txt: $(tail -n 20 "${verifier_log}" 2>/dev/null)"
else
  raw_reward="$(cat "${reward_file}" 2>/dev/null | tr -d '[:space:]')"
  if [[ ! "${raw_reward}" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
    message="non-numeric reward '${raw_reward}': $(tail -n 20 "${verifier_log}" 2>/dev/null)"
  elif [ "$(awk -v r="${raw_reward}" 'BEGIN { if (r + 0 == 1) print 1; else print 0 }')" = "1" ]; then
    passed="true"
    score="1.0"
  else
    score="$(awk -v r="${raw_reward}" 'BEGIN { printf "%.2f", r + 0 }')"
    message="$(tail -n 20 "${verifier_log}" 2>/dev/null)"
  fi
fi

escaped_message="$(printf '%s' "${message}" | awk '{
  gsub(/[\001-\010\013\014\016-\037\177]/, "");
  gsub(/\\/, "\\\\");
  gsub(/"/, "\\\"");
  gsub(/\r/, "\\r");
  gsub(/\t/, "\\t");
  if (NR > 1) {
    printf("\\n");
  }
  printf("%s", $0);
}')"

cat <<EOF > "${OUT_DIRECTORY}/results.json"
{
  "schemaVersion": 1,
  "samples": [
    {
      "id": "${task_name}",
      "passed": ${passed},
      "score": ${score},
      "message": "${escaped_message}"
    }
  ]
}
EOF
