# Rootfs Distribution & Image Management

**Date:** 2026-02-09
**Status:** Research (not yet implemented)
**Related:** `docs/research/firecracker-oci-integration.md` (detailed technology survey)

---

## Problem Statement

The current rootfs lifecycle is fragile, unversioned, and tightly coupled to GCE snapshots:

```
Docker image → ext4 file → bake into GCE snapshot → host boot disk → io.Copy to XFS → reflink per VM
```

**Issues:**
1. **No checksums or integrity verification** — rootfs.ext4 is never validated before use
2. **No versioning** — host has whatever was in the GCE snapshot, no way to verify which version
3. **Coupled to GCE snapshots** — can't update rootfs without replacing hosts or SSH
4. **`refresh-rootfs` is a no-op** — just re-copies the file already on the boot disk (same version)
5. **No rollback** — if a bad rootfs ships, must create new snapshot and replace all hosts

---

## Current Architecture

### Rootfs Supply Chain

```
scripts/build-rootfs.sh
  │
  ├── Docker build from rootfs/Dockerfile.openclaw
  │     (Node.js 22, clawdbot gateway, Playwright, Chromium, seed-entropy)
  │
  ├── docker export → tar → extract into ext4 image (dynamic size + 1GB buffer)
  │
  ├── Inject scripts/init-openclaw.sh → /sbin/overlay-init
  ├── Inject agent binary → /usr/local/bin/agent
  ├── Inject utility scripts (ocm-metadata, ocm-test-llm, ocm-env)
  │
  └── Output: /var/lib/ocm/images/rootfs.ext4 (~3-4GB)
```

### How Rootfs Gets to Hosts

```
~cloudbuild.yaml (or scripts/create-snapshot.sh)
  │
  ├── Build rootfs.ext4 on snapshot VM
  ├── Place at /var/lib/ocm/images/rootfs.ext4 (ext4 boot disk)
  ├── Create GCE snapshot of entire VM disk
  │
  └── Snapshot name: ocm-snapshot-{timestamp} (e.g., ocm-snapshot-20260208-080316)

provisioner.go
  │
  ├── Creates GCE instance FROM snapshot (boot disk = clone of snapshot)
  ├── /var/lib/ocm/images/rootfs.ext4 already present on boot disk
  └── Stores snapshot name as source_image in hosts table
```

### Per-VM Rootfs Lifecycle

```
Agent startup (firecracker_linux.go):
  stageBaseRootfs()
    /var/lib/ocm/images/rootfs.ext4 (ext4 boot disk)
      → cp → /var/lib/ocm/vms/.base-rootfs.ext4 (XFS scratch disk)
    Skips if size + mtime match (no checksum)

Per-VM Create():
  reflinkCopy(.base-rootfs.ext4 → {machineID}.ext4)    ~5-20ms (instant CoW on XFS)
  Firecracker boots with {machineID}.ext4 as /dev/vda
```

### Version Tracking (Current)

| Mechanism | What it tracks |
|-----------|---------------|
| `.snapshot` file | Current snapshot name (timestamp-based) |
| `hosts.source_image` column | Which snapshot created each host |
| `scheduler.expectedImage` | Only places VMs on hosts matching current snapshot |
| `stageBaseRootfs()` size/mtime check | Whether staged copy matches source (no checksum) |

**What's missing:** No SHA256, no content verification, no way to detect corruption or drift.

### refresh-rootfs (Current — Broken)

```
POST /api/admin/hosts/{id}/refresh-rootfs
  → agent handler (handlers.go:289):
      io.Copy(
        src: /var/lib/ocm/images/rootfs.ext4,    ← on boot disk (from snapshot)
        dst: /var/lib/ocm/vms/.base-rootfs.ext4   ← XFS staging
      )
```

**Problem:** Source is on the boot disk, which was baked into the GCE snapshot. Refresh just re-copies the same file. To get a NEW rootfs, you'd need to either:
1. Replace the host (new snapshot)
2. Somehow get the new rootfs.ext4 onto the host's boot disk first

---

## VM Startup Timing Breakdown

