# OpenClaw Machines Configuration Architecture

## Status: Superseded — Managed Runtime (2026-03-19)

> **Superseded (2026-03-18):** The managed appliance model (Decision 1 below) has been replaced by **managed runtime** via native config mode. The key shift: the platform *seeds* config at first boot but the user *owns* it after that. The exec secret provider eliminates the OpenClaw fork entirely. See `docs/superpowers/specs/2026-03-18-native-config-mode-design.md` for the superseding design. The architecture below documents the fork-based approach for historical context.

**Decision 1 (Product model) — SUPERSEDED:** ~~Option A — Curated managed machine. OCM machines are managed appliances.~~ Replaced by managed runtime: the platform seeds, assists, and secures — but the VM is the source of truth for config after first boot. The dashboard is a convenience helper, not the authoritative control plane. User customizations (via SSH, terminal, or dashboard) persist across reboots on `/data`.

**Decision 2 (Extension ownership) — SUPERSEDED:** ~~Direction A — Managed control plane.~~ In native mode, users install and manage skills, plugins, and agents freely. The dashboard assists via the exec endpoint but does not control.

**Current implications (native mode):**
- Config push uses `config.patch` RPC (JSON merge patch) — preserves user customizations in keys the platform didn't touch
- The exec endpoint is the universal control channel for all dashboard → VM interactions
- API key security is unchanged — nonce-based proxy auth, real keys never enter the VM
- Implementation plan: `docs/superpowers/plans/2026-03-18-native-config-mode.md`

**Historical implications (fork mode, for reference):**
- Config write-back and sidecar designs are future options, not current work
- `machine_config.user_config` is confirmed as a misleading name for an assembled-config cache — rename pending
- Agent CRUD must move to the control plane (next major work item)
- Implementation plan: `docs/plans/2026-03-13-managed-extensions-control-plane-plan.md`

## Scope

This document explains:

- how configuration works in OpenClaw Machines today
- what is actually required to configure an OpenClaw machine
- how this differs from upstream OpenClaw's local-file model
- where the readonly config-assembly pattern breaks down
- why issues like "Telegram pairing is not persisting" are confusing in the current design
- what architectural direction makes the most sense next

The analysis is based on the current OpenClaw Machines codebase, the local OpenClaw trees at `/Users/mantiz/openclaw` and `/Users/mantiz/patched/openclaw`, and the official OpenClaw Gateway documentation reviewed during this analysis.

## Executive Summary

OpenClaw Machines does **not** configure a machine by editing `~/.openclaw/openclaw.json`.

Instead, it currently has three different configuration/state layers:

| Layer | Owner | Storage | Purpose |
|---|---|---|---|
| Platform config | OCM control plane | DB -> assembled JSON -> metadata `/v1/config` | Capabilities, identity, credentials, model selection, gateway defaults |
| User/runtime config | Ambiguous today | partly local file, partly nowhere durable from the platform's view | Agents, skill settings, arbitrary config edits |
| Local runtime state | OpenClaw inside the VM | `/data` persistent volume | Pairing stores, offsets, workspace files, installed skill/plugin files |

The current architecture fails because it treats "the machine config" as if it were entirely platform-owned and readonly, but OpenClaw is not built that way. OpenClaw assumes there is a writable source config that runtime actions can mutate and later re-read.

The result is a split-brain system:

- many filesystem-backed things actually **do** persist because `/home/openclaw` and `/workspace` live on `/data`
- many config-backed things do **not** persist in a platform-authoritative way because OCM has no durable user overlay
- the control plane cannot clearly tell the difference between "platform policy", "user intent", and "runtime application state"

The concrete recommendation is now split into two layers:

1. fix the obvious product bugs immediately, especially the dashboard pairing RPC mismatch
2. make a clear product choice for Phase 1: **managed machine**, not self-extending machine
3. move skill/plugin install ownership, sandbox policy, and memory slot selection into the control plane
4. treat `machine_config.user_config` as a misleading cache unless and until OCM truly needs user-owned overlay config
5. keep the sidecar/write-back path as the future option only if OCM later decides to support self-authored machine config

## Important Terminology

The architecture becomes much easier to reason about if we use three precise terms.

### 1. Platform config

Platform config is what OCM owns and must be able to regenerate from the database.

Examples:

- enabled registry capabilities
- per-capability config templates and overrides
- machine identity
- linked credentials
- preferred model
- gateway/control UI defaults
- browser CDP proxy rewrites

This is what `assembleConfigForMachine()` and `configassembly.AssembleConfig()` produce.

### 2. User config

