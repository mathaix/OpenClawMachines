# Local-Dev Profile Implementation Plan

**Status:** Deferred  
**Date:** 2026-03-12  
**Depends on:** `docs/designs/deployment-profiles.md`

## Decision

This work is valuable, but it is not an immediate delivery priority.

This document should be treated as:

- a prepared implementation plan
- a record of the current coupling points
- a future cleanup path for `local-dev`, `self-hosted`, and OSS packaging

It is **not** an active commitment to begin implementation now.

## Goal

Make `local-dev` a first-class runtime profile so the stack can run without:

- Cloudflare Access
- Cloudflare Tunnel
- Cloudflare Worker
- public DNS

The local profile must still preserve the managed assembly. This is not a fork. It is the same stack with different providers and defaults.

## Scope

This plan covers the first usable `local-dev` mode:

- frontend on localhost
- backend on localhost
- host agent on localhost or a developer-managed Linux VM
- machine access through existing path-based control-plane proxy routes
- dev auth instead of CF Access
- no tunnel or KV requirement

This plan does **not** attempt to make Firecracker run natively on macOS. Full VM execution still requires a Linux host with KVM. On macOS, the supported shape is:

- frontend + backend on the laptop
- agent on a Linux VM, bare-metal Linux box, or remote Linux dev host

## Success Criteria

1. A developer can boot the control plane locally without any Cloudflare credentials.
2. A developer can start the agent without any tunnel token.
3. Machine creation and start succeed in `local-dev` without `tunnel_token` or `vm_hostname`.
4. Workspace, terminal, files, and gateway access work through localhost path-based routes.
5. Frontend auth works in `dev` mode without CF cookie reload loops.
6. Managed behavior remains the default and does not regress.

## Current Blockers

### 1. Backend bootstrap is hard-coupled to Cloudflare services

- `backend/cmd/server/main.go` exits if `tunnel.New(...)` returns `nil`.
- `backend/cmd/server/main.go` exits if `kvstore.New(...)` returns `nil`.
- `backend/cmd/server/main.go` always constructs the GCP provisioner with a tunnel manager.

### 2. VM create contract always requires tunnel fields

- `backend/internal/agentapi/handlers.go` rejects requests without `signing_key`, `tunnel_token`, and `vm_hostname`.
- `scripts/init-openclaw.sh` hard-fails the guest boot if those values are empty.

### 3. Frontend auth assumes CF Access

- `frontend/src/lib/api.ts` forwards `CF_Authorization` cookies as headers.
- `frontend/src/lib/auth.tsx` expects CF cookie-driven auth state.
- `frontend/src/App.tsx` clears CF cookies and reloads on auth failures.

### 4. UI still assumes tunnel/subdomain access in several places

- `frontend/src/pages/MachineView.tsx` only renders access info when `tunnel_hostname` or `custom_domain` exists.
- `frontend/src/pages/MachineWorkspace.tsx` and `frontend/src/components/MachineCard.tsx` still hardcode tunnel-shaped SSH/browser links.

The SSH/browser links in those screens still hardcode `https://ssh-{slug}.openclawmachines.com`.

### 5. KV sync is still treated as required in a few control-plane handlers

- `backend/internal/api/server.go` writes account records to KV on account creation.
- `backend/internal/api/server.go` self-heals route/account KV entries in `/api/internal/resolve`.

The machine runtime already tolerates `kvStore == nil`, but those API paths do not.

## Design Decisions

### 1. Add one top-level deployment profile switch

Use a single top-level env:

- `DEPLOYMENT_PROFILE=local-dev|self-hosted|managed`

This should control defaults. It should not replace every existing env. Existing envs such as `AUTH_MODE` still remain available for overrides.

### 2. Keep the first implementation simple

Do **not** begin with a full interface explosion. The first usable local profile can be delivered with:

- explicit profile-aware config defaults
- guarded `nil` handling for tunnel/KV/provisioner
- a local guest boot branch in the init script
- frontend auth and access-mode switches

Once that works, provider interfaces can be extracted cleanly for `self-hosted`.

### 3. Use path-based machine access for local-dev

Use the existing control-plane proxy routes as the local data plane:

- `/api/accounts/{accountId}/machines/{machineId}/terminal/...`
- `/api/accounts/{accountId}/machines/{machineId}/gateway/...`
- `/api/accounts/{accountId}/machines/{machineId}/files`

This keeps local-dev aligned with code that already exists.

