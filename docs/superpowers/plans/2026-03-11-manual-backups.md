# Manual Backups Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add manual backup/restore/download for machine data volumes, with per-machine encryption and a user-facing feature toggle.

**Architecture:** Agent handles all GCS I/O (upload, download, decrypt, tar). Control plane orchestrates and stores metadata. User triggers operations via API/CLI/Dashboard. Machine must be stopped for create/restore.

**Tech Stack:** Go (AES-256-GCM, zstd, GCS client), PostgreSQL (metadata), React/TypeScript (dashboard), Cobra (CLI)

---

## Chunk 1: Encryption Primitives + BackupStore Interface

### Task 1: Streaming Encryption Primitives

**Files:**
- Create: `backend/internal/backup/crypto.go`
- Create: `backend/internal/backup/crypto_test.go`

- [ ] **Step 1: Write failing tests for key generation and streaming encrypt/decrypt**

Create `backend/internal/backup/crypto_test.go`:

```go
package backup

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"
)

func TestGenerateKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Fatalf("key length = %d, want 32", len(key))
	}
	// Two keys should be different
	key2, _ := GenerateKey()
	if bytes.Equal(key, key2) {
		t.Fatal("two generated keys should not be equal")
	}
}

func TestEncryptDecryptMasterKey(t *testing.T) {
	masterKey := make([]byte, 32)
	rand.Read(masterKey)
	machineKey, _ := GenerateKey()

	encrypted, err := EncryptKey(machineKey, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := DecryptKey(encrypted, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(machineKey, decrypted) {
		t.Fatal("decrypted key does not match original")
	}
}

func TestStreamEncryptDecrypt(t *testing.T) {
	key, _ := GenerateKey()
	plaintext := make([]byte, 1024*1024) // 1MB
	rand.Read(plaintext)

	// Encrypt
	var cipherBuf bytes.Buffer
	nonce, hmacHash, err := StreamEncrypt(bytes.NewReader(plaintext), &cipherBuf, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(nonce) != 12 {
		t.Fatalf("nonce length = %d, want 12", len(nonce))
	}
	if len(hmacHash) != 32 {
		t.Fatalf("hmac length = %d, want 32", len(hmacHash))
	}

	// Decrypt
	var plainBuf bytes.Buffer
	err = StreamDecrypt(&cipherBuf, &plainBuf, key, nonce, hmacHash)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plaintext, plainBuf.Bytes()) {
		t.Fatal("decrypted data does not match original")
	}
}

func TestStreamDecryptTamperedData(t *testing.T) {
	key, _ := GenerateKey()
	plaintext := []byte("hello world backup data")

	var cipherBuf bytes.Buffer
	nonce, hmacHash, _ := StreamEncrypt(bytes.NewReader(plaintext), &cipherBuf, key)

	// Tamper with ciphertext
	cipherBytes := cipherBuf.Bytes()
	cipherBytes[len(cipherBytes)/2] ^= 0xFF

	var plainBuf bytes.Buffer
	err := StreamDecrypt(bytes.NewReader(cipherBytes), &plainBuf, key, nonce, hmacHash)
	if err == nil {
		t.Fatal("expected error for tampered data")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/backup/ -v -run TestGenerate`
Expected: FAIL — package doesn't exist yet

- [ ] **Step 3: Implement crypto primitives**

Create `backend/internal/backup/crypto.go`:

```go
package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

// GenerateKey creates a 32-byte random AES-256 key.
func GenerateKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return key, nil
}

// EncryptKey encrypts a machine backup key with the platform master key using AES-256-GCM.
func EncryptKey(machineKey, masterKey []byte) ([]byte, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, machineKey, nil), nil
}

// DecryptKey decrypts a machine backup key with the platform master key.
func DecryptKey(encrypted, masterKey []byte) ([]byte, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(encrypted) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	return gcm.Open(nil, encrypted[:nonceSize], encrypted[nonceSize:], nil)
}

// StreamEncrypt reads plaintext from r, encrypts with AES-256-CTR, and writes to w.
// Returns the nonce and HMAC-SHA256 of the ciphertext for verification.
// Uses CTR mode (not GCM) for streaming — GCM requires knowing full size upfront.
func StreamEncrypt(r io.Reader, w io.Writer, key []byte) (nonce []byte, hmacHash []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}

	nonce = make([]byte, aes.BlockSize) // 16 bytes for CTR
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}

	stream := cipher.NewCTR(block, nonce)
	mac := hmac.New(sha256.New, key)

	// Stream: read plaintext -> encrypt -> write to both output and HMAC
	buf := make([]byte, 32*1024)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			encrypted := make([]byte, n)
			stream.XORKeyStream(encrypted, buf[:n])
			if _, err := w.Write(encrypted); err != nil {
				return nil, nil, fmt.Errorf("write encrypted: %w", err)
			}
			mac.Write(encrypted)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, nil, fmt.Errorf("read plaintext: %w", readErr)
		}
	}

	return nonce, mac.Sum(nil), nil
}

// StreamDecrypt reads ciphertext from r, verifies HMAC, decrypts with AES-256-CTR, writes to w.
func StreamDecrypt(r io.Reader, w io.Writer, key, nonce, expectedHMAC []byte) error {
	// First pass: read all ciphertext, verify HMAC
	ciphertext, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read ciphertext: %w", err)
	}

	mac := hmac.New(sha256.New, key)
	mac.Write(ciphertext)
	if !hmac.Equal(mac.Sum(nil), expectedHMAC) {
		return fmt.Errorf("HMAC verification failed: backup may be corrupted or tampered")
	}

	// Decrypt
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	stream := cipher.NewCTR(block, nonce)
	plaintext := make([]byte, len(ciphertext))
	stream.XORKeyStream(plaintext, ciphertext)

	_, err = w.Write(plaintext)
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/backup/ -v`
Expected: All 4 tests PASS

- [ ] **Step 5: Commit**

```bash
cd /home/mantiz/OpenClawMachines
git add backend/internal/backup/crypto.go backend/internal/backup/crypto_test.go
git commit -m "feat(backup): add streaming encryption primitives (AES-256-CTR + HMAC-SHA256)"
```

---

### Task 2: BackupStore Interface

**Files:**
- Create: `backend/internal/backup/store.go`

- [ ] **Step 1: Create the BackupStore interface**

```go
package backup

import (
	"context"
	"io"
	"time"
)

// BackupInfo contains metadata about a completed backup.
type BackupInfo struct {
	GCSPath        string
	SizeBytes      int64
	CompressedBytes int64
	ChecksumSHA256 string
	HMAC           []byte
	Nonce          []byte
	Timestamp      time.Time
}

// BackupStore handles backup upload/download to cloud storage.
type BackupStore interface {
	// Upload compresses, encrypts, and uploads a data volume to GCS.
	// dataVolumePath is the local path to the ext4 file.
	// Returns metadata about the uploaded backup.
	Upload(ctx context.Context, machineID string, dataVolumePath string, encryptionKey []byte) (*BackupInfo, error)

	// Download downloads, decrypts, and decompresses a backup from GCS to a local path.
	Download(ctx context.Context, gcsPath string, destPath string, encryptionKey, nonce, expectedHMAC []byte) error

	// StreamTarGz downloads a backup, decrypts, decompresses, mounts the ext4, and streams as tar.gz.
	StreamTarGz(ctx context.Context, gcsPath string, encryptionKey, nonce, expectedHMAC []byte, w io.Writer) error

	// Delete removes a backup from GCS.
	Delete(ctx context.Context, gcsPath string) error
}
```

