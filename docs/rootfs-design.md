# Rootfs Design: Persistence Model

> "Every MicroVM boots from a shared Firecracker snapshot. The snapshot is not the source of truth — all long-lived state must survive migrations, restarts, and snapshot upgrades."

---

## Architecture Overview

Each MicroVM has two storage layers:

| Layer | Mount | Lifecycle | Analogy (Hetzner) |
|-------|-------|-----------|-------------------|
| **Rootfs** | `/` | Ephemeral — fresh from shared snapshot each boot | Docker image layer |
| **Data volume** | `/data/` (`/dev/vdb`, ext4) | Persistent — survives migration, restart, snapshot upgrade | Host volume mounts |

The init script (`/sbin/overlay-init`) symlinks persistent directories from the data volume into the rootfs:
- `/home/openclaw` → `/data/home/openclaw`
- `/workspace` → `/data/workspace`

---

## Current State: What Persists Where

### Persistent (data volume — survives everything)

| What | Path | Notes |
|------|------|-------|
| Gateway configuration | `~/.openclaw/openclaw.json` | Written on first boot, preserved on restarts |
| Auth profiles / API keys | `~/.openclaw/` | User runs `openclaw setup` to configure |
| Skill definitions | `~/.openclaw/skills/*.md` | Managed skill files |
| Extensions / plugins | `~/.openclaw/extensions/{id}/` | Self-contained with own `node_modules/` |
| Downloaded tool binaries | `~/.openclaw/tools/{skill-key}/` | Skills with `kind: "download"` |
| Agent workspace | `/workspace/` | Code, repos, execution artifacts |
| Go binaries | `~/go/bin/` | Skills with `kind: "go"` |
| Python/uv tools | `~/.local/share/uv/tools/` | Skills with `kind: "uv"` |
| OpenClaw state (SQLite) | `~/.openclaw/` | Agent memory, history |
| SSH host keys | `~/.ssh-host-keys/` | Restored to `/etc/ssh/` on boot; prevents host key mismatch after migration |

### Rootfs (snapshot — rebuilt on snapshot upgrade)

| What | Path | Notes |
|------|------|-------|
| Node.js 22 | `/usr/local/` | Base runtime from `node:22-slim` |
| openclaw gateway | `/root/.local/share/pnpm/` | `pnpm install -g openclaw@X.Y.Z` |
| pnpm | `/root/.local/share/pnpm/` | Corepack-managed |
| cloudflared | `/usr/local/bin/cloudflared` | Per-VM Cloudflare tunnel |
| gh (GitHub CLI) | `/usr/bin/gh` | For GitHub skill |
| agent + authproxy | `/usr/local/bin/` | Injected by `build-rootfs.sh` |
| System packages | `/usr/bin/`, `/usr/lib/` | curl, git, jq, procps, openssh-server, iproute2, iputils-ping |

### Rebuilt every boot (stateless, no persistence needed)

| What | Path | Notes |
|------|------|-------|
| CF Access SSH CA pubkey | `/etc/ssh/cf_ca.pub` | Fetched fresh from metadata service |
| sshd config (CF Access) | `/etc/ssh/sshd_config` | Appended by init script |
| Cloudflare tunnel | (in-memory) | `--token` mode, stateless — token from metadata |
| `/etc/profile.d/*.sh` | `/etc/profile.d/` | Written by init script every boot |

### Ephemeral (lost on migration)

| What | Path | Why it's lost |
|------|------|---------------|
| npm global packages | `/usr/local/lib/node_modules/` | On rootfs, not data volume |
| npm global binaries | `/usr/local/bin/` | On rootfs, not data volume |
| `/tmp/` contents | `/tmp/` | tmpfs, cleared each boot |

---

## How OpenClaw Skills Install

OpenClaw skills support multiple install methods. Each has different persistence characteristics in our MicroVM:

### Install methods and persistence

| Install kind | How it works | Where it goes | Persists? | Skill count |
|---|---|---|---|---|
| `download` | curl binary from GitHub release → extract | `~/.openclaw/tools/{key}/` | Yes | 1 |
| `go` | `go install module@latest` | `~/go/bin/` | Yes | 9 |
| `uv` | `uv tool install` | `~/.local/share/uv/tools/` | Yes | 1 |
| `node` | `npm install -g` | `/usr/local/lib/node_modules/` | **No** | 3 |
| `brew` | `brew install formula` | N/A — brew not in MicroVM | **N/A** | 25 |

