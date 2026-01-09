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
# - Or ensure Kind cluster 'kube-startup-cpu-boost' exists with registry on localhost:5001

# ====================
# Configuration
# ====================

# Restrict to kind cluster
allow_k8s_contexts(['kind-kube-startup-cpu-boost'])

# Configure default registry for Kind cluster
# Explicitly set registry to avoid auto-detection from ConfigMap
# The registry is set up by scripts/setup_kind.py as 'kind-registry' on localhost:5001
# host_from_cluster uses the container name 'kind-registry:5000' which Docker DNS resolves
# Note: Host port is 5001 to avoid conflict with macOS AirPlay Receiver (port 5000)
default_registry(
    'localhost:5001',           # Registry host as seen from local machine (port 5001)
    host_from_cluster='kind-registry:5000'  # Registry host as seen from within Kind cluster (container port 5000)
)

# Suppress warning for custom_build image that uses full registry path
# We use custom_build with explicit registry path, so Tilt won't find the image name in manifests
update_settings(suppress_unused_image_warnings=["kube-startup-cpu-boost"])

# Get the directory where this Tiltfile is located
PROJECT_DIR = '.'

# ====================
# Architecture Detection
# ====================
# Detect the Kind cluster architecture for Go binary build
# This ensures the binary matches the cluster architecture
# Works on both Intel (amd64) and Apple Silicon (arm64) hosts
# Uses Python script for reliable cross-platform detection
# Note: We use native docker build (no --platform flag) for host-aware builds
arch_result = str(local('python3 scripts/detect_kind_architecture.py')).strip()
arch_parts = arch_result.split(' ')
GO_ARCH = arch_parts[0]

