package blobstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
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

	// Corrupt file on disk directly
	rawPath := filepath.Join(tmpDir, "data", cid)
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

	// Corrupt blob 2 on disk
	corruptPath := filepath.Join(tmpDir, "data", cid2)
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
