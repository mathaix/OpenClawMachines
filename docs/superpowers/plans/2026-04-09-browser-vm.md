# Browser VM Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Decouple browser VMs from the main machine lifecycle into independent, account-scoped resources with explicit provisioning and pairing.

**Architecture:** New `browser_vms` DB table + separate placement table. New CRUD API endpoints. Orchestrator gets a standalone `browserVMs` map. Config assembly derives browser config from pairing (`machines.browser_vm_id`) instead of the old capability toggle. Agent gets dedicated browser VM create/destroy/pair endpoints.

**Tech Stack:** Go (backend), PostgreSQL (pgx/v5), React + TypeScript (frontend), Firecracker (VM), CDP (Chrome DevTools Protocol)

**Spec:** `docs/superpowers/specs/2026-04-09-browser-vm-design.md`

---

## Task 1: Database Migration

**Files:**
- Create: `backend/migrations/072_browser_vms.sql`

- [x] **Step 1: Write the migration**

```sql
-- browser_vms table
CREATE TABLE browser_vms (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      INT NOT NULL REFERENCES accounts(id),
    slug            TEXT NOT NULL,
    name            TEXT NOT NULL DEFAULT '',
    host_id         INT REFERENCES hosts(id),
    vm_ip           TEXT,
    status          TEXT NOT NULL DEFAULT 'stopped',
    vcpus           INT NOT NULL DEFAULT 1,
    memory_mb       INT NOT NULL DEFAULT 1024,
    cdp_port        INT NOT NULL DEFAULT 9222,
    rootfs_version  TEXT,
    error_message   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT browser_vms_status_check CHECK (status IN ('stopped', 'provisioning', 'running', 'error')),
    UNIQUE(account_id, slug)
);

CREATE INDEX idx_browser_vms_account ON browser_vms(account_id);
CREATE INDEX idx_browser_vms_host ON browser_vms(host_id) WHERE host_id IS NOT NULL;

-- browser_vm_placements table
CREATE TABLE browser_vm_placements (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    browser_vm_id   UUID NOT NULL REFERENCES browser_vms(id) ON DELETE CASCADE,
    host_id         INT NOT NULL REFERENCES hosts(id),
    vm_ip           TEXT,
    state           TEXT NOT NULL DEFAULT 'reserved',
    reserved_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at    TIMESTAMPTZ,
    released_at     TIMESTAMPTZ,
    CONSTRAINT browser_vm_placements_state_check CHECK (state IN ('reserved', 'active', 'released'))
);

CREATE UNIQUE INDEX idx_bvp_active_browser_vm
ON browser_vm_placements(browser_vm_id) WHERE released_at IS NULL;

CREATE UNIQUE INDEX idx_bvp_active_host_ip
ON browser_vm_placements(host_id, vm_ip) WHERE released_at IS NULL;

CREATE INDEX idx_bvp_host
ON browser_vm_placements(host_id) WHERE released_at IS NULL;

-- pairing column on machines
ALTER TABLE machines ADD COLUMN browser_vm_id UUID REFERENCES browser_vms(id);
CREATE UNIQUE INDEX idx_machines_browser_vm ON machines(browser_vm_id) WHERE browser_vm_id IS NOT NULL;
```

- [x] **Step 2: Run the migration**

Run: `make test-go` (migrations are applied in test setup)
Expected: Tests pass, no migration errors.

- [x] **Step 3: Commit**

```bash
git add backend/migrations/072_browser_vms.sql
git commit -m "feat(db): add browser_vms, browser_vm_placements, and machines.browser_vm_id"
```

---

## Task 2: Store — BrowserVM Struct & CRUD

**Files:**
- Modify: `backend/internal/store/store.go` (add BrowserVM struct + interface)
- Modify: `backend/internal/store/postgres.go` (add implementations)

- [x] **Step 1: Add BrowserVM struct to store.go**

Add after the Machine struct (around line 103):

```go
// BrowserVM represents a standalone browser VM instance.
type BrowserVM struct {
	ID            string     `json:"id"`
	AccountID     int        `json:"account_id"`
	Slug          string     `json:"slug"`
	Name          string     `json:"name"`
	HostID        *int       `json:"host_id,omitempty"`
	VMIP          *string    `json:"vm_ip,omitempty"`
	Status        string     `json:"status"`
	VCPUs         int        `json:"vcpus"`
	MemoryMB      int        `json:"memory_mb"`
	CDPPort       int        `json:"cdp_port"`
	RootfsVersion *string    `json:"rootfs_version,omitempty"`
	ErrorMessage  *string    `json:"error_message,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}
```

- [x] **Step 2: Add BrowserVMRepo interface to store.go**

Add after the ConfigRepo interface (around line 723):

```go
// BrowserVMRepo handles browser VM CRUD and pairing.
type BrowserVMRepo interface {
	CreateBrowserVM(ctx context.Context, bvm *BrowserVM) error
	GetBrowserVM(ctx context.Context, id string) (*BrowserVM, error)
	ListBrowserVMsByAccount(ctx context.Context, accountID int) ([]BrowserVM, error)
	UpdateBrowserVMStatus(ctx context.Context, id, status string, errMsg *string) error
	AssignBrowserVMToHost(ctx context.Context, id string, hostID int, vmIP string) error
	UnassignBrowserVMFromHost(ctx context.Context, id string) error
	DeleteBrowserVM(ctx context.Context, id string) error
	PairBrowserVM(ctx context.Context, machineID, browserVMID string) error
	UnpairBrowserVM(ctx context.Context, machineID string) error
	GetMachineByBrowserVMID(ctx context.Context, browserVMID string) (*Machine, error)
}
```

- [x] **Step 3: Add BrowserVMRepo to the Store aggregate interface**

Find the `Store` interface in store.go and add `BrowserVMRepo` to the list of embedded interfaces.

- [x] **Step 4: Implement BrowserVM CRUD in postgres.go**

Add at the end of postgres.go (before the closing of the file):

```go
const browserVMColumns = `id, account_id, slug, name, host_id, vm_ip, status,
	vcpus, memory_mb, cdp_port, rootfs_version, error_message, created_at, updated_at`

