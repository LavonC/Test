package blobstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestBlobStore_BasicPutGet(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	data := []byte("hello unicompute objective 1 storage substrate")
	hasher := sha256.New()
	hasher.Write(data)
	cid := hex.EncodeToString(hasher.Sum(nil))

	n, err := store.Put(cid, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if n != int64(len(data)) {
		t.Fatalf("expected written %d, got %d", len(data), n)
	}

	if !store.Has(cid) {
		t.Fatalf("expected store to have %s", cid)
	}

	rc, err := store.Get(cid)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	defer rc.Close()

	readData, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(readData, data) {
		t.Fatalf("read data mismatch: expected %q, got %q", string(data), string(readData))
	}

	// Verify integrity
	valid, err := store.Verify(cid)
	if err != nil || !valid {
		t.Fatalf("Verify failed, valid=%v, err=%v", valid, err)
	}

	// Stat
	info, err := store.Stat(cid)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if info.Size != int64(len(data)) {
		t.Fatalf("expected size %d, got %d", len(data), info.Size)
	}

	usage := store.Usage()
	if usage.UsedBytes != int64(len(data)) {
		t.Fatalf("expected used %d, got %d", len(data), usage.UsedBytes)
	}
	if usage.BlobCount != 1 {
		t.Fatalf("expected 1 blob, got %d", usage.BlobCount)
	}
}

func TestBlobStore_QuotaEnforcement(t *testing.T) {
	tmpDir := t.TempDir()
	limit := int64(100)
	store, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: limit,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Put 60 bytes
	data1 := bytes.Repeat([]byte("a"), 60)
	h1 := sha256.Sum256(data1)
	cid1 := hex.EncodeToString(h1[:])
	_, err = store.Put(cid1, bytes.NewReader(data1))
	if err != nil {
		t.Fatalf("first Put failed: %v", err)
	}

	// Put another 60 bytes (total 120 > limit 100) -> should fail
	data2 := bytes.Repeat([]byte("b"), 60)
	h2 := sha256.Sum256(data2)
	cid2 := hex.EncodeToString(h2[:])
	_, err = store.Put(cid2, bytes.NewReader(data2))
	if err == nil {
		t.Fatalf("expected quota exceeded error, got nil")
	}
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded, got: %v", err)
	}

	// Delete first blob, reclaiming 60 bytes
	if err := store.Delete(cid1); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Now Put second blob should succeed
	_, err = store.Put(cid2, bytes.NewReader(data2))
	if err != nil {
		t.Fatalf("Put after delete failed: %v", err)
	}
}

func TestBlobStore_EarlyQuotaRejection(t *testing.T) {
	tmpDir := t.TempDir()
	limit := int64(1024)
	store, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: limit,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// An oversized reader (10 KiB > 1 KiB quota)
	oversizedReader := io.LimitReader(bytes.NewReader(bytes.Repeat([]byte("z"), 10*1024)), 10*1024)
	dummyID := "custom-oversized-blob"
	_, err = store.Put(dummyID, oversizedReader)
	if err == nil {
		t.Fatalf("expected early quota exceeded error, got nil")
	}
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded, got: %v", err)
	}

	// Verify temp directory has no leftover files
	entries, err := os.ReadDir(filepath.Join(tmpDir, "tmp"))
	if err == nil && len(entries) > 0 {
		t.Errorf("expected no temp files left, found %d", len(entries))
	}
}

func TestBlobStore_ChecksumMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	fakeCID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	data := []byte("some actual data that does not hash to fakeCID")

	_, err = store.Put(fakeCID, bytes.NewReader(data))
	if err == nil {
		t.Fatalf("expected checksum mismatch error, got nil")
	}
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got: %v", err)
	}
}

