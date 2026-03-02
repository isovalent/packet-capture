package controllers

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	capturev1alpha1 "github.com/packet-capture/operator/api/v1alpha1"
)

const (
	captureFinalizerName = "capture.k8s.io/finalizer"
	captureJobPrefix     = "packet-capture"
)

// PacketCaptureReconciler reconciles a PacketCapture object
type PacketCaptureReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	OperatorNodeName string // node where the operator pod is running
}

// +kubebuilder:rbac:groups=capture.k8s.io,resources=packetcaptures,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=capture.k8s.io,resources=packetcaptures/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=capture.k8s.io,resources=packetcaptures/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *PacketCaptureReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the PacketCapture instance
	capture := &capturev1alpha1.PacketCapture{}
	if err := r.Get(ctx, req.NamespacedName, capture); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch PacketCapture")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !capture.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, capture)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(capture, captureFinalizerName) {
		controllerutil.AddFinalizer(capture, captureFinalizerName)
		if err := r.Update(ctx, capture); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Initialize status if needed
	if capture.Status.Phase == "" {
		capture.Status.Phase = "Pending"
		if err := r.Status().Update(ctx, capture); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Handle different phases
	switch capture.Status.Phase {
	case "Pending":
		return r.handlePending(ctx, capture)
	case "Running":
		return r.handleRunning(ctx, capture)
	case "Completed", "Failed":
		// Still run GC in case pods finished after the initial cleanup
		if err := r.cleanupJobPods(ctx, capture); err != nil {
			logger.Error(err, "failed to cleanup Job pods")
		}
		return ctrl.Result{}, nil
	default:
		return ctrl.Result{}, nil
	}
}

// handlePending starts the packet capture
func (r *PacketCaptureReconciler) handlePending(ctx context.Context, capture *capturev1alpha1.PacketCapture) (ctrl.Result, error) {
	// Check if this is a pod-based capture (has pod selectors)
	hasPodSelectors := (capture.Spec.Source != nil && capture.Spec.Source.PodSelector != nil) ||
		(capture.Spec.Destination != nil && capture.Spec.Destination.PodSelector != nil)

	if hasPodSelectors {
		// Pod-based capture using ephemeral containers
		return r.handlePodBasedCapture(ctx, capture)
	}

	// Node-based capture using jobs (fallback for non-pod captures)
	return r.handleNodeBasedCapture(ctx, capture)
}

