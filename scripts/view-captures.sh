#!/bin/bash
set -e

KUBECONFIG="${KUBECONFIG:-/Users/pijablon/Downloads/projects/kind/kubeconfig}"
CAPTURE_NAME="${1}"
OUTPUT_DIR="${2:-./captures}"

export KUBECONFIG="$KUBECONFIG"

if [ -z "$CAPTURE_NAME" ]; then
    echo "Usage: $0 <capture-name> [output-dir]"
    echo ""
    echo "Available captures:"
    kubectl get packetcaptures.capture.k8s.io -A
    exit 1
fi

echo "==> Retrieving packet captures for: $CAPTURE_NAME"
echo ""

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Find all pods for this capture
PODS=$(kubectl get pods -l "app=packet-capture,capture-name=$CAPTURE_NAME" -o jsonpath='{.items[*].metadata.name}')

if [ -z "$PODS" ]; then
    echo "No capture pods found for: $CAPTURE_NAME"
    echo "Checking jobs..."
    kubectl get jobs -l app=packet-capture
    exit 1
fi

echo "Found capture pods:"
for POD in $PODS; do
    echo "  - $POD"
done
echo ""

# Copy pcap files from each pod
for POD in $PODS; do
    echo "==> Copying capture from pod: $POD"
    
    # Check if pod has completed
    STATUS=$(kubectl get pod "$POD" -o jsonpath='{.status.phase}')
    
    if [ "$STATUS" != "Succeeded" ] && [ "$STATUS" != "Failed" ]; then
        echo "Warning: Pod $POD is in status: $STATUS (expected Succeeded)"
    fi
    
    # Try to copy the pcap file
    PCAP_FILE="${OUTPUT_DIR}/${POD}.pcap"
    
    if kubectl cp "${POD}:/tmp/capture.pcap" "$PCAP_FILE" 2>/dev/null; then
        FILE_SIZE=$(ls -lh "$PCAP_FILE" | awk '{print $5}')
        echo "✓ Saved to: $PCAP_FILE ($FILE_SIZE)"
        
        # Show packet count
        if command -v tcpdump &> /dev/null; then
            PACKET_COUNT=$(tcpdump -r "$PCAP_FILE" 2>/dev/null | wc -l)
            echo "  Packets captured: $PACKET_COUNT"
        fi
    else
        echo "✗ Failed to copy capture from $POD"
        echo "  Checking pod logs for errors..."
        kubectl logs "$POD" --tail=10
    fi
    echo ""
done

echo "==> Summary"
echo "Captures saved to: $OUTPUT_DIR"
ls -lh "$OUTPUT_DIR"/*.pcap 2>/dev/null || echo "No pcap files found"

echo ""
echo "To view captures with tcpdump:"
echo "  tcpdump -r $OUTPUT_DIR/<pod-name>.pcap"
echo ""
echo "To view in Wireshark:"
echo "  wireshark $OUTPUT_DIR/<pod-name>.pcap"
echo ""
echo "To merge all captures:"
echo "  mergecap -w $OUTPUT_DIR/merged.pcap $OUTPUT_DIR/*.pcap"