func scanBrowserVM(scan func(dest ...any) error) (*BrowserVM, error) {
	bvm := &BrowserVM{}
	err := scan(&bvm.ID, &bvm.AccountID, &bvm.Slug, &bvm.Name, &bvm.HostID, &bvm.VMIP,
		&bvm.Status, &bvm.VCPUs, &bvm.MemoryMB, &bvm.CDPPort, &bvm.RootfsVersion,
		&bvm.ErrorMessage, &bvm.CreatedAt, &bvm.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return bvm, nil
}

func (s *PostgresStore) CreateBrowserVM(ctx context.Context, bvm *BrowserVM) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO browser_vms (account_id, slug, name, vcpus, memory_mb, cdp_port)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, created_at, updated_at`,
		bvm.AccountID, bvm.Slug, bvm.Name, bvm.VCPUs, bvm.MemoryMB, bvm.CDPPort,
	).Scan(&bvm.ID, &bvm.CreatedAt, &bvm.UpdatedAt)
}

func (s *PostgresStore) GetBrowserVM(ctx context.Context, id string) (*BrowserVM, error) {
	return scanBrowserVM(s.pool.QueryRow(ctx,
		`SELECT `+browserVMColumns+` FROM browser_vms WHERE id = $1`, id).Scan)
}

func (s *PostgresStore) ListBrowserVMsByAccount(ctx context.Context, accountID int) ([]BrowserVM, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+browserVMColumns+` FROM browser_vms WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bvms []BrowserVM
	for rows.Next() {
		bvm, err := scanBrowserVM(rows.Scan)
		if err != nil {
			return nil, err
		}
		bvms = append(bvms, *bvm)
	}
	return bvms, nil
}

func (s *PostgresStore) UpdateBrowserVMStatus(ctx context.Context, id, status string, errMsg *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE browser_vms SET status = $2, error_message = $3, updated_at = now() WHERE id = $1`,
		id, status, errMsg)
	return err
}

func (s *PostgresStore) AssignBrowserVMToHost(ctx context.Context, id string, hostID int, vmIP string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE browser_vms SET host_id = $2, vm_ip = $3, updated_at = now() WHERE id = $1`,
		id, hostID, vmIP)
	return err
}

func (s *PostgresStore) UnassignBrowserVMFromHost(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE browser_vms SET host_id = NULL, vm_ip = NULL, updated_at = now() WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) DeleteBrowserVM(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM browser_vms WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) PairBrowserVM(ctx context.Context, machineID, browserVMID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET browser_vm_id = $2 WHERE id = $1`, machineID, browserVMID)
	return err
}

func (s *PostgresStore) UnpairBrowserVM(ctx context.Context, machineID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE machines SET browser_vm_id = NULL WHERE id = $1`, machineID)
	return err
}

func (s *PostgresStore) GetMachineByBrowserVMID(ctx context.Context, browserVMID string) (*Machine, error) {
	return scanMachine(s.pool.QueryRow(ctx,
		`SELECT `+machineColumns+` FROM machines WHERE browser_vm_id = $1`, browserVMID).Scan)
}
```

- [x] **Step 5: Add browser_vm_id to Machine struct and scan**

In store.go, add to Machine struct (after `BackupKey`):
```go
BrowserVMID *string `json:"browser_vm_id,omitempty"`
```

In postgres.go, update `machineColumns` (line 450) — append `, browser_vm_id` to the end.

Update `scanMachine` (line 459) — add `&m.BrowserVMID` as the last scan target.

- [x] **Step 6: Run tests**

Run: `make test-go`
Expected: All existing tests pass. New columns scanned correctly.

- [x] **Step 7: Commit**

```bash
git add backend/internal/store/store.go backend/internal/store/postgres.go
git commit -m "feat(store): add BrowserVM struct, CRUD methods, and pairing"
```

---

## Task 3: Browser VM Placement

**Files:**
- Modify: `backend/internal/fleet/placement.go`
- Modify: `backend/internal/store/store.go` (add placement methods)
- Modify: `backend/internal/store/postgres.go` (add placement implementations)

- [x] **Step 1: Add browser VM placement store methods to store.go**

Add to the `BrowserVMRepo` interface:

```go
	PlaceBrowserVMOnHost(ctx context.Context, hostID int, browserVMID string, vcpus, memoryMB int) (vmIP string, err error)
	ActivateBrowserVMPlacement(ctx context.Context, browserVMID string) error
	ReleaseBrowserVMPlacement(ctx context.Context, browserVMID string) error
```

- [x] **Step 2: Implement browser VM placement in postgres.go**

```go
func (s *PostgresStore) PlaceBrowserVMOnHost(ctx context.Context, hostID int, browserVMID string, vcpus, memoryMB int) (string, error) {
	// Find a free IP on this host's bridge subnet.
	// Query both machine_placements and browser_vm_placements to avoid collisions.
	var vmIP string
	err := s.pool.QueryRow(ctx, `
		WITH used_ips AS (
			SELECT vm_ip FROM machine_placements WHERE host_id = $1 AND released_at IS NULL
			UNION ALL
			SELECT vm_ip FROM browser_vm_placements WHERE host_id = $1 AND released_at IS NULL
		),
		candidates AS (
			SELECT '192.168.100.' || n AS ip
			FROM generate_series(2, 254) n
			WHERE '192.168.100.' || n NOT IN (SELECT vm_ip FROM used_ips WHERE vm_ip IS NOT NULL)
			LIMIT 1
		)
		INSERT INTO browser_vm_placements (browser_vm_id, host_id, vm_ip, state)
		SELECT $2, $1, ip, 'reserved' FROM candidates
		RETURNING vm_ip`, hostID, browserVMID).Scan(&vmIP)
	if err != nil {
		return "", fmt.Errorf("no free IP on host %d: %w", hostID, err)
	}

	// Debit host capacity
	_, err = s.pool.Exec(ctx,
		`UPDATE hosts SET used_vcpus = used_vcpus + $2, used_memory_mb = used_memory_mb + $3 WHERE id = $1`,
		hostID, vcpus, memoryMB)
	if err != nil {
		return "", fmt.Errorf("debit host capacity: %w", err)
	}

	return vmIP, nil
}

