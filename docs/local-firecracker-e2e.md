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
| `scripts/init-openclaw.sh` | In‑VM init no longer fatals on empty `tunnel_token` / `vm_hostname`; skips cloudflared supervision when there is no tunnel. Requires a rootfs rebuild to take effect. |

## Reaching a running workspace (OpenClaw gateway up)

A bare VM boots, but reaching `running` (auth proxy `:8080` + OpenClaw gateway `:7681`
ready) needs three more things — all because the runtime is **artifact‑only** and the
rootfs init assumes a hosted (Cloudflare) deployment:

1. **Enable the runtime resolver** so OpenClaw machines resolve a version from
   `artifact_releases`: set `FF_RUNTIME_VERSION_RESOLVER=1` on the control plane.
   Without it, `resolveRuntimeSelection` never runs for OpenClaw, so no runtime is
   staged and the VM init fatals with `[FATAL] Artifact runtime binary is unavailable`.

2. **Stage the OpenClaw runtime + register a release.** The agent attaches the runtime
   as a read‑only `/dev/vdc` drive (mounted at `/ocm-runtime`); it only does so when the
   release is staged at `OpenClawRuntimeDir/releases/<version>` with `bin/openclaw` +
   the bundled‑plugins dir, and a matching `artifact_releases` row exists. Locally:
   - extract a tarball from `/var/lib/ocm/openclaw-artifacts/openclaw-vX.Y.Z-…tar.zst`
     into `/var/lib/ocm/openclaw/releases/vX.Y.Z/`;
   - add a `manifest.json` there whose `runtime.bundled_plugins_relpath` points at
     `node_modules/openclaw/dist/extensions` (this artifact differs from the
     `dist/extensions` default), plus required `version`/`artifact_url`/`sha256`;
   - `insert into artifact_releases (kind,version,channel,url,sha256) values
     ('openclaw','vX.Y.Z','stable',…)`.

3. **In‑VM tunnel fatal.** The rootfs init (`scripts/init-openclaw.sh`) hard‑required a
   `tunnel_token`; fixed in this branch (see table above). It needs a `make build-rootfs`
   to land in the image, or — for an already‑staged box — patch the line in the staged
   base `/var/lib/ocm/vms/.base-rootfs.ext4` (mount rw) and delete the per‑VM clone
   `/var/lib/ocm/vms/<machine_id>.ext4` so it re‑clones.

With all three in place the VM boots to `[gateway] ready (N plugins)`, the machine goes
`running`, and the **Workspace** page streams live gateway logs + a shell. Note: clones
come from the **staged** base `/var/lib/ocm/vms/.base-rootfs.ext4` (created at agent
startup), not `/var/lib/ocm/images/rootfs.ext4`; refresh the staged copy after any rootfs
change.
