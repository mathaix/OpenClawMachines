# Technical Debt & Improvement Roadmap

Research conducted 2026-02-14 across 8 areas: Ravel orchestrator, cloudflare-go SDK, Fly.io init-snapshot, firecracker-go-sdk, Hocus workspace patterns, Rust VM/init ecosystem, Rust CLI/perf tools, and Rust+Go interop.

---

## Critical (fix now)

### 1. Init: No graceful shutdown
**File:** `scripts/init-openclaw.sh`
**Risk:** Data corruption on VM stop — child processes aren't signaled, data volume isn't unmounted.
**Fix:** Add `trap cleanup SIGTERM SIGINT` handler (~15 lines of bash).
```bash
cleanup() {
    kill "$GATEWAY_PID" "$PTY_PID" "$CONFIG_WATCHER_PID" 2>/dev/null
    [ -n "$AUTHPROXY_PID" ] && kill "$AUTHPROXY_PID" 2>/dev/null
    [ -n "$CLOUDFLARED_PID" ] && kill "$CLOUDFLARED_PID" 2>/dev/null
    for pid in $GATEWAY_PID $PTY_PID; do
        timeout 5 tail --pid="$pid" -f /dev/null 2>/dev/null
    done
    sync
    mountpoint -q /data 2>/dev/null && { umount /data 2>/dev/null || umount -l /data; }
}
trap cleanup SIGTERM SIGINT
```
**Effort:** 30 min | **Source:** Fly.io init-snapshot

### 2. Init: No zombie reaping
**File:** `scripts/init-openclaw.sh`
**Risk:** PID 1 must reap zombies. Orphaned child processes accumulate.
**Fix:** Add `wait -n` in the supervisor loop.
**Effort:** 10 min | **Source:** Fly.io init-snapshot

---

## High Priority

### 3. Snapshot/Restore for sub-second boot
**Files:** `backend/internal/orchestrator/firecracker_linux.go`
**Problem:** Gateway startup takes ~55s (Node.js module loading). Every VM cold-boots.
**Solution:** Pre-warm a VM, wait for gateway to be ready, snapshot it via `machine.CreateSnapshot()`. Restore future VMs from snapshot in <1s. The firecracker-go-sdk already supports this:
```go
machine.PauseVM(ctx)
machine.CreateSnapshot(ctx, memFilePath, snapshotPath)
// Later:
Config.Snapshot = SnapshotConfig{MemFilePath: "...", SnapshotPath: "...", ResumeVM: true}
```
**Effort:** High (requires handling network state, file descriptors, app state on restore)
**Impact:** Transformative — 55s → <1s perceived boot time
**Source:** firecracker-go-sdk, Ravel

### 4. Server-side VM health monitoring
**Files:** `backend/internal/orchestrator/firecracker_linux.go`, `backend/internal/api/server.go`
**Problem:** We only check health at boot (one-shot `waitForPort`). If a VM dies after boot, it stays "running" in the DB forever. Frontend polls every 10s but that's client-side only.
**Solution:** Add continuous health check goroutine per VM with states: `Starting → Healthy → Unhealthy`. Track consecutive failures against a retry threshold.
```go
go func() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    failures := 0
    for range ticker.C {
        if err := checkHealth(vm); err != nil {
            failures++
            if failures >= 3 { vm.SetStatus("error"); return }
        } else {
            failures = 0
        }
    }
}()
```
Also add `machine.Wait(ctx)` goroutine to detect unexpected VM crashes (returns when VMM process exits).
**Effort:** Low | **Source:** Ravel, Hocus

### 5. Transitional VM states
**Files:** `backend/internal/store/store.go`, `backend/internal/api/server.go`
**Problem:** No intermediate states between API call and completion. Double-start races possible. UI can't show accurate transitional state.
**Solution:** Add `pending_start`, `pending_stop`, `pending_delete` states. Set transitional state before doing work, set final state after completion.
**Effort:** Low | **Source:** Hocus

