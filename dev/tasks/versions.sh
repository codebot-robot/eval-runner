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

# Pinned versions and commit SHAs for upstream dependencies.
# To bump:
# - AGENT_SANDBOX_VERSION: Check latest release at https://github.com/kubernetes-sigs/agent-sandbox/releases
# - SANDBOXD_COMMIT: Pinned commit in https://github.com/codebot-robot/agent-sandbox
# - SANDBOXD_TAG: Short SHA or tag corresponding to SANDBOXD_COMMIT.
#   After bumping, rebuild and push the image via dev/tasks/sandboxd-image --push.
AGENT_SANDBOX_VERSION="v1.0.7"
SANDBOXD_COMMIT="0d9e0d2e62d1aff4ce6849b5d0433a672ec82db7"
SANDBOXD_TAG="0d9e0d2"
