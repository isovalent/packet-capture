# How to View Packet Captures

## Current Limitation

The current implementation stores packet captures in `/tmp/capture.pcap` inside the job pods. Once the job completes, the pods enter "Succeeded" state and the files are no longer accessible via `kubectl cp`.

## Solutions

### Option 1: View Captures While Jobs Are Running (Quick)

Monitor the capture in real-time before it completes:

```bash
export KUBECONFIG=/Users/pijablon/Downloads/projects/kind/kubeconfig

# Find running capture pods
kubectl get pods -l app=packet-capture

# Copy the pcap file while pod is still running (before duration expires)
kubectl cp default/<pod-name>:/tmp/capture.pcap ./captures/<pod-name>.pcap

# View with tcpdump
tcpdump -r ./captures/<pod-name>.pcap

# Or open in Wireshark
wireshark ./captures/<pod-name>.pcap
```

### Option 2: Use Storage Backend (Recommended)

Configure the PacketCapture to use persistent storage. Update your capture spec:

```yaml
apiVersion: capture.k8s.io/v1alpha1
kind: PacketCapture
metadata:
  name: my-capture
spec:
  duration: "2m"
  
  # Add storage configuration
  storage:
    type: "pv"
    persistentVolumeClaim:
      claimName: "packet-capture-pvc"
```

First, create a PVC:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: packet-capture-pvc
  namespace: default
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 10Gi
  storageClassName: standard  # or your storage class
```

### Option 3: View Logs for Packet Summary

View the tcpdump summary from pod logs:

```bash
# List all capture jobs
kubectl get jobs -l app=packet-capture

# View logs from each capture pod
kubectl logs -l app=packet-capture --tail=100

# For a specific pod
kubectl logs packet-capture-cidr-capture-kind-worker-njtqh
```

This shows:
- Number of packets captured
- Packets received by filter
- Packets dropped by kernel

### Option 4: Extend Job Completion Time

Modify the capture job to keep pods alive longer after capture completes, giving you time to copy files:

Add to your PacketCapture spec:
```yaml
spec:
  duration: "2m"
  # Add a sleep after capture to keep pod alive
  postCaptureDelay: "5m"
```

## Current Workaround for Your Capture

Since your `cidr-capture` has already completed, the pcap files are lost. To capture again with accessible results:

### Quick Test with Short Duration

```bash
# Create a new capture with very short duration
cat <<EOF | kubectl apply -f -
apiVersion: capture.k8s.io/v1alpha1
kind: PacketCapture
metadata:
  name: test-capture
  namespace: default
spec:
  duration: "30s"
  filter: "tcp port 443 or tcp port 80"
  interface: "eth0"
  maxPackets: 1000
EOF

# Immediately start watching for the pod
kubectl get pods -l app=packet-capture -w

# In another terminal, copy files while pods are running:
# Wait ~10 seconds for pods to start, then:
for pod in $(kubectl get pods -l app=packet-capture,capture-name=test-capture -o name); do
  kubectl cp "default/${pod#pod/}:/tmp/capture.pcap" "./captures/${pod#pod/}.pcap" &
done
```

## Analyzing Captured Packets

Once you have the pcap files:

### Using tcpdump

```bash
# View all packets
tcpdump -r captures/pod-name.pcap

# View with timestamps
tcpdump -tttt -r captures/pod-name.pcap

# Filter specific traffic
tcpdump -r captures/pod-name.pcap 'tcp port 443'

# Show packet contents (hex + ASCII)
tcpdump -X -r captures/pod-name.pcap

# Count packets by type
tcpdump -r captures/pod-name.pcap | awk '{print $3}' | sort | uniq -c
```

### Using Wireshark

```bash
# Open in Wireshark GUI
wireshark captures/pod-name.pcap

# Or use tshark (CLI version)
tshark -r captures/pod-name.pcap

# Export specific fields
tshark -r captures/pod-name.pcap -T fields -e ip.src -e ip.dst -e tcp.port
```

### Merge Multiple Captures

```bash
# Merge all pcap files from different nodes
mergecap -w captures/merged.pcap captures/*.pcap

# View merged file
tcpdump -r captures/merged.pcap
```

## Next Steps

To make packet retrieval easier, I recommend:

1. **Add PersistentVolume support** - Modify the controller to mount PVCs
2. **Add S3/GCS upload** - Automatically upload captures to cloud storage
3. **Add sidecar container** - Keep a container running to serve pcap files via HTTP
4. **Extend TTL** - Keep completed pods around longer with `ttlSecondsAfterFinished`

Would you like me to implement any of these improvements?
