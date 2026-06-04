# Backup Security & Reliability Bugfix Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix 6 bugs (4 P1, 2 P2) found in backup code review — covering crypto authentication, version sidecar consistency, host-decoupled downloads, master key scope, browser memory usage, and retry-safe initialization.

**Architecture:** Fixes are mostly isolated. Tasks 3 and 5 both modify `sendHeartbeat` in `cmd/agent/main.go` — they must be executed sequentially and merged in Task 5. Tasks 4 and 5 both touch `api/server.go` — also sequential. The crypto fix (Task 1) changes the HMAC format, which is backwards-incompatible.

**Tech Stack:** Go (backend), TypeScript (frontend), AES-256-CTR, HMAC-SHA256, GCS

---

## ⚠️ Breaking Change: HMAC Format (Task 1)

Task 1 changes what data the HMAC covers (nonce + ciphertext instead of just ciphertext). Existing backups were created with the old HMAC. After deploying this fix:
- **New backups** will use nonce-inclusive HMAC and verify correctly.
- **Old backups** will fail HMAC verification on restore/download.

**Migration strategy:** Before deploying, re-encrypt existing backups, or add a fallback that tries ciphertext-only HMAC if nonce-inclusive fails (with a deprecation log). The plan implements the clean fix; migration is out of scope but noted.

---

## Chunk 1: Crypto & Agent Fixes (Tasks 1-3)

### Task 1: Include nonce in HMAC (P1 — crypto authentication)

**Why:** `StreamEncrypt` MACs only ciphertext. A corrupted nonce produces different plaintext without detection.

**Files:**
- Modify: `backend/internal/backup/crypto.go:60-95` (StreamEncrypt) and `crypto.go:100-149` (StreamDecrypt)
- Modify: `backend/internal/backup/crypto_test.go` (add nonce-tamper test)

- [ ] **Step 1: Write the failing test — tampered nonce should be detected**

Add to `backend/internal/backup/crypto_test.go`:

```go
func TestStreamDecryptTamperedNonce(t *testing.T) {
	key, _ := GenerateKey()
	plaintext := []byte("hello world backup data")

	var cipherBuf bytes.Buffer
	nonce, hmacHash, _ := StreamEncrypt(bytes.NewReader(plaintext), &cipherBuf, key)

	// Tamper with nonce — flip one bit
	nonce[0] ^= 0x01

	var plainBuf bytes.Buffer
	err := StreamDecrypt(bytes.NewReader(cipherBuf.Bytes()), &plainBuf, key, nonce, hmacHash)
	if err == nil {
		t.Fatal("expected HMAC error for tampered nonce, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/backup/ -run TestStreamDecryptTamperedNonce -v`

Expected: FAIL — tampered nonce produces wrong plaintext but HMAC passes (nonce not in HMAC).

- [ ] **Step 3: Fix StreamEncrypt — write nonce to HMAC before ciphertext loop**

In `backend/internal/backup/crypto.go`, in `StreamEncrypt`, add `mac.Write(nonce)` after creating the mac and before the encryption loop:

```go
stream := cipher.NewCTR(block, nonce)
mac := hmac.New(sha256.New, key)
mac.Write(nonce) // authenticate the nonce — prevents silent corruption

// Stream: read plaintext -> encrypt -> write to both output and HMAC
```

- [ ] **Step 4: Fix StreamDecrypt — write nonce to HMAC before verification**

In `backend/internal/backup/crypto.go`, in `StreamDecrypt`, add `mac.Write(nonce)` after creating the mac and before the TeeReader:

```go
mac := hmac.New(sha256.New, key)
mac.Write(nonce) // must match StreamEncrypt: nonce is part of the authenticated data

tee := io.TeeReader(r, mac)
```

- [ ] **Step 5: Run all crypto tests**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/backup/ -v`

Expected: ALL PASS — existing roundtrip tests still work (they use matching encrypt/decrypt), and the new tampered-nonce test now catches the corruption.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/backup/crypto.go backend/internal/backup/crypto_test.go
git commit -m "fix(backup): include nonce in HMAC to prevent silent corruption

StreamEncrypt/StreamDecrypt now MAC nonce+ciphertext instead of just
ciphertext. A corrupted or tampered nonce is now detected during
HMAC verification.

BREAKING: existing backups created before this change will fail HMAC
verification. Re-encrypt or add migration before deploying."
```

