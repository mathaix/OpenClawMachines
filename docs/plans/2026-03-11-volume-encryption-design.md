# Data Volume Encryption at Rest

**Date:** 2026-03-11
**Status:** Design / Not Started
**Branch:** TBD
**Related:** `docs/plans/2026-03-11-backup-design.md`, `docs/designs/persistence.md` (item 3)

## Problem

Data volumes on host machines are plaintext ext4 files stored at `/var/lib/ocm/data/{machineID}.ext4`. Any process with root access on the host can read any VM's data volume. The current security model relies on:

1. **GCP disk-level encryption** — encrypts the physical disk, but the OS sees plaintext
2. **Linux file permissions** — `0600` root-only, but root can read everything
3. **Firecracker KVM isolation** — prevents VM-to-VM access, but doesn't protect against host compromise

### Threat Model

| Threat | Current Protection | With Volume Encryption |
|--------|-------------------|----------------------|
| Unauthorized GCP employee accessing disk | GCP CMEK | GCP CMEK + LUKS |
| Compromised agent process | None (agent runs as root) | LUKS keys only in memory during VM runtime |
| SSH access to host | None (root sees all volumes) | Volumes locked when VM stopped |
| Escaped VM (container escape) | Firecracker isolation only | LUKS — even if escape succeeds, other volumes are encrypted |
| Physical disk theft / decommissioned disk | GCP CMEK | GCP CMEK + LUKS |
| Backup exfiltration from GCS | Already encrypted (backup feature) | N/A |

### What This Does NOT Solve

- A running VM's volume is unlocked (decrypted mapper is open). A root attacker during VM runtime can still read it.
- The agent must hold keys in memory to unlock volumes. Memory dump or agent compromise during operation exposes keys.
- This is defense-in-depth, not a complete solution against a fully compromised host.

## Current Data Volume Lifecycle

Understanding the current flow is critical — encryption wraps around this existing lifecycle.

### Host Level (GCE VM)

```
GCE provisioner creates host with two disks:
  Boot disk (pd-ssd)     → /          (OS, agent, rootfs images)
  Data disk (pd-balanced) → /var/lib/ocm/data/   (all VM volumes)

Systemd units on host:
  ocm-data-init.service  → blkid || mkfs.ext4 (first boot only)
  var-lib-ocm-data.mount → mount /dev/disk/by-id/google-ocm-data → /var/lib/ocm/data/
  ocm-agent.service      → starts after data mount
```

### Per-VM Volume (Orchestrator)

```
ensureDataVolume(machineID, sizeGB, dataVersion):
  1. Check if /var/lib/ocm/data/{machineID}.ext4 exists
     → Yes: reuse (check version sidecar for upgrade backup)
     → No:  create sparse file (Truncate), mkfs.ext4
  2. Return path

Firecracker VM config:
  drives: [
    {id: "rootfs", path: "{machineID}.ext4", root: true},   ← per-VM CoW copy
    {id: "data",   path: "{machineID}.ext4", root: false},  ← the data volume
  ]

Inside VM (init-openclaw.sh):
  /dev/vdb → blkid || mkfs.ext4 → mount /data
  /data/home/openclaw, /data/workspace, /data/ocm/configs
```

### Volume States

```
Created    → sparse ext4 file on host disk (VM never started)
Mounted    → Firecracker has it as a block device, VM has it mounted at /data
Unmounted  → VM stopped, file exists on host, not in use
Backed up  → Copy exists in GCS (encrypted via backup feature)
Deleted    → os.Remove() during machine deletion
```

## Design

### Approach: LUKS2 Per-Volume Encryption

Each VM's data volume becomes a LUKS2-encrypted container. The agent manages the encrypt/decrypt lifecycle at VM start/stop boundaries.

```
Before (current):
  /var/lib/ocm/data/{machineID}.ext4  ← plaintext ext4

After (encrypted):
  /var/lib/ocm/data/{machineID}.ext4.luks  ← LUKS2 container
      └─► /dev/mapper/ocm-{machineID}      ← decrypted block device (only while VM runs)
```

### Why LUKS2 (Not Alternatives)

| Option | Pros | Cons |
|--------|------|------|
| **LUKS2** (chosen) | Kernel-native, zero-copy, works with Firecracker block devices, well-audited | Requires `cryptsetup` on host, adds ~100ms to VM start |
| fscrypt (ext4 encryption) | No extra tools | Per-file, not per-volume; doesn't protect metadata; can't pass encrypted block device to Firecracker |
| Application-level (Go crypto) | Portable | CPU-intensive, can't give Firecracker a block device, would need FUSE |
| GCP CMEK with customer keys | Zero code changes | Only protects at disk level, same as current; doesn't isolate volumes from each other |