### 4. Reuse dev auth on the backend

The backend already has `AUTH_MODE=dev` and `auth.DevBypassMiddleware(...)`. The missing work is the frontend behavior, not the backend primitive.

## Configuration Model

### Backend

Add to `backend/internal/config/config.go`:

- `DeploymentProfile string`
- `IngressMode string`
- `RouteStoreMode string`
- `ProvisionerMode string`

Recommended values:

| Field | `local-dev` default | `managed` default |
|------|----------------------|-------------------|
| `DeploymentProfile` | `local-dev` | `managed` |
| `AuthMode` | `dev` | `cfaccess` |
| `IngressMode` | `local-path` | `cloudflare-tunnel` |
| `RouteStoreMode` | `none` | `cloudflare-kv` |
| `ProvisionerMode` | `manual` | `gcp-managed` |

Rules:

- profile picks defaults
- explicit env overrides are allowed for testing
- `managed` remains the default when `DEPLOYMENT_PROFILE` is unset

### Agent

Add to `config.AgentConfig`:

- `DeploymentProfile string`
- `IngressMode string`

Recommended values:

| Field | `local-dev` default | `managed` default |
|------|----------------------|-------------------|
| `DeploymentProfile` | `local-dev` | `managed` |
| `IngressMode` | `none` | `tunnel` |

The agent already tolerates `TunnelToken == ""`. The missing piece is the VM guest boot contract.

### Frontend

Add:

- `VITE_DEPLOYMENT_PROFILE=local-dev|self-hosted|managed`
- `VITE_AUTH_MODE=dev|cfaccess`
- `VITE_MACHINE_ACCESS_MODE=path|subdomain`

Recommended local defaults:

- `VITE_DEPLOYMENT_PROFILE=local-dev`
- `VITE_AUTH_MODE=dev`
- `VITE_MACHINE_ACCESS_MODE=path`

## Implementation Phases

## Phase 1: Profile Plumbing and Optional Providers

**Goal:** Let the backend start in `local-dev` without tunnel/KV/provisioner hard failures.

### Changes

1. Add the new config fields and profile-derived defaults in `backend/internal/config/config.go`.
2. Make `backend/cmd/server/main.go` conditional:
   - only create `tunnelMgr` when `IngressMode == "cloudflare-tunnel"`
   - only create `kv` when `RouteStoreMode == "cloudflare-kv"`
   - only create `prov` when `ProvisionerMode == "gcp-managed"`
3. Guard KV-dependent control-plane paths:
   - `handleCreateAccount`
   - `handleInternalResolve`
4. Keep `machines.NewRuntimeService(...)` and `api.NewServer(...)` working with `nil` tunnel/KV/provisioner in local mode.

### Acceptance Criteria

- `go run ./cmd/server` starts with `DEPLOYMENT_PROFILE=local-dev` and no Cloudflare envs.
- account creation works in `local-dev`.
- machine runtime setup does not panic when tunnel/KV are absent.

## Phase 2: Local Auth and Frontend Behavior

**Goal:** Remove CF Access assumptions from the local browser flow.

### Changes

1. Add a frontend auth mode helper in `frontend/src/lib/auth.tsx` and `frontend/src/lib/api.ts`.
2. In `VITE_AUTH_MODE=dev`:
   - do not read or forward `CF_Authorization`
   - do not clear CF cookies
   - do not reload to trigger CF Access
3. Update `frontend/src/App.tsx` so `ProtectedRoute` has a `dev` path:
   - wait for `authMe()`
   - if backend returns 401 in `dev`, show a plain auth error instead of a reload loop
4. Keep `AUTH_MODE=dev` on the backend as the source of truth for the local user identity.

### Acceptance Criteria

- dashboard loads locally without CF cookies
- `/welcome`, `/dashboard`, and workspace routes do not trigger CF reload logic in local mode

## Phase 3: No-Tunnel VM Boot Path

**Goal:** Let a VM boot successfully without per-VM Cloudflare tunnel config.

### Changes

1. Extend `backend/internal/agentapi/handlers.go`:
   - keep `gateway_token` and `proxy_token` required
   - require `signing_key` only if guest auth proxy is still needed
   - require `tunnel_token` and `vm_hostname` only when `IngressMode == "tunnel"`
2. Update `scripts/init-openclaw.sh`:
   - add a local/no-tunnel branch
   - start `authproxy` only if the local profile still needs it
   - skip `cloudflared` and do not `exit 1` when tunnel fields are empty in `local-dev`