### 6. Resilient cleanup on stop/delete
**Files:** `backend/internal/api/server.go` (`handleStopMachine`, `handleDeleteMachine`)
**Problem:** Cleanup steps run sequentially. If an intermediate step fails (e.g., tunnel delete), later cleanup is skipped, leaving orphaned resources.
**Solution:** Check-before-delete pattern. Log warnings instead of failing when resources are already gone. Continue cleanup even if one step fails.
**Effort:** Low | **Source:** Hocus

---

## Medium Priority

### 7. Extract `cfapi` library
**Files:** `backend/internal/tunnel/tunnel.go` (419 lines), `backend/internal/kvstore/cloudflare.go`
**Problem:** The CF API request/response pattern is duplicated 9 times in tunnel.go. `CreateTunnel`/`CreateVMTunnel` are nearly identical (40 lines each). Every method repeats: build URL, set auth header, do request, decode envelope, check success, unmarshal result.
**Solution:** Extract `backend/pkg/cfapi/` — a thin Cloudflare REST client:
```go
type Client struct { apiToken, accountID string; http *http.Client }
func (c *Client) Do(ctx, method, path string, body interface{}) (json.RawMessage, error)
func (c *Client) AccountPath(suffix string) string
```
Reduces tunnel.go from 419 → ~250 lines. Merges duplicate Create/Configure methods.
**Eliminates:** ~180 lines of boilerplate
**Open-sourceable:** Yes (stdlib only)
**Effort:** Medium | **Source:** cloudflare-go research

### 8. Extract `jwkauth` library
**Files:** `backend/internal/auth/cfaccess.go` (201 lines)
**Problem:** JWKS fetch → cache → validate → middleware chain is tightly coupled to CF Access naming but architecturally generic.
**Solution:** Extract `backend/pkg/jwkauth/` — a provider-agnostic JWKS JWT validator:
```go
v := jwkauth.New(jwkauth.Config{JWKSURL: "...", Issuer: "...", Audience: "..."})
r.Use(v.Middleware("Cf-Access-Jwt-Assertion", newClaims))
```
**Eliminates:** ~120 lines of boilerplate
**Open-sourceable:** Yes (depends only on `golang-jwt/v5`)
**Effort:** Medium

### 9. Extract `authproxy` library
**Files:** `backend/cmd/authproxy/main.go` (191 lines)
**Problem:** Token validation + scoped routing + prefix stripping + WebSocket proxy is inline in `main()`. Same pattern needed in `agentapi/proxy.go`.
**Solution:** Extract `backend/pkg/authproxy/`:
```go
proxy := authproxy.New(validateToken, []authproxy.Route{
    {PathPrefix: "/terminal", Upstream: "http://127.0.0.1:7681", RequiredScope: "terminal"},
    {PathPrefix: "/gateway",  Upstream: "http://127.0.0.1:18789", RequiredScope: "gateway"},
})
```
**Eliminates:** ~80 lines
**Open-sourceable:** Yes (stdlib only)
**Effort:** Medium

### 10. Extract `reaper` library
**Files:** `backend/internal/tunnel/reaper.go` (79 lines)
**Problem:** The "list → check liveness → delete orphans" pattern will be copy-pasted for KV entries, GCP VMs, data volumes.
**Solution:** Extract `backend/pkg/reaper/`:
```go
reaper.Start(ctx, reaper.Config{
    Name: "tunnel-reaper", Interval: 10 * time.Minute,
    List: listCFTunnels, Check: isMachineActive, Delete: deleteTunnelAndDNS,
})
```
**Eliminates:** ~40 lines per reaper instance
**Open-sourceable:** Yes (stdlib only)
**Effort:** Low

### 11. Replace `ip` shell-outs with `vishvananda/netlink`
**Files:** `backend/internal/network/bridge_linux.go`
**Problem:** TAP creation shells out to `ip tuntap add`, `ip link set master`, etc. Fragile error parsing, PATH dependency.
**Solution:** Use Go `netlink` library for programmatic TAP/bridge management:
```go
tap := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: tapName}, Mode: netlink.TUNTAP_MODE_TAP}
netlink.LinkAdd(tap)
netlink.LinkSetMaster(tap, bridge)
netlink.LinkSetUp(tap)
```
**Effort:** Low | **Source:** Ravel

