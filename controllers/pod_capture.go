package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	capturev1alpha1 "github.com/packet-capture/operator/api/v1alpha1"
)

// PodCaptureTarget represents a pod that needs packet capture
type PodCaptureTarget struct {
	Pod       *corev1.Pod
	Direction string // "source" or "destination"
}

// getTargetPods returns all pods that match source and destination selectors
func (r *PacketCaptureReconciler) getTargetPods(ctx context.Context, capture *capturev1alpha1.PacketCapture) ([]PodCaptureTarget, error) {
	logger := log.FromContext(ctx)
	var targets []PodCaptureTarget

	// Get source pods
	if capture.Spec.Source != nil && capture.Spec.Source.PodSelector != nil {
		sourcePods, err := r.getPodsMatchingSelector(ctx, capture.Namespace, capture.Spec.Source.PodSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to get source pods: %w", err)
		}
		logger.Info("Found source pods", "count", len(sourcePods))
		for i := range sourcePods {
			targets = append(targets, PodCaptureTarget{
				Pod:       &sourcePods[i],
				Direction: "source",
			})
		}
	}

	// Get destination pods
	if capture.Spec.Destination != nil && capture.Spec.Destination.PodSelector != nil {
		destPods, err := r.getPodsMatchingSelector(ctx, capture.Namespace, capture.Spec.Destination.PodSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to get destination pods: %w", err)
		}
		logger.Info("Found destination pods", "count", len(destPods))
		for i := range destPods {
			targets = append(targets, PodCaptureTarget{
				Pod:       &destPods[i],
				Direction: "destination",
			})
		}
	}

	// If no pod selectors, fall back to node-based capture
	if len(targets) == 0 {
		logger.Info("No pod selectors specified, will use node-based capture")
	}

	return targets, nil
}

// getPodsMatchingSelector returns pods matching the given label selector
func (r *PacketCaptureReconciler) getPodsMatchingSelector(ctx context.Context, namespace string, selector *metav1.LabelSelector) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}

	labelSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	listOpts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabelsSelector{Selector: labelSelector},
	}

	if err := r.List(ctx, podList, listOpts...); err != nil {
		return nil, err
	}

	// Filter out non-running pods
	var runningPods []corev1.Pod
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning {
			runningPods = append(runningPods, pod)
		}
	}

	return runningPods, nil
}

// buildCaptureJobName creates a unique job name for pod-based capture
func buildCaptureJobName(captureName, podName, direction string) string {
	// Sanitize pod name to be DNS-compliant
	sanitized := strings.ReplaceAll(podName, ".", "-")
	sanitized = strings.ToLower(sanitized)

	// Truncate if too long (max 63 chars for k8s names)
	prefix := fmt.Sprintf("pc-%s-%s-%s", captureName, direction, sanitized)
	if len(prefix) > 63 {
		prefix = prefix[:63]
	}

	return prefix
}

// buildPodCaptureCommand builds tcpdump command for pod-attached capture
func (r *PacketCaptureReconciler) buildPodCaptureCommand(capture *capturev1alpha1.PacketCapture, targetPod *corev1.Pod, direction string) string {
	// Write to /tmp in the ephemeral container
	outputFile := fmt.Sprintf("/tmp/%s-%s-%s.pcap",
		capture.Name,
		targetPod.Name,
		direction)

	// Use -i any to capture on all interfaces in the pod's network namespace
	// Use -U for unbuffered output
	cmd := fmt.Sprintf("tcpdump -i any -w %s -U", outputFile)

	if capture.Spec.MaxPackets > 0 {
		cmd += fmt.Sprintf(" -c %d", capture.Spec.MaxPackets)
	}

	if capture.Spec.MaxPacketSize > 0 {
		cmd += fmt.Sprintf(" -s %d", capture.Spec.MaxPacketSize)
	}

	// Add BPF filter - capture IPv4 and ARP like visual-inspector
	if capture.Spec.Filter != "" {
		cmd += fmt.Sprintf(" '%s'", capture.Spec.Filter)
	} else {
		// Default to IPv4 and ARP to capture pod's network traffic
		cmd += " 'ip or arp'"
	}

	// Add timeout based on duration - tcpdump runs for exactly X minutes
	duration, _ := time.ParseDuration(capture.Spec.Duration)
	cmd = fmt.Sprintf("timeout -s TERM %d %s", int(duration.Seconds()), cmd)

	return cmd
}

// buildBPFFilterForPod builds BPF filter specific to the pod's traffic
func (r *PacketCaptureReconciler) buildBPFFilterForPod(capture *capturev1alpha1.PacketCapture, targetPod *corev1.Pod, direction string) string {
	var filters []string

	// Add pod IP to filter
	if targetPod.Status.PodIP != "" {
		if direction == "source" {
			filters = append(filters, fmt.Sprintf("src host %s", targetPod.Status.PodIP))
		} else {
			filters = append(filters, fmt.Sprintf("dst host %s", targetPod.Status.PodIP))
		}
	}

	// Add port filters
	var portFilters []string
	if direction == "source" && capture.Spec.Source != nil {
		for _, port := range capture.Spec.Source.Ports {
			proto := strings.ToLower(port.Protocol)
			portFilters = append(portFilters, fmt.Sprintf("%s port %d", proto, port.Port))
		}
	} else if direction == "destination" && capture.Spec.Destination != nil {
		for _, port := range capture.Spec.Destination.Ports {
			proto := strings.ToLower(port.Protocol)
			portFilters = append(portFilters, fmt.Sprintf("%s port %d", proto, port.Port))
		}
	}

	if len(portFilters) > 0 {
		filters = append(filters, "("+strings.Join(portFilters, " or ")+")")
	}

	if len(filters) == 0 {
		return ""
	}

	return strings.Join(filters, " and ")
}

