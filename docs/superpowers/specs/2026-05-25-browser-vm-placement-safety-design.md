# Browser VM Placement Safety and Recovery Design

**Date:** 2026-05-25
**Branch:** `browservm_upgrade`
**Status:** Implementation in progress; stable default is legacy Alpine, Kernel Images browser rootfs is explicit test-only selection

## Summary

Browser VM provisioning currently has too many ambiguous outcomes. The most
dangerous one is: the backend times out while the host agent may already have
started a browser Firecracker VM. If the backend then releases the browser VM
placement, the same bridge IP can be allocated to another browser VM while the
first one is still alive on the host.

The design goal is to simplify the lifecycle so the control plane never reuses
a host bridge IP until it has explicit evidence that the agent-side resource is
gone.

Separately, the current new Kernel Images browser rootfs must not be treated as
the stable browser runtime yet. There are multiple browser rootfs artifacts:

- legacy Alpine `browser-rootfs` (`3f404bb-20260409T205552Z`), CDP only.
- known-good Kernel Images `kernel-browser-rootfs`
  (`9a80fb2-20260411T202541Z`), with Neko/WebRTC live view.
- new upstream-synced Kernel Images builds such as
  `b0ec113-20260525T005324Z` / later canaries, which need more rollout
  evidence before being considered stable.

When this document says "Kernel Images is experimental," it means the new
upstream-synced Kernel Images build, not the old known-good
`9a80fb2-20260411T202541Z` rollback build.

## Facts Observed

These are observations from production logs or current code, not guesses.

1. Host 105 is reachable:
   - `GET /health` returns `status=ok`
   - `GET /ready` returns `status=ready`
   - heartbeats arrive every minute
   - it reports agent version `2c0227b-20260525T013626Z`

2. Host 105 is not safe for new browser work:
   - browser VM starts against `15.204.104.201:9090/browser-vms` timed out
     after the backend's 4 minute HTTP client deadline.
   - host 105 was put into maintenance at `2026-05-25T05:04:47Z`.

3. The host logs showed multiple browser VMs on host 105 using the same VM IP:
   - affected VMs were created with `192.168.100.2`.
   - duplicate browser VM IPs imply duplicate generated MAC addresses, because
     Firecracker network config derives the MAC from the VM IP.

4. Agent logs showed `standalone_browser_vm.started`, but no later
   `standalone_browser_vm.cdp.ready` or `standalone_browser_vm.cdp.not_ready`
   for the affected VM.

5. Host inspection showed many browser Firecracker processes:
   - the expected long-running browser VMs were still present.
   - several newer browser Firecracker processes looked orphaned.
   - this creates real resource pressure: browser VMs are large relative to
     normal OCM VMs.

6. Current backend browser start flow releases placement on agent create error:
   - `handleStartBrowserVM` reserves placement and assigns `host_id/vm_ip`.
   - it calls `agentClient.CreateBrowserVM`.
   - if that call returns error, it sets status `error`, releases the browser
     placement, and unassigns `host_id/vm_ip`.

7. Current browser placement IPAM excludes only DB-visible allocations:
   - active `machine_placements`
   - active `browser_vm_placements`
   - `machines.host_id/vm_ip`

8. The host agent has an in-memory and persisted browser VM map, but the backend
   allocator does not check live agent state before reusing an IP.

9. `waitForPort` is not context-aware in the current agent code:
   - it accepts only `ip`, `port`, and `timeout`.
   - it does not check `ctx.Done()`.
   - it polls with `net.DialTimeout` until its local deadline.

10. `CreateBrowserVM` registers and persists a browser VM before CDP readiness:
    - it starts Firecracker.
    - it logs `standalone_browser_vm.started`.
    - it inserts the VM into `o.browserVMs`.
    - it calls `saveBrowserState`.
    - only then does it wait for `192.168.100.x:9222`.

11. The new Kernel Images browser rootfs is materially heavier than the older
    Alpine browser rootfs:
    - older `browser-rootfs`: Alpine + headless Chromium + CDP.
    - new `kernel-browser-rootfs`: Ubuntu/Kernel Images + headed Chromium +
      Neko/WebRTC live view + Xvfb/display + supervisor/wrapper + CDP proxy.
    - default browser VM creation should reserve at least `2 vCPU / 4096 MB`
      for this lineage, and any existing `1024 MB` browser rows should be
      treated as suspect for slow or failed CDP readiness.

12. A later provisioning incident produced a concrete late-readiness orphan:
    - browser VM `0fd740b3-3f40-405f-a4dc-f7e669f3fbe2` was marked `error`
      with no backend `host_id`.
    - a matching browser Firecracker process was still running on host 105.
    - `curl http://192.168.100.2:9222/json/version` returned Chrome
      `148.0.7778.97`, proving CDP eventually became reachable after the
      control plane had already treated the create as failed.
    - this confirms that at least some Kernel Images browser starts are slow
      but ultimately healthy, and timeout-based failure handling can create
      live orphans.

13. Local profiling showed the Kernel Images rootfs copy cost is dominated by
    host storage behavior:
    - current host storage for `/var/lib/ocm` is ext4, so
      `cp --reflink=auto` silently falls back to a full copy.
    - the Kernel Images rootfs is several GiB uncompressed, so full copy can
      add tens of seconds before Firecracker even starts.
    - this is not primarily a Chromium boot regression on an uncongested host;
      it is artifact size plus hidden full-copy cost, with memory pressure and
      orphan accumulation making late readiness more likely in production.

## Assumptions

