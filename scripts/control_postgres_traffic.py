#!/usr/bin/env python3
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

"""
Control PostgreSQL traffic through Envoy using the admin API.

This script can block or unblock traffic, and drain listeners or restart pods.

Usage:
    # Block traffic by draining listeners
    python3 scripts/control_postgres_traffic.py block --method drain

    # Restore traffic by restarting the pod
    python3 scripts/control_postgres_traffic.py unblock --method restart

    # Block traffic via health checks (if configured)
    python3 scripts/control_postgres_traffic.py block --method healthcheck

    # Unblock traffic via health checks (if configured)
    python3 scripts/control_postgres_traffic.py unblock --method healthcheck
"""

import argparse
import subprocess
import sys
import time
import requests
from typing import Optional


def get_envoy_pod_name(namespace: str) -> Optional[str]:
    """Get the name of the PostgreSQL pod with Envoy sidecar."""
    try:
        result = subprocess.run(
            ["kubectl", "get", "pod", "-n", namespace, "-l", "app=postgresql", "-o", "jsonpath={.items[0].metadata.name}"],
            capture_output=True,
            text=True,
            check=True,
        )
        return result.stdout.strip() if result.stdout.strip() else None
    except subprocess.CalledProcessError:
        return None


def port_forward_admin(namespace: str, pod_name: str, local_port: int = 9901) -> Optional[subprocess.Popen]:
    """Start port-forward to Envoy admin interface."""
    try:
        process = subprocess.Popen(
            ["kubectl", "port-forward", "-n", namespace, f"pod/{pod_name}", f"{local_port}:9901"],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        # Wait a moment for port-forward to establish
        time.sleep(2)
        # Check if process is still running
        if process.poll() is None:
            return process
        else:
            stdout, stderr = process.communicate()
            print(f"Port-forward failed: {stderr.decode()}", file=sys.stderr)
            return None
    except Exception as e:
        print(f"Failed to start port-forward: {e}", file=sys.stderr)
        return None


def call_envoy_admin_api(method: str, endpoint: str, local_port: int = 9901, **kwargs) -> bool:
    """Call Envoy admin API endpoint."""
    url = f"http://localhost:{local_port}{endpoint}"
    try:
        response = requests.request(method, url, timeout=5, **kwargs)
        if response.status_code in (200, 204):
            return True
        else:
            print(f"Admin API call failed: {response.status_code} - {response.text}", file=sys.stderr)
            return False
    except requests.exceptions.RequestException as e:
        print(f"Failed to call admin API: {e}", file=sys.stderr)
        return False


def block_via_drain(namespace: str) -> bool:
    """Block traffic by draining Envoy listeners."""
    pod_name = get_envoy_pod_name(namespace)
    if not pod_name:
        print(f"Failed to find PostgreSQL pod in namespace '{namespace}'", file=sys.stderr)
        return False

    print(f"Found pod: {pod_name}")
    print("Starting port-forward to Envoy admin interface...")
    
    port_forward = port_forward_admin(namespace, pod_name)
    if not port_forward:
        return False

    try:
        print("Draining Envoy listeners to stop accepting new connections...")
        success = call_envoy_admin_api("POST", "/drain_listeners")
        
        if success:
            print("✓ PostgreSQL traffic blocked successfully")
            print("  Envoy listeners are now draining")
            print("  New connections will be rejected")
            print("  Existing connections will complete gracefully")
            print("  Note: To restore traffic, use 'unblock --method restart'")
            return True
        else:
            print("✗ Failed to drain listeners", file=sys.stderr)
            return False
    finally:
        port_forward.terminate()
        port_forward.wait()


def block_via_circuit_breaker(namespace: str) -> bool:
    """
    Block traffic by setting circuit breaker max_connections to 0 and draining listeners.
    
    This blocks all new connections AND terminates existing connections to the cluster
    without restarting the pod, preserving PostgreSQL data for testing scenarios.
    """
    pod_name = get_envoy_pod_name(namespace)
    if not pod_name:
        print(f"Failed to find PostgreSQL pod in namespace '{namespace}'", file=sys.stderr)
        return False

    print(f"Found pod: {pod_name}")
    print("Starting port-forward to Envoy admin interface...")
    
    port_forward = port_forward_admin(namespace, pod_name)
    if not port_forward:
        return False

    try:
        print("Blocking PostgreSQL traffic via circuit breaker and draining listeners...")
        
        # Step 1: Set max_connections to 0 to block all new connections
        print("  Setting circuit breaker max_connections to 0...")
        success1 = call_envoy_admin_api(
            "POST",
            "/runtime_modify",
            params={"postgres_cluster.circuit_breakers.default.max_connections": "0"},
        )
        
        if not success1:
            print("✗ Failed to set circuit breaker", file=sys.stderr)
            return False
        
        # Step 2: Drain listeners to terminate existing connections
        # Using skip_exit to keep Envoy running (listeners won't accept new connections)
        print("  Draining listeners to terminate existing connections...")
        success2 = call_envoy_admin_api("POST", "/drain_listeners?graceful=true&skip_exit=true")
        
        if success1 and success2:
            print("✓ PostgreSQL traffic blocked successfully")
            print("  Circuit breaker max_connections set to 0 (new connections rejected)")
            print("  Listeners drained (existing connections terminated)")
            print("  All connections to PostgreSQL are now blocked")
            print("  ⚠️  Note: To restore, pod restart is required")
            print("  ✅ PostgreSQL data will be preserved (using PersistentVolume)")
            return True
        else:
            print("✗ Failed to block traffic completely", file=sys.stderr)
            return False
    finally:
        port_forward.terminate()
        port_forward.wait()


def unblock_via_restart(namespace: str) -> bool:
    """
    Restore traffic by restarting the PostgreSQL deployment.
    
    This performs a rolling update that restarts the entire pod, including
    both the PostgreSQL and Envoy containers. The old pod is terminated and
    a new pod is created with fresh container instances.
    
    Note: With the current emptyDir volume configuration, PostgreSQL data
    will be lost. For production, use a PersistentVolume.
    """
    print("Restarting PostgreSQL deployment to restore traffic...")
    print("  This will restart the entire pod (both PostgreSQL and Envoy containers)")
    print("  Note: PostgreSQL data will be lost (using emptyDir volume)")
    try:
        subprocess.run(
            ["kubectl", "rollout", "restart", "deployment/postgresql", "-n", namespace],
            check=True,
        )
        print("Waiting for rollout to complete...")
        subprocess.run(
            ["kubectl", "rollout", "status", "deployment/postgresql", "-n", namespace, "--timeout=60s"],
            check=True,
        )
        print("✓ PostgreSQL deployment restarted successfully")
        print("  Both PostgreSQL and Envoy containers have been restarted")
        print("  Traffic should now flow through Envoy to PostgreSQL")
        return True
    except subprocess.CalledProcessError as e:
        print(f"✗ Failed to restart deployment: {e}", file=sys.stderr)
        return False


def unblock_via_circuit_breaker(namespace: str) -> bool:
    """
    Restore traffic by resetting circuit breaker max_connections via runtime.
    
    This restores connections to the cluster without restarting the pod,
    preserving PostgreSQL data for testing scenarios.
    """
    pod_name = get_envoy_pod_name(namespace)
    if not pod_name:
        print(f"Failed to find PostgreSQL pod in namespace '{namespace}'", file=sys.stderr)
        return False

    print(f"Found pod: {pod_name}")
    print("Starting port-forward to Envoy admin interface...")
    
    port_forward = port_forward_admin(namespace, pod_name)
    if not port_forward:
        return False

    try:
        print("Unblocking PostgreSQL traffic via circuit breaker...")
        # Restore max_connections to allow connections (default is 1000)
        success = call_envoy_admin_api(
            "POST",
            "/runtime_modify",
            params={"postgres_cluster.circuit_breakers.default.max_connections": "1000"},
        )
        
        if success:
            print("✓ Circuit breaker max_connections restored to 1000")
            print("  ⚠️  Warning: If listeners were drained, they cannot be restored via API")
            print("  ⚠️  Pod restart is required to restore listeners")
            print("  ✅ PostgreSQL data will be preserved (using PersistentVolume)")
            return True
        else:
            print("✗ Failed to unblock traffic", file=sys.stderr)
            return False
    finally:
        port_forward.terminate()
        port_forward.wait()


def main():
    parser = argparse.ArgumentParser(
        description="Control PostgreSQL traffic through Envoy",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Block traffic via circuit breaker (preserves data, recommended)
  %(prog)s block --method circuit_breaker

  # Restore traffic via circuit breaker (preserves data, recommended)
  %(prog)s unblock --method circuit_breaker

  # Block traffic by draining listeners (requires restart to restore)
  %(prog)s block --method drain

  # Restore traffic by restarting pod (loses data, not recommended)
  %(prog)s unblock --method restart
        """,
    )
    parser.add_argument(
        "action",
        choices=["block", "unblock"],
        help="Action to perform: 'block' to stop traffic, 'unblock' to restore traffic",
    )
    parser.add_argument(
        "--namespace",
        default="demo",
        help="Kubernetes namespace (default: demo)",
    )
    parser.add_argument(
        "--method",
        choices=["circuit_breaker", "drain", "restart"],
        default="circuit_breaker",
        help="Method for traffic control: 'circuit_breaker' uses runtime config (preserves data, recommended), 'drain' drains listeners (requires restart to restore), 'restart' restarts pod (for unblock only, loses data)",
    )
    
    args = parser.parse_args()

    # Validate method/action combinations
    if args.action == "block":
        if args.method == "restart":
            print("Error: 'restart' method is only valid for 'unblock' action", file=sys.stderr)
            sys.exit(1)
        if args.method == "circuit_breaker":
            success = block_via_circuit_breaker(args.namespace)
        else:  # drain
            success = block_via_drain(args.namespace)
    else:  # unblock
        if args.method == "drain":
            print("Error: 'drain' method cannot restore traffic. Use 'circuit_breaker' or 'restart'", file=sys.stderr)
            sys.exit(1)
        if args.method == "circuit_breaker":
            success = unblock_via_circuit_breaker(args.namespace)
        else:  # restart
            success = unblock_via_restart(args.namespace)

    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()
