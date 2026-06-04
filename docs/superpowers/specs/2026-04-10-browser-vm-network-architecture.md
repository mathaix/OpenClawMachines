# Browser VM Network Architecture

**Date:** 2026-04-10
**Branch:** `browser`
**Status:** Implemented — live view working end-to-end

This document captures the complete network topology and data flows for
independent browser VMs with live view support. Written after the feature
shipped, so it reflects reality rather than design-time assumptions.

---

## Goals

1. **AI agent automation** — the agent inside the main VM can drive a real
   Chromium instance via Chrome DevTools Protocol (CDP).
2. **Human visual access** — a user in the OCM frontend can see what the
   browser is doing (and optionally interact) via an embedded iframe.
3. **Isolation** — the browser VM runs in its own Firecracker microVM with
   no direct public network access; all traffic is mediated by the host
   agent.

---

## Overview

Each browser VM is a standalone Firecracker microVM on the same bridge
network (`ocm-br0`, `192.168.100.0/24`) as its paired machine VM. The
browser VM boots the `kernel-browser-rootfs` image, which contains:

- **Xvfb + Mutter** — X server and window manager (port :1)
- **Chromium** — real (headful) browser rendering into Xvfb, CDP on
  `127.0.0.1:9223` (internal only)
- **kernel-images-api** — a Go service that proxies CDP traffic from
  `0.0.0.0:9222` to `127.0.0.1:9223`
- **Neko** — WebRTC server (HTTP+WebSocket on `:8080`, RTP on
  UDP `56000-56100`) that captures the Xvfb display and streams it to
  remote browsers
- **supervisord** — orchestrates all the above processes
- **init-kernel-browser.sh** — Firecracker PID 1, configures the guest
  environment (network, mounts, PATH, Neko env vars) then execs
  `/wrapper.sh`

## Components

```
                    ┌──────────────────────────────────────────┐
                    │ Internet                                 │
                    │                                          │
                    │  ┌───────────┐       ┌───────────┐      │
                    │  │ User's    │       │ Cloud Run │      │
                    │  │ browser   │       │ Backend   │      │
                    │  └─────┬─────┘       └─────┬─────┘      │
                    │        │                    │            │
                    └────────┼────────────────────┼────────────┘
                             │                    │
                             │ HTTPS + WS         │ HTTPS + WS
                             │ (live iframe,      │ (pair/start/stop
                             │  signalling)       │  API calls)
                             │                    │
                             │                    │ Cloudflare Tunnel
                             │                    │ → host_public_ip:9091
                             │                    │
                             │ UDP 56000-56100    │
                             │ (WebRTC media)     │
                             │                    │
            ┌────────────────┼────────────────────┼────────────────┐
            │ OVH host       │                    │                │
            │                ▼                    ▼                │
            │  ┌──────────────────┐   ┌───────────────────────┐    │
            │  │ iptables         │   │ ocm-agent             │    │
            │  │ PREROUTING DNAT  │   │  • control :9090      │    │
            │  │ 56000-56100/udp  │   │  • proxy   :9091      │    │
            │  │ → 192.168.100.3  │   │  • cdpproxy :9222     │    │
            │  └────────┬─────────┘   │    (bridge IP only)   │    │
            │           │              └─────────┬─────────────┘    │
            │           │                        │                  │
            │           │                        │ HTTP reverse     │
            │           │                        │ proxy to VM:8080 │
            │           ▼                        ▼                  │
            │  ┌──────────────────────────────────────────────┐    │
            │  │ Bridge ocm-br0 (192.168.100.0/24)            │    │
            │  │ Gateway 192.168.100.1                        │    │
            │  │                                              │    │
            │  │  ┌───────────────┐    ┌───────────────┐     │    │
            │  │  │ Machine VM    │    │ Browser VM    │     │    │
            │  │  │ 192.168.100.20│    │ 192.168.100.3 │     │    │
            │  │  │               │    │               │     │    │
            │  │  │ OpenClaw      │    │ Xvfb + Mutter │     │    │
            │  │  │ gateway       │    │               │     │    │
            │  │  │               │    │ Chromium      │     │    │
            │  │  │ AI agent uses │    │ :9223 (local) │     │    │
            │  │  │ browser.cdpUrl│    │               │     │    │
            │  │  │ → 192.168.    │    │ kernel-images-│     │    │
            │  │  │   100.1:9222  │    │ api :9222     │     │    │
            │  │  │               │    │ (TCP proxy to │     │    │
            │  │  │               │    │  :9223)       │     │    │
            │  │  │               │    │               │     │    │
            │  │  │               │    │ Neko          │     │    │
            │  │  │               │    │ :8080 HTTP/WS │     │    │
            │  │  │               │    │ 56000-56100   │     │    │
            │  │  │               │    │   UDP (RTP)   │     │    │
            │  │  └───────────────┘    └───────────────┘     │    │
            │  │                                              │    │
            │  └──────────────────────────────────────────────┘    │
            │                                                       │
            └───────────────────────────────────────────────────────┘
```