- [ ] **Step 2: Commit**

```bash
cd /home/mantiz/OpenClawMachines
git add backend/internal/backup/store.go
git commit -m "feat(backup): add BackupStore interface"
```

---

### Task 3: GCS BackupStore Implementation

**Files:**
- Create: `backend/internal/backup/gcs.go`
- Create: `backend/internal/backup/gcs_test.go`

- [ ] **Step 1: Write the GCS implementation**

Reference: `backend/internal/rootfs/gcs.go` for GCS client patterns.

```go
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
)

// GCSStore implements BackupStore using Google Cloud Storage.
type GCSStore struct {
	client *storage.Client
	bucket string
	prefix string // e.g. "backups"
}

// NewGCSStore creates a new GCS-backed backup store.
func NewGCSStore(client *storage.Client, bucket, prefix string) *GCSStore {
	return &GCSStore{client: client, bucket: bucket, prefix: prefix}
}

func (s *GCSStore) gcsPath(machineID string, ts time.Time) string {
	return fmt.Sprintf("%s/%s/%s.ext4.zst.enc", s.prefix, machineID, ts.UTC().Format("20060102T150405Z"))
}

func (s *GCSStore) Upload(ctx context.Context, machineID string, dataVolumePath string, encryptionKey []byte) (*BackupInfo, error) {
	// Get original file size
	fi, err := os.Stat(dataVolumePath)
	if err != nil {
		return nil, fmt.Errorf("stat data volume: %w", err)
	}

	ts := time.Now()
	objPath := s.gcsPath(machineID, ts)

	slog.Info("backup.upload.start", "machine_id", machineID, "gcs_path", objPath, "size_bytes", fi.Size())

	// Open data volume
	f, err := os.Open(dataVolumePath)
	if err != nil {
		return nil, fmt.Errorf("open data volume: %w", err)
	}
	defer f.Close()

	// Hash the original file while reading
	hasher := sha256.New()
	teeReader := io.TeeReader(f, hasher)

	// Compress with zstd
	pr, pw := io.Pipe()
	zstdEncoder, err := zstd.NewWriter(pw, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, fmt.Errorf("create zstd encoder: %w", err)
	}

	// Compress in background goroutine
	var compressErr error
	go func() {
		defer pw.Close()
		if _, err := io.Copy(zstdEncoder, teeReader); err != nil {
			compressErr = err
			pw.CloseWithError(err)
			return
		}
		if err := zstdEncoder.Close(); err != nil {
			compressErr = err
			pw.CloseWithError(err)
		}
	}()

	// Encrypt compressed stream and upload to GCS
	obj := s.client.Bucket(s.bucket).Object(objPath)
	gcsWriter := obj.NewWriter(ctx)
	gcsWriter.ContentType = "application/octet-stream"

	nonce, hmacHash, err := StreamEncrypt(pr, gcsWriter, encryptionKey)
	if err != nil {
		gcsWriter.Close()
		return nil, fmt.Errorf("encrypt and upload: %w", err)
	}

	if err := gcsWriter.Close(); err != nil {
		return nil, fmt.Errorf("close GCS writer: %w", err)
	}
	if compressErr != nil {
		return nil, fmt.Errorf("compress: %w", compressErr)
	}

	// Get compressed+encrypted size from GCS
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		return nil, fmt.Errorf("get object attrs: %w", err)
	}

	info := &BackupInfo{
		GCSPath:         objPath,
		SizeBytes:       fi.Size(),
		CompressedBytes: attrs.Size,
		ChecksumSHA256:  hex.EncodeToString(hasher.Sum(nil)),
		HMAC:            hmacHash,
		Nonce:           nonce,
		Timestamp:       ts,
	}

	slog.Info("backup.upload.complete", "machine_id", machineID, "gcs_path", objPath,
		"size_bytes", fi.Size(), "compressed_bytes", attrs.Size)

	return info, nil
}

func (s *GCSStore) Download(ctx context.Context, gcsPath string, destPath string, encryptionKey, nonce, expectedHMAC []byte) error {
	slog.Info("backup.download.start", "gcs_path", gcsPath, "dest", destPath)

	obj := s.client.Bucket(s.bucket).Object(gcsPath)
	reader, err := obj.NewReader(ctx)
	if err != nil {
		return fmt.Errorf("open GCS object: %w", err)
	}
	defer reader.Close()

	// Decrypt
	var decryptedBuf strings.Builder
	// We need bytes, not strings — use a pipe instead
	decPR, decPW := io.Pipe()
	var decryptErr error
	go func() {
		defer decPW.Close()
		if err := StreamDecrypt(reader, decPW, encryptionKey, nonce, expectedHMAC); err != nil {
			decryptErr = err
			decPW.CloseWithError(err)
		}
	}()

	// Decompress zstd
	zr, err := zstd.NewReader(decPR)
	if err != nil {
		return fmt.Errorf("create zstd reader: %w", err)
	}
	defer zr.Close()

	// Write to temp file, then atomic rename
	tmpPath := destPath + ".tmp"
	outFile, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if _, err := io.Copy(outFile, zr); err != nil {
		outFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("decompress to file: %w", err)
	}
	outFile.Close()

	if decryptErr != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("decrypt: %w", decryptErr)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	slog.Info("backup.download.complete", "gcs_path", gcsPath, "dest", destPath)
	return nil
}

func (s *GCSStore) StreamTarGz(ctx context.Context, gcsPath string, encryptionKey, nonce, expectedHMAC []byte, w io.Writer) error {
	// Download and decrypt to temp file
	tmpDir, err := os.MkdirTemp("", "backup-export-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	ext4Path := filepath.Join(tmpDir, "volume.ext4")
	if err := s.Download(ctx, gcsPath, ext4Path, encryptionKey, nonce, expectedHMAC); err != nil {
		return fmt.Errorf("download backup: %w", err)
	}

	// Mount ext4 read-only
	mountDir := filepath.Join(tmpDir, "mnt")
	if err := os.MkdirAll(mountDir, 0o755); err != nil {
		return fmt.Errorf("create mount dir: %w", err)
	}

	cmd := exec.CommandContext(ctx, "mount", "-o", "loop,ro", ext4Path, mountDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount ext4: %s: %w", string(out), err)
	}
	defer exec.Command("umount", mountDir).Run()

	// Tar + gzip the mounted contents
	gzw := gzip.NewWriter(w)
	defer gzw.Close()
	tw := tar.NewWriter(gzw)
	defer tw.Close()

	return filepath.Walk(mountDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(mountDir, path)
		if relPath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relPath

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

func (s *GCSStore) Delete(ctx context.Context, gcsPath string) error {
	slog.Info("backup.delete", "gcs_path", gcsPath)
	return s.client.Bucket(s.bucket).Object(gcsPath).Delete(ctx)
}
```