// handlePodBasedCapture creates ephemeral containers on target pods
func (r *PacketCaptureReconciler) handlePodBasedCapture(ctx context.Context, capture *capturev1alpha1.PacketCapture) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get target pods (source and destination)
	targets, err := r.getTargetPods(ctx, capture)
	if err != nil {
		logger.Error(err, "failed to get target pods")
		return r.updateStatusFailed(ctx, capture, fmt.Sprintf("Failed to get target pods: %v", err))
	}

	if len(targets) == 0 {
		return r.updateStatusFailed(ctx, capture, "No pods matched the pod selectors")
	}

	logger.Info("Creating ephemeral containers for packet capture", "targetCount", len(targets))

	// Create ephemeral container on each target pod
	for _, target := range targets {
		if err := r.createEphemeralContainer(ctx, capture, target); err != nil {
			logger.Error(err, "failed to create ephemeral container",
				"pod", target.Pod.Name,
				"namespace", target.Pod.Namespace,
				"direction", target.Direction)
			continue
		}

		// Update status with pod info
		jobName := buildCaptureJobName(capture.Name, target.Pod.Name, target.Direction)
		capture.Status.CaptureJobs = append(capture.Status.CaptureJobs, capturev1alpha1.CaptureJobStatus{
			NodeName: target.Pod.Spec.NodeName,
			JobName:  jobName,
			Status:   "Running",
		})
	}

	// Update status to Running
	now := metav1.Now()
	capture.Status.Phase = "Running"
	capture.Status.StartTime = &now
	capture.Status.Message = fmt.Sprintf("Started capture on %d pods", len(targets))

	if err := r.Status().Update(ctx, capture); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue to check progress
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// handleNodeBasedCapture creates jobs on target nodes (original behavior)
func (r *PacketCaptureReconciler) handleNodeBasedCapture(ctx context.Context, capture *capturev1alpha1.PacketCapture) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get target nodes
	nodes, err := r.getTargetNodes(ctx, capture)
	if err != nil {
		logger.Error(err, "failed to get target nodes")
		return r.updateStatusFailed(ctx, capture, fmt.Sprintf("Failed to get target nodes: %v", err))
	}

	if len(nodes) == 0 {
		return r.updateStatusFailed(ctx, capture, "No nodes matched the node selector")
	}

	// Create capture jobs for each node
	for _, node := range nodes {
		job, err := r.createCaptureJob(ctx, capture, node.Name)
		if err != nil {
			logger.Error(err, "failed to create capture job", "node", node.Name)
			continue
		}

		// Update status with job info
		capture.Status.CaptureJobs = append(capture.Status.CaptureJobs, capturev1alpha1.CaptureJobStatus{
			NodeName: node.Name,
			JobName:  job.Name,
			Status:   "Running",
		})
	}

	// Update status to Running
	now := metav1.Now()
	capture.Status.Phase = "Running"
	capture.Status.StartTime = &now
	capture.Status.Message = fmt.Sprintf("Started capture on %d nodes", len(nodes))

	if err := r.Status().Update(ctx, capture); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue to check progress
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// handleRunning monitors the capture progress
func (r *PacketCaptureReconciler) handleRunning(ctx context.Context, capture *capturev1alpha1.PacketCapture) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Parse duration
	duration, err := time.ParseDuration(capture.Spec.Duration)
	if err != nil {
		return r.updateStatusFailed(ctx, capture, fmt.Sprintf("Invalid duration: %v", err))
	}

	// Check if capture should be completed
	if capture.Status.StartTime != nil {
		elapsed := time.Since(capture.Status.StartTime.Time)
		if elapsed >= duration {
			return r.completeCapture(ctx, capture)
		}
	}

	// Check job statuses
	allCompleted := true
	totalPackets := int64(0)
	totalBytes := int64(0)

	for i := range capture.Status.CaptureJobs {
		jobStatus := &capture.Status.CaptureJobs[i]

		job := &batchv1.Job{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      jobStatus.JobName,
			Namespace: capture.Namespace,
		}, job)

		if err != nil {
			logger.Error(err, "failed to get job", "job", jobStatus.JobName)
			continue
		}

		if job.Status.Succeeded > 0 {
			jobStatus.Status = "Completed"
		} else if job.Status.Failed > 0 {
			jobStatus.Status = "Failed"
		} else {
			allCompleted = false
			jobStatus.Status = "Running"
		}

		totalPackets += jobStatus.PacketsCaptured
		totalBytes += int64(jobStatus.PacketsCaptured) * 1500 // Estimate
	}

	capture.Status.PacketsCaptured = totalPackets
	capture.Status.BytesCaptured = totalBytes

	if err := r.Status().Update(ctx, capture); err != nil {
		return ctrl.Result{}, err
	}

	if allCompleted {
		return r.completeCapture(ctx, capture)
	}

	// Requeue to check again
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// completeCapture marks the capture as completed, aggregates pcap files, and cleans up Job pods
func (r *PacketCaptureReconciler) completeCapture(ctx context.Context, capture *capturev1alpha1.PacketCapture) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	now := metav1.Now()
	capture.Status.Phase = "Completed"
	capture.Status.EndTime = &now
	capture.Status.Message = "Capture completed successfully"

	if err := r.Status().Update(ctx, capture); err != nil {
		return ctrl.Result{}, err
	}

	// Aggregate pcap files from worker nodes to operator node
	if r.OperatorNodeName != "" {
		logger.Info("Aggregating pcap files to operator node", "node", r.OperatorNodeName)
		if err := r.aggregatePcapFiles(ctx, capture); err != nil {
			logger.Error(err, "failed to aggregate pcap files")
			// Don't fail the completion, just log the error
		}
	}

	// Garbage collection: Delete completed Job pods
	logger.Info("Starting garbage collection of completed Job pods")
	if err := r.cleanupJobPods(ctx, capture); err != nil {
		logger.Error(err, "failed to cleanup Job pods")
		// Don't fail the completion, just log the error
	}

	return ctrl.Result{}, nil
}