```
Step                          Time         Source
──────────────────────────────────────────────────────────────
API receives POST /start      ~0ms        server.go
DB placement transaction      ~20-50ms    postgres.go (Neon, remote)
Agent HTTP call (CreateVM)    ~50-100ms   agentclient → host:9090
Reflink copy rootfs           ~5-20ms     cp --reflink=auto (XFS CoW)
ensureDataVolume              ~0ms        reuse existing (or ~200-500ms first time)
Firecracker boot              ~125ms      machine.Start()
──── VM kernel boots, init-openclaw.sh runs as PID 1 ────
Mount filesystems             ~10ms       /proc, /sys, /dev, tmpfs
Mount data volume             ~10-50ms    blkid + mount
Seed entropy                  ~50ms       seed-entropy binary
Network setup                 ~10ms       ip addr/route
Wait for metadata svc         ~100-200ms  curl loop, 0.2s sleep
Fetch config + LLM keys       ~50ms       curl metadata
Start PTY server              ~100ms      agent --pty-server
Start gateway (clawdbot)      ~2-10s      Node.js cold start     ← BOTTLENECK
──── waitForPort(7681, 120s timeout) succeeds ────
Poll detects "running"        ~3s         pollVMStatus, 3s interval  ← BOTTLENECK
Sync KV route                 ~100-200ms  Cloudflare KV write
──── Machine status = "running" ────

Total: ~5-15 seconds
```

**Key finding:** Rootfs-related steps (reflink + staging) are ~20ms total. The bottleneck is Node.js gateway cold start (2-10s) and the 3s polling interval.

---

## Industry Research

### How Fly.io Does It

| Concern | Fly.io | OpenClaw (current) |
|---------|--------|--------------------|
| Image source | OCI registry (`registry.fly.io`, S3-backed) | Baked into GCE snapshot |
| Distribution | Pull-based — worker pulls on demand | Pre-baked into host boot disk |
| Conversion | containerd + devmapper → LVM2 thin-provisioned block devices | Docker export → ext4 file |
| CoW | OverlayFS (since April 2024): read-only base + per-VM upper layer | Reflink (cp --reflink=auto on XFS) |
| Versioning | SHA256 content-addressable digests (OCI standard) | Snapshot name (timestamp) |
| Caching | containerd lease-based GC | Manual staging (size/mtime check) |
| Cold start | 10-20s (registry pull). Warm: sub-second | 5-15s (all local) |
| Init | Rust-based, /dev/vda = config, /dev/vdb = rootfs | Bash, /dev/vda = rootfs |

### How AWS Lambda Does It

| Concern | Lambda |
|---------|--------|
| Distribution | Block-level lazy loading — only 6.4% of image needed to start |
| Integrity | Convergent encryption: SHA256 each 512KB chunk, key = content hash |
| Dedup | Multi-tenant block-level dedup via content-addressing |
| SOCI | Seekable OCI index alongside image — lazy pulling without modification |

### How E2B Does It

| Concern | E2B |
|---------|-----|
| CoW | OverlayFS: read-only base + per-VM tmpfs upper |
| Scale | 150 VMs/sec/host |
| Disk | Thousands of instances share one base rootfs |

### Integrity Patterns

| Method | Description | Used by |
|--------|-------------|---------|
| OCI digests | SHA256 of every layer, manifest, config | All OCI registries |
| dm-verity | Block-by-block Merkle tree, kernel verifies on read | Android, ChromeOS |
| Cosign/Notary | Cryptographic signing of OCI manifests | Supply chain security |
| SOCI index | Separate zTOC per layer with checksums | AWS Fargate |

---

## Recommended Options

### Option A: GCS as Image Store (Simplest Improvement)

Decouple rootfs from GCE snapshots. Store in GCS with checksums.

```
Build:    Docker → ext4 → sha256sum → upload to GCS bucket
                                        ├── gs://ocm-images/rootfs-{sha256}.ext4
                                        └── gs://ocm-images/latest.json
                                              {"sha256": "abc123", "version": "v42", "built_at": "..."}

Host:     Agent startup or refresh-rootfs:
            1. Read latest.json from GCS
            2. Compare SHA256 with local staged copy
            3. If different: download new rootfs, verify checksum, stage to XFS
            4. Reflink per VM (same as now)
```

**Changes required:**
- Build pipeline: upload rootfs + checksum to GCS after build
- Agent config: add `FC_IMAGE_BUCKET` env var
- `refresh-rootfs` handler: download from GCS + verify SHA256 + stage
- Agent startup: check for updates on boot (optional)

**Impact on startup:**
- Per-VM: **no change** (still reflink from local staged copy)
- Host refresh: 30-60s download (one-time, admin-triggered or on agent boot)

**Cost:** ~$0.02/GB/month for a 4GB rootfs. Negligible.

**Advantages:**
- Rootfs updates without replacing hosts
- SHA256 integrity verification
- Version tracking (latest.json)
- Rollback by changing latest.json
- GCS versioning for audit trail

### Option B: OCI Registry (Artifact Registry on GCP)

Same as Option A but uses Google Artifact Registry instead of a raw GCS bucket.

```
Build:    oras push us-docker.pkg.dev/project/ocm/rootfs:v42 rootfs.ext4
Host:     oras pull us-docker.pkg.dev/project/ocm/rootfs:v42 --verify
```

**Advantages over GCS:**
- Content-addressable by SHA256 digest (immutable)
- Standard tooling (oras, crane, skopeo)
- Cosign signing for supply chain verification
- Same registry for Docker images + rootfs artifacts

