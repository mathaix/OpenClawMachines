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

Attach the Worker route and create proxied DNS coverage for the account
hostnames. A Workers route does not create DNS. Use a proxied wildcard record
on a dedicated data-plane domain or provision one proxied record per account;
otherwise `{account-slug}.{DATA_PLANE_DOMAIN}` returns `NXDOMAIN` before the
Worker can authorize or route it. The control plane manages the `m-*`, `ssh-*`,
and enrolled-host records separately.

### Cloudflare

The self-hosted control plane uses the same Cloudflare primitives as the hosted
control plane:

- Cloudflare account ID.
- Cloudflare zone ID for the data-plane domain.
- Cloudflare API token (`CLOUDFLARE_API_TOKEN`) with these exact permission
  groups — the control plane uses it to mint per-VM tunnels, DNS records, and
  route KV entries at runtime:
  - **Account · Cloudflare Tunnel · Edit**
  - **Account · Workers KV Storage · Edit**
  - **Zone · DNS · Edit** (scoped to the data-plane zone)
  - **Zone · Zone · Read** (scoped to the data-plane zone)

  Deploying the Worker/KV with `wrangler` is a separate, one-time step that
  authenticates via `wrangler login`; if scripted with a token instead, add
  **Account · Workers Scripts · Edit**.
- Cloudflare Tunnel for the control-plane origin.
- Cloudflare Tunnel for registered KVM hosts.
- Per-machine Cloudflare Tunnel and DNS records.
- Cloudflare Worker deployed on the data-plane routes.
- Cloudflare KV namespace for route and account cache entries.

The `SEO_CACHE` KV and Browser Rendering bindings in `worker/wrangler.toml` are
optional hosted-site prerendering features, not machine-routing dependencies.
Remove those two blocks for an operator data-plane Worker, or provision both
resources and replace the SEO cache placeholder before deploying.

Cloudflare Tunnel is the ingress model for users and VM services. The current
control plane nevertheless calls each registered agent's authenticated control
API on port `9090`. Prefer private connectivity; if a public rule is necessary,
restrict it to the control plane's egress CIDR. Do not expose the agent's
workspace proxy on `9091` directly.

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

Use Cloudflare Access when the operator wants identity enforced at the edge. The
control plane only **validates** the Access JWT — it does not create the Access
application, so you set it up once in the Cloudflare Zero Trust dashboard:

1. **Zero Trust → Settings → Custom Pages / team domain** — note your **team
   domain** (`<your-team>.cloudflareaccess.com`); this is `CF_ACCESS_TEAM_DOMAIN`.
2. **Zero Trust → Access → Applications → Add an application → Self-hosted.**
   - **Application domain(s):** cover the control-plane/API hostname (your
     `BACKEND_URL` host) and the frontend host. The per-VM `m-<slug>` and
     `ssh-<slug>` hostnames are authenticated by the in-VM auth proxy + machine
     tokens, so they do **not** need to be inside this Access app.
   - **Identity/policy:** add an Allow policy (e.g. emails in `OCM_ADMIN_EMAILS`,
     or your IdP group). Anyone the policy admits becomes an OCM user on first
     request.
3. After creating the app, open its **Overview → Application Audience (AUD) tag**
   — that value is `CF_ACCESS_AUD`.
4. Set `AUTH_MODE=cfaccess`, `CF_ACCESS_TEAM_DOMAIN`, and `CF_ACCESS_AUD` in the
   control-plane env.

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
- `OCM_ARTIFACT_BUCKET`: bucket used by `provision-host.sh` for the guest
  kernel and browser assets.
- `AGENT_GCS_MANIFEST`, `ROOTFS_GCS_MANIFEST`, and
  `OPENCLAW_GCS_MANIFEST`: explicit operator artifact manifests. They are not
  derived from `OCM_ARTIFACT_BUCKET`; use the standard paths shown in
  `docs/self-hosted.env.example`.
- `WORKSPACE_INTEGRATIONS_API_URL`: optional override for the per-machine
  native MCP token audience. Leave unset to derive
  `${BACKEND_URL}/api/ocm-integrations`.