func (s *PostgresStore) ActivateBrowserVMPlacement(ctx context.Context, browserVMID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE browser_vm_placements SET state = 'active', activated_at = now()
		 WHERE browser_vm_id = $1 AND released_at IS NULL`, browserVMID)
	return err
}

func (s *PostgresStore) ReleaseBrowserVMPlacement(ctx context.Context, browserVMID string) error {
	var hostID, vcpus, memoryMB int
	err := s.pool.QueryRow(ctx, `
		UPDATE browser_vm_placements SET state = 'released', released_at = now()
		WHERE browser_vm_id = $1 AND released_at IS NULL
		RETURNING host_id,
			(SELECT vcpus FROM browser_vms WHERE id = $1),
			(SELECT memory_mb FROM browser_vms WHERE id = $1)`,
		browserVMID).Scan(&hostID, &vcpus, &memoryMB)
	if err != nil {
		return err
	}
	// Credit host capacity back
	_, err = s.pool.Exec(ctx,
		`UPDATE hosts SET used_vcpus = used_vcpus - $2, used_memory_mb = used_memory_mb - $3 WHERE id = $1`,
		hostID, vcpus, memoryMB)
	return err
}
```

- [x] **Step 3: Add ReserveBrowserVM to PlacementService**

In `backend/internal/fleet/placement.go`, add a method that wraps the store call with host selection logic:

```go
// ReserveBrowserVM picks a host and reserves capacity for a browser VM.
func (ps *PlacementService) ReserveBrowserVM(ctx context.Context, browserVMID string, req PlacementRequest) (*store.Host, string, error) {
	hosts, err := ps.store.ListReadyHosts(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("list ready hosts: %w", err)
	}

	eligible := filterEligible(hosts, req)
	if len(eligible) == 0 {
		return nil, "", fmt.Errorf("no eligible hosts for browser VM")
	}

	ranked := ps.rank(eligible)
	for _, host := range ranked {
		vmIP, err := ps.store.PlaceBrowserVMOnHost(ctx, host.ID, browserVMID, req.VCPUs, req.MemoryMB)
		if err != nil {
			slog.Warn("browser_vm.placement.attempt_failed", "host_id", host.ID, "error", err)
			continue
		}
		return &host, vmIP, nil
	}
	return nil, "", fmt.Errorf("all eligible hosts exhausted for browser VM")
}
```

- [x] **Step 4: Run tests**

Run: `make test-go`
Expected: PASS

- [x] **Step 5: Commit**

```bash
git add backend/internal/store/store.go backend/internal/store/postgres.go backend/internal/fleet/placement.go
git commit -m "feat(fleet): add browser VM placement with cross-table IP allocation"
```

---

## Task 4: Control Plane API — Browser VM CRUD Handlers

**Files:**
- Create: `backend/internal/api/browser_vms.go`
- Modify: `backend/internal/api/server.go` (register routes)

- [x] **Step 1: Create browser VM handlers**

Create `backend/internal/api/browser_vms.go`:

```go
package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/auth"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/pkg/slug"
)

func (s *Server) handleListBrowserVMs(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	bvms, err := s.store.ListBrowserVMsByAccount(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, bvms)
}

func (s *Server) handleCreateBrowserVM(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	bvm := &store.BrowserVM{
		AccountID: accountID,
		Slug:      slug.Generate(7),
		Name:      req.Name,
		Status:    "stopped",
		VCPUs:     1,
		MemoryMB:  1024,
		CDPPort:   9222,
	}

	if err := s.store.CreateBrowserVM(r.Context(), bvm); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("browser_vm.created", "id", bvm.ID, "slug", bvm.Slug, "account_id", accountID)
	writeJSON(w, http.StatusCreated, bvm)
}

func (s *Server) handleGetBrowserVM(w http.ResponseWriter, r *http.Request) {
	bvmID := chi.URLParam(r, "browserVmId")
	accountID := accountIDFromContext(r.Context())

	bvm, err := s.store.GetBrowserVM(r.Context(), bvmID)
	if err != nil {
		writeError(w, http.StatusNotFound, "browser VM not found")
		return
	}
	if bvm.AccountID != accountID {
		writeError(w, http.StatusForbidden, "browser VM does not belong to this account")
		return
	}

	writeJSON(w, http.StatusOK, bvm)
}

func (s *Server) handleDeleteBrowserVM(w http.ResponseWriter, r *http.Request) {
	bvmID := chi.URLParam(r, "browserVmId")
	accountID := accountIDFromContext(r.Context())

	bvm, err := s.store.GetBrowserVM(r.Context(), bvmID)
	if err != nil {
		writeError(w, http.StatusNotFound, "browser VM not found")
		return
	}
	if bvm.AccountID != accountID {
		writeError(w, http.StatusForbidden, "browser VM does not belong to this account")
		return
	}

	// Auto-unpair if paired
	if m, err := s.store.GetMachineByBrowserVMID(r.Context(), bvmID); err == nil {
		if err := s.store.UnpairBrowserVM(r.Context(), m.ID); err != nil {
			slog.Warn("browser_vm.delete.unpair_failed", "browser_vm_id", bvmID, "machine_id", m.ID, "error", err)
		}
	}

	// Stop if running
	if bvm.Status == "running" && bvm.HostID != nil {
		if err := s.stopBrowserVM(r.Context(), bvm); err != nil {
			slog.Warn("browser_vm.delete.stop_failed", "browser_vm_id", bvmID, "error", err)
		}
	}

	if err := s.store.DeleteBrowserVM(r.Context(), bvmID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("browser_vm.deleted", "id", bvmID, "account_id", accountID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

- [x] **Step 2: Add start/stop handlers**

Append to `browser_vms.go`:

```go
func (s *Server) handleStartBrowserVM(w http.ResponseWriter, r *http.Request) {
	bvmID := chi.URLParam(r, "browserVmId")
	accountID := accountIDFromContext(r.Context())

	bvm, err := s.store.GetBrowserVM(r.Context(), bvmID)
	if err != nil {
		writeError(w, http.StatusNotFound, "browser VM not found")
		return
	}
	if bvm.AccountID != accountID {
		writeError(w, http.StatusForbidden, "browser VM does not belong to this account")
		return
	}
	if bvm.Status != "stopped" && bvm.Status != "error" {
		writeError(w, http.StatusConflict, fmt.Sprintf("browser VM is %s, must be stopped", bvm.Status))
		return
	}

	var req struct {
		HostID *int    `json:"host_id"`
		Region *string `json:"region"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.store.UpdateBrowserVMStatus(r.Context(), bvmID, "provisioning", nil); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Reserve placement
	placementReq := s.buildBrowserVMPlacementReq(bvm, req.HostID, req.Region)
	host, vmIP, err := s.placement.ReserveBrowserVM(r.Context(), bvmID, placementReq)
	if err != nil {
		errMsg := err.Error()
		s.store.UpdateBrowserVMStatus(r.Context(), bvmID, "error", &errMsg)
		writeError(w, http.StatusServiceUnavailable, "no host available: "+err.Error())
		return
	}

	if err := s.store.AssignBrowserVMToHost(r.Context(), bvmID, host.ID, vmIP); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Call agent to create browser VM
	if err := s.agentClient.CreateBrowserVM(r.Context(), host, bvmID, vmIP); err != nil {
		errMsg := err.Error()
		s.store.UpdateBrowserVMStatus(r.Context(), bvmID, "error", &errMsg)
		s.store.ReleaseBrowserVMPlacement(r.Context(), bvmID)
		writeError(w, http.StatusInternalServerError, "agent failed: "+err.Error())
		return
	}

	if err := s.store.ActivateBrowserVMPlacement(r.Context(), bvmID); err != nil {
		slog.Warn("browser_vm.start.activate_failed", "id", bvmID, "error", err)
	}
	s.store.UpdateBrowserVMStatus(r.Context(), bvmID, "running", nil)

	bvm, _ = s.store.GetBrowserVM(r.Context(), bvmID)
	slog.Info("browser_vm.started", "id", bvmID, "host_id", host.ID, "vm_ip", vmIP)
	writeJSON(w, http.StatusOK, bvm)
}

func (s *Server) handleStopBrowserVM(w http.ResponseWriter, r *http.Request) {
	bvmID := chi.URLParam(r, "browserVmId")
	accountID := accountIDFromContext(r.Context())

	bvm, err := s.store.GetBrowserVM(r.Context(), bvmID)
	if err != nil {
		writeError(w, http.StatusNotFound, "browser VM not found")
		return
	}
	if bvm.AccountID != accountID {
		writeError(w, http.StatusForbidden, "browser VM does not belong to this account")
		return
	}
	if bvm.Status != "running" && bvm.Status != "provisioning" {
		writeError(w, http.StatusConflict, fmt.Sprintf("browser VM is %s, must be running", bvm.Status))
		return
	}

	// Auto-unpair if paired
	if m, err := s.store.GetMachineByBrowserVMID(r.Context(), bvmID); err == nil {
		s.store.UnpairBrowserVM(r.Context(), m.ID)
		slog.Info("browser_vm.stop.auto_unpaired", "browser_vm_id", bvmID, "machine_id", m.ID)
		// Push config to drop browser block (best-effort)
		go s.pushMachineConfigAsync(m.ID)
	}

	if err := s.stopBrowserVM(r.Context(), bvm); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	bvm, _ = s.store.GetBrowserVM(r.Context(), bvmID)
	writeJSON(w, http.StatusOK, bvm)
}

func (s *Server) stopBrowserVM(ctx context.Context, bvm *store.BrowserVM) error {
	if bvm.HostID != nil {
		host, err := s.store.GetHost(ctx, *bvm.HostID)
		if err == nil {
			if err := s.agentClient.DestroyBrowserVM(ctx, host, bvm.ID); err != nil {
				slog.Warn("browser_vm.stop.agent_failed", "id", bvm.ID, "error", err)
			}
		}
		s.store.ReleaseBrowserVMPlacement(ctx, bvm.ID)
		s.store.UnassignBrowserVMFromHost(ctx, bvm.ID)
	}
	return s.store.UpdateBrowserVMStatus(ctx, bvm.ID, "stopped", nil)
}

func (s *Server) buildBrowserVMPlacementReq(bvm *store.BrowserVM, hostID *int, region *string) fleet.PlacementRequest {
	req := fleet.PlacementRequest{
		VCPUs:    bvm.VCPUs,
		MemoryMB: bvm.MemoryMB,
	}
	if hostID != nil {
		req.TargetHostID = hostID
	}
	if region != nil {
		req.Region = *region
	}
	return req
}
```

- [x] **Step 3: Add pairing handlers**

Append to `browser_vms.go`:

```go
func (s *Server) handlePairBrowser(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	accountID := accountIDFromContext(r.Context())

	var req struct {
		BrowserVMID string `json:"browser_vm_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.BrowserVMID == "" {
		writeError(w, http.StatusBadRequest, "browser_vm_id is required")
		return
	}

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}
	if machine.Status != "running" {
		writeError(w, http.StatusConflict, "machine must be running to pair")
		return
	}

	bvm, err := s.store.GetBrowserVM(r.Context(), req.BrowserVMID)
	if err != nil {
		writeError(w, http.StatusNotFound, "browser VM not found")
		return
	}
	if bvm.AccountID != accountID {
		writeError(w, http.StatusForbidden, "browser VM does not belong to this account")
		return
	}
	if bvm.Status != "running" {
		writeError(w, http.StatusConflict, "browser VM must be running to pair")
		return
	}

	// Same-host check
	if machine.HostID == nil || bvm.HostID == nil || *machine.HostID != *bvm.HostID {
		writeError(w, http.StatusConflict, "machine and browser VM must be on the same host")
		return
	}

	// Already paired check
	if existing, err := s.store.GetMachineByBrowserVMID(r.Context(), req.BrowserVMID); err == nil && existing.ID != machineID {
		writeError(w, http.StatusConflict, "browser VM is already paired to another machine")
		return
	}

	// Pair in DB
	if err := s.store.PairBrowserVM(r.Context(), machineID, req.BrowserVMID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Add firewall rules via agent
	host, _ := s.store.GetHost(r.Context(), *machine.HostID)
	if host != nil && machine.VMIP != nil && bvm.VMIP != nil {
		if err := s.agentClient.PairBrowserVM(r.Context(), host, bvm.ID, *machine.VMIP); err != nil {
			slog.Warn("browser_vm.pair.firewall_failed", "browser_vm_id", bvm.ID, "machine_id", machineID, "error", err)
			// Rollback pairing on firewall failure
			s.store.UnpairBrowserVM(r.Context(), machineID)
			writeError(w, http.StatusInternalServerError, "failed to configure network: "+err.Error())
			return
		}
	}

	// Push config with browser block
	go s.pushMachineConfigAsync(machineID)

	slog.Info("browser_vm.paired", "browser_vm_id", req.BrowserVMID, "machine_id", machineID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleUnpairBrowser(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}
	if machine.BrowserVMID == nil {
		writeError(w, http.StatusConflict, "machine has no paired browser VM")
		return
	}

	bvm, _ := s.store.GetBrowserVM(r.Context(), *machine.BrowserVMID)

	// Remove firewall rules
	if machine.HostID != nil && machine.VMIP != nil && bvm != nil && bvm.VMIP != nil {
		host, _ := s.store.GetHost(r.Context(), *machine.HostID)
		if host != nil {
			s.agentClient.UnpairBrowserVM(r.Context(), host, bvm.ID, *machine.VMIP)
		}
	}

	if err := s.store.UnpairBrowserVM(r.Context(), machineID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Push config to drop browser block
	go s.pushMachineConfigAsync(machineID)

	slog.Info("browser_vm.unpaired", "machine_id", machineID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// pushMachineConfigAsync pushes config to a running machine in the background.
func (s *Server) pushMachineConfigAsync(machineID string) {
	// Reuse existing pushMachineConfig logic from machine_config.go
	// This is a best-effort push — failures are logged, not returned.
	ctx := context.Background()
	if err := s.pushMachineConfigInternal(ctx, machineID); err != nil {
		slog.Warn("browser_vm.config_push_failed", "machine_id", machineID, "error", err)
	}
}
```

- [x] **Step 4: Register routes in server.go**

In `backend/internal/api/server.go`, find the account-scoped route block (around line 397). Add browser VM routes alongside machine routes:

```go
// Browser VMs
r.Route("/browser-vms", func(r chi.Router) {
	r.Get("/", srv.handleListBrowserVMs)
	r.Post("/", srv.handleCreateBrowserVM)
	r.Route("/{browserVmId}", func(r chi.Router) {
		r.Get("/", srv.handleGetBrowserVM)
		r.Post("/start", srv.handleStartBrowserVM)
		r.Post("/stop", srv.handleStopBrowserVM)
		r.Delete("/", srv.handleDeleteBrowserVM)
	})
})
```

Also add pairing routes to the existing machine `/{id}` route block:

```go
r.Post("/pair-browser", srv.handlePairBrowser)
r.Delete("/pair-browser", srv.handleUnpairBrowser)
```

- [x] **Step 5: Run tests**

Run: `make test-go`
Expected: PASS (compilation check — handlers compile against store interface)

- [x] **Step 6: Commit**

```bash
git add backend/internal/api/browser_vms.go backend/internal/api/server.go
git commit -m "feat(api): add browser VM CRUD, start/stop, and pair/unpair endpoints"
```

---

## Task 5: Agent Client — Browser VM Methods

**Files:**
- Modify: `backend/internal/agentclient/client.go`

- [x] **Step 1: Add browser VM methods to agent client**

Add these methods to the `Client` struct in `client.go`:

```go
func (c *Client) CreateBrowserVM(ctx context.Context, host *store.Host, browserVMID, vmIP string) error {
	body := map[string]string{
		"browser_vm_id": browserVMID,
		"vm_ip":         vmIP,
	}
	return c.postAgent(ctx, host, "/browser-vms", body, nil)
}

func (c *Client) DestroyBrowserVM(ctx context.Context, host *store.Host, browserVMID string) error {
	return c.deleteAgent(ctx, host, "/browser-vms/"+browserVMID)
}

func (c *Client) PairBrowserVM(ctx context.Context, host *store.Host, browserVMID, machineVMIP string) error {
	body := map[string]string{
		"machine_vm_ip": machineVMIP,
	}
	return c.postAgent(ctx, host, "/browser-vms/"+browserVMID+"/pair", body, nil)
}

func (c *Client) UnpairBrowserVM(ctx context.Context, host *store.Host, browserVMID, machineVMIP string) error {
	body := map[string]string{
		"machine_vm_ip": machineVMIP,
	}
	return c.postAgent(ctx, host, "/browser-vms/"+browserVMID+"/unpair", body, nil)
}
```

- [x] **Step 2: Run tests**

Run: `make test-go`
Expected: PASS

- [x] **Step 3: Commit**

```bash
git add backend/internal/agentclient/client.go
git commit -m "feat(agentclient): add browser VM create/destroy/pair/unpair methods"
```

---

## Task 6: Orchestrator — Standalone Browser VM Management

**Files:**
- Modify: `backend/internal/orchestrator/orchestrator.go` (add interface methods)
- Modify: `backend/internal/orchestrator/types.go` (add BrowserVMConfig, remove BrowserVMIP)
- Modify: `backend/internal/orchestrator/firecracker_linux.go` (extract standalone methods)

- [x] **Step 1: Add BrowserVMConfig and update types.go**

In `types.go`, add a new struct and remove `BrowserVMIP` from `VMConfig` and `VMInstance`:

```go
// BrowserVMConfig is the configuration for a standalone browser VM.
type BrowserVMConfig struct {
	BrowserVMID string
	VMIP        string
}
```

Remove `BrowserVMIP string` from `VMConfig` (line ~93).
Remove `BrowserVMIP string` and `BrowserVMStatus string` from `VMInstance` (lines ~117-118).

- [x] **Step 2: Add browser VM methods to Orchestrator interface**

In `orchestrator.go`, add to the `Orchestrator` interface:

```go
	CreateBrowserVM(ctx context.Context, cfg BrowserVMConfig) error
	DestroyBrowserVM(ctx context.Context, browserVMID string) error
	PairBrowserVM(machineVMIP, browserVMIP string) error
	UnpairBrowserVM(machineVMIP, browserVMIP string) error
	ListBrowserVMs(ctx context.Context) ([]BrowserVMInstance, error)
```

Add a `BrowserVMInstance` struct:

```go
type BrowserVMInstance struct {
	ID         string `json:"id"`
	VMIP       string `json:"vm_ip"`
	Status     string `json:"status"`
	CDPPort    int    `json:"cdp_port"`
	TapDevice  string `json:"tap_device"`
	SocketPath string `json:"socket_path"`
}
```

- [x] **Step 3: Extract standalone browser VM methods in firecracker_linux.go**

Add a `browserVMs` map to `firecrackerOrchestrator`:

```go
type firecrackerOrchestrator struct {
	// ... existing fields ...
	browserVMs map[string]*browserRunningVM
	browserMu  sync.Mutex
}

type browserRunningVM struct {
	ID         string
	VMIP       string
	TapDevice  string
	SocketPath string
	RootfsPath string
	Status     string
	CDPPort    int
	Cmd        *exec.Cmd
}
```

Initialize in the constructor:

```go
browserVMs: make(map[string]*browserRunningVM),
```

Implement `CreateBrowserVM` by extracting from the existing `createBrowserVM()` method. The key changes:
- Takes `BrowserVMConfig` instead of `VMConfig`
- Keys the map by `cfg.BrowserVMID` instead of machine ID
- TAP device named `btap<first-10-chars-of-browserVMID>`
- No dependency on main VM state

Implement `DestroyBrowserVM` by extracting from the existing `destroyBrowserVM()`:
- Looks up in `browserVMs` map by ID
- Graceful shutdown, cleanup TAP, remove rootfs

Implement `PairBrowserVM` / `UnpairBrowserVM`:
```go
func (o *firecrackerOrchestrator) PairBrowserVM(machineVMIP, browserVMIP string) error {
	return o.bridge.AllowVMPair(machineVMIP, browserVMIP)
}

func (o *firecrackerOrchestrator) UnpairBrowserVM(machineVMIP, browserVMIP string) error {
	return o.bridge.RemoveVMPair(machineVMIP, browserVMIP)
}
```

- [x] **Step 4: Remove old companion browser VM code from Create()**

In the `Create()` method, remove the block that calls `createBrowserVM()` when `cfg.BrowserVMIP` is set. Remove the `destroyBrowserVM()` call from `Destroy()`.

- [x] **Step 5: Update the stub orchestrator (if any) used in tests**

Search for any test stub/mock that implements the Orchestrator interface and add the new methods as no-ops.

- [x] **Step 6: Run tests**

Run: `make test-go`
Expected: PASS

- [x] **Step 7: Commit**

```bash
git add backend/internal/orchestrator/
git commit -m "feat(orchestrator): extract standalone browser VM create/destroy/pair/unpair"
```

---

## Task 7: Agent API — Browser VM Handlers

**Files:**
- Modify: `backend/internal/agentapi/handlers.go` (add new handlers)
- Modify: `backend/internal/agentapi/proxy.go` (remove BrowserVMIP from VMRequest/VMResponse)

- [x] **Step 1: Add browser VM handlers to handlers.go**

```go
func (s *Server) handleCreateBrowserVM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BrowserVMID string `json:"browser_vm_id"`
		VMIP        string `json:"vm_ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.BrowserVMID == "" || req.VMIP == "" {
		http.Error(w, "browser_vm_id and vm_ip are required", http.StatusBadRequest)
		return
	}

	cfg := orchestrator.BrowserVMConfig{
		BrowserVMID: req.BrowserVMID,
		VMIP:        req.VMIP,
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := s.orchestrator.CreateBrowserVM(ctx, cfg); err != nil {
			slog.Error("browser_vm.create.failed", "id", req.BrowserVMID, "error", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"browser_vm_id": req.BrowserVMID,
		"status":        "creating",
	})
}

func (s *Server) handleDestroyBrowserVM(w http.ResponseWriter, r *http.Request) {
	bvmID := chi.URLParam(r, "browserVmId")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := s.orchestrator.DestroyBrowserVM(ctx, bvmID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handlePairBrowserVM(w http.ResponseWriter, r *http.Request) {
	bvmID := chi.URLParam(r, "browserVmId")
	var req struct {
		MachineVMIP string `json:"machine_vm_ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	// Look up browser VM IP from orchestrator
	bvms, _ := s.orchestrator.ListBrowserVMs(r.Context())
	var browserVMIP string
	for _, bvm := range bvms {
		if bvm.ID == bvmID {
			browserVMIP = bvm.VMIP
			break
		}
	}
	if browserVMIP == "" {
		http.Error(w, "browser VM not found on this host", http.StatusNotFound)
		return
	}

	if err := s.orchestrator.PairBrowserVM(req.MachineVMIP, browserVMIP); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleUnpairBrowserVM(w http.ResponseWriter, r *http.Request) {
	bvmID := chi.URLParam(r, "browserVmId")
	var req struct {
		MachineVMIP string `json:"machine_vm_ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	bvms, _ := s.orchestrator.ListBrowserVMs(r.Context())
	var browserVMIP string
	for _, bvm := range bvms {
		if bvm.ID == bvmID {
			browserVMIP = bvm.VMIP
			break
		}
	}
	if browserVMIP == "" {
		http.Error(w, "browser VM not found on this host", http.StatusNotFound)
		return
	}

	if err := s.orchestrator.UnpairBrowserVM(req.MachineVMIP, browserVMIP); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

- [x] **Step 2: Register agent routes**

In the agent's route setup (in `handlers.go` or `server.go`), add:

```go
r.Route("/browser-vms", func(r chi.Router) {
	r.Post("/", s.handleCreateBrowserVM)
	r.Route("/{browserVmId}", func(r chi.Router) {
		r.Delete("/", s.handleDestroyBrowserVM)
		r.Post("/pair", s.handlePairBrowserVM)
		r.Post("/unpair", s.handleUnpairBrowserVM)
	})
})
```

- [x] **Step 3: Remove BrowserVMIP from VMRequest and VMResponse**

In `proxy.go`, remove `BrowserVMIP` from `VMRequest` and `VMResponse` structs. Remove it from `vmConfigFromRequest()` mapping. Update `validateVMRequest()` if it references it.

- [x] **Step 4: Run tests**

Run: `make test-go`
Expected: PASS

- [x] **Step 5: Commit**

```bash
git add backend/internal/agentapi/
git commit -m "feat(agent): add browser VM create/destroy/pair/unpair handlers, remove BrowserVMIP"
```

---

## Task 8: Config Assembly — Browser Block from Pairing

**Files:**
- Modify: `backend/internal/configassembly/assembler.go`
- Modify: `backend/internal/api/machine_config.go` (remove resolveBrowserVMIP)
- Modify: `backend/internal/machines/runtime.go` (remove browser capability detection)

- [x] **Step 1: Update config assembly to use browser_vm_id**

In `assembler.go`, find the browser CDP injection block (around line 435). Replace it:

**Remove:**
```go
// 3b. Inject browser CDP URL when a companion browser VM is available.
browserCDPHost := params.BridgeIP
if browserCDPHost == "" {
    browserCDPHost = params.BrowserVMIP
}
if browserCDPHost != "" {
    browser := getOrCreateMap(result, "browser")
    if enabled, _ := browser["enabled"].(bool); enabled {
        browser["cdpUrl"] = fmt.Sprintf("http://%s:9222", browserCDPHost)
        browser["attachOnly"] = true
        browser["noSandbox"] = true
        browser["headless"] = true
        result["browser"] = browser
    }
}
```

**Replace with:**
```go
// 3b. Inject browser config when a browser VM is paired.
if params.BrowserVMIP != "" {
    result["browser"] = map[string]interface{}{
        "enabled":   true,
        "cdpUrl":    fmt.Sprintf("http://%s:9222", params.BrowserVMIP),
        "attachOnly": true,
        "noSandbox":  true,
        "headless":   true,
    }
}
```

Note: `params.BrowserVMIP` is now populated from the paired browser VM's IP (resolved by the caller), not from `BrowserVMIP()` derivation.

- [x] **Step 2: Update machine_config.go — remove resolveBrowserVMIP, use pairing**

Remove the `resolveBrowserVMIP()` function entirely. Where it was called (in config push/preview), replace with a direct lookup:

```go
// Resolve browser VM IP from pairing
var browserVMIP string
if machine.BrowserVMID != nil {
    if bvm, err := s.store.GetBrowserVM(ctx, *machine.BrowserVMID); err == nil && bvm.Status == "running" && bvm.VMIP != nil {
        browserVMIP = *bvm.VMIP
    }
}
```

Pass `browserVMIP` to the assembler params instead of calling `resolveBrowserVMIP`.

- [x] **Step 3: Remove browser capability detection from runtime.go**

In `runtime.go` around line 396, remove the entire block:
```go
// Detect browser capability to determine if a companion browser VM is needed
var browserVMIP string
if caps, capErr := rs.store.ListMachineCapabilities(ctx, machine.ID); capErr == nil {
    ...
}
```

And remove `BrowserVMIP: browserVMIP` from the `VMRequest` construction below.

Also remove similar blocks in the upgrade path (~line 946) and any other places that set `BrowserVMIP` on the request.

- [x] **Step 4: Update assembler tests**

In `configassembly/assembler_test.go`, update tests that set `BrowserVMIP` on params to reflect the new behavior (browser block emitted when `BrowserVMIP` is set, regardless of capability toggle).

- [x] **Step 5: Run tests**

Run: `make test-go`
Expected: PASS

- [x] **Step 6: Commit**

```bash
git add backend/internal/configassembly/ backend/internal/api/machine_config.go backend/internal/machines/runtime.go
git commit -m "feat(config): derive browser block from pairing instead of capability toggle"
```

---

## Task 9: Remove Old Browser Companion VM Code

**Files:**
- Modify: `backend/internal/orchestrator/firecracker_linux.go` (remove old createBrowserVM calls)
- Modify: `backend/internal/metadata/metadata.go` (remove browser targeting fields)
- Modify: `backend/internal/integration/browser_vm_test.go` (remove old tests)

- [x] **Step 1: Clean up remaining BrowserVMIP references**

Search for remaining `BrowserVMIP` references across the codebase and remove them:

```bash
grep -rn "BrowserVMIP\|BrowserVMStatus\|browser_vm_ip\|browser_vm_status" backend/
```

For each file found:
- `metadata/metadata.go`: Remove browser VM targeting fields
- `agentclient/client.go`: Remove any old browser VM IP passing
- `integration/browser_vm_test.go`: Remove old companion browser VM tests (they'll be replaced in Task 10)
- `integration/helpers_test.go`: Remove browser VM helper references

- [x] **Step 2: Run tests**

Run: `make test-go`
Expected: PASS (some old tests removed, new ones coming in Task 10)

- [x] **Step 3: Commit**

```bash
git add -A
git commit -m "refactor: remove old companion browser VM code (BrowserVMIP, BrowserVMStatus)"
```

---

## Task 10: Frontend — Browser VMs List Page

**Files:**
- Create: `frontend/src/pages/BrowserVMsPage.tsx`
- Modify: `frontend/src/lib/api.ts` (add browser VM API calls)
- Modify: `frontend/src/lib/types.ts` (add BrowserVM type)
- Modify: `frontend/src/App.tsx` (add route)
- Modify: `frontend/src/components/AppShell.tsx` (add nav item)

- [x] **Step 1: Add BrowserVM type**

In `frontend/src/lib/types.ts`, add:

```typescript
export interface BrowserVM {
  id: string;
  account_id: number;
  slug: string;
  name: string;
  host_id: number | null;
  vm_ip: string | null;
  status: "stopped" | "provisioning" | "running" | "error";
  vcpus: number;
  memory_mb: number;
  cdp_port: number;
  rootfs_version: string | null;
  error_message: string | null;
  created_at: string;
  updated_at: string;
}
```

- [x] **Step 2: Add API client methods**

In `frontend/src/lib/api.ts`, add:

```typescript
export const listBrowserVMs = (accountId: number) =>
  request<BrowserVM[]>(`/accounts/${accountId}/browser-vms`);

export const createBrowserVM = (accountId: number, data: { name?: string }) =>
  request<BrowserVM>(`/accounts/${accountId}/browser-vms`, {
    method: "POST",
    body: JSON.stringify(data),
  });

export const getBrowserVM = (accountId: number, id: string) =>
  request<BrowserVM>(`/accounts/${accountId}/browser-vms/${id}`);

export const startBrowserVM = (accountId: number, id: string, data?: { host_id?: number; region?: string }) =>
  request<BrowserVM>(`/accounts/${accountId}/browser-vms/${id}/start`, {
    method: "POST",
    body: JSON.stringify(data ?? {}),
  });

export const stopBrowserVM = (accountId: number, id: string) =>
  request<BrowserVM>(`/accounts/${accountId}/browser-vms/${id}/stop`, {
    method: "POST",
  });

export const deleteBrowserVM = (accountId: number, id: string) =>
  request<void>(`/accounts/${accountId}/browser-vms/${id}`, {
    method: "DELETE",
  });

export const pairBrowser = (accountId: number, machineId: string, browserVmId: string) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/pair-browser`, {
    method: "POST",
    body: JSON.stringify({ browser_vm_id: browserVmId }),
  });

export const unpairBrowser = (accountId: number, machineId: string) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/pair-browser`, {
    method: "DELETE",
  });
```

- [x] **Step 3: Create BrowserVMsPage**

Create `frontend/src/pages/BrowserVMsPage.tsx` following the pattern of the existing machines list. Include:
- List with status badges (stopped=yellow, provisioning=blue, running=green, error=red)
- "New Browser VM" button
- Start/stop/delete actions per row
- Host and paired machine columns

This is a standard CRUD list page — follow the pattern of the existing `Dashboard` or machines list page in the codebase.

- [x] **Step 4: Add route and nav item**

In `App.tsx`, add the route inside the AppShell layout block:
```tsx
<Route path="/browser-vms" element={<BrowserVMsPage />} />
```

In `AppShell.tsx`, add to `navItems`:
```tsx
{ label: "Browser VMs", path: "/browser-vms", Icon: Globe },
```

Import `Globe` from `lucide-react`.

- [x] **Step 5: Run typecheck**

Run: `make typecheck`
Expected: PASS

- [x] **Step 6: Commit**

```bash
git add frontend/
git commit -m "feat(frontend): add Browser VMs list page with CRUD"
```

---

## Task 11: Frontend — Browser Tab Pairing UI

**Files:**
- Rewrite: `frontend/src/pages/machine-tabs/BrowserTab.tsx`

- [x] **Step 1: Rewrite BrowserTab with pairing UI**

Replace the entire contents of `BrowserTab.tsx`:

```tsx
import { useState, useEffect } from "react";
import { Globe } from "lucide-react";
import {
  listBrowserVMs,
  pairBrowser,
  unpairBrowser,
  getBrowserVM,
  pushMachineConfig,
} from "../../lib/api";
import { useToast } from "../../components/Toast";
import type { Machine } from "../../lib/types";
import type { BrowserVM } from "../../lib/types";

interface BrowserTabProps {
  machine: Machine;
  accountId: number;
}

export function BrowserTab({ machine, accountId }: BrowserTabProps) {
  const [pairedBVM, setPairedBVM] = useState<BrowserVM | null>(null);
  const [availableBVMs, setAvailableBVMs] = useState<BrowserVM[]>([]);
  const [selectedBVMId, setSelectedBVMId] = useState("");
  const [loading, setLoading] = useState(true);
  const [pairing, setPairing] = useState(false);
  const { toast } = useToast();

  useEffect(() => {
    async function load() {
      try {
        // Load paired browser VM if any
        if (machine.browser_vm_id) {
          const bvm = await getBrowserVM(accountId, machine.browser_vm_id);
          setPairedBVM(bvm);
        }

        // Load available browser VMs (running, same host, not already paired)
        const all = await listBrowserVMs(accountId);
        const available = all.filter(
          (bvm) =>
            bvm.status === "running" &&
            bvm.host_id === machine.host_id &&
            bvm.id !== machine.browser_vm_id
        );
        setAvailableBVMs(available);
      } catch {
        // ignore
      } finally {
        setLoading(false);
      }
    }
    load();
  }, [accountId, machine.id, machine.browser_vm_id, machine.host_id]);

  const handlePair = async () => {
    if (!selectedBVMId) return;
    setPairing(true);
    try {
      await pairBrowser(accountId, machine.id, selectedBVMId);
      const bvm = await getBrowserVM(accountId, selectedBVMId);
      setPairedBVM(bvm);
      toast({ title: "Browser paired", description: `Connected to ${bvm.slug}`, variant: "success" });
    } catch (err) {
      toast({
        title: "Failed to pair",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setPairing(false);
    }
  };

  const handleUnpair = async () => {
    setPairing(true);
    try {
      await unpairBrowser(accountId, machine.id);
      setPairedBVM(null);
      toast({ title: "Browser unpaired", variant: "success" });
    } catch (err) {
      toast({
        title: "Failed to unpair",
        description: err instanceof Error ? err.message : "Unknown error",
        variant: "error",
      });
    } finally {
      setPairing(false);
    }
  };

  if (loading) {
    return <div className="p-6 text-text-tertiary">Loading...</div>;
  }

  return (
    <div className="space-y-4">
      <div className="bg-card border border-border rounded-[var(--radius-lg)] shadow-card overflow-hidden">
        <div className="p-4 md:p-6">
          <p className="text-lg md:text-xl font-semibold text-text-primary mb-1">Web Browser</p>
          <p className="text-xs md:text-sm text-text-tertiary mb-4">
            Connect a browser VM to enable web browsing capabilities
          </p>

          {pairedBVM ? (
            /* Paired state */
            <div className="border border-green-500/30 rounded-[var(--radius-sm)] p-3 md:p-4 bg-green-500/5">
              <div className="flex items-center justify-between gap-3">
                <div className="flex items-center gap-3">
                  <div className="w-9 h-9 rounded-lg bg-green-500/10 flex items-center justify-center flex-shrink-0">
                    <Globe className="w-4 h-4 text-green-400" />
                  </div>
                  <div>
                    <div className="flex items-center gap-2">
                      <span className="text-[11px] px-2 py-0.5 rounded-full bg-green-500/10 text-green-400 border border-green-500/20 font-medium">
                        connected
                      </span>
                      <span className="text-sm font-medium text-text-primary">{pairedBVM.slug}</span>
                    </div>
                    <p className="text-xs text-text-tertiary mt-0.5">
                      CDP: {pairedBVM.vm_ip}:{pairedBVM.cdp_port}
                    </p>
                  </div>
                </div>
                <button
                  onClick={handleUnpair}
                  disabled={pairing}
                  className="text-sm px-3 py-1.5 rounded-md border border-red-500/30 text-red-400 hover:bg-red-500/10 disabled:opacity-50"
                >
                  Unpair
                </button>
              </div>
            </div>
          ) : machine.status !== "running" ? (
            /* Machine not running */
            <p className="text-sm text-text-tertiary">Start the machine to pair a browser VM.</p>
          ) : availableBVMs.length === 0 ? (
            /* No available browser VMs */
            <div className="text-sm text-text-tertiary">
              <p>No browser VMs available on this host.</p>
              <a href="/browser-vms" className="text-brand-500 hover:underline">
                Create a browser VM
              </a>
            </div>
          ) : (
            /* Unpaired — show dropdown */
            <div className="flex items-center gap-3">
              <select
                value={selectedBVMId}
                onChange={(e) => setSelectedBVMId(e.target.value)}
                className="flex-1 bg-deep border border-border rounded-md px-3 py-2 text-sm text-text-primary"
              >
                <option value="">Select a browser VM...</option>
                {availableBVMs.map((bvm) => (
                  <option key={bvm.id} value={bvm.id}>
                    {bvm.slug} ({bvm.status})
                  </option>
                ))}
              </select>
              <button
                onClick={handlePair}
                disabled={!selectedBVMId || pairing}
                className="px-4 py-2 rounded-md bg-brand-600 text-white text-sm hover:bg-brand-700 disabled:opacity-50"
              >
                Pair
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
```

- [x] **Step 2: Add browser_vm_id to Machine type**

In `frontend/src/lib/types.ts`, add to the Machine interface:
```typescript
browser_vm_id?: string | null;
```

- [x] **Step 3: Run typecheck**

Run: `make typecheck`
Expected: PASS

- [x] **Step 4: Commit**

```bash
git add frontend/src/pages/machine-tabs/BrowserTab.tsx frontend/src/lib/types.ts
git commit -m "feat(frontend): rewrite Browser tab with pairing UI"
```

---

## Task 12: Auto-Unpair on Machine Host Change

**Files:**
- Modify: `backend/internal/machines/runtime.go`

- [x] **Step 1: Add auto-unpair check to machine start**

In `runtime.go`, after placement is resolved and before the VM request is sent to the agent, add:

```go
// Auto-unpair if browser VM is on a different host
if machine.BrowserVMID != nil {
    bvm, bvmErr := rs.store.GetBrowserVM(ctx, *machine.BrowserVMID)
    if bvmErr != nil || bvm.HostID == nil || *bvm.HostID != host.ID {
        slog.Info("machine.start.auto_unpair", "machine_id", machine.ID,
            "browser_vm_id", *machine.BrowserVMID,
            "reason", "host_mismatch")
        rs.store.UnpairBrowserVM(ctx, machine.ID)
    }
}
```

- [x] **Step 2: Run tests**

Run: `make test-go`
Expected: PASS

- [x] **Step 3: Commit**

```bash
git add backend/internal/machines/runtime.go
git commit -m "feat(runtime): auto-unpair browser VM on host mismatch at machine start"
```

---

## Task 13: Update CurrentFeature.md

**Files:**
- Modify: `docs/CurrentFeature.md`

- [x] **Step 1: Update the feature doc**

Update `docs/CurrentFeature.md` with a summary of the implemented browser VM feature, referencing the spec and key files changed.

- [x] **Step 2: Run full test suite**

Run: `make test`
Expected: All tests PASS

- [x] **Step 3: Final commit**

```bash
git add docs/CurrentFeature.md
git commit -m "docs: update CurrentFeature.md for browser VM feature"
```
