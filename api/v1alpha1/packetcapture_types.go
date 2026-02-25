package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PacketCaptureSpec defines the desired state of PacketCapture
type PacketCaptureSpec struct {
	// Source defines the source endpoint selector
	// +optional
	Source *EndpointSelector `json:"source,omitempty"`

	// Destination defines the destination endpoint selector
	// +optional
	Destination *EndpointSelector `json:"destination,omitempty"`

	// Duration specifies how long to capture packets (e.g., "5m", "1h")
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	Duration string `json:"duration"`

	// MaxPackets limits the number of packets to capture (0 = unlimited)
	// +optional
	// +kubebuilder:default=0
	MaxPackets int `json:"maxPackets,omitempty"`

	// MaxPacketSize limits the snapshot length of each packet in bytes
	// +optional
	// +kubebuilder:default=65535
	MaxPacketSize int `json:"maxPacketSize,omitempty"`

	// Filter is a BPF filter expression (tcpdump syntax)
	// +optional
	Filter string `json:"filter,omitempty"`

	// Interface specifies the network interface to capture on (default: any)
	// +optional
	// +kubebuilder:default="any"
	Interface string `json:"interface,omitempty"`

	// Storage defines where to store captured packets
	// +optional
	Storage *StorageSpec `json:"storage,omitempty"`

	// NodeSelector allows targeting specific nodes for capture
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
}

// EndpointSelector defines how to select source or destination endpoints
type EndpointSelector struct {
	// PodSelector selects pods by labels
	// +optional
	PodSelector *metav1.LabelSelector `json:"podSelector,omitempty"`

	// NamespaceSelector selects namespaces by labels
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// CIDR specifies an IP address range in CIDR notation
	// +optional
	CIDR []string `json:"cidr,omitempty"`

	// FQDN specifies fully qualified domain names
	// +optional
	FQDN []string `json:"fqdn,omitempty"`

	// Ports specifies port numbers or ranges
	// +optional
	Ports []PortSelector `json:"ports,omitempty"`
}

// PortSelector defines port selection criteria
type PortSelector struct {
	// Port is a single port number
	// +optional
	Port *int32 `json:"port,omitempty"`

	// PortRange defines a range of ports (e.g., "8000-9000")
	// +optional
	PortRange *string `json:"portRange,omitempty"`

	// Protocol is the transport protocol (TCP, UDP, SCTP)
	// +optional
	// +kubebuilder:validation:Enum=TCP;UDP;SCTP
	Protocol string `json:"protocol,omitempty"`
}

// StorageSpec defines storage configuration for captured packets
type StorageSpec struct {
	// Type specifies the storage backend type
	// +kubebuilder:validation:Enum=PersistentVolume;S3;GCS;ConfigMap
	// +kubebuilder:default=PersistentVolume
	Type string `json:"type"`

	// PersistentVolumeClaim name for PersistentVolume storage
	// +optional
	PersistentVolumeClaim string `json:"persistentVolumeClaim,omitempty"`

	// S3 configuration for S3 storage
	// +optional
	S3 *S3StorageSpec `json:"s3,omitempty"`

	// GCS configuration for Google Cloud Storage
	// +optional
	GCS *GCSStorageSpec `json:"gcs,omitempty"`

	// RetentionDays specifies how long to keep capture files
	// +optional
	// +kubebuilder:default=7
	RetentionDays int `json:"retentionDays,omitempty"`
}

// S3StorageSpec defines S3 storage configuration
type S3StorageSpec struct {
	// Bucket name
	Bucket string `json:"bucket"`

	// Region of the S3 bucket
	// +optional
	Region string `json:"region,omitempty"`

	// Prefix for object keys
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// CredentialsSecret references a secret containing AWS credentials
	// +optional
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
}

// GCSStorageSpec defines Google Cloud Storage configuration
type GCSStorageSpec struct {
	// Bucket name
	Bucket string `json:"bucket"`

	// Prefix for object keys
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// CredentialsSecret references a secret containing GCS credentials
	// +optional
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
}

// PacketCaptureStatus defines the observed state of PacketCapture
type PacketCaptureStatus struct {
	// Phase represents the current phase of the packet capture
	// +kubebuilder:validation:Enum=Pending;Running;Completed;Failed
	Phase string `json:"phase,omitempty"`

	// StartTime is when the capture started
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// EndTime is when the capture completed or failed
	// +optional
	EndTime *metav1.Time `json:"endTime,omitempty"`

	// CaptureJobs tracks the jobs created for this capture
	// +optional
	CaptureJobs []CaptureJobStatus `json:"captureJobs,omitempty"`

	// PacketsCaptured is the total number of packets captured
	// +optional
	PacketsCaptured int64 `json:"packetsCaptured,omitempty"`

	// BytesCaptured is the total bytes captured
	// +optional
	BytesCaptured int64 `json:"bytesCaptured,omitempty"`

	// CaptureFiles lists the generated capture files
	// +optional
	CaptureFiles []string `json:"captureFiles,omitempty"`

	// Conditions represent the latest available observations of the capture's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Message provides additional information about the current state
	// +optional
	Message string `json:"message,omitempty"`
}

// CaptureJobStatus tracks individual capture jobs per node
type CaptureJobStatus struct {
	// NodeName is the node where this capture is running
	NodeName string `json:"nodeName"`

	// JobName is the name of the Kubernetes Job
	JobName string `json:"jobName"`

	// Status is the current status of this job
	Status string `json:"status"`

	// PacketsCaptured on this node
	// +optional
	PacketsCaptured int64 `json:"packetsCaptured,omitempty"`

	// CaptureFile path or URL
	// +optional
	CaptureFile string `json:"captureFile,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=pc
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Duration",type=string,JSONPath=`.spec.duration`
// +kubebuilder:printcolumn:name="Packets",type=integer,JSONPath=`.status.packetsCaptured`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PacketCapture is the Schema for the packetcaptures API
type PacketCapture struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PacketCaptureSpec   `json:"spec,omitempty"`
	Status PacketCaptureStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PacketCaptureList contains a list of PacketCapture
type PacketCaptureList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PacketCapture `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PacketCapture{}, &PacketCaptureList{})
}
