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

# Variables
localbin := env_var_or_default("PWD", ".") + "/bin"
envtest_k8s_version := "1.30.x"
coverprofile := "cover.out"
coverage_dir := "coverage"

# Default recipe
default:
    @just --list

# ============================================================================
# Development Environment
# ============================================================================

# Start development environment (Kind + Tilt)
dev-up: kind-setup tilt-up

# Stop development environment (Tilt + optionally Kind)
dev-down: tilt-down
    @echo "💡 To delete Kind cluster, run: just kind-delete"

# Setup Kind cluster and registry
kind-setup:
    @echo "🚀 Setting up Kind cluster and registry..."
    @python3 scripts/setup_kind.py

# Delete Kind cluster
kind-delete:
    @echo "🗑️  Deleting Kind cluster..."
    @kind delete cluster --name kube-startup-cpu-boost || echo "Cluster not found or already deleted"
    @echo "✅ Kind cluster deleted"

# Start Tilt only (assumes cluster is already running)
tilt-up:
    @echo "🎯 Starting Tilt..."
    @echo "   Tilt UI: http://localhost:10350"
    @tilt up

# Stop Tilt only
tilt-down:
    @echo "🛑 Stopping Tilt..."
    @tilt down || echo "Tilt not running or already stopped"

# Run all tests with coverage
test:
    @just coverage

coverage:
    #!/usr/bin/env bash
    set -e
    LOCALBIN="$(pwd)/bin"
    mkdir -p {{coverage_dir}}
    if [ ! -f "$LOCALBIN/setup-envtest" ]; then
        echo "Installing envtest..."
        GOBIN="$LOCALBIN" go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
    fi
    export KUBEBUILDER_ASSETS="$($LOCALBIN/setup-envtest use {{envtest_k8s_version}} --bin-dir "$LOCALBIN" -p path)"
    go test ./... -coverprofile={{coverage_dir}}/{{coverprofile}} -covermode=atomic
    echo "Coverage profile written to {{coverage_dir}}/{{coverprofile}}"

# Run tests without coverage (faster)
test-fast:
    #!/usr/bin/env bash
    set -e
    LOCALBIN="$(pwd)/bin"
    if [ ! -f "$LOCALBIN/setup-envtest" ]; then
        echo "Installing envtest..."
        GOBIN="$LOCALBIN" go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
    fi
    export KUBEBUILDER_ASSETS="$($LOCALBIN/setup-envtest use {{envtest_k8s_version}} --bin-dir "$LOCALBIN" -p path)"
    go test ./... -short

# Run tests for a specific package
test-package package:
    #!/usr/bin/env bash
    set -e
    LOCALBIN="$(pwd)/bin"
    if [ ! -f "$LOCALBIN/setup-envtest" ]; then
        echo "Installing envtest..."
        GOBIN="$LOCALBIN" go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
    fi
    export KUBEBUILDER_ASSETS="$($LOCALBIN/setup-envtest use {{envtest_k8s_version}} --bin-dir "$LOCALBIN" -p path)"
    go test ./{{package}} -v

# Run tests with verbose output
test-verbose:
    #!/usr/bin/env bash
    set -e
    LOCALBIN="$(pwd)/bin"
    if [ ! -f "$LOCALBIN/setup-envtest" ]; then
        echo "Installing envtest..."
        GOBIN="$LOCALBIN" go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
    fi
    export KUBEBUILDER_ASSETS="$($LOCALBIN/setup-envtest use {{envtest_k8s_version}} --bin-dir "$LOCALBIN" -p path)"
    go test ./... -v -coverprofile={{coverage_dir}}/{{coverprofile}} -covermode=atomic

# Generate HTML coverage report
coverage-html:
    #!/usr/bin/env bash
    set -e
    if [ ! -f {{coverage_dir}}/{{coverprofile}} ]; then
        echo "Coverage profile not found. Running tests first..."
        just coverage
    fi
    mkdir -p {{coverage_dir}}
    go tool cover -html={{coverage_dir}}/{{coverprofile}} -o {{coverage_dir}}/coverage.html
    echo "HTML coverage report generated: {{coverage_dir}}/coverage.html"
    echo "Open with: open {{coverage_dir}}/coverage.html"

# Generate text coverage report
coverage-text:
    #!/usr/bin/env bash
    set -e
    if [ ! -f {{coverage_dir}}/{{coverprofile}} ]; then
        echo "Coverage profile not found. Running tests first..."
        just coverage
    fi
    mkdir -p {{coverage_dir}}
    go tool cover -func={{coverage_dir}}/{{coverprofile}} > {{coverage_dir}}/coverage.txt
    cat {{coverage_dir}}/coverage.txt
    echo ""
    echo "Text coverage report saved to: {{coverage_dir}}/coverage.txt"

