# Build & Deploy Process

## Overview

OpenClaw Machines uses an artifact-based deployment model. All build artifacts are stored in GCS and pulled by hosts at runtime. There are no VM snapshots — host VMs are stateless and bootstrap themselves from GCS on boot.

### Artifacts

| Artifact | Built from | Stored at | Pulled by |
|----------|-----------|-----------|-----------|
| `rootfs.ext4.zst` | `rootfs/Dockerfile.openclaw` + agent + authproxy + scripts | `gs://YOUR-ARTIFACT-BUCKET/rootfs/` | Agent on host VMs (on boot / periodically) |
| `agent` | `backend/cmd/agent/` | `gs://YOUR-ARTIFACT-BUCKET/agent/` | Agent self-update on host VMs |
| `ocm` CLI | `cli/` | `gs://YOUR-ARTIFACT-BUCKET/cli/` | Developers / customers |
| `authproxy` | `backend/cmd/authproxy/` | Baked into rootfs | Not distributed separately |
| Backend image | `backend/Dockerfile` | Container registry | Control plane |
| Frontend image | `frontend/Dockerfile` | Container registry | Control plane |

### GCS Layout

```
gs://YOUR-ARTIFACT-BUCKET/
├── rootfs/
│   ├── manifest.json                    ← latest pointer
│   ├── manifest-{version}.json          ← versioned (for rollback)
│   └── rootfs-{version}.ext4.zst        ← zstd-compressed ext4
├── agent/
│   ├── manifest.json                    ← latest pointer
│   ├── manifest-{version}.json
│   └── agent-{version}                  ← static Linux binary
└── cli/
    ├── manifest.json                    ← latest pointer
    ├── manifest-{version}.json
    └── ocm-{version}-{os}-{arch}        ← per-platform binaries
```

All manifests follow the same schema:

```json
{
  "version": "abc1234-20260224T120000Z",
  "sha256": "e3b0c44...",
  "size_bytes": 52428800,
  "url": "gs://YOUR-ARTIFACT-BUCKET/agent/agent-abc1234-20260224T120000Z",
  "built_at": "2026-02-24T12:00:00Z"
}
```

Rootfs manifests include additional fields: `compressed_size_bytes`, `compression`, `openclaw_version`, `agent_version`, `data_version`.

---

## Build Pipeline

### `make build-components`

Builds all artifacts and uploads to GCS. **Must run on a Linux machine** (rootfs build requires Docker, mkfs.ext4, mount).

```
┌─────────────────────────────────────────────────────────┐
│  make build-components                                   │
│                                                          │
│  1. Build Go binaries (agent, authproxy, CLI)            │
│     └─ CGO_ENABLED=0 GOOS=linux go build ...             │
│                                                          │
│  2. Build OpenClaw fork                                  │
│     └─ pnpm install + build + pack → openclaw-fork.tgz   │
│                                                          │
│  3. Build rootfs                                         │
│     ├─ Docker build (Dockerfile.openclaw)                │
│     ├─ Docker export → ext4 image                        │
│     └─ Inject agent, authproxy, init script, OCM scripts │
│                                                          │
│  4. Upload to GCS                                        │
│     ├─ rootfs: zstd compress → gs://YOUR-ARTIFACT-BUCKET/rootfs/
│     ├─ agent: → gs://YOUR-ARTIFACT-BUCKET/agent/             │
│     └─ CLI:   → gs://YOUR-ARTIFACT-BUCKET/cli/               │
│                                                          │
│  5. Update manifests (versioned + latest pointer)        │
└─────────────────────────────────────────────────────────┘
```

### Deploy Services

After `build-components`, deploy the control plane and worker:

```bash
make deploy-backend    # Backend → your control-plane deployment (reads rootfs manifest URI from env)
make deploy-frontend   # Frontend → your control-plane deployment
make deploy-worker     # Worker → Cloudflare
```

### Full Deploy (everything)

```bash
make build-components  # Build all artifacts → GCS
make deploy-all        # Deploy backend + frontend + worker
```

Agents on host VMs pick up new rootfs and agent binaries automatically — no manual intervention needed.

---

## Host VM Bootstrap

Host VMs boot from a minimal base image containing only: Linux kernel, KVM, systemd, Docker, gsutil, and the `ocm-agent` systemd service. Everything else is pulled from GCS at runtime.

### Boot Sequence

