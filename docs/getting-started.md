# Getting Started

This guide gets you from a clone to OpenClaw agents running in isolated
Firecracker microVMs on hardware you control. It is split into **three stages**,
and each stage ends with something working:

| Stage | What you get | What you need |
|---|---|---|
| [**1 · Local evaluation**](#stage-1--local-evaluation) | The full stack — control plane, worker, and a **real Firecracker machine** — on one box, using prebuilt artifacts | One KVM-enabled Linux box |
| [**2 · Cloudflare + a dedicated host**](#stage-2--cloudflare--a-dedicated-host) | A real deployment: machines reachable at their own `m-<name>.yourdomain.com`, hosts enrolled remotely | A Cloudflare account + domain, a cloud or bare-metal host |
| [**3 · The full workflow**](#stage-3--the-full-workflow) | Day-to-day usage: create and use machines, browser VMs, lifecycle, upgrades | Stages 1–2 |

Start with Stage 1 even if your goal is a real deployment — it verifies your
understanding of the moving parts on one box before you add a domain and a
fleet.

---

## Stage 1 · Local evaluation

**Goal:** boot a real OpenClaw machine — a Firecracker microVM — on a single
Linux box, through the web UI. No Cloudflare, no cloud account, no auth
provider: the `local` profile uses dev auth and reaches the machine through the
worker agent's local proxy instead of a tunnel.

```text
 browser ──▶ frontend :5173 ──▶ control plane :8080 (profile=local)
                                      │  places the machine on the local host
                                      ▼
                              ocm-agent  :9090 control · :9091 proxy
                                      │
                                      ▼
                              Firecracker microVM on bridge ocm-br0
```

### 1.1 Requirements

One **KVM-enabled Linux box** (bare metal, or a cloud VM with nested
virtualization — e.g. GCP n2). Plus: Go ≥ 1.25, Node ≥ 20, Docker (for local
Postgres), and passwordless sudo (the agent sets up the bridge network and KVM
devices). macOS/WSL can't run this stage — Firecracker needs Linux + KVM.

```bash
git clone https://github.com/mathaix/OpenClawMachines.git
cd OpenClawMachines
make preflight    # read-only check of tools, KVM, Firecracker, and config hints
```

Fix anything preflight flags as a blocker before continuing
([docs/local-setup.md](local-setup.md) explains each check).

### 1.2 Stage the VM artifacts

A machine boots from three prebuilt artifacts — you don't build any code for
this stage, just put them where the agent looks:

| Artifact | Path on the box | Where to get it |
|---|---|---|
| Guest kernel | `/var/lib/ocm/vmlinux` | [Artifact sources](#artifact-sources) below |
| Base rootfs | `/var/lib/ocm/images/rootfs.ext4` | [Artifact sources](#artifact-sources) below |
| OpenClaw runtime | staged by the control plane | seeded automatically by the bring-up script |

Also prepare the VM state directory — an XFS reflink mount at
`/var/lib/ocm/vms`, so each VM's rootfs copy is instant instead of a multi-GB
copy (without it, machine creation still works, just slower):

```bash
sudo scripts/configure-vm-state-xfs.sh
```

### 1.3 Bring the stack up

One script starts Postgres (Docker), runs migrations, starts the control plane
(`:8080`, dev auth) and frontend (`:5173`), builds the agent, registers this box
as a host, and starts the worker:

```bash
scripts/local-e2e-firecracker.sh up
scripts/local-e2e-firecracker.sh status   # everything green + host 'ready'?
```

The first run stages the base rootfs (~30s); the host flips
`provisioning → ready` on its first heartbeat.

### 1.4 Create your machine

Open **http://localhost:5173** — dev auth logs you in automatically.

1. **New Machine** → name it, pick **Basic**, region **External** → **Create**.
2. Watch it boot: `allocating → rootfs → network → booting → booted → running`.
3. Open the machine: you get a **web chat** with the agent and a **live
   terminal**, served through the agent proxy on `:9091`.

When you're done:

```bash
scripts/local-e2e-firecracker.sh down
```

**You now have the whole product working on one box.** Deeper notes on this
path, including how it was derived and debugged:
[docs/local-firecracker-e2e.md](local-firecracker-e2e.md).

---

## Stage 2 · Cloudflare + a dedicated host

**Goal:** a real deployment. The control plane runs with the `operator` profile
behind a public HTTPS URL, machines get their own hostnames
(`m-<name>.yourdomain.com`) with auth enforced at the edge, and you enroll
dedicated KVM hosts — cloud or bare metal — that the control plane places
machines onto.

The host-onboarding path is **the same on every provider** (GCP, AWS, Hetzner,
bare metal): create a KVM-capable Linux box, install dependencies with
`provision-host.sh`, and **enroll** it. We use a GCP n2 instance as the worked
example because n2 supports nested virtualization out of the box. One-click
auto-provisioning from the UI for every cloud is tracked in
[#11](https://github.com/mathaix/OpenClawMachines/issues/11).

### 2.1 Set up Cloudflare

The data plane uses Cloudflare for edge auth and a **per-VM tunnel**, so each
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

See [docs/self-hosted-control-plane.md](self-hosted-control-plane.md) for the
full Cloudflare + auth prerequisites and the architecture behind this.

### 2.2 Configure and run the control plane

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
| `CLOUDFLARE_API_TOKEN` / `CLOUDFLARE_ACCOUNT_ID` / `CLOUDFLARE_ZONE_ID` | from step 2.1 |
| `CLOUDFLARE_KV_NAMESPACE_ID` | the `OCM_ROUTES` namespace id from step 2.1 |
| `OCM_ADMIN_EMAILS` | comma-separated admin emails (who can manage hosts) |
| `OCM_ARTIFACT_BUCKET` | where hosts pull the agent + kernel — see [Artifact sources](#artifact-sources) |

Frontend env (`frontend/.env.local`) must include `VITE_OCM_ADMIN_EMAILS` with
the **same** admin email(s) as `OCM_ADMIN_EMAILS`, or the admin UI won't show.

> Admin gating is two layers: the **backend** checks `OCM_SUPERUSER_EMAILS` /
> `OCM_ADMIN_EMAILS` (the real gate), and the **frontend** checks
> `VITE_OCM_ADMIN_EMAILS` (UI visibility). Keep them in sync.

Run it. For evaluation on your workstation:

```bash
make local-postgres   # start Dockerized Postgres
make local-migrate    # run schema migrations
make backend          # go run ./cmd/server  (:8080)
make frontend         # Vite dev server      (:5173, proxies /api → :8080)
```

For a real deployment, build and run the server binary on your control-plane
host behind HTTPS so `BACKEND_URL` is publicly reachable:

```bash
make build-server     # produces ./backend/server
./backend/server      # reads .env
```

Confirm it's up: `make status` (checks `:8080/health`). Every variable and
per-profile default is documented in
[docs/control-plane-profiles.md](control-plane-profiles.md).

### 2.3 Provision a host

Create a KVM-capable VM. The GCP n2 example:

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

> **AWS / Hetzner / bare metal:** create a nested-virt-capable instance instead
> (AWS `*.metal`, Hetzner dedicated/CCX with KVM) running Ubuntu 24.04. The rest
> of this stage is identical.

Install the host dependencies (KVM + nested virt, Firecracker, cloudflared, the
guest kernel, XFS/reflink state storage, sysctl tuning):

```bash
gcloud compute scp scripts/provision-host.sh ocm-host-1:~ --zone=us-central1-b
gcloud compute ssh ocm-host-1 --zone=us-central1-b --command \
  'sudo OCM_ARTIFACT_BUCKET=gs://YOUR-ARTIFACT-BUCKET bash ~/provision-host.sh'
```

`OCM_ARTIFACT_BUCKET` is where the kernel (`/vmlinux`) is pulled from — see
[Artifact sources](#artifact-sources).

### 2.4 Register the host with your control plane

Mint an enrollment token. In the UI: **Admin → Hosts → Enroll Host**, or via the
API:

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

**Verify:** the host appears in **Admin → Hosts** as `ready` within ~60s, with
its capacity bars and heartbeat visible:

![Admin fleet view with enrolled hosts ready](images/fleet-hosts.png)

On the host:

```bash
gcloud compute ssh ocm-host-1 --zone=us-central1-b --command \
  'systemctl status ocm-agent --no-pager; journalctl -u ocm-agent -n 30 --no-pager'
```

Host enrollment in more depth (OVH, Hetzner, bare metal):
[docs/host-enrollment.md](host-enrollment.md).

---

## Stage 3 · The full workflow

**Goal:** day-to-day usage on your deployment from Stage 2.

### 3.1 Log in and create a machine

1. Open the frontend (your deployed URL) and log in. The first login
   auto-creates your user + a personal account. (In `dev` auth mode you're
   logged in as `DEV_USER_EMAIL` automatically.)
2. **New Machine** → pick a name and size → **Create**. The control plane places
   the machine on a `ready` host with capacity and boots the microVM; the
   machine page shows it reach **Running**.

### 3.2 Use it

Each running machine is reachable at its own hostname
(`m-<name>.yourdomain.com`) through its own Cloudflare Tunnel that terminates
**inside the VM** — auth is enforced at the edge and again in the VM:

- **Workspace** — web chat with the agent and a live terminal, scoped to that
  one isolated VM.
- **Channels / Integrations / Model** — connect messaging channels, configure
  which AI model powers the machine, and add provider credentials (encrypted
  per-machine secrets, injected into the VM).
- **Browser VM** — pair a separate, account-scoped microVM running headful
  Chromium; the agent drives it over CDP and you can watch the live view in a
  tab.
- **SSH** — `cloudflared access ssh` through the machine's hostname.

### 3.3 Operate it

- **Lifecycle** — stop, start, and delete machines from the machine page;
  stopped machines free their host capacity but keep their state.
- **Backups / snapshots** — capture and restore a machine's state from the
  Backups tab.
- **Runtime upgrades** (admin) — the machine page's Runtime section lets you
  upgrade/roll back a machine's OpenClaw runtime and rootfs between published
  releases.
- **Fleet** — **Admin → Hosts** shows host health, capacity, and heartbeats;
  enroll more hosts with the same Stage 2.4 flow to grow capacity.

---

## Artifact sources

Hosts need three prebuilt artifacts: the **`ocm-agent` binary**, a **Firecracker
guest kernel (`vmlinux`)**, and a **rootfs** image.

- `OCM_ARTIFACT_BASE_URL` (default
  `https://github.com/mathaix/OpenClawMachines/releases/latest/download`) — the
  plain-HTTPS source the host bootstrap downloads from. The
  [`Release Artifacts` workflow](../.github/workflows/release-artifacts.yml)
  publishes `ocm-agent` (+ `authproxy`, `ocm-secrets`, and a manifest) on each
  `v*` tag. **Publishing the kernel and rootfs there is in progress
  ([#21](https://github.com/mathaix/OpenClawMachines/issues/21))** — until the
  first release containing them is cut, use one of the options below.
- `OCM_ARTIFACT_BUCKET=gs://your-bucket` — override to pull everything from your
  own GCS bucket (uses the instance service account / `gsutil`).
- **Build them yourself:**
  ```bash
  make build-agent     # -> backend/agent-linux (GOOS=linux GOARCH=amd64)
  make build-rootfs    # rootfs image (needs Docker)
  ```
  For the guest kernel, use a Firecracker-compatible `vmlinux` (see the
  [Firecracker getting-started docs](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md)),
  placed at `/var/lib/ocm/vmlinux` (Stage 1) or in your artifact bucket
  (Stage 2).

---

## Where to go next

- [Architecture](architecture.md) — full data-plane, routing, tunnel, lifecycle design
- [Tech stack](tech-stack.md) — the five layers, from browser to sandbox
- [Control plane profiles](control-plane-profiles.md) — `local` / `operator` / `hosted`
- [Self-hosted control plane](self-hosted-control-plane.md) — Cloudflare + auth prerequisites
- [Host enrollment](host-enrollment.md) — the enrollment path in depth
- [Local + BYO-host setup](local-setup.md) — preflight checks and host requirements
