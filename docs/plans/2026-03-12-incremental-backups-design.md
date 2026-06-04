# Incremental Backups Design

**Date:** 2026-03-12
**Status:** Draft — design review
**Depends on:** Phase 1 backup system (manual full backups, `2026-03-11-backup-design.md`)

## Problem

Every backup uploads the entire ext4 data volume (up to 10GB) to GCS, even if only a few KB changed since the last backup. This means:

- **Slow backups** — a 2GB volume takes 10-30s even when nothing changed
- **Wasted storage** — 3 retained full copies × 2GB = 6GB per machine in GCS
- **Wasted bandwidth** — OVH→GCS uploads are especially slow for large volumes
- **Can't increase backup frequency** — daily or on-stop backups would multiply the cost and time

## Current Pipeline

```
ext4 file (sparse, up to 10GB)
  → read entire file
  → zstd compress (SpeedDefault, ~3x ratio on ext4)
  → AES-256-CTR encrypt + HMAC-SHA256
  → upload to GCS
```

Every backup is a full copy. Restore is simple: download one file, decrypt, decompress, write.

## Options

### Option 1: Sparse-Aware Full Backup

**What:** Skip zero-filled (unallocated) regions of the ext4 file using `SEEK_DATA`/`SEEK_HOLE` or `e2image -rap`.

**How it works:**
- Before compressing, use `SEEK_DATA`/`SEEK_HOLE` syscalls to identify which byte ranges contain actual data
- Only read and compress data regions, writing a small manifest of (offset, length) pairs alongside the compressed stream
- On restore, write data regions at correct offsets, leaving holes as sparse regions

**Changes required:**
- `backup/gcs.go` `Upload()` — replace sequential file read with sparse-aware reader
- `backup/gcs.go` `Download()` — write data at offsets, preserving sparsity
- GCS object format changes — need a header or sidecar with the hole map

**Pros:**
- Simple conceptually — still a single backup file per snapshot
- Restore is still self-contained (one file = one backup)
- Huge win for fresh/lightly-used volumes (a 10GB volume with 200MB of data → ~200MB read instead of 10GB)
- No backup chains to manage
- No change to retention, deletion, or migration logic
- Works with existing encryption pipeline (just compressing less data)

**Cons:**
- No benefit for volumes where most blocks are written (densely used volumes)
- zstd already compresses zero blocks very well (~1000:1), so the upload size savings are modest — the main win is reduced *read* I/O, not upload size
- Still uploads *all* data every time, even if only 1 byte changed
- New GCS object format — old backups incompatible (need format versioning)
- `SEEK_DATA`/`SEEK_HOLE` behavior varies across filesystems and kernel versions

**Estimated effort:** 2-3 days
**Estimated improvement:** 50-90% faster for sparse volumes, minimal for dense volumes

---

### Option 2: Block-Level Change Tracking (True Incremental)

**What:** Divide the ext4 volume into fixed-size blocks (e.g. 4KB or 64KB), hash each block, compare against the previous backup's block map, and upload only changed blocks.

**How it works:**
1. On first backup, upload full volume + a block hash manifest (SHA-256 per block)
2. On subsequent backups:
   - Read each block, compute its hash
   - Compare against previous backup's manifest
   - Upload only blocks whose hash changed
   - Store: changed-blocks blob + new manifest + reference to parent backup
3. On restore:
   - Download the base backup
   - Apply each incremental layer in order (overwrite changed blocks)

**GCS layout:**
```
backups/{machineID}/
  20260312T100000Z.ext4.zst.enc          ← full backup
  20260312T100000Z.manifest.json         ← block hashes
  20260312T120000Z.inc.zst.enc           ← incremental (changed blocks only)
  20260312T120000Z.manifest.json         ← updated block hashes
  20260312T120000Z.meta.json             ← parent ref, block size, block count
```

**Changes required:**
- `backup/store.go` — new `UploadIncremental()` method, manifest types
- `backup/gcs.go` — incremental upload/download logic, manifest storage
- `backup/blockhash.go` — new file: block reading, hashing, diffing
- `store/postgres.go` — `machine_backups` needs `parent_backup_id`, `backup_type` (full/incremental) columns
- `agentapi/handlers.go` — pass previous manifest to incremental backup
- `api/machine_backups.go` — track backup chains, periodic full backup scheduling
- Migration — new columns on `machine_backups`

**Pros:**
- Dramatic size reduction for small changes (typical dev session changes <5% of blocks)
- Enables frequent backups (on-stop, hourly) without cost/time explosion
- Block-level is filesystem-agnostic — works even if we change from ext4
- Well-understood technique (used by ZFS, Borg, restic, etc.)

