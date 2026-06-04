# Host Optimization Guide

Strategies for increasing VM density on OCM worker hosts. Ranked by impact and ease of implementation.

## Current Baseline

Per host (ADVANCE-1 / AMD EPYC 4244P):
- 12 threads (6c/12t), 64 GB RAM, 2×960 GB NVMe
- 2x CPU oversubscription → 24 vCPU capacity
- No memory oversubscription → 64 GB capacity
- ~4 GB reserved for OS, agent, rootfs cache → ~60 GB usable

| VM Size | vCPUs | RAM | Max per host | Bottleneck |
|---------|-------|-----|-------------|------------|
| Basic (1 vCPU, 3 GB) | 1 | 3 GB | 20 | RAM |
| Standard (2 vCPU, 6 GB) | 2 | 6 GB | 10 | RAM |
| Pro (4 vCPU, 12 GB) | 4 | 12 GB | 5 | RAM |

With browser VM (+1 vCPU, +1 GB per machine with browser capability).

## 1. KSM (Kernel Same-page Merging)

**Impact: High | Effort: 5 minutes | Risk: None**

All VMs boot from the same rootfs via reflink copy. They share huge amounts of identical memory pages — kernel, glibc, Node.js runtime, Python, shared libraries. KSM deduplicates these pages transparently.

Typical savings: **30-50% of VM memory** for identical workloads.

### Enable on a host

```bash
# Enable KSM
echo 1 > /sys/kernel/mm/ksm/run

# Scan every 200ms (default 20ms is aggressive, 200ms is a good balance)
echo 200 > /sys/kernel/mm/ksm/sleep_millisecs

# Allow up to 50% of pages to be shared (default 100%)
echo 50 > /sys/kernel/mm/ksm/max_page_sharing
```

### Make persistent (survives reboot)

Add to `/etc/sysctl.d/99-ocm.conf`:
```
vm.ksm_run = 1
```

And to `/etc/rc.local` or a systemd unit:
```bash
echo 200 > /sys/kernel/mm/ksm/sleep_millisecs
```

### Monitor

```bash
# Pages shared (higher = more memory saved)
cat /sys/kernel/mm/ksm/pages_shared

# Memory saved (pages_sharing - pages_shared) * 4KB
cat /sys/kernel/mm/ksm/pages_sharing

# Compute savings in MB
echo "scale=2; ($(cat /sys/kernel/mm/ksm/pages_sharing) * 4) / 1024" | bc
```

### Add to provision-host.sh

Add a section after sysctl tuning to enable KSM by default on all new hosts.

### Expected improvement

| VM Size | Before KSM | After KSM (~40% savings) |
|---------|-----------|-------------------------|
| Standard (6 GB) | 10 per host | ~14 per host |
| Basic (3 GB) | 20 per host | ~28 per host |

## 2. zswap (Compressed Swap Cache)

**Impact: Medium | Effort: 5 minutes | Risk: Low**

zswap compresses swap pages in RAM before writing to disk. Cold/inactive pages get compressed ~3:1, effectively extending RAM. Uses ~20% of RAM for the compressed pool, but those pages are worth ~3x their size in uncompressed memory.

With 64 GB RAM and 20% pool: ~12 GB compressed pool ≈ ~36 GB of effective extra memory for cold pages.

### Enable on a host

```bash
# Enable zswap
echo 1 > /sys/module/zswap/parameters/enabled

# Use zstd compression (best ratio)
echo zstd > /sys/module/zswap/parameters/compressor

# Use zbud allocator (good for variable-size pages)
echo zbud > /sys/module/zswap/parameters/zpool

# Pool size: 20% of RAM
echo 20 > /sys/module/zswap/parameters/max_pool_percent
```

### Make persistent (kernel boot param)

Add to `/etc/default/grub`:
```
GRUB_CMDLINE_LINUX_DEFAULT="zswap.enabled=1 zswap.compressor=zstd zswap.zpool=zbud zswap.max_pool_percent=20"
```

Then `update-grub && reboot`.

### Prerequisite

Ensure swap is configured on the host (OVH default Ubuntu install includes swap).

```bash
# Verify swap exists
swapon --show

# If no swap, create one on NVMe (fast)
fallocate -l 16G /swapfile
chmod 600 /swapfile
mkswap /swapfile
swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab
```

### Monitor

