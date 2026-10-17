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

FROM alpine:3.21
RUN apk add --no-cache git git-daemon
WORKDIR /work
COPY examples/support-agent/ ./
RUN git config --global user.email "test@example.com" && \
    git config --global user.name "Test" && \
    git init -b main && \
    git add . && \
    git commit -m "initial commit" && \
    git clone --bare /work /git/support-agent.git && \
    touch /git/support-agent.git/git-daemon-export-ok && \
    git rm -r .evals && \
    git commit -m "remove evals list" && \
    git clone --bare /work /git/no-evals.git && \
    touch /git/no-evals.git/git-daemon-export-ok && \
    rm -rf /work

WORKDIR /docs-work
COPY evals/ evals/
COPY .evals/ .evals/
COPY README.md README.md
COPY docs/ docs/
RUN git config --global user.email "test@example.com" && \
    git config --global user.name "Test" && \
    git init -b main && \
    git add . && \
    git commit -m "initial commit" && \
    git clone --bare /docs-work /git/docs-eval.git && \
    touch /git/docs-eval.git/git-daemon-export-ok && \
    chown -R 65534:65534 /git && \
    git config --system --add safe.directory '*' && \
    rm -rf /docs-work
USER 65534:65534
EXPOSE 9418
CMD ["git", "daemon", "--reuseaddr", "--base-path=/git", "--export-all", "--verbose", "/git"]
