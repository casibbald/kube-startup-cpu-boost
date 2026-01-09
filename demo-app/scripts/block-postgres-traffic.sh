#!/bin/bash
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

# Script to block PostgreSQL traffic through Envoy
# This simulates database failures to test ContainerRestart and PodConditionTransition triggers

set -euo pipefail

NAMESPACE="${NAMESPACE:-demo}"
CONFIGMAP_NAME="postgresql-envoy-config"

# Create Envoy config that blocks traffic by pointing to a non-existent endpoint
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${CONFIGMAP_NAME}
  namespace: ${NAMESPACE}
data:
  envoy.yaml: |
    admin:
      address:
        socket_address:
          protocol: TCP
          address: 0.0.0.0
          port_value: 9901
    static_resources:
      listeners:
      - name: postgres_listener
        address:
          socket_address:
            protocol: TCP
            address: 0.0.0.0
            port_value: 5432
        filter_chains:
        - filters:
          - name: envoy.filters.network.tcp_proxy
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy
              stat_prefix: postgres_tcp
              cluster: postgres_cluster
              access_log:
              - name: envoy.access_loggers.stdout
                typed_config:
                  "@type": type.googleapis.com/envoy.extensions.access_loggers.stream.v3.StdoutAccessLog
      clusters:
      - name: postgres_cluster
        connect_timeout: 5s
        type: STATIC_DNS
        lb_policy: ROUND_ROBIN
        load_assignment:
          cluster_name: postgres_cluster
          endpoints:
          - lb_endpoints:
            - endpoint:
                address:
                  socket_address:
                    protocol: TCP
                    address: 127.0.0.1
                    port_value: 65535  # Invalid port - blocks traffic
EOF

echo "PostgreSQL traffic blocked. Envoy will reject connections."
echo "Restarting Envoy pod to apply new configuration..."
kubectl rollout restart deployment/postgresql -n "${NAMESPACE}"
echo "Waiting for rollout to complete..."
kubectl rollout status deployment/postgresql -n "${NAMESPACE}"
