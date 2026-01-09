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
"""Configure containerd registry mirror for Kind cluster nodes.

This script configures containerd on all Kind cluster nodes to use
a local Docker registry as a mirror.
"""

import os
import subprocess
import sys


def run_command(cmd, check=True):
    """Run a shell command and return the result."""
    try:
        result = subprocess.run(
            cmd,
            shell=True,
            capture_output=True,
            text=True,
            check=check,
            timeout=30
        )
        return result.stdout.strip(), result.returncode
    except subprocess.TimeoutExpired:
        print(f"ERROR: Command timed out: {cmd}")
        return None, 1
    except subprocess.CalledProcessError as e:
        if check:
            print(f"ERROR: Command failed: {cmd}")
            print(f"Error: {e.stderr}")
            return None, e.returncode
        return e.stdout.strip(), e.returncode


def get_registry_ip(registry_name):
    """Get the IP address of the Docker registry container from the kind network."""
    # First try to get IP from kind network specifically
    cmd = (
        f"docker inspect {registry_name} "
        "--format='{{range $net, $conf := .NetworkSettings.Networks}}{{if eq $net \"kind\"}}{{$conf.IPAddress}}{{end}}{{end}}'"
    )
    output, returncode = run_command(cmd, check=False)
    if returncode == 0 and output and output.strip():
        ip = output.strip().strip("'").strip('"')
        if ip:
            return ip
    
    # Fallback: get first IP from any network
    cmd = (
        f"docker inspect {registry_name} "
        "--format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'"
    )
    output, returncode = run_command(cmd, check=False)
    if returncode != 0 or not output:
        return None
    # Get first IP if multiple networks (split by whitespace/newline)
    ip = output.split()[0] if output.split() else output
    return ip.strip().strip("'").strip('"')


def get_kind_nodes():
    """Get list of Kind cluster node names."""
    cmd = "kubectl get nodes -o jsonpath='{.items[*].metadata.name}'"
    output, returncode = run_command(cmd, check=False)
    if returncode != 0 or not output:
        return []
    return output.split()


def configure_node_registry(node, registry_name):
    """Configure containerd registry mirror on a Kind node.
    
    Uses the registry container name instead of IP for better reliability.
    Docker's DNS resolution within the kind network will handle name resolution.
    """
    # Use container name instead of IP - Docker DNS will resolve it
    # This is more reliable than using IP addresses which can change
    registry_endpoint = f"http://{registry_name}:5001"
    
    # Build TOML config lines
    toml_lines = [
        '[plugins."io.containerd.grpc.v1.cri".registry.mirrors."localhost:5001"]',
        f'  endpoint = ["{registry_endpoint}"]',
        f'[plugins."io.containerd.grpc.v1.cri".registry.mirrors."{registry_name}:5001"]',
        f'  endpoint = ["{registry_endpoint}"]',
    ]

    # Escape each line for shell and write via printf
    escaped_lines = [line.replace("'", "'\\''") for line in toml_lines]
    printf_cmd = "printf '%s\\n' " + " ".join([f"'{line}'" for line in escaped_lines])

    # Write config file via docker exec using printf (no heredoc)
    write_cmd = (
        f"docker exec {node} sh -c '"
        "mkdir -p /etc/containerd && "
        f"{printf_cmd} > /tmp/registry-config.toml && "
        "cp /tmp/registry-config.toml /etc/containerd/config.toml.d/registry-config.toml && "
        "systemctl restart containerd || true"
        "'"
    )

    output, returncode = run_command(write_cmd, check=False)
    if returncode != 0:
        print(f"Warning: Could not configure registry on {node}")
        if output:
            print(f"  Error: {output}")
        return False
    return True


def main():
    """Main function."""
    # Get registry name from environment variable, with fallback to default
    registry_name = os.environ.get("REGISTRY_NAME", "kind-registry")
    print(f"Using registry: {registry_name}")

    # Verify registry exists and is running
    cmd = f"docker inspect {registry_name} --format='{{{{.State.Running}}}}'"
    output, returncode = run_command(cmd, check=False)
    if returncode != 0 or output.strip() != "true":
        print(f"ERROR: Registry '{registry_name}' is not running")
        print(f"Make sure the registry container exists and is running")
        sys.exit(1)

    print(f"Registry '{registry_name}' is running")

    # Get all nodes
    nodes = get_kind_nodes()
    if not nodes:
        print("ERROR: No Kind cluster nodes found")
        sys.exit(1)

    # Configure each node using container name (Docker DNS will resolve it)
    success_count = 0
    for node in nodes:
        print(f"Configuring registry mirror on node: {node}")
        if configure_node_registry(node, registry_name):
            success_count += 1
            print(f"  ✓ Successfully configured {node}")
        else:
            print(f"  ✗ Failed to configure {node}")

    if success_count == 0:
        print("ERROR: Failed to configure registry on any node")
        sys.exit(1)

    print(f"Successfully configured registry on {success_count}/{len(nodes)} nodes")


if __name__ == "__main__":
    main()