- [ ] **Step 2: Write a basic unit test for GCSStore (using interface)**

Create `backend/internal/backup/gcs_test.go` — basic tests that verify the struct implements the interface:

```go
package backup

import "testing"

func TestGCSStoreImplementsInterface(t *testing.T) {
	// Compile-time check
	var _ BackupStore = (*GCSStore)(nil)
}

func TestGCSPathFormat(t *testing.T) {
	s := &GCSStore{prefix: "backups"}
	path := s.gcsPath("machine-123", mustParseTime("2026-03-11T08:15:00Z"))
	expected := "backups/machine-123/20260311T081500Z.ext4.zst.enc"
	if path != expected {
		t.Errorf("gcsPath = %q, want %q", path, expected)
	}
}
```

Add helper at bottom:

```go
import "time"

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
```

- [ ] **Step 3: Run tests**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/backup/ -v`
Expected: All tests PASS

- [ ] **Step 4: Commit**

```bash
cd /home/mantiz/OpenClawMachines
git add backend/internal/backup/gcs.go backend/internal/backup/gcs_test.go
git commit -m "feat(backup): add GCS BackupStore implementation"
```

---

## Chunk 2: Database Schema + Store Layer

### Task 4: Migration

**Files:**
- Create: `backend/migrations/032_machine_backups.sql`

- [ ] **Step 1: Create migration file**

```sql
-- 032: Machine backups

ALTER TABLE machines ADD COLUMN IF NOT EXISTS backups_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE machines ADD COLUMN IF NOT EXISTS backup_key BYTEA;

CREATE TABLE IF NOT EXISTS machine_backups (
    id               SERIAL PRIMARY KEY,
    machine_id       UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    timestamp        TIMESTAMPTZ NOT NULL,
    gcs_path         TEXT NOT NULL,
    size_bytes       BIGINT NOT NULL,
    compressed_bytes BIGINT NOT NULL,
    checksum_sha256  TEXT NOT NULL,
    hmac_sha256      BYTEA NOT NULL,
    nonce            BYTEA NOT NULL,
    trigger          TEXT NOT NULL DEFAULT 'manual',
    host_id          INT REFERENCES hosts(id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_machine_backups_machine_id ON machine_backups(machine_id);
CREATE INDEX IF NOT EXISTS idx_machine_backups_latest ON machine_backups(machine_id, timestamp DESC);
```

- [ ] **Step 2: Commit**

```bash
cd /home/mantiz/OpenClawMachines
git add backend/migrations/032_machine_backups.sql
git commit -m "feat(backup): add machine_backups schema migration"
```

---

### Task 5: Store Types + Queries

**Files:**
- Modify: `backend/internal/store/store.go`
- Modify: `backend/internal/store/postgres.go`

- [ ] **Step 1: Add BackupRecord type and Machine fields to store.go**

In `backend/internal/store/store.go`, after the Machine struct (line 72), add:

```go
type BackupRecord struct {
	ID              int       `json:"id"`
	MachineID       string    `json:"machine_id"`
	Timestamp       time.Time `json:"timestamp"`
	GCSPath         string    `json:"gcs_path"`
	SizeBytes       int64     `json:"size_bytes"`
	CompressedBytes int64     `json:"compressed_bytes"`
	ChecksumSHA256  string    `json:"checksum_sha256"`
	HMACSHA256      []byte    `json:"-"`
	Nonce           []byte    `json:"-"`
	Trigger         string    `json:"trigger"`
	HostID          *int      `json:"host_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}
```

Add two fields to the Machine struct (after `StorageMode` on line 71):

```go
	BackupsEnabled bool   `json:"backups_enabled"`
	BackupKey      []byte `json:"-"` // encrypted with platform master key
```

Add to the Store interface (find where other method groups are):

```go
	// Backups
	CreateBackupRecord(ctx context.Context, b *BackupRecord) error
	ListBackupRecords(ctx context.Context, machineID string) ([]BackupRecord, error)
	GetBackupRecord(ctx context.Context, id int) (*BackupRecord, error)
	DeleteBackupRecord(ctx context.Context, id int) error
	DeleteAllBackupRecords(ctx context.Context, machineID string) error
	EnableBackups(ctx context.Context, machineID string, backupKey []byte) error
	DisableBackups(ctx context.Context, machineID string) error
```

- [ ] **Step 2: Add Postgres queries**

In `backend/internal/store/postgres.go`, add the implementation. Find the end of existing methods and add:

```go
// ---- Backups ----

func (s *PostgresStore) CreateBackupRecord(ctx context.Context, b *BackupRecord) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO machine_backups (machine_id, timestamp, gcs_path, size_bytes, compressed_bytes,
		 checksum_sha256, hmac_sha256, nonce, trigger, host_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id, created_at`,
		b.MachineID, b.Timestamp, b.GCSPath, b.SizeBytes, b.CompressedBytes,
		b.ChecksumSHA256, b.HMACSHA256, b.Nonce, b.Trigger, b.HostID,
	).Scan(&b.ID, &b.CreatedAt)
}

func (s *PostgresStore) ListBackupRecords(ctx context.Context, machineID string) ([]BackupRecord, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, machine_id, timestamp, gcs_path, size_bytes, compressed_bytes,
		 checksum_sha256, hmac_sha256, nonce, trigger, host_id, created_at
		 FROM machine_backups WHERE machine_id = $1 ORDER BY timestamp DESC`,
		machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backups []BackupRecord
	for rows.Next() {
		var b BackupRecord
		if err := rows.Scan(&b.ID, &b.MachineID, &b.Timestamp, &b.GCSPath,
			&b.SizeBytes, &b.CompressedBytes, &b.ChecksumSHA256, &b.HMACSHA256,
			&b.Nonce, &b.Trigger, &b.HostID, &b.CreatedAt); err != nil {
			return nil, err
		}
		backups = append(backups, b)
	}
	return backups, nil
}

func (s *PostgresStore) GetBackupRecord(ctx context.Context, id int) (*BackupRecord, error) {
	var b BackupRecord
	err := s.pool.QueryRow(ctx,
		`SELECT id, machine_id, timestamp, gcs_path, size_bytes, compressed_bytes,
		 checksum_sha256, hmac_sha256, nonce, trigger, host_id, created_at
		 FROM machine_backups WHERE id = $1`, id,
	).Scan(&b.ID, &b.MachineID, &b.Timestamp, &b.GCSPath,
		&b.SizeBytes, &b.CompressedBytes, &b.ChecksumSHA256, &b.HMACSHA256,
		&b.Nonce, &b.Trigger, &b.HostID, &b.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *PostgresStore) DeleteBackupRecord(ctx context.Context, id int) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM machine_backups WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) DeleteAllBackupRecords(ctx context.Context, machineID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM machine_backups WHERE machine_id = $1`, machineID)
	return err
}

