# OVH VPS Direct Provisioning — Design Exploration

**Status:** Exploration / Not yet implemented
**Date:** 2026-03-31

## Overview

Instead of running Firecracker microVMs on bare-metal hosts, provision each OCM "machine" as a standalone OVH VPS. The control plane manages VPS lifecycle via the OVH API. This eliminates the Firecracker layer, the network bridge, and the heavyweight agent orchestrator.

## Current vs Proposed Architecture

### Current: Bare Metal → Firecracker → Services

```
OVH Bare Metal ($90/mo, 6c/12t, 64GB)
  └── Agent (orchestrator, bridge, metadata server, proxy)
       ├── Firecracker VM 1 (machine A) — gateway, pty, tunnel
       ├── Firecracker VM 2 (machine B) — gateway, pty, tunnel
       └── ... (many VMs per host)
```

### Proposed: VPS = Machine Directly

```
OVH VPS 1 ($6-20/mo) → machine A — gateway, pty, tunnel
OVH VPS 2 ($6-20/mo) → machine B — gateway, pty, tunnel
```

## Trade-offs

| Factor | Bare Metal + Firecracker | VPS Direct |
|--------|--------------------------|------------|
| Cost per machine | ~$5-8 (amortized across ~10 VMs) | $6-20 (OVH VPS pricing) |
| Boot time | ~125ms (Firecracker) | ~2-5 min (order + rebuild + cloud-init) |
| Isolation | KVM hardware isolation | Full VPS isolation |
| Density | 10+ machines per host | 1 machine = 1 VPS |
| Ops complexity | High (agent, bridge, rootfs pipeline) | Low (systemd services) |
| Scaling | Manual (buy more bare metal) | API-driven (OVH API) |
| Stop billing | Yes (just kill process) | VPS still billed when stopped |
| Data persistence | Host-local ext4 volume | VPS disk (survives reboot, lost on terminate) |

## What Changes vs What Stays

| Layer | Current (Firecracker) | New (VPS Direct) |
|-------|----------------------|-------------------|
| Control Plane API | No change | No change |
| Machine DB model | No change | Add `provider=ovh-vps` |
| Placement | Find host with capacity, allocate bridge IP | Call OVH API to order VPS |
| Config delivery | Agent metadata server on bridge | cloud-init userdata script |
| VM creation | Agent Firecracker API | OVH order + rebuild API |
| Start/Stop | Agent `/vms/{id}/stop` | OVH `POST /vps/{name}/start\|stop` |
| Services inside | init-openclaw.sh (PID 1) | systemd units (same services) |
| Tunnel | Per-VM cloudflared | Per-VPS cloudflared (same) |
| Networking | Bridge 192.168.100.x + NAT | Public IP (VPS has its own) |
| Agent | Full orchestrator on bare metal | Lightweight agent (heartbeat + self-update) |
| Data persistence | ext4 volume on host disk | VPS disk |

## OVH VPS Plans (US Region)

| Plan | vCores | RAM | Storage | Bandwidth | Monthly |
|------|--------|-----|---------|-----------|---------|
| VPS-1 | 4 | 8 GB | 75 GB SSD | 400 Mbps | $6.46 |
| VPS-2 | 6 | 12 GB | 100 GB NVMe | 1 Gbps | $9.99 |
| VPS-3 | 8 | 24 GB | 200 GB NVMe | 1.5 Gbps | $19.97 |
| VPS-4 | 12 | 48 GB | 300 GB NVMe | 2 Gbps | $36.98 |
| VPS-5 | 16 | 64 GB | 350 GB NVMe | 2.5 Gbps | $54.82 |
| VPS-6 | 24 | 96 GB | 400 GB NVMe | 3 Gbps | $73.10 |

All plans include daily backup, unlimited traffic, and free installation.

### Suggested Size Mapping

| OCM Size | VPS Plan | Rationale |
|----------|----------|-----------|
| Basic | VPS-1 | 4 vCores, 8GB — enough for gateway + terminal |
| Standard | VPS-2 | 6 vCores, 12GB — comfortable for most workloads |
| Pro | VPS-3 | 8 vCores, 24GB — heavy workloads, browser companion |

## OVH VPS API

### Authentication