### The brew problem

25 of 39 skills only define `brew` install specs. Brew is not available in our Linux MicroVM. Most of these are **Go or Rust binaries distributed via Homebrew taps** — the brew formula just downloads a pre-built binary from GitHub releases.

The Hetzner self-hosted guide solves this by downloading binaries directly at Docker build time:

```dockerfile
# Hetzner pattern — no brew, just curl the binary
RUN curl -L https://github.com/steipete/gog/releases/latest/download/gog_Linux_x86_64.tar.gz \
  | tar -xz -C /usr/local/bin && chmod +x /usr/local/bin/gog
```

### Plugins are not affected

Plugins/extensions are self-contained under `~/.openclaw/extensions/{id}/` with their own `node_modules/` directory. Dependencies are installed via `npm install` inside the plugin directory, not globally. No persistence gap.

---

## Gap Analysis vs Hetzner Self-Hosted

| Hetzner (self-hosted) | OCM Platform | Status |
|-----------------------|--------------|--------|
| Config in host-mounted `~/.openclaw/` | `~/.openclaw/` on data volume | **Parity** |
| Skills in host-mounted `~/.openclaw/skills/` | `~/.openclaw/skills/` on data volume | **Parity** |
| Plugins in host-mounted `~/.openclaw/extensions/` | `~/.openclaw/extensions/` on data volume | **Parity** |
| Binaries baked into Docker image at build time | Rootfs snapshot (same concept) | **Parity** |
| User rebuilds Docker image to add tool binaries | User cannot rebuild snapshot | **Gap** — we must pre-bake |
| npm globals survive container restart (same image) | npm globals lost on migration (new rootfs) | **Gap** — 3 skills affected |
| Brew available (Linuxbrew) | Brew not installed | **Gap** — 25 skills have no Linux install path |

---

## Future State: Proposed Changes

### 1. Pre-bake common skill binaries into rootfs (Hetzner pattern)

Download pre-built Linux binaries directly from GitHub releases in the Dockerfile. This is exactly what Hetzner users do — no brew needed.

Pre-baked in the Dockerfile (pinned versions):

| Skill | Binary | Version | Source |
|---|---|---|---|
| gog (Gmail) | `gog` | 0.6.0 | steipete/gog |
| goplaces | `goplaces` | 0.3.0 | steipete/goplaces |
| gifgrep | `gifgrep` | 0.2.1 | steipete/gifgrep |
| himalaya | `himalaya` | 1.1.0 | pimalaya/himalaya |
| 1password | `op` | 2.32.1 | 1Password CDN |

**Skipped — no Linux binary available**: summarize (macOS only), wacli (macOS only).

**Skipped — no hardware in VM**: blucli (Bluetooth), camsnap (camera), songsee (audio), sag (TTS), openhue (smart home), spotify-player (audio), eightctl (IoT).

**Skipped — macOS APIs**: apple-notes, apple-reminders, imsg, peekaboo.

### 2. Redirect npm globals to data volume

Set environment variables so the 3 `kind: "node"` skills (`clawhub`, `mcporter`, `@steipete/oracle`) persist:

Add `/etc/profile.d/openclaw-user-installs.sh` in the init script:

```bash
export NPM_CONFIG_PREFIX="$HOME/.local"
export PATH="$HOME/.local/bin:$PATH"
```

This redirects `npm install -g` to `~/.local/lib/node_modules/` + `~/.local/bin/` — both on the data volume.

### What persists after changes

| What | Path | Persists? |
|------|------|-----------|
| *All current persistent items* | — | Yes |
| npm global packages | `~/.local/lib/node_modules/` | **Yes** (was: No) |
| npm global binaries | `~/.local/bin/` | **Yes** (was: No) |
| Pre-baked skill binaries | `/usr/local/bin/` | Rootfs (rebuilt with snapshot) |

### PATH order

```
$HOME/.local/bin            ← user npm globals + user binaries (data volume)
/root/.local/share/pnpm     ← system openclaw (rootfs, read-only)
/usr/local/bin               ← pre-baked skill binaries, agent, authproxy, cloudflared
/usr/bin:/bin                ← OS packages
```

