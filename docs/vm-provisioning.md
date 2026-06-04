# VM Provisioning & Firecracker Bootstrapping

## Overview

Each OpenClaw Machine runs inside a Firecracker MicroVM on a shared Host VM. This document describes the full provisioning pipeline — from a user clicking "Start" in the dashboard to an OpenClaw gateway responding inside a VM — and every system involved along the way.

> **See also**: [config-lifecycle.md](config-lifecycle.md) for how `openclaw.json` is assembled, written to disk, and updated at runtime (covers the two assembly paths: `AssembleSeedConfig` for first boot vs `AssembleConfig` for runtime pushes).

```
User clicks "Start"
       │
       ▼
Control Plane API (Cloud Run)
       │
       ├─ 1. Scheduler: find Host with free capacity
       │     (or provision a new one)
       │
       ├─ 2. HTTP POST to Host Agent: /vms
       │
       ▼
Host Agent (GCP Compute VM, port 9090)
       │
       ├─ 3. Copy rootfs (reflink/CoW)
       ├─ 4. Create TAP device on bridge
       ├─ 5. Register VM metadata
       ├─ 6. Start Firecracker process
       │
       ▼
Firecracker MicroVM (KVM)
       │
       ├─ 7. Kernel boots (~125ms)
       ├─ 8. /init mounts, configures network
       ├─ 9. Reads config from metadata service
       ├─ 10. Starts OpenClaw gateway on :3000
       │
       ▼
Gateway responding on bridge IP
```

---

## 1. Host VM Provisioning (GCP Compute Engine)

Before any MicroVM can run, there must be a Host VM with nested virtualization, Firecracker installed, and the Worker Agent running.

### How a Host VM is Created

The **Provisioner** (`backend/internal/provisioner/provisioner.go`) creates GCE instances:

1. Generate instance name: `ocm-host-{timestamp_ms}`
2. Create GCE instance from a pre-baked snapshot with:
   - **Nested virtualization enabled** (`AdvancedMachineFeatures.EnableNestedVirtualization`)
   - **SSD boot disk** (50GB `pd-ssd` from snapshot)
   - **Scheduling**: `TERMINATE` on host maintenance (required for nested virt)
   - **Labels**: `ocm=true`
   - **Metadata injection** (key-value pairs, readable by the Agent at boot):
     - `agent-token` — authenticates Agent ↔ Control Plane
     - `backend-url` — Control Plane API endpoint
     - `anthropic-api-key`, `openai-api-key`, `google-api-key` — LLM API keys
3. Wait for GCE operation to complete
4. Fetch instance details (external IP, internal IP, instance ID)
5. Insert `hosts` DB record (`status = 'provisioning'`)
6. Poll Agent `/health` endpoint every 5s (up to 5 minutes)
7. When healthy, update `status = 'ready'`

### What the Snapshot Contains

The Host VM snapshot is a pre-configured disk image with:

- Linux kernel with KVM support
- Firecracker binary at `/usr/local/bin/firecracker`
- Worker Agent binary at `/usr/local/bin/ocm-agent`
- Base rootfs image at `/var/lib/ocm/images/rootfs.ext4`
- Firecracker-compatible kernel at `/var/lib/ocm/images/vmlinux.bin`
- systemd service for the Agent (auto-starts on boot)

### Host Lifecycle States

```
provisioning → ready → draining → stopped
                 │
                 └──→ error (if health check fails)
```

| State | Meaning |
|-------|---------|
| `provisioning` | GCE instance created, waiting for Agent health |
| `ready` | Agent responding, accepts new Machine placements |
| `draining` | No new placements, existing Machines run until stopped |
| `stopped` | GCE instance deleted |

---

## 2. Worker Agent Startup Sequence

When the Host VM boots, the Agent process (`backend/cmd/agent/main.go`) starts with a 12-step initialization:

```
Step 1:  Load config from env vars
Step 2:  Prefetch config from GCP instance metadata
Step 3:  Create bridge (ocm-br0, 192.168.100.0/24)
Step 3b: Setup NAT + iptables security rules
Step 4:  Start metadata server on bridge gateway IP
Step 5:  Start LLM proxy on bridge IP:4000 (if keys available)
Step 6:  Init Firecracker orchestrator
Step 7:  Connect metadata registrar to orchestrator
Step 8:  Create Agent API server
Step 9:  Start HTTP servers (control :9090, proxy :9091)
Step 10: Start Cloudflare tunnel (production only)
Step 11: Wait for SIGINT/SIGTERM
Step 12: Graceful shutdown in reverse order
```