These are assumptions that should be verified, but they are good enough to
shape the fix.

1. A backend timeout after sending `POST /browser-vms` does not prove the agent
   failed. The agent may still have started Firecracker.

2. The missing `cdp.ready` / `cdp.not_ready` log means the agent did not reach
   the observable CDP-ready or CDP-timeout branches for the affected creates.
   Candidate explanations are:
   - the goroutine is blocked before `waitForPort` returns.
   - `waitForPort` is still polling an address made unreliable by duplicate
     TAPs/MACs/IPs.
   - cleanup/logging failed or was bypassed after request cancellation.
   - logs were lost or filtered, although the repeated startup logs make this
     less likely.

3. Duplicate `192.168.100.2` happened because a previous timed-out browser
   start released the DB placement while the agent-side VM remained alive.

4. Duplicate `192.168.100.2` can make bridge behavior undefined enough that
   readiness probes and browser traffic are nondeterministic. Multiple TAPs
   with the same IP-derived MAC are worse than a simple stale DB row.

5. Host 105 can answer simple health checks while still being a poor target for
   browser provisioning. Health means "agent process alive," not "safe to boot a
   new browser VM."

6. Memory pressure can explain slow or failed CDP readiness, especially for the
   Kernel Images lineage or when orphaned browser Firecracker processes have
   accumulated. It does not explain duplicate bridge IP reuse; that remains a
   placement/lifecycle safety bug.

7. Extending the CDP wait timeout may reduce false failures, but it does not
   fix the safety problem. A browser create can still outlive a backend request
   or client timeout, so the lifecycle must treat post-dispatch timeout as
   unknown outcome and reconcile agent state explicitly.

## Refined Failure Analysis

This section captures the current working theory from host 105 logs and code
inspection. It should drive implementation, but the "unresolved" items still
need confirmation during host cleanup.

### Confirmed Sequence

1. Backend sends `POST /browser-vms` to the host agent.

2. Agent stages the browser rootfs and starts Firecracker.

3. Agent logs `standalone_browser_vm.started` with the requested VM IP.

4. Agent registers the browser VM in `o.browserVMs` and persists
   `browser-vms.json` before CDP readiness.

5. Backend waits up to 4 minutes for the agent HTTP request.

6. Backend times out and treats the create as failed.

7. Backend releases the browser placement and clears `host_id/vm_ip`.

8. A later browser start sees the IP as free in the DB and can allocate the
   same bridge IP again.

9. Host 105 ended up with multiple browser Firecracker processes using
   `192.168.100.2`.

10. A later orphan check found browser VM
    `0fd740b3-3f40-405f-a4dc-f7e669f3fbe2` still running on host 105 even
    though the backend had moved it to `error` and cleared host assignment.

11. CDP on that orphan was reachable and reported Chrome `148.0.7778.97`.
    Therefore the Kernel Images guest can cross from "not ready before timeout"
    to "usable browser" after the backend has already lost control-plane
    ownership.

### Inferred Network Effect

Duplicate browser VMs with the same `192.168.100.2` also share the same
IP-derived MAC address. On a Linux bridge, multiple TAPs presenting the same
source MAC/IP can cause forwarding table churn and nondeterministic packet
delivery. A CDP readiness probe to `192.168.100.2:9222` may hit the wrong guest,
no guest, or alternate between guests.

This explains why later creates can make earlier readiness waits worse, and why
an otherwise booted Chromium process may still not produce a reliable
`cdp.ready` signal through the host bridge.

### Updated Readiness Finding

`waitForPort` has a local 3 minute timeout. The later orphan inspection showed
CDP reachable after the backend had already marked the browser VM failed, so
the Kernel Images browser rootfs can exceed the current readiness window while
still eventually booting Chromium successfully.

That means the new rootfs is not simply "broken"; it is too slow for the
current synchronous create contract. The dangerous bug is the contract around
that slowness:

- Firecracker can be running.
- CDP can become healthy later.
- the backend can already have released or cleared ownership.
- a retry can reuse the same IP.

Increasing the 3 minute CDP wait to 5 minutes is acceptable only as a temporary
mitigation for false failures. It must not be the stability plan, because any
fixed foreground timeout can still be exceeded under host pressure, cold cache,
or a future heavier browser image.

### Remaining Unresolved Details

The later orphan proves late readiness is real, but some details still need
confirmation on the host:

Possible explanations to verify on the host:

- the goroutine is blocked before entering `waitForPort`, likely in DNAT install
  or another synchronous step.
- `waitForPort` is executing but repeated duplicate-IP/MAC behavior interacts
  badly with the dial loop and cleanup path.
- cleanup reached `stopOwnedVM` or resource removal and blocked before logging
  enough context.
- the relevant ready/not-ready logs were missed by the filter.

The design does not require this to be fully solved before the first safety
patch. Preventing duplicate IP reuse is still required either way.

## Unknowns

These should be investigated, but the primary safety fix should not depend on
knowing them.

1. Whether the agent goroutine is blocked in `iptables -w`, Firecracker start,
   CDP wait, or cleanup after the backend times out.

2. Whether the duplicate-IP browser VMs are both still alive at the Firecracker
   process level, or whether one is only preserved in agent state.

3. Whether host 105 has stale TAP devices, stale browser rootfs files, stale
   socket files, or stale DNAT chains that recovery did not reconcile.

4. Why `waitForPort` did not produce a visible ready/not-ready log after its
   expected 3 minute local timeout.

5. Whether the browser rootfs itself is slow but healthy, or whether duplicate
   IP / stale network state is the reason CDP never became reachable.

