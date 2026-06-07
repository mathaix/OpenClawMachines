# Local Firecracker E2E — provisioning a real microVM through the browser

This documents how to stand up the **entire** OpenClaw stack on a single KVM box —
control plane **and** a local Firecracker worker — and provision a **real**
Firecracker microVM by clicking through the web UI.

## How this was actually run (script vs. agent)

This procedure was **not** originally a script. It was produced by an interactive,
adaptive debugging session: bring up a component → hit a failure → diagnose → patch
→ continue. Roughly:

- **1 read‑only mapping pass** (parallel exploration of backend / frontend / worker / UI).
- **~40 shell commands** for bring‑up, host registration, and log/DB inspection.
- **5 code fixes** discovered reactively (a nil‑pointer panic, three `503`s, a `400`,
  two frontend white‑screens) — see [Code fixes](#code-fixes-required) below.
- **~15 browser actions** driven live via Playwright (login → create → start → watch).

The deterministic ~90% (everything except the live debugging) is now captured in
[`scripts/local-e2e-firecracker.sh`](../scripts/local-e2e-firecracker.sh). The browser
step is **not** in that script — it is the Playwright layer
([`frontend/e2e/machine-lifecycle.spec.ts`](../frontend/e2e/machine-lifecycle.spec.ts)),
or a manual/agent‑driven browser session.

## Architecture for a single box

```
 browser ──▶ frontend :5173 ──▶ control plane :8080 (RUN_MODE=api, profile=local)
                                      │  schedules onto a registered host
                                      ▼
                              ocm-agent  :9090 control / :9091 proxy   (RUN_MODE=worker)
                                      │  VM_RUNTIME_OWNER=direct
                                      ▼
                              firecracker microVM on bridge ocm-br0 (192.168.100.0/24)
```

The control plane (`api`) and the worker (`ocm-agent`) are **separate processes** that
happen to run on the same box. The control plane never launches Firecracker itself; it
calls the agent's control API (`:9090`) to create the VM. No Cloudflare tunnel is needed
locally — the workspace is reached through the agent proxy (`:9091`).

## Prerequisites

- `/dev/kvm`, nested virt, `firecracker` in `PATH`.
- VM assets present: `/var/lib/ocm/images/{rootfs.ext4,vmlinux}`.
- XFS reflink mount at `/var/lib/ocm/vms` (`scripts/test-xfs-setup.sh` or `provision-host.sh`).
- Docker (Postgres), Go, Node, **passwordless sudo** (agent sets up bridge/NAT/KVM).
- The [code fixes](#code-fixes-required) applied (they make the `local` profile actually work).

## Quick start

```bash
scripts/local-e2e-firecracker.sh up        # stack up, host registered, agent running, host=ready
# then provision a VM by driving the browser:
cd frontend && PLAYWRIGHT_BASE_URL=http://localhost:5173 npx playwright test e2e/machine-lifecycle.spec.ts
# or manually at http://localhost:5173/dashboard : New Machine -> Basic / region External -> Create -> Start
scripts/local-e2e-firecracker.sh status    # component health + host/machine status
scripts/local-e2e-firecracker.sh down      # stop agent + backend + frontend
```

## What the script does (the manual steps, distilled)

1. **Env**: `scripts/local-dev.sh env`, then append `OCM_ADMIN_EMAILS=dev@localhost`
   (+`OCM_SUPERUSER_EMAILS`). Backend admin is gated by these env vars (`api/server.go`),
   **not** the `VITE_OCM_ADMIN_EMAILS` used by the frontend.
2. **Postgres + migrations + control plane** on `:8080` (`scripts/local-dev.sh backend`).
   Profile `local`, `AUTH_MODE=dev` → every request auto‑authenticates as `dev@localhost`
   (no login form).
3. **Frontend** on `:5173` (`scripts/local-dev.sh frontend`).
4. **Build the agent**: `go build -o … ./cmd/agent`.
5. **Mint an enrollment token**: `POST /api/admin/hosts/enrollment-tokens`.
6. **Register this box as a host**: `POST /api/agent/register` with
   `agent_endpoint=http://127.0.0.1:9090` and `capabilities {cpu_threads, memory_mb, disk_mb}`.
   Returns `host_id` + a per‑host `agent_token`.
7. **Run the worker**:
   ```bash
   sudo env FC_AGENT_TOKEN=<per-host token> HOST_ID=<id> \
        BACKEND_URL=http://localhost:8080 AGENT_ENDPOINT=http://127.0.0.1:9090 \
        VM_RUNTIME_OWNER=direct CONTROL_ALLOWED_CIDRS=0.0.0.0/0 ./ocm-agent
   ```
   The agent creates `ocm-br0`, NAT, metadata server (`192.168.100.1:80`),
   apiproxy (`:4000`), control (`:9090`), proxy (`:9091`); stages the base rootfs
   (~30s first time); then its **first heartbeat auto‑promotes the host
   `provisioning → ready`** so the scheduler can place machines on it.

Placement eligibility (`PlaceMachineOnHost`): `status='ready'` AND fresh heartbeat
(<180s) AND enough capacity AND, if set, matching `region`/`source_image`. The UI's
region selector defaults to **External**, which matches the registered host.

## The browser step (agent‑ or test‑driven)

From a logged‑in dashboard: **New Machine → Basic, region External → Create → Start**.
Start triggers: scheduler reserves the host + a VM IP (`192.168.100.10`), control plane
calls the agent's `POST /vms`, and the agent boots a real Firecracker microVM:

```
vm.create_start → allocating → rootfs → network → booting → booted
→ vm.started pid=… vm_ip=192.168.100.10 → machine_ready
```

This is exactly what `frontend/e2e/machine-lifecycle.spec.ts` automates.

## Code fixes required

The `local`/self‑hosted profile had several hosted‑only assumptions that block VM start.
These are on `issue-7-self-hosted-portability`:

| File | Fix |
|------|-----|
| `backend/cmd/server/main.go` | Don't box a nil `*tunnel.Manager` into the routing interface (typed‑nil) — it made machine start **panic** with no Cloudflare. Guard `if tunnelMgr != nil` like `NewServer` does. |
| `backend/internal/routing/service.go` | Generate the per‑VM signing key **before** the no‑tunnel early return (the agent requires `signing_key` for every VM). |
| `backend/internal/machines/runtime.go` | Set `machine.SigningKey` independently of whether a tunnel exists. |
| `backend/internal/agentapi/handlers.go` | `validateVMRequest` no longer hard‑requires `tunnel_token` / `vm_hostname` (hosted‑only; the VM is reached via the agent proxy). |
| `frontend/src/pages/MachineView.tsx` | Null‑guard the release lists (`openclawReleases?.[0]`, `!list ||`) — empty `artifact_releases` returned `null` and white‑screened the page. |

## Known gap — reaching `running`

The microVM **boots**, but the in‑VM OpenClaw gateway does not come up:
`"Auth proxy did not start — gateway unreachable"`, then
`vm.health.failed: timeout waiting for 192.168.100.10:7681 after 5m` → the machine
reverts to `stopped`. Cause: the **OpenClaw runtime is never staged** —
`/var/lib/ocm/openclaw/` is empty and `artifact_releases` is empty
(`runtime_source=artifact` resolves nothing), even though the runtime tarballs exist at
`/var/lib/ocm/openclaw-artifacts/`. This is the host/agent path working correctly and the
**artifact/gateway layer** being the remaining work (the integration suite boots working
OpenClaw VMs via the orchestrator directly). To reach `running`: register a local
`artifact_release` (or set `OPENCLAW_GCS_MANIFEST` / pre‑stage `/var/lib/ocm/openclaw`),
and resolve why the in‑VM auth proxy needs CF certs.