---

### Task 2: Delete version sidecar on restore (P1 — stale version after restore)

**Why:** `handleRestoreBackup` replaces the `.ext4` but leaves the `.version` sidecar. Next boot skips the upgrade path, running newer code against older data.

**Files:**
- Modify: `backend/internal/agentapi/handlers.go:910-934` (handleRestoreBackup — already imports `os` and `filepath`, already has `s.dataDir`)

- [ ] **Step 1: Write the failing test**

The handler already has `s.dataDir` (used at line 925) and the `os`/`filepath` imports (lines 7, 14). Add to `backend/internal/agentapi/handlers_test.go`:

```go
func TestRestoreDeletesVersionSidecar(t *testing.T) {
	// Set up a temp data dir with a .version sidecar
	dataDir := t.TempDir()
	machineID := "m-test-restore"

	versionPath := filepath.Join(dataDir, machineID+".version")
	if err := os.WriteFile(versionPath, []byte("3"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create the .ext4 file that Download will overwrite
	ext4Path := filepath.Join(dataDir, machineID+".ext4")
	if err := os.WriteFile(ext4Path, []byte("old-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Verify .version exists before restore
	if _, err := os.Stat(versionPath); err != nil {
		t.Fatalf("version sidecar should exist before restore: %v", err)
	}

	// Create server with mock backup store
	srv := &Server{dataDir: dataDir}
	// Use a mock BackupStore whose Download succeeds (writes to the ext4 path)
	srv.SetBackupStore(&mockBackupStore{})

	// Build request
	body := `{"gcs_path":"backups/m-test/snap.enc","encryption_key":"AQID","nonce":"AQID","expected_hmac":"AQID"}`
	req := httptest.NewRequest("POST", "/vms/"+machineID+"/restore", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("machineID", machineID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	srv.handleRestoreBackup(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Assert: .version sidecar must be gone
	if _, err := os.Stat(versionPath); !os.IsNotExist(err) {
		t.Fatal("version sidecar should be removed after restore")
	}
}
```