## Current Invariant Violations

### Violation 1: Unknown agent outcome frees IP

When the backend times out waiting for `CreateBrowserVM`, it treats the request
as a failed create and releases placement. That is unsafe because the agent
request is synchronous and performs side effects before returning:

1. stage rootfs
2. create TAP
3. start Firecracker
4. add browser VM to agent state
5. install DNAT
6. wait for CDP

If the backend times out during steps 3-6, releasing placement can make the IP
available while the VM is live.

### Violation 2: Placement uses DB state only

The DB allocator cannot see an orphan browser VM on the host if its placement
was released. Therefore a DB-only allocator can reuse an IP that is still in use
on the host bridge.

### Violation 2b: Agent accepts duplicate requested browser IPs

The agent trusts the backend-provided `vm_ip`. If backend placement state is
stale, the agent can create another browser VM on an IP already owned by a live
or booting browser VM. The agent has the best local view of live browser VMs and
must reject duplicate requested IPs as a second line of defense.

### Violation 3: Machine and browser host policy differ

Browser targeted placement rejects maintenance-mode hosts. Machine affinity
restart can still reuse a host with existing affinity unless the host is in one
of a narrower set of bad statuses. This let a machine restart onto host 105
while browser placement refused host 105.

### Violation 4: Browser VM is persisted as running before readiness

Agent-side browser create records the VM as `running` and persists it before
CDP readiness. That is useful for recovery, but it also makes an incomplete
create look like a usable browser VM. There is no durable distinction between:

- Firecracker started, CDP not ready yet
- CDP ready and browser usable
- create timed out and cleanup is required

## Design Principles

1. Unknown outcome is not failure.
   If the backend sent a side-effecting request to an agent and then timed out,
   the outcome is unknown. Do not release placement.

2. IP reservations are durable until confirmed destroyed.
   A bridge IP can be reused only after the agent confirms the VM is gone, or an
   operator/reconciler proves no live host resource owns it.

3. Host eligibility must be consistent.
   Machine placement, browser placement, targeted placement, and affinity
   restart should all use the same health gates:
   - `status = ready`
   - `maintenance_mode = false`
   - fresh heartbeat
   - enough capacity

4. OCM and browser must be colocated.
   A paired browser VM must run on the same host as its machine VM. If that host
   is bad, migrate or restart the machine on a healthy host first.

5. Keep the first fix small.
   First prevent IP reuse and bad host reuse. Then optimize browser boot and
   cleanup ergonomics.

6. Agent local truth protects against stale control-plane truth.
   The backend allocator is necessary but insufficient. The host agent must
   reject duplicate IPs and duplicate IDs based on live local state.

7. Booting is not running.
   A browser VM whose Firecracker process exists but whose CDP port is not ready
   should not be exposed as a normal running browser VM.

8. Runtime lineages must be explicit.
   The old browser rootfs is the stable rollback path. The Kernel Images rootfs
   is experimental until proven by controlled starts, resource measurements, and
   cleanup/recovery tests.

9. Browser readiness should be asynchronous.
   Firecracker start and CDP readiness are separate phases. The agent can return
   after it has durably recorded a `creating` browser VM, then continue CDP
   readiness in the background and publish the result through health/heartbeat
   or a browser VM status endpoint.

10. Full-copy fallback must be explicit for new experimental browser images.
    Stable legacy browser rootfs and the known-good Kernel Images rollback
    build can tolerate fallback for compatibility. New/unpinned Kernel Images
    canaries should require reflink-capable VM storage by default. A full-copy
    escape hatch is acceptable only for named canaries.

## Proposed State Model

Keep the existing public statuses for now:

- `stopped`
- `provisioning`
- `running`
- `error`

But tighten the meaning of `error`:

- `error` with no active placement: safe to retry start normally.
- `error` with active placement and `host_id/vm_ip`: unknown agent outcome.
  Do not allocate that IP to anything else. Retry should first destroy/reconcile
  the existing agent-side resource.

A future schema can add `reconciling` or `unknown` status, but this is not
needed for the immediate safety fix.

## Proposed Backend Changes

### 0. Default browser rootfs back to stable legacy lineage

Set the stable default browser rootfs manifest to:

```text
BROWSER_ROOTFS_GCS_MANIFEST=gs://openclawmachines/browser-rootfs/manifest.json
```

Keep the Kernel Images rootfs available only as an explicit opt-in:

```text
BROWSER_ROOTFS_GCS_MANIFEST=gs://openclawmachines/kernel-browser-rootfs/manifest.json
```

Operational meaning:

- legacy `browser-rootfs` is `stable`
- `kernel-browser-rootfs` is `experimental`
- a host heartbeat or create log must identify the manifest URI, lineage, and
  staged rootfs version
- UI/API surfaces that show "latest browser rootfs" should distinguish stable
  legacy from experimental Kernel Images
- WebRTC DNAT setup should be skipped for the legacy lineage because it has CDP
  but no Neko/WebRTC live view; the stable rollback path must not fail because
  an experimental live-view-only network rule could not be installed

### 0b. Allow per-browser VM experimental image selection

Host-level `BROWSER_ROOTFS_GCS_MANIFEST` remains the default for normal starts,
but testers need to launch an experimental browser image without making the
whole host experimental. Browser VM create therefore accepts a request-level
image selection:

- `browser_image=default`: use the host agent default manifest/version.
- `browser_image=legacy`: use stable legacy
  `gs://openclawmachines/browser-rootfs/manifest.json`.