### 12. Firecracker MMDS instead of custom metadata server
**Files:** `backend/internal/metadata/metadata.go`, `backend/internal/orchestrator/firecracker_linux.go`
**Problem:** Custom HTTP metadata server on bridge gateway IP. VMs need network up before fetching config. 15+ fork+exec cycles for curl+jq in init.
**Solution:** Use Firecracker's built-in MMDS (Machine Metadata Service) at `169.254.169.254`. Push config via SDK before boot:
```go
machine.SetMetadata(ctx, configJSON) // goes through API socket, not network
```
Init reads from `curl http://169.254.169.254/latest/meta-data/` — no bridge dependency.
**Caveat:** MMDS is for static/semi-static data. Keep custom server if dynamic updates (secret rotation) are needed.
**Effort:** Medium | **Source:** firecracker-go-sdk, Fly.io init

### 13. FSM for VM states
**Files:** `backend/internal/orchestrator/firecracker_linux.go`
**Problem:** Ad-hoc `vm.instance.Status = "running"` string assignments. No transition guards — invalid state changes are possible.
**Solution:** Formal state machine with explicit transitions:
```
created → preparing → stopped → starting → running → stopping → stopped → destroying → destroyed
```
With guards: `canStart()` only from `stopped`, `canStop()` only from `running`, etc.
**Effort:** Medium | **Source:** Ravel

---

## Low Priority

### 14. Firecracker Jailer
**Files:** `backend/internal/orchestrator/firecracker_linux.go`
**Problem:** We run Firecracker as root with no jailer. If a VM escapes the sandbox, it has root access to the host.
**Solution:** Enable the Jailer via SDK config (chroot + non-root UID/GID + cgroups). Requires restructuring file layout.
**Effort:** High | **Source:** firecracker-go-sdk, Ravel

### 15. tmux session wrapping for terminal persistence
**Files:** `scripts/init-openclaw.sh`, rootfs
**Problem:** Terminal session lost on browser refresh — ttyd starts a new shell.
**Solution:** Run ttyd against a tmux session. User reconnects to same session with full history.
**Effort:** Low | **Source:** Hocus

### 16. fstrim cron in rootfs
**Files:** `rootfs/Dockerfile.openclaw`
**Problem:** Deleted files inside VM don't free disk space on host (Firecracker doesn't support TRIM by default on all configs).
**Solution:** Add periodic `fstrim` via systemd timer or cron inside the VM.
**Effort:** Very low | **Source:** Hocus (Firecracker memory/storage analysis)

### 17. Memory balloon for dynamic VM sizing
**Files:** `backend/internal/orchestrator/firecracker_linux.go`
**Problem:** Fixed 2048MB per VM. Unused memory can't be reclaimed.
**Solution:** Enable virtio-balloon via SDK. Inflate to reclaim, deflate to give back.
```go
machine.CreateBalloon(ctx, amountMib, deflateOnOom, statsInterval)
machine.GetBalloonStats(ctx)
```
**Effort:** Low | **Source:** firecracker-go-sdk

### 18. Instance recovery on agent restart
**Files:** `backend/internal/orchestrator/firecracker_linux.go`
**Problem:** On agent restart, we kill orphaned VMs and clean up. Users experience downtime.
**Solution:** Reconnect to running VMs via Firecracker API socket instead of killing them. SDK supports this.
**Effort:** Medium | **Source:** Ravel

### 19. Go init rewrite
**Files:** `scripts/init-openclaw.sh` (611 lines)
**Problem:** Bash init forks 15+ processes for config, has ~100-200ms overhead vs compiled init.
**Solution:** Rewrite PID 1 in Go with embedded API, direct syscalls, process supervision. Only worth it after gateway startup bottleneck (~55s) is solved (via snapshot/restore).
**Effort:** Medium-High | **Source:** Fly.io init-snapshot

### 20. `caarlos0/env` for config loading
**Files:** `backend/internal/config/config.go` (165 lines)
**Problem:** 40+ manual `os.Getenv()` calls with type conversion.
**Solution:** Struct tags: `Port int \`env:"PORT" envDefault:"8080"\``
**Effort:** Low

