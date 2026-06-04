# Non-GCP Host Provisioning

Guide for provisioning and managing bare-metal hosts (OVH, Hetzner, etc.) as OCM worker nodes.

## Architecture: GCP vs Non-GCP Hosts

The OCM agent was originally designed for GCP Compute VMs. Non-GCP hosts use the enrollment system instead:

| Concern | GCP Path | Non-GCP Path |
|---------|----------|--------------|
| Agent config | GCE metadata (`metadata.google.internal`) | `agent.env` file (systemd `EnvironmentFile`) |
| GCS auth | Attached service account (ADC) | `gcs-key.json` + `GOOGLE_APPLICATION_CREDENTIALS` env var |
| Tunnel token | GCE metadata `tunnel-token` | `TUNNEL_TOKEN` in `agent.env` |
| External IP | GCE metadata network interface | `AGENT_ENDPOINT` env var → `ifconfig.me` fallback |
| Cloudflare tunnel | Agent spawns cloudflared subprocess | Same (agent-managed) |
| Host lifecycle | Provisioner creates GCE VM | Enrollment API creates DB record + Cloudflare tunnel |
| Reconciler | GCP Instance API check | Heartbeat-only checker |

## Current Fleet

| Host | Name | ID | IP | Region | Status |
|------|------|----|----|--------|--------|
| ns1027704 | east | 98 | 15.204.241.166 | us-east-vin (Vint Hill, VA) | Operational |
| ns1028709 | west | 104 | 15.204.104.54 | us-west-hil (Hillsboro, OR) | Operational |
| ns1028714 | west2 | 105 | 15.204.104.201 | us-west-hil (Hillsboro, OR) | Operational |

All servers: ADVANCE-1 / AMD EPYC 4244P, 6c/12t, 64 GB RAM, 2×960 GB NVMe, Ubuntu 24.04.

### Quick Access

Named SSH aliases and Makefile shortcuts are configured for all hosts:

```bash
# SSH directly (aliases in ~/.ssh/config)
ssh ocm-east
ssh ocm-west

# Makefile shortcuts
make ssh-east              # SSH into east host
make ssh-west              # SSH into west host
make status-east           # Check east host health
make status-west           # Check west host health
make logs-east             # Tail east agent logs
make logs-west             # Tail west agent logs
make status-all            # Check all hosts at once

# Long form (equivalent)
make ssh-host HOST=east
make host-status HOST=west
make host-logs HOST=east
```

All host targets accept `HOST=<name>` (east, west) or `HOST_IP=x.x.x.x` for unlisted hosts.

## Adding a New Host — Step by Step

### Step 1: Install OS via OVH Panel