- `browser_image=kernel-stable`: use Kernel Images manifest pinned to
  `9a80fb2-20260411T202541Z`.
- `browser_image=kernel-experimental`: use Kernel Images manifest with no
  version pin, meaning the latest manifest release.

The backend persists the requested manifest/version on `browser_vms` and passes
it to the agent on start. The agent stages that request-level selection for the
specific browser VM only. This mirrors non-stable OCM VM creation: stable
remains the default, but a tester can opt a single launch into an experimental
runtime.

Browser-specific rootfs state can be isolated from normal VM state with:

```text
BROWSER_STATE_DIR=/var/lib/ocm-browser
```

When set, browser rootfs release caches, per-browser VM rootfs copies, and the
agent browser VM state file live under `BROWSER_STATE_DIR`. This lets operators
mount a dedicated XFS/reflink filesystem for new browser canaries without
mounting over `/var/lib/ocm` or moving normal OCM VM state. Reflink checks for
new/unpinned Kernel Images browser canaries must evaluate this browser state
directory, not the general `STATE_DIR`.

The preferred rollout path is through the admin control plane. A host admin
operation should enter maintenance so no new placements land, verify no active
browser VMs are present, ask the agent to mount or format the selected block
device as XFS with `reflink=1`, write `/etc/ocm-agent/browser-state.env` and
`BROWSER_STATE_DIR` into `/etc/ocm-agent/agent.env`, restart the agent, then
complete only after heartbeat reports `browser_storage.reflink_supported=true`
for `/var/lib/ocm-browser`. Active normal machine VMs do not need to be drained
when browser storage is on a separate filesystem; Firecracker children should
keep running across the agent restart. SSH/provisioning-script setup remains a
fallback for first boot or break-glass repair, not the normal canary path.

Custom `rootfs_manifest` remains restricted to known browser manifests. A
custom `rootfs_version` without a manifest targets the Kernel Images browser
manifest, because legacy is the stable rollback path and should not be used as
an unbounded experimental channel.

### 1. Classify timeout as unknown outcome

In `agentclient.CreateBrowserVM`, wrap timeout and cancellation errors with a
sentinel such as:

```go
var ErrOutcomeUnknown = errors.New("agent operation outcome unknown")
```

This applies when:

- the request was sent to the agent
- the HTTP client timed out
- the context was canceled/deadlined while waiting for response
- the network error is a timeout

It should not apply to:

- connection refused before the request reached the agent
- immediate DNS/route failure
- HTTP 4xx/5xx response from the agent

### 2. Preserve placement on unknown outcome

In `handleStartBrowserVM`, if `CreateBrowserVM` returns `ErrOutcomeUnknown`:

1. set browser VM status to `error`
2. keep `host_id`
3. keep `vm_ip`
4. keep active `browser_vm_placements` row
5. return `504 Gateway Timeout` with an "outcome unknown" message
6. log `browser_vm.start.agent_create_unknown`

Do not call:

- `ReleaseBrowserVMPlacement`
- `UnassignBrowserVMFromHost`

### 3. Release placement only after confirmed destroy

The stop/delete path already has the right broad shape: if agent destroy fails,
keep the DB record and placement. Preserve that rule. Extend tests so this is
covered for browser start timeouts too.

### 4. Reject bad hosts for machine affinity

Machine affinity restart must reject the same host conditions browser targeted
placement rejects:

- status not `ready`
- maintenance mode enabled
- no heartbeat
- stale heartbeat

If the home host fails these checks, return "migration required" instead of
starting there.

### 5. Add a conservative agent-side duplicate IP guard

Before starting a browser VM, the agent should reject a requested `vm_ip` if
any live or quarantined browser VM already owns it. This protects against DB
bugs and operator mistakes.

For a first pass, check:

- `o.browserVMs[*].VMIP`
- `o.quarantinedBrowserVMs[*].VMIP`

Optional later checks:

- scan TAPs on `ocm-br0`
- ARP/neigh table
- compare guest IP metadata if available

### 6. Make browser CDP wait context-aware

Replace `waitForPort(ip, port, timeout)` with a context-aware form for browser
creation:

```go
waitForPort(ctx, ip, port, timeout)
```

The loop should check `ctx.Done()` between dial attempts and before sleeping.
This ensures request cancellation, shutdown, or timeout can trigger cleanup
without waiting for the local timeout.

For normal machine health checks, use `context.Background()` or a fresh timeout
context so existing machine boot behavior does not accidentally inherit a
canceled request context.

### 7. Track browser create state explicitly on the agent

When Firecracker starts but CDP is not ready, store the browser VM with
`Status: "creating"` or `Status: "starting"`, not `running`.

Only transition to `ready` after the CDP port is reachable.

On CDP failure or context cancellation:

1. stop Firecracker
2. remove TAP/rootfs/socket/DNAT
3. delete the browser VM from `o.browserVMs`
4. save browser state
5. log `standalone_browser_vm.cdp.not_ready` or a distinct
   `standalone_browser_vm.create.cancelled`

If cleanup fails, leave a quarantined record rather than pretending the create
fully failed.

### 8. Add browser provisioning timing logs

Add structured logs around every slow or failure-prone step in agent-side
browser create:

- `browser_rootfs.stage.starting`: manifest URI, lineage, timeout, retry count
- `browser_rootfs.gcs.staged`: version, path, lineage, duration
- `standalone_browser_vm.create.start`: requested vCPU/memory, VM IP, lineage
- `standalone_browser_vm.tap.created`: TAP name and duration
- `standalone_browser_vm.rootfs_copied`: source, destination, duration
- `standalone_browser_vm.started`: PID, owner kind, effective vCPU/memory,
  rootfs version/lineage, Firecracker start duration, total elapsed time
