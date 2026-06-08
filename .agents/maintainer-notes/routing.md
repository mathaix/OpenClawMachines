# Routing Maintainer Decisions

Use this note during reviews that touch Worker routing, route setup, host
enrollment, tunnel management, cookie domains, or self-hosted/operator docs.
These are intentional project decisions.

## Deployment Shape

- Production-like operator deployments preserve the control-plane plus
  Cloudflare Worker/KV data-plane model.
- User-facing machine access should go through the configured data-plane
  protection model. Do not expose KVM worker hosts directly to users.
- Workers are infrastructure. Authenticate them with OCM enrollment/agent tokens
  and optional Cloudflare service tokens, not Firebase human auth.

## Worker Origins

- `FRONTEND_ORIGIN_HOST` and `BACKEND_ORIGIN_HOST` must be origin hostnames that
  are not covered by the Worker route.
- Do not set Worker origin defaults to `BASE_DOMAIN`, `www.BASE_DOMAIN`,
  `app.BASE_DOMAIN`, or `api.BASE_DOMAIN` when those names are routed to the
  same Worker. That creates recursive fetches for frontend, API, and
  `/internal/resolve` fallback.
- Sample config should leave origins empty or clearly mark them as placeholders
  until the operator supplies distinct origin hosts.

## Route Resolution

- KV is the fast path for account/machine route lookup.
- Backend `/api/internal/resolve` fallback must be treated as privileged
  operator infrastructure. Protect it with service-token or equivalent controls
  in production-like deployments.
- A route entry is invalid if it lacks machine ID, host hostname, or proxy token.

## Cookies And Domains

- Localhost cookies must not set a parent domain.
- Operator cookies should use an explicit `COOKIE_DOMAIN` or derive from the
  configured data-plane domain when appropriate.
- Frontend and backend cookie-domain examples must stay aligned.

## Review Standard

Routing changes need tests or proof for both the hosted/tunnel path and the
local/operator path when the touched behavior is shared. Config examples count
as user-facing behavior: review them for loops, private defaults, and secret
leakage.
