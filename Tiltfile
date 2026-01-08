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

# kube-startup-cpu-boost Tiltfile
#
# This Tiltfile manages local development resources:
# - Kind cluster setup
# - Controller manager build and deployment via Helm
#
# Usage: tilt up
#
# Prerequisites:
# - Run `python3 scripts/setup_kind.py` first to create Kind cluster and registry
# - Or ensure Kind cluster 'kube-startup-cpu-boost' exists with registry on localhost:5000

# ====================
# Configuration
# ====================

# Restrict to kind cluster
allow_k8s_contexts(['kind-kube-startup-cpu-boost'])

# Configure default registry for Kind cluster
# Tilt will automatically push docker_build images to this registry
# The registry is set up by scripts/setup_kind.py
default_registry('localhost:5000')

# Suppress warning for custom_build image that uses full registry path
# We use custom_build with explicit registry path, so Tilt won't find the image name in manifests
update_settings(suppress_unused_image_warnings=["kube-startup-cpu-boost"])

# Get the directory where this Tiltfile is located
PROJECT_DIR = '.'

# ====================
# Architecture Detection
# ====================
# Detect the Kind cluster architecture to build for the correct platform
# This ensures the binary and image match the cluster architecture
# Works on both Intel (amd64) and Apple Silicon (arm64) hosts
# Uses Python script for reliable cross-platform detection
arch_result = str(local('python3 scripts/detect_kind_architecture.py')).strip()
arch_parts = arch_result.split(' ')
GO_ARCH = arch_parts[0]
DOCKER_PLATFORM = arch_parts[1] if len(arch_parts) > 1 else 'linux/amd64'

# ====================
# CRD Generation
# ====================
# Generate CRDs when CRD code changes
# This ensures CRDs are always up-to-date with the Go code
# CRDs are installed via Helm chart (included in charts/kube-startup-cpu-boost/templates/startupcpuboost-crd.yaml)
# No need to install separately - Helm will handle it
local_resource(
    'generate-crds',
    cmd='make manifests',
    deps=[
        'api/v1alpha1/startupcpuboost_types.go',
        'Makefile',
    ],
    labels=['infrastructure'],
    allow_parallel=True,
)

# ====================
# Build Manager Binary
# ====================
# Build the manager binary for the detected Kind cluster architecture
# Uses go build directly to avoid make build running manifests/generate/fmt/vet
# which could trigger rebuild loops
# Note: We don't include 'bin/' in deps to prevent watching build output
local_resource(
    'build-manager',
    cmd='GOOS=linux GOARCH=%s go build -o bin/manager cmd/main.go' % GO_ARCH,
    deps=[
        'cmd/',
        'api/',
        'internal/',
        'go.mod',
        'go.sum',
    ],
    resource_deps=['generate-crds'],  # Wait for CRDs to be generated
    labels=['controllers'],
    allow_parallel=True,
)

# ====================
# Build Docker Image
# ====================
# Build Docker image for manager
# Use local_resource for build/push, then custom_build for Tilt integration
# This ensures the build actually runs and the image is pushed to registry
BINARY_PATH = 'bin/manager'
IMAGE_NAME = 'kube-startup-cpu-boost'
REGISTRY = 'localhost:5000'
FULL_IMAGE_NAME = '%s/%s' % (REGISTRY, IMAGE_NAME)

# Step 1: Build and push Docker image using local_resource
# This ensures the build command actually executes
local_resource(
    'docker-build-and-push',
    'docker buildx build --platform %s -f Dockerfile.dev -t %s:tilt . && docker tag %s:tilt %s:tilt && docker push %s:tilt' % (
        DOCKER_PLATFORM,
        IMAGE_NAME,
        IMAGE_NAME,
        FULL_IMAGE_NAME,
        FULL_IMAGE_NAME
    ),
    deps=[
        BINARY_PATH,  # File dependency ensures binary exists before Docker build
        'Dockerfile.dev',
    ],
    resource_deps=['build-manager'],  # Wait for binary to be built
    labels=['build'],
    allow_parallel=False,
)

# Step 2: Tell Tilt about the image (already built and pushed)
# The image was already pushed by docker-build-and-push, so we just tag it
# Note: custom_build doesn't support resource_deps, but the deps parameter ensures
# the binary exists, and docker-build-and-push will complete before this runs
# due to the k8s_resource dependency chain
custom_build(
    IMAGE_NAME,
    'docker tag %s:tilt $EXPECTED_REF && docker push $EXPECTED_REF' % FULL_IMAGE_NAME,
    deps=[BINARY_PATH],  # File dependency for live_update
    tag='tilt',
    live_update=[
        sync(BINARY_PATH, '/manager'),
        run('kill -HUP 1', trigger=[BINARY_PATH]),
    ],
)

# ====================
# Deploy via Helm Chart
# ====================
# Deploy the controller manager using Helm chart
# Override image to use the locally built image from Kind registry
# Use local() to run helm template with --set flags, then apply via k8s_yaml
# Tilt will watch the chart directory for changes and re-template
# Note: CRD is included in the Helm chart, so no separate CRD installation needed
k8s_yaml(
    local('helm template kube-startup-cpu-boost %s/charts/kube-startup-cpu-boost --namespace kube-startup-cpu-boost-system --set controllerManager.manager.image.repository=%s --set controllerManager.manager.image.tag=tilt' % (PROJECT_DIR, FULL_IMAGE_NAME))
)

k8s_resource(
    'kube-startup-cpu-boost-controller-manager',
    labels=['controllers'],
    resource_deps=['generate-crds', 'build-manager', 'docker-build-and-push', 'kube-startup-cpu-boost'],  # Wait for CRDs, binary, image build/push, and Tilt image registration
)
