# Clean Update Flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix machine lifecycle so "provisioning" only happens on first boot, updates use a lightweight stop (keep tunnels), "restarting" status is removed, and capacity failures on restart return an error instead of silently losing data.

**Architecture:** The runtime service gains a `StopForUpdate` method that skips tunnel/DNS/KV cleanup. The trigger-update handler properly stops machines before triggering the agent, stores restart intent in the host's `status_message`, and restarts them on heartbeat resume. `RecoverAffinity` no longer silently falls through to fresh placement when the home host lacks capacity.

**Tech Stack:** Go (backend), TypeScript/React (frontend), PostgreSQL

**Spec:** `docs/superpowers/specs/2026-03-23-clean-update-flow-design.md`

---

## File Structure

| File | Responsibility | Action |
|------|---------------|--------|
| `backend/internal/machines/runtime.go` | Machine start/stop lifecycle | Modify: add `StopForUpdate`, fix restart status, fix capacity fallback |
| `backend/internal/machines/runtime_test.go` | Runtime unit tests | Modify: add tests for `StopForUpdate`, restart status, capacity error |
| `backend/internal/fleet/placement.go` | Host placement & affinity | Modify: `RecoverAffinity` returns error on capacity failure |
| `backend/internal/fleet/placement_test.go` | Placement unit tests | Modify: update capacity failure test expectation |
| `backend/internal/api/server.go` | HTTP handlers | Modify: rewrite `handleTriggerHostUpdate`, `restartMachinesAfterUpdate` |
| `backend/internal/store/postgres.go` | DB queries | Modify: add `'starting'` to `FindStuckMachines` |
| `frontend/src/lib/types.ts` | Type definitions | Modify: remove `"restarting"` |
| `frontend/src/components/MachineCard.tsx` | Machine card UI | Modify: remove `restarting` entries |
| `frontend/src/pages/MachineView.tsx` | Machine detail page | Modify: remove `"restarting"` check |
| `frontend/src/pages/MachineWorkspace.tsx` | Workspace page | Modify: remove `"restarting"` checks |
| `frontend/src/pages/GatewayDashboard.tsx` | Gateway dashboard | Modify: remove `"restarting"` checks |
| `frontend/src/pages/admin/AdminMachines.tsx` | Admin machines page | Modify: remove `"restarting"` from filters/badges/options |

---

### Task 1: Lightweight stop for updates

Add `StopForUpdate` to `RuntimeService` — stops the VM and releases capacity but preserves tunnels, DNS, and KV routes.

**Files:**
- Modify: `backend/internal/machines/runtime.go:556-657`
- Test: `backend/internal/machines/runtime_test.go`

- [ ] **Step 1: Write failing test for StopForUpdate**

In `runtime_test.go`, add a test that verifies `StopForUpdate` calls `StopVM` and releases capacity but does NOT call `ClearMachineTunnel` or `DeleteRouteFromKV`:

```go
func TestStopForUpdate_SkipsTunnelAndKVCleanup(t *testing.T) {
	hostID := 9
	ms := &mockStore{
		getHostResult: &store.Host{ID: hostID, Status: "ready"},
		getMachineResult: &store.Machine{
			ID: "m-update-stop", Slug: "test", AccountID: 1,
			HostID: &hostID, VCPUs: 2, MemoryMB: 2048,
			TunnelID: strPtr("tun-123"),
		},
	}
	ac := &mockAgentClient{}
	ps := fleet.NewPlacementService(ms, "", "")
	rs := NewRuntimeService(ms, ps, ac, nil, nil, nil, RuntimeConfig{})

	machine := ms.getMachineResult
	err := rs.StopForUpdate(context.Background(), machine)
	require.NoError(t, err)

	assert.True(t, ac.stopVMCalled, "should call StopVM on agent")
	assert.True(t, ms.softStopCalled, "should release capacity")
	assert.False(t, ms.clearTunnelCalled, "should NOT clear tunnel")
	assert.False(t, ms.kvRouteDeleteCalled, "should NOT delete KV route")
}
```

