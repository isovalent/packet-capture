#!/bin/bash
set -e

KUBECONFIG="${KUBECONFIG:-/Users/pijablon/Downloads/projects/kind/kubeconfig}"
IMAGE_TAR="${1:-/tmp/packet-capture-operator.tar}"
IMAGE_NAME="packet-capture-operator:latest"

export KUBECONFIG="$KUBECONFIG"

echo "==> Loading image to kind nodes..."

# Get all kind nodes
NODES=$(kubectl get nodes -o jsonpath='{.items[*].metadata.name}')

for NODE in $NODES; do
    echo "Loading image to node: $NODE"
    
    # Copy tar to node
    docker cp "$IMAGE_TAR" "$NODE:/tmp/image.tar"
    
    # Load image on node
    docker exec "$NODE" ctr -n k8s.io images import /tmp/image.tar
    
    # Clean up
    docker exec "$NODE" rm /tmp/image.tar
    
    echo "✓ Image loaded to $NODE"
done

echo ""
echo "✅ Image loaded to all nodes!"
echo ""
echo "Verify with:"
echo "  docker exec kind-control-plane crictl images | grep packet-capture"