// aggregatePcapFiles creates a Job on the operator's node that copies pcap files
// from each worker node's hostPath into a central directory on the operator's node.
func (r *PacketCaptureReconciler) aggregatePcapFiles(ctx context.Context, capture *capturev1alpha1.PacketCapture) error {
	logger := log.FromContext(ctx)

	// Collect unique nodes and their capture files
	type nodeFile struct {
		node string
		file string
	}
	var targets []nodeFile
	for _, job := range capture.Status.CaptureJobs {
		if job.CaptureFile != "" && job.NodeName != "" && job.NodeName != r.OperatorNodeName {
			targets = append(targets, nodeFile{node: job.NodeName, file: job.CaptureFile})
		}
	}

	if len(targets) == 0 {
		logger.Info("No remote pcap files to aggregate")
		return nil
	}

	// Build shell script: for each worker node, find the preloader DaemonSet pod
	// on that node and kubectl cp the pcap file to the operator node's hostPath
	const (
		preloaderLabel   = "app=packet-capture-image-preloader"
		aggregateBasedir = "/var/lib/packet-captures"
	)

	var copyCommands string
	var aggregatedFiles []string
	for _, t := range targets {
		filename := t.file[len("/var/lib/packet-captures/"):] // strip base dir
		destPath := fmt.Sprintf("%s/%s", aggregateBasedir, filename)
		aggregatedFiles = append(aggregatedFiles, destPath)
		copyCommands += fmt.Sprintf(`
echo "Copying %s from node %s..."
PRELOADER_POD=$(kubectl get pods -n packet-capture-system -l %s \
  --field-selector spec.nodeName=%s \
  -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
if [ -z "$PRELOADER_POD" ]; then
  echo "No preloader pod found on node %s, skipping"
else
  kubectl cp packet-capture-system/$PRELOADER_POD:/var/lib/packet-captures/%s %s/%s -c pause \
    && echo "Copied %s successfully" \
    || echo "Failed to copy %s"
fi
`,
			filename, t.node, preloaderLabel, t.node,
			t.node,
			filename, aggregateBasedir, filename,
			filename, filename)
	}

	script := fmt.Sprintf(`set -e
echo "Starting pcap aggregation for capture %s..."
%s
echo "Aggregation complete. Files in %s:"
ls -lh %s/ || true
`, capture.Name, copyCommands, aggregateBasedir, aggregateBasedir)

	hostPathType := corev1.HostPathDirectoryOrCreate
	aggregatorJobName := fmt.Sprintf("pc-aggregate-%s", capture.Name)
	true := true

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      aggregatorJobName,
			Namespace: capture.Namespace,
			Labels: map[string]string{
				"app":                         "packet-capture",
				"capture.k8s.io/capture-name": capture.Name,
				"capture.k8s.io/role":         "aggregator",
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":                         "packet-capture",
						"capture.k8s.io/capture-name": capture.Name,
						"capture.k8s.io/role":         "aggregator",
					},
				},
				Spec: corev1.PodSpec{
					NodeName:           r.OperatorNodeName,
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: "packet-capture-job",
					Containers: []corev1.Container{
						{
							Name:            "aggregator",
							Image:           "bitnami/kubectl:latest",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         []string{"sh", "-c", script},
							SecurityContext: &corev1.SecurityContext{
								Privileged: &true,
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "captures",
									MountPath: "/var/lib/packet-captures",
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

	if err := ctrl.SetControllerReference(capture, job, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on aggregator job: %w", err)
	}

	if err := r.Create(ctx, job); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create aggregator job: %w", err)
	}

	logger.Info("Created aggregator Job", "job", aggregatorJobName, "node", r.OperatorNodeName)

	// Update status with aggregated file paths
	capture.Status.CaptureFiles = append(capture.Status.CaptureFiles, aggregatedFiles...)
	if err := r.Status().Update(ctx, capture); err != nil {
		logger.Error(err, "failed to update CaptureFiles status")
	}

	return nil
}

// updateStatusFailed updates the status to Failed
func (r *PacketCaptureReconciler) updateStatusFailed(ctx context.Context, capture *capturev1alpha1.PacketCapture, message string) (ctrl.Result, error) {
	now := metav1.Now()
	capture.Status.Phase = "Failed"
	capture.Status.EndTime = &now
	capture.Status.Message = message

	if err := r.Status().Update(ctx, capture); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// cleanupJobPods deletes completed Job pods to free up resources
func (r *PacketCaptureReconciler) cleanupJobPods(ctx context.Context, capture *capturev1alpha1.PacketCapture) error {
	logger := log.FromContext(ctx)

	// List pods by capture-name label — more reliable than matching status.captureJobs
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList,
		client.InNamespace(capture.Namespace),
		client.MatchingLabels{"capture.k8s.io/capture-name": capture.Name},
	); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			logger.Info("Deleting completed Job pod", "pod", pod.Name, "phase", pod.Status.Phase)
			if err := r.Delete(ctx, pod); err != nil && !errors.IsNotFound(err) {
				logger.Error(err, "failed to delete pod", "pod", pod.Name)
			}
		}
	}

	return nil
}

