# Browser VM DNAT cleanup — fix plan

> **Revision note (2026-04-20):** Rewritten after a Codex critique caught
> the install-site attribution (it's `bridge_linux.go`, not
> `firecracker_linux.go`), a missing `filter/FORWARD` accept rule that
> must be cleaned up in parallel with the DNAT rule, an unsafe one-time
> cleanup approach, soft-failure in the current DNAT install path, and
> missing `iptables -w` locking. All seven findings folded in below.

## Problem

The host's iptables state accumulates DNAT rules across browser VM create/destroy cycles instead of cleaning them up. Overlapping and stale rules route WebRTC media UDP packets (the live browser preview stream) to the wrong VM — or to VMs that no longer exist — silently breaking the browser view.

CDP is unaffected because it rides on the bridge CDP proxy (port 9222), not through the host-edge UDP DNAT. That's why the visible symptom is "agent can still control and take screenshots, but the web-browser preview is blank / disconnect timeout."

**Observed on OVH host `ns1028714` on 2026-04-20:**

```
# iptables -t nat -L PREROUTING -n --line-numbers | grep 192.168.100
2  DNAT   udp dpts:56100:56199  to:192.168.100.3     ← live VM's slice (valid)
3  DNAT   udp dpts:56000:56099  to:192.168.100.2     ← stale; preempts rule 6
6  DNAT   udp dpts:56000:56099  to:192.168.100.3     ← shadowed by rule 3
7  DNAT   udp dpts:56000:56100  to:192.168.100.2     ← stale, wrong-shaped (end=100 not 99)
```

`iptables` evaluates PREROUTING in order with first-match-wins on `nat`, so every UDP packet in 56000-56099 hits rule 3 and gets routed to `192.168.100.2` (a destroyed VM). The user's live VM at `192.168.100.3` never sees its own WebRTC media.

An identical but parallel problem exists in **`filter/FORWARD`**: `AllowUDPPortRangeDNAT` also installs a matching `ACCEPT` rule so the DNATed UDP packets can cross from the primary interface to the bridge. Every accumulated PREROUTING DNAT likely has an accumulated FORWARD ACCEPT alongside it. A cleanup that only sweeps `nat/PREROUTING` leaves the `filter/FORWARD` sediment untouched.

User symptom: Neko HTTP shell loads and renders fine, WebSocket signalling completes, peer connection times out with "disconnect timeout", no console errors visible, no CSP/fetch failures. Agent `bh` / CDP operations keep working throughout.

## Root cause

Four independent defects compound:

1. **Removal is exact-match by rule spec.** `RemoveUDPPortRangeDNAT` at `backend/internal/network/bridge_linux.go:237` does `iptables -D` with the exact spec it expects to have installed. If the previous-version agent installed a differently-shaped rule (e.g. rule 7: `56000:56100` vs. today's `56000:56099`), the delete silently no-ops. Silently — `run()` is called as `_ = run(...)` with the return value discarded.

2. **Install is idempotent only within its own shape.** `AllowUDPPortRangeDNAT` at `bridge_linux.go:189` does `-C` (check) then `-I` (insert). "Check" means exact-match too, so differently-shaped sediment doesn't prevent a new insert — it just adds alongside. That's how rules 3, 6, and 7 coexist targeting overlapping port ranges.

3. **Soft-fail on DNAT programming failure.** In `backend/internal/orchestrator/firecracker_linux.go:2394` the call to `AllowUDPPortRangeDNAT` has its error logged as `Warn` and swallowed. VM create still succeeds with "CDP works, live preview broken" — the exact pattern of today's bug.

4. **No reconcile pass on agent startup** to garbage-collect orphan rules left by crashed teardowns, host reboots, agent version upgrades that changed rule shape, or `loadBrowserState` replays where the underlying VM set changed.

## Fix plan

### Step 1 — Per-VM iptables chains (atomic install + teardown, both tables)

Own a dedicated chain per browser VM in **each table we write to**: `nat/OCM-BVM-<uuid>` for the DNAT, and `filter/OCM-BVM-<uuid>` for the FORWARD ACCEPT. Chain names are table-scoped, so reusing the name across tables is safe. Jump the appropriate parent chain into ours.

**Install (replaces `AllowUDPPortRangeDNAT`):**
```sh
CHAIN="OCM-BVM-${uuid}"

# nat/OCM-BVM-<uuid> → DNAT
iptables -w -t nat -N "$CHAIN"
iptables -w -t nat -A "$CHAIN" -p udp --dport "${port_base}:${port_end}" \
  -j DNAT --to-destination "${vm_ip}"
iptables -w -t nat -A PREROUTING -j "$CHAIN"

# filter/OCM-BVM-<uuid> → FORWARD ACCEPT
iptables -w -N "$CHAIN"
iptables -w -A "$CHAIN" -i "${primary_iface}" -o "${bridge_name}" \
  -p udp --dport "${port_base}:${port_end}" -j ACCEPT
iptables -w -I FORWARD -j "$CHAIN"
```

**Teardown (replaces `RemoveUDPPortRangeDNAT`):**
```sh
CHAIN="OCM-BVM-${uuid}"

# filter first (reverse order of install)
iptables -w -D FORWARD -j "$CHAIN" || true
iptables -w -F "$CHAIN"             || true
iptables -w -X "$CHAIN"             || true

# nat
iptables -w -t nat -D PREROUTING -j "$CHAIN" || true
iptables -w -t nat -F "$CHAIN"                || true
iptables -w -t nat -X "$CHAIN"                || true
```

**Six commands per destroy, all idempotent; together they remove all trace of this VM's rules.** No line-number arithmetic against a shared chain; no risk of deleting another VM's entries (chain names disjoint by UUID); no sensitivity to rule-shape drift across agent versions (owner-by-chain-name, not owner-by-rule-spec).

`-w` (wait for xtables-lock) is mandatory on every command. The existing helper uses separate `-C` then `-I` without `-w` and is racy under concurrent mutation; adding more chain operations in flight makes that worse.

**Why chains over alternatives:**
- **vs. rule comments (`-m comment --comment ocm-bvm=<uuid>`):** comments identify ownership but iptables has no "delete all rules matching comment" verb — still need to parse-and-match on delete, still shape-sensitive.
- **vs. `iptables-save | sed | iptables-restore` rewriter:** too intrusive for a host whose `nat` table isn't exclusively ours; would conflict with other daemons.
- **Chains are iptables' first-class primitive for exactly this use case.** The `OCM-BVM-` prefix self-identifies orphans for the reconcile sweep.

### Step 2 — Fail hard on DNAT programming failure

At the call site (`firecracker_linux.go:2394`), stop swallowing the error. If chain install fails, `CreateBrowserVM` must return an error and the VM must not be marked `running`. The current silent-degradation behavior IS the bug class we're trying to eliminate — a green VM with a dead live view is worse than a red VM with a clear error.

```go
// was:
if err := o.bridge.AllowUDPPortRangeDNAT(...); err != nil {
    slog.Warn("standalone_browser_vm.dnat.failed", ..., "error", err)
}

// becomes:
if err := o.bridge.InstallBrowserDNATChain(cfg.BrowserVMID, cfg.VMIP, webrtcBase, webrtcEnd); err != nil {
    return fmt.Errorf("install browser DNAT chain: %w", err)
}
```

If the agent is missing `CAP_NET_ADMIN` or can't exec `iptables`, today this is a warning log and a broken VM. Under the new behavior, create fails loudly, operator sees it, it gets fixed. Existing deployed hosts already have sufficient privileges (the current DNAT install already works when it works); the hard-fail only surfaces genuinely broken environments.

### Step 3 — Reconcile pass piggybacking on existing browser-VM recovery

`firecracker_linux.go:1453` already runs browser-VM recovery (`loadBrowserState` etc.) during orchestrator init **before requests are served**. That's the correct hook point: no extra lock needed, because no concurrent `CreateBrowserVM` can be in flight yet. Extending this path (rather than adding a new reconcile goroutine later) avoids a TOCTOU where reconcile drops a chain that `CreateBrowserVM` is about to rely on.

```go
// in loadBrowserState, after live VMs are known but before serving:
live := map[string]bool{}
for _, bvm := range o.browserVMs { live[bvm.ID] = true }
if err := o.bridge.ReconcileBrowserChains(live); err != nil {
    slog.Error("browser_vm.dnat.reconcile.failed", "error", err)
    // best-effort; don't block startup, but surface loudly
}
```

**`ReconcileBrowserChains` sweeps both tables and catches legacy rules:**

1. **Orphan chains.** List `nat` chains matching `OCM-BVM-*`; drop any whose UUID isn't in `live`. Same for `filter`.
2. **Legacy raw rules (pre-chain era).** Any DNAT in `nat/PREROUTING` targeting an IP in the bridge subnet (`192.168.100.0/24`) with *no wrapping `OCM-BVM-*` chain* must be legacy sediment by construction — the new code puts all DNAT inside chains. Delete those, plus their matching `filter/FORWARD` ACCEPTs by port range.

The legacy-rule sweep is **chain-marker-based, not live-IP-based.** "Rule not inside an OCM chain" is a definite signal of legacy sediment. "Rule targets an IP that isn't currently live" is not — it races with in-flight creates. Using the chain-absence marker avoids the race.

### Step 4 — One-time cleanup for already-deployed hosts

Already-deployed hosts (like `ns1028714`) have pre-chain sediment. Two paths:

- **Preferred: Step 3's legacy-rule sweep.** On first boot of the new agent, the reconcile in `loadBrowserState` automatically drops orphan raw rules. No human intervention, no race window (runs before request serving). This is the main cleanup mechanism and handles every already-deployed host automatically.

- **Backup: `scripts/cleanup-stale-dnat.sh`** for operators who need to clean before the agent upgrade lands (e.g., preview broken NOW, can't wait). The script must run during a **maintenance window with browser VM creates blocked.** Without it, the sweep can race with an in-flight `CreateBrowserVM`. Gate via a flag file — `touch /var/lib/ocm/agent/disable-browser-vm-create` before, remove after — or via an admin endpoint that pauses browser-VM creates for the sweep's duration.

The script itself is NOT the live-IP-probing shape originally proposed ("delete every rule whose destination isn't currently live") — that races with creates. Instead it uses the same chain-marker logic as Step 3:

```sh
# scripts/cleanup-stale-dnat.sh — maintenance window only
# Deletes raw DNAT rules in nat/PREROUTING targeting 192.168.100.0/24 that
# aren't wrapped in an OCM-BVM-<uuid> chain, and the corresponding FORWARD
# ACCEPTs in filter. Idempotent, but operator MUST block creates first.
```

## Verification

### Integration tests (regression guards)

Without these the rot returns silently — the failure mode only surfaces after multiple create/destroy cycles.

```go
func TestBrowserVMDNATCleanup(t *testing.T) {
    bvm := createBrowserVM(t, ...)
    destroyBrowserVM(t, bvm.ID)

    // nat: no OCM-BVM-<uuid> chain, no raw DNAT for the VM's IP
    natOut := exec.Command("iptables", "-t", "nat", "-S").Output()
    require.NotContains(t, natOut, "OCM-BVM-"+bvm.ID)
    require.NotContains(t, natOut, bvm.VMIP)

    // filter: same assertions
    filterOut := exec.Command("iptables", "-S").Output()
    require.NotContains(t, filterOut, "OCM-BVM-"+bvm.ID)
    require.NotContains(t, filterOut, bvm.VMIP)
}

func TestBrowserVMDNATHardFailsOnIptablesError(t *testing.T) {
    // Mock bridge whose InstallBrowserDNATChain returns err.
    // Assert CreateBrowserVM returns err and doesn't mark the VM running.
}

func TestBrowserVMDNATReconcileSweepsLegacyRules(t *testing.T) {
    // Pre-seed nat/PREROUTING with a raw DNAT → 192.168.100.5:56000-56099
    // Bring orchestrator up with no VM at .5.
    // Assert the legacy rule is gone after loadBrowserState.
}

func TestBrowserVMDNATConcurrentDestroy(t *testing.T) {
    // Create two VMs, destroy both concurrently; verify both chains gone
    // and no cross-contamination (each destroy only touches its own chain).
}
```

### Manual smoke

On a clean host:
1. Create three browser VMs back-to-back → expect three `OCM-BVM-<uuid>` chains in each of `nat` and `filter`, plus three jumps from `PREROUTING` and three from `FORWARD`.
2. Destroy the middle one → expect two chains in each table, two jumps; live VMs still reachable via WebRTC.
3. `kill -9` the agent mid-destroy of another; restart. Startup reconcile drops the orphan chain in both tables.
4. After every permutation: `iptables -t nat -S | grep OCM-BVM-` has exactly one entry per live VM; same in filter.

On a sediment-bearing host (e.g. `ns1028714` post-fix):

5. Before upgrade: rules 2/3/6/7 as observed.
6. After agent upgrade + restart: reconcile sweep drops rules 3/6/7 (legacy, no owning chain). Rule 2 gets replaced by a new chain-wrapped equivalent on next VM create.

## Out of scope

- **Port-slice allocator robustness.** Chains isolate per-VM state so allocator bugs become less catastrophic; revisit if 500+ concurrent VMs per host becomes real.
- **Move from iptables to nftables.** Long-term direction, separate spec.
- **Per-rule comments.** Obviated — chain name is the owner ID.
- **Reconcile of non-DNAT browser networking state** (tap devices, bridge firewall rules for inter-VM pairing). Separate lifecycles; confirm those are clean or file separately.

## Routing

- **Label:** `rootfs-build` — changes agent code; needs new agent binary + host deploy via admin "Update host" button.
- **Branch target:** `main`. Independent of the in-flight `browserprofile` / cookies.md work.
- **Deploy caveat:** per CLAUDE.md, the agent update restarts the systemd unit on each host, which kills running VMs. Schedule accordingly.
- **Rollout note:** first agent-upgrade on an already-sedimented host both installs the new chain mechanism AND sweeps legacy rules in the same startup pass. No separate operator step required for the common case; the standalone cleanup script is a backup for "fix me now without a full agent deploy."

## References

- Architecture: `docs/superpowers/specs/2026-04-10-browser-vm-network-architecture.md` → Data Flow 3 (WebRTC Media Stream), NAT1TO1 + DNAT sections.
- Actual install site: `backend/internal/network/bridge_linux.go:189` (`AllowUDPPortRangeDNAT`) — writes PREROUTING DNAT at `:197` and the matching FORWARD ACCEPT around `:205`.
- Actual removal site: `backend/internal/network/bridge_linux.go:237` (`RemoveUDPPortRangeDNAT`) — exact-match `-D`, errors discarded via `_ = run(...)`.
- Call site with soft-fail: `backend/internal/orchestrator/firecracker_linux.go:2394` — Warn-and-continue on DNAT failure.
- Reconcile hook point: `backend/internal/orchestrator/firecracker_linux.go:1453` (`loadBrowserState` / browser-VM recovery; runs at orchestrator init, before serving).
- Discovery: live iptables triage on OVH host `ns1028714`, 2026-04-20.
- Codex critique (2026-04-20 session): identified the correct install site, the missing FORWARD cleanup, the `iptables -w` gap, the one-time-script race, and the soft-fail site.
