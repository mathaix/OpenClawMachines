# Getting Started

One stack, grown in **three stages — each ends with something working**, and
each builds on the last:

```mermaid
flowchart LR
    S1["Stage 1 · Local evaluation<br/>everything on one KVM box<br/>→ a running OpenClaw workspace"]
    S2["Stage 2 · Cloudflare + a dedicated host<br/>same stack, real domain, enrolled fleet host<br/>→ machines at m-&lt;name&gt;.your-domain.com"]
    S3["Stage 3 · The full workflow<br/>day-to-day on what you built<br/>→ chat, terminal, browser VMs, backups, upgrades"]
    S1 --> S2 --> S3
```

| Stage | You have | You'll end with |
|---|---|---|
| [1 — Local evaluation](#stage-1--local-evaluation) | One KVM-capable Linux box (we show how to get one) | The full stack and a **running OpenClaw workspace** in a real Firecracker microVM — no Cloudflare, no domain |
| [2 — Cloudflare + a dedicated host](#stage-2--cloudflare--a-dedicated-host) | A Cloudflare account + a domain | The production-shaped deployment: an enrolled fleet host, machines reachable at **`m-<name>.your-domain.com`** |
| [3 — The full workflow](#stage-3--the-full-workflow) | A stack from stage 1 or 2 | Day-to-day fluency: machines, browser VMs, lifecycle, backups, runtime upgrades |

Stage 1 and stage 2 are independent — you can evaluate locally without ever
touching Cloudflare, or go straight to stage 2. Stage 3 works against either.

---

## Stage 1 — Local evaluation

**You have:** one KVM-capable Linux box.
**You'll end with:** the entire stack — control plane, frontend, and a
Firecracker worker — on that box, and a **running OpenClaw workspace** in a
real microVM, provisioned by clicking through the web UI. No Cloudflare: the
workspace is reached through the local agent proxy.

**Stage-1 architecture — every component on one box:**

```mermaid
flowchart LR
    B["browser"]
    subgraph BOX["your KVM box"]
        FE["frontend :5173"] --> CP["control plane :8080<br/>profile=local · AUTH_MODE=dev"]
        CP --- DB[("Postgres<br/>(Docker)")]
        CP -->|schedules| AG["ocm-agent<br/>:9090 control · :9091 proxy"]
        AG --> VM["Machine<br/>Firecracker microVM<br/>bridge 192.168.100.0/24"]
    end
    B --> FE
    B -.->|workspace, via the agent proxy| AG
```

### 1.1 Get a KVM box

Firecracker needs `/dev/kvm` — bare metal, or a VM with **nested
virtualization**. macOS, Windows/WSL, and ordinary cloud VMs won't work. If you
don't already have one:

| Option | What to do | Notes |
|---|---|---|
| **GCP n2** (used in our examples) | `gcloud compute instances create` with `--enable-nested-virtualization` — full command below | n2 supports nested virt out of the box; ~10 minutes to running |
| **AWS** | Any `*.metal` instance (e.g. `c5.metal`), Ubuntu 24.04 | Only metal instances expose KVM |
| **Hetzner** | A dedicated (bare-metal) server, Ubuntu 24.04 | Best price for a permanent host |
| **Your own machine** | Any Linux box/workstation with KVM enabled | `ls /dev/kvm` to check |

The GCP recipe (the same box can later become your stage-2 fleet host):

```bash
gcloud compute instances create ocm-eval \
  --project=YOUR_PROJECT --zone=us-central1-b \
  --machine-type=n2-standard-4 \
  --enable-nested-virtualization \
  --image-family=ubuntu-2404-lts-amd64 --image-project=ubuntu-os-cloud \
  --boot-disk-size=100GB --boot-disk-type=pd-ssd
```

> If creation fails with `ZONE_RESOURCE_POOL_EXHAUSTED`, that zone is
> temporarily out of `n2` capacity — retry with a different `--zone` (e.g.
> `us-central1-a`, `us-west1-b`).

### 1.2 Prepare the box

A fresh Ubuntu 24.04 image has none of the developer tools. Install them first:

```bash
sudo apt-get update
# make/docker/git plus the rootfs-build deps (buildx, mkfs.ext4, bsdtar, strings):
sudo apt-get install -y make docker.io docker-buildx git curl jq e2fsprogs libarchive-tools binutils
# Go ≥ 1.25.10 and Node ≥ 20 (adjust for your arch):
curl -sL https://go.dev/dl/go1.26.2.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/golang.sh
curl -fsSL https://deb.nodesource.com/setup_22.x | sudo bash - && sudo apt-get install -y nodejs
sudo usermod -aG docker,kvm "$USER"   # log out/in for group changes to take effect
```

Then clone the repo and check readiness:

```bash
git clone https://github.com/mathaix/OpenClawMachines.git
cd OpenClawMachines
make preflight
```

`make preflight` is read-only: it checks the developer tools (Go ≥ 1.25.10,
Node ≥ 20, Docker), KVM access, Firecracker, and the VM assets, and tells you
exactly what's missing. Two things it will ask for:

- **`firecracker` on PATH and host assets** — `scripts/provision-host.sh` is
  the one-shot installer (Firecracker, guest kernel, cloudflared, sysctl
  tuning, and the **XFS reflink mount** at `/var/lib/ocm/vms` — a
  copy-on-write filesystem so each VM's disk is a cheap clone of a shared base
  image):
  ```bash
  sudo OCM_ARTIFACT_BUCKET=gs://YOUR-ARTIFACT-BUCKET bash scripts/provision-host.sh
  ```
- **VM images** — a guest kernel at `/var/lib/ocm/vmlinux` and a rootfs at
  `/var/lib/ocm/images/rootfs.ext4`.
  - The **kernel** is fetched by `provision-host.sh` above (or build one per
    the [Firecracker docs](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md)).
  - **Build the rootfs from source** — this is the recommended path for local
    evaluation: it bakes in this repo's `scripts/init-openclaw.sh` (which boots
    correctly in the no-tunnel local profile) and needs no artifact bucket.
    Needs Docker **with the buildx plugin** (`docker buildx version`),
    `mkfs.ext4`, and `bsdtar` — all installed in the toolchain step above.
    ```bash
    make install-vm-binaries   # authproxy, ocm-secrets, ocmptyd — baked into the image
    make build-rootfs          # ~10 min; writes /var/lib/ocm/images/rootfs.ext4
    ```
  Pulling a prebuilt rootfs from a bucket you populate is the alternative — see
  the [Artifacts](#artifacts) section (read it once now; everything else refers
  back to it).

### 1.3 Bring the stack up

One script stands up Postgres (Docker), migrations, the control plane (`local`
profile, dev auth — you're auto-logged-in as `dev@localhost`), the frontend,
and then builds, registers, and starts a local Firecracker worker whose first
heartbeat flips the host to `ready`:

```bash
scripts/local-e2e-firecracker.sh up
scripts/local-e2e-firecracker.sh status   # component health + host/machine status
```

### 1.4 Boot a machine to a running workspace

The control plane resolves which OpenClaw runtime version a machine gets from
the **`artifact_releases`** table (one row per published artifact version), via
a resolver behind the `FF_RUNTIME_VERSION_RESOLVER=1` flag — the bring-up
script's env sets the flag; what's left is staging a runtime release on the
host and registering its row. The exact steps (extract the runtime tarball
under `/var/lib/ocm/openclaw/releases/<version>`, add its `manifest.json`,
insert the `artifact_releases` row) are documented step-by-step in
[local-firecracker-e2e.md](local-firecracker-e2e.md#reaching-a-running-workspace-openclaw-gateway-up).

Then open `http://localhost:5173/dashboard` → **New Machine → Basic, region
External → Create → Start**. The control plane places the machine on your
local host, the agent boots a Firecracker microVM
(`allocating → rootfs → network → booting → booted`), and the in-VM gateway
comes up. Or drive the same flow headlessly:

```bash
cd frontend && PLAYWRIGHT_BASE_URL=http://localhost:5173 \
  npx playwright test e2e/machine-lifecycle.spec.ts --project=chromium-dev
```

The `chromium-dev` project targets the `AUTH_MODE=dev` stack (no login form,
uses the system Chrome channel); run `npx playwright install chrome` once if
it is not already present.

**Checkpoint — you should see this** (machine `running`, workspace streaming):

![An OpenClaw machine running in a Firecracker microVM](images/machine-running.png)

That workspace — chat, terminal, logs — is exactly what stage 3 is about.
Tear down anytime with `scripts/local-e2e-firecracker.sh down`.

---

## Stage 2 — Cloudflare + a dedicated host

**You have:** a Cloudflare account, a domain (zone) you control, and a
KVM-capable host (the stage-1 box works, or create a fresh one with the same
recipes).
**You'll end with:** the production-shaped deployment — an `operator`-profile
control plane, the Cloudflare data plane, and a dedicated host enrolled into
your fleet. Machines become reachable at `m-<name>.your-domain.com` from
anywhere.

Same stack as stage 1 — what changes is the front door and where the worker
lives:

**Stage-2 architecture — the same components, split apart: Cloudflare becomes
the front door, the worker moves to a dedicated host:**

```mermaid
flowchart LR
    B["browser"] --> EDGE
    subgraph EDGE["Cloudflare edge"]
        CFA["Access (edge auth)"] --> W["Worker<br/>route lookup in KV"]
        W --> T["per-VM tunnel"]
    end
    subgraph CPB["control-plane box (operator profile)"]
        CP["control plane :8080"] --- DB[("Postgres")]
        FE["frontend"]
    end
    subgraph HOST["dedicated host (the box from 1.1, or a new one)"]
        AG["ocm-agent<br/>:9090 control"]
        AG --> VM["Machine<br/>Firecracker microVM"]
        VM --- CFD["cloudflared + authproxy<br/>(inside the VM)"]
    end
    T --> CFD
    CP -.->|publishes routes| W
    CP -.->|enroll + heartbeat :9090| AG
```

The host-onboarding path is **the same on every provider** (GCP, AWS, Hetzner,
bare metal): bring a KVM-capable Linux box, install dependencies, **enroll**
it. (One-click auto-provisioning per cloud is tracked in
[#11](https://github.com/mathaix/OpenClawMachines/issues/11).)

### 2.1 Cloudflare: zone, Worker, KV

The data plane uses Cloudflare for edge auth and a **per-VM tunnel**, so each
machine gets its own subdomain with no inbound ports on your host. You configure
the account-level pieces **once**; from then on the control plane mints each
machine's tunnel + DNS record and publishes its route to KV automatically on
start (and tears them down on stop) — you never create per-machine tunnels by
hand.

1. A **Cloudflare account** and a **domain (zone)** you control (your
   `DATA_PLANE_DOMAIN`, e.g. `example.com`). Machines get `m-<slug>.example.com`.
   Grab your **Account ID** and **Zone ID** from the zone's Overview page.

2. An **API token** the control plane uses at runtime to manage tunnels, DNS,
   and route KV. Create it at **My Profile → API Tokens → Create Token → Custom
   token** with exactly these permission groups:

   | Permission group | Scope | Used for |
   |---|---|---|
   | **Account · Cloudflare Tunnel · Edit** | your account | create/configure/delete per-VM tunnels |
   | **Account · Workers KV Storage · Edit** | your account | publish/expire machine routes in the `OCM_ROUTES` namespace |
   | **Zone · DNS · Edit** | your zone | create/delete the `m-<slug>` DNS records |
   | **Zone · Zone · Read** | your zone | resolve the zone |

   This token is `CLOUDFLARE_API_TOKEN` (§2.2). (The one-time Worker/KV commands
   below authenticate separately via `wrangler login`; if you'd rather script
   them with a token, that token additionally needs **Account · Workers Scripts ·
   Edit**.)

3. The Worker + KV pieces (log in first with `npx wrangler login`):
   ```bash
   cd worker
   npx wrangler kv namespace create OCM_ROUTES   # route-lookup KV namespace
   ```
   Then edit `worker/wrangler.toml`:
   - replace `replace-with-operator-kv-namespace-id` under the `OCM_ROUTES`
     binding with the returned namespace **id** (use the same id for
     `CLOUDFLARE_KV_NAMESPACE_ID` in §2.2);
   - set `BASE_DOMAIN` to your domain and `FRONTEND_ORIGIN_HOST` /
     `BACKEND_ORIGIN_HOST` to origin hostnames **outside** the Worker route (so
     the Worker's API/resolve fallback doesn't loop back on itself);

   set the Worker secrets and deploy:
   ```bash
   npx wrangler secret put JWT_SECRET             # same value as the backend
   npx wrangler secret put CF_SERVICE_TOKEN_ID    # for /internal/resolve
   npx wrangler secret put CF_SERVICE_TOKEN_SECRET
   npx wrangler deploy
   ```
   Finally, attach the Worker to your zone so machine subdomains hit it —
   **Cloudflare dashboard → Workers Routes** (or a `routes` entry), pattern
   `*.<your-domain>/*`. Route entries aren't in `wrangler.toml`.

For **edge authentication** (`AUTH_MODE=cfaccess`) you also create a Cloudflare
Access application over your control-plane/API hostnames — the exact steps
(hostnames to cover, policy, where the AUD tag comes from) and the Firebase
alternative are in
[self-hosted-control-plane.md](self-hosted-control-plane.md#cloudflare).

### 2.2 Configure and run the control plane

```bash
cp docs/self-hosted.env.example .env
```

Required for the host-enrollment + machine flow:

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
| `OCM_ARTIFACT_BUCKET` | the bucket hosts pull artifacts from — see [Artifacts](#artifacts) |

Frontend env (`frontend/.env.local`) must include `VITE_OCM_ADMIN_EMAILS` with
the **same** admin email(s), or the admin UI won't show. (Backend
`OCM_ADMIN_EMAILS` is the real gate; the frontend var only controls UI
visibility.)

Run it — for evaluation, the local helpers:

```bash
make local-postgres   # Dockerized Postgres
make local-migrate    # schema migrations
make backend          # go run ./cmd/server  (:8080)
make frontend         # Vite dev server      (:5173, proxies /api → :8080)
```

For a real deployment, build and run the binary behind HTTPS so `BACKEND_URL`
is publicly reachable: `make build-server && ./backend/server`. Confirm with
`make status` (checks `:8080/health`).

See [control-plane-profiles.md](control-plane-profiles.md) for every variable.

### 2.3 Create the host

Same acquisition table as [1.1](#11-get-a-kvm-box) — for the worked example, a
GCP n2 named `ocm-host-1`, plus the agent control port:

```bash
gcloud compute instances create ocm-host-1 \
  --project=YOUR_PROJECT --zone=us-central1-b \
  --machine-type=n2-standard-4 \
  --enable-nested-virtualization \
  --image-family=ubuntu-2404-lts-amd64 --image-project=ubuntu-os-cloud \
  --boot-disk-size=100GB --boot-disk-type=pd-ssd

# the per-VM tunnel is outbound-only, but the control plane health-checks the
# agent on :9090
gcloud compute firewall-rules create ocm-agent-9090 \
  --allow=tcp:9090 --target-tags=ocm-agent --network=default
gcloud compute instances add-tags ocm-host-1 --tags=ocm-agent --zone=us-central1-b
```

Install the host dependencies (same `provision-host.sh` as stage 1.2):

```bash
gcloud compute scp scripts/provision-host.sh ocm-host-1:~ --zone=us-central1-b
gcloud compute ssh ocm-host-1 --zone=us-central1-b --command \
  'sudo OCM_ARTIFACT_BUCKET=gs://YOUR-ARTIFACT-BUCKET bash ~/provision-host.sh'
```

### 2.4 Enroll it into your fleet

In the UI: log in as an `OCM_ADMIN_EMAILS` admin → **Admin → Hosts → Enroll
Host** → Generate token. The wizard gives you a one-line install command —
run it on the host:

```bash
gcloud compute ssh ocm-host-1 --zone=us-central1-b --command \
  'curl -sL '"$BACKEND_URL"'/api/agent/install | sudo bash -s -- <TOKEN>'
```

(The same is available over the API at
`POST /api/admin/hosts/enrollment-tokens` if you're scripting; authenticate
the call the same way your `AUTH_MODE` authenticates the UI.)

The install script registers the host, creates the per-host Cloudflare tunnel,
mints the agent token, downloads + verifies the `ocm-agent` binary (from your
artifact source — see [Artifacts](#artifacts)), installs the `ocm-agent`
systemd unit, and starts it.

**Checkpoint** — the host appears in **Admin → Hosts** as `ready` within ~60s,
with its capacity bars and heartbeat visible:

![Admin fleet view with enrolled hosts ready](images/fleet-hosts.png)

On the host, if anything looks off:

```bash
gcloud compute ssh ocm-host-1 --zone=us-central1-b --command \
  'systemctl status ocm-agent --no-pager; journalctl -u ocm-agent -n 30 --no-pager'
```

Create a machine from the dashboard and it boots on this host — reachable at
`m-<name>.your-domain.com` through Cloudflare Access and the per-VM tunnel.

---

## Stage 3 — The full workflow

**You have:** a running stack — either the stage-1 box (dashboard at
`http://localhost:5173`, machines via the local agent proxy) or the stage-2
deployment (your domain, machines at `m-<name>.your-domain.com`).
**You'll end with:** day-to-day fluency. Everything below behaves identically
in both; only the URLs differ.

**Stage-3 architecture — the same stack you deployed, with the day-to-day
flows layered on top:**

```mermaid
flowchart LR
    B["browser<br/>(machine page: chat · terminal · live view)"] --> FD["front door<br/>stage 1: agent proxy · stage 2: per-VM tunnel"]
    subgraph HOST["your host(s)"]
        AG["ocm-agent"]
        AG -->|start / stop / restart / destroy| VM["Machine<br/>Firecracker microVM<br/>OpenClaw agent + gateway + terminal"]
        AG -->|stages runtime releases| REL["/var/lib/ocm/openclaw/releases/&lt;version&gt;"]
        VM -->|drives over CDP| BVM["paired Browser VM<br/>headful Chromium"]
    end
    FD --> VM
    CP["control plane"] -.->|placement · lifecycle| AG
    CP -.->|"resolves version from artifact_releases (1.4)"| REL
    VM <-->|create · restore| BK[("backups<br/>object store")]
```

### Create and use machines

1. Open the dashboard and log in. First login auto-creates your user + a
   personal account (in `dev` auth mode you're logged in as `DEV_USER_EMAIL`
   automatically — that's how the stage-1 stack runs).
2. **New Machine** → name and size → create. The control plane places it on a
   `ready` host — the local worker from stage 1.3, or the host you enrolled in
   2.4 — and boots the microVM.
3. The machine page is the workspace you first saw at the stage-1 checkpoint:
   - **Web chat** with the OpenClaw agent (the in-VM gateway). Add a model
     provider key on the machine's **Model** tab first (or via
     `PUT /accounts/{id}/machines/{mid}/credentials/{provider}`); without one
     the gateway logs `no API key configured for provider`.
   - **Live terminal** (ttyd) inside the VM.
   - **Browser VM** — pair a separate account-scoped microVM running headful
     Chromium; the agent drives it over CDP while you watch the live view.
     Browser VMs have their own lifecycle, so one can be created ahead of time
     and re-used across machine restarts.

> **Two workspace panels need extra config in the pure-local (stage-1) path:**
> the **Resources** tab's live CPU/memory charts are sampled from each VM's
> systemd cgroup, so they populate only when the agent runs with the default
> `VM_RUNTIME_OWNER=systemd-unit` (the `local-e2e-firecracker.sh` helper uses
> `direct` for simplicity, which leaves the charts empty); and the **Traces**
> tab (Opik) stays empty until the control plane advertises a trace endpoint
> the VM can reach — set `OPIK_API_URL` (in stage 2 this is derived from
> `PUBLIC_URL`). Both work out of the box in a stage-2 deployment.

### Lifecycle

**Start / stop / restart / destroy** from the machine page (or the API).
Stopping frees host capacity; destroying releases the machine's resources and
its route. Placement respects capacity and your fleet's region/source-image
constraints — with one host (either stage) everything lands there; add more
enrolled hosts (repeat 2.3–2.4) and placement spreads across them.

### Backups

Per-machine backups are built in: **create**, **restore**, **download**, and
**delete** from the machine page or
`/api/machines/{id}/backups`. Retention is enforced server-side.

### Runtime upgrades

The OpenClaw runtime inside VMs is **artifact-driven** — the same
`artifact_releases` mechanism you touched in stage 1.4: hosts stage runtime
releases, the control plane records one row per version, and the resolver
picks the version for each new/restarted machine. Publishing a newer release
makes it available fleet-wide without rebuilding hosts. See
[ci-release.md](ci-release.md) for the release lanes.

---

## Artifacts

Three artifacts make a host able to boot machines, plus one the machines run:

| Artifact | Lands on the host at | Fetched by | Build it yourself | Download |
|---|---|---|---|---|
| `ocm-agent` (worker binary) | `/usr/local/bin/ocm-agent` | the enroll install script (2.4) | `make build-agent` → `backend/agent-linux` *(verified)* | your `OCM_ARTIFACT_BUCKET` |
| Guest kernel `vmlinux` | `/var/lib/ocm/vmlinux` | `provision-host.sh` | a Firecracker-compatible kernel — see the [Firecracker docs](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md) | your `OCM_ARTIFACT_BUCKET` |
| Rootfs `rootfs.ext4` | staged by the agent as a shared `.base-rootfs.ext4` on the reflink mount (each VM disk is a copy-on-write clone) | the **agent**, via `ROOTFS_GCS_MANIFEST` (`provision-host.sh` fetches only the kernel and browser-VM assets) | `make build-rootfs` (Docker + `mkfs.ext4` + `bsdtar`; run `make install-vm-binaries` first) | your `OCM_ARTIFACT_BUCKET` |
| OpenClaw runtime release | `/var/lib/ocm/openclaw/releases/<version>` | the agent, from the version resolved via `artifact_releases` (stage 1.4) | `make build-openclaw` (`scripts/build-openclaw-runtime.sh`) | your bucket via `OPENCLAW_GCS_MANIFEST` |

**Where downloads come from: a GCS bucket you control.** The rootfs is
multiple GB — over GitHub's 2 GB release-asset cap — so a **GCS bucket is the
canonical artifact channel**
([#21](https://github.com/mathaix/OpenClawMachines/issues/21)). The flow is:
build each component (one command each), upload it with its `scripts/upload-*.sh`
(the agent and rootfs each have one; the kernel and OpenClaw runtime release are
uploaded manually — see [building.md](building.md)), and set
`OCM_ARTIFACT_BUCKET=gs://your-bucket` everywhere this guide uses it. Each
upload writes the artifact plus a `manifest.json` (whose `sha256` is the hash of
the **uncompressed** image, even when the stored artifact is `.zst`-compressed)
into a standard bucket layout; the provision and enroll scripts pull and
hash-verify from your bucket via the host's service account.

The component-by-component build manual — every build command, output, upload
script, and the bucket layout — is **[building.md](building.md)**. (The code
also has a GitHub-Releases fallback URL for the small binaries, but no release
has been cut yet; treat the bucket as the real channel.)

> **Building from source is authoritative for self-hosting.** The prebuilt
> artifacts in the project's own bucket are cut by the maintainers' release
> pipeline and can lag this repository. In particular, a rootfs published before
> the no-tunnel init fix **panics on a local/self-hosted (no-tunnel) boot**
> (`[FATAL] tunnel_token is empty`). `make build-rootfs` from this repo bakes in
> the current `scripts/init-openclaw.sh` and boots cleanly in every profile — so
> for local evaluation, build the rootfs (see [1.2](#12-prepare-the-box)) rather
> than pulling a prebuilt one.

---

## Where to go next

- [Building the components](building.md) — every component's build, the upload scripts, and the bucket layout
- [Local Firecracker E2E](local-firecracker-e2e.md) — stage 1 in full depth, including everything `local-e2e-firecracker.sh` does
- [Architecture](architecture.md) — data plane, routing, tunnels, lifecycle design
- [Tech stack](tech-stack.md) — the five layers, client to sandbox
- [Control plane profiles](control-plane-profiles.md) — `local` / `operator` / `hosted`
- [Self-hosted control plane](self-hosted-control-plane.md) — Cloudflare + auth prerequisites
- [Host enrollment](host-enrollment.md) — the enrollment path in depth
- [Local + BYO-host setup](local-setup.md)