func TestBlobStore_CorruptionDetection(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	data := []byte("original uncorrupted data")
	h := sha256.Sum256(data)
	cid := hex.EncodeToString(h[:])

	_, err = store.Put(cid, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Corrupt file on disk directly at sharded path
	rawPath := filepath.Join(tmpDir, "data", cid[:2], cid[2:4], cid)
	err = os.WriteFile(rawPath, []byte("tampered corrupted data"), 0644)
	if err != nil {
		t.Fatalf("failed to corrupt file: %v", err)
	}

	valid, err := store.Verify(cid)
	if err != nil {
		t.Fatalf("Verify error: %v", err)
	}
	if valid {
		t.Fatalf("expected Verify to report false for corrupted file")
	}
}

func TestBlobStore_PersistenceReload(t *testing.T) {
	tmpDir := t.TempDir()
	data := []byte("persist across reloads")
	h := sha256.Sum256(data)
	cid := hex.EncodeToString(h[:])

	store1, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create store1: %v", err)
	}
	_, err = store1.Put(cid, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("store1 Put failed: %v", err)
	}
	store1.Close()

	// Reopen
	store2, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to reopen store2: %v", err)
	}
	defer store2.Close()

	if !store2.Has(cid) {
		t.Fatalf("expected store2 to have persisted cid %s", cid)
	}
	if store2.Usage().UsedBytes != int64(len(data)) {
		t.Fatalf("expected used bytes %d, got %d", len(data), store2.Usage().UsedBytes)
	}
}

func TestBlobStore_ConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: 10 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	var wg sync.WaitGroup
	count := 20

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			b := []byte{byte(idx), byte(idx * 2), byte(idx * 3)}
			h := sha256.Sum256(b)
			cid := hex.EncodeToString(h[:])

			_, err := store.Put(cid, bytes.NewReader(b))
			if err != nil {
				t.Errorf("concurrent put failed: %v", err)
				return
			}

			rc, err := store.Get(cid)
			if err != nil {
				t.Errorf("concurrent get failed: %v", err)
				return
			}
			defer rc.Close()
			read, _ := io.ReadAll(rc)
			if !bytes.Equal(read, b) {
				t.Errorf("concurrent read mismatch")
			}
		}(i)
	}

	wg.Wait()
}

func TestBlobStore_DirectoryTraversalPrevention(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	badIDs := []string{
		"../escape",
		"..\\escape",
		"foo/bar",
		"foo\\bar",
		"/etc/passwd",
		"C:\\Windows\\system32",
		"..",
		".",
	}

	for _, badID := range badIDs {
		_, err := store.Put(badID, bytes.NewReader([]byte("evil")))
		if !errors.Is(err, ErrInvalidID) {
			t.Errorf("expected ErrInvalidID for Put(%q), got %v", badID, err)
		}

		_, err = store.Get(badID)
		if !errors.Is(err, ErrInvalidID) {
			t.Errorf("expected ErrInvalidID for Get(%q), got %v", badID, err)
		}

		if store.Has(badID) {
			t.Errorf("expected Has(%q) to be false", badID)
		}

		err = store.Delete(badID)
		if !errors.Is(err, ErrInvalidID) {
			t.Errorf("expected ErrInvalidID for Delete(%q), got %v", badID, err)
		}

		_, err = store.Stat(badID)
		if !errors.Is(err, ErrInvalidID) {
			t.Errorf("expected ErrInvalidID for Stat(%q), got %v", badID, err)
		}

		_, err = store.Verify(badID)
		if !errors.Is(err, ErrInvalidID) {
			t.Errorf("expected ErrInvalidID for Verify(%q), got %v", badID, err)
		}
	}
}

func TestBlobStore_ConcurrentSameBlobPut(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	data := []byte("identical payload written concurrently by multiple workers")
	h := sha256.Sum256(data)
	cid := hex.EncodeToString(h[:])

	var wg sync.WaitGroup
	workers := 10
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, putErr := store.Put(cid, bytes.NewReader(data))
			if putErr != nil {
				t.Errorf("concurrent put failed: %v", putErr)
			}
		}()
	}
	wg.Wait()

	// CRITICAL: UsedBytes must equal exactly 1x length, not 10x!
	usage := store.Usage()
	expectedBytes := int64(len(data))
	if usage.UsedBytes != expectedBytes {
		t.Fatalf("quota double-accounting detected: expected %d used bytes, got %d", expectedBytes, usage.UsedBytes)
	}
	if usage.BlobCount != 1 {
		t.Fatalf("expected 1 blob count, got %d", usage.BlobCount)
	}
}

