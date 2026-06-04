# Artifact Runtime Performance Analysis

## What Broke

The artifact runtime was working correctly until we tried to deploy to **production hosts**. The local smoke test (`make smoke-test-artifact`) passes. The issue is production VM create speed.

### Timeline

1. **Working**: `v2026.4.2-r4` artifact boots correctly in local Firecracker integration tests (~5 min including GCS download)
2. **Slow in production**: First VM create on a new version takes 3+ minutes, exceeding the control plane's poll timeout (~90s). The control plane gives up before the VM boots.
3. **Attempted fix**: Mount release as read-only ext4 drive instead of copying to data volume. Failed because creating the ext4 image at runtime (2GB, 163K files, inode exhaustion) is also slow.

### Root Cause Chain

The previous artifact format (pnpm tar.zst) was ~400MB compressed but broke due to symlinks/hard links in tar. We switched to npm flat install which works correctly but is **much larger**:

| Metric | pnpm artifact | npm flat artifact |
|--------|--------------|-------------------|
| Compressed | 400MB | 363MB |
| Extracted | ~800MB (with links) | **2GB** (all real files) |
| File count | ~50K (links) | **163K** (full copies) |
| Dirs | ~5K | **16K** |

npm duplicates every transitive dependency as a real file. It also includes test snapshots, `.map` files, and a circular `node_modules/openclaw/node_modules/openclaw/` nested copy.

### Where Time Is Spent (per VM create)

| Step | Time | Notes |
|------|------|-------|
| GCS download (363MB) | ~30s | OVH→GCS, one-time per version |
| Tar extraction (163K files) | ~25s | CPU + I/O bound |
| Data volume mirror (2GB copy) | **60-120s** | cp -a into loopback ext4, the bottleneck |
| VM boot + gateway start | ~15s | Fast once files are in place |
| **Total** | **130-190s** | Exceeds 90s poll timeout |

### What Was Working Before

Before the npm flat install switch, the artifact used the **pnpm store** from the Docker image. That was:
- Smaller extracted size (~800MB with hard links)
- Fewer real files (hard links don't count as separate files for copy)
- But **broken**: pnpm symlinks didn't survive tar extraction, and hard links caused OpenClaw's `openBoundaryFileSync` to reject files with nlink>1

The data volume mirror was already slow with pnpm (~30-40s) but didn't exceed the poll timeout.

## Options

### Option A: Pre-built ext4 image (recommended)

Build the ext4 image at `make build-openclaw` time, not at VM create time.

**Build pipeline:**
```
npm install → directory → strip test files → mkfs.ext4 → cp -a → compress → upload ext4.zst to GCS
```

**Agent side:**
```
download ext4.zst → decompress to ext4 file → done (no extraction, no copy)
```

**VM boot:**
```
Pass ext4 as read-only third Firecracker drive → init script mounts /dev/vdc
```

**Pros:**
- Zero per-VM copy cost (shared read-only image)
- Agent just downloads + decompresses one file
- Build time is where we have patience (CI)

**Cons:**
- Changes the artifact format (ext4.zst instead of tar.zst)
- Needs fetcher changes to handle ext4 instead of tar
- Larger download (~500MB ext4.zst vs 363MB tar.zst)

**Implementation:**
- `build-openclaw-runtime.sh`: already partially implemented (in stash), creates ext4 from directory
- `upload-openclaw.sh`: upload ext4.zst instead of tar.zst, update manifest format
- `openclaw_fetcher.go`: download + zstd decompress (simpler than tar extraction)
- `firecracker_linux.go`: pass ext4 as third drive (already implemented)
- `init-openclaw.sh`: mount /dev/vdc (already implemented)

### Option B: Strip artifact size

Keep tar.zst format but aggressively strip the npm install:
- Remove `__image_snapshots__/`, `__tests__/`, `test/`, `*.map`, `*.md`
- Remove nested `node_modules/openclaw/node_modules/openclaw/` (circular dep)
- Remove unused channel plugin deps
- Target: ~100K files, ~1GB extracted

**Pros:** Simpler change, keeps existing fetcher
**Cons:** Still copies to data volume (slower than option A), fragile (new deps may add files)

### Option C: Background pre-fetch

Agent polls GCS manifest during idle time and pre-downloads artifacts before any VM create request. First create is instant because artifact is already cached.

**Pros:** No format change, simple agent change
**Cons:** Doesn't fix the data volume copy bottleneck (still 60s+ for mirror step)

## Recommendation

**Option A + C combined.** Pre-built ext4 image eliminates per-VM copy. Background pre-fetch eliminates first-create download wait. Together they make every VM create instant regardless of whether the version was previously staged.

## Current State

- Stashed WIP: `build-openclaw-runtime.sh` has ext4 image creation, `firecracker_linux.go` has third drive mount, `init-openclaw.sh` has /dev/vdc mount
- The read-only drive approach is architecturally correct — just needs the ext4 to be pre-built at upload time instead of at runtime
- Production is running `v2026.4.2-r4` (tar.zst approach) — works but first-create is slow
