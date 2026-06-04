# XFS Reflink Storage for VM Rootfs Copies

**Date:** 2026-05-26
**Branch:** `browservm_upgrade`
**Status:** Implemented, deployed to current production hosts, follow-up startup
profiling open

## Summary

Every Firecracker VM boot copies a base rootfs image. On ext4 this is a full
file copy — 1.5 GB for machine VMs, 3.5 GB for browser VMs — taking seconds to
tens of seconds. XFS with `reflink=1` enables copy-on-write (CoW) clones that
complete in milliseconds and consume zero additional disk space until pages are
dirtied at runtime.

This design adds XFS reflink storage to production OVH hosts using a
**loop-mounted XFS image file on the existing ext4 RAID-1 array**. This
preserves disk redundancy, requires no repartitioning, and can be deployed with
a brief agent restart.

## Production Update — 2026-05-27

The XFS storage change is working in production. Hosts updated through the
rollout are running agent build `299a7fe-20260527T142532Z`, and VM create logs
show reflink copies from the staged base rootfs.

Verified examples:

| Machine | Host | Rootfs copy | Backend total | Agent create → running | Notes |
|---------|------|-------------|---------------|--------------------------|-------|
| `newbie` | 104 | `copy_mode=reflink`, `duration_ms=1` | ~23 s | ~18.6 s | Rootfs clone was not the bottleneck |
| `16` | 105 | XFS host, same path | 24.65 s | ~18.6 s | Created `15:54:48.976Z`, running `15:55:13.622Z` |

The remaining startup latency is mostly guest/userspace readiness, not storage:

- ~3 s in backend route/setup before VM creation
- ~16-18 s waiting for guest services (`ttyd`, auth proxy, OpenClaw gateway)
- up to 3 s of backend polling latency before the control plane observes
  `running`

XFS removed the expensive full rootfs copy. Sub-10-second machine starts need a
separate guest boot/readiness optimization.

Operational fixes from the rollout:

- `scripts/configure-vm-state-xfs.sh` is the durable migration helper for
  existing hosts. It refuses to run while Firecracker VMs are live, preserves
  `vms.json` / `browser-vms.json`, verifies state before committing the mount,
  and only writes the persistent `fstab` entry after the mounted state is valid.
- `scripts/provision-host.sh` now uses the same XFS state setup for new hosts
  instead of relying on an ad hoc browser-storage-only handoff.
- Enrollment appends `/etc/ocm-agent/vm-state.env` and
  `/etc/ocm-agent/browser-state.env` to `/etc/ocm-agent/agent.env`, so fresh
  hosts and migrated hosts get the same `STATE_DIR` / `BROWSER_STATE_DIR`.
- Backend/agent fixes through commit `299a7fe` handle stale runtime restart
  selection and allow cleanup of error-only VM registry entries created during
  failed starts.

## Current State

### Original host disk layout

```
nvme0n1 (960 GB Samsung PM9A3) ─┐
                                 ├─ md0 (RAID-1) ─ ext4  /
nvme1n1 (960 GB Samsung PM9A3) ─┘
```

### Agent storage directories

| Config key | Default path | Contents |
|------------|-------------|----------|
| `IMAGES_DIR` | `/var/lib/ocm/images` | Base rootfs images downloaded from GCS |
| `STATE_DIR` | `/var/lib/ocm/vms` | Per-VM rootfs copies, `vms.json`, sockets |
| `BROWSER_STATE_DIR` | falls back to `STATE_DIR` | Browser VM rootfs copies, `browser-vms.json` |
| `DATA_DIR` | `/var/lib/ocm/data` | Persistent user data volumes (5 GB each) |
| `RUNTIME_STATE_DIR` | `/var/lib/ocm/runtime` | OpenClaw version pointers |

Everything lives under `/var/lib/ocm/` on ext4 RAID-1. No XFS exists on
production hosts before this feature.

### Copy cost observed