func (s *PostgresStore) EnableBackups(ctx context.Context, machineID string, backupKey []byte) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET backups_enabled = true, backup_key = $2 WHERE id = $1`,
		machineID, backupKey)
	return err
}

func (s *PostgresStore) DisableBackups(ctx context.Context, machineID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET backups_enabled = false WHERE id = $1`,
		machineID)
	return err
}
```

- [ ] **Step 3: Update the Machine scan query in postgres.go**

Find the `GetMachine` / scan query that reads Machine fields and add `backups_enabled` and `backup_key` to the SELECT and Scan calls. Search for the column list that includes `storage_mode` and add the two new columns after it.

- [ ] **Step 4: Run Go build to check compilation**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`
Expected: No errors

- [ ] **Step 5: Commit**

```bash
cd /home/mantiz/OpenClawMachines
git add backend/internal/store/store.go backend/internal/store/postgres.go
git commit -m "feat(backup): add BackupRecord type and Postgres queries"
```

---

## Chunk 3: Agent Config + Agent Endpoints

### Task 6: Agent Config

**Files:**
- Modify: `backend/internal/config/config.go`

- [ ] **Step 1: Add backup fields to AgentConfig**

In `backend/internal/config/config.go`, add to the `AgentConfig` struct:

```go
	// Backup
	BackupGCSBucket    string
	BackupGCSPrefix    string
	BackupMaxVolumeGB  int
	BackupMasterKey    string // 32-byte hex-encoded platform master key
	GCSServiceAccountKey string // JSON key for non-GCP hosts
```

- [ ] **Step 2: Add loading in LoadAgent()**

In the `LoadAgent()` function, add:

```go
	cfg.BackupGCSBucket = getEnv("BACKUP_GCS_BUCKET", "openclawmachines")
	cfg.BackupGCSPrefix = getEnv("BACKUP_GCS_PREFIX", "backups")
	cfg.BackupMasterKey = os.Getenv("BACKUP_MASTER_KEY")
	cfg.GCSServiceAccountKey = os.Getenv("GCS_SERVICE_ACCOUNT_KEY")
	if v := os.Getenv("BACKUP_MAX_VOLUME_GB"); v != "" {
		cfg.BackupMaxVolumeGB, _ = strconv.Atoi(v)
	}
	if cfg.BackupMaxVolumeGB == 0 {
		cfg.BackupMaxVolumeGB = 10
	}
```

- [ ] **Step 3: Commit**

```bash
cd /home/mantiz/OpenClawMachines
git add backend/internal/config/config.go
git commit -m "feat(backup): add backup config to AgentConfig"
```

---

### Task 7: Agent Backup Endpoints

**Files:**
- Modify: `backend/internal/agentapi/server.go`
- Modify: `backend/internal/agentapi/handlers.go`

- [ ] **Step 1: Add BackupStore to Server struct**

In `backend/internal/agentapi/server.go`, add field to Server struct (line 30):

```go
	backupStore backup.BackupStore
	dataDir     string // path to data volumes, e.g. /var/lib/ocm/data
```

Update `NewServer` signature and constructor to accept and store these.

- [ ] **Step 2: Register backup routes**

In `ControlRouter()`, inside the authenticated group (before line 87), add:

```go
		// Backup endpoints
		r.Post("/vms/{machineID}/backup", s.handleCreateBackup)
		r.Post("/vms/{machineID}/restore", s.handleRestoreBackup)
		r.Get("/vms/{machineID}/backup-download", s.handleBackupDownload)
```

- [ ] **Step 3: Implement agent backup handlers**

In `backend/internal/agentapi/handlers.go`, add:

```go
// ---- Backup handlers ----

type BackupRequest struct {
	EncryptionKey []byte `json:"encryption_key"` // decrypted machine backup key
}

type BackupResponse struct {
	GCSPath         string `json:"gcs_path"`
	SizeBytes       int64  `json:"size_bytes"`
	CompressedBytes int64  `json:"compressed_bytes"`
	ChecksumSHA256  string `json:"checksum_sha256"`
	HMAC            []byte `json:"hmac"`
	Nonce           []byte `json:"nonce"`
	Timestamp       string `json:"timestamp"`
}

type RestoreRequest struct {
	GCSPath       string `json:"gcs_path"`
	EncryptionKey []byte `json:"encryption_key"`
	Nonce         []byte `json:"nonce"`
	ExpectedHMAC  []byte `json:"expected_hmac"`
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	var req BackupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if s.backupStore == nil {
		http.Error(w, "backups not configured", http.StatusServiceUnavailable)
		return
	}

	// Check VM is not running
	s.orchestrator.(*orchestrator.Orchestrator) // We need to check VM status
	// Use a simpler approach: check if data volume exists
	dataVolPath := filepath.Join(s.dataDir, machineID+".ext4")
	if _, err := os.Stat(dataVolPath); os.IsNotExist(err) {
		http.Error(w, "no data volume found for machine", http.StatusNotFound)
		return
	}

	info, err := s.backupStore.Upload(r.Context(), machineID, dataVolPath, req.EncryptionKey)
	if err != nil {
		slog.Error("backup.create.failed", "machine_id", machineID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, BackupResponse{
		GCSPath:         info.GCSPath,
		SizeBytes:       info.SizeBytes,
		CompressedBytes: info.CompressedBytes,
		ChecksumSHA256:  info.ChecksumSHA256,
		HMAC:            info.HMAC,
		Nonce:           info.Nonce,
		Timestamp:       info.Timestamp.Format(time.RFC3339),
	})
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	var req RestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if s.backupStore == nil {
		http.Error(w, "backups not configured", http.StatusServiceUnavailable)
		return
	}

	dataVolPath := filepath.Join(s.dataDir, machineID+".ext4")

	if err := s.backupStore.Download(r.Context(), req.GCSPath, dataVolPath, req.EncryptionKey, req.Nonce, req.ExpectedHMAC); err != nil {
		slog.Error("backup.restore.failed", "machine_id", machineID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	gcsPath := r.URL.Query().Get("gcs_path")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "tar.gz"
	}

	// Encryption params passed as headers (binary data)
	encKey := r.Header.Get("X-Backup-Key")
	nonceHex := r.Header.Get("X-Backup-Nonce")
	hmacHex := r.Header.Get("X-Backup-HMAC")

	if gcsPath == "" || encKey == "" || nonceHex == "" || hmacHex == "" {
		http.Error(w, "missing required parameters", http.StatusBadRequest)
		return
	}

	// Decode hex-encoded binary params
	// ... (decode encKey, nonce, hmac from hex)

	if format == "tar.gz" {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-backup.tar.gz"`, machineID))
		if err := s.backupStore.StreamTarGz(r.Context(), gcsPath, []byte(encKey), nonce, hmac, w); err != nil {
			slog.Error("backup.download.failed", "machine_id", machineID, "error", err)
			// Can't send error status after headers are sent
		}
	} else {
		// Stream raw ext4.zst (decrypt only, no mount)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-backup.ext4.zst"`, machineID))
		// Download, decrypt, stream decompressed
		// ... similar to StreamTarGz but without mount+tar
	}
}
```

**Note for implementor:** The backup download handler needs careful implementation. The encryption key, nonce, and HMAC should be passed as base64-encoded headers or in a JSON request body (POST with body is cleaner than GET with headers). Adjust as needed during implementation.

- [ ] **Step 4: Run Go build**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`
Expected: No errors

