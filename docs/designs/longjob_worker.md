# Design: Long-Job Worker Fleet

**Status:** Partially Implemented
**Date:** 2026-03-13
**Scope:** GCE spot worker fleet for durable workflow execution + migration reliability fixes

## Implementation Status

| Component | Status | Notes |
|-----------|--------|-------|
| RUN_MODE split (API/worker) | Done | `main.go` — API, worker, and both-mode paths |
| Enqueue-only DBOS mode | Done | `workflows.NewService` supports `EnableEnqueue` without `EnableRuntime` |
| Worker startup script | Done | `scripts/worker-startup.sh` — systemd service, SHA256 verify |
| Worker binary upload | Done | `scripts/upload-worker-binary.sh` — GCS manifest pattern |
| Makefile targets | Done | `build-worker-binary`, `upload-worker-binary`, `deploy-fleet`, etc. |
| Preemption handler | Done | `watchForPreemption()` sends SIGTERM to signal channel |
| Health endpoint (worker) | Done | `/healthz` with DB ping, serves on worker port |
| Stall detection | Done | `StartStallDetectionLoop` — 5min interval, 10min threshold |
| `FindStalledWorkflows` store | Done | SQL query on `workflow_runs` + `workflow_events` |
| GCE instance template | TODO | `gcloud` command in doc — needs real SA email |
| MIG creation | TODO | `gcloud` command in doc — run after template |
| Migration reliability fixes | TODO | Fixes 1-3 deferred until migration workflows are tested |
| Secrets via Secret Manager | TODO | Currently uses instance metadata (acceptable for MVP) |

## Problem

The backend binary currently runs both HTTP API and DBOS workflow executor in a single process. When the control plane is deployed on a request-scoped host that throttles CPU between requests, DBOS background goroutines break. Without a dedicated worker process, workflows stall silently whenever there's no HTTP traffic.

Migration workflows also have three reliability bugs:
1. Reconciler expires migration operations after 10 minutes, but migrations can take 20 minutes
2. Migration is fire-and-forget from the HTTP handler — the browser has no way to poll status
3. Post-restore restart can hard-release machines on transient poll failures

## Solution

Deploy 2× `e2-small` spot instances in a GCE managed instance group (MIG). These run the same backend binary with `RUN_MODE=worker` (already implemented). The API on the control-plane host runs with `RUN_MODE=api` (already implemented). Both connect to the same PostgreSQL.

## Architecture

```
                    ┌──────────────────────┐
                    │ Control-plane host   │
                    │   RUN_MODE=api       │
                    │                      │
                    │   HTTP endpoints     │
                    │   Enqueue workflows  │
                    │   No DBOS executor   │
                    └──────────┬───────────┘
                               │
                               │  Enqueue via Postgres
                               │
                    ┌──────────▼───────────┐
                    │      PostgreSQL   │
                    │                      │
                    │  workflow_runs       │
                    │  dbos_operations     │
                    │  dbos_notifications  │
                    └──────────┬───────────┘
                               │
              ┌────────────────┼────────────────┐
              │                                 │
   ┌──────────▼───────────┐          ┌──────────▼───────────┐
   │  Spot Worker A       │          │  Spot Worker B       │
   │  e2-small            │          │  e2-small            │
   │  RUN_MODE=worker     │          │  RUN_MODE=worker     │
   │                      │          │                      │
   │  DBOS executor       │          │  DBOS executor       │
   │  Preemption handler  │          │  Preemption handler  │
   │  Health endpoint     │          │  Health endpoint     │
   └──────────────────────┘          └──────────────────────┘
```

Growth path: replace spot workers with on-demand instances when traffic justifies it.

## Components

### 1. GCE Instance Template

