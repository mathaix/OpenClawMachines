# Getting Started

This guide is in **three stages, each ending with something working**:

| Stage | What you build | What you need |
|---|---|---|
| [1 — Local evaluation](#stage-1--local-evaluation) | The full stack + a **real Firecracker machine** on one KVM box | One KVM-capable Linux box. No Cloudflare, no cloud account. |
| [2 — Cloudflare + a dedicated host](#stage-2--cloudflare--a-dedicated-host) | An operator-profile control plane with an enrolled cloud/bare-metal host; machines reachable at their own subdomains | A Cloudflare account + domain, and a KVM-capable host (GCP n2 worked example) |
| [3 — The full workflow](#stage-3--the-full-workflow) | Day-to-day use: chat, terminal, browser VMs, lifecycle, backups, runtime upgrades | A running stack from stage 1 or 2 |

Stages are independent enough that you can stop after stage 1 if you only want
to evaluate, and stage 2 doesn't require having done stage 1.

---

## Stage 1 — Local evaluation

**Goal:** the entire stack — control plane, frontend, and a Firecracker worker —
on a single KVM box, and a real microVM provisioned by clicking through the web
UI. No Cloudflare: the workspace is reached through the local agent proxy.

```
 browser ──▶ frontend :5173 ──▶ control plane :8080 (profile=local, AUTH_MODE=dev)
                                       │  schedules onto the registered host
                                       ▼
                               ocm-agent :9090/:9091 ──▶ Firecracker microVM
                                                          (bridge 192.168.100.0/24)
```

### Prerequisites

- A **Linux box with KVM** (`/dev/kvm`; bare metal or nested-virt-enabled VM —
  no macOS/WSL), `firecracker` in `PATH`, passwordless sudo.
- VM assets at `/var/lib/ocm/images/{rootfs.ext4,vmlinux}` and an XFS reflink
  mount at `/var/lib/ocm/vms` (`provision-host.sh` sets both up; see
  [local-setup.md](local-setup.md) for building a rootfs with `make build-rootfs`).
- Go ≥ 1.25.10, Node ≥ 20, Docker (for local Postgres).

Check the box first:

```bash
git clone https://github.com/mathaix/OpenClawMachines.git
cd OpenClawMachines
make preflight
```

### Bring it up

One script stands up Postgres, migrations, the control plane (`local` profile,
dev auth — you're auto-logged-in as `dev@localhost`), the frontend, and then
builds, registers, and starts a local Firecracker worker whose first heartbeat
flips the host to `ready`:

```bash
scripts/local-e2e-firecracker.sh up
scripts/local-e2e-firecracker.sh status   # component health + host/machine status
```

### Provision a machine

Open `http://localhost:5173/dashboard` → **New Machine → Basic, region
External → Create → Start**. The control plane places the machine on your local
host and the agent boots a real Firecracker microVM
(`allocating → rootfs → network → booting → booted`). Or drive the same flow
headlessly:

```bash
cd frontend && PLAYWRIGHT_BASE_URL=http://localhost:5173 \
  npx playwright test e2e/machine-lifecycle.spec.ts
```

**You now have a hardware-isolated microVM running on your own box.** To take
the VM all the way to a `running` OpenClaw **workspace** (gateway + terminal
inside the VM), the runtime artifact must be staged and the version resolver
enabled (`FF_RUNTIME_VERSION_RESOLVER=1`) — the exact steps, including
registering an `artifact_releases` row, are in
[local-firecracker-e2e.md](local-firecracker-e2e.md), which also documents
everything `local-e2e-firecracker.sh` does under the hood.

Tear down with `scripts/local-e2e-firecracker.sh down`.

---

## Stage 2 — Cloudflare + a dedicated host

**Goal:** the production-shaped deployment — an `operator`-profile control
plane, the Cloudflare data plane (edge auth + per-VM tunnels), and a dedicated
KVM host enrolled into your fleet. Machines become reachable at
`m-<name>.your-domain.com` from anywhere.

It uses **one host-onboarding path that is the same on every provider** (GCP,
AWS, Hetzner, bare metal): bring a KVM-capable Linux box, install dependencies,
**enroll** it. We use a **GCP n2** instance as the worked example because n2
supports nested virtualization out of the box; only the "create a VM" command
changes per provider. (One-click auto-provisioning for every cloud is tracked in
[#11](https://github.com/mathaix/OpenClawMachines/issues/11).)

### 2.1 Create a Cloudflare account and zone

The data plane uses Cloudflare for edge auth and a **per-VM tunnel** so each
machine is reachable at its own subdomain without opening inbound ports on your
host. You need:

1. A **Cloudflare account** and a **domain (zone)** you control (your
   `DATA_PLANE_DOMAIN`, e.g. `example.com`). Machines get `m-<slug>.example.com`.
2. An **API token** with permissions to manage that zone's DNS + Tunnels.
3. Your **Account ID** and **Zone ID** (Cloudflare dashboard → the domain's
   overview page).
4. A **Workers KV namespace** for subdomain → host/VM route lookups:
   ```bash
   cd worker && npx wrangler kv namespace create OCM_ROUTES
   ```
5. The **Worker** deployed (it terminates browser/terminal/gateway traffic at
   the edge and looks up routes in KV):
   ```bash
   cd worker && npx wrangler deploy
   ```

See [self-hosted-control-plane.md](self-hosted-control-plane.md) for the full
Cloudflare + auth prerequisites and the architecture behind this.

### 2.2 Configure the control plane

Copy the operator template and fill it in:

```bash
cp docs/self-hosted.env.example .env
```

The variables that must be set for the host-enrollment + machine flow:

| Variable | What it is |
|---|---|
| `CONTROL_PLANE_PROFILE` | `operator` |
| `DATABASE_URL` | Postgres connection string |
| `BACKEND_URL` | **Public HTTPS URL** of the control plane — the host must reach this |
| `DATA_PLANE_DOMAIN` | Your Cloudflare zone, e.g. `example.com` |
| `AUTH_MODE` | `firebase` or `cfaccess` (or `dev` + `OCM_ALLOW_DEV_AUTH=1` for evaluation) |
| `SECRET_ENCRYPTION_KEY` | Exactly 32 bytes — `openssl rand -hex 16` |
| `FC_AGENT_TOKEN` | Shared control-plane ↔ agent token — `openssl rand -hex 32` |
| `CLOUDFLARE_API_TOKEN` / `CLOUDFLARE_ACCOUNT_ID` / `CLOUDFLARE_ZONE_ID` | from 2.1 |
| `CLOUDFLARE_KV_NAMESPACE_ID` | the `OCM_ROUTES` namespace id from 2.1 |
| `OCM_ADMIN_EMAILS` | comma-separated admin emails (who can manage hosts) |
| `OCM_ARTIFACT_BUCKET` | where hosts pull the agent + kernel (see [Artifacts](#artifacts)) |

Frontend env (`frontend/.env.local`) must include `VITE_OCM_ADMIN_EMAILS` with
the **same** admin email(s) as `OCM_ADMIN_EMAILS`, or the admin UI won't show.

> Admin gating is two layers: the **backend** checks `OCM_SUPERUSER_EMAILS` /
> `OCM_ADMIN_EMAILS` (the real gate), and the **frontend** checks
> `VITE_OCM_ADMIN_EMAILS` (UI visibility). Keep them in sync.

See [control-plane-profiles.md](control-plane-profiles.md) for every variable
and per-profile defaults.

### 2.3 Run the backend + frontend

For evaluation, the local helper runs Postgres (Docker), migrations, and the
server:

```bash
make local-postgres   # start Dockerized Postgres
make local-migrate    # run schema migrations
make backend          # go run ./cmd/server  (:8080)
make frontend         # Vite dev server       (:5173, proxies /api -> :8080)
```

For a real deployment, build and run the server binary on your control-plane
host behind HTTPS so `BACKEND_URL` is publicly reachable:

```bash
make build-server     # produces ./backend/server
./backend/server      # reads .env
```

Confirm it's up: `make status` (checks `:8080/health`).

### 2.4 Add a host (GCP n2) and enroll it

Create a KVM-capable VM, install dependencies with `provision-host.sh`, then
enroll it.

**Create a GCP n2 instance (nested virtualization on):**

```bash
gcloud compute instances create ocm-host-1 \
  --project=YOUR_PROJECT \
  --zone=us-central1-b \
  --machine-type=n2-standard-4 \
  --enable-nested-virtualization \
  --image-family=ubuntu-2404-lts-amd64 \
  --image-project=ubuntu-os-cloud \
  --boot-disk-size=100GB \
  --boot-disk-type=pd-ssd

# Allow the agent control port (the per-VM tunnel is outbound, but the control
# plane health-checks the agent on :9090)
gcloud compute firewall-rules create ocm-agent-9090 \
  --allow=tcp:9090 --target-tags=ocm-agent --network=default
gcloud compute instances add-tags ocm-host-1 --tags=ocm-agent --zone=us-central1-b
```

> **AWS / Hetzner:** create a nested-virt-capable instance instead (AWS
> `*.metal`, Hetzner dedicated/CCX with KVM) running Ubuntu 24.04. The rest of
> this stage is identical.

**Install host dependencies** (KVM + nested virt, Firecracker, cloudflared, the
guest kernel, XFS/reflink state storage, sysctl tuning):

```bash
gcloud compute scp scripts/provision-host.sh ocm-host-1:~ --zone=us-central1-b
gcloud compute ssh ocm-host-1 --zone=us-central1-b --command \
  'sudo OCM_ARTIFACT_BUCKET=gs://YOUR-ARTIFACT-BUCKET bash ~/provision-host.sh'
```

`OCM_ARTIFACT_BUCKET` is where the kernel (`/vmlinux`) is pulled from. See
[Artifacts](#artifacts) to build your own or use the public default.

**Create an enrollment token and enroll.** In the UI: **Admin → Hosts → Enroll
Host**, or via the API:

```bash
curl -sS -X POST "$BACKEND_URL/api/admin/hosts/enrollment-tokens" \
  -H "Authorization: Bearer <your-session>" \
  -H "Content-Type: application/json" \
  -d '{"provider":"gcp","provider_class":"cloud","expires_in_hours":1}'
# -> { "token": "...", "install_command": "curl -sL .../api/agent/install | bash -s -- <token>" }
```

Run the returned install command on the host:

```bash
gcloud compute ssh ocm-host-1 --zone=us-central1-b --command \
  'curl -sL '"$BACKEND_URL"'/api/agent/install | sudo bash -s -- <TOKEN>'
```

The install script registers the host (`/api/agent/register`), which creates the
per-host Cloudflare tunnel, mints the agent token, downloads + verifies the
`ocm-agent` binary, installs the `ocm-agent` systemd unit, and starts it.

**Verify.** The host should appear in **Admin → Hosts** as `ready` within ~60s.
On the host:

```bash
gcloud compute ssh ocm-host-1 --zone=us-central1-b --command \
  'systemctl status ocm-agent --no-pager; journalctl -u ocm-agent -n 30 --no-pager'
```

**You now have an enrolled fleet host.** Log in to the frontend, create a
machine, and it boots on that host — reachable at `m-<name>.your-domain.com`
through Cloudflare Access + the per-VM tunnel.

---

## Stage 3 — The full workflow

**Goal:** use the platform day to day. Everything here works against a stack
from stage 1 (local URLs) or stage 2 (your domain).

### Create and use machines

1. Open the dashboard and log in. The first login auto-creates your user + a
   personal account. (In `dev` auth mode you're logged in as `DEV_USER_EMAIL`
   automatically.)
2. **New Machine** → pick a name and size → create. With auto-start, the control
   plane places the machine on a `ready` host and boots the microVM.
3. Open the machine's page to work with the agent — each surface is scoped to
   that one isolated VM, with auth enforced at the edge and again inside the VM:
   - **Web chat** with the OpenClaw agent (the in-VM gateway).
   - **Live terminal** (ttyd) into the VM.
   - **Browser VM** — pair a separate account-scoped microVM running headful
     Chromium; the agent drives it over CDP and you watch the live view in a
     tab. Browser VMs have their own lifecycle, so one can be created ahead of
     time and re-used across machine restarts.

### Lifecycle

Machines are **start / stop / restart / destroy** from the machine page (or the
API). Stopping frees the host capacity; destroying releases the machine's
resources and route. Placement respects host capacity and your fleet's
region/source-image constraints.

### Backups

Per-machine backups are built in: from the machine page (or
`/api/machines/{id}/backups`) you can **create**, **restore**, **download**, and
**delete** backups of the machine's state. Retention is enforced server-side.

### Runtime upgrades

The OpenClaw runtime inside VMs is artifact-driven: hosts stage runtime releases
and the control plane records them in `artifact_releases`. Publishing a new
release makes it available to new/restarted machines — see
[ci-release.md](ci-release.md) for the release lanes and the
[`Release Artifacts` workflow](../.github/workflows/release-artifacts.yml).

---

## Artifacts

Hosts need the **`ocm-agent` binary**, a **Firecracker guest kernel
(`vmlinux`)**, and a **rootfs** image. By default the host bootstrap pulls these
over **plain HTTPS from the project's public GitHub Releases** — no auth, no
bucket, works for any operator:

- `OCM_ARTIFACT_BASE_URL` (default
  `https://github.com/mathaix/OpenClawMachines/releases/latest/download`) — the
  HTTPS source the GCP startup-script and `provision-host.sh` download from.
- `OCM_ARTIFACT_BUCKET=gs://your-bucket` — **override** to pull from your own GCS
  bucket instead (uses the instance service account / `gsutil`).

The [`Release Artifacts` workflow](../.github/workflows/release-artifacts.yml)
builds and publishes `ocm-agent` (+ `authproxy`, `ocm-secrets`, and a manifest)
to the GitHub Release on each `v*` tag.

**Build the agent yourself** (cross-compiled for Linux):

```bash
make build-agent        # -> backend/agent-linux (GOOS=linux GOARCH=amd64)
```

**Guest kernel + rootfs** — a Firecracker-compatible `vmlinux` (see the
[Firecracker docs](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md))
and the rootfs (`make build-rootfs`). Publishing these to GitHub Releases
alongside the agent is tracked in
[#21](https://github.com/mathaix/OpenClawMachines/issues/21); until then, supply
`/var/lib/ocm/vmlinux` on the host (or point `OCM_ARTIFACT_BUCKET` at a bucket
that has them).

---

## Where to go next

- [Architecture](architecture.md) — full data-plane, routing, tunnel, lifecycle design
- [Tech stack](tech-stack.md) — the five layers, client to sandbox
- [Control plane profiles](control-plane-profiles.md) — `local` / `operator` / `hosted`
- [Self-hosted control plane](self-hosted-control-plane.md) — Cloudflare + auth prerequisites
- [Host enrollment](host-enrollment.md) — the enrollment path in depth
- [Local + BYO-host setup](local-setup.md)
- [Local Firecracker E2E](local-firecracker-e2e.md) — stage 1 in full depth