User config is mutable intent that belongs to the user, not to the platform bootstrap.

Examples:

- custom agents created from the Control UI
- per-agent model overrides
- skill settings in `skills.entries`
- arbitrary `config.set`, `config.patch`, `config.apply` changes the user makes inside OpenClaw
- plugin enablement/config that lives in config

OCM does **not** currently have a clean durable home for this layer.

### 3. Local runtime state

Local runtime state is application state that OpenClaw maintains on disk and should usually stay VM-local.

Examples:

- pairing request stores
- `allowFrom` stores
- Telegram update offsets
- device pairing state
- workspace files
- installed skill files
- installed plugin files

This mostly persists today because the init script mounts `/dev/vdb` at `/data` and moves `HOME`/workspace state there.

## What Configuring an OpenClaw Machine Actually Means Today

An OpenClaw machine in OCM is configured through control-plane data, not by writing `openclaw.json`.

### Control-plane inputs

The effective machine config is built from:

- machine capabilities from `machine_capabilities`
- registry entry templates from `registry_entries`
- capability overrides
- machine identity from `machine_config.identity_*`
- preferred model from `machine_config.platform_overrides`
- linked machine credentials, or if none exist, all account credentials

This happens in `backend/internal/api/machine_config.go` via `assembleConfigForMachine()`.

### Assembly rules

`backend/internal/configassembly/assembler.go` starts with platform defaults and then merges:

1. capability templates
2. capability overrides
3. identity
4. proxy/channel wiring
5. skills allowlist

But it explicitly protects major top-level config areas:

- `gateway`
- `skills`
- `ui`
- `server`
- `reload`
- `agents`
- `commands`
- `session`

That means OCM capability overrides are intentionally **not** a general-purpose user config surface. They are a tightly constrained platform extension mechanism.

### Delivery into the VM

The init script (`scripts/init-openclaw.sh`) makes the current design explicit:

- it logs `Config source: metadata endpoint (no local openclaw.json)`
- it exports `OCM_CONFIG_SOURCE=metadata`
- it exports `OCM_METADATA_URL` and `OCM_METADATA_NONCE`
- it sets `OPENCLAW_STATE_DIR=$OPENCLAW_DIR`

At runtime the metadata server serves `/v1/config` from `cfg.OpenClawConf`, then injects channel credentials and rewrites browser CDP URLs in `backend/internal/metadata/server_linux.go`.

### Extra bootstrap files

The same init script also manages a small set of important files via the persistent data volume:

- `auth-profiles.json`
- `device.json`
- `IDENTITY.md`

Those are symlinked through `config-current` under `/data/ocm/configs/...`.

This is an important clue: the system already knows some machine state must survive reboot outside the readonly assembled config.

## Persistence Model in Practice

The current persistence story is mixed, not uniformly broken.

### What clearly persists today

Because `/home/openclaw` and `/workspace` are backed by `/data`, these should survive reboot and restart:

| Item | Backing location | Expected persistence |
|---|---|---|
| Channel pairing request store and `allowFrom` files | `~/.openclaw/credentials/...` | persists |
| Device pairing state | `~/.openclaw/...` pairing paths | persists |
| Telegram update offsets | `~/.openclaw/telegram/...` | persists |
| Workspace files | `/workspace` -> `/data/workspace` | persists |
| Skill files installed into workspace | agent workspace under `~/.openclaw/workspace` | persists |
| Plugin files | `~/.openclaw/extensions` | persists |
| `device.json` | `config-current/device.json` on `/data` | persists |
| `IDENTITY.md` | `config-current/IDENTITY.md` on `/data` | persists |
| `auth-profiles.json` | `config-current/auth-profiles.json` on `/data` | persists |

### What does not have a durable platform contract

These are the areas that are not modeled cleanly:

| Item | Why it is fragile |
|---|---|
| Agents created/updated/deleted from OpenClaw | they are config mutations, but OCM does not treat them as durable user overlay |
| Skill settings in `skills.entries` | same problem |
| Arbitrary `config.set` / `config.patch` / `config.apply` changes | same problem |
| Plugin enablement/config stored in config | files may persist, but the config layer is not owned durably by OCM |

### Migration across hosts

Machine migration (backup → restore to new host) preserves the `/data` volume via encrypted GCS backup. All items in the "persists" table above survive migration because they live on `/data`. However, any ephemeral rootfs state (e.g., files written to `/tmp` or outside `/data`) is lost. This is consistent with the managed-machine model: the persistent volume carries runtime state, the control plane carries config and extension intent.

### Extension management and RBAC