```bash
gcloud compute instance-templates create ocm-worker-v1 \
  --project=YOUR-GCP-PROJECT \
  --machine-type=e2-small \
  --provisioning-model=SPOT \
  --instance-termination-action=STOP \
  --maintenance-policy=TERMINATE \
  --boot-disk-size=10GB \
  --boot-disk-type=pd-standard \
  --image-family=ubuntu-2204-lts \
  --image-project=ubuntu-os-cloud \
  --scopes=storage-ro,logging-write,monitoring-write \
  --tags=ocm-worker \
  --labels=ocm=true,role=worker \
  --metadata-from-file=startup-script=scripts/worker-startup.sh \
  --service-account=<COMPUTE_SA>
```

Key differences from host VMs:
- **No nested virtualization** — workers don't run Firecracker, only DBOS
- **Small boot disk** (10GB) — no rootfs, no VM images
- **SPOT provisioning** — 60-91% cheaper, GCE can preempt with 30s warning
- **No data disk** — all state is in Postgres

### 2. Worker Startup Script (`scripts/worker-startup.sh`)

```bash
#!/bin/bash
set -euo pipefail

# Download backend binary from GCS (same artifact as the control-plane host)
MANIFEST=$(curl -sf -H "Metadata-Flavor: Google" \
  http://metadata.google.internal/computeMetadata/v1/instance/attributes/worker-manifest)

VERSION=$(echo "$MANIFEST" | jq -r .version)
URL=$(echo "$MANIFEST" | jq -r .url)
SHA=$(echo "$MANIFEST" | jq -r .sha256)

gsutil cp "$URL" /usr/local/bin/ocm-backend
echo "$SHA  /usr/local/bin/ocm-backend" | sha256sum -c -
chmod +x /usr/local/bin/ocm-backend

# Fetch secrets from metadata
DATABASE_URL=$(curl -sf -H "Metadata-Flavor: Google" \
  http://metadata.google.internal/computeMetadata/v1/instance/attributes/database-url)

# Instance identity for executor ID
INSTANCE_NAME=$(curl -sf -H "Metadata-Flavor: Google" \
  http://metadata.google.internal/computeMetadata/v1/instance/name)
ZONE=$(curl -sf -H "Metadata-Flavor: Google" \
  http://metadata.google.internal/computeMetadata/v1/instance/zone | awk -F/ '{print $NF}')

# Run as systemd service
cat > /etc/systemd/system/ocm-worker.service <<EOF
[Unit]
Description=OCM Workflow Worker
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/ocm-backend
Environment=RUN_MODE=worker
Environment=DATABASE_URL=${DATABASE_URL}
Environment=EXECUTOR_ID=${INSTANCE_NAME}-${ZONE}
Environment=ENABLE_DURABLE_WORKFLOWS=1
Environment=PORT=8080
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now ocm-worker
```

### 3. Managed Instance Group

```bash
# Health check — worker exposes /healthz on port 8080
gcloud compute health-checks create http ocm-worker-health \
  --project=YOUR-GCP-PROJECT \
  --port=8080 \
  --request-path=/healthz \
  --check-interval=30s \
  --timeout=10s \
  --healthy-threshold=1 \
  --unhealthy-threshold=3

# Managed instance group — 2 instances, auto-healing
gcloud compute instance-groups managed create ocm-workers \
  --project=YOUR-GCP-PROJECT \
  --zone=us-central1-b \
  --template=ocm-worker-v1 \
  --size=2 \
  --health-check=ocm-worker-health \
  --initial-delay=60
```

Auto-healing: if a worker fails the health check 3 times (90s total), MIG recreates it. New worker starts, DBOS scans for orphaned workflows, resumes them.

### 4. Preemption Signal Handler (Go code)

GCE spot instances get a 30-second warning before preemption via the metadata server. The worker must listen for this and gracefully shut down DBOS.

**File:** `backend/cmd/server/main.go` (worker mode block)

```go
// In the worker mode block, after DBOS starts:
if isWorker && !isAPI {
    // Listen for GCE preemption signal
    go watchForPreemption(cancel)

    // Expose /healthz for MIG health checks
    healthMux := http.NewServeMux()
    healthMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("ok"))
    })
    healthServer := &http.Server{Addr: ":" + cfg.Port, Handler: healthMux}
    go healthServer.ListenAndServe()

    // Block until shutdown signal
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    <-sigCh
    slog.Info("worker.shutdown_start")
    cancel()
    healthServer.Shutdown(context.Background())
}
```