- [ ] **Step 2: Add mock tracking fields**

Add to `mockStore`:
```go
clearTunnelCalled  bool
kvRouteDeleteCalled bool
```

Update `ClearMachineTunnel` mock to set `clearTunnelCalled = true`.

Add to `mockAgentClient`:
```go
stopVMCalled bool
```

- [ ] **Step 3: Run test — verify it fails**

```bash
cd backend && go test ./internal/machines/... -run TestStopForUpdate -v
```

Expected: compilation error — `StopForUpdate` not defined.

- [ ] **Step 4: Implement StopForUpdate**

In `runtime.go`, add after the existing `stop` method:

```go
// StopForUpdate stops a machine for an agent update. Calls agent StopVM and
// releases host capacity, but preserves tunnels, DNS records, and KV routes
// since the machine will restart on the same host.
func (rs *RuntimeService) StopForUpdate(ctx context.Context, machine *store.Machine) error {
	// Call agent to stop VM (sends SIGTERM to gateway via Firecracker Shutdown)
	if rs.agentClient != nil && machine.HostID != nil {
		host, err := rs.store.GetHost(ctx, *machine.HostID)
		if err == nil {
			if stopErr := rs.agentClient.StopVM(ctx, host, machine.ID); stopErr != nil {
				slog.Error("machine.stop_for_update.vm.failed", "machine_id", machine.ID, "error", stopErr)
				return fmt.Errorf("failed to stop VM for update: %w", stopErr)
			}
		}
	}

	// Release capacity but keep host_id/vm_ip (soft release)
	if err := rs.placement.Release(ctx, machine.ID, fleet.ReleaseSoft); err != nil {
		return fmt.Errorf("failed to release capacity: %w", err)
	}

	// Set status to stopped — skip tunnel/KV/DNS cleanup
	if err := rs.store.UpdateMachineStatus(ctx, machine.ID, "stopped", nil); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}
```

- [ ] **Step 5: Run test — verify it passes**

```bash
cd backend && go test ./internal/machines/... -run TestStopForUpdate -v
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add backend/internal/machines/runtime.go backend/internal/machines/runtime_test.go
git commit -m "feat: add StopForUpdate — lightweight stop that preserves tunnels"
```

---

### Task 2: Fix restart status (starting, not provisioning)

The `UpdateMachineStatus` call added in the earlier session is already in `runtime.go:418-424`. Verify it works with a test.

**Files:**
- Verify: `backend/internal/machines/runtime.go:418-424` (already modified)
- Test: `backend/internal/machines/runtime_test.go`

- [ ] **Step 1: Write failing test for restart status**

```go
func TestStart_RestartSetsStartingStatus(t *testing.T) {
	hostID := 42
	ms := &mockStore{
		getHostResult: &store.Host{ID: hostID, Status: "ready",
			LastHeartbeat: timePtr(time.Now())},
		getMachineResult: &store.Machine{
			ID: "m-restart-status", Slug: "test", AccountID: 1,
			HostID: &hostID, VMIP: strPtr("10.0.0.5"),
			VCPUs: 2, MemoryMB: 2048,
		},
	}
	ac := &mockAgentClient{}
	ps := fleet.NewPlacementService(ms, "", "")
	rs := NewRuntimeService(ms, ps, ac, nil, nil, nil, RuntimeConfig{})

	_, _, err := rs.Start(context.Background(), 1, ms.getMachineResult)
	require.NoError(t, err)

	assert.Equal(t, "starting", ms.lastStatusUpdate,
		"restart should set status to 'starting', not 'provisioning'")
}
```

Add tracking to `mockStore`:
```go
lastStatusUpdate string
```

Update `UpdateMachineStatus` mock:
```go
func (m *mockStore) UpdateMachineStatus(_ context.Context, _, status string, _ *string) error {
	m.lastStatusUpdate = status
	return nil
}
```

