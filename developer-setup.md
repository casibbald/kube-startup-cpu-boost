# Developer Setup Guide

This guide explains how to set up and use the local development environment for
kube-startup-cpu-boost using Tilt and Kind.

## Prerequisites

Before starting, ensure you have the following installed:

- **Docker** - Container runtime
- **Kind** - Kubernetes in Docker (`kind` CLI)
- **kubectl** - Kubernetes command-line tool
- **Helm** - Kubernetes package manager
- **Tilt** - Local development tool
- **Python 3** - For setup scripts
- **Go 1.23+** - For building the controller
- **just** - Command runner (optional but recommended)

### Installing Prerequisites

```bash
# macOS (using Homebrew)
brew install kind kubectl helm tilt go just

# Or install individually:
# - Kind: https://kind.sigs.k8s.io/docs/user/quick-start/#installation
# - kubectl: https://kubernetes.io/docs/tasks/tools/
# - Helm: https://helm.sh/docs/intro/install/
# - Tilt: https://docs.tilt.dev/install.html
# - Go: https://go.dev/doc/install
# - just: https://github.com/casey/just#installation
```

## Quick Start

The fastest way to get started:

```bash
# Setup Kind cluster and start Tilt
just dev-up
```

This will:

1. Create a Kind cluster named `kube-startup-cpu-boost`
2. Setup a local Docker registry on `localhost:5000`
3. Configure the cluster to use the local registry
4. Start Tilt to build and deploy the controller

**Tilt UI**: Open <http://localhost:10350> in your browser to monitor the build and deployment.

## Development Workflow

### Starting Development

```bash
# Option 1: Start everything at once
just dev-up

# Option 2: Step by step
just kind-setup    # Setup Kind cluster and registry
just tilt-up       # Start Tilt
```

### During Development

Tilt automatically:

- Rebuilds the Go binary when source files change
- Rebuilds the Docker image
- Pushes to the local Kind registry
- Updates the deployment in the cluster
- Restarts the controller (with live updates when possible)

**View logs:**

```bash
# Follow controller logs
just logs

# Or use kubectl directly
kubectl logs -n kube-startup-cpu-boost-system \
  -l control-plane=controller-manager -f
```

**Check status:**

```bash
# Show cluster and controller status
just status

# Or check pods directly
kubectl get pods -n kube-startup-cpu-boost-system
```

### Stopping Development

```bash
# Stop Tilt (keeps cluster running)
just dev-down

# Or stop Tilt only
just tilt-down

# To delete the Kind cluster
just kind-delete
```

## Justfile Commands

The `justfile` provides convenient commands for common development tasks.

### Development Environment

| Command | Description |
|---------|-------------|
| `just dev-up` | Start Kind cluster and Tilt (full setup) |
| `just dev-down` | Stop Tilt |
| `just kind-setup` | Setup Kind cluster and registry only |
| `just kind-delete` | Delete Kind cluster |
| `just tilt-up` | Start Tilt (assumes cluster exists) |
| `just tilt-down` | Stop Tilt |

### Utilities

| Command | Description |
|---------|-------------|
| `just status` | Show cluster status, pods, and recent logs |
| `just logs` | Follow controller manager logs |
| `just port-forward-metrics` | Port forward to metrics endpoint (8080) |
| `just port-forward-health` | Port forward to health probe (8081) |

### Testing

| Command | Description |
|---------|-------------|
| `just test` | Run all tests with coverage |
| `just test-fast` | Run tests without coverage (faster) |
| `just test-verbose` | Run tests with verbose output |
| `just test-package <package>` | Run tests for a specific package |
| `just coverage` | Generate coverage report |
| `just coverage-html` | Generate HTML coverage report |
| `just coverage-text` | Generate text coverage report |
| `just coverage-summary` | Show coverage percentage |

See `just --list` for all available commands.

## Tilt Usage

### Starting Tilt

```bash
# Using justfile
just tilt-up

# Or directly
tilt up
```

### Tilt UI

Once Tilt is running, access the UI at:

- **URL**: <http://localhost:10350>
- **Features**:
  - View all resources and their status
  - See build logs in real-time
  - Trigger manual rebuilds
  - View resource dependencies
  - Access pod logs and exec into containers

### Tilt Resources

Tilt manages the following resources:

1. **`generate-crds`** - Generates CRDs via `make manifests`
2. **`startupcpuboost`** - Applies CRDs to the cluster
3. **`build-manager`** - Builds the Go binary (`make build`)
4. **`kube-startup-cpu-boost`** - Builds and pushes Docker image
5. **`kube-startup-cpu-boost-controller-manager`** - Deploys via Helm chart

### Live Updates

Tilt supports live updates for faster iteration:

- When the binary changes, Tilt syncs it to the container
- Sends SIGHUP to restart the process
- No need to rebuild the Docker image for code changes

### Stopping Tilt

```bash
# Using justfile
just tilt-down

# Or directly
tilt down
```

## Kind Cluster Details

### Cluster Configuration

- **Name**: `kube-startup-cpu-boost`
- **Registry**: `kube-startup-cpu-boost-registry` on `localhost:5000`
- **Network**:
  - Pod Subnet: `10.206.0.0/16`
  - Service Subnet: `10.207.0.0/16`
- **Feature Gates**: `InPlacePodVerticalScaling: true`

### Registry

The local registry:

- Runs on `localhost:5000`
- Uses persistent storage (survives container restarts)
- Automatically connected to the Kind network
- Configured in containerd on all nodes

### Manual Cluster Management

```bash
# Check cluster status
kind get clusters

# Get cluster kubeconfig
kubectl config use-context kind-kube-startup-cpu-boost

# Check registry
docker ps | grep kube-startup-cpu-boost-registry

# View registry contents (if needed)
curl http://localhost:5000/v2/_catalog
```

## Building and Testing

### Building Locally

```bash
# Build Go binary
make build

# Build Docker image
make docker-build IMG=localhost:5000/kube-startup-cpu-boost:dev

# Push to local registry
docker push localhost:5000/kube-startup-cpu-boost:dev
```

### Running Tests

```bash
# Run all tests with coverage
just test

# Run tests without coverage (faster)
just test-fast

# Run tests for specific package
just test-package internal/controller

# Generate coverage reports
just coverage-html
open coverage/coverage.html
```

### Code Quality

```bash
# Format code
make fmt

# Lint code
make vet

# Run all checks
make test
```

## Deployment Details

### Helm Chart

The controller is deployed via Helm chart located at `charts/kube-startup-cpu-boost/`.

**Values overridden by Tilt:**

- `controllerManager.manager.image.repository`: `localhost:5000/kube-startup-cpu-boost`
- `controllerManager.manager.image.tag`: `tilt`

**Namespace**: `kube-startup-cpu-boost-system`

### Manual Helm Deployment

If you want to deploy manually (without Tilt):

```bash
# Build and push image first
make docker-build IMG=localhost:5000/kube-startup-cpu-boost:dev
docker push localhost:5000/kube-startup-cpu-boost:dev

# Install via Helm
helm install kube-startup-cpu-boost charts/kube-startup-cpu-boost \
  --set controllerManager.manager.image.repository=localhost:5000/kube-startup-cpu-boost \
  --set controllerManager.manager.image.tag=dev \
  -n kube-startup-cpu-boost-system --create-namespace
```

## Troubleshooting

### Kind Cluster Issues

**Cluster already exists:**

```bash
# Delete and recreate
just kind-delete
just kind-setup
```

**Registry not accessible:**

```bash
# Check registry is running
docker ps | grep kube-startup-cpu-boost-registry

# Check registry is on kind network
docker network inspect kind | grep kube-startup-cpu-boost-registry

# Restart registry if needed
docker start kube-startup-cpu-boost-registry
```

**Cluster not found:**

```bash
# Verify cluster exists
kind get clusters

# Recreate if needed
just kind-setup
```

### Tilt Issues

**Tilt can't find cluster:**

```bash
# Verify kubectl context
kubectl config current-context
# Should be: kind-kube-startup-cpu-boost

# Switch context if needed
kubectl config use-context kind-kube-startup-cpu-boost
```

