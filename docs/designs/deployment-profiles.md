# Design: Deployment Profiles

**Status:** Proposed  
**Date:** 2026-03-12  
**Scope:** Local development, self-hosted deployment, and managed deployment profiles for the OpenClaw Machines stack

**Implementation plan:** `docs/plans/2026-03-12-local-dev-profile-plan.md`

## Summary

OpenClaw Machines should support three runtime profiles built from the same core system:

1. `local-dev`
2. `self-hosted`
3. `managed`

The goal is not three separate products. The goal is one stack with different providers and adapters per profile.

Key rule:

- profile differences should be driven by configuration and provider interfaces
- not by forking the codebase into separate architectures

This is required for two reasons:

1. the stack must run locally without Cloudflare tunnels
2. the managed product should still consume the same underlying components as the public/open-source stack

## Problem

Today, the system is too coupled to the managed profile in several places:

- VM create requests still require `tunnel_token` and `vm_hostname` in `backend/internal/agentapi/handlers.go`
- machine start still creates per-VM tunnels in `backend/internal/machines/runtime.go`
- host provisioning still hard-requires the tunnel manager in `backend/internal/provisioner/provisioner.go`
- auth still assumes CF Access and Worker forwarding behavior in `backend/internal/auth/auth.go`
- the Worker is hard-wired to managed domains and Cloud Run origins in `worker/worker.js`

That makes the managed profile the only first-class runtime shape. It also makes local development and future self-hosting harder than necessary.

## Goals

1. Define a clean profiles model for the stack.
2. Make `local-dev` a first-class operating mode.
3. Make `self-hosted` possible without requiring the managed edge.
4. Keep `managed` as the reference premium assembly of the stack.
5. Clarify how open-source components are consumed by the managed product.

## Non-Goals

1. This document does not define billing behavior in detail.
2. This document does not define a full self-hosted product offering.
3. This document does not require all managed-only infrastructure to be available in local mode.
4. This document does not commit to supporting every cloud provider equally.

## Core Model

The stack should be split into:

- **Core services**
  - frontend
  - backend control plane
  - host agent
  - Firecracker runtime
  - machine access proxy

- **Profile-specific adapters**
  - auth provider
  - ingress provider
  - edge router
  - route store
  - artifact source
  - host provisioner
  - billing provider
  - DNS/tunnel automation

The managed profile is the opinionated assembly of those pieces, not a different system.

## Supported Profiles

## 1. `local-dev`

Purpose:

- developer workflow
- local debugging
- integration testing
- contributor onboarding

Requirements:

- runs without Cloudflare Access
- runs without Cloudflare Tunnels
- runs without public DNS
- machine access works on localhost

Expected shape:

- frontend on localhost
- backend on localhost
- host agent on localhost
- direct backend-to-agent communication
- path-based machine access instead of subdomain/tunnel access

## 2. `self-hosted`

Purpose:

- customer-operated or community-operated installation
- one or more hosts, operator-managed infrastructure

Requirements:

- no dependency on OpenClaw Machines managed edge
- can run with or without Cloudflare
- can use operator-provided DNS and reverse proxy
- can use manual host provisioning initially

Expected shape:

- same control plane and host agent contracts
- operator chooses ingress/auth/storage providers
- billing may be disabled or operator-managed

## 3. `managed`

Purpose:

- the OpenClaw Machines hosted product

Requirements:

- production-grade ingress
- Cloudflare Access and Tunnel integration
- managed provisioning
- managed billing
- managed artifact rollout

Expected shape:

- current production architecture remains the premium assembly
- this is the reference deployment, not the only deployment

## Profile Matrix