### 21. `tanstack/react-query` for frontend API layer
**Files:** `frontend/src/lib/api.ts`, `frontend/src/hooks/useMachineToken.ts`
**Problem:** Manual fetch + state + retry logic in custom hooks.
**Solution:** Built-in caching, retry, stale-while-revalidate. Eliminates custom hooks.
**Effort:** Medium

---

## Library Extraction Summary

| Library | Location | Eliminates | Deps | Open-source |
|---------|----------|-----------|------|-------------|
| `cfapi` | `backend/pkg/cfapi/` | ~180 lines | stdlib | Yes |
| `jwkauth` | `backend/pkg/jwkauth/` | ~120 lines | golang-jwt/v5 | Yes |
| `authproxy` | `backend/pkg/authproxy/` | ~80 lines | stdlib | Yes |
| `reaper` | `backend/pkg/reaper/` | ~40 lines/instance | stdlib | Yes |

---

## Not Worth Changing

| Area | Why |
|------|-----|
| Raw SQL with pgx | Deliberate choice, full query control, no ORM overhead |
| Chi router | Solid, minimal, no reason to switch |
| XFS reflink copies | Better than Ravel's devmapper, instant CoW |
| Single-binary agent | Simpler than Ravel's multi-component stack |
| Firecracker (vs QEMU) | Hocus's reasons for switching don't apply (no Docker-in-VM, no GPU needed) |
| Kubernetes/Istio | Would help with auth proxy + deploys but can't orchestrate Firecracker VMs |
| CF Access browser rendering (SSH/VNC) | CF can render SSH/VNC terminals in-browser without client software. However: (1) sets `X-Frame-Options: DENY` — cannot be embedded in an iframe, (2) `CF_Authorization` cookie doesn't propagate cross-origin due to third-party cookie restrictions, (3) renders on full subdomain only (`vm-xyz.openclawmachines.com`), not a path. This forces a multi-tab UX and separate auth flow. Our current approach (ttyd embedded in React workspace via tunnel) provides a unified single-page experience. **Use as:** admin escape hatch / fallback when the app is down but VMs are running. |

---

## Implementation Order

```
Critical (#1-2)     →  High (#3-6)      →  Medium (#7-13)    →  Low (#14-21)
  30 min bash fixes      Easy wins first       Library extractions     When needed
                         #4,5,6 in parallel    #7 first (biggest win)
                         #3 is transformational
```

---

## Rust Ecosystem

### Go+Rust Integration Strategy

Use the **standalone binary** pattern (how Fly.io does it): Go orchestrator calls Rust binaries, no FFI/CGO/WASM needed. Communicate via CLI args, stdin JSON, Unix socket, or vsock.

- **Keep in Go:** API server, orchestrator, CLI, anything needing fast iteration
- **Use Rust for:** VM init (startup time, binary size), data plane proxies (no GC pauses), CPU-intensive compute
- **Build system:** `cargo zigbuild` for cross-compilation, Docker multi-stage builds, independent dep trees
- **Validation:** Fly.io (Go `flyd` + Rust init + Rust proxy), Linkerd (Go control plane + Rust proxy at 1/9th memory), Discord (Go→Rust for latency-sensitive service)

### Init / Process Supervision