The accounts feature adds role-based access control (owner / admin / member). Extension lifecycle operations — installing skills, changing plugins, modifying sandbox policy — should be gated to owner and admin roles. Members should be able to use approved extensions but not modify the extension catalog or machine policy. This applies to both the API endpoints and the admin UI surfaces described in the managed extensions plan.

## Where the Readonly Config Assembly Pattern Fails

The failure is **not** simply "state is readonly so everything is lost."

The failure is more specific:

> OCM has a readonly platform assembly, but it does not have a durable writable user-config layer that OpenClaw can round-trip through.

That causes several distinct problems.

### 1. `machine_config.user_config` is not actually acting as user config

This is the clearest design mismatch in the current code.

`machine_config` has a `user_config` column, and `GetMachineConfig()` loads it. But `assembleConfigForMachine()` does not merge it at all. Instead:

- `handlePushMachineConfig()` re-assembles platform config
- increments the version
- writes the **assembled** bytes into `user_config` through `SetMachineAssembledConfig()`

So `user_config` is currently behaving like an assembled-config snapshot cache, not like a user-owned overlay.

That has two consequences:

- the name is misleading
- the system has no actual persistent user config source of truth

### 2. OpenClaw assumes a writable source config

Upstream OpenClaw already has a strong concept of:

- source config
- resolved runtime config snapshot
- projecting runtime edits back onto source config

In the local trees this is visible in `src/config/io.ts`:

- `setRuntimeConfigSnapshot()`
- `getRuntimeConfigSourceSnapshot()`
- `projectConfigOntoRuntimeSourceSnapshot()`
- `writeConfigFile()`

That design only works if there is a durable source config path behind it.

OCM's metadata-driven model breaks that assumption unless it also provides a write-back path or a synchronized local source file.

### 3. Capability overrides are intentionally too narrow to stand in for user config

Protected keys block writes into:

- `agents`
- `skills`
- `commands`
- `session`
- and the gateway/platform surfaces

That is correct for platform safety. But it also means capability overrides cannot absorb the kinds of runtime edits OpenClaw naturally produces.

So the current system has:

- a safe platform assembly layer
- persistent local state
- but no middle layer for durable user intent

That is the actual architectural hole.

### 4. Runtime changes become invisible to the control plane

A runtime mutation can succeed locally and still be non-durable from OCM's point of view.

Typical examples:

- user creates an agent
- user updates a skill API key
- user changes config through the Control UI

Those may modify local state or a local config file, but unless OCM stores them as overlay intent, the next metadata-derived config refresh has no reason to preserve them.

### 5. Skills and plugins become split-brain features

OpenClaw supports both:

- files on disk for installed skill/plugin code
- config entries that enable, route, or parameterize them

In OCM today:

- file payloads can persist on `/data`
- platform awareness of those installs is incomplete
- OCM UI and DB do not model arbitrary user-installed skills/plugins as first-class machine state

So a user can install something that physically survives, but the platform still does not have a coherent story for:

- listing it
- backing it up as machine intent
- migrating it across architecture changes
- merging it with future platform assembly safely

## Telegram Pairing: What Is Probably Happening

The user-facing symptom "Telegram pairing is not persisting" needs to be split into two different possibilities.

### A. The underlying pairing store is probably persistent

The OpenClaw pairing store writes channel pairing requests and `allowFrom` files under the state directory. In OCM, `OPENCLAW_STATE_DIR` points into the persistent `/data`-backed home directory.

That means the raw filesystem state for channel pairing should normally survive reboot.

I did **not** find evidence that the pairing-store files themselves are being thrown away on every restart.

### B. The dashboard pairing flow appears wired to the wrong RPC

This is a concrete bug.

`frontend/src/components/ChannelSetup.tsx` submits:

- method: `node.pair.approve`
- params: `{ channel, code }`

But `node.pair.approve` in OpenClaw is for **node/device pairing**, not channel pairing. It expects a `requestId`, not a channel/code pair.

Channel pairing in OpenClaw goes through the pairing store and functions like `approveChannelPairingCode()`.

So the dashboard pairing UI is very likely approving the wrong thing, or failing outright in a way that looks like "pairing does not persist."

This should be treated as a product bug independent of the larger architecture work.

## Skills and Plugins: What Happens If Users Install Them?

### Skills

There are two separate concerns:

1. skill files
2. skill config

Skill files installed into the agent workspace should persist because the workspace lives on `/data`.

But skill config stored in `skills.entries` is a config mutation, not just a file. OCM does not currently model that as durable user overlay intent.

So:

- installation artifacts can persist
- configuration can drift or be lost from the platform's perspective