| VM type | Base rootfs | Full copy time | Reflink copy time |
|---------|------------|----------------|-------------------|
| Machine VM | ~1.5 GB | ~3-5 s | ~8 ms |
| Browser VM (kernel-images) | ~3.5 GB | ~15-21 s | ~8 ms |

The `browservm_upgrade` branch already gates new kernel-images-experimental
browser rootfs behind reflink (`browserRootfsRequiresReflink`), refusing to
boot on ext4. Machine VMs fall back to full copy silently.

## Design

### Loop-mounted XFS image on ext4 RAID-1

```
nvme0n1 ─┐
          ├─ md0 (RAID-1) ─ ext4  /
nvme1n1 ─┘
              │
              ├── /var/lib/ocm/images/    (base rootfs, ext4, read-heavy)
              ├── /var/lib/ocm/data/      (user data, ext4, RAID-1 protected)
              ├── /var/lib/ocm/runtime/   (openclaw releases, ext4)
              └── /var/lib/ocm/ocm-xfs.img  (sparse, RAID-1 protected)
                      ↓ loop mount
                  /var/lib/ocm/vms/  (XFS reflink=1)
                      ├── {machineID}.ext4      (CoW clone)
                      ├── {browserID}-browser.ext4  (CoW clone)
                      ├── browser-rootfs/releases/  (staged versions)
                      ├── vms.json
                      └── browser-vms.json
```

### What goes through the XFS loop mount

Only **ephemeral VM scratch I/O**:

1. Reflink copy at VM create time (instant, near-zero I/O)
2. Firecracker runtime writes to the rootfs (package installs, temp files,
   logs inside the VM — all throwaway)
3. Small state files (`vms.json`, `browser-vms.json`)

### What stays on native ext4 RAID-1

All **durable, user-facing data**:

- `/var/lib/ocm/data/` — persistent user data volumes (workspace files,
  project data). This is the only data that matters across VM lifecycles.
- `/var/lib/ocm/images/` — base rootfs images (re-downloadable from GCS)
- `/var/lib/ocm/runtime/` — OpenClaw releases (re-downloadable from GCS)

The loop device overhead (~10-15% I/O penalty) affects only expendable scratch
writes. User data sees zero overhead.

### Sizing

The XFS image is created as a **sparse file** — it does not consume its full
size upfront. Actual disk usage grows only as dirty CoW pages accumulate.

| Component | Size estimate |
|-----------|--------------|
| Base rootfs staged into XFS (2-3 machine + 2-3 browser versions) | ~15 GB |
| Per-VM dirty CoW pages (worst case 500 MB each, 20 machines + 10 browsers) | ~15 GB |
| State files and metadata | < 1 MB |
| XFS internal overhead + headroom | ~20 GB |
| **Sparse image allocation** | **200 GB** |
| **Typical actual usage** | **30-50 GB** |

200 GB sparse allocation provides ample room. Can be grown online with
`truncate` + `xfs_growfs` if needed.

### Base rootfs staging

The base rootfs images are downloaded from GCS into `IMAGES_DIR`
(`/var/lib/ocm/images/`) on ext4. The orchestrator then stages (copies) them
into `STATE_DIR` (`/var/lib/ocm/vms/`) at startup — see
`firecracker_linux.go:266-271`. This staging copy crosses the ext4 → XFS
boundary, so it is a full copy (not a reflink). It happens once per version,
at agent startup or when a new rootfs version is detected. Subsequent per-VM
copies from the staged base are all reflinks within XFS.

Browser rootfs staging follows the same pattern but targets
`BrowserStateDir` / `browserRootfsBaseDir()`.

## Implementation

### Phase 1 — Provision script changes

Update `scripts/provision-host.sh` to:

1. Create sparse XFS image:
   ```bash
   truncate -s 200G /var/lib/ocm/ocm-xfs.img
   mkfs.xfs -m reflink=1 /var/lib/ocm/ocm-xfs.img
   ```

