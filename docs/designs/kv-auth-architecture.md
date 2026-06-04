# Architecture: Cloudflare Worker KV Authorization

**Status:** Documented (existing system)
**Date:** 2026-03-13
**Scope:** Request authorization for machine traffic via Cloudflare Worker + KV cache

## Summary

All browser traffic to machine workspaces flows through a Cloudflare Worker that authorizes requests using a KV cache backed by the control plane's Postgres database. The Worker makes fast authorization decisions without hitting the backend on every request, falling back to a backend resolve endpoint on cache miss.

## Components

| Component | Location | Role |
|-----------|----------|------|
| Cloudflare Worker | `worker/worker.js` | Intercepts requests, verifies JWT, checks KV, proxies to agent |
| JWT verifier | `worker/jwt.js` | HS256 JWT verification via WebCrypto |
| KV Store client | `backend/internal/kvstore/cloudflare.go` | Go client for Cloudflare KV REST API |
| Internal resolve | `backend/internal/api/server.go` (`handleInternalResolve`) | Fallback endpoint for KV misses |
| KV cache refresh | `backend/internal/api/server.go` (`refreshAccountKVCache`) | Refreshes account entry after membership changes |
| Route sync | `backend/internal/machines/runtime.go` (`syncRouteToKV`) | Writes route entries when VMs start |

## KV Key Schema

All keys live in the `OCM_ROUTES` KV namespace. When `KV_SIGNING_KEY` is set, values are wrapped as `{"p": "<payload>", "s": "<hmac>"}`.

### `account:{slug}` — TTL 24h

```json
{
  "account_id": 42,
  "user_ids": [7, 13, 99]
}
```

Written by:
- Account creation (`server.go`) — initial `[ownerID]`
- `refreshAccountKVCache` — after invitation accept, member remove, member leave
- Internal resolve fallback — self-heal on cache miss
- Worker self-heal — fire-and-forget after fallback (6h TTL)

### `route:{accountSlug}:{machineSlug}` — TTL 1h

```json
{
  "machine_id": "mach_abc123",
  "host_hostname": "ocm-host-xxx.openclawmachines.com",
  "proxy_token": "tok_..."
}
```

Written by:
- `pollVMStatus` / `syncRouteToKV` — when VM reaches running
- `syncMachineRouteToKV` — during migration workflow
- Internal resolve fallback — self-heal on cache miss
- Worker self-heal — fire-and-forget after fallback

Deleted by:
- `RuntimeService.Stop` — after VM stop
- `RuntimeService.Delete` — before DB record deletion
- `cleanupMachineIngress` — during migration cleanup
- `handleDestroyHost` — for all machines on host
- Reconciler — for machines on dead hosts

### `revoked:{userId}` — TTL 24h (declared but unused)

Defined in `kvstore/cloudflare.go` but no callers exist. Per-user token revocation is not implemented — revocation works only via account membership changes.

## Sequence Diagram — Happy Path (KV Hit)

```
Browser                    Cloudflare Worker              KV Store
  │                              │                           │
  │  GET myteam.ocm.com/bot/     │                           │
  │─────────────────────────────>│                           │
  │                              │                           │
  │                              │  extractAccountSlug       │
  │                              │  → "myteam"               │
  │                              │                           │
  │                              │  extractMachinePath       │
  │                              │  → {slug:"bot", sub:"/"}  │
  │                              │                           │
  │                              │  verifyRequestJWT         │
  │                              │  → claims {user_id: 7}    │
  │                              │                           │
  │                              │  GET account:myteam       │
  │                              │──────────────────────────>│
  │                              │  {account_id:42,          │
  │                              │   user_ids:[7,13,99]}     │
  │                              │<──────────────────────────│
  │                              │                           │
  │                              │  7 in [7,13,99]? ✓        │
  │                              │                           │
  │                              │  GET route:myteam:bot     │
  │                              │──────────────────────────>│
  │                              │  {machine_id:"m-abc",     │
  │                              │   host_hostname:"h.ocm",  │
  │                              │   proxy_token:"tok_x"}    │
  │                              │<──────────────────────────│
  │                              │                           │
  │                              │  Proxy to agent           │
  │                              │  ──────────────────────>  Agent
  │                              │                           │
  │  <response>                  │                           │
  │<─────────────────────────────│                           │
```

## Sequence Diagram — Cache Miss (Fallback + Self-Heal)

```
Browser              Worker                KV Store           Backend
  │                    │                      │                   │
  │  GET request       │                      │                   │
  │───────────────────>│                      │                   │
  │                    │                      │                   │
  │                    │  GET account:myteam  │                   │
  │                    │─────────────────────>│                   │
  │                    │  (not found)         │                   │
  │                    │<─────────────────────│                   │
  │                    │                      │                   │
  │                    │  GET route:myteam:bot│                   │
  │                    │─────────────────────>│                   │
  │                    │  (not found)         │                   │
  │                    │<─────────────────────│                   │
  │                    │                      │                   │
  │                    │  POST /api/internal/resolve              │
  │                    │  {account_slug,machine_slug}             │
  │                    │─────────────────────────────────────────>│
  │                    │                      │                   │
  │                    │                      │   ResolveRoute()  │
  │                    │                      │   SQL JOIN        │
  │                    │                      │                   │
  │                    │                      │  PutRoute (1h)    │
  │                    │                      │<──────────────────│
  │                    │                      │  PutAccount (24h) │
  │                    │                      │<──────────────────│
  │                    │                      │                   │
  │                    │  {account_id, machine_id, host,          │
  │                    │   proxy_token, user_ids}                 │
  │                    │<─────────────────────────────────────────│
  │                    │                      │                   │
  │                    │  user_id in user_ids?│                   │
  │                    │  ✓ Proxy to agent    │                   │
  │                    │                      │                   │
  │                    │  (fire & forget)     │                   │
  │                    │  PUT route + account │                   │
  │                    │─────────────────────>│                   │
  │                    │                      │                   │
  │  <response>        │                      │                   │
  │<───────────────────│                      │                   │
```