- [ ] **Step 5: Commit**

```bash
cd /home/mantiz/OpenClawMachines
git add backend/internal/agentapi/server.go backend/internal/agentapi/handlers.go
git commit -m "feat(backup): add agent backup/restore/download endpoints"
```

---

## Chunk 4: Control Plane Endpoints + Agent Client

### Task 8: Agent Client Methods

**Files:**
- Modify: `backend/internal/agentclient/client.go`

- [ ] **Step 1: Add backup methods to agent client**

Follow the existing patterns (e.g., `StopVM`, `RollbackVM`):

```go
// BackupRequest is the request body for creating a backup on the agent.
type BackupAgentRequest struct {
	EncryptionKey []byte `json:"encryption_key"`
}

// BackupAgentResponse is the response from the agent backup endpoint.
type BackupAgentResponse struct {
	GCSPath         string `json:"gcs_path"`
	SizeBytes       int64  `json:"size_bytes"`
	CompressedBytes int64  `json:"compressed_bytes"`
	ChecksumSHA256  string `json:"checksum_sha256"`
	HMAC            []byte `json:"hmac"`
	Nonce           []byte `json:"nonce"`
	Timestamp       string `json:"timestamp"`
}

// RestoreAgentRequest is the request body for restoring a backup on the agent.
type RestoreAgentRequest struct {
	GCSPath       string `json:"gcs_path"`
	EncryptionKey []byte `json:"encryption_key"`
	Nonce         []byte `json:"nonce"`
	ExpectedHMAC  []byte `json:"expected_hmac"`
}

// BackupVM tells the agent to create a backup of a machine's data volume.
func (c *Client) BackupVM(ctx context.Context, host *store.Host, machineID string, encryptionKey []byte) (*BackupAgentResponse, error) {
	url := c.agentURL(host) + "/vms/" + machineID + "/backup"

	body, _ := json.Marshal(BackupAgentRequest{EncryptionKey: encryptionKey})

	// Use longer timeout for backup uploads
	longClient := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create backup request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.tokenForHost(host))
	req.Header.Set("Content-Type", "application/json")

	resp, err := longClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("backup VM on agent: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("backup VM: status %d: %s", resp.StatusCode, string(respBody))
	}

	var result BackupAgentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode backup response: %w", err)
	}
	return &result, nil
}

// RestoreVM tells the agent to restore a machine's data volume from a backup.
func (c *Client) RestoreVM(ctx context.Context, host *store.Host, machineID string, gcsPath string, encryptionKey, nonce, expectedHMAC []byte) error {
	url := c.agentURL(host) + "/vms/" + machineID + "/restore"

	body, _ := json.Marshal(RestoreAgentRequest{
		GCSPath:       gcsPath,
		EncryptionKey: encryptionKey,
		Nonce:         nonce,
		ExpectedHMAC:  expectedHMAC,
	})

	longClient := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create restore request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.tokenForHost(host))
	req.Header.Set("Content-Type", "application/json")

	resp, err := longClient.Do(req)
	if err != nil {
		return fmt.Errorf("restore VM on agent: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("restore VM: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
```

- [ ] **Step 2: Commit**

```bash
cd /home/mantiz/OpenClawMachines
git add backend/internal/agentclient/client.go
git commit -m "feat(backup): add BackupVM and RestoreVM to agent client"
```

---

### Task 9: Control Plane Backup Endpoints

**Files:**
- Create: `backend/internal/api/machine_backups.go`
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Create machine_backups.go**

Follow patterns from `machine_files.go` and existing handlers:

```go
package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/backup"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

func (s *Server) handleListMachineBackups(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	backups, err := s.store.ListBackupRecords(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list backups")
		return
	}
	if backups == nil {
		backups = []store.BackupRecord{}
	}

	writeJSON(w, http.StatusOK, backups)
}

func (s *Server) handleCreateMachineBackup(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}
	if machine.Status != "stopped" {
		writeError(w, http.StatusBadRequest, "machine must be stopped to create a backup")
		return
	}
	if !machine.BackupsEnabled {
		writeError(w, http.StatusBadRequest, "backups are not enabled for this machine")
		return
	}
	if machine.HostID == nil {
		writeError(w, http.StatusBadRequest, "machine has no host assigned (never started)")
		return
	}

	// Decrypt machine backup key
	masterKey, err := hex.DecodeString(s.backupMasterKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid backup master key config")
		return
	}
	encryptionKey, err := backup.DecryptKey(machine.BackupKey, masterKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decrypt backup key")
		return
	}

	// Get host for agent call
	host, err := s.store.GetHost(r.Context(), *machine.HostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get host")
		return
	}

	// Call agent to create backup
	result, err := s.agentClient.BackupVM(r.Context(), host, machineID, encryptionKey)
	if err != nil {
		slog.Error("backup.create.agent_failed", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "backup failed: "+err.Error())
		return
	}

	// Store metadata
	record := &store.BackupRecord{
		MachineID:       machineID,
		Timestamp:       mustParseTime(result.Timestamp),
		GCSPath:         result.GCSPath,
		SizeBytes:       result.SizeBytes,
		CompressedBytes: result.CompressedBytes,
		ChecksumSHA256:  result.ChecksumSHA256,
		HMACSHA256:      result.HMAC,
		Nonce:           result.Nonce,
		Trigger:         "manual",
		HostID:          machine.HostID,
	}
	if err := s.store.CreateBackupRecord(r.Context(), record); err != nil {
		slog.Error("backup.create.store_failed", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "backup created but failed to store metadata")
		return
	}

	// Enforce retention: delete oldest if > 3
	s.enforceBackupRetention(r.Context(), machineID)

	writeJSON(w, http.StatusCreated, record)
}

func (s *Server) handleRestoreMachineBackup(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")
	backupIDStr := chi.URLParam(r, "backupId")
	backupID, err := strconv.Atoi(backupIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid backup ID")
		return
	}

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil || machine.AccountID != accountID {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.Status != "stopped" {
		writeError(w, http.StatusBadRequest, "machine must be stopped to restore a backup")
		return
	}
	if machine.HostID == nil {
		writeError(w, http.StatusBadRequest, "machine has no host assigned")
		return
	}

	record, err := s.store.GetBackupRecord(r.Context(), backupID)
	if err != nil || record.MachineID != machineID {
		writeError(w, http.StatusNotFound, "backup not found")
		return
	}

	masterKey, _ := hex.DecodeString(s.backupMasterKey)
	encryptionKey, err := backup.DecryptKey(machine.BackupKey, masterKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decrypt backup key")
		return
	}

	host, err := s.store.GetHost(r.Context(), *machine.HostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get host")
		return
	}

	if err := s.agentClient.RestoreVM(r.Context(), host, machineID, record.GCSPath, encryptionKey, record.Nonce, record.HMACSHA256); err != nil {
		writeError(w, http.StatusInternalServerError, "restore failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "restored"})
}

func (s *Server) handleDeleteMachineBackup(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")
	backupIDStr := chi.URLParam(r, "backupId")
	backupID, _ := strconv.Atoi(backupIDStr)

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil || machine.AccountID != accountID {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	record, err := s.store.GetBackupRecord(r.Context(), backupID)
	if err != nil || record.MachineID != machineID {
		writeError(w, http.StatusNotFound, "backup not found")
		return
	}

	// Delete from GCS (best effort)
	// s.backupGCSClient.Delete(r.Context(), record.GCSPath) — TODO: add GCS client to server

	if err := s.store.DeleteBackupRecord(r.Context(), backupID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete backup")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDownloadMachineBackup(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")
	backupIDStr := chi.URLParam(r, "backupId")
	backupID, _ := strconv.Atoi(backupIDStr)

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil || machine.AccountID != accountID {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.HostID == nil {
		writeError(w, http.StatusBadRequest, "no host assigned — machine must have been started at least once")
		return
	}

	record, err := s.store.GetBackupRecord(r.Context(), backupID)
	if err != nil || record.MachineID != machineID {
		writeError(w, http.StatusNotFound, "backup not found")
		return
	}

	// Proxy download request to agent
	host, err := s.store.GetHost(r.Context(), *machine.HostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get host")
		return
	}

	masterKey, _ := hex.DecodeString(s.backupMasterKey)
	encryptionKey, _ := backup.DecryptKey(machine.BackupKey, masterKey)

	// Build agent URL with params
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "tar.gz"
	}

	agentURL := fmt.Sprintf("%s/vms/%s/backup-download?gcs_path=%s&format=%s",
		s.agentClient.AgentURL(host), machineID, record.GCSPath, format)

	// Forward encryption params as headers
	agentReq, _ := http.NewRequestWithContext(r.Context(), "GET", agentURL, nil)
	agentReq.Header.Set("Authorization", "Bearer "+s.agentClient.TokenForHost(host))
	agentReq.Header.Set("X-Backup-Key", hex.EncodeToString(encryptionKey))
	agentReq.Header.Set("X-Backup-Nonce", hex.EncodeToString(record.Nonce))
	agentReq.Header.Set("X-Backup-HMAC", hex.EncodeToString(record.HMACSHA256))

	resp, err := http.DefaultClient.Do(agentReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent unreachable")
		return
	}
	defer resp.Body.Close()

	// Forward response headers and body
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (s *Server) enforceBackupRetention(ctx context.Context, machineID string) {
	backups, err := s.store.ListBackupRecords(ctx, machineID)
	if err != nil || len(backups) <= 3 {
		return
	}
	// Delete oldest (list is ordered by timestamp DESC, so oldest is last)
	for _, b := range backups[3:] {
		s.store.DeleteBackupRecord(ctx, b.ID)
		// TODO: also delete from GCS
	}
}
```

- [ ] **Step 2: Register routes in server.go**

In `backend/internal/api/server.go`, find the machine routes (after rollback), add:

```go
		// Backups
		r.Get("/backups", srv.handleListMachineBackups)
		r.Post("/backups", srv.handleCreateMachineBackup)
		r.Post("/backups/{backupId}/restore", srv.handleRestoreMachineBackup)
		r.Delete("/backups/{backupId}", srv.handleDeleteMachineBackup)
		r.Get("/backups/{backupId}/download", srv.handleDownloadMachineBackup)
```

Add `backupMasterKey string` field to Server struct and wire it from config.

- [ ] **Step 3: Add backup toggle to handleUpdateMachine**

In `handleUpdateMachine`, add support for `backups_enabled` field in the request body. When enabling:

```go
if req.BackupsEnabled != nil && *req.BackupsEnabled && !machine.BackupsEnabled {
	// Generate and encrypt backup key
	machineKey, _ := backup.GenerateKey()
	masterKey, _ := hex.DecodeString(s.backupMasterKey)
	encryptedKey, _ := backup.EncryptKey(machineKey, masterKey)
	s.store.EnableBackups(r.Context(), machineID, encryptedKey)
} else if req.BackupsEnabled != nil && !*req.BackupsEnabled {
	s.store.DisableBackups(r.Context(), machineID)
}
```

- [ ] **Step 4: Build and verify**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`
Expected: No errors

- [ ] **Step 5: Commit**

```bash
cd /home/mantiz/OpenClawMachines
git add backend/internal/api/machine_backups.go backend/internal/api/server.go
git commit -m "feat(backup): add control plane backup CRUD endpoints"
```

---

## Chunk 5: CLI Commands

### Task 10: CLI Backup Subcommands

**Files:**
- Create: `cli/internal/commands/machines_backups.go`
- Modify: `cli/internal/commands/machines.go`
- Modify: `cli/internal/api/types.go`

- [ ] **Step 1: Add Backup type to CLI API types**

In `cli/internal/api/types.go`, add:

```go
type Backup struct {
	ID              int       `json:"id"`
	MachineID       string    `json:"machine_id"`
	Timestamp       time.Time `json:"timestamp"`
	GCSPath         string    `json:"gcs_path"`
	SizeBytes       int64     `json:"size_bytes"`
	CompressedBytes int64     `json:"compressed_bytes"`
	Trigger         string    `json:"trigger"`
	CreatedAt       time.Time `json:"created_at"`
}
```

- [ ] **Step 2: Create machines_backups.go**

Follow patterns from `machines_secrets.go` and `machines_logs.go`:

```go
package commands

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"
)

var backupsCmd = &cobra.Command{
	Use:   "backups",
	Short: "Manage machine backups",
	RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
}

var backupsListCmd = &cobra.Command{
	Use:   "list [MACHINE]",
	Short: "List backups for a machine",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupsList,
}

var backupsCreateCmd = &cobra.Command{
	Use:   "create [MACHINE]",
	Short: "Create a backup (machine must be stopped)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupsCreate,
}

var backupsDownloadCmd = &cobra.Command{
	Use:   "download [MACHINE]",
	Short: "Download a backup",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupsDownload,
}

var backupsRestoreCmd = &cobra.Command{
	Use:   "restore [MACHINE]",
	Short: "Restore from a backup (machine must be stopped)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupsRestore,
}

var backupsEnableCmd = &cobra.Command{
	Use:   "enable [MACHINE]",
	Short: "Enable backups for a machine",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupsEnable,
}

var backupsDisableCmd = &cobra.Command{
	Use:   "disable [MACHINE]",
	Short: "Disable backups for a machine",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupsDisable,
}