- `standalone_browser_vm.dnat.installed`: port range and duration
- `standalone_browser_vm.cdp.wait_start`: timeout, lineage, elapsed time
- `standalone_browser_vm.cdp.ready`: CDP wait duration and total elapsed time
- `standalone_browser_vm.cdp.not_ready`: rootfs version/lineage, CDP wait
  duration, total elapsed time, error

These logs should make it possible to distinguish:

- rootfs download/cache latency
- rootfs copy latency
- Firecracker start latency
- iptables/DNAT blocking
- guest/browser/CDP readiness latency
- memory or duplicate-IP pathologies

### 9. Move host-agent updates to background operations

Admin host-agent update endpoints should not keep the backend HTTP request open
while waiting for the host to reboot, restart the agent, and report a fresh
heartbeat. The update trigger should return `202 Accepted` once the update work
has been accepted by the backend and expose an operation that can be polled.

Initial operation contract:

- `POST /api/admin/hosts/{hostId}/trigger-update`
  - verifies the host is currently `ready`
  - creates a `trigger_update` operation
  - starts the update trigger and host-ready wait in a background goroutine
  - returns `operation_id` and `operation_url`
- `POST /api/admin/hosts/{hostId}/drain-update`
  - verifies the host is currently `ready`
  - creates a `drain_update` operation
  - stops running machines, enables maintenance, triggers update, and waits for
    readiness in the background
  - returns `operation_id` and `operation_url`
- `GET /api/admin/host-update-operations/{operationId}`
  - returns `queued`, `running`, `succeeded`, `timed_out`, or `failed`
  - includes machine stop/restart counters and failure text

The current implementation keeps operation state in the backend process. That
is enough to stop long foreground admin waits and to make status visible during
the update. A later durability pass can move the same operation model into
Postgres if operations must survive API process restart.

## Proposed Reconciler

Add a manual or periodic browser VM reconciliation path:

1. For each browser VM in `error` with active placement:
   - ask the host agent whether the browser VM ID exists
   - if it exists and CDP is ready, mark DB `running`
   - if it exists but CDP is not ready, leave `error` or attempt destroy
   - if it does not exist, release placement and unassign host

2. For each host agent browser VM not represented by DB:
   - log as orphan
   - optionally destroy if older than a grace period

This avoids requiring humans to SSH into the host for every unknown outcome.

## Implementation Checklist

This checklist is the implementation backlog for making browser VM placement
safe and for keeping host-agent browser runtime updates observable. Items marked
complete are already represented in the current branch; unchecked items are
still required before the Kernel Images browser runtime should be treated as
stable.

### Completed in current branch

- [x] Default unset `BROWSER_ROOTFS_GCS_MANIFEST` to the stable legacy
  `gs://openclawmachines/browser-rootfs/manifest.json`.
- [x] Keep explicit Kernel Images opt-in via
  `gs://openclawmachines/kernel-browser-rootfs/manifest.json`.
- [x] Add `BROWSER_ROOTFS_VERSION` so hosts can pin the old known-good Kernel
  Images build `9a80fb2-20260411T202541Z` instead of taking the latest
  manifest.
- [x] Add per-browser VM rootfs selection in the control plane:
  `default`, `legacy`, `kernel-stable`, and `kernel-experimental`.
- [x] Persist desired browser rootfs manifest/version on `browser_vms`.
- [x] Forward per-browser VM rootfs manifest/version from backend to host agent
  create requests.
- [x] Add `BROWSER_STATE_DIR` so browser VM rootfs caches/copies and
  `browser-vms.json` can live on a dedicated XFS/reflink filesystem.
- [x] Add an authenticated agent control endpoint that configures dedicated
  browser VM storage by mounting or formatting an admin-selected XFS block
  device, writing the browser state env handoff, probing reflink, and
  scheduling an agent restart.
- [x] Add a backend admin host operation for browser storage setup that enters
  maintenance, rejects active browser VMs, allows active normal machine VMs,
  calls the agent, and waits for heartbeat to report reflink-ready browser
  storage.
- [x] Add Admin Hosts UI controls to trigger the browser storage operation from
  the control plane.
- [x] Make Kernel Images browser reflink gating probe browser VM storage rather
  than general VM `STATE_DIR`.
- [x] Expose image selection in the standalone Browser VMs page.
- [x] Expose the same image selection in the machine Browser tab launch flow.
- [x] Preserve explicit empty `BROWSER_ROOTFS_GCS_MANIFEST=""` as browser
  support disabled.
- [x] Expose stable and experimental browser rootfs manifest metadata through
  the latest-versions API.
- [x] Add startup logs on the backend server and host agent for browser rootfs
  manifest URI and lineage.
- [x] Add provisioning logs around browser rootfs staging, Firecracker start,
  DNAT setup, and CDP readiness.
- [x] Add rootfs copy-mode logging so reflink versus full-copy fallback is
  visible in agent logs.
- [x] Make new/unpinned Kernel Images browser rootfs require reflink-capable VM
  storage by default; allow full copy only with
  `OCM_ALLOW_KERNEL_BROWSER_FULL_COPY=1`.
- [x] Do not apply the strict reflink gate to known-good Kernel Images rollback
  version `9a80fb2-20260411T202541Z`.
- [x] Skip WebRTC DNAT setup for legacy browser rootfs because it does not run
  Neko/WebRTC live view.