## Sequence Diagram — Membership Change (Cache Invalidation)

```
Admin                  Backend API              Postgres            KV Store
  │                       │                        │                   │
  │  POST /members/{id}   │                        │                   │
  │  (remove member)      │                        │                   │
  │──────────────────────>│                        │                   │
  │                       │                        │                   │
  │                       │  RemoveAccountMember   │                   │
  │                       │───────────────────────>│                   │
  │                       │  (deleted)             │                   │
  │                       │<───────────────────────│                   │
  │                       │                        │                   │
  │                       │  refreshAccountKVCache │                   │
  │                       │                        │                   │
  │                       │  GetAccount            │                   │
  │                       │───────────────────────>│                   │
  │                       │  ListAccountMembers    │                   │
  │                       │───────────────────────>│                   │
  │                       │  [user 7, user 99]     │                   │
  │                       │<───────────────────────│                   │
  │                       │                        │                   │
  │                       │  PutAccount(slug,      │                   │
  │                       │   {user_ids:[7,99]})   │                   │
  │                       │───────────────────────────────────────────>│
  │                       │                        │                   │
  │  204 No Content       │                        │                   │
  │<──────────────────────│                        │                   │
  │                       │                        │                   │
  ═══════ Next request from removed user (id=13) ═══════
  │                       │                        │                   │
Removed                Worker                      │               KV Store
User                     │                         │                   │
  │  GET myteam.ocm.com  │                         │                   │
  │─────────────────────>│                         │                   │
  │                      │  GET account:myteam     │                   │
  │                      │────────────────────────────────────────────>│
  │                      │  {user_ids:[7,99]}      │                   │
  │                      │<────────────────────────────────────────────│
  │                      │                         │                   │
  │                      │  13 NOT in [7,99] → 403 │                   │
  │  403 Forbidden       │                         │                   │
  │<─────────────────────│                         │                   │
```

## Sequence Diagram — VM Start (Route Cache Population)

```
User                Backend API          RuntimeService         Agent           KV Store
  │                    │                      │                   │                │
  │  POST /machines    │                      │                   │                │
  │  /{id}/start       │                      │                   │                │
  │───────────────────>│                      │                   │                │
  │                    │  Start()             │                   │                │
  │                    │─────────────────────>│                   │                │
  │                    │                      │                   │                │
  │                    │                      │  CreateVM()       │                │
  │                    │                      │──────────────────>│                │
  │                    │                      │  (accepted)       │                │
  │                    │                      │<──────────────────│                │
  │                    │                      │                   │                │
  │  202 Accepted      │                      │                   │                │
  │<───────────────────│                      │                   │                │
  │                    │                      │                   │                │
  │                    │    ┌─ pollVMStatus goroutine ──────────────────┐          │
  │                    │    │                  │                   │    │          │
  │                    │    │  GetVM() poll    │                   │    │          │
  │                    │    │─────────────────────────────────────>│    │          │
  │                    │    │  {status:"running"}                  │    │          │
  │                    │    │<─────────────────────────────────────│    │          │
  │                    │    │                  │                   │    │          │
  │                    │    │  syncRouteToKV   │                   │    │          │
  │                    │    │  PutRouteSync(account, machine,     │    │          │
  │                    │    │   {machine_id, host, proxy_token})  │    │          │
  │                    │    │────────────────────────────────────────────────────>│
  │                    │    │                  │                   │    │          │
  │                    │    │  UpdateMachineStatus("running")     │    │          │
  │                    │    │  CompleteMachineOperation           │    │          │
  │                    │    └──────────────────────────────────────────┘          │
```

## TTL Summary

| Key | Backend writes | Worker self-heal | Implication |
|-----|---------------|-----------------|-------------|
| `account:{slug}` | 24h | 6h | Removed member can access for up to 24h if KV refresh fails |
| `route:{slug}:{machine}` | 1h | 1h | Stopped machine still routable for up to 1h if delete fails |
| `revoked:{userId}` | 24h | N/A | Not implemented — no per-user revocation |

## Security Considerations

1. **KV refresh is best-effort.** If `refreshAccountKVCache` fails (KV API down, network error), the stale entry persists until TTL expiry. The 24h account TTL is the worst-case window for a removed member to retain access.

2. **No per-user token revocation.** The `revoked:{userId}` key type is defined but unused. A compromised JWT remains valid until expiry. Revocation currently requires either: (a) removing the user from the account, or (b) rotating `JWT_SECRET` (which invalidates all tokens).

3. **Route entries survive VM stop failures.** If `DeleteRouteSync` fails during VM stop, the route entry persists for up to 1h. The Worker will proxy to the agent, which will return 404 (VM not found).

4. **Signed KV prevents cache poisoning.** When `KV_SIGNING_KEY` is set, all KV values are HMAC-signed. The Worker verifies signatures on read, preventing direct KV manipulation from affecting authorization.