```go
// watchForPreemption polls the GCE metadata server for the preemption signal.
// When preemption is detected, it cancels the context, triggering graceful DBOS shutdown.
func watchForPreemption(cancel context.CancelFunc) {
    // GCE signals preemption via a metadata endpoint that blocks until
    // preemption is scheduled, then returns "TRUE".
    // See: https://cloud.google.com/compute/docs/instances/preemptible#detecting
    client := &http.Client{Timeout: 0} // no timeout — long-poll
    for {
        req, _ := http.NewRequest("GET",
            "http://metadata.google.internal/computeMetadata/v1/instance/preempted?wait_for_change=true",
            nil)
        req.Header.Set("Metadata-Flavor", "Google")

        resp, err := client.Do(req)
        if err != nil {
            // Not on GCE, or metadata server unreachable — stop polling
            slog.Debug("preemption.watch_disabled", "error", err)
            return
        }
        body, _ := io.ReadAll(resp.Body)
        resp.Body.Close()

        if strings.TrimSpace(string(body)) == "TRUE" {
            slog.Warn("preemption.detected", "action", "graceful_shutdown")
            cancel() // triggers dbos.Shutdown via deferred Close()
            return
        }
    }
}
```

The `defer workflowSvc.Close(10 * time.Second)` already in main.go will call `dbos.Shutdown()` when the context is cancelled, giving in-flight steps up to 10 seconds to checkpoint.

### 5. Worker Health Endpoint

Workers need a minimal HTTP endpoint for the MIG health check. This is NOT the full API server — just `/healthz`.

The current code already handles this: when `isWorker && !isAPI`, we add a lightweight health server (see above). When `RUN_MODE=""` (both modes), the existing API server already has health endpoints.

### 6. Worker Binary Distribution

Two options:

**Option A: GCS manifest (like agent self-update)**
- Upload backend binary to GCS after each build
- Workers download on startup from manifest URL in instance metadata
- Pro: same pattern as agent, simple
- Con: requires a new upload step in CI

**Option B: Container image (like the control-plane host)**
- Build a Docker image with the backend binary
- Use Container-Optimized OS on the instance template
- Pro: same artifact as the control-plane host, simpler deploy
- Con: need to set up container startup on GCE

**Recommendation:** Option A (GCS manifest). The agent already uses this exact pattern. Add a `make upload-backend` target that uploads the backend binary to `gs://YOUR-ARTIFACT-BUCKET/backend/` with a manifest.

### 7. Deploy Workflow

```
Deploy sequence:
  1. make deploy-backend          (control-plane host — API mode)
  2. make upload-worker-binary    (GCS — same binary)
  3. gcloud compute instance-groups managed rolling-action restart ocm-workers \
       --zone=us-central1-b
  4. Workers restart, download new binary, DBOS resumes orphaned workflows
```

No drain needed because `ApplicationVersion` is pinned to `"v1"`. Old and new workers run the same DBOS version. The rolling restart stops one worker at a time, so the other keeps executing workflows.

## Migration Reliability Fixes

### Fix 1: Operation expiry timeout (Bug #7)

The reconciler expires machine operations after 10 minutes (`staleOperationThreshold`). Migration workflows can take up to 20 minutes (backup + restore + DNS).

**Fix:** The migration workflow already uses DBOS workflow locks for exclusivity. The `MachineOperation` is a legacy mechanism that predates workflows. Instead of raising the timeout, make the migration workflow refresh the operation timestamp periodically.

**File:** `backend/internal/api/admin_migrate_workflow.go`

Add a heartbeat step between long-running steps:

```go
// Between backup and restore steps:
_, _ = dbos.RunAsStep(ctx, func(stepCtx context.Context) (bool, error) {
    return true, s.store.TouchMachineOperation(stepCtx, input.OperationID)
}, dbos.WithStepName("migration.heartbeat"))
```

**File:** `backend/internal/store/postgres.go`