---

## Data Flow 1 — AI Agent Browsing (CDP)

The AI agent inside the main machine VM drives Chromium via the Chrome
DevTools Protocol, same as any remote CDP client. It connects to a
single, stable URL: `http://192.168.100.1:9222`.

```
AI agent (inside machine VM 192.168.100.20)
  │
  │ HTTP/WS → http://192.168.100.1:9222/json/version
  │          (browser.cdpUrl from assembled config)
  ▼
cdpproxy (in ocm-agent, listens on bridge IP 192.168.100.1:9222)
  │
  │ extractIP(remoteAddr) = 192.168.100.20
  │ target = metaSrv.GetBrowserTarget("192.168.100.20") = "192.168.100.3:9222"
  │
  │ TCP bidirectional pipe
  ▼
Browser VM 192.168.100.3:9222
  │ (kernel-images-api DevToolsProxy)
  ▼
127.0.0.1:9223 (Chromium remote debugging)
```

### Why the proxy chain?

- **Gateway config stability** — the OpenClaw gateway's `browser.cdpUrl`
  is a single constant value across all browser VMs. We don't have to
  rewrite the config when the paired browser VM changes.
- **Source-based routing** — the cdpproxy looks up the target based on
  the requester's bridge IP, which lets multiple machine VMs on the same
  bridge each have a distinct paired browser VM.
- **kernel-images-api's internal proxy** — inside the browser VM,
  Chromium only listens on loopback (security best practice). The Go
  proxy bridges 0.0.0.0:9222 → 127.0.0.1:9223.

### Setup during pairing

When a machine is paired with a browser VM:

1. Control plane validates same host + running status
2. `agent.PairBrowserVM` adds firewall rules via `bridge.AllowVMPair`
   (two iptables `FORWARD ACCEPT` rules between the two VM IPs)
3. `agent.SetCDPTarget` stores `GetBrowserTarget[machineVMIP] = "bvmIP:9222"`
4. `pushBrowserConfigAsync` sets `browser.cdpUrl = "http://192.168.100.1:9222"`
   on the running gateway via `ConfigBatch` + `RestartGateway`

Unpair reverses all four steps. Stopping a paired browser VM auto-unpairs.

---

## Data Flow 2 — Live View HTTP / WebSocket (Control Plane)

The frontend embeds an iframe pointing at the control plane API. The
iframe loads Neko's HTML client and opens a signalling WebSocket.

```
User's browser
  │ HTTPS GET / WS UPGRADE
  │ https://api.openclawmachines.com/api/accounts/:id/browser-vms/:id/live/*
  │ (cookie auth: ocm_token or firebase session)
  ▼
Cloud Run backend
  │ Middleware chain:
  │  • DualModeMiddleware / FirebaseMiddleware (auth)
  │  • userResolverMiddleware
  │  • AccountMiddleware (membership check)
  │ Handler: handleBrowserVMLiveProxy
  │  • Validates bvm.AccountID == accountID
  │  • Validates bvm.Status == "running"
  │  • Resolves host URL via agentClient.ProxyURL(host)
  │  • For WS: forwards Sec-WebSocket-Protocol to upstream
  │
  │ HTTPS/WSS via Cloudflare Tunnel
  ▼
ocm-agent proxy on host:9091
  │ Authorization: Bearer $FC_AGENT_TOKEN
  │ Handler: handleBrowserVMLiveProxy (agentapi)
  │  • Looks up browser VM in orchestrator's browserVMs map → VMIP
  │  • For WS: forwards subprotocol
  │
  │ HTTP/WS (plaintext, bridge network)
  ▼
Browser VM 192.168.100.3:8080 (Neko)
  │ Serves /var/www (HTML client)
  │ Serves /api/ws (signalling WebSocket)
```

