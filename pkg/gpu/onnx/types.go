package onnx

import "time"

// Checkpoint stores model weights, optimizer momentum, and training progress.
type Checkpoint struct {
	JobID            string               `json:"job_id"`
	ModelHash        string               `json:"model_hash"`
	Step             int64                `json:"step"`
	Epoch            int                  `json:"epoch"`
	Loss             float64              `json:"loss"`
	LearningRate     float64              `json:"learning_rate"`
	Weights          map[string][]float32 `json:"weights"`
	OptimizerState   map[string][]float32 `json:"optimizer_state,omitempty"`
	Timestamp        time.Time            `json:"timestamp"`
	SHA256Checksum   string               `json:"sha256_checksum,omitempty"`
}

// Tensor represents an input or output tensor buffer.
type Tensor struct {
	Name  string    `json:"name"`
	Shape []int64   `json:"shape"`
	Data  []float32 `json:"data"`
}

// ModelConfig configures an ONNX inference or training session.
type ModelConfig struct {
	ModelPath         string `json:"model_path"`
	ExecutionProvider string `json:"execution_provider"` // "DirectML", "CUDA", "CPU"
	DeviceID          int    `json:"device_id"`
	BatchSize         int    `json:"batch_size"`
	MaxMemoryMB       int    `json:"max_memory_mb"`
}