func TestBlobStore_Scrub(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Write blob 1 (healthy)
	data1 := []byte("healthy blob 1")
	h1 := sha256.Sum256(data1)
	cid1 := hex.EncodeToString(h1[:])
	_, _ = store.Put(cid1, bytes.NewReader(data1))

	// Write blob 2 (will corrupt)
	data2 := []byte("soon to be corrupted blob 2")
	h2 := sha256.Sum256(data2)
	cid2 := hex.EncodeToString(h2[:])
	_, _ = store.Put(cid2, bytes.NewReader(data2))

	// Write non-hash ID blob (will also test persistence & scrub)
	data3 := []byte("custom named blob")
	cid3 := "named-blob-3"
	_, _ = store.Put(cid3, bytes.NewReader(data3))

	// Corrupt blob 2 on disk at sharded path
	corruptPath := filepath.Join(tmpDir, "data", cid2[:2], cid2[2:4], cid2)
	_ = os.WriteFile(corruptPath, []byte("garbage tampered bytes"), 0644)

	corrupted, err := store.Scrub()
	if err != nil {
		t.Fatalf("Scrub returned error: %v", err)
	}

	if len(corrupted) != 1 || corrupted[0] != cid2 {
		t.Fatalf("expected corrupted list [%s], got %v", cid2, corrupted)
	}

	info2, _ := store.Stat(cid2)
	if !info2.IsCorrupt {
		t.Fatalf("expected BlobInfo.IsCorrupt=true for corrupted blob")
	}

	info1, _ := store.Stat(cid1)
	if info1.IsCorrupt {
		t.Fatalf("healthy blob falsely marked corrupt")
	}
}

func TestBlobStore_StrictIDAllowlistAndUppercaseSHA256(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	badIDs := []string{
		"",
		"id'quote",
		`id"doublequote`,
		"id<tag>",
		"id>angle",
		"id with space",
		"id\tcontrol",
		"id\nnewline",
		"id\x00null",
		"id;semicolon",
		"id&amp",
		strings.Repeat("a", 129),
	}

	for _, id := range badIDs {
		if _, err := store.Put(id, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Put(%q) expected ErrInvalidID, got %v", id, err)
		}
		if _, err := store.Get(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Get(%q) expected ErrInvalidID, got %v", id, err)
		}
		if _, err := store.Verify(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Verify(%q) expected ErrInvalidID, got %v", id, err)
		}
		if err := store.Delete(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Delete(%q) expected ErrInvalidID, got %v", id, err)
		}
	}

	// Valid uppercase 64-char SHA-256 hex ID must succeed cleanly
	data := []byte("uppercase sha256 verification payload")
	h := sha256.Sum256(data)
	upperCID := strings.ToUpper(hex.EncodeToString(h[:]))

	n, err := store.Put(upperCID, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put with uppercase SHA-256 failed: %v", err)
	}
	if n != int64(len(data)) {
		t.Fatalf("expected %d written, got %d", len(data), n)
	}
	valid, err := store.Verify(upperCID)
	if err != nil || !valid {
		t.Fatalf("Verify with uppercase SHA-256 failed: valid=%v, err=%v", valid, err)
	}
}

