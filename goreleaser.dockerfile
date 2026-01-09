# Copyright 2023 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Release Dockerfile for kube-startup-cpu-boost (GoReleaser)
# Binary is built by GoReleaser and placed in root as 'manager'
# Uses distroless base for minimal runtime
FROM gcr.io/distroless/static:nonroot
WORKDIR /

# Copy pre-built binary from GoReleaser build output
# GoReleaser places the binary in the root of the build context as 'manager'
# --chmod ensures the binary is executable
COPY --chmod=755 manager /manager

USER 65532:65532

# Run the manager
ENTRYPOINT ["/manager"]