### Key Hierarchy

Reuse the envelope encryption pattern from the backup feature:

```
Platform Master Key (BACKUP_MASTER_KEY / VOLUME_MASTER_KEY)
    │
    └─► Per-Machine Volume Key (32 bytes, AES-256)
            │
            └─► LUKS2 keyslot (unlocks the volume)
```

- **Platform Master Key:** Same 32-byte hex key used for backup key encryption. Can be shared or separate (`VOLUME_MASTER_KEY` env var, falls back to `BACKUP_MASTER_KEY`).
- **Per-Machine Volume Key:** Random 32-byte key generated at machine creation. Stored in `machines.volume_key` column, encrypted with the platform master key (AES-256-GCM, same as `backup_key`).
- The agent receives the decrypted volume key from the control plane and passes it to `cryptsetup luksOpen --key-file=-`.

### Data Flow

#### VM Start (Volume Unlock)

```
Control Plane                          Agent (Host)
─────────────                          ────────────
1. User starts machine
2. Place on host, call agent
3. Send start request with              4. Receive start request
   encrypted volume_key                 5. Decrypt volume_key with master key
                                        6. cryptsetup luksOpen \
                                             /var/lib/ocm/data/{id}.ext4.luks \
                                             ocm-{machineID} \
                                             --key-file=- <<< key
                                        7. Firecracker gets /dev/mapper/ocm-{machineID}
                                           as the data drive
                                        8. VM boots, mounts /dev/vdb as usual
```

#### VM Stop (Volume Lock)

```
Agent (Host)
────────────
1. Firecracker VM exits
2. cryptsetup luksClose ocm-{machineID}
3. /dev/mapper/ocm-{machineID} removed
4. File on disk is opaque LUKS container
```

#### Volume Creation (New Machine)

```
Agent (Host)
────────────
1. Create sparse file: {machineID}.ext4.luks
2. cryptsetup luksFormat --type luks2 \
     --cipher aes-xts-plain64 \
     --key-size 512 \
     --key-file=- \
     {machineID}.ext4.luks <<< key
3. cryptsetup luksOpen ... ocm-{machineID}
4. mkfs.ext4 -m 1 /dev/mapper/ocm-{machineID}
5. cryptsetup luksClose ocm-{machineID}
```

### Migration Path (Existing Volumes)

Existing plaintext volumes need migration. Two options:

**Option A: Encrypt-in-place on next start (recommended)**
1. Agent detects `.ext4` file (no `.ext4.luks`)
2. Creates new LUKS container, copies data block-by-block
3. Renames `.ext4` → `.ext4.plain.bak`, `.ext4.luks` is the new volume
4. Deletes `.ext4.plain.bak` after successful first boot

**Option B: Require fresh volume**
- Only encrypt new machines
- Existing machines keep plaintext until user recreates

Option A is better for user experience — transparent migration with no data loss.

### Interaction with Backup Feature

Backups read the raw data volume file. With LUKS:

- **Backup creates from LUKS container:** The agent opens LUKS, reads the decrypted mapper device, then runs the existing compress→encrypt→upload pipeline. Backup encryption is independent of volume encryption.
- **Restore writes to LUKS container:** Agent opens LUKS, writes decrypted backup data to the mapper device.
- **Download streams from LUKS:** Same as backup — open LUKS, read decrypted data.

The backup pipeline doesn't change. The only change is the agent opens/closes LUKS around the operation instead of reading the raw file directly.

## Schema Changes

```sql
-- Migration 033: Machine volume encryption keys

ALTER TABLE machines ADD COLUMN IF NOT EXISTS volume_key BYTEA;
-- Encrypted per-machine key for LUKS volume (same envelope encryption as backup_key)
-- NULL = plaintext volume (legacy), non-NULL = LUKS encrypted

ALTER TABLE machines ADD COLUMN IF NOT EXISTS volume_encrypted BOOLEAN NOT NULL DEFAULT false;
-- Tracks whether the volume has been migrated to LUKS
```

## Configuration

| Env Var | Default | Where | Purpose |
|---------|---------|-------|---------|
| `VOLUME_MASTER_KEY` | falls back to `BACKUP_MASTER_KEY` | Agent + Control Plane | Master key for volume key envelope encryption |
| `VOLUME_ENCRYPTION_ENABLED` | `false` | Agent | Enable LUKS encryption for new + migrated volumes |

