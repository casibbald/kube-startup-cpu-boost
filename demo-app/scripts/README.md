# PostgreSQL Traffic Control Scripts

These scripts allow you to simulate PostgreSQL failures by blocking/unblocking
traffic through the Envoy sidecar proxy. This is useful for testing the
ContainerRestart and PodConditionTransition triggers in the StartupCPUBoost
controller.

## Architecture

- **PostgreSQL** runs on port **5433** internally
- **Envoy sidecar** listens on port **5432** (standard PostgreSQL port) and proxies to PostgreSQL
- **Spring Boot app** connects to `postgresql:5432` (Envoy)
- Envoy configuration is stored in a ConfigMap and can be updated to block/unblock traffic
- Envoy admin API (port 9901) allows dynamic traffic control without pod restarts

## Scripts

### Python Script (Recommended - Fast)

#### `control_postgres_traffic.py`

Unified script to block or unblock PostgreSQL traffic using Envoy's admin API.
Much faster than ConfigMap-based approach.

**Usage:**

```bash
# Block traffic via circuit breaker (preserves data, recommended)
python3 scripts/control_postgres_traffic.py block --method circuit_breaker

# Restore traffic via circuit breaker (preserves data, recommended)
python3 scripts/control_postgres_traffic.py unblock --method circuit_breaker

# Alternative: Block by draining listeners (requires restart to restore)
python3 scripts/control_postgres_traffic.py block --method drain

# Alternative: Restore by restarting pod (loses data, not recommended for testing)
python3 scripts/control_postgres_traffic.py unblock --method restart
```

**What it does:**

1. Finds the PostgreSQL pod with Envoy sidecar
2. Port-forwards to Envoy admin interface (port 9901) or restarts deployment
3. Calls admin API or restarts to control traffic:
   - **Circuit breaker method** (recommended): Sets `max_connections=0` via
     runtime to block new connections, preserves data
   - **Drain method**: Drains listeners to stop accepting new connections (fast,
     requires restart to restore)
   - **Restart method**: Restarts the deployment to restore traffic (for unblock
     only, loses data)

**Advantages:**

- ✅ Fast (2-5 seconds vs 30-60 seconds)
- ✅ Single unified script for both block and unblock
- ✅ **Circuit breaker method preserves PostgreSQL data** (no pod restart)
- ✅ Better for rapid testing scenarios with data assertions
- ✅ Clear action-based interface (`block` or `unblock`)

### Shell Scripts (Legacy - Slower)

#### `block-postgres-traffic.sh`

Blocks PostgreSQL traffic by updating the Envoy ConfigMap to point to an invalid
port (65535). This causes connection failures.

**Usage:**

```bash
./demo-app/scripts/block-postgres-traffic.sh
```

**What it does:**

1. Updates the `postgresql-envoy-config` ConfigMap with invalid backend port
2. Restarts the PostgreSQL deployment to apply the new Envoy config
3. Envoy will reject connections, causing the Spring Boot app to fail

**Limitations:**

- ⚠️ Slow (30-60 seconds due to pod restart)
- ⚠️ Disruptive (kills existing connections)
- ⚠️ Not suitable for rapid testing

#### `unblock-postgres-traffic.sh`

Restores PostgreSQL traffic by updating the Envoy ConfigMap to point to the
correct port (5433).

**Usage:**

```bash
./demo-app/scripts/unblock-postgres-traffic.sh
```

**What it does:**

1. Updates the `postgresql-envoy-config` ConfigMap with correct backend port
2. Restarts the PostgreSQL deployment to apply the new Envoy config
3. Envoy will proxy connections to PostgreSQL, restoring connectivity

## Envoy Configuration

The deployed Envoy configuration (`postgresql-with-envoy-runtime.yaml`) includes:

- **Runtime layer**: Enables dynamic configuration via admin API
- **Lua filter**: Checks `block_postgres_traffic` runtime flag before proxying
- **Admin API**: Accessible on port 9901 for traffic control

See `docs/ENVOY_TRAFFIC_CONTROL_ANALYSIS.md` for detailed analysis.

## Testing Scenarios

### Test ContainerRestart Trigger

**Using Python script (recommended):**

1. Block traffic: `python3 scripts/control_postgres_traffic.py block --method
   circuit_breaker`
