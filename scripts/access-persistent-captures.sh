#!/bin/bash
set -e

KUBECONFIG="${KUBECONFIG:-/Users/pijablon/Downloads/projects/kind/kubeconfig}"
OUTPUT_DIR="${1:-./captures}"

export KUBECONFIG="$KUBECONFIG"

echo "==> Accessing packet captures from persistent storage"
echo ""

# Create a temporary pod to access the PVC
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: capture-accessor
  namespace: default
spec:
  containers:
  - name: accessor
    image: alpine:latest
    command: ["/bin/sh", "-c", "sleep 3600"]
    volumeMounts:
    - name: captures
      mountPath: /captures
  volumes:
  - name: captures
    persistentVolumeClaim:
      claimName: packet-capture-pvc
  restartPolicy: Never
EOF

echo "Waiting for accessor pod to be ready..."
kubectl wait --for=condition=ready pod/capture-accessor --timeout=60s

echo ""
echo "==> Listing captured files:"
kubectl exec capture-accessor -- ls -lh /captures/

echo ""
echo "==> Copying capture files to local directory: $OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

# Get list of files
FILES=$(kubectl exec capture-accessor -- ls /captures/)

for FILE in $FILES; do
    echo "Copying $FILE..."
    kubectl cp "default/capture-accessor:/captures/$FILE" "$OUTPUT_DIR/$FILE"
done

echo ""
echo "==> Cleaning up accessor pod..."
kubectl delete pod capture-accessor

echo ""
echo "✅ Capture files downloaded to: $OUTPUT_DIR"
ls -lh "$OUTPUT_DIR"

echo ""
echo "==> To analyze captures:"
echo "  # View with tcpdump:"
echo "  tcpdump -r $OUTPUT_DIR/<filename>.pcap"
echo ""
echo "  # View with tcpdump (detailed):"
echo "  tcpdump -tttt -n -r $OUTPUT_DIR/<filename>.pcap"
echo ""
echo "  # Open in Wireshark:"
echo "  wireshark $OUTPUT_DIR/<filename>.pcap"
echo ""
echo "  # Merge all captures:"
echo "  mergecap -w $OUTPUT_DIR/merged.pcap $OUTPUT_DIR/*.pcap"