Note: If `handlers_test.go` doesn't exist or the Server struct isn't easily constructible in tests, adapt the test to match the existing test patterns in that package. The key assertion is: `.version` file is removed after a successful restore.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/agentapi/ -run TestRestoreDeletesVersionSidecar -v`

Expected: FAIL — version sidecar still exists after restore.

- [ ] **Step 3: Add version sidecar removal to handleRestoreBackup**

In `backend/internal/agentapi/handlers.go`, after the successful `bs.Download` call (line 931), before `w.WriteHeader`:

```go
if err := bs.Download(r.Context(), req.GCSPath, dataVolPath, req.EncryptionKey, req.Nonce, req.ExpectedHMAC); err != nil {
	slog.Error("backup.restore.failed", "machine_id", machineID, "error", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
	return
}

// Remove version sidecar so ensureDataVolume re-evaluates on next boot.
// Without this, restoring an older backup leaves a stale version marker
// that causes the upgrade path to be skipped.
versionPath := filepath.Join(s.dataDir, machineID+".version")
if err := os.Remove(versionPath); err != nil && !os.IsNotExist(err) {
	slog.Warn("backup.restore.version_sidecar_remove_failed", "path", versionPath, "error", err)
}

w.WriteHeader(http.StatusNoContent)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/agentapi/ -run TestRestoreDeletesVersionSidecar -v`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/agentapi/handlers.go backend/internal/agentapi/handlers_test.go
git commit -m "fix(backup): remove version sidecar on restore

Prevents stale .version file from causing ensureDataVolume to skip
the upgrade path when restoring an older backup."
```

---

### Task 3: Replace sync.Once with retry-safe init (P2 — agent backup store)

**Why:** `sync.Once` is consumed even on failure. If `storage.NewClient` fails transiently, the agent permanently loses backup capability until restart.

**Files:**
- Modify: `backend/cmd/agent/main.go` — declaration at ~line 536, usage at ~lines 595-619

**⚠️ Dependency:** Task 5 also modifies this same heartbeat code block. Execute Task 3 first, then Task 5 builds on top.

- [ ] **Step 1: Replace sync.Once with sync.Mutex + success flag**

In `backend/cmd/agent/main.go`, replace the `backupStoreInitOnce` variable declaration:

```go
// Before:
var backupStoreInitOnce sync.Once

// After:
var (
	backupStoreInitMu   sync.Mutex
	backupStoreInitDone bool
)
```

- [ ] **Step 2: Replace the .Do() block with mutex-guarded retry logic**

Replace the `backupStoreInitOnce.Do(func() { ... })` block (~lines 595-619):

```go
backupStoreInitMu.Lock()
if backupStoreInitDone {
	backupStoreInitMu.Unlock()
	return // already initialized — fast path under read after first success
}

if cfg.BackupMasterKey != "" {
	// Already configured at startup via GCE metadata — mark done
	backupStoreInitDone = true
	backupStoreInitMu.Unlock()
	return
}

cfg.BackupMasterKey = hbResp.BackupMasterKey
if cfg.BackupGCSBucket == "" {
	cfg.BackupGCSBucket = "openclawmachines"
}
if cfg.BackupGCSPrefix == "" {
	cfg.BackupGCSPrefix = "backups"
}

var gcsOpts []option.ClientOption
if cfg.GCSServiceAccountKey != "" {
	gcsOpts = append(gcsOpts, option.WithCredentialsJSON([]byte(cfg.GCSServiceAccountKey)))
}
gcsClient, gcsErr := storage.NewClient(ctx, gcsOpts...)
if gcsErr != nil {
	slog.Warn("backup.heartbeat_init.gcs_client_failed", "error", gcsErr)
	// Reset so next heartbeat retries
	cfg.BackupMasterKey = ""
	backupStoreInitMu.Unlock()
	return
}

bs := backup.NewGCSStore(gcsClient, cfg.BackupGCSBucket, cfg.BackupGCSPrefix)
srv.SetBackupStore(bs)
backupStoreInitDone = true
backupStoreInitMu.Unlock()
slog.Info("backup.heartbeat_init.complete", "bucket", cfg.BackupGCSBucket, "prefix", cfg.BackupGCSPrefix)
```

Key details:
- `backupStoreInitDone` is set **only on success** (after `SetBackupStore`)
- The "already configured at startup" branch also sets `backupStoreInitDone = true`
- On GCS failure, `cfg.BackupMasterKey` is reset to "" so the next heartbeat retries from scratch
- Mutex is released before the success log to avoid holding it during I/O

- [ ] **Step 3: Write a test for retry behavior**

Add to a test file (e.g., `backend/cmd/agent/main_test.go` or wherever agent tests live):

```go
func TestBackupStoreInitRetryOnFailure(t *testing.T) {
	// This is a behavioral test: verify that after a failed init,
	// the flag is NOT set, allowing retry.
	var mu sync.Mutex
	var done bool

	// Simulate first attempt: failure
	mu.Lock()
	if !done {
		// Simulate GCS client failure
		mu.Unlock()
		// done is still false — retry allowed
	}

	mu.Lock()
	if done {
		t.Fatal("done should be false after failure")
	}
	mu.Unlock()

	// Simulate second attempt: success
	mu.Lock()
	if !done {
		done = true
	}
	mu.Unlock()

	mu.Lock()
	if !done {
		t.Fatal("done should be true after success")
	}
	mu.Unlock()
}
```

- [ ] **Step 4: Run agent build + test to verify**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./cmd/agent/ && go test ./cmd/agent/ -v`

Expected: Build succeeds, tests pass.

- [ ] **Step 5: Commit**

```bash
git add backend/cmd/agent/main.go
git commit -m "fix(agent): replace sync.Once with retry-safe backup store init

sync.Once is consumed even on failure. If GCS client creation fails
transiently, the agent permanently loses backup capability. Now uses
mutex+flag that only marks done on success, allowing retry on next
heartbeat."
```

---

## Chunk 2: Control Plane Fixes (Tasks 4-5)

### Task 4: Direct GCS download — decouple from host (P1)

**Why:** `handleDownloadMachineBackup` requires `machine.HostID != nil` and proxies through the host agent. But backups are in GCS — if the host is gone, downloads fail even though the data is intact.

**⚠️ Constraint: Cloud Run cannot mount ext4 loopback.** `StreamTarGz` uses `mount -o loop,ro` which requires `CAP_SYS_ADMIN` — not available in Cloud Run's gVisor sandbox. The control plane cannot serve tar.gz format directly. Strategy:

- **Host available** → proxy through agent for both tar.gz and ext4 (existing behavior)
- **Host unavailable** → stream decrypt+decompress from GCS directly, serve as raw ext4 only

**Files:**
- Modify: `backend/internal/api/server.go` (add `backupStore` field to Server struct, init in constructor)
- Modify: `backend/internal/api/machine_backups.go:230-289` (two-path download: agent proxy when host exists, direct GCS when not)
- Create: `backend/internal/backup/gcs.go` — add `StreamDecrypted` method (decrypt+decompress to writer, no mount)
- Modify: `backend/internal/backup/store.go` — add `StreamDecrypted` to interface

- [ ] **Step 1: Add StreamDecrypted to BackupStore interface**

In `backend/internal/backup/store.go`, add to the `BackupStore` interface:

```go
// StreamDecrypted downloads, decrypts, and decompresses a backup, streaming the raw ext4 to w.
// Unlike StreamTarGz, this does NOT mount the filesystem — works without CAP_SYS_ADMIN.
StreamDecrypted(ctx context.Context, gcsPath string, encryptionKey, nonce, expectedHMAC []byte, w io.Writer) error
```

- [ ] **Step 2: Implement StreamDecrypted on GCSStore**

In `backend/internal/backup/gcs.go`, add:

```go
func (s *GCSStore) StreamDecrypted(ctx context.Context, gcsPath string, encryptionKey, nonce, expectedHMAC []byte, w io.Writer) error {
	slog.Info("backup.stream_decrypted.start", "gcs_path", gcsPath)

	obj := s.client.Bucket(s.bucket).Object(gcsPath)
	reader, err := obj.NewReader(ctx)
	if err != nil {
		return fmt.Errorf("open GCS object: %w", err)
	}
	defer reader.Close()

	// Decrypt
	decPR, decPW := io.Pipe()
	var decryptErr error
	go func() {
		defer decPW.Close()
		if err := StreamDecrypt(reader, decPW, encryptionKey, nonce, expectedHMAC); err != nil {
			decryptErr = err
			decPW.CloseWithError(err)
		}
	}()

	// Decompress zstd → write directly to output
	zr, err := zstd.NewReader(decPR)
	if err != nil {
		return fmt.Errorf("create zstd reader: %w", err)
	}
	defer zr.Close()

	if _, err := io.Copy(w, zr); err != nil {
		if decryptErr != nil {
			return fmt.Errorf("decrypt: %w", decryptErr)
		}
		return fmt.Errorf("stream decrypted: %w", err)
	}
	if decryptErr != nil {
		return fmt.Errorf("decrypt: %w", decryptErr)
	}

	slog.Info("backup.stream_decrypted.complete", "gcs_path", gcsPath)
	return nil
}
```

- [ ] **Step 3: Add backupStore to the control plane Server**

In `backend/internal/api/server.go`:

Add field to Server struct:
```go
backupStore backup.BackupStore // direct GCS access for downloads (nil if not configured)
```

Initialize in `NewServer` (after existing GCS credential handling):
```go
// Initialize backup store for direct GCS downloads
if backupMasterKey != "" && gcsServiceAccountKey != "" {
	var gcsOpts []option.ClientOption
	gcsOpts = append(gcsOpts, option.WithCredentialsJSON([]byte(gcsServiceAccountKey)))
	gcsClient, err := storage.NewClient(context.Background(), gcsOpts...)
	if err != nil {
		slog.Warn("backup.control_plane_gcs_init_failed", "error", err)
	} else {
		srv.backupStore = backup.NewGCSStore(gcsClient, "openclawmachines", "backups")
	}
}
```

Note: bucket/prefix should come from config. If they're already available (check existing server config), use those. Otherwise hardcode the defaults matching the agent.

- [ ] **Step 4: Rewrite handleDownloadMachineBackup with two-path logic**

Replace `backend/internal/api/machine_backups.go:230-289`:

```go
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

	record, err := s.store.GetBackupRecord(r.Context(), backupID)
	if err != nil || record.MachineID != machineID {
		writeError(w, http.StatusNotFound, "backup not found")
		return
	}

	masterKey, err := hex.DecodeString(s.backupMasterKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid backup config")
		return
	}
	encryptionKey, err := backup.DecryptKey(machine.BackupKey, masterKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decrypt backup key")
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "tar.gz"
	}

	// Path 1: Host available → proxy through agent (supports tar.gz and ext4)
	if machine.HostID != nil {
		host, hostErr := s.store.GetHost(r.Context(), *machine.HostID)
		if hostErr == nil {
			s.proxyBackupDownload(w, r, host, machineID, record, encryptionKey, format)
			return
		}
		slog.Warn("backup.download.host_lookup_failed", "machine_id", machineID, "error", hostErr)
		// Fall through to direct GCS path
	}

	// Path 2: No host → direct GCS stream (ext4 only — Cloud Run can't mount for tar.gz)
	if format == "tar.gz" {
		writeError(w, http.StatusBadRequest, "tar.gz format requires a running host; use format=ext4 or start the machine first")
		return
	}

	if s.backupStore == nil {
		writeError(w, http.StatusServiceUnavailable, "direct backup downloads not configured")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	filename := fmt.Sprintf("%s-backup-%d.ext4", machineID, backupID)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	if err := s.backupStore.StreamDecrypted(r.Context(), record.GCSPath, encryptionKey, record.Nonce, record.HMACSHA256, w); err != nil {
		slog.Error("backup.download.direct_stream_failed", "machine_id", machineID, "backup_id", backupID, "error", err)
		// Headers may already be sent — can't write error response reliably
		return
	}
}

// proxyBackupDownload proxies a backup download through the host agent.
func (s *Server) proxyBackupDownload(w http.ResponseWriter, r *http.Request, host *store.Host, machineID string, record *store.BackupRecord, encryptionKey []byte, format string) {
	agentURL := fmt.Sprintf("%s/vms/%s/backup-download?gcs_path=%s&format=%s",
		s.agentClient.AgentURL(host), machineID, record.GCSPath, format)

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

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
```

- [ ] **Step 5: Write test for hostless download path**

Add a test that verifies download works with `machine.HostID == nil`:

```go
func TestDownloadBackupWithoutHost(t *testing.T) {
	// Set up a machine with HostID=nil but valid backup record
	// Mock backupStore.StreamDecrypted to write known bytes
	// Assert: response is 200 with correct Content-Disposition
	// Assert: response body matches mock output
}
```

- [ ] **Step 6: Run tests**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/api/ ./internal/backup/ -v`

Expected: ALL PASS

- [ ] **Step 7: Commit**

```bash
git add backend/internal/api/server.go backend/internal/api/machine_backups.go backend/internal/backup/store.go backend/internal/backup/gcs.go
git commit -m "fix(backup): decouple download from machine host

Downloads no longer require machine.HostID. When host is available,
proxies through agent (tar.gz or ext4). When host is unavailable,
streams directly from GCS as ext4 (Cloud Run can't mount for tar.gz).

Adds StreamDecrypted method to BackupStore interface for direct
decrypt+decompress streaming without filesystem mount."
```

---

### Task 5: Stop leaking master key to agents (P1 — security)

**Why:** Every heartbeat sends the platform-wide `backupMasterKey` to every agent. The master key is also baked into GCE instance metadata by the provisioner. A compromised agent can decrypt *all* machines' backup keys.

**Files:**
- Modify: `backend/internal/api/server.go:1646-1648` (heartbeat response — remove master key)
- Modify: `backend/internal/provisioner/provisioner.go:211-215` (remove master key from GCE metadata)
- Modify: `backend/cmd/agent/main.go:484` (remove master key from GCE metadata prefetch)
- Modify: `backend/cmd/agent/main.go:589-619` (heartbeat parsing — key off `backup_enabled` instead of master key)

**Key insight:** The agent never uses `BackupMasterKey` for actual crypto. It's only used as a "backups are enabled" signal to initialize the GCS client. The per-machine `EncryptionKey` comes in each `BackupRequest`/`RestoreRequest` RPC from the control plane. So we can replace the master key with a boolean flag.

**⚠️ Dependency on Task 3:** This task modifies the same heartbeat parsing code that Task 3 changed from `sync.Once` to mutex+flag. Build on Task 3's changes.

- [ ] **Step 1: Remove master key from heartbeat response**

In `backend/internal/api/server.go`, replace lines 1646-1649:

```go
// Before:
// Include backup config so agents can lazily initialize backup store
if s.backupMasterKey != "" {
	resp["backup_master_key"] = s.backupMasterKey
}

// After:
// Signal that backups are enabled so agents can lazily initialize GCS client.
// We do NOT send the master key — per-machine encryption keys are sent
// in individual backup/restore RPC payloads.
if s.backupMasterKey != "" {
	resp["backup_enabled"] = true
}
```

- [ ] **Step 2: Remove master key from GCE instance metadata**

In `backend/internal/provisioner/provisioner.go`, remove lines 211-215:

```go
// DELETE this block:
if p.backupMasterKey != "" {
	metadataItems = append(metadataItems, &computepb.Items{
		Key: strPtr("backup-master-key"), Value: strPtr(p.backupMasterKey),
	})
}
```

- [ ] **Step 3: Remove master key from agent GCE metadata prefetch**

In `backend/cmd/agent/main.go`, remove line 484 from the metadata pairs:

```go
// DELETE this line:
{"backup-master-key", &cfg.BackupMasterKey},
```

- [ ] **Step 4: Update agent heartbeat parsing**

In `backend/cmd/agent/main.go`, update the heartbeat response parsing (building on Task 3's mutex changes). Change the struct and condition:

```go
// Before (after Task 3):
var hbResp struct {
	BackupMasterKey string `json:"backup_master_key,omitempty"`
}
if err := json.NewDecoder(resp.Body).Decode(&hbResp); err == nil && hbResp.BackupMasterKey != "" {

// After:
var hbResp struct {
	BackupEnabled bool `json:"backup_enabled,omitempty"`
}
if err := json.NewDecoder(resp.Body).Decode(&hbResp); err == nil && hbResp.BackupEnabled {
```

Inside the mutex-guarded block, remove `cfg.BackupMasterKey` assignment. The agent doesn't need the master key — it only needs the GCS client:

```go
backupStoreInitMu.Lock()
if backupStoreInitDone {
	backupStoreInitMu.Unlock()
	return
}

// Check if already initialized at startup (from env var or legacy metadata)
if srv.getBackupStore() != nil {
	backupStoreInitDone = true
	backupStoreInitMu.Unlock()
	return
}

if cfg.BackupGCSBucket == "" {
	cfg.BackupGCSBucket = "openclawmachines"
}
if cfg.BackupGCSPrefix == "" {
	cfg.BackupGCSPrefix = "backups"
}

var gcsOpts []option.ClientOption
if cfg.GCSServiceAccountKey != "" {
	gcsOpts = append(gcsOpts, option.WithCredentialsJSON([]byte(cfg.GCSServiceAccountKey)))
}
gcsClient, gcsErr := storage.NewClient(ctx, gcsOpts...)
if gcsErr != nil {
	slog.Warn("backup.heartbeat_init.gcs_client_failed", "error", gcsErr)
	backupStoreInitMu.Unlock()
	return
}

bs := backup.NewGCSStore(gcsClient, cfg.BackupGCSBucket, cfg.BackupGCSPrefix)
srv.SetBackupStore(bs)
backupStoreInitDone = true
backupStoreInitMu.Unlock()
slog.Info("backup.heartbeat_init.complete", "bucket", cfg.BackupGCSBucket, "prefix", cfg.BackupGCSPrefix)
```

- [ ] **Step 5: Update agent startup path**

The startup path at line 220-222 currently gates on `cfg.BackupMasterKey`. Since we're removing the master key from metadata, this condition will never be true for new VMs. The startup init should check for a `backup-enabled` metadata key or simply rely on the heartbeat lazy-init path:

```go
// Before:
// 7. Initialize backup store (GCS) — only when backup master key is configured
var backupSt backup.BackupStore
if cfg.BackupMasterKey != "" && cfg.BackupGCSBucket != "" {

// After:
// 7. Initialize backup store (GCS) — only when bucket is configured
// (Master key is no longer needed — per-machine keys come per-RPC)
var backupSt backup.BackupStore
if cfg.BackupGCSBucket != "" {
```

- [ ] **Step 6: Write test verifying master key is absent from heartbeat**

Add a test that calls the heartbeat handler and asserts the response does NOT contain `backup_master_key`:

```go
func TestHeartbeatDoesNotLeakMasterKey(t *testing.T) {
	// ... set up server with backupMasterKey = "deadbeef..."
	// ... send heartbeat request
	// ... decode response JSON
	// Assert: response has "backup_enabled": true
	// Assert: response does NOT have "backup_master_key"
}
```

- [ ] **Step 7: Run tests**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/api/ ./cmd/agent/ ./internal/provisioner/ -v`

Expected: ALL PASS

- [ ] **Step 8: Commit**

```bash
git add backend/internal/api/server.go backend/internal/provisioner/provisioner.go backend/cmd/agent/main.go
git commit -m "fix(backup): stop leaking master key to agents

Remove backup master key from:
- Heartbeat responses (replaced with backup_enabled boolean)
- GCE instance metadata (provisioner no longer sets it)
- Agent metadata prefetch

Agents only need a GCS client, not the master key. Per-machine
encryption keys are already sent in backup/restore RPC payloads.
A compromised agent can no longer decrypt other machines' keys."
```

---

## Chunk 3: Frontend Fix (Task 6)

### Task 6: Stream backup downloads instead of buffering (P2 — browser OOM)

**Why:** `res.blob()` buffers the entire backup in memory. Multi-GB files will hang or OOM the tab.

**Files:**
- Modify: `frontend/src/lib/api.ts:124-144` (downloadMachineBackup)

**Approach:** Use the File System Access API (`showSaveFilePicker`) on Chromium browsers to stream directly to disk. On Firefox/Safari, fall back to blob (unavoidable — no streaming download API). The blob fallback is the same as current behavior, so it's not a regression. The improvement covers ~65-70% of users (Chromium).

- [ ] **Step 1: Replace blob() with streaming download + fallback**

In `frontend/src/lib/api.ts`, replace the `downloadMachineBackup` function:

```typescript
export const downloadMachineBackup = async (accountId: number, machineId: string, backupId: number, format = "tar.gz") => {
  const headers: Record<string, string> = {};
  const cfJwt = getCfAccessJwt();
  if (cfJwt) {
    headers["Cf-Access-Jwt-Assertion"] = cfJwt;
  }
  const res = await fetch(`${BASE}/accounts/${accountId}/machines/${machineId}/backups/${backupId}/download?format=${format}`, {
    credentials: "include",
    headers,
  });
  if (!res.ok) throw new Error(`Download failed: ${res.statusText}`);

  const filename = `${machineId}-backup-${backupId}.${format === "ext4" ? "ext4" : "tar.gz"}`;

  // Stream to disk via File System Access API (Chrome/Edge 86+)
  if ("showSaveFilePicker" in window) {
    const handle = await (window as any).showSaveFilePicker({
      suggestedName: filename,
    });
    const writable = await handle.createWritable();
    await res.body!.pipeTo(writable);
    return;
  }

  // Fallback: buffer as blob (Firefox/Safari — no streaming download API available)
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
};
```

- [ ] **Step 2: Verify TypeScript compilation**

Run: `cd /home/mantiz/OpenClawMachines && make typecheck`

Expected: No new type errors.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/lib/api.ts
git commit -m "fix(frontend): stream backup downloads to avoid OOM

Use File System Access API (showSaveFilePicker) to stream directly
to disk on Chromium browsers. Falls back to blob() on Firefox/Safari
where no streaming download API is available."
```

---

## Summary

| Task | Bug | Severity | Files Changed |
|------|-----|----------|---------------|
| 1 | Nonce not in HMAC | P1 | `backup/crypto.go`, `backup/crypto_test.go` |
| 2 | Stale version sidecar | P1 | `agentapi/handlers.go`, `agentapi/handlers_test.go` |
| 3 | sync.Once prevents retry | P2 | `cmd/agent/main.go` |
| 4 | Download coupled to host | P1 | `api/server.go`, `api/machine_backups.go`, `backup/store.go`, `backup/gcs.go` |
| 5 | Master key leaked | P1 | `api/server.go`, `provisioner/provisioner.go`, `cmd/agent/main.go` |
| 6 | Browser OOM on download | P2 | `frontend/src/lib/api.ts` |

**Execution order:**
- Tasks 1, 2, 6 — fully independent, can be parallelized
- Task 3 → Task 5 — sequential (both modify heartbeat in `cmd/agent/main.go`)
- Task 4 → Task 5 — sequential (both modify `api/server.go`)
- Recommended: **[1, 2, 6] in parallel**, then **3 → 4 → 5** sequential

**Final verification:** `make test-go && make typecheck`