2. Mount at `STATE_DIR`:
   ```bash
   mount -o loop,noatime /var/lib/ocm/ocm-xfs.img /var/lib/ocm/vms
   ```

3. Add fstab entry:
   ```
   /var/lib/ocm/ocm-xfs.img /var/lib/ocm/vms xfs loop,noatime,nofail 0 0
   ```

4. Set durable state handoff files before enrollment:
   ```bash
   echo STATE_DIR=/var/lib/ocm/vms > /etc/ocm-agent/vm-state.env
   echo BROWSER_STATE_DIR=/var/lib/ocm/vms > /etc/ocm-agent/browser-state.env
   ```

5. Write an `ocm-agent.service` drop-in:
   ```ini
   [Unit]
   RequiresMountsFor=/var/lib/ocm/vms
   ```

6. During enrollment, append both state handoff files to
   `/etc/ocm-agent/agent.env` after the control-plane credentials are written.
   This avoids coupling VM state configuration to the browser-storage handoff.

### Phase 2 — Migration for existing hosts

For hosts already running with data in `/var/lib/ocm/vms/` on ext4:

1. Drain the host (stop all VMs via AdminHosts UI)
2. Enter maintenance mode
3. Create and mount XFS image (as above)
4. Copy the full existing `STATE_DIR` into the new mount, including
   `vms.json`, `browser-vms.json`, per-VM rootfs/data images, runtime files,
   and cached manifests
5. Restart agent — it re-stages base rootfs into XFS on startup
6. Exit maintenance mode

The migration helper verifies the `vms.json` and `browser-vms.json` array
lengths before and after the temporary copy and final mount. If the final mount
does not expose the expected state, the helper restores the pre-XFS state
directory rather than leaving the agent pointed at an empty mount. It writes the
persistent `fstab` entry only after the final mounted state has passed
verification. If rollback cannot safely unmount the failed final mount, it
leaves the pre-XFS backup in place and fails loudly rather than modifying a
mounted state path.

Alternatively, use the browser storage control plane operation
(`POST /admin/hosts/{id}/configure-browser-storage`) already built in
`da3a58b` as a model for a general storage setup operation.

The migration helper is `scripts/configure-vm-state-xfs.sh`. It is idempotent,
refuses to run while Firecracker VMs are live, preserves and verifies the full
state directory, writes the fstab entry and systemd mount dependency, and
updates the same `vm-state.env` / `browser-state.env` handoff files used by
fresh provisioning.

### Phase 3 — Remove reflink gate for browser VMs

Once all production hosts have XFS, the `AllowKernelBrowserFullCopy` escape
hatch and the `StableKernelBrowserRootfsVersion` exemption in
`browserRootfsRequiresReflink` can be removed. All browser VM creates will
use reflinks unconditionally.

### Phase 4 — Machine VM reflink awareness (optional)

Machine VMs already use `copyRootfs` which tries `--reflink=always` first.
On XFS this silently succeeds. No code changes needed — machine VMs
automatically benefit from the XFS mount.

Optionally, add the same reflink-required gating for machine VMs so operators
are alerted if a host falls back to full copy:

```go
if copyMode != rootfsCopyModeReflink {
    slog.Error("vm.rootfs.reflink_failed", ...)
    // optionally reject creation on hosts without reflink
}
```

## Tradeoffs

### Why loop mount instead of dedicated partition?

| | Loop mount (chosen) | Dedicated NVMe partition |
|---|---|---|
| RAID-1 protection | Kept — image file is mirrored | Lost — one NVMe is standalone |
| Repartitioning | None | Requires breaking RAID, reformatting |
| Downtime | Brief agent restart | Extended, risky on remote hosts |
| I/O overhead | ~10-15% on scratch writes | None |
| Resize | Online (`truncate` + `xfs_growfs`) | Offline, complex |
| Recovery | Remount loop after crash | Standard fsck |
| User data safety | RAID-1 mirrors everything | Need separate backup strategy |