func TestBlobStore_ShardedLayoutAndLegacyFlatReload(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	payload := []byte("sharded blob content verification")
	h := sha256.Sum256(payload)
	cid := hex.EncodeToString(h[:])
	if _, err := store.Put(cid, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Put sharded failed: %v", err)
	}

	shardedPath := filepath.Join(tmpDir, "data", cid[:2], cid[2:4], cid)
	if _, err := os.Stat(shardedPath); err != nil {
		t.Fatalf("expected sharded file at %s, got err: %v", shardedPath, err)
	}

	// Short ID (< 4 chars) fallback
	shortID := "ab1"
	shortData := []byte("short id data")
	if _, err := store.Put(shortID, bytes.NewReader(shortData)); err != nil {
		t.Fatalf("Put short ID failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "data", shortID)); err != nil {
		t.Fatalf("expected short ID fallback at data/%s: %v", shortID, err)
	}
	_ = store.Close()

	// Seed a legacy flat file in data/
	legacyPayload := []byte("legacy flat blob content")
	legacyH := sha256.Sum256(legacyPayload)
	legacyCID := hex.EncodeToString(legacyH[:])
	if err := os.WriteFile(filepath.Join(tmpDir, "data", legacyCID), legacyPayload, 0644); err != nil {
		t.Fatalf("failed to write legacy flat file: %v", err)
	}

	// Reopen and verify both sharded, short, and legacy flat blobs are indexed and usable
	reopened, err := NewDiskBlobStore(Config{
		RootDir:    tmpDir,
		QuotaBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer reopened.Close()

	for _, id := range []string{cid, shortID, legacyCID} {
		if !reopened.Has(id) {
			t.Fatalf("expected reopened store to have %s", id)
		}
		valid, err := reopened.Verify(id)
		if err != nil || !valid {
			t.Fatalf("Verify(%s) failed after reload: valid=%v err=%v", id, valid, err)
		}
	}

	rc, err := reopened.Get(legacyCID)
	if err != nil {
		t.Fatalf("Get legacy flat blob failed: %v", err)
	}
	gotLegacy, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(gotLegacy, legacyPayload) {
		t.Fatalf("legacy flat blob content mismatch")
	}

	if err := reopened.Delete(legacyCID); err != nil {
		t.Fatalf("Delete legacy flat blob failed: %v", err)
	}
	if reopened.Has(legacyCID) {
		t.Fatalf("expected legacy flat blob to be deleted")
	}
}

func TestBlobStore_SweepsOrphanedTempFilesOnStartup(t *testing.T) {
	root := t.TempDir()
	tmpDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		t.Fatalf("MkdirAll tmp failed: %v", err)
	}
	orphan1 := filepath.Join(tmpDir, "upload-crash-12345")
	if err := os.WriteFile(orphan1, []byte("partial crash data"), 0644); err != nil {
		t.Fatalf("WriteFile orphan1 failed: %v", err)
	}
	orphanSub := filepath.Join(tmpDir, "nested-crash")
	if err := os.MkdirAll(orphanSub, 0755); err != nil {
		t.Fatalf("MkdirAll nested failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphanSub, "chunk"), []byte("stale"), 0644); err != nil {
		t.Fatalf("WriteFile nested failed: %v", err)
	}

	store, err := NewDiskBlobStore(Config{
		RootDir:    root,
		QuotaBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewDiskBlobStore failed: %v", err)
	}
	defer store.Close()

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir tmpDir failed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected NewDiskBlobStore to sweep all crash-orphaned temp entries, found %d", len(entries))
	}
}

type blockingChunkReader struct {
	data    []byte
	offset  int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingChunkReader) Read(p []byte) (int, error) {
	if r.offset == 0 && len(r.data) > 0 {
		n := copy(p, r.data)
		r.offset += n
		return n, nil
	}
	r.once.Do(func() {
		close(r.started)
	})
	<-r.release
	return 0, io.EOF
}

func TestBlobStore_ConcurrentStagingBoundedByQuota(t *testing.T) {
	root := t.TempDir()
	const quota int64 = 1024
	store, err := NewDiskBlobStore(Config{
		RootDir:    root,
		QuotaBytes: quota,
	})
	if err != nil {
		t.Fatalf("NewDiskBlobStore failed: %v", err)
	}
	defer store.Close()

	// Goroutine 1 stages 700 bytes and pauses mid-stream before committing
	r1 := &blockingChunkReader{
		data:    bytes.Repeat([]byte("A"), 700),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	put1ErrCh := make(chan error, 1)
	go func() {
		_, err := store.Put("staged-blob-1", r1)
		put1ErrCh <- err
	}()

	<-r1.started

	// Concurrent Put of 600 bytes (700 + 600 = 1300 > 1024) must fail immediately due to reservedBytes
	_, err2 := store.Put("staged-blob-2", bytes.NewReader(bytes.Repeat([]byte("B"), 600)))
	if !errors.Is(err2, ErrQuotaExceeded) {
		close(r1.release)
		t.Fatalf("expected concurrent Put exceeding remaining reserved quota to fail with ErrQuotaExceeded, got: %v", err2)
	}

	// Verify tmp/ never holds more than quota
	var tmpBytes int64
	entries, _ := os.ReadDir(filepath.Join(root, "tmp"))
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			tmpBytes += info.Size()
		}
	}
	if tmpBytes > quota {
		close(r1.release)
		t.Fatalf("tmp/ staged bytes %d exceeded quota %d", tmpBytes, quota)
	}

	// Release Put 1 so it commits cleanly
	close(r1.release)
	if err1 := <-put1ErrCh; err1 != nil {
		t.Fatalf("first Put failed: %v", err1)
	}

	store.mu.RLock()
	reservedAfter := store.reservedBytes
	store.mu.RUnlock()
	if reservedAfter != 0 {
		t.Fatalf("expected reservedBytes == 0 after Put completion, got %d", reservedAfter)
	}

	// Now 324 bytes remain; a 300-byte Put must succeed
	if _, err := store.Put("staged-blob-3", bytes.NewReader(bytes.Repeat([]byte("C"), 300))); err != nil {
		t.Fatalf("expected 300-byte Put within remaining quota to succeed, got: %v", err)
	}
}