// createEphemeralContainer creates a Job that uses kubectl debug to attach to the target pod
func (r *PacketCaptureReconciler) createEphemeralContainer(ctx context.Context, capture *capturev1alpha1.PacketCapture, target PodCaptureTarget) error {
	logger := log.FromContext(ctx)

	// Build job name
	jobName := buildCaptureJobName(capture.Name, target.Pod.Name, target.Direction)

	// Build tcpdump command
	tcpdumpCmd := r.buildPodCaptureCommand(capture, target.Pod, target.Direction)

	// Create Job that runs kubectl debug
	job := r.createDebugJob(capture, target.Pod, jobName, tcpdumpCmd)

	// Set owner reference so the Job watch triggers reconciliation of the PacketCapture
	if err := ctrl.SetControllerReference(capture, job, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference: %w", err)
	}

	// Create the job
	if err := r.Create(ctx, job); err != nil {
		return fmt.Errorf("failed to create debug job: %w", err)
	}

	logger.Info("Created debug job for packet capture",
		"pod", target.Pod.Name,
		"namespace", target.Pod.Namespace,
		"job", jobName,
		"direction", target.Direction)

	return nil
}

// createDebugJob creates a Job that runs kubectl debug to attach ephemeral container to pod
func (r *PacketCaptureReconciler) createDebugJob(capture *capturev1alpha1.PacketCapture, targetPod *corev1.Pod, jobName, tcpdumpCmd string) *batchv1.Job {
	// Parse duration - add 2 minutes buffer for file copy
	duration, _ := time.ParseDuration(capture.Spec.Duration)
	deadline := int64(duration.Seconds() + 120)

	// Create a hostPath volume to store captures
	hostPathType := corev1.HostPathDirectoryOrCreate

	// Build the capture filename
	direction := "source"
	if strings.Contains(jobName, "destination") {
		direction = "destination"
	}
	captureFilename := fmt.Sprintf("%s-%s-%s.pcap", capture.Name, targetPod.Name, direction)

	// Get the first container name for --target
	targetContainer := targetPod.Name
	if len(targetPod.Spec.Containers) > 0 {
		targetContainer = targetPod.Spec.Containers[0].Name
	}

	// Escape single quotes in tcpdump command for shell
	escapedCmd := strings.ReplaceAll(tcpdumpCmd, "'", "'\"'\"'")

	// Calculate wait time in seconds - wait for capture duration
	waitSeconds := int(duration.Seconds())

	// Use kubectl debug with netadmin profile to run tcpdump in pod's network namespace
	script := fmt.Sprintf(`
set -e
echo "Starting packet capture for pod %s in namespace %s..."

# Start kubectl debug in background with netadmin profile
kubectl debug -n %s %s \
  --profile=netadmin \
  --image=nicolaka/netshoot:latest \
  --target=%s \
  -- sh -c '%s' &

KUBECTL_PID=$!
echo "kubectl debug started with PID $KUBECTL_PID"

# Wait for capture to complete
echo "Waiting %d seconds for capture to complete..."
sleep %d

# Get the ephemeral container name
EPHEM_CONTAINER=$(kubectl get pod %s -n %s -o jsonpath='{.spec.ephemeralContainers[-1].name}')
echo "Ephemeral container: $EPHEM_CONTAINER"

# Copy the capture file from the ephemeral container
echo "Copying capture file from ephemeral container..."
if kubectl cp %s/%s:/tmp/%s /captures/%s -c $EPHEM_CONTAINER; then
  echo "Copy successful"
  # Kill the kubectl debug process immediately after successful copy
  if kill -0 $KUBECTL_PID 2>/dev/null; then
    echo "Terminating kubectl debug process..."
    kill $KUBECTL_PID 2>/dev/null || true
  fi
else
  echo "Copy failed"
fi

# List captured files
ls -lh /captures/ || true

echo "Packet capture completed"
`,
		targetPod.Name, targetPod.Namespace,
		targetPod.Namespace, targetPod.Name, targetContainer, escapedCmd,
		waitSeconds, waitSeconds,
		targetPod.Name, targetPod.Namespace,
		targetPod.Namespace, targetPod.Name, captureFilename, captureFilename)

	labels := map[string]string{
		"app":                         "packet-capture",
		"capture.k8s.io/capture-name": capture.Name,
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: targetPod.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					NodeName:           targetPod.Spec.NodeName,
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: "packet-capture-job",
					Containers: []corev1.Container{
						{
							Name:            "capture-and-copy",
							Image:           "bitnami/kubectl:latest",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         []string{"sh", "-c", script},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "captures",
									MountPath: "/captures",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "captures",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/var/lib/packet-captures",
									Type: &hostPathType,
								},
							},
						},
					},
				},
			},
		},
	}
}
