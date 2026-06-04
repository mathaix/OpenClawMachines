# P0 Safety Fixes Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Prevent the scheduler from placing machines onto dead hosts, and add automatic detection and cleanup when hosts die.

**Architecture:** A new host reconciler goroutine runs every 60s on the control plane, detecting stale heartbeats, confirming host death via GCP API, and cleaning up affected machines. The agent gets graceful VM shutdown on SIGTERM. The placement queries get heartbeat freshness filters.

**Tech Stack:** Go 1.25, pgx/v5 (raw SQL), GCP Compute API, Cloudflare KV + Tunnels, Chi router

---

### Task 1: Heartbeat Filter in Store Queries

**Files:**
- Modify: `backend/internal/store/postgres.go:446-461` (FindHostWithCapacity)
- Modify: `backend/internal/store/postgres.go:1147-1149` (PlaceMachineOnHost)
- Test: `backend/internal/store/postgres_placement_test.go` (new file)

**Step 1: Write the failing test**

Create `backend/internal/store/postgres_placement_test.go`. Since these are SQL queries against a real DB, and the existing scheduler tests use mocks, we'll write a unit test at the scheduler layer that verifies the mock behavior, then manually verify the SQL change.

Actually — the scheduler tests use `mockStore`, so we test the contract there. The SQL change is tested via `make test-gateway-e2e` which hits a real DB.

Write a scheduler-level test instead:

```go
// backend/internal/scheduler/scheduler_test.go — add to existing file

func TestPlaceMachine_StaleHostExcluded(t *testing.T) {
	// mockStore.PlaceMachineOnHost returns error when no host available
	ms := &mockStore{placeErr: fmt.Errorf("no host with matching image and sufficient capacity")}
	sched := New(ms, "us-central1", "")

	machine := &store.Machine{ID: "m-1", VCPUs: 2, MemoryMB: 2048}
	_, _, err := sched.PlaceMachine(context.Background(), machine)
	if err == nil {
		t.Fatal("expected error when no host available")
	}
}
```

This validates the existing contract. The real test is the SQL WHERE clause change — verified by `make test-gateway-e2e`.

**Step 2: Run test to verify it passes (this is a contract test)**

Run: `cd backend && go test ./internal/scheduler/ -run TestPlaceMachine_StaleHostExcluded -v`
Expected: PASS

**Step 3: Modify the SQL queries**

In `backend/internal/store/postgres.go`, add heartbeat filter to both queries.

For `FindHostWithCapacity` (line 454), add after `AND status = 'ready'`:
```sql
AND last_heartbeat IS NOT NULL
AND last_heartbeat > now() - interval '180 seconds'
```

For `PlaceMachineOnHost` (line 1147-1149), add after `WHERE status = 'ready'`:
```sql
AND last_heartbeat IS NOT NULL
AND last_heartbeat > now() - interval '180 seconds'
```

**Step 4: Run all tests**

Run: `make test-go`
Expected: PASS

**Step 5: Commit**

```bash
git add backend/internal/store/postgres.go backend/internal/scheduler/scheduler_test.go
git commit -m "fix: exclude stale-heartbeat hosts from placement queries"
```

---

### Task 2: New Store Methods for Reconciler

**Files:**
- Modify: `backend/internal/store/store.go:290-307` (Store interface — add new methods)
- Modify: `backend/internal/store/postgres.go` (implement new methods)
- Modify: `backend/internal/scheduler/scheduler_test.go` (update mockStore)

**Step 1: Add interface methods to Store**

In `backend/internal/store/store.go`, add to the `// Hosts` section:

```go
ListStaleHosts(ctx context.Context, threshold time.Duration) ([]Host, error)
ListUnreachableHosts(ctx context.Context) ([]Host, error)
MarkMachinesOnHostError(ctx context.Context, hostID int, message string) ([]string, error)
```

**Step 2: Implement in postgres.go**

