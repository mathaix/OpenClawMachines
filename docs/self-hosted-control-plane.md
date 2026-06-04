# Self-Hosted Control Plane

This guide captures the production-like self-hosted deployment shape for the
public core. Self-hosted does not mean a reduced localhost mode. It means the
operator runs the control plane on infrastructure they control while preserving
the hosted architecture:

```text
User
  -> Cloudflare edge
  -> Cloudflare Tunnel
  -> OCM control plane
  -> OCM-issued ocm_token
  -> Cloudflare Worker + KV data plane
  -> KVM worker tunnel
  -> Firecracker VM
```

Use `docs/local-setup.md` for trusted local development with `AUTH_MODE=dev`.
Use this document for an operator-controlled deployment that should behave like
the hosted control plane.

## Prerequisites

Configure these before deploying the control plane.

### Domain

- A domain or delegated subdomain controlled by the operator.
- DNS managed in Cloudflare.
- A chosen data-plane domain, for example `example.com` or `ocm.example.com`.
- A chosen control-plane hostname, for example `app.example.com` or
  `ocm.example.com`.

The data-plane domain must support account and machine hostnames:

```text
{account-slug}.{DATA_PLANE_DOMAIN}
m-{machine-slug}.{DATA_PLANE_DOMAIN}
ssh-{machine-slug}.{DATA_PLANE_DOMAIN}
ocm-host-*.{DATA_PLANE_DOMAIN}
```

### Cloudflare

The self-hosted control plane uses the same Cloudflare primitives as the hosted
control plane:

- Cloudflare account ID.
- Cloudflare zone ID for the data-plane domain.
- Cloudflare API token with enough permission to manage DNS records, Tunnels,
  and Worker/KV resources used by OCM.
- Cloudflare Tunnel for the control-plane origin.
- Cloudflare Tunnel for registered KVM hosts.
- Per-machine Cloudflare Tunnel and DNS records.
- Cloudflare Worker deployed on the data-plane routes.
- Cloudflare KV namespace for route and account cache entries.

Cloudflare Tunnel is the ingress model. The control plane and workers should not
require open inbound firewall ports.

### Human Auth

OCM supports two first-class human auth modes for this deployment shape.

#### Firebase

Use Firebase when the operator wants app-style login handled inside OCM.

Required:

- Firebase project ID.
- Firebase Web app config for the frontend.
- The operator domain added to Firebase authorized domains.
- `AUTH_MODE=firebase`.
- `FIREBASE_PROJECT_ID`.
- Frontend Firebase config values.

Flow:

```text
User -> OCM frontend -> Firebase login -> Firebase ID token
     -> /api/auth/session/exchange
     -> OCM user/account
     -> ocm_token
```

The data plane always consumes the OCM `ocm_token`; it should not need to know
whether the original identity provider was Firebase or Cloudflare Access.

#### Cloudflare Access

Use Cloudflare Access when the operator wants identity enforced at the edge.

Required:

- Cloudflare Access application covering the control-plane/API hostnames.
- Access team domain.
- Access application AUD tag.
- `AUTH_MODE=cfaccess`.
- `CF_ACCESS_TEAM_DOMAIN`.
- `CF_ACCESS_AUD`.

Flow:

```text
User -> Cloudflare Access -> OCM backend
     -> OCM user/account
     -> ocm_token
```

If agent or automation endpoints are behind Cloudflare Access, configure a
Cloudflare Access service token and pass:

```text
CF-Access-Client-Id
CF-Access-Client-Secret
```

### OCM Secrets And Config

Generate and store these before deployment:

- `JWT_SECRET`: signs OCM sessions and tokens consumed by the data plane.
- `SECRET_ENCRYPTION_KEY`: exactly 32 bytes for encrypted machine secrets.
- `FC_AGENT_TOKEN`: control-plane to agent authentication.
- `DATA_PLANE_DOMAIN`: operator domain used for account and machine routes.
- `BACKEND_URL`: public control-plane API URL.
- `FRONTEND_URL`: public frontend URL.
- `CORS_ORIGINS`: expected browser origins.
- `CONTROL_PLANE_PROFILE=operator`.

The self-hosted target also needs operator bootstrap values:

- `COOKIE_DOMAIN`: parent cookie domain for `ocm_token`, such as
  `.example.com`. If unset, the backend derives this from
  `DATA_PLANE_DOMAIN`; localhost domains are intentionally not set on cookies.