| Concern | `local-dev` | `self-hosted` | `managed` |
|---------|-------------|---------------|-----------|
| Frontend | local Vite / local static build | operator-hosted | managed |
| Backend API | localhost | operator-hosted | managed |
| Host agent | localhost single host | operator-managed hosts | managed hosts |
| Firecracker runtime | local Linux host | operator Linux hosts | managed Linux hosts |
| Edge router | none or local reverse proxy | optional | Cloudflare Worker |
| Auth | dev/local auth | OIDC/dev/operator choice | Cloudflare Access |
| Machine ingress | path-based localhost or direct host port | path-based or subdomain | per-VM tunnel + subdomain |
| Tunnel provider | none | optional | Cloudflare Tunnel |
| Route store | none or local DB | DB / optional KV | Cloudflare KV + DB |
| Artifact source | local filesystem | local/S3/GCS | GCS |
| Provisioner | none/manual | manual or provider plugin | GCP managed provisioner |
| Billing | disabled | optional/operator choice | Stripe |
| CLI default target | localhost | self-hosted base URL | managed base URL |

## Desired Request Paths

## `local-dev`

Recommended request flow:

1. browser -> `http://localhost:5173`
2. frontend -> `http://localhost:8080`
3. backend -> `http://localhost:9090`
4. browser machine access -> `http://localhost:9091/proxy/{machineID}/...`

Important:

- no Worker required
- no tunnel required
- no subdomain routing required

Recommended local machine URL shape:

- `http://localhost:9091/proxy/{machineID}/gateway/...`
- `http://localhost:9091/proxy/{machineID}/terminal/...`
- optional local reverse proxy aliases later

## `self-hosted`

Two acceptable shapes:

1. path-based mode
   - easiest to operate
   - no wildcard DNS required

2. subdomain mode
   - operator-provided DNS and reverse proxy
   - optional Cloudflare integration

## `managed`

Request flow stays aligned with production:

1. browser -> Cloudflare edge
2. Cloudflare Access -> frontend/backend
3. Worker -> host or machine route resolution
4. per-VM tunnel ingress for machine access

## Provider Interfaces

These are the interfaces the stack should grow toward.

## `AuthProvider`

Responsibilities:

- authenticate browser/API requests
- resolve user identity
- support session or token-based local/dev flow

Implementations:

- `dev`
- `oidc`
- `cfaccess`

## `IngressProvider`

Responsibilities:

- determine how machine traffic is exposed
- produce user-facing access URLs
- optionally provision machine ingress resources

Implementations:

- `local_path`
- `reverse_proxy`
- `cloudflare_tunnel`

## `EdgeRouter`

Responsibilities:

- route frontend/API/machine requests at the edge
- optional optimization, not a universal dependency

Implementations:

- `none`
- `local_proxy`
- `cloudflare_worker`

## `ArtifactSource`

Responsibilities:

- fetch rootfs and agent artifacts
- fetch manifests
- stage artifacts locally

Implementations:

- `local_file`
- `s3`
- `gcs`

## `RouteStore`

Responsibilities:

- store machine route data when indirect routing is used

Implementations:

- `none`
- `postgres`
- `kv`

## `HostProvisioner`

Responsibilities:

- create and destroy hosts
- or return existing hosts in manual mode

Implementations:

- `none`
- `manual`
- `gcp_managed`
- future provider adapters

## `BillingProvider`

Responsibilities:

- enforce paid/comped state
- create/manage subscriptions where applicable

Implementations:

- `none`
- `stripe`
- future operator billing adapters

## Managed Profile as Reference Assembly

The managed profile should consume the shared stack like this:

- closed control plane assembles the system
- open-source CLI is the official public client
- machine access proxy is embedded in the machine/guest path
- tunnel manager is enabled as the ingress provider
- artifact updater is enabled with the GCS artifact source
- Firecracker host runtime is used inside the managed host agent

In other words:

- `managed` is the reference assembly of the public infrastructure building blocks
- plus the closed control plane and product UX

That lets us keep the managed product opinionated without forking the stack.

## Current Code Constraints

These are the main blockers to the profile model.

### 1. Tunnel fields are still mandatory in the agent VM contract

Current behavior:

- `backend/internal/agentapi/handlers.go` requires `signing_key`
- it also requires `tunnel_token`
- it also requires `vm_hostname`

Impact:

- `local-dev` cannot create a VM without pretending tunnels exist

Desired behavior:

- these should only be required when `IngressProvider=cloudflare_tunnel`

### 2. Machine start assumes tunnel creation

Current behavior:

- `backend/internal/machines/runtime.go` creates per-VM tunnels during start

