# Packet Capture Operator for Kubernetes

A Kubernetes operator that enables declarative, cluster-wide packet capture using a `PacketCapture` Custom Resource Definition (CRD). Target traffic by pod labels, CIDR ranges, FQDNs, or capture at the node level — all without touching the host directly.

## Features

- **Pod-scoped capture** — attaches ephemeral containers via `kubectl debug` to capture inside a pod's network namespace
- **Node-wide capture** — runs a privileged Job on the node for host-level packet capture
- **Flexible selectors** — pod labels, namespace labels, CIDR ranges, FQDNs, ports & protocols
- **BPF filter support** — custom `tcpdump` filter expressions (`ip or arp`, `tcp port 443`, etc.)
- **Duration control** — `"30s"`, `"5m"`, `"1h"` — any Go duration string
- **Automatic garbage collection** — completed Job pods are deleted after capture finishes
- **Image pre-loading** — a DaemonSet pre-pulls `nicolaka/netshoot:latest` on all nodes at startup to minimize cold-start delay
- **Storage to host** — `.pcap` files written to `/var/lib/packet-captures/` on each node via HostPath volume

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      Kubernetes Cluster                     │
│                                                             │
│  packet-capture-system namespace                            │
│  ┌─────────────────────────────────────┐                    │
│  │  PacketCapture Controller           │                    │
│  │  - Watches PacketCapture CRs        │                    │
│  │  - Creates Jobs / ephemeral ctrs    │                    │
│  │  - GC: deletes completed Job pods   │                    │
│  └─────────────────────────────────────┘                    │
│  ┌─────────────────────────────────────┐                    │
│  │  Image Preloader DaemonSet          │                    │
│  │  - Runs on every node               │                    │
│  │  - Pre-pulls nicolaka/netshoot      │                    │
│  └─────────────────────────────────────┘                    │
│                                                             │
│  default namespace                                          │
│  PacketCapture CR ──► Controller ──►                        │
│                                                             │
│  [Pod-based]                    [Node-based]                │
│  Job (bitnami/kubectl)          Job (nicolaka/netshoot)     │
│   └─ kubectl debug               └─ tcpdump (hostNetwork)   │
│        └─ ephemeral container                               │
│             └─ tcpdump in pod netns                         │
│                                                             │
│  Capture files ──► /var/lib/packet-captures/ (HostPath)    │
└─────────────────────────────────────────────────────────────┘
```

## Prerequisites

- Kubernetes **1.25+** (ephemeral containers are GA)
- `kubectl` configured with cluster-admin access
- Go **1.22+** (to build from source)
- `podman` or `docker` (to build the container image)
- A local cluster for the demo: [kind](https://kind.sigs.k8s.io/) + [podman](https://podman.io/) recommended

---

## Build from Source

```bash
# 1. Clone the repository
git clone https://github.com/isovalent/packet-capture.git
cd packet-capture

# 2. Download Go dependencies
go mod tidy

# 3. Build the operator binary
make build
# Binary is output to: bin/manager

# 4. Build the container image
podman build --platform=linux/arm64 -t localhost/packet-capture-operator:latest .
# For amd64:
# podman build --platform=linux/amd64 -t localhost/packet-capture-operator:latest .
```

---

## Demo: Pod-to-Pod Capture on kind

This demo captures IPv4 traffic between two pods in a local kind cluster.

### Step 1 — Create a kind cluster

```bash
kind create cluster --name demo
export KUBECONFIG=$(kind get kubeconfig-path --name demo 2>/dev/null || echo ~/.kube/config)
```

### Step 2 — Load the operator image into kind nodes

```bash
podman save localhost/packet-capture-operator:latest -o /tmp/pco.tar

for node in $(kind get nodes --name demo); do
  podman cp /tmp/pco.tar $node:/tmp/pco.tar
  podman exec $node ctr -n k8s.io images import /tmp/pco.tar
done

rm /tmp/pco.tar
```

### Step 3 — Deploy the operator

```bash
# Install CRD
kubectl apply -f config/crd/bases/

# Install RBAC (ServiceAccounts, ClusterRole, ClusterRoleBinding)
kubectl apply -f config/rbac/

# Deploy the controller (uses imagePullPolicy: IfNotPresent)
kubectl apply -f config/manager/manager.yaml

# Wait for it to be ready
kubectl -n packet-capture-system rollout status deploy/packet-capture-controller-manager
```

Verify the image preloader DaemonSet is running on all nodes:

```bash
kubectl -n packet-capture-system get daemonset packet-capture-image-preloader
```

### Step 4 — Deploy demo workloads

```bash
# Source pod: netshoot (curl client)
kubectl run netshoot \
  --image=nicolaka/netshoot:latest \
  --labels="class=netshoot" \
  -- sleep infinity

# Destination pod: nginx (web server)
kubectl run proxy \
  --image=nginx:latest \
  --labels="app=proxy" \
  --expose --port=80