The gateway process is started by the init script with an explicit PATH — not affected by user PATH changes.

---

## What Changes

| File | Change |
|------|--------|
| `rootfs/Dockerfile.openclaw` | Add `curl \| tar` blocks for common skill binaries |
| `scripts/init-openclaw.sh` | Create `~/.local/bin` on data volume; write `/etc/profile.d/openclaw-user-installs.sh` with `NPM_CONFIG_PREFIX`; bump `ROOTFS_DATA_VERSION` to 2 |
| Snapshot | Full rebuild required |

---

## Beyond the Snapshot: Future Options for Advanced Users

The changes above (pre-baked binaries + npm redirect) cover skill installs and most user needs. But some users will want databases, custom system packages, or full environment control. Three options, in order of complexity:

### Option 1: External Add-on Services (recommended first)

Don't run heavy services inside the VM. Provide them externally, like Heroku add-ons or Railway services. Credentials injected via the metadata service at boot.

| Add-on | What the VM gets | Provider |
|---|---|---|
| Postgres | `DATABASE_URL` | Neon (already integrated) |
| Redis | `REDIS_URL` | Upstash |
| Search | `SEARCH_URL` + API key | Typesense / Meilisearch |
| Object storage | `S3_ENDPOINT` + credentials | S3-compatible (Cloudflare R2, etc.) |

**Pros**: VM stays lean. No rootfs changes. Shared snapshot works for everyone. Services are managed, backed up, scaled independently.

**Cons**: Adds latency (network hop). Limited to services we integrate. Users can't run arbitrary daemons.

### Option 2: Docker/Podman inside the VM (pro feature)

Enable Docker or Podman in the MicroVM. Users run whatever they want as containers alongside the agent.

```bash
docker run -d -e POSTGRES_PASSWORD=secret postgres
docker run -d redis
```

Container data stored on the data volume (`/data/docker/`), persists across migration.

**Pros**: Maximum flexibility. No build pipeline. No per-user snapshots. Users already know Docker. Dynamic — start/stop services without rebuilding.

**Cons**: Firecracker's minimal kernel may need cgroup/namespace support verified. Docker daemon adds ~100-200MB memory overhead. Docker images consume data volume space. Podman (rootless, daemonless) may be a better fit for constrained VMs.

### Option 3: Custom Dockerfile / rootfs (pro feature)

Let advanced users provide a Dockerfile extending our base image. We build a custom rootfs and store it in GCS.

```dockerfile
FROM ocm-base:latest

RUN apt-get update && apt-get install -y postgresql python3-pip
RUN pip install pandas numpy
RUN curl -L https://example.com/custom-tool.tar.gz | tar -xz -C /usr/local/bin
```

**How it works:**

Today, the rootfs is baked into the snapshot on each worker VM's local disk — one shared image for all VMs. Custom rootfs can't work that way because you'd need to push per-user images to every worker.

Instead, store rootfs images in GCS and pull on demand:

1. User pushes a Dockerfile (via UI, CLI, or git repo)
2. Build service (Cloud Build) creates rootfs ext4 image from `ocm-base` + user's Dockerfile
3. Rootfs image uploaded to GCS (`gs://ocm-rootfs-images/{account}/{image_id}.ext4.zst`)
4. On VM start: worker pulls the user's rootfs from GCS instead of using the shared local copy
5. Data volume attached as usual — user data + installs persist independently

This also decouples rootfs distribution from worker deploys — update a rootfs in GCS and all new VMs pick it up without redeploying workers. The pattern already exists: test infrastructure auto-downloads kernel + rootfs images from GCS on first run (see `docs/TESTING.md`).

```
GCS rootfs storage:
gs://ocm-rootfs-images/
  shared/
    base-v{version}.ext4.zst          ← default rootfs (current shared snapshot)
  {account_id}/
    {image_id}.ext4.zst               ← custom rootfs
    {image_id}.manifest.json          ← Dockerfile hash, base version, build time
```

**Caching:** Workers cache recently-used rootfs images locally. Reflink copy from cached image — same fast path as today. Cache eviction based on LRU when disk space is low.

**Base image updates:** When we release a new `ocm-base` version, trigger rebuilds of all custom images. Store the base version in the manifest so we know which custom images are stale.