### Plugins

Plugin packages installed into `~/.openclaw/extensions` should also persist on `/data`.

But again, persisted files are only half the story. If plugin enablement or configuration depends on config state, OCM does not currently provide a first-class durable layer for that.

### Bottom line

The current system supports "files survive" better than it supports "user-managed machine behavior survives in a platform-authoritative way."

That is why arbitrary user customization feels unreliable even when the VM disk is persistent.

## Managed-Control-Plane Extensions: The Phase 1 Direction

Based on the follow-up product discussion, the most coherent Phase 1 direction is:

> skills, plugins, tool surfaces, and safety policy are managed by the control plane; the VM is not authoritative for extension lifecycle.

That means:

- skills and plugins are installed from the control plane
- installs are templated and pinned
- users do not install or configure extensions directly inside the VM
- approval flow can come later as Phase 2

This is a meaningful product decision. It makes the machine behave more like a managed appliance and less like a fully self-extending OpenClaw instance.

### What this would prevent

If enforced strictly, this model prevents:

- in-VM `skills.install`
- in-VM `skills.update`
- in-VM plugin install/update flows
- arbitrary local extension registration
- arbitrary local switching of extension-related config

It does **not** prevent OpenClaw from using approved extensions. It only prevents OpenClaw from being the source of truth for installing or authoring them.

### Why this is architecturally cleaner

This removes the current split-brain problem for extensions:

- desired state lives in OCM
- install state is reconciled in the VM
- runtime config is assembled from approved extension templates
- local disk is just execution state, not the source of truth

Under this model, readonly config assembly largely stops being a bug for skills/plugins. It becomes the correct boundary.

### What this means relative to upstream OpenClaw

The official Gateway docs assume a writable local OpenClaw control plane:

- local `openclaw.json` is authoritative
- config mutations are first-class
- Control UI and CLI can update config directly
- skills/plugins are normal local config/install surfaces

So if OCM chooses the managed-control-plane model, it is intentionally diverging from upstream's default operating model. That is acceptable, but it must be explicit and consistently enforced.

## Sandboxing and Safety Policy

Sandboxing belongs with platform policy, not with VM-local customization.

If OCM wants sandboxing enabled by default, then sandbox-related settings should be:

- centrally assembled
- enabled by default
- not user-editable from inside the VM
- overridable only through approved templates or admin policy

This is the same pattern as gateway auth and command restrictions:

- the control plane owns the safety envelope
- the VM executes inside it

The protected-key approach in OCM is directionally correct for this. It just needs to be treated as part of a broader managed-policy model rather than only as a config-merge safeguard.

## Memory Plugin Architecture

The new memory architecture is relevant because it is only partly pluginized.

Today OpenClaw has:

- a top-level `memory.*` config surface
- an exclusive plugin slot at `plugins.slots.memory`
- a bundled default memory plugin, `memory-core`
- at least one alternate memory plugin, `memory-lancedb`

That means memory is currently hybrid:

- plugin slot selects **which plugin owns memory capability**
- `memory.*` still configures **how memory runtime behaves**

### Implication for OCM

For a managed-control-plane model, OCM should own both:

- memory slot selection
- memory runtime config

Phase 1 should keep this simple:

- pin `plugins.slots.memory` to `memory-core` by default
- treat alternate memory plugins as curated control-plane options, not VM-local installs
- keep `memory.*` authored by OCM templates/policy

OCM should not assume that "memory is just another plugin now." Upstream is moving in that direction, but it has not fully removed the core `memory.*` surface.

## Remote Access, Cloudflare, and a Future iOS App

Remote access changes the transport boundary, not the configuration authority boundary.

The official Gateway remote-access model still assumes one authoritative Gateway host. Remote clients connect to that Gateway; they do not become independent sources of machine state.

### Remote access

For OCM this means:

- remote access can stay close to upstream
- the machine's Gateway remains the authority for paired devices and runtime sessions
- remote access should not be used to justify VM-local extension authorship

### Cloudflare

Cloudflare can be used in two different ways:

- as tunnel/TLS transport only
- as a browser/admin identity layer with Cloudflare Access

The clean pattern is:

- Cloudflare Access for browser/admin surfaces
- direct Gateway auth + device pairing for native/node-style clients

### Future iOS app

If OpenClaw ships an iOS app, the likely connection model is:

- connect to the machine Gateway over `wss://`
- authenticate to that Gateway
- pair as a device/node
- reconnect using the paired identity on future sessions

That does not conflict with the managed-control-plane model. It only means the app is a remote client of the machine Gateway, not an author of extension state.