kubectl wait --for=condition=Ready pod/netshoot pod/proxy --timeout=60s
```

### Step 5 — Start a packet capture

```bash
kubectl apply -f examples/pod-to-pod-capture.yaml
```

The example captures traffic from pods labelled `class=netshoot` to pods labelled `app=proxy` on port 80, for 1 minute:

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

### Step 6 — Generate some traffic

```bash
# In a separate terminal, send HTTP requests during the capture window
kubectl exec netshoot -- sh -c 'for i in $(seq 1 20); do curl -s http://proxy/; sleep 2; done'
```

### Step 7 — Watch capture progress

```bash
kubectl get packetcaptures.capture.k8s.io -n default -w
```

Expected output:

```
NAME                 PHASE     DURATION   PACKETS   AGE
pod-to-pod-capture   Running   1m                   15s
pod-to-pod-capture   Completed 1m                   75s
```

Check the capture Jobs:

```bash
kubectl get jobs -n default | grep "^pc-"
kubectl logs -n default -l app=packet-capture
```

After the capture completes, the controller automatically deletes the Job pods (garbage collection).

### Step 8 — Retrieve and inspect the capture file

The `.pcap` file is on the node that ran the capture pod:

```bash
# Find which node the capture ran on
kubectl get pods -n default -l app=packet-capture -o wide

# Copy the file from the node (kind uses podman/docker containers as nodes)
node=$(kubectl get pods -n default -l app=packet-capture -o jsonpath='{.items[0].spec.nodeName}')
podman cp $node:/var/lib/packet-captures/ ./captures/

# Inspect with tcpdump
tcpdump -r captures/pod-to-pod-capture-netshoot-source.pcap -n -q | head -30

# Or open in Wireshark
wireshark captures/pod-to-pod-capture-netshoot-source.pcap
```

### Step 9 — Clean up

```bash
kubectl delete packetcaptures.capture.k8s.io pod-to-pod-capture
kubectl delete pod netshoot proxy
kubectl delete svc proxy
kind delete cluster --name demo
```

---

## More Examples

| Example | File |
|---|---|
| Capture all traffic on all nodes | `examples/basic-capture.yaml` |
| Pod-to-pod with port filter | `examples/pod-to-pod-capture.yaml` |
| CIDR-based capture | `examples/cidr-based-capture.yaml` |
| FQDN-based capture | `examples/fqdn-capture.yaml` |
| With PersistentVolume storage | `examples/persistent-storage-capture.yaml` |

---

## API Reference

### PacketCaptureSpec

| Field | Type | Description |
|---|---|---|
| `source` | `EndpointSelector` | Source endpoint (pod selector, CIDR, FQDN, ports) |
| `destination` | `EndpointSelector` | Destination endpoint |
| `duration` | `string` | Capture duration — Go duration format (`"1m"`, `"30s"`) |
| `maxPackets` | `int` | Max packets to capture (0 = unlimited) |
| `maxPacketSize` | `int` | Snapshot length per packet (default: 65535) |
| `filter` | `string` | Custom BPF filter expression (default: `"ip or arp"`) |
| `interface` | `string` | Network interface (default: `"any"`) |
| `nodeSelector` | `map[string]string` | Pin capture Jobs to specific nodes |

### EndpointSelector

| Field | Type | Description |
|---|---|---|
| `podSelector` | `LabelSelector` | Match pods by labels |
| `namespaceSelector` | `LabelSelector` | Match namespaces by labels |
| `cidr` | `[]string` | IP ranges in CIDR notation |
| `fqdn` | `[]string` | Fully qualified domain names |
| `ports` | `[]PortSelector` | Port + protocol pairs |

### PacketCaptureStatus

| Field | Type | Description |
|---|---|---|
| `phase` | `string` | `Pending` \| `Running` \| `Completed` \| `Failed` |
| `startTime` | `Time` | Capture start time |
| `endTime` | `Time` | Capture end time |
| `captureJobs` | `[]CaptureJobStatus` | Per-pod/node job status |
| `packetsCaptured` | `int64` | Total packets captured |
| `message` | `string` | Human-readable status |

---

## Troubleshooting

| Symptom | Check | Fix |
|---|---|---|
| Controller `ImagePullBackOff` | `kubectl -n packet-capture-system describe pod` | Load image into nodes; set `imagePullPolicy: IfNotPresent` |
| `leases is forbidden` | Controller logs | Apply updated `config/rbac/role.yaml` with `coordination.k8s.io/leases` |
| Capture shows 0 packets | Job logs: `kubectl logs -l app=packet-capture` | Start traffic after capture begins; check BPF filter |
| Job pods not cleaned up | `kubectl auth can-i delete pods --as=system:serviceaccount:packet-capture-system:packet-capture-controller-manager` | Ensure `delete` verb on `pods` in RBAC |
| Jobs stuck `Pending` | `kubectl describe pod -l app=packet-capture` | Check node taints; verify image is cached |

Full architecture and troubleshooting guide: [`docs/packet-capture-operator-architecture.md`](docs/packet-capture-operator-architecture.md)

---

## Security

- Capture Jobs run with `NET_ADMIN` + `NET_RAW` capabilities
- Pod-based capture uses `kubectl debug --profile=netadmin` (ephemeral container stays in pod netns)
- Node-based capture uses `hostNetwork: true`
- RBAC is scoped to the minimum required verbs

---

## License

Apache License 2.0

---

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feat/my-feature`)
3. Commit with sign-off (`git commit -s -m "feat: my feature"`)
4. Open a pull request

Signed-off-by: Piotr Jabłoński \<pijablon@cisco.com\>
