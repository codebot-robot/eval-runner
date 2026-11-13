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

FROM --platform=$BUILDPLATFORM golang:1.27 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG SERVICE_PORTALS_COMMIT=1d6a9416d9ca1be59fdf45b9c78e5d1ee9fbaa03

RUN git clone https://github.com/gke-labs/service-portals.git /src && \
    cd /src && \
    git checkout ${SERVICE_PORTALS_COMMIT}

WORKDIR /src
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -o /portal-nri-plugin ./cmd/portal-nri-plugin

FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends iptables iproute2 util-linux && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /portal-nri-plugin /usr/local/bin/portal-nri-plugin

ENTRYPOINT ["/usr/local/bin/portal-nri-plugin"]