**Cons:**
- **Restore complexity** — must download base + all increments in order; any corruption in the chain breaks all subsequent restores
- **Chain management** — need periodic full backups to cap chain length (e.g. full every 7 days, incremental in between)
- **Manifest storage overhead** — a 10GB volume with 4KB blocks = 2.5M hashes × 32 bytes = ~80MB manifest (can compress to ~5MB, but still significant)
- **Read amplification on backup** — must read the *entire* volume to hash every block, even to produce a small incremental (can't avoid this without kernel-level dirty tracking)
- **Deletion complexity** — can't delete a backup in the middle of a chain without rebasing descendants
- **Migration complexity** — `admin_migrate.go` must handle chain of backups, not single file
- **Download format** — `StreamTarGz` and `StreamDecrypted` need to reconstruct full image from chain before serving
- **~500-800 new lines** of code, significant testing surface

**Estimated effort:** 1-2 weeks
**Estimated improvement:** 80-95% smaller incremental uploads, but full read I/O remains

---

### Option 3: File-Level Incremental (rsync-style)

**What:** Mount the ext4 volume read-only, compare file mtimes/sizes against a previous manifest, and tar only changed files.

**How it works:**
1. First backup: full tar.gz of all files + file manifest (path, size, mtime, checksum)
2. Subsequent backups: compare file metadata, tar only new/modified/deleted files
3. Restore: extract base tar, overlay each incremental tar in order

**Changes required:**
- Mount ext4 read-only (already have this for `StreamTarGz`)
- File manifest generation and diffing
- Layered tar restore logic

**Pros:**
- Human-readable incremental backups (it's just a tar of changed files)
- Smaller read I/O than block-level (only reads changed files, not entire volume)
- Easy to inspect what changed between backups

**Cons:**
- **Requires mounting** — needs root, `noload` for dirty journals, CAP_SYS_ADMIN
- **Loses filesystem metadata** — ext4 journal, reserved blocks, inode allocation not preserved
- **Restore is lossy** — can't perfectly reconstruct the original ext4 image (file-level only)
- **Deleted files** — need to track deletions explicitly in the manifest (whiteout files, like Docker layers)
- **Permission/ownership edge cases** — tar doesn't always preserve all ext4 attributes
- **Symlink/hardlink complexity** — must handle correctly in both diff and restore
- **Not useful for our migration flow** — migration needs exact ext4 image, not file-level restore

**Estimated effort:** 1 week
**Estimated improvement:** Variable — good for workloads with few large files, poor for many small file changes

---

### Option 4: Binary Delta (xdelta3/bsdiff)

**What:** Compute a binary diff between the current ext4 image and the previous full backup. Upload only the delta.

**How it works:**
1. Keep previous full backup locally (or download from GCS)
2. Run xdelta3/bsdiff to produce a patch file
3. Upload patch + reference to base
4. Restore: download base + all patches, apply in order

**Pros:**
- Smallest possible incremental — captures byte-level changes
- No need to understand ext4 internals
- Simple conceptually

**Cons:**
- **Requires previous backup locally** — must keep or re-download the base image (~10GB) to compute diff
- **Extremely CPU/memory intensive** — xdelta3 on a 2GB file takes 30-60s and ~2GB RAM; bsdiff is worse
- **Same chain problems as Option 2** — corruption, deletion, chain length
- **Impractical for large volumes** — 10GB diff computation is very slow
- **Agent resource contention** — computing diff while VMs are running impacts performance

**Estimated effort:** 3-5 days
**Estimated improvement:** 90-99% smaller deltas, but very high compute cost

---

## Comparison Matrix

| Criteria | Option 1: Sparse | Option 2: Block | Option 3: File | Option 4: Delta |
|----------|:-:|:-:|:-:|:-:|
| Upload size reduction | Modest (zeros already compress) | Large (80-95%) | Medium (varies) | Very large (90-99%) |
| Read I/O reduction | Large (skip holes) | None (reads all) | Medium | None + prev backup |
| Backup speed improvement | High for sparse | Moderate | Moderate | Slow (diff compute) |
| Restore complexity | Same as today | Chain reconstruction | Layered tar | Chain + patch apply |
| Data integrity risk | Same as today | Chain corruption risk | Lossy restore | Chain corruption risk |
| Implementation effort | 2-3 days | 1-2 weeks | 1 week | 3-5 days |
| Migration compatibility | Full | Needs chain logic | Not compatible | Needs chain logic |
| Enables frequent backups | Not really | Yes | Somewhat | Yes |

## Recommendation

**Start with Option 1 (sparse-aware), then evaluate Option 2 (block-level) based on real-world usage data.**

Rationale:
- Option 1 is low-risk, low-effort, and addresses the most common case (fresh/lightly-used volumes where most of the ext4 file is zeros)
- Option 2 is the right long-term solution for Phase 2/3 automated backups, but the chain management complexity is significant and shouldn't be built until we have real data on backup frequency needs
- Options 3 and 4 have fundamental limitations that make them poor fits for our use case

### Decision points for Option 2

Build block-level incremental when any of these become true:
- Backup frequency needs to increase beyond daily (on-stop, hourly)
- GCS storage costs become meaningful (>$10/month)
- Backup time becomes a bottleneck for migration operations
- User-facing backup UX demands faster/cheaper incremental snapshots

## Open Questions

1. **Block size for Option 2** — 4KB (ext4 native) gives finest granularity but largest manifest. 64KB reduces manifest 16x but may upload more unchanged data. Need benchmarks.
2. **Manifest encryption** — should block hash manifests be encrypted? They leak information about which blocks changed (though not the content). Probably yes, for consistency.
3. **Chain length cap** — if we go with Option 2, how many incrementals before forcing a full? 7 (weekly full) or 30 (monthly)?
4. **Concurrent backup safety** — for Phase 2 on-stop backups, can we read the ext4 while the VM is shutting down, or must we wait for clean unmount?
