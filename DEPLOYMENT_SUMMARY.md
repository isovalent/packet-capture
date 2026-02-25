# Deployment Summary

## ✅ Completed Steps

1. **Built arm64 image** - `localhost/packet-capture-operator:latest`
2. **Saved image** - `/tmp/packet-capture-operator.tar`
3. **Deployed to kind cluster**:
   - ✅ Namespace: `packet-capture-system`
   - ✅ CRD: `packetcaptures.capture.k8s.io`
   - ✅ RBAC: ServiceAccount, ClusterRole, ClusterRoleBinding
   - ✅ Deployment: `packet-capture-controller-manager`

## 🔧 Manual Image Loading Required

Since Docker isn't available with your Podman setup, you need to manually load the image into kind nodes.

### Option 1: Using Podman Machine (Recommended)

If you have Podman machine running with Docker compatibility:

```bash
# Set Docker socket
export DOCKER_HOST="unix://${HOME}/.local/share/containers/podman/machine/podman-machine-default/podman.sock"

# Load image to kind
kind load image-archive /tmp/packet-capture-operator.tar --name kind
```

### Option 2: Manual Node-by-Node Loading

```bash
# Find kind nodes (they run as containers)
podman ps | grep kind

# For each node (replace <node-name> with actual container name):
podman cp /tmp/packet-capture-operator.tar <node-name>:/tmp/image.tar
podman exec <node-name> ctr -n k8s.io images import /tmp/image.tar
podman exec <node-name> rm /tmp/image.tar
```

Example:
```bash
podman cp /tmp/packet-capture-operator.tar kind-control-plane:/tmp/image.tar
podman exec kind-control-plane ctr -n k8s.io images import /tmp/image.tar

podman cp /tmp/packet-capture-operator.tar kind-worker:/tmp/image.tar
podman exec kind-worker ctr -n k8s.io images import /tmp/image.tar

podman cp /tmp/packet-capture-operator.tar kind-worker2:/tmp/image.tar
podman exec kind-worker2 ctr -n k8s.io images import /tmp/image.tar
```

### Option 3: Use Docker Desktop (if available)

If you have Docker Desktop installed:

```bash
kind load image-archive /tmp/packet-capture-operator.tar --name kind
```

## 📊 Verify Deployment

```bash
export KUBECONFIG=/Users/pijablon/Downloads/projects/kind/kubeconfig

# Check if image is loaded on nodes
kubectl get nodes -o wide
kubectl debug node/kind-control-plane -it --image=alpine -- crictl images | grep packet-capture

# Check operator status
kubectl get pods -n packet-capture-system
kubectl logs -n packet-capture-system deployment/packet-capture-controller-manager

# Check if operator is ready
kubectl get deployment -n packet-capture-system
```

## 🧪 Test with Example Capture

Once the operator pod is running:

```bash
# Apply a basic capture
kubectl apply -f examples/basic-capture.yaml

# Watch the capture
kubectl get pc -w

# Check capture details
kubectl describe pc basic-capture

# View capture jobs
kubectl get jobs -l app=packet-capture

# Check job logs
kubectl logs -l app=packet-capture
```

## 🐛 Troubleshooting

### Pod stuck in ImagePullBackOff

This means the image isn't loaded on the node. Follow the manual loading steps above.

```bash
kubectl get pods -n packet-capture-system
# If you see ImagePullBackOff, load the image to nodes
```

### Check which node needs the image

```bash
kubectl get pod -n packet-capture-system -o wide
# Note the NODE column, then load image to that specific node
```

### Verify image is loaded

```bash
# On each node
podman exec kind-control-plane crictl images | grep packet-capture
podman exec kind-worker crictl images | grep packet-capture
podman exec kind-worker2 crictl images | grep packet-capture
```

## 📝 Quick Commands Reference

```bash
# Set kubeconfig
export KUBECONFIG=/Users/pijablon/Downloads/projects/kind/kubeconfig

# Check everything
kubectl get all -n packet-capture-system
kubectl get pc -A

# View logs
kubectl logs -n packet-capture-system -l control-plane=controller-manager --tail=100 -f

# Delete a capture
kubectl delete pc basic-capture

# Restart operator
kubectl rollout restart deployment/packet-capture-controller-manager -n packet-capture-system
```

## 🎯 Next Steps

1. Load the image to kind nodes using one of the methods above
2. Verify the operator pod is running
3. Test with `examples/basic-capture.yaml`
4. Try other examples: `pod-to-pod-capture.yaml`, `cidr-based-capture.yaml`