// handleDeletion handles cleanup when PacketCapture is deleted
func (r *PacketCaptureReconciler) handleDeletion(ctx context.Context, capture *capturev1alpha1.PacketCapture) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(capture, captureFinalizerName) {
		// Clean up Job pods first
		if err := r.cleanupJobPods(ctx, capture); err != nil {
			logger.Error(err, "failed to cleanup Job pods during deletion")
		}

		// Delete all associated jobs
		for _, jobStatus := range capture.Status.CaptureJobs {
			job := &batchv1.Job{}
			err := r.Get(ctx, types.NamespacedName{
				Name:      jobStatus.JobName,
				Namespace: capture.Namespace,
			}, job)

			if err == nil {
				if err := r.Delete(ctx, job); err != nil {
					logger.Error(err, "failed to delete job", "job", jobStatus.JobName)
				}
			}
		}

		// Remove finalizer
		controllerutil.RemoveFinalizer(capture, captureFinalizerName)
		if err := r.Update(ctx, capture); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// getTargetNodes returns the list of nodes to run captures on
func (r *PacketCaptureReconciler) getTargetNodes(ctx context.Context, capture *capturev1alpha1.PacketCapture) ([]corev1.Node, error) {
	nodeList := &corev1.NodeList{}

	listOpts := []client.ListOption{}
	if len(capture.Spec.NodeSelector) > 0 {
		listOpts = append(listOpts, client.MatchingLabels(capture.Spec.NodeSelector))
	}

	if err := r.List(ctx, nodeList, listOpts...); err != nil {
		return nil, err
	}

	return nodeList.Items, nil
}

// createCaptureJob creates a Job to run packet capture on a specific node
func (r *PacketCaptureReconciler) createCaptureJob(ctx context.Context, capture *capturev1alpha1.PacketCapture, nodeName string) (*batchv1.Job, error) {
	jobName := fmt.Sprintf("%s-%s-%s", captureJobPrefix, capture.Name, nodeName)

	// Build tcpdump command
	cmd := r.buildTcpdumpCommand(capture)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: capture.Namespace,
			Labels: map[string]string{
				"app":                         "packet-capture",
				"capture.k8s.io/capture-name": capture.Name,
				"capture.k8s.io/node":         nodeName,
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":                         "packet-capture",
						"capture.k8s.io/capture-name": capture.Name,
					},
				},
				Spec: corev1.PodSpec{
					NodeName:      nodeName,
					HostNetwork:   true,
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:            "tcpdump",
							Image:           "nicolaka/netshoot:latest",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command: []string{
								"/bin/sh",
								"-c",
								cmd,
							},
							SecurityContext: &corev1.SecurityContext{
								Privileged: func() *bool { b := true; return &b }(),
								Capabilities: &corev1.Capabilities{
									Add: []corev1.Capability{"NET_ADMIN", "NET_RAW"},
								},
							},
							VolumeMounts: r.buildVolumeMounts(capture),
						},
					},
					Volumes: r.buildVolumes(capture),
				},
			},
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(capture, job, r.Scheme); err != nil {
		return nil, err
	}

	if err := r.Create(ctx, job); err != nil {
		return nil, err
	}

	return job, nil
}

// buildTcpdumpCommand constructs the tcpdump command based on capture spec
func (r *PacketCaptureReconciler) buildTcpdumpCommand(capture *capturev1alpha1.PacketCapture) string {
	outputFile := fmt.Sprintf("/captures/%s-%s.pcap", capture.Name, time.Now().Format("20060102-150405"))

	cmd := fmt.Sprintf("tcpdump -i %s -w %s", capture.Spec.Interface, outputFile)

	if capture.Spec.MaxPackets > 0 {
		cmd += fmt.Sprintf(" -c %d", capture.Spec.MaxPackets)
	}

	if capture.Spec.MaxPacketSize > 0 {
		cmd += fmt.Sprintf(" -s %d", capture.Spec.MaxPacketSize)
	}

	// Add BPF filter
	if capture.Spec.Filter != "" {
		cmd += fmt.Sprintf(" '%s'", capture.Spec.Filter)
	} else {
		// Build filter from source/destination
		filter := r.buildBPFFilter(capture)
		if filter != "" {
			cmd += fmt.Sprintf(" '%s'", filter)
		}
	}

	// Add timeout based on duration
	duration, _ := time.ParseDuration(capture.Spec.Duration)
	cmd = fmt.Sprintf("timeout %d %s", int(duration.Seconds()), cmd)

	return cmd
}