### GCP Metadata Prefetch (Step 2)

The Agent reads configuration from the GCP instance metadata service at `http://metadata.google.internal/computeMetadata/v1/instance/attributes/`. This is how secrets (API keys, tokens) are passed from the Provisioner to the Agent without being stored on disk.

Environment variables take precedence — if a value is already set in the environment, the GCP metadata value is skipped.

### Ordering Constraints

- Bridge must exist **before** the metadata server can bind to its IP
- Metadata server must be running **before** any VM boots (VMs query it during init)
- Orchestrator loads persisted VM state and cleans up orphans on startup (before accepting new requests)

---

## 3. Network Architecture

### Bridge Network

All MicroVMs on a Host share a Linux bridge for network connectivity:

```
┌───────────────────────────────────────────────────────┐
│ Host VM (192.168.100.1)                                │
│                                                        │
│   ocm-br0 (192.168.100.1/24)                          │
│     │                                                  │
│     ├── tap3a8f2b1c ← VM 1 (192.168.100.10)          │
│     ├── tapb7e9d0a4 ← VM 2 (192.168.100.11)          │
│     └── tapc1f0e5d2 ← VM 3 (192.168.100.12)          │
│                                                        │
│   Services on bridge IP:                               │
│     :80   Metadata server                              │
│     :4000 LLM proxy                                    │
│                                                        │
│   ens4 (external) ← NAT masquerade for VM outbound    │
└───────────────────────────────────────────────────────┘
```

### Bridge Setup (`network/bridge_linux.go`)

1. `ip link add ocm-br0 type bridge`
2. `ip addr add 192.168.100.1/24 dev ocm-br0`
3. `ip link set ocm-br0 up`

### TAP Device per VM

Each VM gets its own TAP device, named `tap` + first 11 characters of the machine UUID (Linux has a 15-character interface name limit):

1. `ip tuntap add dev tapXXXXXXXXXXX mode tap`
2. `ip link set tapXXXXXXXXXXX master ocm-br0`
3. `ip link set tapXXXXXXXXXXX up`

### NAT & Security Rules

```
# Enable IP forwarding
sysctl -w net.ipv4.ip_forward=1

# VMs can reach the internet via NAT
iptables -t nat -A POSTROUTING -s 192.168.100.0/24 -o ens4 -j MASQUERADE

# SECURITY: Block VMs from accessing GCP instance metadata (prevent SSRF)
iptables -I FORWARD -s 192.168.100.0/24 -d 169.254.169.254 -j DROP

# SECURITY: Block VM-to-VM lateral movement (tenant isolation)
iptables -I FORWARD -i ocm-br0 -o ocm-br0 -j DROP

# Allow established connections back
iptables -A FORWARD -i ens4 -o ocm-br0 -m state --state RELATED,ESTABLISHED -j ACCEPT

# Allow VMs to reach the internet
iptables -A FORWARD -i ocm-br0 -o ens4 -j ACCEPT
```

### IP Allocation

IPs are deterministic, based on a machine index: `192.168.100.{10 + index}`. The Scheduler allocates the IP when placing a Machine on a Host.

### MAC Address Generation

MAC addresses are deterministic, derived from the VM's IP to avoid conflicts:

```
IP: 192.168.100.42  →  MAC: 02:FC:00:A8:64:2A
                             │  │   │  ▲  ▲  ▲
                             │  │   │  │  │  └─ ip[3]=42  → 0x2A
                             │  │   │  │  └──── ip[2]=100 → 0x64
                             │  │   │  └─────── ip[1]=168 → 0xA8
                             │  └───┘  locally administered, unicast
                             └──────── "FC" = Firecracker
```

---

## 4. Metadata Service

The metadata server (`metadata/server_linux.go`) is an HTTP server running on the bridge gateway IP that provides configuration to MicroVMs at boot time. This follows the same pattern as AWS EC2 and GCP Compute metadata services.

### How It Works