```go
// ListStaleHosts returns hosts that are 'ready' but have stale or missing heartbeats.
func (s *PostgresStore) ListStaleHosts(ctx context.Context, threshold time.Duration) ([]Host, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, vm_name, vm_id, zone, region, machine_type, external_ip, internal_ip,
		        tunnel_url, status, status_message, source_image, capacity_vcpus, capacity_memory_mb,
		        used_vcpus, used_memory_mb, machine_count, agent_version, openclaw_version,
		        rootfs_version, browser_rootfs_version, last_heartbeat, created_at
		 FROM hosts
		 WHERE status = 'ready'
		   AND (last_heartbeat IS NULL OR last_heartbeat < now() - $1::interval)
		 ORDER BY id`, fmt.Sprintf("%d seconds", int(threshold.Seconds())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hosts []Host
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.ID, &h.VMName, &h.VMID, &h.Zone, &h.Region, &h.MachineType,
			&h.ExternalIP, &h.InternalIP, &h.TunnelURL, &h.Status, &h.StatusMessage, &h.SourceImage,
			&h.CapacityVCPUs, &h.CapacityMemoryMB, &h.UsedVCPUs, &h.UsedMemoryMB,
			&h.MachineCount, &h.AgentVersion, &h.OpenclawVersion,
			&h.RootfsVersion, &h.BrowserRootfsVersion, &h.LastHeartbeat, &h.CreatedAt); err != nil {
			return nil, err
		}
		hosts = append(hosts, h)
	}
	return hosts, rows.Err()
}

// ListUnreachableHosts returns hosts with status 'unreachable'.
func (s *PostgresStore) ListUnreachableHosts(ctx context.Context) ([]Host, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, vm_name, vm_id, zone, region, machine_type, external_ip, internal_ip,
		        tunnel_url, status, status_message, source_image, capacity_vcpus, capacity_memory_mb,
		        used_vcpus, used_memory_mb, machine_count, agent_version, openclaw_version,
		        rootfs_version, browser_rootfs_version, last_heartbeat, created_at
		 FROM hosts
		 WHERE status = 'unreachable'
		 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hosts []Host
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.ID, &h.VMName, &h.VMID, &h.Zone, &h.Region, &h.MachineType,
			&h.ExternalIP, &h.InternalIP, &h.TunnelURL, &h.Status, &h.StatusMessage, &h.SourceImage,
			&h.CapacityVCPUs, &h.CapacityMemoryMB, &h.UsedVCPUs, &h.UsedMemoryMB,
			&h.MachineCount, &h.AgentVersion, &h.OpenclawVersion,
			&h.RootfsVersion, &h.BrowserRootfsVersion, &h.LastHeartbeat, &h.CreatedAt); err != nil {
			return nil, err
		}
		hosts = append(hosts, h)
	}
	return hosts, rows.Err()
}