### Auth summary

| Hop | Protocol | Auth |
|---|---|---|
| Browser → Backend | HTTPS | Cookie (Firebase or CF Access JWT) |
| Backend → Agent | HTTPS via Cloudflare Tunnel | `FC_AGENT_TOKEN` Bearer |
| Agent → Neko | HTTP on bridge | None (private bridge, firewall-isolated) |

### WebSocket subprotocol forwarding

Both proxies (control plane and agent) forward the client's
`Sec-WebSocket-Protocol` header and honor the negotiated subprotocol
when upgrading the client connection. This is required for noVNC or
websockify clients that request `binary` or `base64` subprotocols.
Without forwarding, the upgrade fails or the stream is unreadable.

---

## Data Flow 3 — WebRTC Media Stream (UDP)

Once signalling is done, Neko and the user's browser negotiate ICE
candidates to establish a direct RTP media stream. This is the hard
part of running WebRTC behind a reverse proxy: the HTTP proxy chain
can't carry UDP packets.

### The NAT1TO1 trick

The browser VM is on a private bridge (`192.168.100.3`). If Neko
advertises that IP as an ICE candidate, the remote browser can't
reach it. `NEKO_WEBRTC_NAT1TO1` tells Neko to advertise a **different**
IP as its host candidate — the host's public IP.

At VM creation time, the orchestrator passes the host's public IP via
the kernel cmdline (`nat1to1_ip=X.X.X.X`). `init-kernel-browser.sh`
parses it and exports:

```
NEKO_WEBRTC_NAT1TO1=X.X.X.X
NEKO_WEBRTC_EPR=56000-56100
NEKO_WEBRTC_ICELITE=true
```

Neko then advertises `<host_public_ip>:56000-56100/udp` to the client.

### The DNAT

For packets arriving at the host's public IP to actually reach the
browser VM, the agent installs iptables rules on `CreateBrowserVM`:

```
# PREROUTING: rewrite destination
iptables -t nat -I PREROUTING -p udp --dport 56000:56100 \
  -j DNAT --to-destination 192.168.100.3

# FORWARD: allow the rewritten traffic to cross into the bridge
iptables -I FORWARD -i <primary_iface> -o ocm-br0 \
  -p udp --dport 56000:56100 -j ACCEPT
```

`RemoveUDPPortRangeDNAT` reverses these on `DestroyBrowserVM`.

### The packet path

```
User's browser
  │
  │ UDP → <host_public_ip>:56042 (one of the RTP ports)
  ▼
Host public interface
  │
  │ iptables PREROUTING DNAT
  │  → dst = 192.168.100.3:56042
  │
  │ iptables FORWARD ACCEPT (primary_iface → ocm-br0)
  ▼
Bridge ocm-br0
  │
  ▼
Browser VM 192.168.100.3:56042
  │
  ▼
Neko → Xvfb capture → VP8/H.264 encode → RTP
```

Return path uses Linux's connection tracking: outbound packets from
the VM's 56000-56100 ports are reverse-NATed back to the same public
source port the user's browser is listening on.

### Requirements the host must satisfy

- **Inbound UDP 56000-56100 allowed** at the host's upstream firewall
  (OVH dashboard, GCP firewall rule, etc.). The agent can install
  iptables rules, but can't reach outside the OS.
- **IP forwarding enabled** (`net.ipv4.ip_forward=1`) — already done
  by the bridge setup during agent startup.
- **connection tracking module** (`nf_conntrack`) — standard Linux
  default, no action needed.

---

## Data Flow 4 — Pairing Setup (Control Plane)

The pair operation is the only multi-step orchestration in this
system. It touches three places (DB, agent firewall, agent metadata
server, gateway config) and has to roll back on any failure.