```bash
# zswap stats
grep -r . /sys/kernel/debug/zswap/ 2>/dev/null

# Or via /proc
cat /sys/module/zswap/parameters/enabled
```

## 3. Memory Oversubscription

**Impact: High | Effort: Small code change | Risk: Moderate**

Similar to CPU's `VCPU_OVERSUBSCRIPTION_RATIO`, add a memory oversubscription ratio. Most VMs don't use their full allocation — a 6 GB standard VM doing lightweight coding may only touch 2-3 GB.

### Implementation

Add `MEMORY_OVERSUBSCRIPTION_RATIO` env var (default 1.0, max 2.0):
- Applied during enrollment and provisioning (same pattern as `vcpuOversubRatio`)
- `capacity_memory_mb = physical_memory_mb * ratio`
- `FindHostWithCapacity` already checks `used_memory_mb < capacity_memory_mb`

### Recommended values

| Ratio | Effective capacity (64 GB host) | Standard VMs | Risk |
|-------|-------------------------------|-------------|------|
| 1.0 (current) | 64 GB | 10 | None |
| 1.3 | 83 GB | 13 | Low — combine with KSM |
| 1.5 | 96 GB | 16 | Moderate — needs KSM + zswap |
| 2.0 | 128 GB | 21 | High — OOM risk without careful monitoring |

Start at 1.3x with KSM enabled. Monitor for OOM kills before increasing.

### Safety

Add OOM monitoring to the agent heartbeat:
```bash
# Count OOM kills since boot
dmesg | grep -c "Out of memory"
```

If OOM kills appear, reduce the ratio or add more swap.

## 4. Firecracker Balloon Device

**Impact: High | Effort: Days | Risk: Low**

Firecracker supports a virtio-balloon device that dynamically reclaims unused memory from VMs. A VM allocated 6 GB but using 2 GB could have 4 GB reclaimed by inflating the balloon.

### How it works

1. Configure balloon device in Firecracker VM config
2. Agent monitors per-VM memory usage via `/proc` or balloon stats
3. Inflate balloon (reclaim memory) when VM is idle
4. Deflate balloon (return memory) when VM needs it

### When to use

- After KSM and zswap are in place and you need more density
- Particularly useful for long-running idle VMs (e.g., stopped machines that remain in memory)

### Complexity

Requires changes to:
- Firecracker VM config (add balloon device)
- Agent orchestrator (balloon management loop)
- Memory pressure monitoring (when to inflate/deflate)

## 5. Browser VM Optimization

**Impact: Medium | Effort: Medium | Risk: Low**

Currently each machine with browser capability gets a companion browser VM (1 vCPU, 1 GB) at start time, regardless of whether it's used.

### On-demand browser VMs

Only create the browser VM when the first CDP request arrives:

1. CDP proxy receives connection from main VM
2. No browser VM exists → boot one (125ms Firecracker boot)
3. Register target in CDP proxy, forward connection
4. Start idle timer — no CDP connections for 5 minutes → destroy browser VM
5. Next CDP request → boot fresh browser VM

Saves 1 vCPU + 1 GB per machine that doesn't use the browser.

### Browser profile persistence

Mount the main VM's data volume as a second drive in the browser VM so Chromium profile (cookies, logins, localStorage) survives across restarts.

## Implementation Order

| Phase | Change | Effort | Density gain |
|-------|--------|--------|-------------|
| 1 | Enable KSM | Add to `provision-host.sh` | +40% |
| 2 | On-demand browser VMs | Agent code change | +1 GB per non-browser machine |
| 3 | Memory oversub 1.3x | Small code change | +30% |
| 4 | zswap | Add to `provision-host.sh` + grub | +15% headroom |
| 5 | Balloon device | Agent code change | Variable |

Phases 1 and 4 can be deployed to existing hosts immediately. Phases 2, 3, and 5 require code changes and deployment.

## Monitoring

After enabling optimizations, monitor these metrics in agent heartbeat or via SSH:

```bash
# Memory overview
free -h

# KSM savings
echo "KSM saved: $(echo "scale=0; $(cat /sys/kernel/mm/ksm/pages_sharing) * 4 / 1024" | bc) MB"

# zswap stats
echo "zswap pool: $(cat /sys/kernel/debug/zswap/pool_total_size 2>/dev/null || echo N/A)"

# OOM kills
dmesg | grep -c "Out of memory"

# Per-VM memory (from Firecracker balloon or /proc on host)
ls /var/lib/ocm/vms/
```