## Agent Requirements

The host VM image (GCE snapshot) needs:
- `cryptsetup` package installed (`apt install cryptsetup`)
- `dm-crypt` kernel module loaded (standard on Ubuntu/Debian)
- Sufficient entropy for key operations (`/dev/urandom`)

## Performance Impact

| Operation | Current | With LUKS | Delta |
|-----------|---------|-----------|-------|
| Volume creation | ~200ms (truncate + mkfs) | ~500ms (luksFormat + mkfs) | +300ms (one-time) |
| VM start | ~125ms | ~225ms (luksOpen + start) | +100ms |
| VM stop | ~50ms | ~100ms (stop + luksClose) | +50ms |
| Disk I/O (runtime) | Native ext4 | AES-NI accelerated | ~1-3% overhead (negligible on modern CPUs with AES-NI) |
| Migration (existing volume) | N/A | ~30s per GB | One-time per machine |

AES-NI is available on all GCE instance types and enrolled hosts. The `aes-xts-plain64` cipher is hardware-accelerated.

## Implementation Phases

### Phase 1: New Volumes Only
- Add `volume_key` column and key generation at machine creation
- LUKS format for new data volumes
- luksOpen/luksClose at VM start/stop
- Backup/restore/download work through LUKS
- Existing volumes remain plaintext (no migration)
- Feature flag: `VOLUME_ENCRYPTION_ENABLED=true` to opt in

### Phase 2: Transparent Migration
- Detect plaintext volumes on VM start
- Migrate in-place (copy to LUKS container, swap)
- Update `volume_encrypted` flag after successful migration
- Backup before migration (safety net)

### Phase 3: Enforcement
- Remove feature flag — encryption is always on
- Audit: alert on any plaintext volumes
- Key rotation support (re-encrypt LUKS keyslot with new key)

## Files to Create/Modify

### New Files

| File | Purpose |
|------|---------|
| `backend/internal/volume/encryption.go` | LUKS lifecycle: format, open, close, migrate |
| `backend/internal/volume/encryption_test.go` | Tests (mock cryptsetup) |
| `backend/migrations/033_volume_encryption.sql` | Schema |

### Modified Files

| File | Change |
|------|--------|
| `backend/internal/orchestrator/firecracker_linux.go` | `ensureDataVolume` → LUKS-aware; pass mapper device to Firecracker |
| `backend/internal/orchestrator/firecracker_linux.go` | VM cleanup → `luksClose` before file removal |
| `backend/internal/store/store.go` | `Machine.VolumeKey`, `Machine.VolumeEncrypted` fields |
| `backend/internal/store/postgres.go` | Scan new columns, key CRUD |
| `backend/internal/config/config.go` | `VolumeMasterKey`, `VolumeEncryptionEnabled` in AgentConfig + Config |
| `backend/internal/agentapi/handlers.go` | Backup/restore handlers open LUKS before operating |
| `backend/cmd/agent/main.go` | Pass master key to orchestrator |
| `scripts/create-snapshot.sh` | Install `cryptsetup` package in host image |

## Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| Lost master key = all volumes unreadable | Key stored in GCP Secret Manager with backup; document recovery procedure |
| LUKS header corruption = volume lost | LUKS2 has redundant header; backup feature provides recovery |
| Migration failure mid-copy | Keep `.ext4.plain.bak` until first successful boot; rollback path |
| `cryptsetup` not installed on host | Check at agent startup, log error, skip encryption (graceful degradation) |
| Performance regression | AES-NI eliminates meaningful overhead; benchmark before enforcing |
| Concurrent LUKS open during backup | Backup already requires VM stopped; LUKS opened briefly for backup operation only |

## Open Questions

1. **Shared or separate master key?** Reusing `BACKUP_MASTER_KEY` for volume encryption reduces operational complexity but means one key compromise exposes both. Separate `VOLUME_MASTER_KEY` is more secure but doubles key management burden.

2. **Should the control plane or agent hold the master key?** Currently the agent needs it to decrypt volume keys at VM start. The control plane could send the decrypted volume key instead (like backups), but that means the key travels over the network on every start.

3. **Enrolled (non-GCP) hosts:** These hosts may not have `cryptsetup` or the right kernel modules. Should encryption be optional per-host, or required for all?

4. **Key rotation:** LUKS2 supports multiple keyslots. Should we plan for key rotation from day one, or add it later?