2. Spring Boot app will fail to connect to PostgreSQL (new connections rejected)
3. Container may restart due to health check failures
4. StartupCPUBoost should detect the restart and apply CPU boost
5. Unblock traffic: `python3 scripts/control_postgres_traffic.py unblock --method circuit_breaker`
6. App recovers and boost expires after duration policy
7. **PostgreSQL data is preserved** (no pod restart, suitable for testing with
   assertions)

**Using shell scripts (legacy):**

1. Block traffic: `./demo-app/scripts/block-postgres-traffic.sh`
2. Spring Boot app will fail to connect to PostgreSQL
3. Container may restart due to health check failures
4. StartupCPUBoost should detect the restart and apply CPU boost
5. Unblock traffic: `./demo-app/scripts/unblock-postgres-traffic.sh`
6. App recovers and boost expires after duration policy

### Test PodConditionTransition Trigger

**Using Python script (recommended):**

1. Block traffic: `python3 scripts/control_postgres_traffic.py block --method
   circuit_breaker`
2. Spring Boot app health checks fail
3. Pod condition transitions from `Ready=True` to `Ready=False`
4. Unblock traffic: `python3 scripts/control_postgres_traffic.py unblock --method circuit_breaker`
5. Pod condition transitions from `Ready=False` to `Ready=True`
6. StartupCPUBoost should detect the transition and apply CPU boost
7. **PostgreSQL data is preserved** (no pod restart, suitable for testing with
   assertions)

**Using shell scripts (legacy):**

1. Block traffic: `./demo-app/scripts/block-postgres-traffic.sh`
2. Spring Boot app health checks fail
3. Pod condition transitions from `Ready=True` to `Ready=False`
4. Unblock traffic: `./demo-app/scripts/unblock-postgres-traffic.sh`
5. Pod condition transitions from `Ready=False` to `Ready=True`
6. StartupCPUBoost should detect the transition and apply CPU boost

### Test Cooldown Policy

**Using Python script (recommended - better for rapid testing):**

1. Rapidly block/unblock traffic multiple times:

   ```bash
   python3 scripts/control_postgres_traffic.py block --method circuit_breaker
   python3 scripts/control_postgres_traffic.py unblock --method circuit_breaker
   # Repeat quickly - data is preserved!
   ```

2. Cooldown policy should limit activations:
   - `minIntervalSeconds: 300` (5 minutes between activations)
   - `maxActivationsPerHour: 3` (maximum 3 per hour)
3. Check logs for "skipping boost activation due to cooldown" messages

**Note**: The circuit breaker method is ideal for this scenario:

- ✅ Fast (2-5 seconds per operation)
- ✅ Reversible (no restart needed)
- ✅ **Preserves PostgreSQL data** (perfect for testing with assertions)
- ✅ Can be used repeatedly without data loss

## Monitoring

### Check Envoy Admin Interface

```bash
# Port-forward to Envoy admin interface
kubectl port-forward -n demo deployment/postgresql 9901:9901

# Access admin UI
open http://localhost:9901

# Or check stats via curl
curl http://localhost:9901/stats
```

### Check Spring Boot App Logs

```bash
# View app logs
kubectl logs -n demo deployment/spring-demo-app -f

# Look for database connection errors when traffic is blocked
```

### Check PostgreSQL Logs

```bash
# View PostgreSQL logs
kubectl logs -n demo deployment/postgresql -c postgresql -f

# View Envoy logs
kubectl logs -n demo deployment/postgresql -c envoy -f
```

## Manual Envoy Config Update

You can also manually update the ConfigMap:

```bash
# Edit the ConfigMap
kubectl edit configmap postgresql-envoy-config -n demo

# Or apply a new config
kubectl apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: postgresql-envoy-config
  namespace: demo
data:
  envoy.yaml: |
    # Your Envoy config here
EOF

# Restart to apply
kubectl rollout restart deployment/postgresql -n demo
```

## Troubleshooting

### Envoy not restarting

If Envoy doesn't pick up the new config:

```bash
# Force restart the pod
kubectl delete pod -n demo -l app=postgresql
```

### Connection still working after block

Check that the ConfigMap was updated:

```bash
kubectl get configmap postgresql-envoy-config -n demo -o yaml
```

Verify Envoy is using the new config:

```bash
kubectl exec -n demo deployment/postgresql -c envoy -- cat /etc/envoy/envoy.yaml
```

### Spring Boot app not failing

Check app logs for connection errors:

```bash
kubectl logs -n demo deployment/spring-demo-app | grep -i "connection\|error\|exception"
```

Verify the app is connecting to the correct service:

```bash
kubectl get service postgresql -n demo
```
