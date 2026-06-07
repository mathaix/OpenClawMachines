# Getting Started

This guide takes you from a clone to a running OpenClaw agent in an isolated
microVM on a host you control. It uses **one host-onboarding path that is the
same on every provider** (GCP, AWS, Hetzner, bare metal): you bring a
KVM-capable Linux box, install the dependencies, and **enroll** it with your
control plane. We use a **GCP n2** instance as the worked example because n2
supports nested virtualization out of the box.

> **The same steps work on AWS, Hetzner, or bare metal** — only the "create a
> VM" command in Step 5 changes. Everything after it (`provision-host.sh` +
> enroll) is identical. One-click auto-provisioning from the UI for every cloud
> is tracked in [#11](https://github.com/mathaix/OpenClawMachines/issues/11).

## Overview

```
1. Install dependencies            (your workstation)
2. Create a Cloudflare account     (edge auth + per-VM tunnels + routing)
3. Set up environment variables    (.env + frontend/.env.local)
4. Run the backend + frontend      (the control plane)
5. Add a host: GCP n2 + enroll     (the worker that runs microVMs)
6. Log in and provision a VM       (create a Machine -> it boots on your host)
```

Two machines are involved:
- **Control plane** (backend + frontend) — runs anywhere reachable over HTTPS
  (your workstation for evaluation, or a small cloud VM). macOS or Linux.
- **Host** — a KVM-enabled **Linux** box that actually boots Firecracker
  microVMs. This is the GCP n2 instance below. (No macOS/WSL.)

---

## 1. Install dependencies

On your **workstation** (to build/run the control plane):

| Tool | Version | Notes |
|---|---|---|
| Go | ≥ 1.25.10 | `backend/go.mod`; `make preflight` enforces it |
| Node + npm | ≥ 20 | frontend (Vite) |
| Docker | any recent | local Postgres helper |
| openssl | any | generates local secrets |
| gcloud CLI | any | only for the GCP example in Step 5 |
| psql | optional | host migrations (falls back to a container) |

```bash
git clone https://github.com/mathaix/OpenClawMachines.git
cd OpenClawMachines
make preflight   # checks tools; KVM/Firecracker checks only matter on the HOST
```

`make preflight` will report Linux/KVM/Firecracker items as failing on macOS —
that is expected; those checks apply to the **host** (Step 5), not the control
plane.

---

## 2. Create a Cloudflare account

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

See [docs/self-hosted-control-plane.md](self-hosted-control-plane.md) for the
full Cloudflare + auth prerequisites and the architecture behind this.

> **Evaluating without Cloudflare?** You can run the control plane + UI locally
> in the `local` profile (`make local-backend` / `make local-frontend`) to click
> around, but **provisioning a reachable machine requires Cloudflare** because
> the data plane routes through it.

---

## 3. Set up environment variables

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
| `CLOUDFLARE_API_TOKEN` / `CLOUDFLARE_ACCOUNT_ID` / `CLOUDFLARE_ZONE_ID` | from Step 2 |
| `CLOUDFLARE_KV_NAMESPACE_ID` | the `OCM_ROUTES` namespace id from Step 2 |
| `OCM_ADMIN_EMAILS` | comma-separated admin emails (who can manage hosts) |
| `OCM_ARTIFACT_BUCKET` | where hosts pull the agent + kernel (see [Artifacts](#artifacts)) |

Frontend env (`frontend/.env.local`) must include `VITE_OCM_ADMIN_EMAILS` with
the **same** admin email(s) as `OCM_ADMIN_EMAILS`, or the admin UI won't show.

> Admin gating is two layers: the **backend** checks `OCM_SUPERUSER_EMAILS` /
> `OCM_ADMIN_EMAILS` (the real gate), and the **frontend** checks
> `VITE_OCM_ADMIN_EMAILS` (UI visibility). Keep them in sync.

See [docs/control-plane-profiles.md](control-plane-profiles.md) for every
variable and per-profile defaults.

---

## 4. Run the backend + frontend

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

---

## 5. Add a host (GCP n2) and enroll it

This is the universal path. Create a KVM-capable VM, install dependencies with
`provision-host.sh`, then enroll it.

### 5a. Create a GCP n2 instance (nested virtualization on)

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
> this step is identical.

### 5b. Install host dependencies

Copy and run `provision-host.sh` (installs KVM + nested virt, Firecracker,
cloudflared, the guest kernel, XFS/reflink state storage, sysctl tuning):

```bash
gcloud compute scp scripts/provision-host.sh ocm-host-1:~ --zone=us-central1-b
gcloud compute ssh ocm-host-1 --zone=us-central1-b --command \
  'sudo OCM_ARTIFACT_BUCKET=gs://YOUR-ARTIFACT-BUCKET bash ~/provision-host.sh'
```

`OCM_ARTIFACT_BUCKET` is where the kernel (`/vmlinux`) is pulled from. See
[Artifacts](#artifacts) to build your own or use the public default.

### 5c. Create an enrollment token and enroll

In the UI: **Admin → Hosts → Enroll Host**, or via the API:

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

### 5d. Verify

The host should appear in **Admin → Hosts** as `ready` within ~60s. On the host:

```bash
gcloud compute ssh ocm-host-1 --zone=us-central1-b --command \
  'systemctl status ocm-agent --no-pager; journalctl -u ocm-agent -n 30 --no-pager'
```

---

## 6. Log in and provision a VM

1. Open the frontend (`:5173` locally, or your deployed URL) and log in. The
   first login auto-creates your user + a personal account. (In `dev` auth mode
   you're logged in as `DEV_USER_EMAIL` automatically.)
2. **New Machine** → pick a name and size → create. With auto-start, the control
   plane places the machine on your `ready` host and boots the microVM.
3. Open the machine to get a **web chat**, a **live terminal**, and (if a browser
   VM is paired) a **browser view** — each scoped to that one isolated VM.

That's it: an OpenClaw agent running in its own Firecracker microVM on hardware
you control.

---

## Artifacts

Hosts need two artifacts: the **`ocm-agent` binary** and a **Firecracker guest
kernel (`vmlinux`)**. Both can be pulled from your `OCM_ARTIFACT_BUCKET`, or you
can build them.

**Agent binary** (cross-compiled for Linux):

```bash
make build-agent        # -> backend/agent-linux (GOOS=linux GOARCH=amd64)
# publish to your bucket as agent/ocm-agent + a manifest the agent self-update reads
```

**Guest kernel** — use a Firecracker-compatible `vmlinux` (see the
[Firecracker docs](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md)
for building one, or use a known-good prebuilt kernel) and place it at
`/var/lib/ocm/vmlinux` on the host (or host it at `OCM_ARTIFACT_BUCKET/vmlinux`
so `provision-host.sh` pulls it automatically).

> A GitHub Action to build and publish `ocm-agent` + the kernel to the artifact
> bucket is planned. Until then, build and upload them once, or point
> `OCM_ARTIFACT_BUCKET` at a bucket that already has them.

---

## Where to go next

- [Architecture](architecture.md) — full data-plane, routing, tunnel, lifecycle design
- [Control plane profiles](control-plane-profiles.md) — `local` / `operator` / `hosted`
- [Self-hosted control plane](self-hosted-control-plane.md) — Cloudflare + auth prerequisites
- [Host enrollment](host-enrollment.md) — the enrollment path in depth
- [Local + BYO-host setup](local-setup.md)