- [x] Classify browser create HTTP timeouts as `ErrOutcomeUnknown` in the
  backend agent client.
- [x] Preserve browser placement on backend create timeout / unknown outcome.
- [x] Add config tests for stable default, experimental opt-in, and explicit
  disable behavior.
- [x] Add tests for browser image selection presets and agent payload
  forwarding.

### Backend control-plane safety

- [x] Add a focused `handleStartBrowserVM` unit test proving
  `ErrOutcomeUnknown` keeps `host_id`, `vm_ip`, and the active placement row.
- [x] Add a focused start-failure test proving definite agent failures still
  release placement and unassign host.
- [x] Fix the `error + host_id/vm_ip + active placement` lifecycle path so
  stop, delete, and retry never drop a live agent-side browser VM without a
  confirmed destroy.
- [x] Add tests for stop/delete/retry behavior when a browser VM is in `error`
  but still has an active placement.
- [x] Add a DB/reconciler query for browser VMs in `error` with active
  placement so operators can see unknown-outcome resources.
- [x] Add a manual admin reconciliation endpoint or command for a single
  browser VM.
- [x] Add a periodic reconciler that compares DB browser placements with agent
  browser state and releases placement only after confirmed absence or destroy.
- [x] Make machine affinity restart use the same host eligibility gates as
  browser placement: ready status, no maintenance mode, fresh heartbeat, and
  capacity.
- [x] Add tests proving affinity restart refuses maintenance or stale-heartbeat
  hosts.
- [x] Convert admin host update endpoints to create an update operation and
  return `202` instead of holding the request while waiting for the host.
- [x] Add in-process operation status storage and polling for host updates.
- [x] Keep foreground waiting only as a CLI/client-side polling convenience,
  not as the backend's primary update mechanism.
- [ ] Persist host update operations in Postgres if update operation status
  must survive backend process restart.
- [x] Change browser start response semantics so the backend can represent
  `creating` separately from `running`.
- [x] Add API support for polling browser VM readiness after create instead of
  requiring the initial create request to block until CDP is ready.
- [ ] Ensure backend retry logic first reconciles/destroys an existing
  unknown-outcome browser VM before attempting a fresh placement.

### Host agent update behavior

- [x] Add agent API tests for `/trigger-update` returning immediately after
  scheduling background update work.
- [x] Add agent API tests for concurrent `/trigger-update` requests returning
  `409` while an update is already in progress.
- [x] Add agent API tests proving `updateInProgress` is cleared after update
  failure.
- [x] Add agent API tests proving `updateInProgress` is cleared when no update
  is needed.
- [ ] Add a startup-path test or integration harness for host-agent config load,
  startup self-update, service registration, and browser rootfs lineage logging.
- [x] Include browser rootfs manifest URI and lineage in host heartbeat
  capabilities.
- [x] Include host VM storage filesystem, mount point, and reflink support in
  host heartbeat capabilities.
- [x] Include browser VM storage filesystem, mount point, state directory, and
  reflink support in host heartbeat capabilities.
- [x] Include cached browser rootfs version in host heartbeat capabilities,
  read from `BROWSER_STATE_DIR` when configured.
- [ ] Add backend assertions that host heartbeat lineage matches the expected
  stable or experimental rollout target.
- [ ] Add rollout tooling to update existing host-agent environment/metadata
  from Kernel Images back to stable legacy browser rootfs.
- [ ] Add host update canary tooling that updates one host, waits for heartbeat
  confirmation, then proceeds to the next host.

### Host agent browser VM safety

- [x] Reject duplicate requested browser VM IDs before doing any side effects.
- [x] Reject duplicate requested browser VM IPs already present in live browser
  state.
- [x] Reject duplicate requested browser VM IPs present in quarantined browser
  state.
- [x] Add tests for duplicate ID and duplicate IP rejection.
- [x] Make browser CDP wait context-aware so request cancellation or shutdown
  can interrupt the wait loop.
- [x] Add a unit test proving canceled CDP wait returns promptly.
- [ ] Temporarily raise the Kernel Images CDP readiness budget only if needed
  for canary diagnostics; do not rely on a longer foreground wait as the stable
  design.
- [x] Split `CreateBrowserVM` into durable start and asynchronous readiness
  phases:
  - start Firecracker
  - persist browser VM as `creating`
  - return success/accepted to the backend
  - continue CDP wait in a background task
  - transition to `ready` only after CDP responds
  - transition to `error` or `quarantined` on readiness failure
- [x] Track agent-side browser state as `creating` before CDP readiness and
  `ready` only after CDP is reachable.
- [x] Persist the intermediate `creating` state so agent restart recovery can
  distinguish booting from usable browser VMs.
- [x] Expose browser VM readiness through the agent health endpoint or a
  dedicated browser VM status endpoint so backend reconciliation does not depend
  on the original create HTTP request.
- [ ] Include CDP status, CDP last-check time, and readiness error in the agent
  browser VM status payload.
- [ ] On CDP timeout or cancellation, clean up Firecracker, TAP, rootfs, socket,
  and DNAT state before removing the browser VM from active state.
- [ ] If cleanup fails, quarantine the browser VM instead of forgetting it.
- [ ] Add tests for cleanup success and cleanup failure/quarantine behavior.
- [ ] Add a regression test for a browser VM whose CDP becomes reachable after
  the old 3 minute wait window; verify it remains owned and transitions to
  ready instead of becoming an orphan.
- [ ] Ensure legacy browser rootfs can start successfully without WebRTC/Neko
  assumptions.