```
Host VM boots
  │
  ├─ systemd starts ocm-agent.service
  │
  ├─ Agent checks gs://YOUR-ARTIFACT-BUCKET/agent/manifest.json
  │   ├─ Version matches local binary? → continue
  │   └─ Newer version available?
  │       ├─ Download to /tmp/agent-new
  │       ├─ Verify SHA256
  │       ├─ mv → /usr/local/bin/agent
  │       └─ systemctl restart ocm-agent (self-replace)
  │
  ├─ Agent registers with control plane
  │
  └─ On first VM creation request:
      ├─ Agent checks gs://YOUR-ARTIFACT-BUCKET/rootfs/manifest.json
      │   ├─ Cached rootfs matches manifest SHA256? → use cache
      │   └─ Download + decompress (zstd) + verify SHA256
      └─ Boot MicroVM from rootfs
```

### Agent Self-Update

The agent checks for updates on startup and periodically (every 5 minutes):

1. Fetch `gs://YOUR-ARTIFACT-BUCKET/agent/manifest.json`
2. Compare `version` field against compiled-in version
3. If newer:
   - Download binary to `/tmp/agent-{version}`
   - Verify SHA256 checksum
   - Atomic rename to `/usr/local/bin/agent`
   - `systemctl restart ocm-agent` — systemd launches the new binary
4. If same version: no-op

The rootfs update uses the existing `rootfs.EnsureRootfs()` which follows the same pattern (manifest check → cache check → download → decompress → verify → atomic rename).

### Minimal Base Image

The base image for host VMs is intentionally thin:

| Component | Purpose |
|-----------|---------|
| Ubuntu 22.04 | OS |
| KVM / `/dev/kvm` | Firecracker hypervisor |
| Docker | Rootfs builds (if building locally) |
| `gsutil` / gcloud SDK | Pull artifacts from GCS |
| `systemd` | Service management |
| `ocm-agent.service` | Agent systemd unit |
| `zstd` | Rootfs decompression |

No application code, no rootfs, no snapshots. Everything is pulled from GCS.

---

## Rollback

### Rootfs Rollback

```bash
make list-rootfs                           # Show available versions
make rollback-rootfs VERSION=abc1234-...   # Point manifest.json to older version
```

Agents pick up the rollback on next rootfs check. Running MicroVMs are unaffected — only new VMs use the rolled-back rootfs.

### Agent Rollback

```bash
make list-agent                            # Show available versions
make rollback-agent VERSION=abc1234-...    # Point manifest.json to older version
```

Running agents will detect the "newer" (actually older) manifest on their next check cycle and self-update to the rolled-back version.

### Backend/Frontend Rollback

Redeploy the previous backend/frontend image revision on your control-plane host. The exact command depends on how you run the control plane (container orchestrator, systemd unit, etc.).

---

## Migration from Snapshots

The snapshot-based workflow (`create-snapshot.sh`, `.snapshot` file, `FC_SNAPSHOT_NAME` env var) is being replaced by GCS artifact distribution.

### Bootstrap

One final snapshot is required to ship the agent self-update feature. After that:

1. New host VMs boot from the base image (no snapshot needed)
2. Agent self-updates from GCS
3. Rootfs pulled from GCS (already implemented)
4. `create-snapshot.sh` and `.snapshot` file are deprecated
5. `validate` target no longer checks for GCP compute snapshots

### What's Deprecated

| Old | New |
|-----|-----|
| `make snapshot VM=ocm` | `make build-components` |
| `make snapshot-vm-full VM=ocm` | `make build-components` |
| `create-snapshot.sh` | Replaced by GCS upload |
| `.snapshot` file | GCS manifests |
| `FC_SNAPSHOT_NAME` env var | `ROOTFS_GCS_MANIFEST` env var |
| GCP compute snapshots | GCS artifacts |
| VM-specific builds | Stateless hosts, artifacts in GCS |

---

## GCS Bucket

- **Bucket:** `gs://YOUR-ARTIFACT-BUCKET`
- **Region:** `us-central1`
- **Access:** `public_access_prevention: enforced`, `uniform_bucket_level_access: true`
- **Write:** Project editors/owners only (CI/CD, admin)
- **Read:** Agent VMs via project-level `roles/storage.objectViewer`
- **TODO:** Harden IAM — dedicated service account for VMs with read-only on this bucket only (see `TODO.md`)

---

## See Also

- [Architecture](architecture.md) — split-plane architecture, data model, scheduling