```
Frontend "Pair" button
  │ POST /api/accounts/:id/machines/:id/pair-browser
  │      Body: {browser_vm_id}
  ▼
Backend handlePairBrowser
  │
  │ 1. Validate (same account, same host, both running, not
  │    already paired)
  │
  │ 2. DB: UPDATE machines SET browser_vm_id = ?
  │    (unique index provides backstop for race conditions)
  │
  │ 3. agent.PairBrowserVM → bridge.AllowVMPair(machine_ip, bvm_ip)
  │    (two iptables FORWARD ACCEPT rules)
  │    Failure → rollback DB
  │
  │ 4. agent.SetCDPTarget → metaSrv.SetBrowserTarget(
  │      machine_ip, "bvm_ip:9222")
  │    Failure → rollback firewall + DB
  │
  │ 5. async: pushBrowserConfigAsync
  │      │
  │      ├─ ConfigBatch: set browser=<json blob>
  │      │    (single StrictJSON op — gateway rejects sub-keys)
  │      ▼
  │    agent.ConfigBatch → openclaw config set browser '{...}'
  │      │
  │      ▼
  │    agent.RestartGateway (hot-reload ignores browser config)
  │
  │ 6. Return 200 to client
```

### Why the config push is async

`RestartGateway` kills and respawns the gateway, which terminates any
open connections including the one that triggered the pair. Running
that synchronously inside the pair handler would close the HTTP
response before the client sees it. The handler returns 200
immediately and the config push runs in a goroutine.

### Why `browser` is set as a single StrictJSON op

The OpenClaw gateway's config schema rejects individual sub-keys like
`browser.attachOnly`. It accepts the whole `browser` object as one
unit. The CLI browse session uses the same pattern.

---

## Lifecycle Edge Cases

### Agent restart with running browser VMs

Before this branch, agent self-update would kill Firecracker processes
because they were children of the agent's cgroup. Main VMs use state
file recovery (`vms.json` + `Recover()` on startup) to reconstruct
their in-memory state after restart.

Browser VMs now follow the same pattern:

- `browser-vms.json` state file with `persistedBrowserVM` records
- `saveBrowserState()` called on every create/destroy/status change
- `recoverBrowserVMs()` called from `Recover()` — for each entry it
  performs identity verification before re-registering the VM:
  `pidAlive(p.PID)` must succeed, `/proc/<pid>/comm` must equal
  `firecracker`, the Firecracker socket must exist, and the reattached
  Firecracker instance must report the expected VM ID before the entry
  is accepted back into the `browserVMs` map
- Orphan cleanup: if the Firecracker process is dead, remove the TAP
  device, rootfs file, and socket file

One important implementation detail: browser VM persistence does not
call `machine.PID()` after reattach. The in-memory `browserRunningVM`
stores a stable `PID` field captured at create time or restored from
the persisted record, and `saveBrowserState()` writes that field back
to disk. This avoids a Firecracker Go SDK edge case where a reattached
`*firecracker.Machine` still has a live socket client but no child
`exec.Cmd`, so `machine.PID()` returns 0 even though Firecracker is
still running.

This only works if the Firecracker process survives the agent
restart. Today that depends on the systemd unit's `KillMode`. If it's
`control-group` (the default), systemd kills all children, including
Firecracker. The recovery logic still runs but finds nothing to
recover. `KillMode=process` is required for true zero-downtime agent
updates. (This is a pre-existing constraint on the main VM path too.)

If identity verification fails, the current implementation chooses
non-destructive failure over PID-based cleanup: it does not try to kill
the PID just because it appears alive. Instead, the record is kept in a
quarantined state on disk and excluded from the active in-memory routing
tables until a later recovery attempt or manual/operator reconciliation
resolves the mismatch.

### CDP readiness timeout

The kernel-browser rootfs boots Ubuntu + Chromium + Neko under Xvfb,
which takes 60–90 seconds on a cold start. `CreateBrowserVM` polls
the CDP port with a 3-minute timeout. If CDP doesn't come up, the VM
is torn down (Firecracker stopped, TAP removed, rootfs file removed,
browserVMs map entry deleted) before returning the error — no orphans.

### Host mismatch after machine migration

Browser VM pairing is host-local: the machine and browser VM must be
on the same host for the bridge network to carry their CDP traffic.
If a machine gets migrated or restarted on a different host, its
paired browser VM stays on the original host. The machine start path
checks `machine.BrowserVMID` and calls `UnpairBrowserVM` if the
browser VM is on a different host.

