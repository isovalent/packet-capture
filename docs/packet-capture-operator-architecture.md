# Packet Capture Operator — Architecture & Operations Guide

## Table of Contents

1. [Overview](#overview)
2. [Architecture](#architecture)
3. [Components](#components)
4. [Capture Modes](#capture-modes)
5. [PacketCapture CRD Reference](#packetcapture-crd-reference)
6. [Lifecycle & State Machine](#lifecycle--state-machine)
7. [Garbage Collection](#garbage-collection)
8. [Image Pre-loading](#image-pre-loading)
9. [RBAC & Security](#rbac--security)
10. [Deployment](#deployment)
11. [Usage Examples](#usage-examples)
12. [Troubleshooting](#troubleshooting)

---

## Overview

The Packet Capture Operator is a Kubernetes operator that automates network packet capture on running pods and nodes. It exposes a `PacketCapture` custom resource (CRD) that allows users to declaratively capture IPv4/ARP traffic from specific pods or nodes, store the resulting `.pcap` files on the host filesystem, and automatically clean up all ephemeral resources after capture completes.

**Key capabilities:**

- Capture traffic scoped to a specific pod's network namespace using `kubectl debug` ephemeral containers
- Capture traffic at the node level using privileged Jobs with `hostNetwork`
- BPF filter support (`ip or arp` by default, fully customizable)
- Configurable duration, packet count limits, and snapshot length
- Automatic garbage collection of completed Job pods
- Image pre-loading on all nodes via a DaemonSet to minimize cold-start latency

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                        Kubernetes Cluster                           │
│                                                                     │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │              packet-capture-system namespace                 │   │
│  │                                                              │   │
│  │  ┌────────────────────────────────────────┐                  │   │
│  │  │  PacketCapture Controller (Deployment) │                  │   │
│  │  │  - Watches PacketCapture CRs           │                  │   │
│  │  │  - Creates Jobs / ephemeral containers │                  │   │
│  │  │  - Manages lifecycle & GC              │                  │   │
│  │  └────────────────────────────────────────┘                  │   │
│  │                                                              │   │
│  │  ┌────────────────────────────────────────┐                  │   │
│  │  │  Image Preloader DaemonSet             │                  │   │
│  │  │  - Runs on every node                  │                  │   │
│  │  │  - Pulls nicolaka/netshoot:latest      │                  │   │
│  │  │  - Ensures /var/lib/packet-captures    │                  │   │
│  │  └────────────────────────────────────────┘                  │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │                    default namespace                         │   │
│  │                                                              │   │
│  │  PacketCapture CR ──► Controller reconciles ──►             │   │
│  │                                                              │   │
│  │  [Pod-based mode]          [Node-based mode]                 │   │
│  │  Job (bitnami/kubectl)     Job (nicolaka/netshoot)           │   │
│  │    └─ kubectl debug          └─ tcpdump (hostNetwork)        │   │
│  │         └─ ephemeral container                               │   │
│  │              └─ tcpdump in pod netns                         │   │
│  │                                                              │   │
│  │  Capture files ──► /var/lib/packet-captures/ (HostPath)     │   │
│  └──────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

---

## Components

### 1. PacketCapture Controller (`controllers/packetcapture_controller.go`)

The main reconciliation loop. Watches `PacketCapture` custom resources and drives the full lifecycle:

| Method | Responsibility |
|---|---|
| `Reconcile` | Entry point; routes to phase handlers |
| `handlePending` | Decides pod-based vs node-based capture, creates Jobs |
| `handlePodBasedCapture` | Creates one Job per target pod (source + destination) |
| `handleNodeBasedCapture` | Creates one Job per target node |
| `handleRunning` | Polls Job status; transitions to Completed/Failed |
| `completeCapture` | Marks CR as Completed; triggers GC |
| `handleDeletion` | Finalizer cleanup; deletes Jobs and pods |
| `cleanupJobPods` | Deletes completed/failed Job pods (garbage collection) |
| `preloadImagesRunnable` | Runnable that creates the image preloader DaemonSet at startup |

### 2. Pod Capture Logic (`controllers/pod_capture.go`)

Handles pod-targeted captures:

| Function | Responsibility |
|---|---|
| `getTargetPods` | Lists pods matching source/destination label selectors |
| `getPodsMatchingSelector` | Filters running pods by `LabelSelector` |
| `createEphemeralContainer` | Creates a Kubernetes Job that runs `kubectl debug` against the target pod |
| `buildPodCaptureCommand` | Builds the `tcpdump` command with BPF filter, duration, output path |
| `createDebugJob` | Assembles the `batchv1.Job` spec for pod-based capture |
| `buildCaptureJobName` | Generates a DNS-safe Job name from capture name + pod name + direction |

### 3. PacketCapture CRD (`api/v1alpha1/packetcapture_types.go`)

Custom resource definition with full spec and status.

### 4. Image Preloader DaemonSet

Created automatically at controller startup. Runs on every node in the cluster.

- **Init container**: pulls `nicolaka/netshoot:latest` with `PullAlways`
- **Main container**: lightweight `pause` container that keeps the pod alive
- **Side effect**: also ensures `/var/lib/packet-captures/` directory exists on each node via a HostPath volume mount

### 5. Capture Jobs

Two types depending on capture mode:

| Type | Image | Network | Use case |
|---|---|---|---|
| Pod-based orchestrator | `bitnami/kubectl:latest` | Pod network | Runs `kubectl debug` to attach ephemeral container to target pod |
| Pod-based ephemeral | `nicolaka/netshoot:latest` | Pod's network namespace | Runs `tcpdump` inside the pod's netns |
| Node-based | `nicolaka/netshoot:latest` | `hostNetwork: true` | Captures all traffic on the node |

---

## Capture Modes

### Pod-Based Capture (default when `podSelector` is specified)

Used when `source.podSelector` or `destination.podSelector` is set in the spec.

**Flow:**

```
Controller
  └─► Creates Job (bitnami/kubectl) per target pod
        └─► Job runs shell script:
              1. kubectl debug -n <ns> <pod>
                   --profile=netadmin
                   --image=nicolaka/netshoot:latest
                   --target=<container>
                   -- sh -c 'timeout -s TERM <N> tcpdump -i any -w /tmp/<file> -U "ip or arp"'  &
              2. sleep <duration>
              3. kubectl cp <ns>/<pod>:/tmp/<file> /captures/<file> -c <ephemeral-container>
              4. kill kubectl debug process
              5. ls -lh /captures/
```

**Key properties:**
- Captures traffic **inside the pod's network namespace** — only traffic the pod itself sends/receives
- Uses `--profile=netadmin` for `NET_ADMIN` and `NET_RAW` capabilities inside the ephemeral container
- `tcpdump` writes to `/tmp` inside the ephemeral container; the Job copies it to the HostPath volume
- Job `ActiveDeadlineSeconds` = capture duration + 120s buffer

### Node-Based Capture (fallback when no `podSelector`)

Used when no pod selectors are specified, or `nodeSelector` is set.

**Flow:**

```
Controller
  └─► Creates Job (nicolaka/netshoot) per target node
        └─► Job runs tcpdump directly:
              timeout <N> tcpdump -i <iface> -w /captures/<file> -U '<filter>'
```

**Key properties:**
- Captures **all traffic on the node** — not scoped to a single pod
- `hostNetwork: true` with `NET_ADMIN` + `NET_RAW` capabilities
- Scheduled on the target node via `NodeName`
- Tolerates all taints

---

## PacketCapture CRD Reference

```yaml
apiVersion: capture.k8s.io/v1alpha1
kind: PacketCapture
metadata:
  name: my-capture
  namespace: default
spec:
  # Source pod selector (optional)
  source:
    podSelector:
      matchLabels:
        app: frontend
    ports:
      - port: 80
        protocol: TCP
    cidr:
      - "10.0.0.0/8"

  # Destination pod selector (optional)
  destination:
    podSelector:
      matchLabels:
        app: backend
    ports:
      - port: 8080
        protocol: TCP

  # Capture duration (required) — Go duration format
  duration: "2m"

  # Max packets to capture (0 = unlimited)
  maxPackets: 0

  # Snapshot length per packet in bytes (default: 65535)
  maxPacketSize: 65535

  # BPF filter (default: "ip or arp")
  filter: "ip or arp"

  # Network interface (default: "any")
  interface: "any"

  # Node selector for node-based capture (optional)
  nodeSelector:
    kubernetes.io/os: linux

  # Storage configuration
  storage:
    type: "PersistentVolume"
    persistentVolumeClaim: "packet-capture-pvc"
    retentionDays: 7
```

### Status Fields

```yaml
status:
  phase: "Completed"          # Pending | Running | Completed | Failed
  startTime: "2026-02-19T..."
  endTime: "2026-02-19T..."
  message: "Capture completed successfully"
  captureJobs:
    - nodeName: "kind-worker2"
      jobName: "pc-my-capture-source-netshoot"
      status: "Running"
      captureFile: "/var/lib/packet-captures/my-capture-netshoot-source.pcap"
  packetsCaptured: 0
  captureFiles: []
```

### Short name

```bash
kubectl get pc          # short name for PacketCapture
```

---

## Lifecycle & State Machine

```
                    ┌─────────┐
   kubectl apply ──►│ Pending │
                    └────┬────┘
                         │ Jobs created
                         ▼
                    ┌─────────┐
                    │ Running │◄── requeue every 10s
                    └────┬────┘
                         │ All Jobs complete
              ┌──────────┴──────────┐
              ▼                     ▼
        ┌──────────┐          ┌────────┐
        │Completed │          │ Failed │
        └──────────┘          └────────┘
              │
              ▼
        GC: delete completed Job pods
```

**Finalizer:** `capture.k8s.io/finalizer` is added to every `PacketCapture`. On deletion, the finalizer ensures Jobs and their pods are cleaned up before the CR is removed.

---

## Garbage Collection

The controller automatically deletes completed Job pods after a capture finishes or when the `PacketCapture` CR is deleted.

**Trigger points:**

1. `completeCapture()` — called when all Jobs succeed
2. `handleDeletion()` — called when the CR is deleted (via finalizer)

**Logic (`cleanupJobPods`):**

1. Lists all pods in the capture's namespace
2. Matches pods by `job-name` label against `status.captureJobs[].jobName`
3. Deletes pods in `Succeeded` or `Failed` phase

**Verify GC is working:**

```bash
# After capture completes, no pc- pods should remain
kubectl get pods -n default | grep '^pc-'
```

---

## Image Pre-loading

At controller startup, a `packet-capture-image-preloader` DaemonSet is created in `packet-capture-system`. It runs on every node and pre-pulls `nicolaka/netshoot:latest`.

**Why:** Ephemeral containers and Job pods use `ImagePullPolicy: IfNotPresent`. Without pre-loading, the first capture on a node would be delayed by the image pull (~200MB).

**DaemonSet spec summary:**

```
Init container:  nicolaka/netshoot:latest  (PullAlways — ensures latest)
Main container:  pause:3.1                 (keeps pod alive)
HostPath volume: /var/lib/packet-captures  (creates directory on node)
Tolerations:     all taints                (runs on every node)
```

**Check preloader status:**

```bash
kubectl -n packet-capture-system get daemonset packet-capture-image-preloader
kubectl -n packet-capture-system get pods -l app=packet-capture-image-preloader -o wide
```

---

## RBAC & Security

### Controller ClusterRole (`manager-role`)

| API Group | Resource | Verbs |
|---|---|---|
| `apps` | `daemonsets` | full |
| `batch` | `jobs` | full |
| `coordination.k8s.io` | `leases` | get, list, watch, create, update, patch |
| `capture.k8s.io` | `packetcaptures` | full |
| `capture.k8s.io` | `packetcaptures/status` | get, patch, update |
| `capture.k8s.io` | `packetcaptures/finalizers` | update |
| `""` (core) | `nodes` | get, list, watch |
| `""` (core) | `pods` | full |

### Job ServiceAccount (`packet-capture-job`)

The Job pods (orchestrator containers running `kubectl debug`) use the `packet-capture-job` ServiceAccount. This account needs:

- `pods/exec` — to exec into pods
- `pods/ephemeralcontainers` — to attach ephemeral containers
- `pods` get/list — to read pod specs

### Ephemeral Container Capabilities

The `kubectl debug --profile=netadmin` profile grants the ephemeral container:
- `NET_ADMIN` — required for promiscuous mode and interface manipulation
- `NET_RAW` — required for raw socket access by tcpdump

---

## Deployment

### Prerequisites

- Kubernetes 1.25+ (ephemeral containers GA)
- `kubectl` configured with cluster access
- `podman` or `docker` for building the operator image

### Build and Deploy

```bash
# 1. Build the operator binary
make build

# 2. Build the container image (for kind/local clusters)
podman build --platform=linux/arm64 -t localhost/packet-capture-operator:latest .

# 3. Load image into kind nodes
podman save localhost/packet-capture-operator:latest -o /tmp/packet-capture-operator.tar
for node in kind-control-plane kind-worker kind-worker2; do
  podman cp /tmp/packet-capture-operator.tar $node:/tmp/
  podman exec $node ctr -n k8s.io images import /tmp/packet-capture-operator.tar
done
rm /tmp/packet-capture-operator.tar

# 4. Apply CRD, RBAC, and controller deployment
kubectl apply -f config/crd/bases/
kubectl apply -f config/rbac/
kubectl apply -f config/manager/manager.yaml

# 5. Verify controller is running
kubectl -n packet-capture-system rollout status deploy/packet-capture-controller-manager
kubectl -n packet-capture-system get pods
```

### Verify Image Preloader

```bash
kubectl -n packet-capture-system get daemonset packet-capture-image-preloader
# Expected: DESIRED=CURRENT=READY=number of nodes
```

### Run a Capture

```bash
kubectl apply -f examples/pod-to-pod-capture.yaml

# Watch status
kubectl get pc pod-to-pod-capture -w

# Check Jobs
kubectl get jobs -n default | grep '^pc-'

# Check Job logs
kubectl logs -n default -l job-name=pc-pod-to-pod-capture-source-netshoot
```

### Copy Capture Files from Nodes

```bash
for node in kind-control-plane kind-worker kind-worker2; do
  for file in $(podman exec $node ls /var/lib/packet-captures/ 2>/dev/null); do
    podman cp $node:/var/lib/packet-captures/$file ./captures/$file
    echo "Copied: $file from $node"
  done
done
```

### Inspect Capture Files

```bash
# Show first 20 packets
tcpdump -r captures/pod-to-pod-capture-netshoot-source.pcap -n -c 20

# Show only HTTP traffic
tcpdump -r captures/pod-to-pod-capture-netshoot-source.pcap -n 'tcp port 80'

# Show packet summary
tcpdump -r captures/pod-to-pod-capture-netshoot-source.pcap -q
```

---

## Usage Examples

### Example 1: Pod-to-Pod Capture

Capture traffic between a `netshoot` source pod and `proxy` destination pods on port 80:

```yaml
apiVersion: capture.k8s.io/v1alpha1
kind: PacketCapture
metadata:
  name: pod-to-pod-capture
  namespace: default
spec:
  source:
    podSelector:
      matchLabels:
        class: netshoot
  destination:
    podSelector:
      matchLabels:
        app: proxy
    ports:
      - port: 80
        protocol: TCP
  duration: "1m"
  interface: "any"
```

### Example 2: Node-Wide Capture

Capture all traffic on worker nodes:

```yaml
apiVersion: capture.k8s.io/v1alpha1
kind: PacketCapture
metadata:
  name: node-capture
  namespace: default
spec:
  duration: "5m"
  interface: "eth0"
  filter: "ip or arp"
  nodeSelector:
    node-role.kubernetes.io/worker: ""
```

### Example 3: Custom BPF Filter

Capture only DNS traffic:

```yaml
apiVersion: capture.k8s.io/v1alpha1
kind: PacketCapture
metadata:
  name: dns-capture
  namespace: default
spec:
  source:
    podSelector:
      matchLabels:
        app: frontend
  duration: "2m"
  filter: "udp port 53"
```

---

## Troubleshooting

### Controller not starting

```bash
kubectl -n packet-capture-system describe pod -l control-plane=controller-manager
kubectl -n packet-capture-system logs -l control-plane=controller-manager
```

**Common causes:**

| Error | Fix |
|---|---|
| `ImagePullBackOff` | Re-import image into kind nodes; set `imagePullPolicy: IfNotPresent` in deployment |
| `leases is forbidden` | Apply updated `config/rbac/role.yaml` with `coordination.k8s.io/leases` permissions |
| `events is forbidden` | Non-fatal; controller still works. Add `""` events create permission to RBAC if needed |

### Capture shows 0 packets

```bash
# Check ephemeral container logs
EPHEM=$(kubectl get pod <target-pod> -n default -o jsonpath='{.spec.ephemeralContainers[-1].name}')
kubectl logs -n default <target-pod> -c $EPHEM
```

**Common causes:**

| Symptom | Cause | Fix |
|---|---|---|
| `0 packets captured` on source pod | Ephemeral container started after traffic window | Increase `duration` or start traffic after capture begins |
| `WARNING: any: That device doesn't support promiscuous mode` | Normal for `any` interface | Not an error; capture still works |
| Only IPv6 traffic visible | BPF filter not applied | Ensure `filter: "ip or arp"` is set |
| `tcpdump: command not found` | Wrong image | Ensure `nicolaka/netshoot:latest` is used |

### Job pods not cleaned up (GC not working)

```bash
# Check controller logs for GC activity
kubectl -n packet-capture-system logs -l control-plane=controller-manager | grep -i "garbage\|cleanup\|Deleting"

# Check RBAC — pods/delete must be allowed
kubectl auth can-i delete pods --as=system:serviceaccount:packet-capture-system:packet-capture-controller-manager
```

**Fix:** Ensure `config/rbac/role.yaml` includes `delete` verb for `pods`.

### Jobs stuck in Pending

```bash
kubectl describe job <job-name> -n default
kubectl describe pod -l job-name=<job-name> -n default
```

**Common causes:**

| Symptom | Cause | Fix |
|---|---|---|
| `ImagePullBackOff` | Image not cached on node | Wait for preloader DaemonSet to complete, or manually pull |
| `Unschedulable` | Node selector / taint mismatch | Check `nodeSelector` and tolerations in Job spec |

### Capture files not found on node

```bash
# Check if directory exists
podman exec kind-worker2 ls -lh /var/lib/packet-captures/

# Check Job logs for copy errors
kubectl logs -n default -l job-name=<job-name> | grep -i "copy\|error\|failed"
```

**Fix:** Ensure the preloader DaemonSet is running and the HostPath `/var/lib/packet-captures` exists on the node.

### Ephemeral containers not attaching

```bash
kubectl -n packet-capture-system logs -l control-plane=controller-manager | grep "ephemeral"
kubectl get pod <target-pod> -n default -o jsonpath='{.spec.ephemeralContainers}' | jq .
```

**Requirements:**
- Kubernetes 1.25+ (ephemeral containers are GA)
- `packet-capture-job` ServiceAccount must have `pods/ephemeralcontainers` permission
- Target pod must be in `Running` phase

---

## File Naming Convention

Capture files are stored at:

```
/var/lib/packet-captures/<capture-name>-<pod-name>-<direction>.pcap
```

Examples:
```
pod-to-pod-capture-netshoot-source.pcap
pod-to-pod-capture-rebel-base-5c54b7dd9c-4dh2g-destination.pcap
```