- [ ] **Step 2: Run test — verify it passes (already implemented)**

```bash
cd backend && go test ./internal/machines/... -run TestStart_RestartSetsStartingStatus -v
```

Expected: PASS (the code change from the earlier session is already in place).

- [ ] **Step 3: Commit**

```bash
git add backend/internal/machines/runtime_test.go
git commit -m "test: verify restart sets 'starting' status, not 'provisioning'"
```

---

### Task 3: RecoverAffinity — error on capacity failure instead of silent fresh placement

**Files:**
- Modify: `backend/internal/fleet/placement.go:147-182`
- Test: `backend/internal/fleet/placement_test.go`
- Modify: `backend/internal/machines/runtime.go:342-377`

- [ ] **Step 1: Write/update failing test for capacity failure**

In `placement_test.go`, update `TestRecoverAffinity_FreshWhenCapacityFull` to expect an error instead of a fresh placement:

```go
func TestRecoverAffinity_ErrorWhenCapacityFull(t *testing.T) {
	ms := &mockPlacementStore{
		machine: &store.Machine{
			ID: "m-060", HostID: intPtr(20), VMIP: strPtr("192.168.2.10"),
			HomeHostID: intPtr(20),
		},
		host: &store.Host{ID: 20, Status: "ready",
			LastHeartbeat: timePtr(time.Now()),
			CapacityVCPUs: 4, CapacityMemoryMB: 8192,
			UsedVCPUs: 4, UsedMemoryMB: 8192}, // full
		reAllocateErr: fmt.Errorf("host 20 has insufficient capacity"),
	}
	svc := NewPlacementService(ms)

	_, _, _, err := svc.RecoverAffinity(context.Background(), "m-060", PlacementRequest{
		VCPUs: 2, MemoryMB: 2048,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migration required")
}
```

- [ ] **Step 2: Run test — verify it fails**

```bash
cd backend && go test ./internal/fleet/... -run TestRecoverAffinity_ErrorWhenCapacityFull -v
```

Expected: FAIL — currently falls through to `Reserve()` and succeeds.

- [ ] **Step 3: Implement — remove fallback to Reserve in RecoverAffinity**

In `placement.go`, replace the fallback at line 176-181:

```go
func (ps *PlacementService) RecoverAffinity(ctx context.Context, machineID string, req PlacementRequest) (*store.Placement, *store.Host, string, error) {
	machine, err := ps.store.GetMachine(ctx, machineID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("get machine: %w", err)
	}

	var homeHostID *int
	if machine.HomeHostID != nil {
		homeHostID = machine.HomeHostID
	} else if machine.HostID != nil {
		homeHostID = machine.HostID
	}

	if homeHostID == nil {
		return nil, nil, "", fmt.Errorf("machine has no home host — cannot recover affinity")
	}

	host, err := ps.store.GetHost(ctx, *homeHostID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("home host lookup failed: %w", err)
	}

	if host.Status != "ready" || host.LastHeartbeat == nil {
		return nil, nil, "", fmt.Errorf("home host %d is %s — migration required to move machine to a new host", host.ID, host.Status)
	}

	if err := ps.store.ReAllocateHostCapacity(ctx, machineID, host.ID, req.VCPUs, req.MemoryMB); err != nil {
		return nil, nil, "", fmt.Errorf("home host %d has insufficient capacity — migration required: %w", host.ID, err)
	}

	vmIP := ""
	if machine.VMIP != nil {
		vmIP = *machine.VMIP
	}
	placement, _ := ps.store.ReservePlacement(ctx, machineID, host.ID, vmIP, req.OperationID)
	slog.Info("placement.affinity_reused", "machine_id", machineID, "host_id", host.ID)
	return placement, host, vmIP, nil
}
```

- [ ] **Step 4: Update runtime.go start() to handle the error**

In `runtime.go`, the affinity path at lines 370-377 currently wraps the error. Update the error message to be user-friendly:

```go
		} else {
			// Normal restart on same host — use affinity recovery
			placement, host, vmIP, err = rs.placement.RecoverAffinity(ctx, machine.ID, placementReq)
			if err != nil {
				return nil, "", fmt.Errorf("cannot restart on home host: %w", err)
			}
			slog.Info("machine.start.host_affinity", "machine_id", machine.ID, "host_id", host.ID, "vm_ip", vmIP)
		}
```

Also remove the unreachable/draining/error fallback to `Reserve()` at lines 363-369 — these should also return errors since the data volume is on the home host:

```go
		} else if prevHost.Status == "unreachable" || prevHost.Status == "error" || prevHost.Status == "draining" {
			return nil, "", fmt.Errorf("home host %d is %s — migration required", *machine.HostID, prevHost.Status)
		} else {
```

- [ ] **Step 5: Update TestRecoverAffinity_FreshWhenHomeDead**

This test should still break affinity when the host is `"terminated"` (permanently gone) — that's correct because data is lost. But verify the test still expects `UnassignMachineFromHost` to be called. No change needed if the test checks for terminated hosts specifically.

- [ ] **Step 6: Run all placement and machines tests**

```bash
cd backend && go test ./internal/fleet/... ./internal/machines/... -v -count=1
```

Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add backend/internal/fleet/placement.go backend/internal/fleet/placement_test.go backend/internal/machines/runtime.go
git commit -m "fix: return error when home host lacks capacity instead of silent fresh placement"
```

---

### Task 4: Rewrite handleTriggerHostUpdate — parallel stop + restart tracking

**Files:**
- Modify: `backend/internal/api/server.go:1781-1897`

- [ ] **Step 1: Rewrite handleTriggerHostUpdate**

Replace the current implementation (lines 1781-1848) with:

```go
func (s *Server) handleTriggerHostUpdate(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseIntParam(r, "hostId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}

	host, err := s.store.GetHost(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}

	if host.Status != "ready" {
		writeError(w, http.StatusConflict, "host is not ready (status: "+host.Status+")")
		return
	}

	ctx := r.Context()
	slog.Info("admin.trigger_update.starting", "host_id", hostID, "host_name", host.VMName)

	// Find all running machines on this host
	machines, err := s.store.ListMachinesByHost(ctx, hostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list machines: "+err.Error())
		return
	}

	var running []store.Machine
	for _, m := range machines {
		if m.Status == "running" {
			running = append(running, m)
		}
	}

	// Stop all running machines in parallel (lightweight — keep tunnels/DNS/KV)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var stoppedIDs []string
	var stopErrors []string

	for _, m := range running {
		wg.Add(1)
		go func(machine store.Machine) {
			defer wg.Done()
			if err := s.machines.StopForUpdate(ctx, &machine); err != nil {
				slog.Error("admin.trigger_update.stop_failed",
					"machine_id", machine.ID, "host_id", hostID, "error", err)
				mu.Lock()
				stopErrors = append(stopErrors, machine.ID+": "+err.Error())
				mu.Unlock()
				return
			}
			slog.Info("admin.trigger_update.stopped",
				"machine_id", machine.ID, "machine_slug", machine.Slug, "host_id", hostID)
			mu.Lock()
			stoppedIDs = append(stoppedIDs, machine.ID)
			mu.Unlock()
		}(m)
	}
	wg.Wait()

	if len(stopErrors) > 0 {
		slog.Error("admin.trigger_update.partial_stop_failure",
			"host_id", hostID, "errors", strings.Join(stopErrors, "; "))
	}

	// Store pending restart list in host status_message and mark as updating
	pendingJSON, _ := json.Marshal(map[string][]string{"pending_restarts": stoppedIDs})
	if err := s.store.UpdateHostStatusMessage(ctx, hostID, "updating", string(pendingJSON)); err != nil {
		slog.Error("admin.trigger_update.host_status_failed", "host_id", hostID, "error", err)
	}
	slog.Info("admin.trigger_update.machines_stopped",
		"host_id", hostID, "stopped", len(stoppedIDs), "failed", len(stopErrors))

	// Trigger agent self-update (no VMs to drain — already stopped)
	if err := s.agentClient.TriggerUpdate(ctx, host); err != nil {
		slog.Error("admin.trigger_update.agent_call_failed", "host_id", hostID, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("admin.trigger_update.accepted", "host_id", hostID, "host_name", host.VMName)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":          "updating",
		"machines_stopped": len(stoppedIDs),
		"message":         "update triggered on " + host.VMName,
	})
}
```

- [ ] **Step 2: Verify existing store methods**

The store already has the methods we need:
- `UpdateHostStatusMessage(ctx, id, status, message string)` — sets status + status_message
- `UpdateHostStatus(ctx, id, status string)` — sets status and clears status_message to NULL

No new store methods needed.

- [ ] **Step 3: Rewrite restartMachinesAfterUpdate**

Replace the current implementation (lines 1853-1897) with:

```go
func (s *Server) restartMachinesAfterUpdate(hostID int, pendingIDs []string) {
	if len(pendingIDs) == 0 {
		slog.Info("heartbeat.restart_machines.none_pending", "host_id", hostID)
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		slog.Info("heartbeat.restart_machines.starting", "host_id", hostID, "count", len(pendingIDs))

		for _, machineID := range pendingIDs {
			machine, err := s.store.GetMachine(ctx, machineID)
			if err != nil {
				slog.Error("heartbeat.restart_machines.get_failed",
					"machine_id", machineID, "host_id", hostID, "error", err)
				continue
			}

			if machine.Status != "stopped" {
				slog.Warn("heartbeat.restart_machines.skip_not_stopped",
					"machine_id", machineID, "status", machine.Status, "host_id", hostID)
				continue
			}

			slog.Info("heartbeat.restart_machines.starting_machine",
				"machine_id", machine.ID, "machine_slug", machine.Slug, "host_id", hostID)

			_, _, err = s.machines.Start(ctx, machine.AccountID, machine)
			if err != nil {
				slog.Error("heartbeat.restart_machines.start_failed",
					"machine_id", machine.ID, "host_id", hostID, "error", err)
				msg := "failed to restart after update: " + err.Error()
				_ = s.store.UpdateMachineStatus(ctx, machine.ID, "stopped", &msg)
			} else {
				slog.Info("heartbeat.restart_machines.started",
					"machine_id", machine.ID, "machine_slug", machine.Slug, "host_id", hostID)
			}
		}

		slog.Info("heartbeat.restart_machines.completed", "host_id", hostID, "count", len(pendingIDs))
	}()
}
```

Note: The host status_message is already cleared when the heartbeat handler calls `UpdateHostStatus(ctx, hostID, "ready")` — it sets `status_message = NULL`.

- [ ] **Step 4: Update heartbeat handler to read pending restarts from status_message**

In `server.go`, around line 2107-2109, update the `"updating"` recovery block:

```go
		if prevStatus == "updating" {
			// Read pending restart list from status_message
			var pendingIDs []string
			if host.StatusMessage != nil {
				var payload struct {
					PendingRestarts []string `json:"pending_restarts"`
				}
				if err := json.Unmarshal([]byte(*host.StatusMessage), &payload); err == nil {
					pendingIDs = payload.PendingRestarts
				}
			}
			s.restartMachinesAfterUpdate(hostID, pendingIDs)
		}
```

- [ ] **Step 5: Run full backend tests**

```bash
cd backend && go test ./internal/api/... ./internal/machines/... ./internal/fleet/... -count=1
```

Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add backend/internal/api/server.go
git commit -m "feat: rewrite update flow — parallel lightweight stop, restart from status_message"
```

---

### Task 5: Update stuck machine detector

**Files:**
- Modify: `backend/internal/store/postgres.go:601-606`

- [ ] **Step 1: Add 'starting' to FindStuckMachines query**

```go
func (s *PostgresStore) FindStuckMachines(ctx context.Context, stuckThreshold time.Duration) ([]Machine, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, slug, account_id, status, host_id
		 FROM machines
		 WHERE status IN ('provisioning', 'starting', 'stopping')
		   AND COALESCE(provisioning_started_at, created_at) < now() - $1::interval`, fmt.Sprintf("%d seconds", int(stuckThreshold.Seconds())))