**Marketplace:** Community-published machine templates stored in a shared GCS prefix. Users pick a template → we clone the rootfs image into their account.

**Pros**: Full control. Reproducible — Dockerfile IS the machine spec. Version it in git, share across teams. Enables marketplace of community templates. No per-worker image distribution.

**Cons**: Requires build infrastructure (Cloud Build). Per-user rootfs storage in GCS. First boot on a new worker is slower (GCS download). When base image updates, all custom images need rebuilding.

**Related design:** See `docs/designs/persistence.md` for the existing GCS-based data volume migration and backup architecture. The custom rootfs storage follows the same pattern.

### Recommended phasing

| Phase | What | Effort |
|---|---|---|
| **Now** | Pre-baked binaries + `NPM_CONFIG_PREFIX` | Small — Dockerfile + init script changes |
| **Next** | External add-on services | Medium — metadata service + UI integration |
| **Later** | Docker/Podman in VM | Medium — kernel config + data volume setup |
| **Future** | Custom rootfs via GCS | Large — build pipeline + GCS storage + worker cache + marketplace |

---

## Additional Risks & Decisions

- Brew-only skills: 25 skills list brew but only a subset are pre-baked. We need a decision matrix per skill: (a) pre-bake from a release asset, (b) swap to an alternate go/rust install, or (c) mark unsupported in cloud VMs.
- Update cadence: Pre-baked binaries are pinned to a snapshot; the doc needs an update/rotation policy so CLIs (e.g., 1password, spotify-player) don’t drift silently. Define how users discover available versions.
- Init env scope: Redirecting `NPM_CONFIG_PREFIX` impacts root and the `openclaw` user—ensure the gateway (runs as root) doesn’t persist unintended npm globals to the shared data volume; clarify which user/session the env applies to.
- Disk quota / cleanup: npm globals and downloaded binaries on the data volume can bloat. A boot-time `du` warning is not enough—document a quota/cleanup policy (e.g., warn/prune above N GB).
- Compatibility list: macOS-only skills are noted, but we need a definitive Linux support matrix so users know which skills work in MicroVMs after these changes.
- Supply chain: Pre-baking GitHub binaries without pinning/checksums is risky. Recommend pinned versions plus checksum verification in Dockerfile snippets.

## Decision Log

| # | Decision | Status | Owner | Due | Notes |
|---|----------|--------|-------|-----|-------|
| D1 | Publish brew-skill decision matrix (pre-bake, alternate install, or unsupported) and maintain alongside skill catalog | OPEN | — | — | 25 brew-only skills need triage; start with most-used |
| D2 | Pin versions and verify checksums for all pre-baked binaries; add update cadence to release checklist | PARTIAL | — | — | cloudflared checksums done; remaining binaries pending |
| D3 | Clarify NPM_CONFIG_PREFIX scoping: confirm which user/session gets the redirect and that gateway/system services are unaffected | OPEN | — | — | Gateway runs as root; must not persist unintended npm globals to data volume |
| D4 | Define data-volume quota/cleanup policy and document pruning for cached tools and npm globals | OPEN | — | — | Boot-time `du` warning alone is insufficient; consider warn/prune threshold |
| D5 | Add “Supported skills on cloud VMs” compatibility table listing yes/no and install source per skill | OPEN | — | — | macOS-only and hardware-dependent skills already identified above |
| D6 | Decide which binaries to pre-bake: full list from design or most-used subset only | OPEN | — | — | Current list: gog, goplaces, gifgrep, himalaya, 1password |
| D7 | Accept snapshot-pinned binary updates as trade-off, or design an in-place update mechanism | OPEN | — | — | New versions currently require full snapshot rebuild |
| D8 | Set GCS rootfs local cache policy: max cached images per worker, LRU eviction threshold | OPEN | — | — | Applies to future custom-rootfs feature (Option 3) |
| D9 | Define custom rootfs build limits: max image size, build timeout, rate limits per account | OPEN | — | — | Applies to future custom-rootfs feature (Option 3) |

---

## See Also

- [architecture.md](architecture.md) — System architecture overview
- [`orchestrator/firecracker_linux.go`](../backend/internal/orchestrator/firecracker_linux.go) — `stageFromGCS` implementation
- [openclaw-version-management.md](openclaw-version-management.md) — Version management and rootfs distribution
