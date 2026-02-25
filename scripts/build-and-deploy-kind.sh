#!/bin/bash
set -e

# Configuration
IMG="${IMG:-packet-capture-operator:latest}"
KUBECONFIG="${KUBECONFIG:-/Users/pijablon/Downloads/projects/kind/kubeconfig}"
KIND_CLUSTER="${KIND_CLUSTER:-kind}"

echo "==> Building multi-arch image with Podman..."
podman build --platform=linux/amd64,linux/arm64 \
    --manifest "$IMG" \
    -f Dockerfile .

echo "==> Saving image to tar..."
podman save -o /tmp/packet-capture-operator.tar "$IMG"

echo "==> Loading image into kind cluster..."
export KUBECONFIG="$KUBECONFIG"
kind load image-archive /tmp/packet-capture-operator.tar --name "$KIND_CLUSTER"

echo "==> Deploying CRD..."
kubectl apply -f config/crd/bases/capture.k8s.io_packetcaptures.yaml

echo "==> Deploying RBAC..."
kubectl apply -f config/rbac/service_account.yaml
kubectl apply -f config/rbac/role.yaml
kubectl apply -f config/rbac/role_binding.yaml

echo "==> Deploying operator..."
kubectl apply -f config/manager/manager.yaml

echo "==> Waiting for operator to be ready..."
kubectl wait --for=condition=available --timeout=120s \
    deployment/packet-capture-controller-manager \
    -n packet-capture-system || true

echo "==> Checking operator status..."
kubectl get pods -n packet-capture-system

echo ""
echo "✅ Deployment complete!"
echo ""
echo "To test, run:"
echo "  export KUBECONFIG=$KUBECONFIG"
echo "  kubectl apply -f examples/basic-capture.yaml"
echo "  kubectl get pc -w"