```

- [ ] **Step 2: Run reconciler tests**

```bash
cd backend && go test ./internal/reconciler/... -v -count=1
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add backend/internal/store/postgres.go
git commit -m "fix: include 'starting' in stuck machine detector"
```

---

### Task 6: Remove "restarting" status from frontend

**Files:**
- Modify: `frontend/src/lib/types.ts:168`
- Modify: `frontend/src/components/MachineCard.tsx:18,27`
- Modify: `frontend/src/pages/MachineView.tsx:68`
- Modify: `frontend/src/pages/MachineWorkspace.tsx:217,317,354`
- Modify: `frontend/src/pages/GatewayDashboard.tsx:123,166`
- Modify: `frontend/src/pages/admin/AdminMachines.tsx:38,55,324,369,568`

- [ ] **Step 1: Remove from types.ts**

Change line 168:
```typescript
status: "stopped" | "provisioning" | "starting" | "running" | "error";
```

- [ ] **Step 2: Remove from MachineCard.tsx**

Remove `restarting` entries from both `statusBadge` and `statusDot` maps (lines 18, 27):
```typescript
// Remove these lines:
//   restarting: "badge-provisioning",
//   restarting: "bg-yellow-400 shadow-[0_0_6px_rgba(250,204,21,0.6)]",
```

Remove `|| machine.status === "restarting"` from `isBooting` (line 117).

- [ ] **Step 3: Remove from MachineView.tsx**

Line 68 — remove `|| machine?.status === "restarting"`:
```typescript
const isBooting = machine?.status === "provisioning" || machine?.status === "starting";
```

- [ ] **Step 4: Remove from MachineWorkspace.tsx**

Lines 217, 317, 354 — remove `|| machine.status === "restarting"` from all three checks.

- [ ] **Step 5: Remove from GatewayDashboard.tsx**

Lines 123, 166 — remove `|| machine.status === "restarting"` from both checks.

- [ ] **Step 6: Remove from AdminMachines.tsx**

- Lines 38, 55, 324 — remove `case "restarting":` from switch/case blocks
- Line 369 — remove `<option value="restarting">Restarting</option>`
- Line 568 — remove `|| detailMachineData.status === "restarting"`

- [ ] **Step 7: Run typecheck**

```bash
cd frontend && npx tsc --noEmit
```

Expected: no errors

- [ ] **Step 8: Run frontend tests**

```bash
cd frontend && npx vitest run
```

Expected: all PASS (MachineCard tests for "restarting" will need removal if they exist — check `MachineCard.test.tsx`).

- [ ] **Step 9: Commit**

```bash
git add frontend/src/
git commit -m "feat: remove 'restarting' status from frontend — machines use stopped→starting flow"
```

---

### Task 7: Run full test suite and gateway E2E

- [ ] **Step 1: Run Go tests**

```bash
make test-go
```

Expected: all PASS

- [ ] **Step 2: Run frontend tests + typecheck**

```bash
make test-frontend && make typecheck
```

Expected: all PASS

- [ ] **Step 3: Run gateway E2E tests**

```bash
make test-gateway-e2e
```

Expected: all PASS

- [ ] **Step 4: Final commit with updated CurrentFeature.md**

Update `docs/CurrentFeature.md` to mark completed items, then:

```bash
git add docs/CurrentFeature.md
git commit -m "docs: mark clean update flow tasks as complete"
```
