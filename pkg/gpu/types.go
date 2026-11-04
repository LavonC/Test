package gpu

import (
	"time"

	"github.com/unicompute/obj1/pkg/gpu/hipify"
)

// AnalyzeCUDASource wraps hipify analysis for external consumers.
func AnalyzeCUDASource(source string) hipify.CompatibilityReport {
	return hipify.AnalyzeCUDASource(source)
}

// TranslateCUDAToHIP wraps automated HIP translation for external consumers.
func TranslateCUDAToHIP(cudaSource string) hipify.TranslationResult {
	return hipify.TranslateCUDAToHIP(cudaSource)
}

// GPUVendor classifies graphics hardware manufacturers.
type GPUVendor string

const (
	VendorNVIDIA  GPUVendor = "NVIDIA"
	VendorAMD     GPUVendor = "AMD"
	VendorIntel   GPUVendor = "Intel"
	VendorUnknown GPUVendor = "Unknown"
)

// ExecutionBackend defines the compute acceleration API.
type ExecutionBackend string

const (
	BackendDirectML ExecutionBackend = "DirectML"
	BackendCUDA     ExecutionBackend = "CUDA"
	BackendVulkan   ExecutionBackend = "Vulkan"
	BackendROCm     ExecutionBackend = "ROCm"
	BackendCPU      ExecutionBackend = "CPU"
)

// GPUProfile contains hardware specs and capabilities discovered on the node.
type GPUProfile struct {
	Vendor             GPUVendor          `json:"vendor"`
	DeviceName         string             `json:"device_name"`
	DeviceID           uint32             `json:"device_id"`
	TotalVRAMBytes     uint64             `json:"total_vram_bytes"`
	AvailableVRAMBytes uint64             `json:"available_vram_bytes"`
	DirectMLSupported  bool               `json:"directml_supported"`
	CUDASupported      bool               `json:"cuda_supported"`
	VulkanSupported    bool               `json:"vulkan_supported"`
	DriverVersion      string             `json:"driver_version"`
	TemperatureC       float64            `json:"temperature_c,omitempty"`
	PowerUsageW        float64            `json:"power_usage_w,omitempty"`
	SupportedBackends  []ExecutionBackend `json:"supported_backends"`
	Timestamp          time.Time          `json:"timestamp"`
}

// WatchdogStatus reports real-time power, battery, and student activity state.
type WatchdogStatus struct {
	State               string        `json:"state"` // "running", "paused_battery", "paused_user_active", "stopped"
	OnBattery           bool          `json:"on_battery"`
	BatteryPercent      uint8         `json:"battery_percent"`
	UserActive          bool          `json:"user_active"`
	UserIdleDuration    time.Duration `json:"user_idle_duration"`
	SustainedIdlePeriod time.Duration `json:"sustained_idle_period"`
	LastPauseReason     string        `json:"last_pause_reason,omitempty"`
	LastPauseTime       time.Time     `json:"last_pause_time,omitempty"`
}

// TaskCapsuleManifest describes a signed GPU workload archive.
type TaskCapsuleManifest struct {
	CapsuleID       string           `json:"capsule_id"`
	Name            string           `json:"name"`
	Version         string           `json:"version"`
	TargetTier      string           `json:"target_tier"` // "tier3"
	TaskType        string           `json:"task_type"`   // "inference" or "training"
	RequiredBackend ExecutionBackend `json:"required_backend"`
	MinVRAMBytes    uint64           `json:"min_vram_bytes"`
	ModelArtifacts  []string         `json:"model_artifacts"`
	DatasetShards   []string         `json:"dataset_shards,omitempty"`
	BatchSize       int              `json:"batch_size,omitempty"`
	TotalBatches    int              `json:"total_batches,omitempty"`
	CheckpointEvery int              `json:"checkpoint_every,omitempty"`
	Signature       string           `json:"signature"` // Ed25519 signature
}

// GPUTaskRequest represents an incoming GPU computation request.
type GPUTaskRequest struct {
	JobID          string              `json:"job_id"`
	Manifest       TaskCapsuleManifest `json:"manifest"`
	SourceCode     string              `json:"source_code,omitempty"` // For native CUDA/HIP tasks
	InputTensors   map[string][]float32 `json:"input_tensors,omitempty"`
	CheckpointBlob string              `json:"checkpoint_blob,omitempty"` // Blob ID of initial checkpoint
	TimeoutSeconds int                 `json:"timeout_seconds"`
}

// GPUTaskResponse represents the execution output of a GPU task.
type GPUTaskResponse struct {
	JobID             string               `json:"job_id"`
	Status            string               `json:"status"` // "completed", "preempted", "failed", "rejected"
	ExitCode          int                  `json:"exit_code"`
	OutputTensors     map[string][]float32 `json:"output_tensors,omitempty"`
	CheckpointBlob    string               `json:"checkpoint_blob,omitempty"` // Stored updated checkpoint blob ID
	ProcessedBatches  int                  `json:"processed_batches"`
	Loss              float64              `json:"loss,omitempty"`
	ExecutionMs       int64                `json:"execution_ms"`
	PreemptedReason   string               `json:"preempted_reason,omitempty"`
	DiagnosticMessage string               `json:"diagnostic_message,omitempty"`
	Error             string               `json:"error,omitempty"`
}