- `NEBIUS_API_KEY`: optional operator-level credential for the built-in Nebius
  platform-model catalog. Catalog entries can still appear without it, but
  selecting one will not make chat work. Leave it unset when users will connect
  per-machine BYOK or subscription credentials. Provider usage charges apply.

The self-hosted target also needs operator bootstrap values:

- `COOKIE_DOMAIN`: parent cookie domain for `ocm_token`, such as
  `.example.com`. If unset, the backend derives this from
  `DATA_PLANE_DOMAIN`; localhost domains are intentionally not set on cookies.
- `OCM_ADMIN_EMAILS`: comma-separated bootstrap admin emails.
- `VITE_COOKIE_DOMAIN`: frontend logout cleanup domain. Keep it aligned with
  `COOKIE_DOMAIN`; if unset, the frontend falls back to `VITE_DATA_PLANE_DOMAIN`.
- `VITE_OCM_ADMIN_EMAILS`: frontend admin UI hint. Keep it aligned with
  `OCM_ADMIN_EMAILS`; backend authorization still comes from the server.
- `GOOGLE_WORKSPACE_OAUTH_CLIENT_ID` and
  `GOOGLE_WORKSPACE_OAUTH_CLIENT_SECRET`: required only when enabling Google
  Workspace integrations. Other OAuth providers use the same slug-derived form,
  for example `NOTION_OAUTH_CLIENT_ID`.

Keep `OCM_ALLOW_INSECURE_WORKSPACE_INTEGRATIONS` unset in operator deployments.
It exists only for trusted local tests that intentionally probe HTTP/private
integration endpoints. `OCM_WORKSPACE_INTEGRATIONS_EXECUTE_ENABLED` and
`OCM_WORKSPACE_INTEGRATIONS_WORKFLOWS_ENABLED` are optional experimental MCP
facade flags and should stay disabled unless the operator has explicitly chosen
to expose those tools.

For the runtime tool flow and native MCP troubleshooting, see
[Workspace Integrations And Native MCP](workspace-integrations-mcp.md).

### KVM Worker Host

Each worker host needs:

- Linux with KVM available at `/dev/kvm`.
- Firecracker installed and executable.
- `cloudflared` installed.
- Bridge/TAP networking support.
- Kernel/rootfs/runtime assets or operator-managed artifact manifests.
- Outbound network access to Cloudflare and the control plane.
- Enough CPU, memory, and disk capacity for placed machines.
- Network reachability from the control plane to the authenticated agent API
  on `9090`, preferably over private networking or a narrowly scoped firewall
  rule. Port `9091` is not a public control-plane endpoint.

The worker registers with OCM using an enrollment token created by an admin. The
worker is infrastructure, not a human user; do not authenticate workers through
Firebase.

After enrollment, set `DATA_PLANE_DOMAIN` in
`/etc/ocm-agent/agent.env` and restart `ocm-agent`. The agent uses it to accept
the operator domain as a WebSocket origin. A host can appear `ready` while
routed gateway and terminal WebSockets still fail if this value is absent.

The current generated installer does not use a GCE host's ambient ADC when it
downloads the private `AGENT_GCS_MANIFEST`. If it reports that no GCS
credentials are available, the host record and tunnel may already exist but
`/usr/local/bin/ocm-agent` does not. Install a manifest-verified agent artifact
manually (or a trusted local `make build-agent` build) and enable the systemd
unit. Do not treat a successful registration message as proof that the binary
was installed.

Backups are optional and disabled by default. To enable them, set
`BACKUP_MASTER_KEY` (the 64-character hex output of `openssl rand -hex 32`),
`BACKUP_GCS_BUCKET`, and `BACKUP_GCS_PREFIX` on the control plane. Set the same
bucket and prefix—but not the master key—on every agent. Both sides also need
GCS credentials through workload identity/ADC or `GCS_SERVICE_ACCOUNT_KEY`.

## Setup Order

1. Put the operator domain under Cloudflare DNS, including proxied account-host
   coverage for the Worker route.
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
- Account hostname resolves to Cloudflare; the Worker route alone is not a DNS
  record.
- Worker rejects unauthenticated account-subdomain requests.
- Admin can create a host enrollment token.
- KVM host can register and appears online.
- Enrolled agent has `DATA_PLANE_DOMAIN` and is reachable from the control plane
  on authenticated port `9090`.
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