// buildBPFFilter constructs a BPF filter from source/destination selectors
func (r *PacketCaptureReconciler) buildBPFFilter(capture *capturev1alpha1.PacketCapture) string {
	var filters []string

	// Source filters
	if capture.Spec.Source != nil {
		if len(capture.Spec.Source.CIDR) > 0 {
			for _, cidr := range capture.Spec.Source.CIDR {
				filters = append(filters, fmt.Sprintf("src net %s", cidr))
			}
		}

		if len(capture.Spec.Source.Ports) > 0 {
			for _, port := range capture.Spec.Source.Ports {
				if port.Port != nil {
					filters = append(filters, fmt.Sprintf("src port %d", *port.Port))
				}
			}
		}
	}

	// Destination filters
	if capture.Spec.Destination != nil {
		if len(capture.Spec.Destination.CIDR) > 0 {
			for _, cidr := range capture.Spec.Destination.CIDR {
				filters = append(filters, fmt.Sprintf("dst net %s", cidr))
			}
		}

		if len(capture.Spec.Destination.Ports) > 0 {
			for _, port := range capture.Spec.Destination.Ports {
				if port.Port != nil {
					filters = append(filters, fmt.Sprintf("dst port %d", *port.Port))
				}
			}
		}
	}

	if len(filters) == 0 {
		return ""
	}

	// Join with "and"
	result := filters[0]
	for i := 1; i < len(filters); i++ {
		result += " and " + filters[i]
	}

	return result
}

// buildVolumeMounts creates volume mounts based on storage configuration
func (r *PacketCaptureReconciler) buildVolumeMounts(capture *capturev1alpha1.PacketCapture) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		{
			Name:      "capture-output",
			MountPath: "/captures",
		},
	}
	return mounts
}

// buildVolumes creates volumes based on storage configuration
func (r *PacketCaptureReconciler) buildVolumes(capture *capturev1alpha1.PacketCapture) []corev1.Volume {
	// Check if storage is configured
	if capture.Spec.Storage != nil && capture.Spec.Storage.Type == "PersistentVolume" {
		// Use PersistentVolumeClaim
		if capture.Spec.Storage.PersistentVolumeClaim != "" {
			return []corev1.Volume{
				{
					Name: "capture-output",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: capture.Spec.Storage.PersistentVolumeClaim,
						},
					},
				},
			}
		}
	}

	// Default to emptyDir
	return []corev1.Volume{
		{
			Name: "capture-output",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}
}

// SetupWithManager sets up the controller with the Manager
func (r *PacketCaptureReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.Add(preloadImagesRunnable(r.Client)); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&capturev1alpha1.PacketCapture{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}

// preloadImagesRunnable returns a Runnable that ensures nicolaka/netshoot is pre-pulled on every node
func preloadImagesRunnable(c client.Client) manager.RunnableFunc {
	return func(ctx context.Context) error {
		logger := log.FromContext(ctx).WithName("preload-images")
		const (
			daemonSetName = "packet-capture-image-preloader"
			namespace     = "packet-capture-system"
			captureImage  = "nicolaka/netshoot:latest"
		)

		hostPathType := corev1.HostPathDirectory
		privileged := false
		ds := &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      daemonSetName,
				Namespace: namespace,
			},
			Spec: appsv1.DaemonSetSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": daemonSetName},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": daemonSetName},
					},
					Spec: corev1.PodSpec{
						Tolerations: []corev1.Toleration{
							{Operator: corev1.TolerationOpExists},
						},
						InitContainers: []corev1.Container{
							{
								Name:            "pull-netshoot",
								Image:           captureImage,
								ImagePullPolicy: corev1.PullAlways,
								Command:         []string{"sh", "-c", "echo image pulled"},
								SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
							},
						},
						Containers: []corev1.Container{
							{
								Name:  "pause",
								Image: "gcr.io/google_containers/pause:3.1",
								VolumeMounts: []corev1.VolumeMount{
									{Name: "captures", MountPath: "/var/lib/packet-captures"},
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

		existing := &appsv1.DaemonSet{}
		err := c.Get(ctx, types.NamespacedName{Name: daemonSetName, Namespace: namespace}, existing)
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		if errors.IsNotFound(err) {
			logger.Info("Creating image preloader DaemonSet", "image", captureImage)
			if err := c.Create(ctx, ds); err != nil {
				logger.Error(err, "failed to create preloader DaemonSet")
			}
		} else {
			logger.Info("Image preloader DaemonSet already exists")
		}
		return nil
	}
}
