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

PINNED_COMMIT="7e917f35c281188532772312d4ad91ca9274febc"
REPOSITORY_URL="https://github.com/laude-institute/terminal-bench-2-0-sample.git"

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tasks_json_file="${script_directory}/tasks.json"

temporary_directory="$(mktemp -d)"
trap 'rm -rf "${temporary_directory}"' EXIT

git clone --quiet "${REPOSITORY_URL}" "${temporary_directory}/checkout"
git -C "${temporary_directory}/checkout" checkout --quiet "${PINNED_COMMIT}"

python3 -c "
import glob, json, os, sys, tomllib

checkout_directory = sys.argv[1]
pinned_commit = sys.argv[2]
output_file = sys.argv[3]

tasks = []
sample_directory = os.path.join(checkout_directory, 'sample')
for task_directory in sorted(os.listdir(sample_directory)):
    task_path = os.path.join(sample_directory, task_directory)
    if not os.path.isdir(task_path):
        continue

    dockerfile_path = os.path.join(task_path, 'environment', 'Dockerfile')
    workdir = '/'
    if os.path.isfile(dockerfile_path):
        with open(dockerfile_path, 'r', encoding='utf-8') as f:
            for line in f:
                if line.startswith('WORKDIR'):
                    workdir = line.split()[1].strip()

    toml_path = os.path.join(task_path, 'task.toml')
    agent_timeout = 900
    verifier_timeout = 900
    if os.path.isfile(toml_path):
        with open(toml_path, 'rb') as f:
            toml_data = tomllib.load(f)
        agent_timeout = int(toml_data.get('agent', {}).get('timeout_sec', 900))
        verifier_timeout = int(toml_data.get('verifier', {}).get('timeout_sec', 900))

    tasks.append({
        'name': task_directory,
        'workdir': workdir,
        'agentTimeoutSeconds': agent_timeout,
        'verifierTimeoutSeconds': verifier_timeout,
    })

data = {
    'commit': pinned_commit,
    'tasks': tasks,
}

with open(output_file, 'w', encoding='utf-8') as f:
    json.dump(data, f, indent=2)
    f.write('\n')

print(f'Wrote {len(tasks)} tasks to {output_file}')
" "${temporary_directory}/checkout" "${PINNED_COMMIT}" "${tasks_json_file}"