1. Go to [OVH Manager](https://manager.us.ovhcloud.com) → Dedicated Servers → select server
2. Click "..." next to Operating System → "Install my server"
3. Select: **Type**: Basic → **OS**: Ubuntu Server 24.04 "Noble Numbat" LTS
4. Disk group: default (JBOD)
5. Set hostname (e.g., `ocm-west-delta2`)
6. Select **ED25519** key type, paste contents of `~/.ssh/ovh_cloud.pub`
7. Leave Post-Installation Script blank
8. Confirm and wait ~15-20 minutes

### Step 2: Verify SSH Access

```bash
ssh -i ~/.ssh/ovh_cloud ubuntu@<IP> "hostname && uname -r && cat /etc/os-release | grep PRETTY"
```

### Step 3: Provision Host

```bash
make provision-host HOST_IP=<IP>
```

Installs system packages, Firecracker, Google Cloud SDK, cloudflared, creates OCM directories, configures sysctl/limits, and SCPs the Firecracker kernel.

**Verify output shows:**
- `KVM available`
- `Firecracker installed: Firecracker v1.10.1`
- `Kernel installed at /var/lib/ocm/vmlinux`

### Step 4: Create Enrollment Token

The admin API requires superuser JWT auth (`mathewma@gmail.com`). Easiest method — run from the browser console on `openclawmachines.com` (uses session cookie):

```javascript
const r = await fetch('https://api.openclawmachines.com/api/admin/hosts/enrollment-tokens', {
  method: 'POST', credentials: 'include',
  headers: {'Content-Type': 'application/json'},
  body: JSON.stringify({provider: 'ovhcloud', expires_in_hours: 24})
});
console.log(await r.json());
```

Or via curl with a JWT:
```bash
curl -s -X POST https://api.openclawmachines.com/api/admin/hosts/enrollment-tokens \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <JWT>" \
  -d '{"provider": "ovhcloud", "expires_in_hours": 24}' | jq .
```

Save the `token` value (format: `ocm_enroll_<hex>`).

### Step 5: Enroll Host

```bash
make enroll-host HOST_IP=<IP> TOKEN=<enrollment-token>
```

This runs the enrollment install script on the host which:
1. Detects external IPv4 via `ifconfig.me`
2. Registers with backend (`POST /api/agent/register`)
3. Backend creates host record + Cloudflare tunnel
4. Writes `/etc/ocm-agent/agent.env` and `/etc/ocm-agent/gcs-key.json`
5. Downloads agent binary from GCS
6. Creates and starts `ocm-agent` systemd service

**All-in-one** (provision + enroll):
```bash
make setup-host HOST_IP=<IP> TOKEN=<enrollment-token>
```

### Step 5b: Fix GCS Key (If Empty)

The enrollment script may fail to write `gcs-key.json` (known issue — empty file). Check and fix:

```bash
# Check if key file is empty
ssh -i ~/.ssh/ovh_cloud ubuntu@<IP> "sudo wc -c /etc/ocm-agent/gcs-key.json"

# If 0 bytes, fetch from Secret Manager and SCP:
gcloud secrets versions access latest --secret=OCM_GCS_SERVICE_ACCOUNT_KEY --project=clarateach > /tmp/gcs-key.json
scp -i ~/.ssh/ovh_cloud /tmp/gcs-key.json ubuntu@<IP>:/tmp/gcs-key.json
ssh -i ~/.ssh/ovh_cloud ubuntu@<IP> "sudo mv /tmp/gcs-key.json /etc/ocm-agent/gcs-key.json && sudo chmod 600 /etc/ocm-agent/gcs-key.json && sudo systemctl restart ocm-agent"
rm -f /tmp/gcs-key.json
```

### Step 5c: Deploy Agent (If Download Failed)

If enrollment couldn't download the agent binary:

```bash
make deploy-agent-host HOST_IP=<IP>
```

Cross-compiles the agent, SCPs it, and restarts the service.

### Step 6: Verify

```bash
# Once the host is added to the Makefile registry (see "Adding a New Named Host"):
make status-east           # or status-west, etc.
make logs-east             # tail agent logs

# Or by IP for hosts not yet in the registry:
make host-status HOST_IP=<IP>
make host-logs HOST_IP=<IP>
```

Expected healthy state:
- Agent: `active` with current version
- Rootfs: `rootfs.gcs.staged` in logs
- Tunnel: 4 `Registered tunnel connection` entries
- Heartbeat: `heartbeat sent` every 60s

### Step 7: Fix Placement (Required)

Enrolled hosts get `region='external'` and `source_image=NULL`. Both must be set correctly or placement will fail with "no host with matching image and sufficient capacity".

```sql
-- Check current values
SELECT id, region, source_image, status FROM hosts WHERE id = <HOST_ID>;

-- Set region to match actual location (us-east, us-west, etc.)
-- and source_image to match the current rootfs image
UPDATE hosts SET region = '<region>', source_image = '<image>' WHERE id = <HOST_ID>;
```

To find the current source_image value, check an existing working host:
```sql
SELECT DISTINCT source_image FROM hosts WHERE source_image IS NOT NULL AND status = 'ready';
```

**Root cause**: `enrollment.go` does not set `source_image` during host registration. Until that is fixed in code, this step is required for every new enrolled host.

## Troubleshooting

| Problem | Solution |
|---------|----------|
| SSH refused | OS still installing (~15-20 min), wait and retry |
| KVM not available | Hardware virtualization not enabled — open OVH support ticket |
| Enrollment fails | Check backend logs, verify token not expired, verify `OCM_GCS_SERVICE_ACCOUNT_KEY` deployed |
| Agent crash-looping with GCS error | Empty `gcs-key.json` — see Step 5b |
| Agent not starting | `make host-logs HOST_IP=<IP>` — check for missing config |
| Agent binary missing | Enrollment download failed — use `make deploy-agent-host HOST_IP=<IP>` |
| Heartbeat not appearing | Check agent running (`systemctl status ocm-agent`), check network |
| Tunnel not connecting | Check `TUNNEL_TOKEN` in agent.env, check cloudflared binary |
| Rootfs not staging | Agent needs valid GCS credentials + manifest URL |
| Host stuck in `provisioning` | Heartbeat auto-recovers to `ready` — wait for first heartbeat |
| Host stuck in `draining` | Fixed in commit 496a5e7 — heartbeat recovers from draining |

## Known Issues

### Open

- **`BrowserRootfsGCSManifest` not populated for enrolled hosts** — Browser companion VMs won't work. Fix: add to enrollment response and `agent.env`.
- **Enrollment does not set `region`** — Enrolled hosts get `region='external'`. Workaround: manually update DB after enrollment (see Step 7). Fix: accept a `region` parameter in the enrollment token or install script.
- **Enrollment may write empty `gcs-key.json`** — Observed during host 104 setup (2026-03-31). Root cause TBD. Workaround: manually SCP key from Secret Manager.

### Resolved

- ~~Heartbeat stuck in `draining`~~ — Fixed (commit 496a5e7)
- ~~Duplicate cloudflared service~~ — Fixed (commit 496a5e7), agent-managed only
- ~~GCP metadata timeout on non-GCP hosts~~ — Fixed (commit 496a5e7), checks `AGENT_ENDPOINT` first
- ~~IPv6 agent endpoint~~ — Install script forces `-4` for IPv4
- ~~cloudflared hardcoded to amd64~~ — Install script auto-detects architecture
- ~~Enrollment does not set `source_image`~~ — Fixed: `enrollment.go` now sets `source_image` from the placement service's expected image

## Key Files on Host

| Path | Purpose |
|------|---------|
| `/etc/ocm-agent/agent.env` | Agent configuration (tokens, URLs, host ID) |
| `/etc/ocm-agent/gcs-key.json` | GCS service account credentials |
| `/etc/systemd/system/ocm-agent.service` | Systemd unit for agent |
| `/usr/local/bin/ocm-agent` | Agent binary |
| `/usr/local/bin/firecracker` | Firecracker binary |
| `/usr/local/bin/jailer` | Firecracker jailer |
| `/usr/local/bin/cloudflared` | Cloudflare tunnel binary |
| `/var/lib/ocm/vmlinux` | Firecracker kernel |
| `/var/lib/ocm/images/` | Rootfs images |
| `/var/lib/ocm/vms/` | Running VM state |

## Key Source Files

| File | Purpose |
|------|---------|
| `scripts/provision-host.sh` | System dependency installation |
| `backend/internal/api/enrollment.go` | Registration handler, install script template |
| `backend/cmd/agent/main.go` | Agent startup, heartbeat, cloudflared |
| `backend/internal/config/config.go` | AgentConfig, env var mappings |
| `backend/internal/selfupdate/updater.go` | GCS-based self-update |
| `backend/internal/rootfs/gcs.go` | GCS rootfs fetcher |

## Make Targets

All targets accept `HOST=<name>` (east, west) or `HOST_IP=x.x.x.x`. Defaults: `HOST_KEY=~/.ssh/ovh_cloud`, `HOST_USER=ubuntu`.

### Shortcuts (Named Hosts)

| Target | Action |
|--------|--------|
| `make ssh-east` / `make ssh-west` | SSH into named host |
| `make status-east` / `make status-west` | Check named host health |
| `make logs-east` / `make logs-west` | Tail named host agent logs |
| `make status-all` | Check all hosts at once |

### Full Targets

| Target | Usage |
|--------|-------|
| `make ssh-host HOST=east` | SSH into host |
| `make host-status HOST=east` | Check host health + versions |
| `make host-logs HOST=east` | Tail agent logs (live) |
| `make deploy-agent-host HOST=east` | Build + SCP agent binary, restart |
| `make provision-host HOST_IP=x` | Install system deps + SCP kernel (new hosts) |
| `make enroll-host HOST_IP=x TOKEN=y` | Register with control plane |
| `make setup-host HOST_IP=x TOKEN=y` | All-in-one: provision + enroll |

### Adding a New Named Host

1. Add the IP to the Makefile host registry:
   ```makefile
   HOST_EAST_IP := 15.204.241.166
   HOST_WEST_IP := 15.204.104.54
   HOST_NEW_IP  := x.x.x.x        # ← add here
   ALL_HOSTS    := east west new    # ← add name here
   ```
2. Add an SSH alias to `~/.ssh/config`:
   ```
   Host ocm-new
       HostName x.x.x.x
       User ubuntu
       IdentityFile ~/.ssh/ovh_cloud
       StrictHostKeyChecking accept-new
       ConnectTimeout 10
   ```
3. Add shorthand targets to the Makefile (optional):
   ```makefile
   ssh-new:
   	@$(MAKE) ssh-host HOST=new
   status-new:
   	@$(MAKE) host-status HOST=new
   logs-new:
   	@$(MAKE) host-logs HOST=new
   ```

## GCS & Secrets

- **Service Account**: `ocm-gcs-reader@clarateach.iam.gserviceaccount.com` (role: `roles/storage.objectViewer`)
- **Secret**: `OCM_GCS_SERVICE_ACCOUNT_KEY` in GCP Secret Manager (plain JSON, not base64)
- **GCS artifacts**: `gs://openclawmachines/{vmlinux, agent/manifest.json, rootfs/manifest.json}`
- **Deployed to Cloud Run** via `make deploy-backend` (fetches secret, injects as env var)
