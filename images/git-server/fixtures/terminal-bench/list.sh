#!/bin/sh
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

set -e
script_dir="$(cd "$(dirname "$0")" && pwd)"
extra_file="${script_dir}/agent-tasks.json"
if [ ! -f "${extra_file}" ]; then
  echo "missing required fixture file: ${extra_file}" >&2
  exit 1
fi

base_output="$(examples/terminal-bench/.evals/list)"
if [ -z "${base_output}" ]; then
  echo "inner list script examples/terminal-bench/.evals/list produced empty output" >&2
  exit 1
fi

printf '%s\n' "${base_output}" | awk -v extra_file="${extra_file}" '
  BEGIN { inserted = 0 }
  /^[[:space:]]*][[:space:]]*$/ {
    while ((getline line < extra_file) > 0) {
      print line
      inserted = 1
    }
    close(extra_file)
  }
  { print }
  END {
    if (!inserted) {
      print "failed to insert extra tasks from " extra_file > "/dev/stderr"
      exit 1
    }
  }
'