# Generate coverage report for a specific package
coverage-package package:
    #!/usr/bin/env bash
    set -e
    LOCALBIN="$(pwd)/bin"
    PACKAGE_NAME=$(echo {{package}} | tr '/' '_')
    mkdir -p {{coverage_dir}}
    if [ ! -f "$LOCALBIN/setup-envtest" ]; then
        echo "Installing envtest..."
        GOBIN="$LOCALBIN" go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
    fi
    export KUBEBUILDER_ASSETS="$($LOCALBIN/setup-envtest use {{envtest_k8s_version}} --bin-dir "$LOCALBIN" -p path)"
    go test ./{{package}} -coverprofile={{coverage_dir}}/${PACKAGE_NAME}_cover.out -covermode=atomic
    go tool cover -html={{coverage_dir}}/${PACKAGE_NAME}_cover.out -o {{coverage_dir}}/${PACKAGE_NAME}_coverage.html
    go tool cover -func={{coverage_dir}}/${PACKAGE_NAME}_cover.out
    echo "Coverage report for {{package}} generated: {{coverage_dir}}/${PACKAGE_NAME}_coverage.html"

# Show coverage summary (percentage only)
coverage-summary:
    #!/usr/bin/env bash
    set -e
    if [ ! -f {{coverage_dir}}/{{coverprofile}} ]; then
        echo "Coverage profile not found. Running tests first..."
        just coverage
    fi
    go tool cover -func={{coverage_dir}}/{{coverprofile}} | tail -1

# Generate all coverage reports (HTML and text)
coverage-all:
    @just coverage-html
    @just coverage-text
    @just coverage-summary

# Clean coverage artifacts
coverage-clean:
    rm -rf {{coverage_dir}}

# Run tests and generate all coverage reports
test-coverage-all:
    @just test
    @just coverage-all

# ============================================================================
# Utilities
# ============================================================================

# Show cluster status
status:
    @echo "📊 Cluster Status..."
    @kubectl cluster-info --context kind-kube-startup-cpu-boost 2>/dev/null || echo "⚠️  Kind cluster not found. Run 'just kind-setup' first."
    @echo ""
    @echo "📦 Pods in kube-startup-cpu-boost-system:"
    @kubectl get pods -n kube-startup-cpu-boost-system 2>/dev/null || echo "⚠️  Namespace not found or cluster not accessible."
    @echo ""
    @echo "🔧 Controller Manager logs (last 20 lines):"
    @kubectl logs -n kube-startup-cpu-boost-system -l control-plane=controller-manager --tail=20 2>/dev/null || echo "⚠️  Controller not found or not running."

# Show controller manager logs
logs:
    @echo "📜 Controller Manager logs..."
    @kubectl logs -n kube-startup-cpu-boost-system -l control-plane=controller-manager --tail=100 -f

# Port forward to controller manager metrics
port-forward-metrics:
    @echo "🔌 Port forwarding to Controller Manager metrics (8080)..."
    @kubectl port-forward -n kube-startup-cpu-boost-system svc/kube-startup-cpu-boost-controller-manager-metrics 8080:8080

# Port forward to controller manager health probe
port-forward-health:
    @echo "🔌 Port forwarding to Controller Manager health probe (8081)..."
    @kubectl port-forward -n kube-startup-cpu-boost-system svc/kube-startup-cpu-boost-controller-manager-metrics 8081:8081

# ============================================================================
# Documentation
# ============================================================================

# Check markdown links in a specific file
check-links file:
    #!/usr/bin/env bash
    set -e
    if ! command -v markdown-link-check &> /dev/null; then
        echo "⚠️  markdown-link-check not found. Installing..."
        npm install -g markdown-link-check
    fi
    echo "🔍 Checking links in {{file}}..."
    markdown-link-check {{file}} --config .mlc_config.json

# Check markdown links in all markdown files (excluding docs/)
check-links-all:
    #!/usr/bin/env bash
    set -e
    if ! command -v markdown-link-check &> /dev/null; then
        echo "⚠️  markdown-link-check not found. Installing..."
        npm install -g markdown-link-check
    fi
    echo "🔍 Checking links in all markdown files..."
    find . -name "*.md" -not -path "./docs/*" -not -path "./.git/*" -not -path "./node_modules/*" | while read -r file; do
        echo ""
        echo "Checking: $file"
        markdown-link-check "$file" --config .mlc_config.json || true
    done
