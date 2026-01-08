# Kube Startup CPU Boost

Kube Startup CPU Boost is a controller that increases CPU resource requests and limits during
Kubernetes workload startup time. Once the workload is up and running,
the resources are set back to their original values.

[![Build](https://github.com/google/kube-startup-cpu-boost/actions/workflows/build.yaml/badge.svg)](https://github.com/google/kube-startup-cpu-boost/actions/workflows/build.yaml)
[![Version](https://img.shields.io/github/v/release/google/kube-startup-cpu-boost?label=version)](https://img.shields.io/github/v/release/google/kube-startup-cpu-boost?label=version)
[![Go Report Card](https://goreportcard.com/badge/github.com/google/kube-startup-cpu-boost)](https://goreportcard.com/report/github.com/google/kube-startup-cpu-boost)
![GitHub](https://img.shields.io/github/license/google/kube-startup-cpu-boost)

Note: this is not an officially supported Google product.

---

## Table of contents

* [Description](#description)
* [Installation](#installation)
* [Usage](#usage)
* [Features](#features)
  * [[Boost target] POD label selector](#boost-target-pod-label-selector)
  * [[Boost resources] percentage increase](#boost-resources-percentage-increase)
  * [[Boost resources] fixed target](#boost-resources-fixed-target)
  * [[Boost duration] fixed time](#boost-duration-fixed-time)
  * [[Boost duration] POD condition](#boost-duration-pod-condition)
* [Configuration](#configuration)
* [Metrics](#metrics)
* [License](#license)

## Description

The primary use cases for Kube Startup CPU Boosts are workloads that require extra CPU resources during
the startup phase - typically JVM based applications.

The Kube Startup CPU Boost leverages [In-place Resource Resize for Kubernetes Pods](https://kubernetes.io/docs/tasks/configure-pod-container/resize-container-resources/)
feature introduced in Kubernetes 1.27. It allows to revert workload's CPU resource requests and limits
back to their original values without the need to recreate the Pods.

The increase of resources is achieved by Mutating Admission Webhook. By default, the webhook also
removes CPU resource limits if present. The original resource values are set by tge operator after a
given period of time or when the POD condition is met.

## Installation

### Prerequisites

* **Requires Kubernetes 1.33 or newer**.
* For older clusters (>= 1.27), enable the `InPlacePodVerticalScaling` feature gate.

### Install with manifests file

 <!-- x-release-please-start-version -->
```sh
kubectl apply -f https://github.com/google/kube-startup-cpu-boost/releases/download/v0.17.1/manifests.yaml
```
 <!-- x-release-please-end -->

The Kube Startup CPU Boost components run in the `kube-startup-cpu-boost-system` namespace.

### Install with Kustomize

You can use [Kustomize](https://github.com/kubernetes-sigs/kustomize) to install the Kube Startup CPU
Boost with your own kustomization file.

 <!-- x-release-please-start-version -->
```sh
cat <<EOF > kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- https://github.com/google/kube-startup-cpu-boost?ref=v0.17.1
EOF
kubectl kustomize | kubectl apply -f -
```
 <!-- x-release-please-end -->

### Install with Helm

Helm installation uses self-hosted Helm chart repo [kube-startup-cpu-boost](https://google.github.io/kube-startup-cpu-boost).

```sh
helm repo add kube-startup-cpu-boost https://google.github.io/kube-startup-cpu-boost
helm repo update
helm install -n kube-startup-cpu-boost-system kube-startup-cpu-boost kube-startup-cpu-boost/kube-startup-cpu-boost
```

### Installation on GKE cluster

Ensure that GKE is running version 1.33 or newer by using
[GKE Rapid release channel](https://cloud.google.com/kubernetes-engine/docs/concepts/release-channels).

```sh
gcloud container clusters create poc \
    --release-channel rapid \
    --region europe-central2
```

## Usage

1. Create `StartupCPUBoost` object in your workload's namespace

   ```yaml
   apiVersion: autoscaling.x-k8s.io/v1alpha1
   kind: StartupCPUBoost
   metadata:
     name: boost-001
     namespace: demo
   selector:
     matchExpressions:
     - key: app.kubernetes.io/name
       operator: In
       values: ["spring-demo-app"]
   spec:
     resourcePolicy:
       containerPolicies:
       - containerName: spring-demo-app
         percentageIncrease:
           value: 50
     durationPolicy:
       podCondition:
         type: Ready
         status: "True"
   ```

   The above example will boost CPU requests and limits of a container `spring-demo-app` in a
   PODs with `app.kubernetes.io/name=spring-demo-app` label in `demo` namespace.
   The resources will be increased by 50% until the
   [POD Condition](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-conditions)
   `Ready` becomes `True`.

2. Schedule your workloads and observe the results

## Features

### [Boost target] POD label selector

Define the PODs that will be subject for resource boost with a label selector.

```yaml
spec:
  selector:
    matchExpressions:
    - key: app.kubernetes.io/name
       operator: In
       values: ["spring-rest-jpa"]
```

### [Boost resources] percentage increase

Define the percentage increase for a target container(s). The CPU requests and limits of selected
container(s) will be increase by the given percentage value.

```yaml
spec:
  resourcePolicy:
    containerPolicies:
     - containerName: spring-rest-jpa
       percentageIncrease:
         value: 50
```

### [Boost resources] fixed target

Define the fixed resources for a target container(s). The CPU requests and limits of selected
container(s) will be set to the given values. Note that specified requests and limits have to be
higher than the ones in the container.

```yaml
spec:
  resourcePolicy:
    containerPolicies:
     - containerName: spring-rest-jpa
       fixedResources:
         requests: "1"
         limits: "2"
```

### [Boost duration] fixed time

Define the fixed amount of time, the resource boost effect will last for it since the
**POD's schedule time**.

```yaml
spec:
 durationPolicy:
  fixedDuration:
    unit: Seconds
    value: 60
```

### [Boost duration] POD condition

Define the POD condition, the resource boost effect will last until the condition is met.

  ```yaml
  spec:
   durationPolicy:
     podCondition:
       type: Ready
       status: "True" 
  ```

### [Boost triggers] PodConditionTransition

Activate CPU boost when pod conditions transition between states. This is
useful for recovery scenarios where pods temporarily lose readiness without
container restarts (e.g., database restarts, network issues).

#### Common Pattern: Ready False → True

The most common use case is to boost CPU when a pod recovers from a NotReady
state:

```yaml
spec:
  triggers:
    - type: PodConditionTransition
      conditionType: Ready
      fromStatus: "False"
      toStatus: "True"
  resourcePolicy:
    containerPolicies:
      - containerName: app
        percentageIncrease:
          value: 50
  durationPolicy:
    podCondition:
      type: Ready
      status: "True"
```

**Configuration Fields:**

* `conditionType`: The pod condition to watch (e.g., "Ready",
  "PodScheduled")
* `fromStatus`: The source condition status ("True", "False", or
  "Unknown")
* `toStatus`: The target condition status ("True", "False", or "Unknown")

**Supported Status Values:**

* `"True"`: Condition is satisfied
* `"False"`: Condition is not satisfied
* `"Unknown"`: Condition status is unknown

**Note:** All three fields (`conditionType`, `fromStatus`, `toStatus`) are
required when using `PodConditionTransition` trigger type.

#### Initial Condition Handling

The `PodConditionTransition` trigger only activates on actual condition
transitions, not on initial state observations. This prevents false positives
when pods are created with conditions already in their target state.

**Behavior:**

* First condition observation is stored but does not trigger boost
* Only transitions from a previous state trigger boost activation
* Example: If a pod starts with `Ready=True`, it will not trigger a boost
  configured for `Ready False → True` until the condition transitions from
  `False` to `True`
* If you need boost on pod creation, combine with `PodCreate` trigger

#### Example: Pod starts with Ready=True

```yaml
# Pod created with Ready=True initially
# This will NOT trigger boost (first observation)
# Boost will only trigger when Ready transitions False → True later
spec:
  triggers:
    - type: PodConditionTransition
      conditionType: Ready
      fromStatus: "False"
      toStatus: "True"
```

### [Boost cooldown] Rate Limiting

Prevent excessive boost activations during crash loops, readiness flapping, or
other rapid failure scenarios by configuring cooldown policies. This ensures
system stability and prevents resource exhaustion.

#### Minimum Interval Between Activations

Enforce a minimum time interval between boost activations:

```yaml
spec:
  cooldown:
    minIntervalSeconds: 300  # 5 minutes between activations
  triggers:
    - type: ContainerRestart
    - type: PodConditionTransition
      conditionType: Ready
      fromStatus: "False"
      toStatus: "True"
```

**Behavior:**

* Activation is skipped if the minimum interval has not elapsed since the last
  activation
* Works per (pod, boost) combination - each pod has independent cooldown state
* Cooldown state persists across controller restarts (stored in pod annotations)
* A Kubernetes event is emitted when activation is skipped due to cooldown

#### Maximum Activations Per Hour

Limit the number of activations within a rolling hour window:

```yaml
spec:
  cooldown:
    maxActivationsPerHour: 3  # Maximum 3 activations per hour
  triggers:
    - type: ContainerRestart
```

**Behavior:**

* Activation is skipped if the limit is exceeded within the last hour
* Uses a rolling window - only activations from the last hour are counted
* Old activation timestamps (>1 hour) are automatically cleaned up
* Annotation size is limited to 100 timestamps to prevent unbounded growth
* Works per (pod, boost) combination

#### Combined Cooldown Policies

Both policies can be used together. The minimum interval is checked first:

```yaml
spec:
  cooldown:
    minIntervalSeconds: 300      # 5 minutes between activations
    maxActivationsPerHour: 3     # Max 3 activations per hour
  triggers:
    - type: ContainerRestart
    - type: PodConditionTransition
      conditionType: Ready
      fromStatus: "False"
      toStatus: "True"
```

**Behavior:**

* If minimum interval is not met, activation is skipped (regardless of rate
  limit)
* If minimum interval is met but rate limit is exceeded, activation is skipped
* Both policies must be satisfied for activation to proceed

#### Cooldown State Persistence

Cooldown state is stored in pod annotations (not controller memory) and
survives controller restarts:

* **State Storage**: All cooldown state is persisted in pod annotations as
  JSON-encoded data
* **Versioned Format**: State format includes a version field ("1") for future
  migration support
* **Backward Compatibility**: Pods without cooldown state or with old state
  formats are handled gracefully
* **State Components**:
  * Last activation time per trigger type
  * Activation history (timestamps from the last hour)
  * Automatic cleanup of old timestamps (>1 hour)
  * Defensive handling of malformed or future timestamps (clock skew
    protection)

**State Format:**

The cooldown state is stored in the pod annotation
`autoscaling.x-k8s.io/startup-cpu-boost` as part of the `activationState`
object:

```json
{
  "activationState": {
    "version": "1",
    "lastActivationTime": {
      "ContainerRestart": "2024-01-15T10:30:00Z"
    },
    "activationHistory": [
      "2024-01-15T10:30:00Z",
      "2024-01-15T10:25:00Z"
    ]
  }
}
```

**Controller Restart Behavior:**

When the controller restarts, it reads the cooldown state from pod annotations
and continues enforcing cooldown policies without interruption. This ensures
reliable cooldown enforcement even during controller upgrades or failures.

#### Observability

When a boost activation is skipped due to cooldown, a detailed Kubernetes
event is emitted for monitoring and troubleshooting:

**Event Properties:**

* Event type: `Warning`
* Event reason: `BoostSkippedCooldown`
* Event message includes:
  * Boost name
  * Pod name
  * Trigger type (ContainerRestart or PodConditionTransition)
  * Reason (minimum interval or rate limit exceeded)
  * Last activation timestamp (if applicable)
  * Remaining cooldown time (if minimum interval triggered)
  * Current activation count (if rate limit triggered)
  * Current timestamp

**Example Event Message:**

```yaml
Boost 'my-boost' activation skipped for pod 'app-pod-123' (trigger: ContainerRestart). 
Reason: minimum interval not met (remaining: 4m0s). 
Last activation: 2024-01-15T10:30:00Z, remaining cooldown: 4m0s. 
Current activations in last hour: 2/5. 
Timestamp: 2024-01-15T10:31:00Z
```

**Viewing Events:**

You can view these events using:

```bash
# View all cooldown-skipped events
kubectl get events --field-selector reason=BoostSkippedCooldown

# View events for a specific pod
kubectl get events --field-selector involvedObject.name=app-pod-123

# View events in a specific namespace
kubectl get events -n my-namespace --field-selector reason=BoostSkippedCooldown
```

Events are searchable and observable through standard Kubernetes tooling,
enabling cluster administrators to monitor cooldown policy effectiveness and
tune cooldown parameters based on actual usage patterns.

#### Boost Skipped Events (Idempotency)

When a boost activation is skipped because the boost is already active
(idempotency check), a Kubernetes event is emitted:

**Event Properties:**

* Event type: `Normal`
* Event reason: `BoostSkippedActive`
* Event message includes:
  * Boost name
  * Pod name
  * Trigger type (ContainerRestart or PodConditionTransition)
  * Reason: boost already active (idempotency)
  * Current activation details (start time, duration, trigger type, expiry type)
  * Current timestamp

**Example Event Message:**

```yaml
Boost 'my-boost' activation skipped for pod 'app-pod-123' (trigger: ContainerRestart). 
Reason: boost already active (idempotency). 
Current activation started: 2024-01-15T10:30:00Z (duration: 2m0s). 
Current activation trigger: ContainerRestart, expiry: FixedDuration. 
Timestamp: 2024-01-15T10:32:00Z
```

**Viewing Events:**

You can view these events using:

```bash
# View all idempotency-skipped events
kubectl get events --field-selector reason=BoostSkippedActive

# View events for a specific pod
kubectl get events --field-selector involvedObject.name=app-pod-123

# View events in a specific namespace
kubectl get events -n my-namespace --field-selector reason=BoostSkippedActive
```

These events help cluster administrators understand when boost activations are
skipped due to idempotency, indicating that a boost is already active and
preventing duplicate activations.

#### Boost Expiration Events

When a boost expires (durationPolicy met), a Kubernetes event is emitted to
track the boost lifecycle end-to-end:

**Event Properties:**

* Event type: `Normal`
* Event reason: `BoostExpired`
* Event message includes:
  * Boost name
  * Pod name
  * Trigger type (ContainerRestart or PodConditionTransition)
  * Total boost duration (calculated from activation start time)
  * Duration policy type (FixedDuration or PodCondition)
  * Duration policy details (duration value or condition type/status)
  * Activation start timestamp
  * Expiration timestamp

**Example Event Message:**

```yaml
CPU boost 'my-boost' expired for pod 'app-pod-123' (trigger: ContainerRestart). 
Total boost duration: 5m0s. 
Duration policy type: FixedDuration (5m0s). 
Activation started: 2024-01-15T10:30:00Z. 
Expired at: 2024-01-15T10:35:00Z
```

**Viewing Events:**

You can view these events using:

```bash
# View all expiration events
kubectl get events --field-selector reason=BoostExpired

# View events for a specific pod
kubectl get events --field-selector involvedObject.name=app-pod-123

# View events in a specific namespace
kubectl get events -n my-namespace --field-selector reason=BoostExpired
```

These events provide visibility into boost lifecycle, helping cluster
administrators track when boosts expire and understand boost duration patterns.

## Configuration

Kube Startup CPU Boost operator can be configured with environmental variables.

| Variable | Type | Default | Description |
| --- | --- | --- | --- |
| `POD_NAMESPACE` | `string` | `kube-startup-cpu-boost-system` |  Kube Startup CPU Boost operator namespace |
| `MGR_CHECK_INTERVAL` | `int` | `5` | Duration in seconds between boost manager checks for time based boost duration policy |
| `LEADER_ELECTION` | `bool` | `false` | Enables leader election for controller manager |
| `METRICS_PROBE_BIND_ADDR` | `string` | `:8080` | Address the metrics endpoint binds to |
| `HEALTH_PROBE_BIND_ADDR` | `string` | `:8081` | Address the health probe endpoint binds to |
| `SECURE_METRICS` | `bool` | `false` | Determines if the metrics endpoint is served securely |
| `ZAP_LOG_LEVEL` | `int` | `0` | Log level for ZAP logger |
| `ZAP_DEVELOPMENT` | `bool` | `false` | Enables development mode for ZAP logger |
| `HTTP2` | `bool` | `false` | Determines if the HTTP/2 protocol is used for webhook and metrics servers|
| `REMOVE_LIMITS` | `bool` | `true` | Enables operator to remove container CPU limits during the boost time |
| `VALIDATE_FEATURE_ENABLED` | `bool` | `true` | Enables validation of required feature gate on operator's startup |

## Metrics

Kube Startup CPU Boost exposes [prometheus](https://prometheus.io) metrics to monitor the health of
the system and the status of Startup CPU Boosts.

| Metric name | Type | Description | Labels |
| --- | --- | --- | --- |
| `boost_configurations` | Gauge | Number of registered Kube Startup CPU Boost configuration | `namespace`: the namespace of a Kube Startup CPU Boost |
| `boost_containers_total` | Counter | Number of a containers which CPU resources were increased | `namespace`: the namespace of container's POD, `boost`: name of a Kube Startup CPU Boost that increased container resources  |
| `boost_containers_active` | Gauge | Number of a containers which CPU resources and not yet reverted to their original values | `namespace`: the namespace of container's POD, `boost`: name of a Kube Startup CPU Boost that increased container resources  |

### Scraping: Google Cloud Managed Service for Prometheus

The below [PodMonitoring](https://github.com/GoogleCloudPlatform/prometheus-engine/blob/main/doc/api.md#monitoring.googleapis.com/v1.PodMonitoring)
example can be used to scrape Kube Startup CPU Boost metrics with
[Google Managed Service for Prometheus](https://cloud.google.com/stackdriver/docs/managed-prometheus).

```yaml
apiVersion: monitoring.googleapis.com/v1
kind: PodMonitoring
metadata:
  labels:
    control-plane: controller-manager
    app.kubernetes.io/name: controller-manager-metrics-monitor
    app.kubernetes.io/instance: controller-manager-metrics-monitor
    app.kubernetes.io/component: metrics
    app.kubernetes.io/created-by: kube-startup-cpu-boost
    app.kubernetes.io/part-of: kube-startup-cpu-boost
  name: controller-manager-metrics-monitor
  namespace: kube-startup-cpu-boost-system
spec:
  selector:
    matchLabels:
      control-plane: controller-manager
  endpoints:
  - port: metrics
    interval: 15s

```

## License

[Apache License 2.0](LICENSE)
