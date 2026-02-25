# Pod-Based Packet Capture with Ephemeral Containers

## Overview

The packet capture operator now supports **pod-based packet capture** using Kubernetes ephemeral containers. This approach attaches debug containers directly to running pods for more accurate pod-to-pod traffic capture.

## Architecture

### Two Capture Modes

1. **Pod-Based Capture** (NEW)
   - Triggered when `source.podSelector` or `destination.podSelector` is specified
   - Creates ephemeral debug containers attached to matching pods
   - Captures traffic directly from pod network namespace
   - Capture files include pod names: `{capture-name}-{pod-name}-{direction}.pcap`

2. **Node-Based Capture** (Original)
   - Triggered when no pod selectors are specified
   - Creates Jobs on nodes with hostNetwork
   - Captures traffic at node level
   - Capture files include node names

### How It Works

When you create a PacketCapture with pod selectors:

1. **Pod Discovery**: Controller finds all pods matching source/destination selectors
2. **Ephemeral Container Creation**: For each pod, creates a debug container with:
   - Name: `capture-{capture-name}-{source|destination}`
   - Image: `nicolaka/netshoot:latest`
   - Capabilities: `NET_ADMIN`, `NET_RAW`
   - Shared network namespace with target pod
3. **Packet Capture**: tcpdump runs inside ephemeral container
4. **Storage**: Captures saved to PersistentVolume with pod name in filename

## Example: Pod-to-Pod Capture

```yaml
apiVersion: capture.k8s.io/v1alpha1
kind: PacketCapture
metadata:
  name: pod-to-pod-capture
  namespace: default
spec:
  # Capture traffic FROM pods with class=netshoot label
  source:
    podSelector:
      matchLabels:
        class: netshoot
  
  # Capture traffic TO pods with app=proxy label
  destination:
    podSelector:
      matchLabels:
        app: proxy
    ports:
    - port: 80
      protocol: TCP
  
  duration: "2m"
  interface: "any"
  
  # Save to persistent storage
  storage:
    type: "PersistentVolume"
    persistentVolumeClaim: "packet-capture-pvc"
```

### What Happens

If you have:
- 2 pods with `class=netshoot` (netshoot-1, netshoot-2)
- 3 pods with `app=proxy` (proxy-a, proxy-b, proxy-c)

The operator creates **5 ephemeral containers**:
- `capture-pod-to-pod-capture-source` on netshoot-1
- `capture-pod-to-pod-capture-source` on netshoot-2
- `capture-pod-to-pod-capture-destination` on proxy-a
- `capture-pod-to-pod-capture-destination` on proxy-b
- `capture-pod-to-pod-capture-destination` on proxy-c

### Capture Files

Files are saved with pod names:
```
pod-to-pod-capture-netshoot-1-source.pcap
pod-to-pod-capture-netshoot-2-source.pcap
pod-to-pod-capture-proxy-a-destination.pcap
pod-to-pod-capture-proxy-b-destination.pcap
pod-to-pod-capture-proxy-c-destination.pcap
```

## Implementation Details

### Code Structure

- **`controllers/pod_capture.go`**: Pod discovery and ephemeral container management
  - `getTargetPods()`: Finds pods matching selectors
  - `createEphemeralContainer()`: Attaches debug container to pod
  - `buildPodCaptureCommand()`: Builds tcpdump command for pod
  - `buildCaptureJobName()`: Creates unique name with pod name

- **`controllers/packetcapture_controller.go`**: Main reconciliation logic
  - `handlePending()`: Routes to pod-based or node-based capture
  - `handlePodBasedCapture()`: Orchestrates ephemeral container creation
  - `handleNodeBasedCapture()`: Original node-level capture

### Ephemeral Container Pattern

Based on cilium-cli implementation (`k8s/client.go`):

