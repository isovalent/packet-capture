#!/bin/bash
set -e

KUBECONFIG="${KUBECONFIG:-/Users/pijablon/Downloads/projects/kind/kubeconfig}"
export KUBECONFIG="$KUBECONFIG"

echo "==> Deploying operator to kind cluster..."

# Deploy the operator with a public base image and we'll update it later
cat <<EOF | kubectl apply -f -
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
        runAsUser: 65532
      containers:
      - command:
        - /manager
        args:
        - --leader-elect
        image: localhost/packet-capture-operator:latest
        imagePullPolicy: IfNotPresent
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

echo ""
echo "✅ Deployment created!"
echo ""
echo "Note: The pod may fail to start because the image needs to be loaded into kind nodes."
echo ""
echo "To load the image manually, run these commands on each kind node:"
echo "  1. Find your kind node containers: docker ps | grep kind"
echo "  2. Copy image: docker cp /tmp/packet-capture-operator.tar <node-name>:/tmp/"
echo "  3. Load image: docker exec <node-name> ctr -n k8s.io images import /tmp/packet-capture-operator.tar"
echo ""
echo "Or use kind load if Docker is available:"
echo "  kind load image-archive /tmp/packet-capture-operator.tar"
echo ""
echo "Check status:"
echo "  kubectl get pods -n packet-capture-system"
echo "  kubectl logs -n packet-capture-system deployment/packet-capture-controller-manager"