// MarkMachinesOnHostError marks all active machines on a host as 'error'.
// Returns the IDs of affected machines for downstream cleanup (routes, tunnels).
func (s *PostgresStore) MarkMachinesOnHostError(ctx context.Context, hostID int, message string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE machines
		 SET status = 'error', status_message = $2
		 WHERE host_id = $1 AND status IN ('running', 'provisioning', 'starting')
		 RETURNING id`, hostID, message)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
```

**Step 3: Add stubs to mockStore in scheduler_test.go**

```go
func (m *mockStore) ListStaleHosts(_ context.Context, _ time.Duration) ([]store.Host, error) {
	return nil, nil
}
func (m *mockStore) ListUnreachableHosts(_ context.Context) ([]store.Host, error) {
	return nil, nil
}
func (m *mockStore) MarkMachinesOnHostError(_ context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
```

**Step 4: Run all tests**

Run: `make test-go`
Expected: PASS (new methods compile, existing tests still work)

**Step 5: Commit**

```bash
git add backend/internal/store/store.go backend/internal/store/postgres.go backend/internal/scheduler/scheduler_test.go
git commit -m "feat: add store methods for host reconciliation"
```

---

### Task 3: Host Reconciler

**Files:**
- Create: `backend/internal/reconciler/host.go`
- Create: `backend/internal/reconciler/host_test.go`

**Step 1: Write the failing test**

Create `backend/internal/reconciler/host_test.go`:

```go
package reconciler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// mockReconcilerStore implements the subset of store.Store the reconciler uses.
type mockReconcilerStore struct {
	staleHosts       []store.Host
	unreachableHosts []store.Host
	updatedStatuses  map[int]string
	hostEvents       []store.HostEvent
	machinesByHost   map[int][]store.Machine
	erroredMachines  map[int][]string
}

func newMockStore() *mockReconcilerStore {
	return &mockReconcilerStore{
		updatedStatuses: make(map[int]string),
		machinesByHost:  make(map[int][]store.Machine),
		erroredMachines: make(map[int][]string),
	}
}

func (m *mockReconcilerStore) ListStaleHosts(_ context.Context, _ time.Duration) ([]store.Host, error) {
	return m.staleHosts, nil
}
func (m *mockReconcilerStore) ListUnreachableHosts(_ context.Context) ([]store.Host, error) {
	return m.unreachableHosts, nil
}
func (m *mockReconcilerStore) UpdateHostStatus(_ context.Context, id int, status string) error {
	m.updatedStatuses[id] = status
	return nil
}
func (m *mockReconcilerStore) UpdateHostStatusMessage(_ context.Context, id int, status, message string) error {
	m.updatedStatuses[id] = status
	return nil
}
func (m *mockReconcilerStore) CreateHostEvent(_ context.Context, event *store.HostEvent) error {
	m.hostEvents = append(m.hostEvents, *event)
	return nil
}
func (m *mockReconcilerStore) MarkMachinesOnHostError(_ context.Context, hostID int, _ string) ([]string, error) {
	ids := m.erroredMachines[hostID]
	return ids, nil
}
func (m *mockReconcilerStore) ListMachinesByHost(_ context.Context, hostID int) ([]store.Machine, error) {
	return m.machinesByHost[hostID], nil
}

// mockInstanceChecker simulates GCP instance existence checks.
type mockInstanceChecker struct {
	exists map[string]bool // key: "zone/vmName"
}

func (m *mockInstanceChecker) InstanceExists(_ context.Context, project, zone, vmName string) (bool, error) {
	key := zone + "/" + vmName
	exists, ok := m.exists[key]
	if !ok {
		return false, nil
	}
	return exists, nil
}

func TestReconciler_StaleHostBecomesUnreachable(t *testing.T) {
	ms := newMockStore()
	ms.staleHosts = []store.Host{{ID: 1, VMName: "host-1", Zone: "us-central1-b", Status: "ready"}}

	ic := &mockInstanceChecker{exists: map[string]bool{}}

	r := New(ms, ic, nil, nil, "test-project", 180*time.Second)
	r.reconcileOnce(context.Background())

	if ms.updatedStatuses[1] != "unreachable" {
		t.Fatalf("expected host 1 to be unreachable, got %q", ms.updatedStatuses[1])
	}
}

func TestReconciler_UnreachableHostTerminated(t *testing.T) {
	ms := newMockStore()
	ms.unreachableHosts = []store.Host{{ID: 2, VMName: "host-2", Zone: "us-central1-b", Status: "unreachable"}}
	ms.erroredMachines[2] = []string{"m-1", "m-2"}

	ic := &mockInstanceChecker{exists: map[string]bool{"us-central1-b/host-2": false}}

	r := New(ms, ic, nil, nil, "test-project", 180*time.Second)
	r.reconcileOnce(context.Background())

	if ms.updatedStatuses[2] != "terminated" {
		t.Fatalf("expected host 2 to be terminated, got %q", ms.updatedStatuses[2])
	}
}

func TestReconciler_SkipsErrorHosts(t *testing.T) {
	ms := newMockStore()
	// No stale or unreachable hosts — reconciler should be a no-op
	ms.staleHosts = nil
	ms.unreachableHosts = nil

	ic := &mockInstanceChecker{}
	r := New(ms, ic, nil, nil, "test-project", 180*time.Second)
	r.reconcileOnce(context.Background())

	if len(ms.updatedStatuses) != 0 {
		t.Fatalf("expected no status updates, got %v", ms.updatedStatuses)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/reconciler/ -v`
Expected: FAIL — package doesn't exist yet

**Step 3: Implement the reconciler**

Create `backend/internal/reconciler/host.go`:

```go
package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// ReconcilerStore is the subset of store.Store the reconciler needs.
type ReconcilerStore interface {
	ListStaleHosts(ctx context.Context, threshold time.Duration) ([]store.Host, error)
	ListUnreachableHosts(ctx context.Context) ([]store.Host, error)
	UpdateHostStatus(ctx context.Context, id int, status string) error
	UpdateHostStatusMessage(ctx context.Context, id int, status, message string) error
	CreateHostEvent(ctx context.Context, event *store.HostEvent) error
	MarkMachinesOnHostError(ctx context.Context, hostID int, message string) ([]string, error)
	ListMachinesByHost(ctx context.Context, hostID int) ([]store.Machine, error)
}

// InstanceChecker checks whether a GCP instance exists.
type InstanceChecker interface {
	InstanceExists(ctx context.Context, project, zone, vmName string) (bool, error)
}

// RouteCleanup deletes KV routes for machines.
type RouteCleanup interface {
	DeleteRouteForMachine(ctx context.Context, machineID string) error
}

// TunnelCleanup deletes Cloudflare tunnels for machines.
type TunnelCleanup interface {
	DeleteTunnelForMachine(ctx context.Context, machineID string) error
}

// HostReconciler detects stale hosts and cleans up affected machines.
type HostReconciler struct {
	store          ReconcilerStore
	instanceCheck  InstanceChecker
	routeCleanup   RouteCleanup
	tunnelCleanup  TunnelCleanup
	gcpProject     string
	staleThreshold time.Duration
}

// New creates a new HostReconciler.
func New(
	s ReconcilerStore,
	ic InstanceChecker,
	rc RouteCleanup,
	tc TunnelCleanup,
	gcpProject string,
	staleThreshold time.Duration,
) *HostReconciler {
	return &HostReconciler{
		store:          s,
		instanceCheck:  ic,
		routeCleanup:   rc,
		tunnelCleanup:  tc,
		gcpProject:     gcpProject,
		staleThreshold: staleThreshold,
	}
}

// Start runs the reconciler loop until the context is cancelled.
func (r *HostReconciler) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	slog.Info("host.reconciler.started", "interval", interval, "stale_threshold", r.staleThreshold)

	for {
		select {
		case <-ctx.Done():
			slog.Info("host.reconciler.stopped")
			return
		case <-ticker.C:
			r.reconcileOnce(ctx)
		}
	}
}

func (r *HostReconciler) reconcileOnce(ctx context.Context) {
	// Phase 1: Mark stale ready hosts as unreachable
	staleHosts, err := r.store.ListStaleHosts(ctx, r.staleThreshold)
	if err != nil {
		slog.Error("host.reconciler.list_stale.failed", "error", err)
		return
	}

	for _, h := range staleHosts {
		slog.Warn("host.reconciler.unreachable",
			"host_id", h.ID, "vm_name", h.VMName,
			"last_heartbeat", h.LastHeartbeat)

		if err := r.store.UpdateHostStatus(ctx, h.ID, "unreachable"); err != nil {
			slog.Error("host.reconciler.mark_unreachable.failed", "host_id", h.ID, "error", err)
			continue
		}

		meta, _ := json.Marshal(map[string]string{"reason": "heartbeat_stale", "previous_status": h.Status})
		_ = r.store.CreateHostEvent(ctx, &store.HostEvent{
			EventType: "host.reconciler.unreachable",
			HostID:    h.ID,
			Metadata:  meta,
		})
	}

	// Phase 2: Check unreachable hosts against GCP
	unreachableHosts, err := r.store.ListUnreachableHosts(ctx)
	if err != nil {
		slog.Error("host.reconciler.list_unreachable.failed", "error", err)
		return
	}

	for _, h := range unreachableHosts {
		if r.instanceCheck == nil {
			continue
		}

		exists, err := r.instanceCheck.InstanceExists(ctx, r.gcpProject, h.Zone, h.VMName)
		if err != nil {
			slog.Error("host.reconciler.gcp_check.failed", "host_id", h.ID, "error", err)
			errMsg := "GCP check failed: " + err.Error()
			_ = r.store.UpdateHostStatusMessage(ctx, h.ID, "error", errMsg)
			continue
		}

		if exists {
			// Instance still running — stay unreachable, may recover via heartbeat
			continue
		}

		// Instance is gone — mark terminated and clean up machines
		slog.Warn("host.reconciler.terminated",
			"host_id", h.ID, "vm_name", h.VMName, "zone", h.Zone)

		if err := r.store.UpdateHostStatus(ctx, h.ID, "terminated"); err != nil {
			slog.Error("host.reconciler.mark_terminated.failed", "host_id", h.ID, "error", err)
			continue
		}

		meta, _ := json.Marshal(map[string]string{"reason": "instance_gone", "zone": h.Zone, "vm_name": h.VMName})
		_ = r.store.CreateHostEvent(ctx, &store.HostEvent{
			EventType: "host.reconciler.terminated",
			HostID:    h.ID,
			Metadata:  meta,
		})

		r.cleanupMachinesOnHost(ctx, h.ID)
	}
}

func (r *HostReconciler) cleanupMachinesOnHost(ctx context.Context, hostID int) {
	affectedIDs, err := r.store.MarkMachinesOnHostError(ctx, hostID, "host lost")
	if err != nil {
		slog.Error("host.reconciler.mark_machines_error.failed", "host_id", hostID, "error", err)
		return
	}

	if len(affectedIDs) == 0 {
		return
	}

	slog.Info("host.reconciler.machines_marked_error", "host_id", hostID, "count", len(affectedIDs))

	for _, machineID := range affectedIDs {
		if r.routeCleanup != nil {
			if err := r.routeCleanup.DeleteRouteForMachine(ctx, machineID); err != nil {
				slog.Error("host.reconciler.route_cleanup.failed", "machine_id", machineID, "error", err)
			}
		}

		if r.tunnelCleanup != nil {
			if err := r.tunnelCleanup.DeleteTunnelForMachine(ctx, machineID); err != nil {
				slog.Error("host.reconciler.tunnel_cleanup.failed", "machine_id", machineID, "error", err)
			}
		}
	}
}
```

**Step 4: Run tests**

Run: `cd backend && go test ./internal/reconciler/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add backend/internal/reconciler/
git commit -m "feat: add host reconciler for stale host detection and cleanup"
```

---

### Task 4: GCP Instance Checker

**Files:**
- Create: `backend/internal/reconciler/gcp.go`

**Step 1: Implement GCP instance checker**

```go
package reconciler

import (
	"context"
	"fmt"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/googleapi"
)

// GCPInstanceChecker checks whether GCE instances exist.
type GCPInstanceChecker struct {
	client *compute.InstancesClient
}

// NewGCPInstanceChecker creates a checker using the given Compute client.
func NewGCPInstanceChecker(client *compute.InstancesClient) *GCPInstanceChecker {
	return &GCPInstanceChecker{client: client}
}

// InstanceExists returns true if the instance exists in GCE, false if it's gone (404).
func (c *GCPInstanceChecker) InstanceExists(ctx context.Context, project, zone, vmName string) (bool, error) {
	_, err := c.client.Get(ctx, &computepb.GetInstanceRequest{
		Project:  project,
		Zone:     zone,
		Instance: vmName,
	})
	if err != nil {
		if gerr, ok := err.(*googleapi.Error); ok && gerr.Code == 404 {
			return false, nil
		}
		return false, fmt.Errorf("check instance %s/%s: %w", zone, vmName, err)
	}
	return true, nil
}
```

**Step 2: Run tests**

Run: `cd backend && go build ./internal/reconciler/`
Expected: compiles

**Step 3: Commit**

```bash
git add backend/internal/reconciler/gcp.go
git commit -m "feat: add GCP instance existence checker for reconciler"
```

---

### Task 5: Heartbeat Auto-Recovery

**Files:**
- Modify: `backend/internal/api/server.go:1863-1886` (handleAgentHeartbeat)

**Step 1: Add heartbeat recovery logic**

After the existing `UpdateHostHeartbeat` call (line 1863) and before the `ipChanged` check (line 1870), add:

```go
// Auto-recover unreachable hosts when heartbeat resumes
host, err := s.store.GetHost(ctx, hostID)
if err == nil && host.Status == "unreachable" {
	if err := s.store.UpdateHostStatus(ctx, hostID, "ready"); err != nil {
		slog.Error("heartbeat.recovery.failed", "host_id", hostID, "error", err)
	} else {
		slog.Info("host.reconciler.recovered", "host_id", hostID)
		meta, _ := json.Marshal(map[string]string{"reason": "heartbeat_resumed"})
		_ = s.store.CreateHostEvent(ctx, &store.HostEvent{
			EventType: "host.reconciler.recovered",
			HostID:    hostID,
			Metadata:  meta,
		})
	}
}
```

**Step 2: Run tests**

Run: `make test-go`
Expected: PASS

**Step 3: Commit**

```bash
git add backend/internal/api/server.go
git commit -m "feat: auto-recover unreachable hosts on heartbeat resume"
```

---

### Task 6: Dead-Host Restart Affinity Skip

**Files:**
- Modify: `backend/internal/api/server.go:917-931` (startMachineInternal)

**Step 1: Add host status check before re-allocation**

Replace the host affinity block (lines 917-931) with:

```go
if machine.HostID != nil && machine.VMIP != nil {
	// Check if the previous host is still viable for restart
	prevHost, err := s.store.GetHost(ctx, *machine.HostID)
	if err != nil || prevHost.Status == "unreachable" || prevHost.Status == "terminated" || prevHost.Status == "error" {
		// Host is dead or unreachable — clear affinity and do fresh placement
		slog.Info("machine.start.affinity_broken",
			"machine_id", machine.ID,
			"host_id", *machine.HostID,
			"host_status", func() string {
				if prevHost != nil {
					return prevHost.Status
				}
				return "unknown"
			}())
		_ = s.store.UnassignMachineFromHost(ctx, machine.ID)
		host, vmIP, err = s.scheduler.PlaceMachine(ctx, machine)
		if err != nil {
			return nil, "", err
		}
	} else {
		// Restart path: re-allocate on the same host where data volume lives
		host, err = s.scheduler.ReAllocateMachine(ctx, machine)
		if err != nil {
			return nil, "", fmt.Errorf("failed to re-allocate on host: %w", err)
		}
		vmIP = *machine.VMIP
		slog.Info("machine.start.host_affinity", "machine_id", machine.ID, "host_id", host.ID, "vm_ip", vmIP)
	}
} else {
	// Fresh placement: first-time start
	host, vmIP, err = s.scheduler.PlaceMachine(ctx, machine)
	if err != nil {
		return nil, "", err
	}
}
```

**Step 2: Run tests**

Run: `make test-go`
Expected: PASS

**Step 3: Commit**

```bash
git add backend/internal/api/server.go
git commit -m "feat: skip dead-host affinity on machine restart"
```

---

### Task 7: Shutdown Notification Endpoint

**Files:**
- Modify: `backend/internal/api/server.go` (add new handler + route)

**Step 1: Add shutdown-notify handler**

Add route in the agent API section (near line 268):
```go
r.Post("/api/agent/shutdown-notify", srv.handleAgentShutdownNotify)
```

Add handler:
```go
// handleAgentShutdownNotify receives a notification from an agent that it is shutting down.
func (s *Server) handleAgentShutdownNotify(w http.ResponseWriter, r *http.Request) {
	if s.agentToken == "" {
		writeError(w, http.StatusForbidden, "agent token not configured")
		return
	}
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.agentToken)) != 1 {
		writeError(w, http.StatusForbidden, "invalid agent token")
		return
	}

	var req struct {
		HostID json.Number `json:"host_id"`
		Reason string      `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	hostID, err := strconv.Atoi(strings.Trim(req.HostID.String(), `"`))
	if err != nil || hostID == 0 {
		writeError(w, http.StatusBadRequest, "host_id is required")
		return
	}

	ctx := r.Context()

	slog.Info("agent.shutdown_notify", "host_id", hostID, "reason", req.Reason)

	if err := s.store.UpdateHostStatus(ctx, hostID, "draining"); err != nil {
		slog.Error("agent.shutdown_notify.update_failed", "host_id", hostID, "error", err)
	}

	meta, _ := json.Marshal(map[string]string{"reason": req.Reason})
	_ = s.store.CreateHostEvent(ctx, &store.HostEvent{
		EventType: "host.agent_shutdown",
		HostID:    hostID,
		Metadata:  meta,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

**Step 2: Run tests**

Run: `make test-go`
Expected: PASS

**Step 3: Commit**

```bash
git add backend/internal/api/server.go
git commit -m "feat: add agent shutdown notification endpoint"
```

---

### Task 8: Graceful VM Shutdown in Agent

**Files:**
- Modify: `backend/internal/orchestrator/firecracker_linux.go:620-639` (Shutdown method)
- Modify: `backend/internal/orchestrator/firecracker_stub.go:62-64` (stub Shutdown)
- Modify: `backend/cmd/agent/main.go:275-311` (shutdown sequence)

**Step 1: Replace Shutdown with GracefulShutdown in orchestrator**

In `firecracker_linux.go`, replace the `Shutdown` method (line 620-639):

```go
func (o *firecrackerOrchestrator) Shutdown(ctx context.Context) error {
	o.mu.Lock()
	ids := make([]string, 0, len(o.vms))
	for id := range o.vms {
		ids = append(ids, id)
	}
	o.mu.Unlock()

	slog.Info("orchestrator.graceful_shutdown", "vm_count", len(ids))

	for _, id := range ids {
		// Try graceful Stop first (sends shutdown to Firecracker API)
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := o.Stop(stopCtx, id)
		cancel()

		if err != nil {
			slog.Warn("orchestrator.graceful_stop.failed",
				"machine_id", id, "error", err)
			// Fall back to hard Destroy
			if destroyErr := o.Destroy(context.Background(), id); destroyErr != nil {
				slog.Error("orchestrator.destroy.fallback.failed",
					"machine_id", id, "error", destroyErr)
			}
		}
	}

	return nil
}
```

**Step 2: Add shutdown notification to agent**

In `backend/cmd/agent/main.go`, replace lines 275-311 with:

```go
// 11. Wait for SIGINT/SIGTERM
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
sig := <-sigCh
slog.Info("agent.shutdown_start", "signal", sig.String(), "uptime_ms", time.Since(startTime).Milliseconds())

// 12. Notify control plane of shutdown
if cfg.BackendURL != "" && cfg.HostID != "" && cfg.AgentToken != "" {
	notifyShutdown(cfg)
}

// 13. Graceful shutdown in reverse order
cancel() // stop metadata server + self-update loop

// Close GCS client used by self-update
if updater != nil {
	updater.Close()
}

// Stop cloudflared
if cloudflaredStop != nil {
	cloudflaredStop()
}

// Shutdown API proxy
shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
defer shutdownCancel()
if err := proxy.Shutdown(shutdownCtx); err != nil {
	slog.Warn("apiproxy.shutdown_error", "error", err)
}

// Shutdown HTTP servers
controlServer.Close()
proxyServer.Close()

// Graceful shutdown all VMs (Stop with timeout, then Destroy fallback)
if err := orch.Shutdown(context.Background()); err != nil {
	slog.Error("orchestrator.shutdown_error", "error", err)
}

slog.Info("agent.shutdown_complete")
```

Add the `notifyShutdown` helper function:

```go
// notifyShutdown sends a shutdown notification to the control plane.
func notifyShutdown(cfg *config.AgentConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	payload, _ := json.Marshal(map[string]string{
		"host_id": cfg.HostID,
		"reason":  "agent_sigterm",
	})

	req, err := http.NewRequestWithContext(ctx, "POST", cfg.BackendURL+"/api/agent/shutdown-notify", bytes.NewReader(payload))
	if err != nil {
		slog.Warn("shutdown_notify.request_failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.AgentToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("shutdown_notify.send_failed", "error", err)
		return
	}
	defer resp.Body.Close()
	slog.Info("shutdown_notify.sent", "status", resp.StatusCode)
}
```

**Step 3: Run tests**

Run: `make test-go`
Expected: PASS

**Step 4: Commit**

```bash
git add backend/internal/orchestrator/firecracker_linux.go backend/internal/orchestrator/firecracker_stub.go backend/cmd/agent/main.go
git commit -m "feat: graceful VM shutdown on agent SIGTERM with control plane notification"
```

---

### Task 9: Phase-Aware Provisioning Failure Events

**Files:**
- Modify: `backend/internal/provisioner/provisioner.go:126-134` (failHost closure)

**Step 1: Make failHost phase-aware**

Replace the `failHost` closure (line 127-134) with:

```go
phase := "init"
failHost := func(err error) (*store.Host, error) {
	errMsg := err.Error()
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	statusMsg := fmt.Sprintf("provisioning failed at phase: %s (%s)", phase, errMsg)
	_ = p.store.UpdateHostStatusMessage(ctx, host.ID, "error", statusMsg)

	// If instance was already created, warn operators about orphaned resources
	if phase == "health_check" || phase == "get_instance" || phase == "wait_instance" {
		p.logHostEvent(ctx, host.ID, "provisioning_failed_instance_orphaned", map[string]string{
			"phase":   phase,
			"vm_name": host.VMName,
			"zone":    p.zone,
			"error":   errMsg,
		})
	}

	return host, err
}
```

Then update the phase variable at each step in `ProvisionHost`:

- Before tunnel creation: `phase = "tunnel"`
- Before GCE Insert: `phase = "gce_insert"`
- Before GCE Wait: `phase = "wait_instance"`
- Before GetInstance: `phase = "get_instance"`
- Before health check: `phase = "health_check"`

**Step 2: Run tests**

Run: `make test-go`
Expected: PASS

**Step 3: Commit**

```bash
git add backend/internal/provisioner/provisioner.go
git commit -m "feat: phase-aware provisioning failure with orphan instance events"
```

---

### Task 10: Wire Reconciler into Server Startup

**Files:**
- Modify: `backend/cmd/server/main.go` (add reconciler startup)

**Step 1: Wire reconciler**

After `srv.StartOAuthRefreshLoop(ctx)` (line 122), add:

```go
// Start host reconciler
if computeClient != nil {
	instanceChecker := reconciler.NewGCPInstanceChecker(computeClient)
	hostReconciler := reconciler.New(
		db, instanceChecker, nil, nil, // route and tunnel cleanup wired later
		cfg.GCPProject, 180*time.Second,
	)
	go hostReconciler.Start(ctx, 60*time.Second)
	slog.Info("host.reconciler.wired")
}
```

Add the import for the reconciler package.

Note: `computeClient` is the `*compute.InstancesClient` — check if it's already created in `main.go` or if the provisioner creates its own. If the provisioner creates its own, extract it so both can share.

**Step 2: Run tests**

Run: `make test-go`
Expected: PASS

**Step 3: Commit**

```bash
git add backend/cmd/server/main.go
git commit -m "feat: wire host reconciler into server startup"
```

---

### Task 11: Final Integration Verification

**Step 1: Run full test suite**

Run: `make test-go`
Expected: ALL PASS

**Step 2: Run gateway E2E tests**

Run: `make test-gateway-e2e`
Expected: ALL PASS (heartbeat filter doesn't break E2E because test hosts have fresh heartbeats)

**Step 3: Build check**

Run: `CGO_ENABLED=0 GOOS=linux go build ./backend/cmd/server/ && CGO_ENABLED=0 GOOS=linux go build ./backend/cmd/agent/`
Expected: Both compile

**Step 4: Commit and push**

```bash
git push
```

---

### Task 12: Update CurrentFeature.md

**Files:**
- Modify: `docs/CurrentFeature.md`

Update with a summary of P0 work completed:

```markdown
# Current Feature: thirdparty_provisioning

## P0 Safety Fixes (completed)

- Heartbeat-based placement filter (180s threshold)
- Host reconciler (60s loop, detects stale → unreachable → terminated)
- GCP instance existence check for dead host confirmation
- Bulk machine error marking on host death with route/tunnel cleanup
- Dead-host restart affinity skip (machines restart on new hosts)
- Heartbeat auto-recovery (unreachable → ready)
- Graceful VM shutdown on agent SIGTERM (Stop before Destroy)
- Shutdown notification to control plane
- Phase-aware provisioning failure events

## Next: Provider Abstraction (Phase 2)
```

**Step 1: Commit**

```bash
git add docs/CurrentFeature.md
git commit -m "docs: update CurrentFeature.md with P0 safety fixes"
git push
```
