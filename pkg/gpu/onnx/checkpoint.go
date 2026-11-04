package onnx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// SerializeCheckpoint serializes a checkpoint to bytes and calculates its SHA-256 hash.
func SerializeCheckpoint(ckpt Checkpoint) ([]byte, string, error) {
	ckpt.SHA256Checksum = "" // clear for deterministic hashing
	if ckpt.Timestamp.IsZero() {
		ckpt.Timestamp = time.Now()
	}

	payload, err := json.Marshal(ckpt)
	if err != nil {
		return nil, "", fmt.Errorf("marshal checkpoint: %w", err)
	}

	hash := sha256.Sum256(payload)
	hashHex := hex.EncodeToString(hash[:])

	// Re-marshal with checksum included
	ckpt.SHA256Checksum = hashHex
	finalData, err := json.MarshalIndent(ckpt, "", "  ")
	if err != nil {
		return nil, "", fmt.Errorf("marshal final checkpoint: %w", err)
	}

	return finalData, hashHex, nil
}

// SaveCheckpoint atomically saves a checkpoint to disk.
func SaveCheckpoint(ckpt Checkpoint, filePath string) (string, error) {
	data, hashHex, err := SerializeCheckpoint(ckpt)
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create checkpoint directory: %w", err)
	}

	tmpFile := filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return "", fmt.Errorf("write temp checkpoint: %w", err)
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
		_ = os.Remove(tmpFile)
		return "", fmt.Errorf("commit checkpoint: %w", err)
	}

	return hashHex, nil
}

// LoadCheckpoint reads and verifies the integrity of a stored checkpoint.
func LoadCheckpoint(filePath string) (Checkpoint, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("read checkpoint file: %w", err)
	}

	return DeserializeCheckpoint(data)
}

// DeserializeCheckpoint parses bytes into a verified Checkpoint struct.
func DeserializeCheckpoint(data []byte) (Checkpoint, error) {
	var ckpt Checkpoint
	if err := json.Unmarshal(data, &ckpt); err != nil {
		return Checkpoint{}, fmt.Errorf("unmarshal checkpoint JSON: %w", err)
	}

	expectedChecksum := ckpt.SHA256Checksum
	if expectedChecksum == "" {
		return Checkpoint{}, errors.New("checkpoint missing SHA-256 checksum")
	}

	// Verify checksum
	ckpt.SHA256Checksum = ""
	verifyBytes, err := json.Marshal(ckpt)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("re-marshal for checksum verification: %w", err)
	}

	actualHash := sha256.Sum256(verifyBytes)
	actualHex := hex.EncodeToString(actualHash[:])

	if actualHex != expectedChecksum {
		return Checkpoint{}, fmt.Errorf("checkpoint integrity check failed: expected %s, got %s", expectedChecksum, actualHex)
	}

	ckpt.SHA256Checksum = expectedChecksum
	return ckpt, nil
}

// CheckpointReader converts a checkpoint into an io.Reader and returns its SHA-256 ID.
func CheckpointReader(ckpt Checkpoint) (io.Reader, string, error) {
	data, hashHex, err := SerializeCheckpoint(ckpt)
	if err != nil {
		return nil, "", err
	}
	return bytes.NewReader(data), hashHex, nil
}