The user sees the machine come up without the live view, and can
either recreate the pairing (starting a new browser VM on the new
host) or move the browser VM. Today we only support the first
option — "move the browser VM" would require live migration, which
Firecracker doesn't support.

### Browser VM stopped while paired

Stopping a browser VM (via UI or agent) is allowed even if paired.
The orchestrator:

1. Auto-unpairs the machine (clears `browser_vm_id`, removes firewall
   rules, resets CDP target, pushes updated config)
2. Stops the Firecracker VM
3. Removes the UDP DNAT

Similarly, deleting a browser VM auto-unpairs before destruction.

---

## Scaling and Limits

### Per-host concurrent browser VMs

| Limit | Value | Bottleneck |
|---|---|---|
| Compute | ~4–8 | Each VM takes 2 vCPU + 4 GB RAM |
| Bridge IP allocation | 150+ | `192.168.100.2-254` minus machines |
| CDP proxy routing | Unlimited | Already source-IP based |
| **WebRTC UDP range** | **1 concurrent live view** | Single flat DNAT rule for 56000-56100 |

The WebRTC port range is the current bottleneck. Only one browser VM
per host can stream live video at a time. Multiple VMs can still run
for agent-only use (CDP path works fine via the cdpproxy).

### Future: multi-viewer scaling

Three approaches to lift the 1-live-view-per-host limit:

1. **Per-VM port range** — allocate a unique 100-port range per
   browser VM (`56000-56099`, `56100-56199`, …). Agent tracks
   allocations, passes both the host IP and the port range to Neko via
   kernel cmdline. Caps at ~50 live views per host.

2. **Stateful conntrack** — single port range shared by all VMs,
   distinguished by source IP at conntrack time. More complex, harder
   to get right.

3. **TURN relay** — run a TURN server on the host or cloud. All
   browser VMs send their media through the TURN relay. No per-VM
   port allocation. Needs one public port (3478 UDP + 5349 TCP).
   Cleanest but adds infra.

---

## File Inventory

| Area | File | Role |
|---|---|---|
| Init script | `scripts/init-kernel-browser.sh` | Firecracker PID 1 — mounts, network, env vars, execs wrapper |
| Rootfs build | `scripts/build-kernel-browser-rootfs.sh` | Builds ext4 from `ocm-kernel-images/chromium-headful` Docker image, injects init |
| Orchestrator | `backend/internal/orchestrator/firecracker_linux.go` | `CreateBrowserVM`, `DestroyBrowserVM`, state persistence |
| Bridge helpers | `backend/internal/network/bridge_linux.go` | `AllowVMPair`, `AllowUDPPortRangeDNAT` |
| CDP proxy | `backend/internal/cdpproxy/proxy.go` | TCP forwarder on `bridge_ip:9222`, source-IP routing |
| Metadata | `backend/internal/metadata/metadata.go` | `SetBrowserTarget`/`GetBrowserTarget` |
| Agent live proxy | `backend/internal/agentapi/browser_live_proxy.go` | HTTP/WS reverse proxy to Neko :8080 |
| Control plane live proxy | `backend/internal/api/browser_vm_live.go` | HTTPS iframe endpoint, forwards to agent |
| Pairing API | `backend/internal/api/browser_vms.go` | `handlePairBrowser`, `handleUnpairBrowser`, `pushBrowserConfigAsync` |
| Config assembly | `backend/internal/configassembly/assembler.go` | Injects `browser.cdpUrl` when paired |
| Frontend tab | `frontend/src/pages/machine-tabs/BrowserTab.tsx` | Pair/unpair UI, live iframe |
| E2E test | `scripts/test-browser-vm-firecracker-e2e.sh` | Local Firecracker boot + CDP/Neko smoke test |

---

## Related Documents

- `docs/superpowers/specs/2026-04-09-browser-vm-design.md` — the
  original design spec for independent browser VMs
- `docs/superpowers/specs/2026-04-10-vnc-browser-experience-design.md` —
  the live view design spec
- `docs/superpowers/plans/2026-04-09-browser-vm.md` — the implementation
  plan that kicked off this feature
- `docs/architecture.md` — overall OCM architecture
- `CLAUDE.md` §"Firecracker VM Environment" — Firecracker guest
  constraints (minimal /dev, no systemd, manual mounts, etc.)