```
MicroVM (192.168.100.10)              Metadata Server (192.168.100.1:80)
         │                                        │
         │  GET /v1/config                        │
         │───────────────────────────────────────▶│
         │                                        │
         │  Server reads source IP: 192.168.100.10│
         │  Looks up config registered for that IP │
         │                                        │
         │  200 OK { machine_id, openclaw_config } │
         │◀───────────────────────────────────────│
```

The source IP lookup means VMs can only access their own config — no authentication needed within the bridge network.

### Endpoints

| Endpoint | Returns |
|----------|---------|
| `GET /health` | `{"status":"ok"}` |
| `GET /v1/instance` | Machine ID, name, slug |
| `GET /v1/config` | Full MachineConfig (OpenClaw config, gateway token) |
| `GET /v1/llm` | LLM proxy endpoint and key |
| `GET /v1/secrets` | Key-value secret map |

### Registration Flow

When the orchestrator creates a VM, it registers the VM's config with the metadata server **before** booting Firecracker. This ensures the config is available by the time the VM's init script queries it.

```go
// In orchestrator.Create():
o.registrar.RegisterMachine(cfg.VMIP, metadata.MachineConfig{...})
// Then boot Firecracker
machine.Start(vmCtx)
```

When a VM is destroyed, its config is unregistered.

---

## 5. Firecracker VM Creation

The orchestrator (`orchestrator/firecracker_linux.go`) manages the full lifecycle of Firecracker MicroVMs.

### Create Flow

```
1. Check capacity (MaxVMs limit)
       │
2. Create TAP device: tapXXXXXXXXXXX
       │
3. Copy rootfs: cp --reflink=auto base.ext4 → {machineID}.ext4
       │
4. Register config with metadata server
       │
5. Configure Firecracker (kernel, rootfs, network, resources)
       │
6. Start Firecracker process (Unix socket API)
       │
7. Save state to vms.json (crash recovery)
       │
8. [async] Wait for port 3000 to respond (up to 120s)
       │
9. Mark status: "starting" → "running"
```

### Rootfs Copy — Reflink/CoW

The base rootfs image (an ext4 filesystem containing Alpine + Node.js + OpenClaw) is copied using `cp --reflink=auto`. On filesystems that support it (XFS, btrfs), this creates a Copy-on-Write clone that takes nearly zero time and zero additional disk space until the VM writes to the filesystem.

```
/var/lib/ocm/images/rootfs.ext4      ← base template (read-only in practice)
/var/lib/ocm/state/{machineID}.ext4  ← per-VM copy (CoW, diverges on writes)
```

### Firecracker Configuration

```go
firecracker.Config{
    SocketPath:      "/var/lib/ocm/sockets/{machineID}.sock",
    KernelImagePath: "/var/lib/ocm/images/vmlinux.bin",
    KernelArgs:      "console=ttyS0 reboot=k panic=1 pci=off init=/sbin/overlay-init",

    MachineCfg: {
        VcpuCount:  2,       // configurable per Machine
        MemSizeMib: 2048,    // configurable per Machine
        Smt:        false,   // no hyperthreading
    },

    Drives: [{
        DriveID:      "rootfs",
        PathOnHost:   "/var/lib/ocm/state/{machineID}.ext4",
        IsRootDevice: true,
        IsReadOnly:   false,
    }],

    NetworkInterfaces: [{
        StaticConfiguration: {
            MacAddress:  "02:FC:00:xx:xx:xx",      // derived from IP
            HostDevName: "tapXXXXXXXXXXX",
            IPConfiguration: {
                IPAddr:      "192.168.100.X/24",
                Gateway:     "192.168.100.1",
                Nameservers: ["8.8.8.8", "8.8.4.4"],
            },
        },
    }],
}
```

### Kernel Boot Arguments

| Argument | Purpose |
|----------|---------|
| `console=ttyS0` | Serial console output (captured by Agent for logs) |
| `reboot=k` | Use kernel reboot on panic |
| `panic=1` | Reboot 1 second after kernel panic |
| `pci=off` | Disable PCI (Firecracker uses MMIO, not PCI) |
| `init=/sbin/overlay-init` | Custom init script (PID 1) |

### Console Output

Firecracker's stdout/stderr are captured by a `consoleWriter` that prefixes each line with the machine ID and writes to the Agent's log:

```
[vm:a3b2c1d4-...] [    0.000000] Linux version 5.10...
[vm:a3b2c1d4-...] [    0.123456] Freeing unused kernel memory...
[vm:a3b2c1d4-...] Starting OpenClaw gateway...
```

