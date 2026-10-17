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

FROM python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1

RUN apt-get update && \
    apt-get install -y --no-install-recommends git ca-certificates && \
    rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir \
    inspect-ai==0.3.281 \
    google-genai==2.31.0 \
    openai==3.33.0 \
    anthropic==1.15.0

RUN groupadd -g 1000 sandbox && \
    useradd -u 1000 -g 1000 -m -s /bin/bash sandbox

RUN mkdir -p /workspace && ln -s /workspace/out /out && chown -R 1000:1000 /workspace /out

USER 1000:1000
WORKDIR /workspace
