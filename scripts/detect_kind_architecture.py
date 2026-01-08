#!/usr/bin/env python3
# Copyright 2023 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
"""
Detect Kind cluster architecture for host-aware builds.

This script detects the architecture of the Kind cluster node to ensure
the Go binary and Docker image are built for the correct platform.

Works on both Intel (amd64) and Apple Silicon (arm64) hosts.

Usage: detect_kind_architecture.py
Output: GO_ARCH DOCKER_PLATFORM (e.g., "arm64 linux/arm64")
"""

import subprocess
import sys


def run_command(cmd, check=False):
    """Run a command and return stdout."""
    try:
        result = subprocess.run(
            cmd,
            shell=True,
            capture_output=True,
            text=True,
            check=check
        )
        return result.stdout.strip()
    except subprocess.CalledProcessError:
        return None


def detect_kind_architecture():
    """Detect the architecture of the Kind cluster node."""
    # Try to get architecture from Kind cluster node
    # First try: query the cluster node directly
    arch = run_command('kubectl get nodes -o jsonpath="{.items[0].status.nodeInfo.architecture}" 2>/dev/null')
    
    if arch:
        arch = arch.strip().strip('"').strip("'")
    
    # If cluster query failed, try to detect from Kind node container
    if not arch or arch == '':
        arch = run_command('docker exec kube-startup-cpu-boost-control-plane uname -m 2>/dev/null')
        if arch:
            arch = arch.strip()
    
    # Map Kubernetes/Docker architecture names to Go/Docker architectures
    arch_map = {
        'amd64': ('amd64', 'linux/amd64'),
        'x86_64': ('amd64', 'linux/amd64'),
        'arm64': ('arm64', 'linux/arm64'),
        'aarch64': ('arm64', 'linux/arm64'),
    }
    
    # Normalize architecture name
    if arch:
        arch_lower = arch.lower()
        if arch_lower in arch_map:
            go_arch, docker_platform = arch_map[arch_lower]
            return go_arch, docker_platform
    
    # Fallback: detect from host if cluster detection fails
    host_arch = run_command('uname -m')
    if host_arch:
        host_arch = host_arch.strip()
        if host_arch in ['arm64', 'aarch64']:
            return ('arm64', 'linux/arm64')
    
    # Final fallback: default to amd64 (most common)
    return ('amd64', 'linux/amd64')


def main():
    """Main function."""
    go_arch, docker_platform = detect_kind_architecture()
    print(f"{go_arch} {docker_platform}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