- [x] Ensure Kernel Images browser rootfs requires explicit experimental
  selection in host-agent config.
- [x] Ensure new/unpinned Kernel Images browser rootfs full-copy fallback is
  blocked unless explicitly enabled with `OCM_ALLOW_KERNEL_BROWSER_FULL_COPY=1`.

### Profiling and observability

- [x] Add a browser VM cold-start profiling script that records:
  rootfs stage duration, rootfs copy duration, TAP creation duration,
  Firecracker start duration, DNAT duration, CDP wait duration, and total
  create duration.
- [x] Record rootfs size, cached vs downloaded rootfs status, rootfs version,
  manifest URI, and lineage in the profiling output.
- [x] Record rootfs copy mode in the profiling output so ext4 full-copy
  fallback is visible.
- [x] Record host memory before and after browser create: `free -m`, relevant
  cgroup memory, Firecracker RSS, and OOM/dmesg evidence.
- [x] Record CPU load and steal/wait indicators during browser create.
- [x] Add a legacy browser rootfs baseline run.
- [x] Add a Kernel Images browser rootfs canary run.
- [x] Add a late-readiness profile case based on the observed Chrome
  `148.0.7778.97` Kernel Images behavior, where CDP becomes ready after the
  current 3 minute agent wait.
- [ ] Define startup and memory thresholds for promoting Kernel Images from
  experimental to stable.
- [ ] Add a regression test or CI/manual gate that fails the canary if startup
  p95 or memory peak exceeds the threshold.
- [ ] Add log-capture tests or structured-log assertions for the critical
  browser create timing fields.

#### Local KVM profiling results, 2026-05-25

Runs were taken on this KVM/Firecracker-capable development host with
`scripts/profile-browser-vm-cold-start.sh`.

| Lineage | Version | Cached | Stage | Copy | Firecracker start | CDP wait | Browser | Peak RSS |
| --- | --- | ---: | ---: | ---: | ---: | ---: | --- | ---: |
| `browser-rootfs` | `3f404bb-20260409T205552Z` | no | 8.3s | 0.6s | 2.5s | 4.2s | Chrome 124 | 519 MiB |
| `kernel-browser-rootfs` | `b0ec113-20260525T005324Z` | no | 30.3s | 23.4s | 2.3s | 8.3s | Chrome 148 | 1278 MiB |
| `kernel-browser-rootfs` | `b0ec113-20260525T005324Z` | yes | 3.9s | 44.9s | 0.2s | 8.3s | Chrome 148 | 1265 MiB |

Interpretation:

- On an uncongested host, Kernel Images CDP readiness was not late; it became
  ready in roughly 8.3 seconds after Firecracker instance start.
- The clear regression versus legacy is artifact size and memory footprint:
  Kernel Images rootfs is about 3.65 GiB uncompressed versus 0.91 GiB for
  legacy, and Firecracker RSS is roughly 1.25 GiB versus 0.52 GiB.
- The production late-readiness orphan is therefore likely load-sensitive:
  orphan accumulation, host memory pressure, disk copy pressure, or duplicate
  IP/MAC bridge state can push the heavier image past the old 3 minute wait
  even though the image can boot quickly on a clean host.

### Integration and recovery testing

- [ ] Simulate an agent that accepts browser create and never responds; verify
  the DB keeps placement and `vm_ip`.
- [ ] Retry after unknown outcome; verify a different browser VM cannot receive
  the same IP.
- [ ] Simulate agent-side orphan browser VM and verify reconciler cleanup.
- [ ] Simulate agent restart while browser VM is `creating`; verify recovery
  does not mark it usable unless CDP is ready.
- [ ] Simulate duplicate TAP/IP state on a test host and verify the agent guard
  rejects the duplicate before Firecracker starts.
- [ ] Run legacy browser rootfs end-to-end: create, CDP ready, pair with OCM VM,
  unpair, stop, delete, recover after agent restart.
- [ ] Run Kernel Images browser rootfs canary end-to-end: create, CDP ready,
  live view, DNAT cleanup, stop, delete, recover after agent restart.
- [ ] Run the same canary with constrained memory to verify failure mode and
  cleanup are safe.

### Operations rollout

- [x] Add host provisioning support for opt-in XFS VM storage:
  `OCM_XFS_DEVICE=/dev/... OCM_FORMAT_XFS=1 sudo bash scripts/provision-host.sh`.
- [x] Add host provisioning support for separate browser VM XFS storage:
  `OCM_BROWSER_XFS_DEVICE=/dev/... OCM_FORMAT_BROWSER_XFS=1 sudo bash scripts/provision-host.sh`,
  which writes `BROWSER_STATE_DIR=/var/lib/ocm-browser` into the agent
  environment handoff.
- [x] Add admin control-plane support for configuring separate browser VM XFS
  storage on an already enrolled host.
- [x] Add provisioning-time reflink probe and warning when `/var/lib/ocm` is
  not suitable for Kernel Images browser canaries.
- [ ] Deploy the browser-storage admin operation to backend, frontend, and host
  agents before expecting experimental browser launches to work from the UI.
- [ ] Keep host 105 in maintenance until duplicate browser IP state is cleaned.
- [ ] Capture host 105 evidence before cleanup: Firecracker PIDs, TAPs,
  browser state file, iptables chains, DB placements, memory, and OOM logs.
- [ ] Destroy or manually clean stale duplicate-IP browser VMs on host 105.
- [ ] Release stale DB placements only after the host resource is confirmed
  gone.
