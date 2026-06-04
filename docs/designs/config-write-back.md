# Config Write-Back: Persisting Runtime Config Mutations in OCM VMs

## Status: Deferred — Managed appliance model chosen (2026-03-15)

> The managed-appliance product decision means write-back is not needed for Phase 1. Agent/skill CRUD moves to the control plane instead. This design remains relevant only if OCM later supports self-extending machines. See `docs/configuration_architecture.md` for the decision.

## Problem

OCM VMs use `OCM_CONFIG_SOURCE=metadata` (HttpConfigSource) to load OpenClaw config from the control plane's metadata server. The config is read-only from the gateway's perspective — `persistConfig = false`.

However, OpenClaw's gateway has many runtime config mutation paths that call `writeConfigFile()` directly. In OCM VMs, these writes go to a local file that is never read back (config reloads from HTTP), so **all runtime config changes are silently lost**.

## Affected Runtime Mutations

| Write Site | When | Impact |
|------------|------|--------|
| `server-methods/agents.ts` | Agent create/update/delete | **Lost** — user-created agents disappear on reload |
| `server-methods/skills.ts` | Skill install/update | **Lost** — skill API keys and env vars disappear |
| `server-methods/config.ts` | `config.set`, `config.patch`, `config.apply` | **Lost** — any Control UI or CLI config change |
| `server.auth.shared.ts` | Runtime auth changes | **Lost** — trusted proxy config changes |
| `startup-control-ui-origins.ts` | Seed allowed origins | **Fixed** — now no-op for HTTP sources |
| `startup-auth.ts` | Generated auth token | **Already guarded** — `cfgSource.persistConfig` check |

## What's Already Covered (No Writes Needed)

Platform-level config assembled by `configassembly`:
- Gateway mode, auth, controlUi, reload settings (protected keys)
- Channel enable/disable, credentials, group/DM policies
- Identity (name, avatar)
- LLM provider routing (models.providers)
- Browser CDP URL routing
- Commands, session, skills.allowBundled

## Gaps Requiring Write-Back

### HIGH Priority
1. **Agents list** — create/update/delete agents, per-agent model overrides, workspace paths, skill allowlists
2. **Skills state** — enable/disable skills, API keys, env vars, per-skill config

### LOW Priority
3. **Route bindings** — agent→channel route mappings
4. **Workspace files** — IDENTITY.md, agents.yml (can defer to VM-local)

## Proposed Approach: Write-Back to Control Plane

Instead of replicating full agent/skill CRUD in the OCM dashboard, add a write-back mechanism so the existing Control UI and CLI commands keep working inside the VM.

### Design

1. **New metadata server endpoint**: `PUT /v1/config` (or `POST /v1/config-patch`)
   - Gateway sends config mutations back to the control plane
   - Control plane stores user config overlay per machine

2. **Config merge on serve**: When metadata server serves config:
   - Start with platform defaults (from `configassembly`)
   - Deep-merge user config overlay on top
   - Inject credentials last

3. **ConfigSource interface change**: Add optional `write(config)` method
   - HttpConfigSource implements it as PUT to metadata server
   - FileConfigSource implements it as writeConfigFile (existing behavior)

4. **Gateway integration**: Replace all `writeConfigFile()` calls in server-methods with `cfgSource.write(config)` (or a gateway-level abstraction)

### Storage

User config overlay per machine:
- Option A: New column `user_config JSONB` on `machines` table
- Option B: Separate `machine_config_overrides` table
- Option C: Store in the existing `capabilities` JSONB with a dedicated key

### Alternative: Full Dashboard Management

Build agent/skill CRUD into the OCM dashboard. Higher effort but cleaner separation:
- OCM dashboard → API → store in DB → assemble into config → serve via metadata
- No write-back needed, gateway is truly read-only
- Requires building UI for agent management, skill configuration, model selection

### Recommendation

**Start with write-back** — less work, preserves existing UX (Control UI works inside VM), and unblocks users immediately. Can migrate to full dashboard management later if needed.

## Files to Modify

### OpenClaw fork (`~/openclaw`)
- `src/config/config-source.ts` — add optional `write(config)` method
- `src/config/http-source.ts` — implement PUT write-back
- `src/config/file-source.ts` — delegate to writeConfigFile
- `src/gateway/server-methods/config.ts` — use cfgSource.write instead of writeConfigFile
- `src/gateway/server-methods/agents.ts` — same
- `src/gateway/server-methods/skills.ts` — same
- `src/gateway/server.auth.shared.ts` — same

### OCM backend
- `backend/internal/metadata/` — add PUT /v1/config endpoint
- `backend/internal/store/` — store user config overlay
- `backend/internal/configassembly/` — merge user overlay during assembly
- Migration for user_config storage

## Related
- `docs/designs/config-sync-sidecar.md` — **recommended approach** — eliminates the fork entirely via a sidecar, with write-back in Phase 2
- `docs/openclaw-config-source-proposal.md` — original ConfigSource design
- `docs/openclaw-version-management.md` — version management docs
- Current OpenClaw fork branch: `feat/config-source-v2026.3.8`