**Disadvantages:** More moving parts. Requires oras binary on hosts.

### Option C: OverlayFS (Architectural Improvement)

Switch from reflink copies to read-only shared rootfs + OverlayFS.

```
Host has ONE base rootfs (read-only, shared across all VMs)
Each VM gets:
  /dev/vda = base rootfs (is_read_only: true in Firecracker config)
  /dev/vdb = data volume (persistent, already implemented)
  init uses OverlayFS: lower=rootfs(ro) + upper=tmpfs(rw) → merged /
```

**Changes required:**
- `init-openclaw.sh`: add OverlayFS mount before everything else
- `firecracker_linux.go`: set rootfs drive `IsReadOnly: true`, no reflink copy needed
- Remove per-VM rootfs copy entirely

**Impact on startup:**
- Saves ~5-20ms per VM (skip reflink copy)
- Main benefit is disk space, not speed

**Impact on disk:**
- Current: 10 VMs = 10 × 4GB rootfs copies (reflink shares blocks, but metadata overhead)
- OverlayFS: 1 × 4GB shared base + 10 × tiny upper layers

**Pairs well with** Option A or B for distribution.

---

## What Would Actually Speed Up Startup

None of the rootfs options meaningfully change the 5-15s startup. The bottlenecks are elsewhere:

| Bottleneck | Current | Fix | Savings |
|-----------|---------|-----|---------|
| Gateway cold start | 2-10s | Firecracker snapshot/restore (Phase 3) | **~8s** |
| Poll interval | 3s fixed | Reduce to 1s or use agent push (webhook) | **~2s** |
| Metadata wait | 100-200ms | Pre-register before boot | ~100ms |
| Reflink copy | 5-20ms | OverlayFS | ~15ms |

**Firecracker snapshots** (Phase 3) would skip the entire init script by restoring from a pre-booted snapshot. CodeSandbox achieves ~2s VM clones this way. That's the real startup optimization.

---

## Recommendation

**Short term (alongside persistence work):** Option A — GCS bucket with SHA256 checksums.

Minimal changes, solves the core problems:
1. Build pipeline uploads `rootfs.ext4` + `rootfs.ext4.sha256` to GCS
2. `refresh-rootfs` downloads from GCS, verifies hash, stages to XFS
3. Agent stores current rootfs version (hash) for comparison
4. No more dependency on GCE snapshots for rootfs-only updates

**Medium term:** Option C — OverlayFS. Natural fit with the existing data volume on `/dev/vdb`. Decouples ephemeral rootfs from per-VM storage.

**Long term:** Firecracker snapshots (Phase 3) for sub-second startup. Snapshot a fully-booted VM with gateway running, restore instead of cold boot.

---

## Sources

### Papers
- [Firecracker: Lightweight Virtualization for Serverless Applications (NSDI 2020)](https://www.usenix.org/conference/nsdi20/presentation/agache)
- [On-demand Container Loading in AWS Lambda (USENIX ATC 2023)](https://www.usenix.org/conference/atc23/presentation/brooker)

### Platform Engineering
- [Fly.io: Docker without Docker](https://fly.io/blog/docker-without-docker/)
- [Fly.io: A new way to RootFS (OverlayFS, April 2024)](https://community.fly.io/t/a-new-way-to-rootfs/19196)
- [E2B: Scaling Firecracker Using OverlayFS](https://e2b.dev/blog/scaling-firecracker-using-overlayfs-to-save-disk-space)
- [CodeSandbox: How we clone a running VM in 2 seconds](https://codesandbox.io/blog/how-we-clone-a-running-vm-in-2-seconds)
- [Cloudflare: Container Platform Preview](https://blog.cloudflare.com/container-platform-preview/)

### Open Source Projects
- [firecracker-containerd](https://github.com/firecracker-microvm/firecracker-containerd)
- [superfly/init-snapshot (Fly.io Rust init)](https://github.com/superfly/init-snapshot)
- [AWS SOCI Snapshotter](https://github.com/awslabs/soci-snapshotter)
- [Nydus Image Service](https://github.com/containerd/nydus-snapshotter)
- [Kata Containers + Firecracker](https://github.com/kata-containers/kata-containers/blob/main/docs/how-to/how-to-use-kata-containers-with-firecracker.md)
- [Flintlock (OCI-native MicroVM manager)](https://github.com/liquidmetal-dev/flintlock)

### Integrity & Verification
- [dm-verity — Linux Kernel](https://docs.kernel.org/admin-guide/device-mapper/verity.html)
- [ORAS — OCI Registry As Storage](https://oras.land/)
- [Sigstore Cosign](https://docs.sigstore.dev/cosign/overview/)
