# Quick Start Guide

## Prerequisites

- Kubernetes cluster (v1.24+)
- kubectl configured
- Go 1.22+ (for building from source)
- Docker (for building container image)

## Quick Deploy (5 minutes)

### 1. Install CRD

```bash
cd ~/Downloads/projects/2026/packet-capture
kubectl apply -f config/crd/bases/capture.k8s.io_packetcaptures.yaml
```

### 2. Deploy Operator

```bash
# Create namespace and RBAC
kubectl apply -f config/rbac/service_account.yaml
kubectl apply -f config/rbac/role.yaml
kubectl apply -f config/rbac/role_binding.yaml

# Build and load image (if using kind/minikube)
make docker-build IMG=packet-capture-operator:latest
kind load docker-image packet-capture-operator:latest  # or minikube image load

# Deploy controller
kubectl apply -f config/manager/manager.yaml
```

### 3. Verify Installation

```bash
# Check operator is running
kubectl get pods -n packet-capture-system

# Check CRD is installed
kubectl get crd packetcaptures.capture.k8s.io
```

## First Capture

### Basic 5-minute capture on all nodes:

```bash
kubectl apply -f examples/basic-capture.yaml
```

### Monitor the capture:

```bash
# Watch status
kubectl get pc -w

# Get detailed info
kubectl describe pc basic-capture

# View capture jobs
kubectl get jobs -l app=packet-capture

# Check logs
kubectl logs -l app=packet-capture --tail=50
```

## Common Use Cases

### Capture Pod-to-Pod Traffic

```bash
kubectl apply -f examples/pod-to-pod-capture.yaml
```

### Capture External Traffic (CIDR-based)

```bash
kubectl apply -f examples/cidr-based-capture.yaml
```

### Capture DNS Traffic to Specific Domains

```bash
kubectl apply -f examples/fqdn-capture.yaml
```

## Development

### Build Locally

```bash
# Build binary
make build

# Run locally (uses your current kubeconfig)
make run
```

### Run Tests

```bash
make test
```

### Generate CRD/Code

```bash
# Regenerate CRD manifests
make manifests

# Regenerate DeepCopy methods
make generate
```

## Troubleshooting

### Operator not starting

```bash
kubectl logs -n packet-capture-system deployment/packet-capture-controller-manager
```

### Capture stuck in Pending

Check if nodes match selector:
```bash
kubectl get nodes --show-labels
```

### No packets captured

Check capture job logs:
```bash
kubectl logs job/packet-capture-<name>-<node>
```

## Clean Up

```bash
# Delete all captures
kubectl delete pc --all

# Uninstall operator
kubectl delete -f config/manager/manager.yaml
kubectl delete -f config/rbac/

# Remove CRD
kubectl delete -f config/crd/bases/capture.k8s.io_packetcaptures.yaml
```

## Next Steps

- Read full [README.md](README.md) for detailed documentation
- Explore [examples/](examples/) for more use cases
- Customize storage backends for production use
- Set up monitoring and alerting