```go
func (s *PostgresStore) TouchMachineOperation(ctx context.Context, opID int) error {
    _, err := s.pool.Exec(ctx,
        `UPDATE machine_operations SET updated_at = now() WHERE id = $1`, opID)
    return err
}
```

This keeps the operation alive without changing the reconciler's threshold, which protects against truly stale operations.

### Fix 2: Migration status polling (Bug #8)

The migration handler returns 202 with the operation, but the frontend has no way to poll progress. The workflow projection already tracks this.

**Already implemented:** `GET /api/workflows/{id}` and `GET /api/workflows/{id}/events` exist. The frontend admin page needs to poll these endpoints for real-time migration status. This is a frontend task, not a backend change.

### Fix 3: Post-restore poller resilience (Bug #9)

After restore, `pollVMStatus` can mark the machine as errored on transient failures (network timeout, temporary KV sync failure), releasing placement and tunnel state even though the VM is healthy.

**Fix:** Add a `migrationRestore` flag to the poll context. When set, transient errors retry instead of hard-releasing:

**File:** `backend/internal/machines/runtime.go`

In the `pollVMStatus` error handler, check if this is a migration-context poll:
```go
if isMigrationRestore(ctx) && isTransientError(err) {
    slog.Warn("poll.transient_error_during_migration", "machine", machineID, "error", err)
    continue // retry instead of releasing
}
```

This is a targeted fix — the poller stays destructive for normal starts (where a failed start should release resources), but becomes resilient during migration restores.

## Stall Detection

A background goroutine in the API server (not the worker) that alerts on stuck workflows.

**File:** `backend/internal/api/server.go`

```go
func (s *Server) StartStallDetectionLoop(ctx context.Context) {
    go func() {
        ticker := time.NewTicker(5 * time.Minute)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                stalled, err := s.store.FindStalledWorkflows(ctx, 10*time.Minute)
                if err != nil {
                    slog.Error("stall_detection.failed", "error", err)
                    continue
                }
                for _, wf := range stalled {
                    slog.Warn("stall_detection.stalled_workflow",
                        "workflow_id", wf.ID,
                        "kind", wf.Kind,
                        "last_event_at", wf.LastEventAt,
                        "stuck_minutes", time.Since(wf.LastEventAt).Minutes(),
                    )
                }
            }
        }
    }()
}
```

**SQL:**
```sql
SELECT wr.id, wr.kind, wr.scope_type, wr.scope_id,
       COALESCE(MAX(we.created_at), wr.created_at) AS last_event_at
FROM workflow_runs wr
LEFT JOIN workflow_events we ON we.workflow_id = wr.id
WHERE wr.status IN ('running', 'waiting')
GROUP BY wr.id
HAVING COALESCE(MAX(we.created_at), wr.created_at) < now() - $1::interval
```

Start with logging. Add Slack/PagerDuty alerting later when we have an alerting pipeline.

## Makefile Targets

```makefile
# Worker binary upload (same binary as the control-plane host, different delivery)
upload-worker-binary:
	@echo "Uploading worker binary to GCS..."
	scripts/upload-worker-binary.sh

# Create/update instance template
create-worker-template:
	gcloud compute instance-templates create ocm-worker-$(VERSION) ...

# Rolling restart workers
restart-workers:
	gcloud compute instance-groups managed rolling-action restart ocm-workers \
	  --project=$(GCP_PROJECT) --zone=$(GCP_ZONE)

# Full worker deploy
deploy-workers: upload-worker-binary restart-workers
```

## Rollout Plan

### Step 1: Preemption handler + health endpoint (code)
- Add `watchForPreemption()` to main.go
- Add `/healthz` endpoint for worker mode
- Add `TouchMachineOperation` store method
- Add heartbeat step to migration workflow
- Add stall detection loop

### Step 2: Infrastructure setup (gcloud commands)
- Create `scripts/worker-startup.sh`
- Create `scripts/upload-worker-binary.sh`
- Create instance template
- Create health check
- Create managed instance group (size=0 initially)

