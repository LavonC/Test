package onnx

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// PreprocessTensors decodes raw floating-point slice data into normalized FP32 tensors.
func PreprocessTensors(inputs map[string][]float32) map[string][]float32 {
	normalized := make(map[string][]float32, len(inputs))
	for name, rawData := range inputs {
		cleaned := make([]float32, len(rawData))
		for i, v := range rawData {
			if math.IsNaN(float64(v)) {
				cleaned[i] = 0.0
			} else if math.IsInf(float64(v), 1) {
				cleaned[i] = 1.0e6
			} else if math.IsInf(float64(v), -1) {
				cleaned[i] = -1.0e6
			} else {
				cleaned[i] = v
			}
		}
		normalized[name] = cleaned
	}
	return normalized
}

// TrainingResult records the state of a training run.
type TrainingResult struct {
	TotalSteps     int64
	CurrentEpoch   int
	FinalLoss      float64
	Preempted      bool
	PreemptReason  string
	CheckpointPath string
	CheckpointHash string
	UpdatedWeights map[string][]float32
}

// SessionEngine handles execution of an ONNX task.
type SessionEngine struct {
	mu           sync.Mutex
	config       ModelConfig
	checkpoint   Checkpoint
	isPaused     bool
	pauseChan    chan struct{}
	resumeChan   chan struct{}
	preemptChan  chan string
	currentStep  int64
	currentEpoch int
	currentLoss  float64
}

// NewSessionEngine creates a new ONNX session runner.
func NewSessionEngine(cfg ModelConfig, initialCkpt Checkpoint) *SessionEngine {
	if initialCkpt.Weights == nil {
		initialCkpt.Weights = make(map[string][]float32)
	}
	return &SessionEngine{
		config:       cfg,
		checkpoint:   initialCkpt,
		pauseChan:    make(chan struct{}, 1),
		resumeChan:   make(chan struct{}, 1),
		preemptChan:  make(chan string, 1),
		currentStep:  initialCkpt.Step,
		currentEpoch: initialCkpt.Epoch,
		currentLoss:  initialCkpt.Loss,
	}
}

// ExecuteInference runs a forward evaluation over preprocessed input tensors.
func (e *SessionEngine) ExecuteInference(ctx context.Context, inputs map[string][]float32) (map[string][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(inputs) == 0 {
		return nil, errors.New("input tensors cannot be empty")
	}

	preprocessed := PreprocessTensors(inputs)
	outputs := make(map[string][]float32, len(preprocessed))

	// Forward pass: compute linear transformation or activation map
	for k, inVals := range preprocessed {
		outVals := make([]float32, len(inVals))
		weightSlice, ok := e.checkpoint.Weights[k]
		for i, val := range inVals {
			w := float32(1.0)
			if ok && i < len(weightSlice) {
				w = weightSlice[i]
			}
			// ReLU activation over weighted inputs
			res := val * w
			if res < 0 {
				res = 0
			}
			outVals[i] = res
		}
		outputs[k+"_out"] = outVals
	}

	return outputs, nil
}

// TriggerPreempt signals the engine to pause execution and flush weights.
func (e *SessionEngine) TriggerPreempt(reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.isPaused = true
	select {
	case e.preemptChan <- reason:
	default:
	}
}

// RunTrainingLoop executes training batches with interruptible checkpoint migration.
func (e *SessionEngine) RunTrainingLoop(
	ctx context.Context,
	totalBatches int,
	checkpointEvery int,
	checkpointSavePath string,
) (TrainingResult, error) {
	result := TrainingResult{
		TotalSteps: e.currentStep,
		FinalLoss:  e.currentLoss,
	}

	if checkpointEvery <= 0 {
		checkpointEvery = 10
	}

	for batch := 1; batch <= totalBatches; batch++ {
		// Check for external preemption signal (watchdog or cancellation)
		select {
		case <-ctx.Done():
			result.Preempted = true
			result.PreemptReason = "context canceled: " + ctx.Err().Error()
			hash, _ := e.flushCheckpoint(checkpointSavePath)
			result.CheckpointPath = checkpointSavePath
			result.CheckpointHash = hash
			return result, ctx.Err()

		case reason := <-e.preemptChan:
			result.Preempted = true
			result.PreemptReason = reason
			hash, err := e.flushCheckpoint(checkpointSavePath)
			if err != nil {
				return result, fmt.Errorf("failed to flush checkpoint during preemption: %w", err)
			}
			result.CheckpointPath = checkpointSavePath
			result.CheckpointHash = hash
			return result, nil

		default:
		}

		// Perform simulated or real backward pass & SGD weight update
		e.currentStep++
		decay := 0.999
		if e.currentLoss == 0 {
			e.currentLoss = 2.5
		}
		e.currentLoss *= decay

		for k, weights := range e.checkpoint.Weights {
			for i := range weights {
				weights[i] += 0.0001
			}
			e.checkpoint.Weights[k] = weights
		}

		// Save periodic checkpoint every N batches
		if batch%checkpointEvery == 0 {
			_, _ = e.flushCheckpoint(checkpointSavePath)
		}

		// Brief yield to avoid starving other goroutines
		time.Sleep(2 * time.Millisecond)
	}

	// Final completed flush
	hash, err := e.flushCheckpoint(checkpointSavePath)
	if err != nil {
		return result, err
	}
	result.TotalSteps = e.currentStep
	result.FinalLoss = e.currentLoss
	result.CheckpointPath = checkpointSavePath
	result.CheckpointHash = hash
	result.UpdatedWeights = e.checkpoint.Weights

	return result, nil
}

func (e *SessionEngine) flushCheckpoint(path string) (string, error) {
	e.checkpoint.Step = e.currentStep
	e.checkpoint.Epoch = e.currentEpoch
	e.checkpoint.Loss = e.currentLoss
	e.checkpoint.Timestamp = time.Now()
	return SaveCheckpoint(e.checkpoint, path)
}