func init() {
	machinesCmd.AddCommand(backupsCmd)
	backupsCmd.AddCommand(backupsListCmd)
	backupsCmd.AddCommand(backupsCreateCmd)
	backupsCmd.AddCommand(backupsDownloadCmd)
	backupsCmd.AddCommand(backupsRestoreCmd)
	backupsCmd.AddCommand(backupsEnableCmd)
	backupsCmd.AddCommand(backupsDisableCmd)

	backupsDownloadCmd.Flags().Int("id", 0, "Backup ID (defaults to latest)")
	backupsDownloadCmd.Flags().String("format", "tar.gz", "Download format: tar.gz or ext4")
	backupsDownloadCmd.Flags().StringP("output", "o", "", "Output file path")

	backupsRestoreCmd.Flags().Int("id", 0, "Backup ID to restore (required)")
	backupsRestoreCmd.MarkFlagRequired("id")
}

func runBackupsList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient()
	if err != nil {
		return err
	}
	machine, err := resolveMachineFromArgs(args)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/accounts/%d/machines/%s/backups", cfg.DefaultAccountID, machine.ID)
	resp, err := client.Get(path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var backups []api.Backup
	if err := readJSON(resp, &backups); err != nil {
		return err
	}

	if isJSONOutput(cmd) {
		return printJSON(backups)
	}

	if len(backups) == 0 {
		fmt.Println("No backups found.")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTIMESTAMP\tSIZE\tTRIGGER\tAGE")
	for _, b := range backups {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n",
			b.ID,
			b.Timestamp.Format("2006-01-02 15:04:05"),
			humanize.IBytes(uint64(b.CompressedBytes)),
			b.Trigger,
			humanize.Time(b.Timestamp),
		)
	}
	return tw.Flush()
}

func runBackupsCreate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient()
	if err != nil {
		return err
	}
	machine, err := resolveMachineFromArgs(args)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/accounts/%d/machines/%s/backups", cfg.DefaultAccountID, machine.ID)
	resp, err := client.PostLong(path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		return apiError(resp.StatusCode, resp.Body)
	}

	var backup api.Backup
	if err := readJSON(resp, &backup); err != nil {
		return err
	}

	fmt.Printf("Backup created (id: %d, size: %s)\n", backup.ID, humanize.IBytes(uint64(backup.CompressedBytes)))
	return nil
}

func runBackupsDownload(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient()
	if err != nil {
		return err
	}
	machine, err := resolveMachineFromArgs(args)
	if err != nil {
		return err
	}

	backupID, _ := cmd.Flags().GetInt("id")
	format, _ := cmd.Flags().GetString("format")
	output, _ := cmd.Flags().GetString("output")

	// If no ID specified, get latest
	if backupID == 0 {
		listPath := fmt.Sprintf("/api/accounts/%d/machines/%s/backups", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Get(listPath)
		if err != nil {
			return err
		}
		var backups []api.Backup
		readJSON(resp, &backups)
		resp.Body.Close()
		if len(backups) == 0 {
			return fmt.Errorf("no backups found for %s", machine.Name)
		}
		backupID = backups[0].ID
	}

	dlPath := fmt.Sprintf("/api/accounts/%d/machines/%s/backups/%d/download?format=%s",
		cfg.DefaultAccountID, machine.ID, backupID, format)

	ctx := cmd.Context()
	resp, err := client.GetStream(ctx, dlPath)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return apiError(resp.StatusCode, resp.Body)
	}

	if output == "" {
		ext := "tar.gz"
		if format == "ext4" {
			ext = "ext4.zst"
		}
		output = fmt.Sprintf("%s-backup-%d.%s", machine.Slug, backupID, ext)
	}

	f, err := os.Create(output)
	if err != nil {
		return err
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return err
	}

	fmt.Printf("Downloaded %s (%s)\n", output, humanize.IBytes(uint64(n)))
	return nil
}

func runBackupsRestore(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient()
	if err != nil {
		return err
	}
	machine, err := resolveMachineFromArgs(args)
	if err != nil {
		return err
	}

	backupID, _ := cmd.Flags().GetInt("id")

	path := fmt.Sprintf("/api/accounts/%d/machines/%s/backups/%d/restore",
		cfg.DefaultAccountID, machine.ID, backupID)
	resp, err := client.PostLong(path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return apiError(resp.StatusCode, resp.Body)
	}

	fmt.Printf("Restored from backup %d\n", backupID)
	return nil
}

func runBackupsEnable(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient()
	if err != nil {
		return err
	}
	machine, err := resolveMachineFromArgs(args)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/accounts/%d/machines/%s", cfg.DefaultAccountID, machine.ID)
	body := []byte(`{"backups_enabled": true}`)
	resp, err := client.Put(path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return apiError(resp.StatusCode, resp.Body)
	}

	fmt.Printf("Backups enabled for %s\n", machine.Name)
	return nil
}

func runBackupsDisable(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient()
	if err != nil {
		return err
	}
	machine, err := resolveMachineFromArgs(args)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/accounts/%d/machines/%s", cfg.DefaultAccountID, machine.ID)
	body := []byte(`{"backups_enabled": false}`)
	resp, err := client.Put(path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return apiError(resp.StatusCode, resp.Body)
	}

	fmt.Printf("Backups disabled for %s\n", machine.Name)
	return nil
}
```

**Note:** The `resolveMachineFromArgs` helper and `humanize` package may need adjustment. Check if `go-humanize` is in `cli/go.mod` — if not, use `fmt.Sprintf("%.1f MB", float64(bytes)/1024/1024)` instead.

- [ ] **Step 3: Build CLI**

Run: `cd /home/mantiz/OpenClawMachines/cli && go build ./...`
Expected: No errors

- [ ] **Step 4: Commit**

```bash
cd /home/mantiz/OpenClawMachines
git add cli/internal/commands/machines_backups.go cli/internal/api/types.go
git commit -m "feat(backup): add CLI backup commands (list, create, download, restore, enable, disable)"
```

---

## Chunk 6: Frontend Dashboard

### Task 11: API Functions + Types

**Files:**
- Modify: `frontend/src/lib/api.ts`

- [ ] **Step 1: Add backup types and API functions**

```typescript
export interface Backup {
  id: number;
  machine_id: string;
  timestamp: string;
  gcs_path: string;
  size_bytes: number;
  compressed_bytes: number;
  trigger: string;
  created_at: string;
}

export const listMachineBackups = (accountId: number, machineId: string) =>
  request<Backup[]>(`/accounts/${accountId}/machines/${machineId}/backups`);

export const createMachineBackup = (accountId: number, machineId: string) =>
  request<Backup>(`/accounts/${accountId}/machines/${machineId}/backups`, {
    method: "POST",
  });