| Project | Stars | What | Status |
|---------|-------|------|--------|
| [superfly/init-snapshot](https://github.com/superfly/init-snapshot) | 297 | Fly.io's production Firecracker init. Signal forwarding, zombie reaping, vsock API, OOM detection. | Production (Fly.io fleet) |
| [Horust](https://github.com/FedericoPonzi/Horust) | 264 | Full supervisor/init with service dependencies, health checks, restart policies. Like systemd-lite in Rust. | Beta, active (Feb 2026) |
| [fpco/pid1-rs](https://github.com/fpco/pid1-rs) | 40 | Minimal PID 1: zombie reaping + signal forwarding. 650KB static binary. Drop-in tini replacement. | Production |
| [rinit](https://github.com/rinit-org/rinit) | 66 | s6-inspired supervision tree in Rust. | Early stage (Dec 2025) |

**Recommendation:** For a full init rewrite, study `init-snapshot` for architecture, use `Horust` if you need multi-service supervision, or `pid1-rs` for minimal PID 1.

### Networking

| Project | Stars | What | Status |
|---------|-------|------|--------|
| [rtnetlink](https://github.com/rust-netlink/rtnetlink) | 158 | Async netlink for managing interfaces, IPs, routes. Replaces `ip` commands. | Production (Feb 2026) |
| [tokio-vsock](https://github.com/rust-vsock/tokio-vsock) | 52 | Async vsock for host↔VM communication without network. | Production (Feb 2026) |
| [vsock-rs](https://github.com/rust-vsock/vsock-rs) | 37 | Sync vsock (VsockListener/VsockStream like TCP). | Production (Feb 2026) |
| [rust-tun](https://github.com/meh/rust-tun) | 642 | Cross-platform TUN device management. | Production (Feb 2026) |
| [rust-iptables](https://github.com/yaa110/rust-iptables) | 97 | iptables management from Rust. | Stable (Aug 2025) |
| [rustables (nftnl-rs)](https://github.com/mullvad/nftnl-rs) | 95 | nftables bindings by Mullvad VPN. Modern iptables replacement. | Production (Jan 2026) |

### Firecracker-Specific

| Project | Stars | What | Status |
|---------|-------|------|--------|
| [buildfs](https://github.com/rust-firecracker/buildfs) | 29 | Declarative TOML-based rootfs builder for Firecracker. "Dockerfile for rootfs." | Early but functional |
| [firepilot](https://github.com/rik-org/firepilot) | 46 | Rust SDK for Firecracker HTTP API. | Early (Oct 2025) |
| [firec](https://github.com/blockjoy/firec) | 75 | Rust client library for Firecracker by BlockJoy. | Production (Mar 2024) |
| [versionize](https://github.com/firecracker-microvm/versionize) | 61 | Firecracker's snapshot serialization format. | Production (Firecracker core) |

### Security

| Project | Stars | What | Status |
|---------|-------|------|--------|
| [seccompiler](https://github.com/rust-vmm/seccompiler) | 105 | Firecracker's own seccomp-bpf filter generator. | Production (AWS scale) |
| [rust-landlock](https://github.com/landlock-lsm/rust-landlock) | 193 | Filesystem access control without root. Defense-in-depth. | Production (Nov 2025) |
| [caps-rs](https://github.com/lucab/caps-rs) | 93 | Linux capabilities management. Drop privileges after setup. Pure Rust, no C deps. | Production (Oct 2025) |

### Terminal / Developer Tools

| Project | Stars | What | Why it matters |
|---------|-------|------|---------------|
| [sshx](https://github.com/ekzhang/sshx) | 7.3k | Collaborative web terminal. E2E encrypted, infinite canvas, predictive echo (Mosh-like). | Almost exactly what we build — study this architecture closely |
| [Zellij](https://github.com/zellij-org/zellij) | 24k | Rust terminal multiplexer with WASM plugins. | Modern tmux replacement for VMs. Solves terminal-survives-reconnect |
| [asciinema 3.0](https://github.com/asciinema/asciinema) | 16k | Terminal recording + live streaming. Rewritten Python→Rust. | Session recording for demos/audit |
| [russh](https://github.com/Eugeny/russh) | — | Pure Rust async SSH client/server. | Embed SSH in agent without OpenSSH |
| [portable-pty](https://crates.io/crates/portable-pty) | (WezTerm) | Cross-platform PTY abstraction. | Foundation for Rust terminal backend |

### Proxies / Tunneling

| Project | Stars | What | Why it matters |
|---------|-------|------|---------------|
| [Pingora](https://github.com/cloudflare/pingora) | 26k | Cloudflare's proxy framework. 40M+ req/s, 70% less CPU than NGINX. | Gold standard if authproxy needs scale |
| [rathole](https://github.com/rathole-org/rathole) | 690 | NAT traversal proxy. Lightweight frp/ngrok alternative. | Cloudflare Tunnel fallback option |
| [bore](https://github.com/ekzhang/bore) | 10.7k | Ultra-simple TCP tunnel (~400 lines). | Dev/debug tool for VM port forwarding |

### Observability

| Project | Stars | What | Why it matters |
|---------|-------|------|---------------|
| [Vector](https://github.com/vectordotdev/vector) | 21k | Log/metrics pipeline by Datadog. 10x faster than Fluentd. | Ship VM logs to central stack |
| [OpenObserve](https://github.com/openobserve/openobserve) | 18k | Full observability platform. 140x less storage than Elasticsearch. Single binary. | Replace entire observability backend |
| [tokio-console](https://github.com/tokio-rs/console) | 9.5k | Async Rust debugger. Real-time task/resource inspection. | Must-have for any async Rust work |

### VM Runtime / Upgrade Path

| Project | Stars | What | When to consider |
|---------|-------|------|-----------------|
| [Cloud Hypervisor](https://github.com/cloud-hypervisor/cloud-hypervisor) | 5.3k | Rust VMM with GPU passthrough, hotplug, Windows. Same rust-vmm ecosystem. | Need GPU for AI workspaces |
| [libkrun](https://github.com/containers/libkrun) | 1.7k | Lightweight process-level VM isolation. | Quick-start scenarios, lighter than full Firecracker |
| [Kata Containers runtime-rs](https://github.com/kata-containers/kata-containers) | 7.4k | Rust VM runtime with built-in Dragonball VMM. | Reference for Rust VM agent architecture |
| [Nydus](https://github.com/dragonflyoss/nydus) | — | Lazy-loading container images. Boot before full download. | Slow rootfs distribution at scale |
| [rust-vmm](https://github.com/rust-vmm) | — | Shared VMM building blocks (KVM, virtio, memory). Used by Firecracker + Cloud Hypervisor. | Building custom VMM components |

### Foundational Crates

| Crate | What | When to use |
|-------|------|-------------|
| `tokio` | Async runtime | Any async Rust code |
| `axum` | HTTP framework (22k stars) | Any Rust HTTP service |
| `rustls` | TLS without OpenSSL | Always (static linking) |
| `nix` | Unix syscall wrappers | Any systems programming |
| `tracing` | Structured logging + distributed tracing | All production Rust code |
| `serde` + `serde_json` | Serialization | All Rust data handling |
| `sys-mount` | Filesystem mount/umount | Init/rootfs tools |

---

## References

- [Ravel](https://github.com/valyentdev/ravel) — Go Firecracker orchestrator (Apache-2.0)
- [firecracker-go-sdk](https://github.com/firecracker-microvm/firecracker-go-sdk) — Official SDK
- [superfly/init-snapshot](https://github.com/superfly/init-snapshot) — Fly.io's Rust init (public snapshot)
- [Hocus](https://github.com/hocus-dev/hocus) — Archived workspace platform (MIT)
- [cloudflare-go](https://github.com/cloudflare/cloudflare-go) — Official CF SDK (verbose but complete)
- [Hocus: Why we replaced Firecracker with QEMU](https://hocus.dev/blog/qemu-vs-firecracker/)
- [Fly.io Stack](https://fly.io/docs/hiring/stack/) — Go+Rust architecture reference
- [Arcjet: Rust FFI from Go](https://blog.arcjet.com/calling-rust-ffi-libraries-from-go/) — FFI practical experience
- [Arcjet: WASM in Production with Go+wazero](https://blog.arcjet.com/lessons-from-running-webassembly-in-production-with-go-wazero/)
- [Discord: Why we switched from Go to Rust](https://discord.com/blog/why-discord-is-switching-from-go-to-rust)
- [Linkerd: Rust proxy in cloud-native](https://www.infoq.com/news/2021/08/linkerd-rust-cloud-native/)
- [Cloudflare: Pingora replacing NGINX](https://blog.cloudflare.com/20-percent-internet-upgrade/)
- [cargo-zigbuild](https://github.com/rust-cross/cargo-zigbuild) — Cross-compile Rust with Zig
- [purego](https://github.com/ebitengine/purego) — Call C/Rust from Go without CGO