**Image pull errors:**

- Ensure registry is running: `docker ps | grep registry`
- Check registry is on kind network
- Verify containerd is configured (done by setup script)

**Build failures:**

- Check Go build errors: `make build`
- Verify Docker is running: `docker info`
- Check Tilt logs in the UI

### Build Issues

**Go build errors:**

```bash
# Clean and rebuild
go clean -cache
make build
```

**Docker build fails:**

```bash
# Ensure binary exists
ls -la bin/manager

# Build binary first
make build

# Then build Docker image
make docker-build IMG=localhost:5000/kube-startup-cpu-boost:dev
```

**Helm template errors:**

```bash
# Validate Helm chart
helm template kube-startup-cpu-boost charts/kube-startup-cpu-boost

# Check values
helm template kube-startup-cpu-boost charts/kube-startup-cpu-boost \
  --set controllerManager.manager.image.repository=localhost:5000/kube-startup-cpu-boost \
  --set controllerManager.manager.image.tag=dev
```

### Controller Issues

**Controller not starting:**

```bash
# Check pod status
kubectl get pods -n kube-startup-cpu-boost-system

# View pod events
kubectl describe pod -n kube-startup-cpu-boost-system \
  -l control-plane=controller-manager

# Check logs
just logs
```

**CRDs not found:**

```bash
# Verify CRDs are installed
kubectl get crd startupcpuboosts.autoscaling.x-k8s.io

# Reinstall CRDs
make manifests
kubectl apply -f config/crd/bases/
```

## Advanced Usage

### Custom Image Tags

Edit `Tiltfile` to change the image tag:

```python
tag='tilt'  # Change to your preferred tag
```

### Multiple Clusters

To use a different cluster name:

1. Update `kind-config.yaml`: `name: your-cluster-name`
2. Update `Tiltfile`: `allow_k8s_contexts(['kind-your-cluster-name'])`
3. Update `scripts/setup_kind.py`: `CLUSTER_NAME = "your-cluster-name"`

### Port Forwards

Add port forwards to `Tiltfile` for debugging:

```python
k8s_resource(
    'kube-startup-cpu-boost-controller-manager',
    port_forwards=['8080:8080'],  # Metrics port
    # ...
)
```

### Debugging

**Exec into controller pod:**

```bash
kubectl exec -it -n kube-startup-cpu-boost-system \
  -l control-plane=controller-manager -- /manager --help
```

**View controller metrics:**

```bash
just port-forward-metrics
# Then access: http://localhost:8080/metrics
```

**Check webhook configuration:**

```bash
kubectl get mutatingwebhookconfigurations
kubectl get validatingwebhookconfigurations
```

## File Structure

```text
.
├── Tiltfile                 # Tilt configuration
├── kind-config.yaml         # Kind cluster configuration
├── Dockerfile.dev           # Development Dockerfile
├── justfile                 # Justfile commands
├── scripts/
│   └── setup_kind.py       # Kind cluster setup script
├── charts/
│   └── kube-startup-cpu-boost/  # Helm chart
└── docs/
    ├── TILT_SETUP.md       # Detailed Tilt documentation
    └── DEVELOPMENT_BUILD.md # Manual build instructions
```

## Additional Resources

- **Tilt Documentation**: <https://docs.tilt.dev/>
- **Kind Documentation**: <https://kind.sigs.k8s.io/>
- **Helm Documentation**: <https://helm.sh/docs/>
- **Detailed Tilt Setup**: See `docs/TILT_SETUP.md`
- **Manual Build Guide**: See `docs/DEVELOPMENT_BUILD.md`

## Quick Reference

```bash
# Start everything
just dev-up

# Check status
just status

# View logs
just logs

# Run tests
just test

# Stop
just dev-down

# Clean up
just kind-delete
```

## Getting Help

If you encounter issues:

1. Check the troubleshooting section above
2. Review Tilt UI for build errors
3. Check controller logs: `just logs`
4. Verify cluster status: `just status`
5. Review documentation in `docs/` directory