- `OCM_ADMIN_EMAILS`: comma-separated bootstrap admin emails.
- `VITE_COOKIE_DOMAIN`: frontend logout cleanup domain. Keep it aligned with
  `COOKIE_DOMAIN`; if unset, the frontend falls back to `VITE_DATA_PLANE_DOMAIN`.
- `VITE_OCM_ADMIN_EMAILS`: frontend admin UI hint. Keep it aligned with
  `OCM_ADMIN_EMAILS`; backend authorization still comes from the server.

### KVM Worker Host

Each worker host needs:

- Linux with KVM available at `/dev/kvm`.
- Firecracker installed and executable.
- `cloudflared` installed.
- Bridge/TAP networking support.
- Kernel/rootfs/runtime assets or operator-managed artifact manifests.
- Outbound network access to Cloudflare and the control plane.
- Enough CPU, memory, and disk capacity for placed machines.

The worker registers with OCM using an enrollment token created by an admin. The
worker is infrastructure, not a human user; do not authenticate workers through
Firebase.

## Setup Order

1. Put the operator domain under Cloudflare DNS.
2. Create Cloudflare API credentials.
3. Create or select the Worker KV namespace.
4. Deploy the data-plane Worker for the operator domain.
5. Create the control-plane Tunnel and route the control-plane hostname.
6. Configure Firebase or Cloudflare Access.
7. Generate OCM secrets.
8. Start the OCM control plane with `CONTROL_PLANE_PROFILE=operator`.
9. Log in as a bootstrap admin.
10. Create a KVM host enrollment token.
11. Install/register the KVM worker.
12. Create a test user account and a test machine.
13. Verify terminal, gateway, and SSH access through the data-plane domain.

## Expected User Flow

After deployment:

1. A user visits the control-plane URL.
2. The user signs in through Firebase or Cloudflare Access.
3. OCM creates or resolves the user and account.
4. OCM issues `ocm_token` for the operator domain.
5. The user creates an OpenClaw machine.
6. OCM places the machine on a registered KVM worker.
7. OCM provisions route metadata and tunnel artifacts.
8. The Cloudflare Worker authorizes route access with `ocm_token`.
9. The KVM worker boots the Firecracker VM.
10. The user opens terminal, gateway, files, or SSH through the configured
    domain.

## Direct Machine Hostnames

The deployment must choose a protection model for direct machine hostnames:

- Prefer routing user-facing machine access through
  `{account-slug}.{DATA_PLANE_DOMAIN}/{machine-slug}/...` so the Worker can
  validate `ocm_token` and account membership.
- If direct `m-*` or `ssh-*` hostnames are user-visible, protect them with
  Cloudflare Access or require OCM-issued machine tokens on sensitive paths.

Do not expose KVM worker hosts directly to users.

## Validation Checklist

Before calling the deployment ready:

- Control-plane hostname resolves through Cloudflare.
- Browser login succeeds.
- `/api/auth/me` returns the expected OCM user.
- `ocm_token` is set for the operator domain.
- Worker can read/write the route KV namespace.
- Worker rejects unauthenticated account-subdomain requests.
- Admin can create a host enrollment token.
- KVM host can register and appears online.
- Machine create/start reaches `running`.
- Terminal/gateway routes work through the data-plane domain.
- SSH certificate issuance works if SSH is enabled.
- Machine stop/delete tears down placement and route state.

## Reference Docs

- Cloudflare Tunnel: `https://developers.cloudflare.com/tunnel/`
- Cloudflare Access JWT validation:
  `https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/`
- Cloudflare Access service tokens:
  `https://developers.cloudflare.com/cloudflare-one/access-controls/service-credentials/service-tokens/`
- Firebase ID token verification:
  `https://firebase.google.com/docs/auth/admin/verify-id-tokens`
- Firebase authorized domains:
  `https://support.google.com/firebase/answer/6400741`
- Firebase Auth emulator:
  `https://firebase.google.com/docs/emulator-suite/connect_auth`

## Current Public-Core Gaps

The architecture is the target, but the public core still needs a portability
pass before this is turn-key for arbitrary operator domains:

- Continue auditing hosted-domain examples and test fixtures so runtime paths
  always use operator configuration.
- Provide operator-specific Worker route and KV templates.
- Make host enrollment tunnel-first and service-token-aware.
- Add Firebase Auth emulator support for integration tests.
- Define and enforce the direct `m-*` and `ssh-*` hostname protection policy.