OVH API uses application key + secret + consumer key (legacy) or OAuth2. Go SDK: [`github.com/ovh/go-ovh`](https://github.com/ovh/go-ovh). Endpoint for US: `ovh-us`.

```go
client, err := ovh.NewClient("ovh-us", appKey, appSecret, consumerKey)
```

### Key API Endpoints

#### VPS Lifecycle

| Operation | Method | Endpoint |
|-----------|--------|----------|
| List VPS instances | GET | `/vps` |
| Get VPS details | GET | `/vps/{serviceName}` |
| Start VPS | POST | `/vps/{serviceName}/start` |
| Stop VPS | POST | `/vps/{serviceName}/stop` |
| Reboot VPS | POST | `/vps/{serviceName}/reboot` |
| Rebuild (reinstall) | POST | `/vps/{serviceName}/rebuild` |
| Get IPs | GET | `/vps/{serviceName}/ips` |
| Get datacenter | GET | `/vps/{serviceName}/datacenter` |
| List tasks | GET | `/vps/{serviceName}/tasks` |
| Get console URL | POST | `/vps/{serviceName}/getConsoleUrl` |
| Request termination | POST | `/vps/{serviceName}/terminate` |
| Confirm termination | POST | `/vps/{serviceName}/confirmTermination` |

#### VPS Ordering

| Operation | Method | Endpoint |
|-----------|--------|----------|
| Create cart | POST | `/order/cart` |
| Add VPS to cart | POST | `/order/cart/{cartId}/vps` |
| Set VPS options | POST | `/order/cart/{cartId}/vps/options` |
| Checkout | POST | `/order/cart/{cartId}/checkout` |
| Get available datacenters | GET | `/vps/order/rule/datacenter` |
| Get available OS choices | GET | `/vps/order/rule/osChoices` |
| Get available images | GET | `/vps/{serviceName}/images/available` |
| Get current image | GET | `/vps/{serviceName}/images/current` |

#### Snapshots & Backups

| Operation | Method | Endpoint |
|-----------|--------|----------|
| Create snapshot | POST | `/vps/{serviceName}/createSnapshot` |
| Get snapshot | GET | `/vps/{serviceName}/snapshot` |
| Revert to snapshot | POST | `/vps/{serviceName}/snapshot/revert` |
| List backup restore points | GET | `/vps/{serviceName}/automatedBackup/restorePoints` |
| Restore from backup | POST | `/vps/{serviceName}/automatedBackup/restore` |

## VPS Lifecycle Flow

### 1. Machine Create + Start (First Boot)

```
User clicks "Create Machine" (size=Basic → VPS-1)
    │
    ▼
Control Plane: machines.Start()
    │
    ├─ Generate tokens (gateway, proxy, signing key)
    ├─ Create Cloudflare tunnel + DNS route
    ├─ Assemble gateway config (same as today)
    │
    ▼
ovhvps.Provider.CreateAndStart()
    │
    ├─ 1. Create cart:       POST /order/cart
    ├─ 2. Add VPS item:      POST /order/cart/{id}/vps (planCode from size)
    ├─ 3. Set datacenter:    POST /order/cart/{id}/item/{itemId}/configuration
    ├─ 4. Set OS image:      POST /order/cart/{id}/item/{itemId}/configuration
    ├─ 5. Checkout:          POST /order/cart/{id}/checkout (auto-pay)
    ├─ 6. Poll until ready:  GET /vps/{serviceName} (wait for delivery)
    ├─ 7. Get IP:            GET /vps/{serviceName}/ips
    ├─ 8. Rebuild with cloud-init:
    │     POST /vps/{serviceName}/rebuild
    │     {imageId: "ubuntu-24.04", sshKeyIds: [...], userData: <cloud-init>}
    │
    ▼
Cloud-init runs on VPS boot:
    ├─ Install cloudflared
    ├─ Write /etc/ocm/agent.env (tokens, backend URL)
    ├─ Write /etc/ocm/openclaw.json (gateway config)
    ├─ Download + start lightweight agent
    ├─ Start gateway, PTY server, auth proxy as systemd units
    ├─ Start cloudflared tunnel
    ├─ Agent sends first heartbeat → status = "running"
```

### 2. Machine Stop

```
machines.Stop()
  → POST /vps/{serviceName}/stop   (power off, preserves disk)
  → Release capacity in DB
  → status = "stopped"
```

### 3. Machine Restart

```
machines.Start()  (machine already has VPS assigned)
  → POST /vps/{serviceName}/start  (power on)
  → Wait for heartbeat
  → status = "running"
```

### 4. Machine Delete

```
machines.Delete()
  → POST /vps/{serviceName}/terminate
  → POST /vps/{serviceName}/confirmTermination
  → Delete Cloudflare tunnel + DNS
  → Remove DB records
```

## New Components

### File Structure

```
backend/internal/ovhvps/
├── client.go        # OVH API client wrapper (go-ovh SDK)
├── provider.go      # VPS lifecycle: order, rebuild, start, stop, destroy
├── cloudinit.go     # Generate cloud-init userdata per machine
└── provider_test.go
```

### Host Model for VPS

Each VPS creates a 1:1 host record in the database:

```go
Host{
    Provider:        "ovh-vps",
    ProviderClass:   "vps",
    LifecycleMode:   "managed",           // Control plane manages via OVH API
    ProviderHostID:  "vps-abc123",        // OVH serviceName
    ProviderMetadata: json.RawMessage(`{
        "plan_code":  "vps-2025-model2",
        "datacenter": "US-HIL",
        "order_id":   "12345678"
    }`),
    CapacityVCPUs:    6,                  // Matches VPS plan (1:1, no oversubscription)
    CapacityMemoryMB: 12288,
}
```

### Cloud-init Template

```yaml
#cloud-config
packages:
  - curl
  - jq

write_files:
  - path: /etc/ocm/agent.env
    permissions: '0600'
    content: |
      BACKEND_URL=${BACKEND_URL}
      HOST_ID=${HOST_ID}
      AGENT_TOKEN=${AGENT_TOKEN}
      MACHINE_ID=${MACHINE_ID}
      GATEWAY_TOKEN=${GATEWAY_TOKEN}
      PROXY_TOKEN=${PROXY_TOKEN}
      SIGNING_KEY=${SIGNING_KEY}
      TUNNEL_TOKEN=${TUNNEL_TOKEN}
      VM_HOSTNAME=${VM_HOSTNAME}
  - path: /etc/ocm/openclaw.json
    permissions: '0644'
    content: |
      ${OPENCLAW_CONFIG_JSON}

runcmd:
  - curl -sL https://pkg.cloudflare.com/cloudflared-stable-linux-amd64.deb -o /tmp/cf.deb
  - dpkg -i /tmp/cf.deb
  - curl -sL ${BACKEND_URL}/api/agent/install-vps | bash
```

### Lightweight VPS Agent

A stripped-down version of the current agent with only:

- **Heartbeat** — report health to control plane every 60s
- **Self-update** — poll GCS for new agent binary, restart on update
- **Service supervision** — ensure gateway, PTY, auth proxy, cloudflared stay running
- **Config updates** — accept config pushes from control plane

Does NOT need:
- Firecracker orchestrator
- Network bridge / TAP devices
- Metadata server
- Rootfs staging / reflink copies

### Placement Routing

```go
func (rs *RuntimeService) Start(ctx context.Context, ...) {
    if machine.Provider == "ovh-vps" {
        // Route to OVH VPS provider
        return rs.ovhvpsProvider.CreateAndStart(ctx, machine, config)
    }
    // Existing flow: bare-metal placement
    return rs.placementService.Reserve(ctx, machineID, req)
}
```

### Reconciler Extension

```go
// OVH VPS instance checker — verifies VPS still exists
type OVHVPSChecker struct {
    client *ovhvps.Client
}

func (c *OVHVPSChecker) InstanceExists(ctx context.Context, serviceName string) (bool, error) {
    var vps struct{ State string }
    err := c.client.Get("/vps/"+serviceName, &vps)
    if err != nil {
        // 404 = terminated
        return false, nil
    }
    return true, nil
}
```

## Optimization: VPS Pool

First-boot provisioning takes ~2-5 minutes (OVH order + delivery + cloud-init). To reduce this:

1. **Pre-provision a pool** of idle VPS instances with base Ubuntu image
2. When a machine is created, **claim a VPS from the pool** and rebuild with cloud-init
3. Rebuild is faster than ordering (~1-2 min vs 2-5 min)
4. Background job **replenishes the pool** to maintain N idle instances

Pool size could be configured per region/plan.

## Open Questions

1. **Cloud-init support** — OVH VPS rebuild supports SSH key injection, but cloud-init userdata support is listed as a roadmap item (not confirmed available). Fallback: SSH into newly created VPS and run setup script remotely (similar to current `make enroll-host` flow).
2. **Stop billing** — Stopped VPS still incurs charges. For cost-sensitive use cases, "stop" could mean "snapshot + terminate" and "start" could mean "order new VPS + restore snapshot". More complex but truly stops billing.
3. **VPS plan codes** — Exact planCodes for current VPS lineup need to be fetched from the catalog API (`GET /order/catalog/public/vps?ovhSubsidiary=US`). Plan codes may follow pattern like `vps-2025-model1` through `vps-2025-model6`.
4. **OVH API rate limits** — Need to verify rate limits for ordering and management endpoints. May need queuing for bulk operations.
5. **Hybrid routing** — How to let users choose VPS vs bare-metal at machine creation time? Could be a machine "infrastructure" option or automatic based on availability.

## References

- OVH Go SDK: https://github.com/ovh/go-ovh
- OVH US API Console: https://api.us.ovhcloud.com/console/
- OVH VPS Ordering Guide: https://support.us.ovhcloud.com/hc/en-us/articles/39499067423379
- OVH API Getting Started: https://help.ovhcloud.com/csm/en-api-getting-started-ovhcloud-api
- Current bare-metal host setup: `docs/ovh-host-setup.md`
- Current architecture: `docs/architecture.md`