---

## 6. What Happens Inside the MicroVM

After Firecracker boots the kernel, the init script (`/sbin/overlay-init`) runs as PID 1:

```
1. Mount /proc, /sys, /dev, /dev/pts, /tmp
2. Set hostname from machine slug
3. Configure eth0 (static IP from kernel cmdline)
4. Set DNS resolvers (8.8.8.8, 8.8.4.4)
5. Query metadata service at 192.168.100.1:80
   ├── GET /v1/config  → write openclaw.json
   ├── GET /v1/secrets → set environment variables
   └── GET /v1/llm     → set LLM endpoint
6. Start OpenClaw gateway (background, port 3000)
7. Start agent shim (foreground, reports health to host)
```

### Network Configuration Inside the VM

The kernel `ip=` boot parameter configures the network statically:

```
ip=192.168.100.X::192.168.100.1:255.255.255.0::eth0:off
     ▲               ▲                            ▲
     │               │                            │
   VM IP          Gateway                    Interface
```

From inside the VM:
- `192.168.100.1:80` — metadata service (config, secrets)
- `192.168.100.1:4000` — LLM proxy (AI API calls)
- Outbound internet — via NAT masquerade through the Host

---

## 7. Health Checking & Readiness

### VM-Level Health Check

After starting Firecracker, the orchestrator asynchronously polls port 3000 (OpenClaw gateway) on the VM's bridge IP:

```go
go func() {
    if err := waitForPort(cfg.VMIP, 3000, 120*time.Second); err != nil {
        vm.instance.Status = "error"
        return
    }
    vm.instance.Status = "running"
}()
```

The poll runs every 500ms with a 1-second TCP dial timeout, for up to 120 seconds total.

### Agent-Level Health Check

The Agent API exposes health proxying at `GET /proxy/{machineID}/health`, which checks both services inside the VM:

| Port | Service | Role |
|------|---------|------|
| 3000 | OpenClaw gateway | Core service |
| 3001 | Playwright browser | Browser automation |

Returns status: `ok` (both up), `degraded` (one down), `unreachable` (both down).

### Host-Level Health Check

The Provisioner polls `GET /health` on the Agent (port 9090) to determine if the Host is ready. The Agent returns uptime, version, and VM count.

---

## 8. Destroy Flow

```
1. Remove VM from in-memory map
2. Graceful shutdown via Firecracker API (10s timeout)
3. If timeout, force kill: StopVMM()
4. Cancel VM context
5. Cleanup:
   ├── Unregister from metadata server
   ├── Remove TAP device
   ├── Delete rootfs copy
   └── Delete API socket
6. Save state (updated vms.json)
```

---

## 9. State Persistence & Crash Recovery

The orchestrator persists its VM inventory to `/var/lib/ocm/state/vms.json` after every create/destroy operation. This enables crash recovery.

### Persisted State per VM

```json
{
  "machine_id": "a3b2c1d4-e5f6-...",
  "vm_ip": "192.168.100.10",
  "tap_device": "tapa3b2c1d4e5f",
  "socket_path": "/var/lib/ocm/sockets/a3b2c1d4-e5f6-....sock",
  "rootfs_path": "/var/lib/ocm/state/a3b2c1d4-e5f6-....ext4",
  "pid": 12345,
  "vcpus": 2,
  "memory_mb": 2048
}
```

### Recovery on Agent Restart

When the Agent starts, it loads `vms.json` and cleans up orphaned resources:

1. For each persisted VM:
   - Kill the Firecracker process (by PID) if still running
   - Remove the TAP device
   - Delete the rootfs copy
   - Delete the socket file
2. Delete `vms.json` (clean slate for new VMs)

This is a **destructive recovery** — it doesn't attempt to reconnect to running VMs. All VMs are cleaned up and must be re-created by the Control Plane.

---

## 10. End-to-End Data Flow

### Start Machine

