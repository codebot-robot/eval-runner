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

FROM golang:1.27.3 AS builder

ENV CGO_ENABLED=0
ENV GOOS=linux

# Pinned commit from dev/tasks/versions.sh (SERVICE_PORTALS_COMMIT)
ARG COMMIT=ae5f0aebfc7870bf8de8110e06b8f267c1b49dd6

RUN go install github.com/gke-labs/service-portals/cmd/service-portal@${COMMIT}

FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /go/bin/service-portal /service-portal

USER 65532:65532

ENTRYPOINT ["/service-portal"]
