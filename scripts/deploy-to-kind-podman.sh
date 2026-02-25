#!/bin/bash
set -e

KUBECONFIG="${KUBECONFIG:-/Users/pijablon/Downloads/projects/kind/kubeconfig}"
IMAGE_TAR="/tmp/packet-capture-operator.tar"

export KUBECONFIG="$KUBECONFIG"

echo "==> Creating namespace..."
kubectl create namespace packet-capture-system --dry-run=client -o yaml | kubectl apply -f -

echo "==> Deploying CRD..."
kubectl apply -f config/crd/bases/capture.k8s.io_packetcaptures.yaml

echo "==> Deploying RBAC..."
kubectl apply -f config/rbac/service_account.yaml
kubectl apply -f config/rbac/role.yaml
kubectl apply -f config/rbac/role_binding.yaml

echo "==> Creating temporary pod to load image..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: image-loader
  namespace: default
spec:
  hostNetwork: true
  hostPID: true
  containers:
  - name: loader
    image: alpine:latest
    command: ["/bin/sh", "-c", "sleep 3600"]
    securityContext:
      privileged: true
    volumeMounts:
    - name: containerd
      mountPath: /run/containerd
  volumes:
  - name: containerd
    hostPath:
      path: /run/containerd
  nodeSelector:
    kubernetes.io/hostname: kind-control-plane
EOF

echo "==> Waiting for loader pod..."
kubectl wait --for=condition=ready pod/image-loader --timeout=60s

echo "==> Copying image to pod..."
kubectl cp "$IMAGE_TAR" image-loader:/tmp/image.tar

echo "==> Loading image into containerd..."
kubectl exec image-loader -- sh -c "ctr -n k8s.io images import /tmp/image.tar"

echo "==> Cleaning up loader pod..."
kubectl delete pod image-loader

echo "==> Updating manager deployment to use local image..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Namespace
metadata:
  name: packet-capture-system
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: packet-capture-controller-manager
  namespace: packet-capture-system
  labels:
    control-plane: controller-manager
spec:
  selector:
    matchLabels:
      control-plane: controller-manager
  replicas: 1
  template:
    metadata:
      labels:
        control-plane: controller-manager
    spec:
      securityContext:
        runAsNonRoot: true
      containers:
      - command:
        - /manager
        args:
        - --leader-elect
        image: localhost/packet-capture-operator:latest
        imagePullPolicy: Never
        name: manager
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop:
            - "ALL"
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8081
          initialDelaySeconds: 15
          periodSeconds: 20
        readinessProbe:
          httpGet:
            path: /readyz
            port: 8081
          initialDelaySeconds: 5
          periodSeconds: 10
        resources:
          limits:
            cpu: 500m
            memory: 128Mi
          requests:
            cpu: 10m
            memory: 64Mi
      serviceAccountName: packet-capture-controller-manager
      terminationGracePeriodSeconds: 10
EOF

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