```go
// Create ephemeral container spec
ec := corev1.EphemeralContainer{
    EphemeralContainerCommon: corev1.EphemeralContainerCommon{
        Name:  "capture-{name}-{direction}",
        Image: "nicolaka/netshoot:latest",
        Command: []string{"/bin/sh", "-c", tcpdumpCmd},
        SecurityContext: &corev1.SecurityContext{
            Privileged: true,
            Capabilities: &corev1.Capabilities{
                Add: []corev1.Capability{"NET_ADMIN", "NET_RAW"},
            },
        },
        VolumeMounts: volumeMounts,
    },
}

// Patch pod to add ephemeral container
patch := strategicpatch.CreateTwoWayMergePatch(oldPod, newPod, pod)
r.Patch(ctx, pod, client.RawPatch(types.StrategicMergePatchType, patch))
```

## Usage

### 1. Deploy a Test Capture

```bash
export KUBECONFIG=/Users/pijablon/Downloads/projects/kind/kubeconfig

# Apply the pod-to-pod capture example
kubectl apply -f examples/pod-to-pod-capture.yaml

# Watch the capture
kubectl get packetcaptures.capture.k8s.io pod-to-pod-capture -w
```

### 2. Verify Ephemeral Containers

```bash
# Check pods for ephemeral containers
kubectl get pods -l class=netshoot -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{range .spec.ephemeralContainers[*]}  - {.name}{"\n"}{end}{end}'

# View ephemeral container logs
kubectl logs <pod-name> -c capture-pod-to-pod-capture-source
```

### 3. Access Capture Files

```bash
# Download all captures
./scripts/access-persistent-captures.sh

# Files will include pod names
ls -lh captures/
# pod-to-pod-capture-netshoot-xxx-source.pcap
# pod-to-pod-capture-proxy-xxx-destination.pcap
```

### 4. Analyze Traffic

```bash
# View specific pod's traffic
tcpdump -tttt -n -r captures/pod-to-pod-capture-netshoot-1-source.pcap

# Filter for specific destination
tcpdump -r captures/pod-to-pod-capture-netshoot-1-source.pcap 'dst host 10.244.x.x'

# Open in Wireshark
wireshark captures/pod-to-pod-capture-netshoot-1-source.pcap
```

## Benefits

1. **Accurate Pod Traffic**: Captures from pod's network namespace, not node
2. **Selective Capture**: Only target specific pods, not all traffic on node
3. **Pod Identification**: Filenames include pod names for easy correlation
4. **Matrix Coverage**: N source pods × M destination pods = complete capture matrix
5. **No Node Access**: Works without node-level permissions
6. **Persistent**: Ephemeral containers remain until pod deletion

## Limitations

1. **Requires Running Pods**: Can only attach to pods in Running state
2. **Kubernetes 1.23+**: Ephemeral containers require recent Kubernetes
3. **Storage**: Requires PersistentVolume with ReadWriteMany for multi-pod access
4. **Cleanup**: Ephemeral containers persist until pod is deleted

## Troubleshooting

### Ephemeral Container Not Created

```bash
# Check operator logs
kubectl logs -n packet-capture-system deployment/packet-capture-controller-manager --tail=50

# Verify pod is running
kubectl get pod <pod-name> -o jsonpath='{.status.phase}'

# Check RBAC permissions
kubectl auth can-i patch pods --as=system:serviceaccount:packet-capture-system:packet-capture-controller-manager
```

### No Capture Files

```bash
# Check ephemeral container status
kubectl get pod <pod-name> -o jsonpath='{.status.ephemeralContainerStatuses[*].state}'

# View container logs
kubectl logs <pod-name> -c capture-{name}-{direction}

# Verify PVC is mounted
kubectl exec <pod-name> -c capture-{name}-{direction} -- ls -la /captures
```

## Future Enhancements

- [ ] Auto-cleanup ephemeral containers after capture completes
- [ ] Support for sidecar pattern (long-running captures)
- [ ] Real-time streaming of capture data
- [ ] Integration with service mesh (Istio, Linkerd)
- [ ] Automatic traffic correlation between source and destination