### Step 3: Smoke test
- Set MIG size to 1
- Verify worker starts, connects to Postgres, DBOS executor runs
- Create a test invitation — verify email workflow executes on worker
- Kill the worker — verify MIG recreates it and workflow resumes

### Step 4: Production
- Set MIG size to 2
- Set `RUN_MODE=api` on the control-plane host (already done in deploy script)
- Monitor for stalled workflows

## Risks

1. **Spot availability** — `e2-small` in `us-central1` has high availability, but during capacity crunches both workers could be preempted simultaneously. MIG auto-heals within 60-90 seconds. Workflows pause but don't lose data.

2. **Database connection limits** — PostgreSQL free tier has connection limits. Two workers add 2 connections. Monitor and upgrade if needed.

3. **Binary version drift** — Workers download binary on startup. If a deploy happens mid-day, workers run the old version until restarted. The `ApplicationVersion` pin makes this safe for DBOS, but API behavioral changes need a rolling restart.

4. **Secrets in instance metadata** — `DATABASE_URL` is passed via instance metadata, which is readable by anyone with SSH access to the VM. For production hardening, use GCP Secret Manager with workload identity instead.

## Code Review Findings (2026-03-13)

### CRITICAL — Must fix before implementation

**C1. API mode cannot enqueue without local DBOS runtime.** ✅ FIXED
`dbos.RunWorkflow()` requires `s.workflows.Context()` which is nil when `EnableRuntime: false` (API mode). The migration handler falls back to the old synchronous path when runtime is disabled. The control-plane host in API mode currently cannot enqueue workflows.

**Fix (implemented):** `workflows.NewService` now supports `EnableEnqueue: true` (creates DBOS context, registers workflows) separate from `EnableRuntime: true` (starts executor). API mode sets `EnableEnqueue: true, EnableRuntime: false`. Worker mode sets both true.

**C2. Worker binary requires too many dependencies.** ✅ FIXED
`main.go` builds auth, tunnel, KV, secret-management, machine runtime, and API server before branching on `RUN_MODE`. Worker mode doesn't need any of these — it only needs Postgres + DBOS. The startup script also uses `jq`/`gsutil` without installing them.

**Fix (implemented):** `main.go` restructured — worker-only path (`isWorker && !isAPI`) initializes only the infrastructure needed for workflows (placement, agent client, tunnel, KV, machines runtime) and skips auth, provisioner, API server. Startup script installs `jq` via `apt-get`.

### IMPORTANT — Should fix

**I1. Preemption handler doesn't trigger DBOS shutdown.** ✅ FIXED
`watchForPreemption(cancel)` only calls `cancel()`, but the worker block waits on `<-sigCh` before `main` returns. `workflowSvc.Close()` is deferred and only runs on function return.

**Fix (implemented):** `watchForPreemption` now takes `chan<- os.Signal` and sends `SIGTERM` to the signal channel, unblocking `main` so deferred cleanup runs.

**I2. Fix 3 targets the wrong code path.**
Migration uses `StartWithOperation(..., SkipPoll: true)` then `waitForMachineRunningOnHost()`, not `pollVMStatus`. The destructive transient-error behavior is in `waitForMachineRunningOnHost`.

**Fix:** Apply the resilience fix to `waitForMachineRunningOnHost` instead of `pollVMStatus`.

**I3. Health check too weak.** ✅ FIXED
`/healthz` always returns 200 if the HTTP server is alive. Won't catch a wedged DBOS executor or broken Postgres session.

**Fix (implemented):** Worker `/healthz` now pings Postgres via `db.Ping(r.Context())`, returning 503 on failure.

### MINOR

**M1. Fix 1 is outdated.** Reconciler already uses 30 minutes (not 10). `TouchMachineOperation` references nonexistent `updated_at` column and wrong ID type. Bug #7 may already be resolved — verify before implementing.

**M2. Stall detection calls `FindStalledWorkflows` which doesn't exist.** ✅ FIXED
Store method `FindStalledWorkflows` implemented in `postgres.go`, `StalledWorkflow` struct added to `store.go`, `WorkflowRepo` added to `Store` interface.