Impact:

- ingress strategy is mixed into lifecycle orchestration

Desired behavior:

- machine start should call an ingress provider
- local and self-hosted modes can return direct/path-based access without tunnel setup

### 3. Host provisioning assumes Cloudflare tunnel management

Current behavior:

- `backend/internal/provisioner/provisioner.go` hard-fails if the tunnel manager is nil

Impact:

- managed host provisioning is acting as the universal provisioning contract

Desired behavior:

- GCP managed provisioning should be one provisioner implementation
- local/manual profiles should not require tunnel automation

### 4. Auth path is still Cloudflare-shaped

Current behavior:

- `backend/internal/auth/auth.go` is built around CF Access first, then legacy fallback

Impact:

- auth is not modeled as a profile-level provider

Desired behavior:

- backend startup should select an auth provider explicitly by profile

### 5. Edge routing is treated as universal

Current behavior:

- `worker/worker.js` encodes managed domains and routing assumptions

Impact:

- local and self-hosted paths are second-class

Desired behavior:

- Worker remains managed-only
- local and self-hosted should not depend on it

## Target Architecture by Profile

## `local-dev`

### Enabled

- frontend
- backend
- host agent
- Firecracker host runtime
- machine access proxy
- dev auth
- local artifact source

### Disabled or optional

- Cloudflare Worker
- Cloudflare Access
- Cloudflare Tunnel
- Stripe
- GCP provisioner

### Access model

- path-based
- localhost
- direct backend -> agent

## `self-hosted`

### Enabled

- backend
- host agent
- Firecracker runtime
- machine access proxy
- operator-selected auth
- operator-selected ingress

### Optional

- Cloudflare Worker
- Cloudflare Tunnel
- Stripe
- provider-specific provisioners

### Access model

- path-based first
- subdomain mode optional

## `managed`

### Enabled

- frontend
- backend
- host agent
- Firecracker runtime
- machine access proxy
- Cloudflare Worker
- Cloudflare Access
- Cloudflare Tunnel
- GCS artifact source
- GCP managed provisioner
- Stripe

### Access model

- edge-authenticated
- per-VM tunnel access
- managed host provisioning

## Recommended Implementation Order

## Phase 1: Introduce profile config

Add a profile config model that explicitly selects:

- auth provider
- ingress provider
- artifact source
- provisioner
- billing provider

Do not start by trying to generalize every package. Start by making the selected profile explicit at process startup.

## Phase 2: Make local ingress work without tunnels

Required changes:

1. make tunnel fields optional in the agent VM request
2. add path-based local machine access
3. let machine start return local access URLs without tunnel creation

This is the critical step for local development.

## Phase 3: Separate managed-only edge dependencies

Required changes:

1. treat Worker as managed-only
2. treat Cloudflare Access as one auth provider
3. treat tunnel manager as one ingress provider

This isolates the managed edge rather than making it universal.

## Phase 4: Define self-hosted support boundary

Required changes:

1. decide which operator dependencies are supported
2. define minimum viable self-hosted topology
3. document what is managed-only vs self-hostable

## Decisions

### 1. `local-dev` is a required profile

This is not optional or “nice to have.”

If the stack cannot run locally without tunneling, the architecture is too coupled to the managed edge.

### 2. `managed` remains the premium assembly

The hosted product should continue to be the best-integrated and most automated profile.

The existence of `local-dev` and `self-hosted` does not reduce the value of `managed`.

### 3. Worker and Cloudflare integrations are adapters, not foundations

They remain important in production, but they should not define the core architecture.

### 4. Open-source components should map onto the profile model

The profile model should explain how the public components are consumed:

- CLI works against all profiles
- machine access proxy works in all profiles
- tunnel manager is optional and profile-specific
- artifact updater works in all profiles with different storage backends
- Firecracker host kit works in all profiles with different ingress and provisioning choices

## Immediate Next Step

The next architecture step should be:

1. define a `local-dev` profile explicitly in configuration
2. remove tunnel requirements from the universal VM create path
3. add a local path-based machine access mode

Until that exists, the stack is still managed-profile-first rather than profile-based.