- [ ] Deploy backend safety patch before re-enabling broad browser VM starts.
- [ ] Redeploy or restart existing host agents so they pick up the stable
  legacy browser rootfs manifest unless explicitly canaried.
- [ ] Run one healthy-host legacy browser VM start before touching host 105.
- [ ] Run one colocated OCM + browser start on a healthy host.
- [ ] Run Kernel Images only on an explicit canary host.
- [ ] Re-enable host 105 only after no duplicate IPs, stale Firecracker
  processes, stale TAPs, or stale DNAT chains remain and a controlled browser
  start reaches CDP ready.

## Immediate Operational Plan

1. Keep host 105 in maintenance.

2. Inspect host 105:
   - list Firecracker processes
   - list TAP devices
   - inspect `/var/lib/ocm/state/browser-vms.json`
   - inspect active `browser_vm_placements`
   - inspect iptables `OCM-BVM-*` chains
   - inspect memory pressure and OOM evidence:
     - `free -m`
     - browser Firecracker RSS
     - `dmesg -T | grep -Ei 'oom|out of memory|killed process'`
   - inspect affected browser VM DB rows for `vcpus` and `memory_mb`

3. Capture duplicate-IP evidence before cleanup:
   - browser VM ID
   - PID
   - TAP name
   - socket path
   - rootfs path
   - VM IP
   - WebRTC port range if present

4. For duplicate IP `192.168.100.2`:
   - identify both browser VM IDs
   - destroy the stale one by ID if the agent knows it
   - if not known, stop its systemd unit / Firecracker PID and remove TAP/rootfs/socket/DNAT
   - only then release the stale DB placement

5. Deploy the IP safety patch with stable legacy browser rootfs as the default.

6. Keep Kernel Images browser rootfs opt-in only. Do not use it for normal
   browser VM starts until the controlled test matrix passes.

7. Deploy the already committed async DNAT recovery agent (`122d3e2`) after
   the IP safety patch is reviewed. DNAT async fixes startup recovery latency,
   but it does not by itself fix duplicate IP reuse.

## Test Plan

### Unit tests

1. `agentclient.CreateBrowserVM` returns `ErrOutcomeUnknown` on HTTP timeout.

2. `handleStartBrowserVM` preserves placement and host assignment when
   `ErrOutcomeUnknown` is returned.

3. `handleStartBrowserVM` still releases placement and unassigns host for
   definite agent errors, such as HTTP 500 or connection refused before request.

4. Machine affinity restart rejects maintenance-mode hosts.

5. Agent `CreateBrowserVM` rejects duplicate requested browser VM IPs already
   present in live/quarantined browser state.

6. Browser `waitForPort` returns promptly when its context is canceled.

7. Browser create stores `creating` before CDP readiness and transitions to
   `ready` only after CDP is reachable.

8. Browser create cleanup removes a not-ready VM from agent state and persisted
   browser state on context cancellation or CDP timeout.

9. Config defaults `BROWSER_ROOTFS_GCS_MANIFEST` to the stable legacy
   `browser-rootfs` manifest when unset.

10. Explicit `BROWSER_ROOTFS_GCS_MANIFEST=gs://openclawmachines/kernel-browser-rootfs/manifest.json`
    still selects the experimental Kernel Images lineage.

11. Agent browser create logs include rootfs manifest URI, lineage, version,
    effective vCPU/memory, step durations, and CDP wait duration.

12. Agent heartbeat persists host capabilities including browser rootfs lineage
    and storage reflink support.

13. Rootfs copy helper records reflink versus full-copy mode and can refuse
    fallback for Kernel Images browser rootfs.

14. Browser VM create selection maps image presets to expected manifest/version
    pairs and rejects unknown manifests.

15. Backend start forwards persisted browser rootfs manifest/version to the
    agent create API.

16. Agent control API passes request-level browser rootfs manifest/version to
    the orchestrator.

### Integration tests

1. Simulate an agent that accepts `POST /browser-vms` and never responds.
   Verify DB keeps the active placement and `browser_vms.vm_ip`.

2. Simulate retry after unknown outcome. Verify retry does not allocate the
   same IP to a different browser VM.

3. Simulate an agent-side orphan and reconciler cleanup. Verify placement is
   released only after agent confirms destroy or absence.

## Rollout Plan

1. Merge and deploy backend safety patch first, with stable legacy
   `browser-rootfs` as the default browser runtime.

2. Keep host 105 in maintenance until duplicate IP state is cleaned.

3. Upload agent with:
   - async DNAT recovery
   - duplicate browser IP guard
   - browser rootfs lineage and timing logs

4. Run one controlled legacy-browser-rootfs VM start on a healthy non-105 host.

5. Run one OCM + browser colocated start.

6. Run Kernel Images browser rootfs only as an explicit experimental canary:
   - set `BROWSER_ROOTFS_GCS_MANIFEST=gs://openclawmachines/kernel-browser-rootfs/manifest.json`
   - require CDP ready within the expected window
   - confirm live view, DNAT cleanup, stop/delete, and recovery
   - confirm memory headroom at `2 vCPU / 4096 MB` or raise the minimum

7. Re-enable host 105 only after:
   - no duplicate bridge IPs
   - no stale browser Firecracker processes
   - no stale TAPs
   - browser VM start reaches CDP ready on that host

## Non-Goals

1. This design does not fully optimize browser rootfs boot time.

2. This design does not redesign browser/OCM colocation. The required invariant
   remains: paired OCM and browser VMs live on the same host.

3. This design does not solve VM metrics ingest noise. Unknown VM metrics should
   be dropped instead of 500ing, but it is not the provisioning safety bug.