```
Dashboard                Control Plane              Scheduler              Host Agent
   │                         │                          │                      │
   │ POST /machines/{id}/start                          │                      │
   │────────────────────────▶│                          │                      │
   │                         │ PlaceMachine(machine)    │                      │
   │                         │─────────────────────────▶│                      │
   │                         │                          │ SELECT host          │
   │                         │                          │ WHERE capacity >= N  │
   │                         │                          │ FOR UPDATE SKIP LOCKED
   │                         │    host, vmIP            │                      │
   │                         │◀─────────────────────────│                      │
   │                         │                                                 │
   │                         │ POST /vms {machineID, config, sizing}          │
   │                         │────────────────────────────────────────────────▶│
   │                         │                                                 │
   │                         │                          Agent creates VM:      │
   │                         │                          ├─ TAP device          │
   │                         │                          ├─ rootfs copy         │
   │                         │                          ├─ metadata register   │
   │                         │                          ├─ Firecracker start   │
   │                         │                          └─ async health poll   │
   │                         │                                                 │
   │                         │ 200 OK                                          │
   │                         │◀────────────────────────────────────────────────│
   │                         │                                                 │
   │ 200 {status: "provisioning", host_id, vm_ip}      │                      │
   │◀────────────────────────│                          │                      │
```

### Stop Machine

```
Dashboard                Control Plane              Host Agent
   │                         │                          │
   │ POST /machines/{id}/stop│                          │
   │────────────────────────▶│                          │
   │                         │ DELETE /vms/{machineID}  │
   │                         │─────────────────────────▶│
   │                         │                          │ Destroy:
   │                         │                          │ ├─ Shutdown VM
   │                         │                          │ ├─ Remove TAP
   │                         │                          │ ├─ Delete rootfs
   │                         │                          │ └─ Unregister metadata
   │                         │ 200 OK                   │
   │                         │◀─────────────────────────│
   │                         │                          │
   │                         │ ReleaseMachine():        │
   │                         │ UPDATE hosts SET         │
   │                         │   used_vcpus -= N        │
   │                         │   machine_count -= 1     │
   │                         │ UPDATE machines SET      │
   │                         │   status='stopped'       │
   │                         │   host_id=NULL           │
   │                         │                          │
   │ 200 {status: "stopped"} │                          │
   │◀────────────────────────│                          │
```

---

## 11. File System Layout on Host

```
/var/lib/ocm/
├── images/
│   ├── vmlinux.bin                 # Firecracker-compatible kernel
│   └── rootfs.ext4                 # Base rootfs template
├── state/
│   ├── vms.json                    # Persisted VM inventory (crash recovery)
│   ├── {machineID-1}.ext4          # Per-VM rootfs copy (reflink/CoW)
│   └── {machineID-2}.ext4
└── sockets/
    ├── {machineID-1}.sock          # Firecracker API socket
    └── {machineID-2}.sock
```

---

## 12. Source Code Map

| Component | Package | Key File(s) |
|-----------|---------|-------------|
| Host provisioning | `internal/provisioner` | `provisioner.go` |
| Agent startup | `cmd/agent` | `main.go` |
| Bridge + TAP + NAT | `internal/network` | `network.go`, `bridge_linux.go` |
| Metadata server | `internal/metadata` | `metadata.go`, `server_linux.go` |
| Orchestrator interface | `internal/orchestrator` | `orchestrator.go`, `types.go` |
| Firecracker lifecycle | `internal/orchestrator` | `firecracker_linux.go` |
| Agent API handlers | `internal/agentapi` | `handlers.go`, `proxy.go` |
| Agent HTTP client | `internal/agentclient` | `client.go` |
| Machine scheduling | `internal/scheduler` | `scheduler.go` |
| Control Plane API | `internal/api` | `server.go` |
| Config loading | `internal/config` | `config.go` |

### Build Tags

Platform-specific code uses Go build tags:
- `//go:build linux` — real implementations (Firecracker, netlink, iptables)
- `//go:build !linux` — stubs returning `ErrNotLinux` (allows macOS development)

---

## 13. Security Model

| Threat | Mitigation |
|--------|------------|
| VM accesses Host GCP metadata | iptables DROP rule for `169.254.169.254` |
| VM-to-VM lateral movement | iptables DROP on `ocm-br0` → `ocm-br0` |
| VM accesses other VM's config | Metadata server uses source-IP lookup (each VM only sees its own) |
| Unauthorized agent access | Bearer token auth on all Agent API endpoints |
| Firecracker escape | KVM hardware isolation, minimal attack surface |
| Orphaned resources after crash | State persistence + cleanup on restart |
| Stale Firecracker process | PID tracked in state file, killed on recovery |