3. Keep browser, terminal, and gateway traffic flowing through the host agent proxy on `:9091`.

### Acceptance Criteria

- VM boot completes with empty `tunnel_token`
- guest init no longer exits during the auth-proxy/cloudflared phase in local-dev
- terminal and gateway still work through the control-plane path-based proxy

## Phase 4: Host Registration and Local Stack Bootstrap

**Goal:** Make the first local host easy to start without managed provisioning.

### Changes

1. Treat local agent startup as a registered/manual host flow, not a provisioned-host flow.
2. Reuse the existing enrollment path in `backend/internal/api/enrollment.go`, but allow it to operate with `s.tunnelCreator == nil`.
3. Add a developer bootstrap script or Make target that does the following:
   - starts Postgres
   - starts backend with `DEPLOYMENT_PROFILE=local-dev`
   - creates or reuses an enrollment token
   - registers the local agent
   - starts the agent with:
     - `BACKEND_URL=http://127.0.0.1:8080`
     - `AGENT_ENDPOINT=http://127.0.0.1:9090`
4. Add a `make local-stack` workflow that documents and automates the local order of operations.

### Acceptance Criteria

- a developer can bring up one local host without touching GCP or Cloudflare
- the host heartbeats with `127.0.0.1` or another developer-supplied local address
- machine start can place onto that host

## Phase 5: Local Access UX

**Goal:** Make the local profile understandable in the product UI.

### Changes

1. Add a machine access helper in the frontend so screens stop hardcoding tunnel URLs.
2. Update:
   - `frontend/src/pages/MachineView.tsx`
   - `frontend/src/pages/MachineWorkspace.tsx`
   - `frontend/src/components/MachineCard.tsx`
3. In `path` access mode:
   - show local route URLs instead of tunnel/subdomain URLs
   - hide tunnel-specific labels
   - keep CLI SSH copy, but do not render `ssh-{slug}.openclawmachines.com`

### Acceptance Criteria

- a local developer sees working localhost links
- no local UI element implies that Cloudflare is required

## Phase 6: Cleanup Into Provider Interfaces

**Goal:** Prepare the same code paths for `self-hosted` without overcomplicating the first local release.

### Interfaces to introduce after the local path works

- `IngressProvider`
  - `cloudflare-tunnel`
  - `local-path`
- `RouteStore`
  - `cloudflare-kv`
  - `none`
- `HostProvisioner`
  - `gcp-managed`
  - `manual`
- `FrontendAccessMode`
  - `subdomain`
  - `path`

This phase should be driven by the working local implementation, not invented up front.

## First Code Changes, In Order

1. `backend/internal/config/config.go`
   - add `DeploymentProfile`, `IngressMode`, `RouteStoreMode`, `ProvisionerMode`
   - derive local defaults from `DEPLOYMENT_PROFILE`
2. `backend/cmd/server/main.go`
   - stop hard-failing when tunnel/KV/provisioner are intentionally disabled in `local-dev`
3. `backend/internal/api/server.go`
   - guard KV writes when `kvStore == nil`
4. `frontend/src/lib/auth.tsx`
   - add a `dev` auth path that does not depend on CF cookies
5. `frontend/src/App.tsx`
   - remove CF reload behavior when `VITE_AUTH_MODE=dev`
6. `backend/internal/agentapi/handlers.go`
   - make tunnel fields conditional
7. `scripts/init-openclaw.sh`
   - add a no-tunnel boot branch
8. `frontend/src/pages/MachineView.tsx`
   - replace tunnel-only access links with profile-aware access helpers
9. `Makefile`
   - add `local-stack` and `local-agent` targets

## Risks

### 1. Linux/KVM dependency remains real

The local profile can remove Cloudflare, but it cannot remove the Firecracker host requirements. This must be documented clearly.

### 2. `localhost` assumptions are only valid when backend and agent share a network namespace

For laptop + Linux VM setups, the bootstrap script should allow overriding the agent address instead of forcing `127.0.0.1`.

### 3. Local-dev can drift from managed if it grows too many special cases

That is why the local data plane should reuse existing path-based proxy handlers instead of inventing a second transport.

## Deliverables

1. `local-dev` config support in backend, agent, and frontend
2. a no-tunnel VM guest boot path
3. a documented local bootstrap workflow
4. localhost-friendly access URLs in the UI
5. explicit guardrails documenting that full VM execution still needs Linux/KVM