## Where We Should Go From Here

There are really two decisions to make.

### Decision 1: What is the product model?

There are two plausible product positions:

#### Option A: Curated managed machine

OCM exposes only curated capabilities and managed settings. Arbitrary agent/skill/plugin mutations inside OpenClaw are intentionally unsupported.

If this is the product, the UI and docs should say so clearly, and OCM should actively block or hide unsupported runtime mutation surfaces.

#### Option B: Full OpenClaw machine

OCM wants users to treat a machine as a real OpenClaw instance they can customize.

If this is the product, then OCM must support a durable user config layer. There is no credible way around that.

Based on the current direction discussed here, my recommendation for **Phase 1** is now **Option A**.

That does not mean Option B is wrong. It means OCM should not try to be both products at once. If OCM later wants fully self-extending machines, it should add that deliberately as a different architecture, not by leaving half-working local mutation paths in place.

### Decision 2: How should extension ownership be implemented?

There are now two coherent technical directions.

#### Direction A: Managed control plane for extensions and policy

This means:

- install skills/plugins from the control plane
- use templated, pinned extension definitions
- assemble extension config from approved state
- keep sandbox and similar safety policy in platform-owned config
- reconcile desired install state into the VM before the Gateway is considered ready

This is the recommended **Phase 1** direction.

#### Direction B: Keep self-authored machine config and add write-back/sidecar sync

This means:

- metadata server remains the authoritative pull source for platform + overlay config
- sidecar writes that config to a local file
- OpenClaw uses its normal file-based config model
- sidecar later pushes user edits back as overlay updates

This matches OpenClaw's native assumptions better and reduces long-term fork maintenance, but it is only necessary if OCM wants to support user-authored machine config and extension lifecycle.

This should be treated as the future path for a self-extending product, not as the default Phase 1 plan.

## Concrete Recommended Plan

The structured implementation plan for this direction is in `docs/plans/2026-03-13-managed-extensions-control-plane-plan.md`.

### Immediate

1. Fix the dashboard pairing flow to use real channel pairing APIs instead of `node.pair.approve`.
2. Decide and document that Phase 1 machines are **managed machines** for extensions and policy.
3. Rename `SetMachineAssembledConfig()` and the stored field semantics if the value is only a cache. The current naming is actively misleading.
4. Add explicit product language about what is and is not durable today.

### Short term: managed extensions and policy

1. Add first-class control-plane definitions for managed skills/plugins/tools with pinned install templates.
2. Add desired-state records per machine for enabled extensions and slot selections.
3. Extend config assembly so OCM owns extension config, memory slot selection, and sandbox defaults.
4. Add a VM-side reconciler/installer that installs approved artifacts from desired state onto `/data`.
5. Disable or bypass conflicting VM-side extension mutation flows.

### Medium term: harden managed mode

1. Introduce stricter managed-mode enforcement in the Gateway path, either via proxy blocking or a small upstream/forked managed-mode patch.
2. Add lifecycle reporting, drift detection, reinstall, and rollback for managed artifacts.
3. Add curated alternatives for memory plugins and other exclusive plugin slots.
4. Add UI for extension catalog, machine extension state, and install health.

### Long term: optional self-service and approvals

1. If desired, add approval-based mutation intents from OpenClaw to the control plane as Phase 2.
2. Decide which runtime changes are still allowed to originate from the VM and which must stay platform-owned.
3. Only introduce a user-owned config overlay and sidecar write-back if OCM intentionally decides to support self-extending machines.
4. Remove the permanent need for the OpenClaw fork wherever possible.

## Final Assessment

The readonly config-assembly pattern was a good first move for getting machines bootstrapped safely, but it is incomplete unless the product is explicit about who owns machine state.

It works well for:

- platform-owned defaults
- capability-driven assembly
- credential injection
- safe machine bootstrap
- centralized safety policy

It is also a strong foundation for a managed-machine product where the control plane owns:

- extension lifecycle
- sandbox policy
- memory slot selection
- admin/browser access policy

It fails for:

- durable user-owned config
- round-tripping OpenClaw runtime mutations
- clear ownership boundaries between config and local state
- a credible story for arbitrary skills/plugins

So the correct next step depends on product choice.

For a self-extending machine, the next step is to formalize the missing layer:

> platform config + user overlay + local runtime state

For the currently discussed Phase 1 managed-machine direction, the next step is different:

> platform policy + managed extension desired state + local runtime execution state

Once that boundary is explicit, OCM can stop fighting OpenClaw's local mutation model halfway and instead replace it cleanly where it matters.