export const restoreMachineBackup = (accountId: number, machineId: string, backupId: number) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/backups/${backupId}/restore`, {
    method: "POST",
  });

export const deleteMachineBackup = (accountId: number, machineId: string, backupId: number) =>
  request<void>(`/accounts/${accountId}/machines/${machineId}/backups/${backupId}`, {
    method: "DELETE",
  });

export const backupDownloadUrl = (accountId: number, machineId: string, backupId: number, format = "tar.gz") =>
  `${BASE}/accounts/${accountId}/machines/${machineId}/backups/${backupId}/download?format=${format}`;

export const updateMachine = (accountId: number, machineId: string, data: { backups_enabled?: boolean }) =>
  request<Machine>(`/accounts/${accountId}/machines/${machineId}`, {
    method: "PUT",
    body: JSON.stringify(data),
  });
```

- [ ] **Step 2: Commit**

---

### Task 12: BackupsTab Component

**Files:**
- Create: `frontend/src/components/BackupsTab.tsx`

- [ ] **Step 1: Create the BackupsTab component**

This is a meaningful UI decision — there are trade-offs in how to present backup actions, confirmation flows, and status updates.

**For the implementor:** Create `frontend/src/components/BackupsTab.tsx` that:
- Takes `accountId`, `machine` (Machine type), and `onStatusChange` callback as props
- Fetches backups via `listMachineBackups` on mount
- Shows enable/disable toggle using `updateMachine`
- Shows backup list as a table with columns: ID, Timestamp, Size, Trigger, Actions
- Download button: opens `backupDownloadUrl` in new tab
- Create button: calls `createMachineBackup`, shows loading state, refreshes list
- Restore button: confirmation dialog first ("This will replace current data"), calls `restoreMachineBackup`
- Delete button: confirmation, calls `deleteMachineBackup`, refreshes list
- Use existing `useToast` hook for success/error notifications
- Use existing `useOperation` hook for loading states
- Follow patterns from existing tabs (CredentialsTab, ConfigTab)

- [ ] **Step 2: Add tab to MachineView**

In `frontend/src/pages/MachineView.tsx`:
- Add `"backups"` to the `Tab` type union
- Add `{ id: "backups", label: "Backups" }` to the `TABS` array
- Import and render `<BackupsTab>` when `activeTab === "backups"`

- [ ] **Step 3: Add backup indicator to MachineCard**

In `frontend/src/components/MachineCard.tsx`:
- Add a small text line showing backup status: "Backups: enabled" or nothing if disabled
- This is informational only — no API call needed, use `machine.backups_enabled` field

- [ ] **Step 4: Build frontend**

Run: `cd /home/mantiz/OpenClawMachines/frontend && npm run build`
Expected: No errors

- [ ] **Step 5: Commit**

```bash
cd /home/mantiz/OpenClawMachines
git add frontend/src/components/BackupsTab.tsx frontend/src/pages/MachineView.tsx frontend/src/components/MachineCard.tsx frontend/src/lib/api.ts
git commit -m "feat(backup): add Dashboard backups tab, enable toggle, and MachineCard indicator"
```

---

## Chunk 7: Agent Wiring + Final Build

### Task 13: Wire BackupStore in Agent Main

**Files:**
- Modify: `backend/cmd/agent/main.go`

- [ ] **Step 1: Initialize BackupStore in agent startup**

Find where the orchestrator and agentapi.Server are created in `main.go`. Add:

```go
// Initialize backup store (GCS)
var backupStore backup.BackupStore
if cfg.BackupGCSBucket != "" {
	gcsClient, err := storage.NewClient(ctx)
	if err != nil {
		slog.Warn("backup.gcs_client_failed", "error", err)
	} else {
		backupStore = backup.NewGCSStore(gcsClient, cfg.BackupGCSBucket, cfg.BackupGCSPrefix)
		slog.Info("backup.enabled", "bucket", cfg.BackupGCSBucket, "prefix", cfg.BackupGCSPrefix)
	}
}
```

Pass `backupStore` and `cfg.DataDir` to `agentapi.NewServer(...)`.

- [ ] **Step 2: Update NewServer signature**

The `agentapi.NewServer` call needs two new params. Update the call site to pass them.

- [ ] **Step 3: Build everything**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`
Expected: No errors

- [ ] **Step 4: Run tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: All tests pass

- [ ] **Step 5: Commit**

```bash
cd /home/mantiz/OpenClawMachines
git add backend/cmd/agent/main.go
git commit -m "feat(backup): wire BackupStore into agent startup"
```

---

### Task 14: Control Plane Wiring

**Files:**
- Modify: `backend/internal/api/server.go`
- Modify: `backend/cmd/server/main.go`

- [ ] **Step 1: Add backupMasterKey to Server**

Add `backupMasterKey string` to the Server struct in `server.go`. Wire it from config in `cmd/server/main.go`.

- [ ] **Step 2: Expose agentURL and tokenForHost from agent client**

The download handler needs to build agent URLs. Either:
- Add `AgentURL(host) string` and `TokenForHost(host) string` public methods to `agentclient.Client`, or
- Proxy through existing client methods

- [ ] **Step 3: Build and test**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: All tests pass

- [ ] **Step 4: Final commit**

```bash
cd /home/mantiz/OpenClawMachines
git add -A
git commit -m "feat(backup): wire backup master key and agent client helpers"
```

---

### Task 15: Verify Full Build

- [ ] **Step 1: Run make verify**

Run: `cd /home/mantiz/OpenClawMachines && make test-go && make typecheck`
Expected: All pass

- [ ] **Step 2: Review all changes**

Run: `git log --oneline backups..HEAD` and `git diff --stat backups` to verify all changes are accounted for.

---

## Summary of Files

### Created (9 files)
| File | Purpose |
|------|---------|
| `backend/internal/backup/crypto.go` | AES-256-CTR streaming encrypt/decrypt, HMAC, key management |
| `backend/internal/backup/crypto_test.go` | Encryption tests |
| `backend/internal/backup/store.go` | BackupStore interface |
| `backend/internal/backup/gcs.go` | GCS implementation |
| `backend/internal/backup/gcs_test.go` | Interface compliance + path tests |
| `backend/internal/api/machine_backups.go` | Control plane backup CRUD |
| `backend/migrations/032_machine_backups.sql` | Schema |
| `cli/internal/commands/machines_backups.go` | CLI commands |
| `frontend/src/components/BackupsTab.tsx` | Dashboard tab |

### Modified (11 files)
| File | Change |
|------|--------|
| `backend/internal/store/store.go` | BackupRecord, Machine.BackupsEnabled/BackupKey, store interface |
| `backend/internal/store/postgres.go` | Backup CRUD queries, Machine scan update |
| `backend/internal/config/config.go` | Backup config in AgentConfig |
| `backend/internal/agentapi/server.go` | BackupStore field, routes |
| `backend/internal/agentapi/handlers.go` | Backup/restore/download handlers |
| `backend/internal/agentclient/client.go` | BackupVM, RestoreVM methods |
| `backend/internal/api/server.go` | Backup routes, backupMasterKey, updateMachine toggle |
| `backend/cmd/agent/main.go` | Init BackupStore |
| `cli/internal/api/types.go` | Backup type |
| `frontend/src/pages/MachineView.tsx` | Backups tab |
| `frontend/src/lib/api.ts` | Backup API functions |