# ====================
# CRD Generation
# ====================
# Generate CRDs when CRD code changes
# This ensures CRDs are always up-to-date with the Go code
# CRDs are installed via Helm chart (included in charts/kube-startup-cpu-boost/templates/startupcpuboost-crd.yaml)
# No need to install separately - Helm will handle it
# After generating CRDs, sync them to the Helm chart template to keep them in sync
local_resource(
    'generate-crds',
    cmd='make manifests && make sync-helm-crd',
    deps=[
        'api/v1alpha1/startupcpuboost_types.go',
        'Makefile',
        'scripts/sync_helm_crd.py',
    ],
    resource_deps=[],  # No dependencies - this runs first
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
    # Embed build-time variables (git hash and timestamp) to force new images
    # This prevents Docker from using cached layers when the binary changes
    # Each build will have unique git hash and timestamp, making the binary unique
    cmd='GOOS=linux GOARCH=%s go build -ldflags "-X main.buildGitHash=$(git rev-parse --short HEAD 2>/dev/null || echo unknown) -X main.buildTimestamp=$(date -u +"%%Y-%%m-%%dT%%H:%%M:%%SZ" 2>/dev/null || echo unknown)" -o bin/manager cmd/main.go' % GO_ARCH,
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
# Follow BRRTRouter pattern: use local_resource to build and push, then custom_build to tag
# This ensures the image is built and pushed before Kubernetes resources try to use it
BINARY_PATH = 'bin/manager'
IMAGE_NAME = 'kube-startup-cpu-boost'
# Registry as seen from host (for docker push)
REGISTRY_HOST = 'localhost:5001'
# Registry as seen from inside Kind cluster (for Kubernetes image pull)
REGISTRY_CLUSTER = 'kind-registry:5000'
FULL_IMAGE_NAME_HOST = '%s/%s' % (REGISTRY_HOST, IMAGE_NAME)
FULL_IMAGE_NAME_CLUSTER = '%s/%s' % (REGISTRY_CLUSTER, IMAGE_NAME)
# Use unique tag per build to prevent Kubernetes from using cached images
# Git hash ensures each build has a unique image tag
IMAGE_TAG = 'tilt-' + str(local('git rev-parse --short HEAD 2>/dev/null || echo unknown')).strip()
IMAGE_REF = '%s:%s' % (FULL_IMAGE_NAME_HOST, IMAGE_TAG)

# Build and push Docker image using local_resource
# This explicitly builds and pushes the image, ensuring it's available before deployment
# --rm and --force-rm prevent intermediate container accumulation
local_resource(
    'docker-build-and-push',
    # Build and push to local registry (much faster than 'kind load')
    # https://kind.sigs.k8s.io/docs/user/local-registry/
    # Verify binary exists and has non-zero size before building (prevents "no such file" errors)
    # The test ensures the binary is fully written before Docker build starts
    # Debug: List binary and verify it's in the build context
    # Use --no-cache to ensure binary is always included (prevents cached COPY layer issues)
    # Verify binary exists and is in build context
    # Use default builder (not remote) to ensure local filesystem access
    # --load ensures image is available locally (required for local registry push)
    # Note: Binary verification is now done in Dockerfile.dev verification stage
    # Additional verification: Check binary architecture matches target platform
    'echo "=== Pre-Docker Build Verification ===" && \
     test -f %s && test -s %s && \
     echo "✅ Binary exists: $(ls -lh %s)" && \
     echo "✅ Binary size: $(stat -c%%s %s 2>/dev/null || stat -f%%z %s) bytes" && \
     echo "✅ Binary architecture: $(file %s 2>/dev/null || echo "file command not available")" && \
     echo "✅ Verifying binary is in Docker build context..." && \
     ls -lh bin/manager && \
     pwd && \
     echo "=== Starting Docker Build (host-aware, no --platform) ===" && \
     docker build --no-cache -f Dockerfile.dev -t %s --rm --force-rm . && \
     echo "=== Post-Build Image Verification ===" && \
     echo "Verifying binary exists in final image..." && \
     docker run --rm --entrypoint="/bin/sh" %s -c "ls -lh /manager" && \
     docker run --rm --entrypoint="/bin/sh" %s -c "test -f /manager" && \
     docker run --rm --entrypoint="/bin/sh" %s -c "test -x /manager" && \
     echo "✅ Binary verified in final image" && \
     echo "=== Pushing Image ===" && \
     docker push %s' % (BINARY_PATH, BINARY_PATH, BINARY_PATH, BINARY_PATH, BINARY_PATH, BINARY_PATH, IMAGE_REF, IMAGE_REF, IMAGE_REF, IMAGE_REF, IMAGE_REF),
    deps=[
        BINARY_PATH,
        'Dockerfile.dev',
    ],
    resource_deps=[
        'build-manager',  # CRITICAL: Binary must be built first
    ],
    labels=['controllers'],
    allow_parallel=False,
)

# Tell Tilt about the image from local registry
# The image was already built and pushed by docker-build-and-push, so we tag it with $EXPECTED_REF
# $EXPECTED_REF is set by Tilt to the expected image reference (includes registry and tag)
# This ensures Tilt tracks the image correctly
custom_build(
    IMAGE_NAME,
    'docker tag %s $EXPECTED_REF && docker push $EXPECTED_REF' % IMAGE_REF,
    deps=[],  # No file deps - image is already built by docker-build-and-push
    tag=IMAGE_TAG,
    # live_update provides fast iteration for code changes:
    # - Syncs binary to running container (no image rebuild needed)
    # - Sends SIGHUP to restart the process
    # - Only works AFTER container is running with initial image
    # - This makes code changes very fast (seconds instead of minutes)
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
# The CRD template is synced from generated CRDs via generate-crds resource
# Tilt automatically watches the chart directory for changes, so when generate-crds
# updates the CRD template, Tilt will detect it and re-template
# 
# Note: We use helm template with --set flags to override image values.
# This respects helmify's chart structure (controllerManager.manager.image.repository/tag)
# which matches the kustomize-to-helm conversion that helmify performs.
# The chart structure is compatible with both helmify generation and manual helm template usage.
k8s_yaml(
    local('helm template kube-startup-cpu-boost %s/charts/kube-startup-cpu-boost --namespace kube-startup-cpu-boost-system --set controllerManager.manager.image.repository=%s --set controllerManager.manager.image.tag=%s' % (PROJECT_DIR, FULL_IMAGE_NAME_CLUSTER, IMAGE_TAG))
)

# Configure controller manager resource
# Note: Readiness timeout is controlled by:
# 1. Kubernetes readiness probe settings (in deployment.yaml)
# 2. Tilt CI overall timeout (--timeout flag in workflow)
# CI environments are slower, so we've optimized:
# - Readiness probe: initialDelaySeconds=10, timeoutSeconds=5 (increased from defaults)
# - Tilt CI timeout: 15m (increased from 10m)
# - GitHub Actions step timeout: 18m (increased from 12m)
k8s_resource(
    'kube-startup-cpu-boost-controller-manager',
    labels=['controllers'],
    # CRITICAL: Wait for image to be built and pushed before deploying
    # docker-build-and-push ensures the image exists in the registry
    # This prevents ErrImagePull errors where pods try to pull images before they're built
    resource_deps=[
        'generate-crds',
        'build-manager',
        'docker-build-and-push',  # MUST be present - ensures image is built before deployment
    ],
)

# ====================
# Build Demo Java App
# ====================
# Build the Spring Boot Java application using Maven
# This compiles the Java code and packages it into a JAR file
DEMO_APP_DIR = '%s/demo-app' % PROJECT_DIR
DEMO_APP_JAR = '%s/target/spring-demo-app-0.0.1-SNAPSHOT.jar' % DEMO_APP_DIR

local_resource(
    'build-demo-app',
    cmd='cd %s && mvn clean package -DskipTests' % DEMO_APP_DIR,
    deps=[
        '%s/pom.xml' % DEMO_APP_DIR,
        '%s/src' % DEMO_APP_DIR,
    ],
    labels=['demo'],
    allow_parallel=True,
)

# ====================
# Build Demo App Docker Image
# ====================
# Build Docker image for the demo Java app
# Uses the Dockerfile in demo-app directory
DEMO_APP_IMAGE_NAME = 'spring-demo-app'
DEMO_APP_FULL_IMAGE_NAME_HOST = '%s/%s' % (REGISTRY_HOST, DEMO_APP_IMAGE_NAME)
DEMO_APP_FULL_IMAGE_NAME_CLUSTER = '%s/%s' % (REGISTRY_CLUSTER, DEMO_APP_IMAGE_NAME)
# Use unique tag per build
DEMO_APP_IMAGE_TAG = 'tilt-' + str(local('git rev-parse --short HEAD 2>/dev/null || echo unknown')).strip()
DEMO_APP_IMAGE_REF = '%s:%s' % (DEMO_APP_FULL_IMAGE_NAME_HOST, DEMO_APP_IMAGE_TAG)

# Build and push Docker image for demo app
local_resource(
    'docker-build-demo-app',
    # Verify JAR exists before building
    # Use --no-cache to ensure JAR is always included
    cmd='test -f %s && test -s %s && echo "JAR exists: $(ls -lh %s)" && docker build --no-cache -f %s/Dockerfile -t %s --rm --force-rm --build-arg JAR_FILE=target/spring-demo-app-0.0.1-SNAPSHOT.jar %s && docker push %s' % (DEMO_APP_JAR, DEMO_APP_JAR, DEMO_APP_JAR, DEMO_APP_DIR, DEMO_APP_IMAGE_REF, DEMO_APP_DIR, DEMO_APP_IMAGE_REF),
    deps=[
        DEMO_APP_JAR,
        '%s/Dockerfile' % DEMO_APP_DIR,
    ],
    resource_deps=[
        'build-demo-app',  # JAR must be built first
    ],
    labels=['demo'],
    allow_parallel=False,
)

# Tell Tilt about the demo app image
# Use the full image name with registry so Tilt can track it correctly
# The image is already built and pushed by docker-build-demo-app, so we tag it with $EXPECTED_REF
# $EXPECTED_REF is set by Tilt to the expected image reference (includes registry and tag)
# This ensures Tilt tracks the image correctly
# Note: We don't add resource_deps here because custom_build doesn't support it
# Instead, the k8s_resource for spring-demo-app has resource_deps that includes docker-build-demo-app
custom_build(
    DEMO_APP_FULL_IMAGE_NAME_HOST,
    'docker tag %s $EXPECTED_REF && docker push $EXPECTED_REF' % DEMO_APP_IMAGE_REF,
    deps=[],  # No file deps - image is already built by docker-build-demo-app
    tag=DEMO_APP_IMAGE_TAG,
)

# ====================
# Deploy Demo App (without CR)
# ====================
# Deploy the demo Java Spring Boot app WITHOUT the StartupCPUBoost CR first
# The CR will be deployed separately after the webhook is ready
# Uses kustomize to build the manifests, then filters out the StartupCPUBoost CR
# Override the image to use the locally built image
# The demo app includes:
# - Spring Boot application deployment
# - Service
# - ConfigMap
# - PostgreSQL with Envoy
# 
# Note: We override the image in the deployment to use the locally built image
# We use Python to properly split YAML documents and exclude the StartupCPUBoost CR
# Use single quotes for Python string to avoid quote escaping issues
# CRITICAL: Wait for docker-build-demo-app to complete before generating YAML
# This ensures the image exists in the registry before Kubernetes tries to pull it
# Generate YAML directly using local() - the k8s_resource dependency ensures
# deployment waits for docker-build-demo-app, even if YAML is generated early
# The image tag is computed at Tiltfile load time, so it will be consistent
k8s_yaml(
    local('kubectl kustomize %s/demo-app | python3 -c \'import sys, yaml; docs = list(yaml.safe_load_all(sys.stdin)); filtered = [d for d in docs if d.get("kind") != "StartupCPUBoost"]; print(yaml.dump_all(filtered, default_flow_style=False))\' | sed "s|ghcr.io/google/spring-demo-app:latest|%s:%s|g"' % (PROJECT_DIR, DEMO_APP_FULL_IMAGE_NAME_CLUSTER, DEMO_APP_IMAGE_TAG)),
)

# Wait for webhook service to be ready
# The webhook service must have endpoints before CRs can be validated
# This ensures the webhook is accessible when StartupCPUBoost CRs are created
local_resource(
    'wait-for-webhook',
    cmd='echo "Waiting for webhook service to be ready..." && timeout 120 bash -c \'until kubectl get endpoints kube-startup-cpu-boost-webhook-service -n kube-startup-cpu-boost-system -o jsonpath="{.subsets[*].addresses[*].ip}" 2>/dev/null | grep -q .; do echo "Waiting for webhook endpoints..."; sleep 2; done\' && echo "Webhook service is ready"',
    resource_deps=[
        'kube-startup-cpu-boost-controller-manager',  # Controller must be ready first
    ],
    labels=['controllers'],
    allow_parallel=False,
)

# Deploy StartupCPUBoost CR separately after webhook is ready
# This ensures the webhook can validate the CR when it's created
# Extract only the StartupCPUBoost CR from kustomize output and apply it
# Use single quotes for Python string to avoid quote escaping issues
local_resource(
    'deploy-startupcpuboost-cr',
    cmd='kubectl kustomize %s/demo-app | python3 -c \'import sys, yaml; docs = list(yaml.safe_load_all(sys.stdin)); cr = [d for d in docs if d.get("kind") == "StartupCPUBoost"][0]; print(yaml.dump(cr, default_flow_style=False))\' | kubectl apply -f -' % PROJECT_DIR,
    resource_deps=[
        'wait-for-webhook',  # Webhook must be ready before CR is created
    ],
    labels=['demo'],
    allow_parallel=False,
)

# Configure demo app resources
# Wait for controller and webhook to be ready before deploying demo app
# This ensures the StartupCPUBoost CRD is installed and the webhook can validate CRs
k8s_resource(
    'spring-demo-app',
    labels=['demo'],
    port_forwards=['5050:5050'],  # Forward port 5050 to access the Spring Boot app
    resource_deps=[
        'kube-startup-cpu-boost-controller-manager',  # Controller must be ready
        'wait-for-webhook',  # Webhook service must have endpoints
        'build-demo-app',  # App must be built
        'docker-build-demo-app',  # Image must be built and pushed
        'postgresql',  # PostgreSQL must be ready before app starts
    ],
)

# Configure PostgreSQL resource
# PostgreSQL should start before the demo app
# Also depends on controller and webhook since StartupCPUBoost CR is in the same kustomize output
k8s_resource(
    'postgresql',
    labels=['demo'],
    resource_deps=[
        'kube-startup-cpu-boost-controller-manager',  # Controller must be ready
        'wait-for-webhook',  # Webhook service must have endpoints
    ],
)

# Note: StartupCPUBoost CR is deployed separately via deploy-startupcpuboost-cr resource
# This ensures the webhook is ready before the CR is created, preventing validation errors
# The CR is extracted from kustomize output and applied after the webhook service has endpoints