The loop overhead applies only to throwaway VM rootfs writes. User data
volumes on `/var/lib/ocm/data/` are unaffected. The RAID-1 protection for
user data is the deciding factor.

### Why not XFS for everything?

Reformatting the root filesystem as XFS would require a full host
reinstallation. The loop approach gives reflink benefits where they matter
most (VM rootfs copies) without touching the rest of the system.

### Future option: dedicated XFS partition

If the loop overhead becomes measurable under heavy load, a future migration
can move to a dedicated XFS partition on one NVMe. This would require:

- Breaking the RAID-1 array
- Creating an XFS partition on one NVMe for `/var/lib/ocm`
- Setting up snapshot-based backup to the other NVMe
- Migrating user data volumes

This is a larger, riskier operation best done during a planned maintenance
window. The loop approach is a safe stepping stone.

### New host partitioning follow-up

The loop-mounted XFS image is the right low-risk migration path for already
provisioned hosts because it preserves the current ext4 RAID-1 root filesystem
without repartitioning a remote machine. For **new hosts**, we should revisit
the disk layout before scaling this pattern further.

Open question for the next host-provisioning pass: should fresh hosts keep the
loop image, or should provisioning create a native XFS mount for VM scratch
state from the beginning? Options to evaluate:

- XFS directly on the RAID-1 md device for `/var/lib/ocm/vms`
- A dedicated XFS partition or logical volume for `/var/lib/ocm/vms`
- Keeping durable data on mirrored storage while giving only ephemeral VM
  rootfs scratch a native XFS filesystem

Do not change the current production hosts just to answer this. The decision is
for the next generation of host provisioning, where partitioning can be chosen
before the machine enters service.

## Validation

- [x] `findmnt -T /var/lib/ocm/vms` shows `xfs` filesystem type on updated
  hosts
- [x] `cp --reflink=always` succeeds within `/var/lib/ocm/vms/`
- [x] Agent heartbeat reports `storage.reflink_supported=true`
- [x] Machine VM create logs `copy_mode=reflink`
- [x] Browser VM create logs `copy_mode=reflink`
- [ ] `du -sh /var/lib/ocm/ocm-xfs.img` shows sparse usage (much less than
  200 GB) after running several VMs
- [ ] Host survives reboot — loop mount comes back via fstab
- [ ] NVMe failure simulation: pull one drive, verify md array degrades but
  host continues operating

### Startup validation

- [x] Machine VM rootfs clone verified as reflink: `duration_ms=1`
- [x] Backend create-to-running measured for `newbie`: ~23 s
- [x] Backend create-to-running measured for machine `16`: 24.65 s
- [x] Rootfs copy ruled out as the current startup bottleneck
- [ ] Add phase timing logs for route setup, VM create, rootfs staging,
  Firecracker boot, `ttyd`, auth proxy, and OpenClaw gateway readiness
- [ ] Reduce backend VM-status polling granularity from 3 s to 1 s or replace
  polling with an agent readiness event
- [ ] Profile and optimize guest startup for the remaining ~16-18 s readiness
  delay

## Risks

1. **Loop device after unclean shutdown**: If the host crashes, XFS journal
   replay on the loop device runs at next mount. XFS journaling handles this
   well, but a corrupted ext4 layer underneath could cascade. Mitigated by
   RAID-1 and ext4 journaling.

2. **ext4 fragmentation of the image file**: Over time the sparse image can
   fragment on ext4, degrading sequential I/O. Mitigated by `noatime` and
   the fact that VM rootfs I/O is mostly random (not sequential). Can be
   defragmented offline with `e4defrag` if needed.

3. **Sparse file fills up**: If dirty CoW pages exceed expectations, the
   sparse file could fill its 200 GB allocation. XFS will return I/O errors
   to VMs. Monitor with `df -h /var/lib/ocm/vms/` and grow with `truncate`
   + `xfs_growfs` as needed.
