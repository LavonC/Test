package blobstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ErrQuotaExceeded    = errors.New("storage quota exceeded")
	ErrBlobNotFound     = errors.New("blob not found")
	ErrChecksumMismatch = errors.New("blob checksum mismatch")
	ErrInvalidID        = errors.New("invalid blob id")
	ErrStoreClosed      = errors.New("blob store closed")
)

// BlobInfo describes metadata about a stored blob.
type BlobInfo struct {
	ID        string    `json:"id"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	SHA256    string    `json:"sha256"`
	IsCorrupt bool      `json:"is_corrupt,omitempty"`
}

// StorageUsage represents current quota and consumption.
type StorageUsage struct {
	QuotaBytes     int64 `json:"quota_bytes"`
	AllocatedBytes int64 `json:"allocated_bytes"`
	UsedBytes      int64 `json:"used_bytes"`
	FreeBytes      int64 `json:"free_bytes"`
	BlobCount      int64 `json:"blob_count"`
}

// Config holds configuration for BlobStore.
type Config struct {
	RootDir    string
	QuotaBytes int64 // Maximum storage capacity in bytes
}

// Store defines the interface required by Objectives 2, 3, 4, and 5.
type Store interface {
	Put(id string, r io.Reader) (int64, error)
	Get(id string) (io.ReadCloser, error)
	Has(id string) bool
	Delete(id string) error
	Stat(id string) (BlobInfo, error)
	List() ([]BlobInfo, error)
	Verify(id string) (bool, error)
	Scrub() ([]string, error)
	Usage() StorageUsage
	Close() error
}

// DiskBlobStore implements Store backed by the local filesystem with quota accounting.
type DiskBlobStore struct {
	mu         sync.RWMutex
	rootDir    string
	tmpDir     string
	dataDir    string
	quotaBytes int64
	usedBytes  int64
	blobs      map[string]BlobInfo
	closed     bool
}

// validateID ensures an ID contains no directory traversal sequences or invalid characters.
func validateID(id string) error {
	if id == "" {
		return ErrInvalidID
	}
	if strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
		return ErrInvalidID
	}
	if filepath.Base(id) != id {
		return ErrInvalidID
	}
	return nil
}

// NewDiskBlobStore initializes a new local blob store.
func NewDiskBlobStore(cfg Config) (*DiskBlobStore, error) {
	if cfg.RootDir == "" {
		return nil, errors.New("root directory is required")
	}
	if cfg.QuotaBytes <= 0 {
		// Default to 10 GiB if unspecified
		cfg.QuotaBytes = 10 * 1024 * 1024 * 1024
	}

	dataDir := filepath.Join(cfg.RootDir, "data")
	tmpDir := filepath.Join(cfg.RootDir, "tmp")

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data dir: %w", err)
	}
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create tmp dir: %w", err)
	}

	s := &DiskBlobStore{
		rootDir:    cfg.RootDir,
		dataDir:    dataDir,
		tmpDir:     tmpDir,
		quotaBytes: cfg.QuotaBytes,
		blobs:      make(map[string]BlobInfo),
	}

	// Scan existing files and rebuild index and usage
	if err := s.scanExisting(); err != nil {
		return nil, fmt.Errorf("failed to scan existing blobs: %w", err)
	}

	return s, nil
}

func (s *DiskBlobStore) scanExisting() error {
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		id := entry.Name()
		if err := validateID(id); err != nil {
			continue
		}

		sha := ""
		if len(id) == 64 && isHex(id) {
			sha = id
		} else {
			f, err := os.Open(filepath.Join(s.dataDir, id))
			if err == nil {
				hasher := sha256.New()
				if _, err := io.Copy(hasher, f); err == nil {
					sha = hex.EncodeToString(hasher.Sum(nil))
				}
				_ = f.Close()
			}
		}

		s.blobs[id] = BlobInfo{
			ID:      id,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			SHA256:  sha,
		}
		s.usedBytes += info.Size()
	}
	return nil
}

func (s *DiskBlobStore) blobPath(id string) string {
	return filepath.Join(s.dataDir, id)
}

// Put writes data atomically from reader to store. If id matches a SHA256 hex string,
// it validates that the content hash matches id.
func (s *DiskBlobStore) Put(id string, r io.Reader) (int64, error) {
	if err := validateID(id); err != nil {
		return 0, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, ErrStoreClosed
	}

	// If already present, return existing size without duplicating
	if existing, ok := s.blobs[id]; ok {
		s.mu.Unlock()
		return existing.Size, nil
	}
	s.mu.Unlock()

	// Write to temporary file first for atomic commit
	tmpFile, err := os.CreateTemp(s.tmpDir, "upload-*")
	if err != nil {
		return 0, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	hasher := sha256.New()
	writer := io.MultiWriter(tmpFile, hasher)

	// Stream with quota monitoring
	written, err := io.Copy(writer, r)
	_ = tmpFile.Close()
	if err != nil {
		return 0, fmt.Errorf("failed during write: %w", err)
	}

	calculatedHash := hex.EncodeToString(hasher.Sum(nil))

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, ErrStoreClosed
	}

	// Re-check under lock in case another goroutine committed the same blob ID concurrently
	if existing, ok := s.blobs[id]; ok {
		return existing.Size, nil
	}

	// Verify quota
	if s.usedBytes+written > s.quotaBytes {
		return 0, fmt.Errorf("%w: current %d + new %d > limit %d", ErrQuotaExceeded, s.usedBytes, written, s.quotaBytes)
	}

	// If ID looks like a SHA256 hash (64 hex characters), verify content-addressing
	if len(id) == 64 && isHex(id) {
		if calculatedHash != id {
			return 0, fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, id, calculatedHash)
		}
	}

	targetPath := s.blobPath(id)
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return 0, fmt.Errorf("failed to commit blob: %w", err)
	}

	info := BlobInfo{
		ID:      id,
		Size:    written,
		ModTime: time.Now(),
		SHA256:  calculatedHash,
	}
	s.blobs[id] = info
	s.usedBytes += written

	return written, nil
}

// Get returns a reader for the requested blob ID.
func (s *DiskBlobStore) Get(id string) (io.ReadCloser, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, ErrStoreClosed
	}

	if _, ok := s.blobs[id]; !ok {
		return nil, ErrBlobNotFound
	}

	file, err := os.Open(s.blobPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrBlobNotFound
		}
		return nil, err
	}
	return file, nil
}

// Has checks if a blob exists in store.
func (s *DiskBlobStore) Has(id string) bool {
	if err := validateID(id); err != nil {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return false
	}
	_, ok := s.blobs[id]
	return ok
}

// Delete removes a blob from store and reclaims quota.
func (s *DiskBlobStore) Delete(id string) error {
	if err := validateID(id); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrStoreClosed
	}

	info, ok := s.blobs[id]
	if !ok {
		return ErrBlobNotFound
	}

	targetPath := s.blobPath(id)
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	s.usedBytes -= info.Size
	if s.usedBytes < 0 {
		s.usedBytes = 0
	}
	delete(s.blobs, id)
	return nil
}

// Stat returns metadata about a stored blob.
func (s *DiskBlobStore) Stat(id string) (BlobInfo, error) {
	if err := validateID(id); err != nil {
		return BlobInfo{}, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return BlobInfo{}, ErrStoreClosed
	}

	info, ok := s.blobs[id]
	if !ok {
		return BlobInfo{}, ErrBlobNotFound
	}
	return info, nil
}

// List returns all stored blobs.
func (s *DiskBlobStore) List() ([]BlobInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, ErrStoreClosed
	}

	list := make([]BlobInfo, 0, len(s.blobs))
	for _, b := range s.blobs {
		list = append(list, b)
	}
	return list, nil
}

// Verify checks the cryptographic integrity of a stored blob against its SHA-256 hash.
func (s *DiskBlobStore) Verify(id string) (bool, error) {
	if err := validateID(id); err != nil {
		return false, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return false, ErrStoreClosed
	}

	info, ok := s.blobs[id]
	if !ok {
		return false, ErrBlobNotFound
	}

	f, err := os.Open(s.blobPath(id))
	if err != nil {
		return false, err
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return false, err
	}

	computed := hex.EncodeToString(hasher.Sum(nil))
	if info.SHA256 != "" && computed != info.SHA256 {
		return false, nil
	}
	if len(id) == 64 && isHex(id) && computed != id {
		return false, nil
	}
	if info.SHA256 == "" && len(id) != 64 {
		return false, nil
	}
	return true, nil
}

// Scrub scans and verifies all stored blobs, flags any corrupted files,
// and returns the slice of corrupted blob IDs.
func (s *DiskBlobStore) Scrub() ([]string, error) {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, ErrStoreClosed
	}
	ids := make([]string, 0, len(s.blobs))
	for id := range s.blobs {
		ids = append(ids, id)
	}
	s.mu.RUnlock()

	var corruptIDs []string
	for _, id := range ids {
		valid, err := s.Verify(id)
		if err != nil || !valid {
			s.mu.Lock()
			if info, ok := s.blobs[id]; ok {
				info.IsCorrupt = true
				s.blobs[id] = info
			}
			s.mu.Unlock()
			corruptIDs = append(corruptIDs, id)
		}
	}
	return corruptIDs, nil
}

// Usage returns storage consumption and quota statistics.
func (s *DiskBlobStore) Usage() StorageUsage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	free := s.quotaBytes - s.usedBytes
	if free < 0 {
		free = 0
	}

	return StorageUsage{
		QuotaBytes:     s.quotaBytes,
		AllocatedBytes: s.quotaBytes,
		UsedBytes:      s.usedBytes,
		FreeBytes:      free,
		BlobCount:      int64(len(s.blobs)),
	}
}

// Close closes the blob store and cleans up temporary files.
func (s *DiskBlobStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	_ = os.RemoveAll(s.tmpDir)
	return nil
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
