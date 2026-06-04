# CLI SSH & Browse: Replace CF Access Auth with Firebase Auth

## Problem

The CLI has two separate authentication flows:

1. **`ocm login`** — Firebase auth (browser-based, works well)
2. **`ocm machines ssh` / `ocm machines browse`** — Cloudflare Access auth (separate browser login)

When a user runs `ocm machines ssh`, it opens a browser for CF Access login even though they already authenticated via Firebase with `ocm login`. This is confusing and redundant. The user has to maintain two separate sessions with two separate identity providers.

### Current SSH Flow

```
ocm machines ssh "My Bot"
  1. CLI resolves machine slug
  2. CLI calls cftoken.FetchToken() → opens browser for CF Access login
  3. CF Access returns token → sshgen.GenerateShortLivedCertificate() signs SSH cert
  4. CLI runs: ssh -i cert -o ProxyCommand="ocm ssh-proxy %h" ssh-{slug}.openclawmachines.com
  5. ssh-proxy calls carrier.StartClient() → WebSocket through CF Tunnel
  6. CF edge validates CF Access token on the tunnel route
  7. sshd validates cert against CF SSH CA (TrustedUserCAKeys)
```

**Three layers of CF Access dependency:**
- Step 2: Browser login to get CF Access token
- Step 3: CF Access token used to sign SSH cert via CF SSH CA
- Step 6: CF edge enforces CF Access policy on tunnel route

### Current Browse Flow

Same CF Access dependency — `generateSSHCert()` triggers CF Access browser login before establishing the SSH reverse tunnel for CDP.

## Constraints

- **Cloudflare Tunnels stay** — they provide the network path to VMs (zero public IPs, auto-TLS, DDoS protection). Tunnels are a transport layer, not an auth layer.
- **SSH protocol stays** — SSH is the right tool for interactive shells, port forwarding, and IDE integration (VS Code Remote).
- **Firebase is the primary auth** — the platform already uses `AUTH_MODE=firebase`. All user identity should flow through Firebase.

## Design

### Core Idea

Replace the CF Access SSH certificate flow with a **backend-issued SSH certificate** signed by a platform-managed CA. The CLI already has a valid Firebase JWT from `ocm login` — use it to request an SSH cert from the backend API. Remove CF Access from SSH tunnel routes entirely — auth is handled by sshd cert validation.

### New SSH Flow

```
ocm machines ssh "My Bot"
  1. CLI resolves machine slug
  2. CLI generates ephemeral Ed25519 key pair locally
  3. CLI calls POST /api/accounts/{id}/machines/{mid}/ssh-cert (Firebase JWT auth)
     → Sends public key in request body
     → Backend validates JWT, checks machine ownership
     → Backend signs SSH cert with platform CA key
     → Returns: { cert, expires_at, username, hostname }
  4. CLI writes cert + private key to temp files
  5. CLI runs: ssh -i key -o ProxyCommand="ocm ssh-proxy %h" ssh-{slug}.openclawmachines.com
  6. ssh-proxy opens WebSocket to tunnel endpoint (no CF Access — tunnel is open)
  7. sshd validates cert against platform CA (TrustedUserCAKeys)
  8. sshd checks principals against metadata service (owner emails)
```

**What changed:**
- Step 2-3: CLI generates keypair locally, backend only signs the public key (private key never transmitted)
- Step 3: Firebase JWT → backend API (no browser popup)
- Step 6: No CF Access on SSH routes — tunnel is a pure transport layer
- Step 7-8: Two-layer auth at the VM: cert signature validation + principal/ownership check

### Access Control Chain

SSH access requires passing **two independent gates**:

1. **Backend cert issuance** — user must have a valid Firebase JWT, must be a member of the machine's account, and the machine must be running. The cert embeds the user's email as a principal.
2. **sshd inside the VM** — validates the cert is signed by the platform CA, then runs `AuthorizedPrincipalsCommand` which queries the metadata service for owner emails. Only if the cert's principal matches an authorized owner does the connection succeed.

An unauthenticated connection to the tunnel endpoint reaches sshd but is immediately rejected without a valid, platform-CA-signed certificate containing a matching principal. This is the standard security model for SSH servers.

### Components

#### 1. Platform SSH CA Key Pair

A new Ed25519 CA key pair managed by the backend:

- **Private key**: Stored in GCP Secret Manager as `OCM_SSH_CA_PRIVATE_KEY`
- **Public key**: Stored in config as `OCM_SSH_CA_PUBLIC_KEY`, pushed to VMs via metadata

Generation (one-time):
```bash
ssh-keygen -t ed25519 -f ocm-ssh-ca -C "ocm-platform-ca" -N ""
# ocm-ssh-ca     → private key → Secret Manager
# ocm-ssh-ca.pub → public key  → env var / config
```

#### 2. Backend: `POST /api/accounts/{accountId}/machines/{id}/ssh-cert`

New authenticated endpoint that issues SSH certificates. The CLI generates a keypair locally and sends only the public key — the backend never sees or transmits the private key.

**Request:**
```json
{
  "public_key": "ssh-ed25519 AAAA... cli-ephemeral"
}
```

**Response:**
```json
{
  "certificate": "ssh-ed25519-cert-v01@openssh.com AAAA...",
  "expires_at": "2026-04-02T18:00:00Z",
  "username": "openclaw",
  "hostname": "ssh-{slug}.openclawmachines.com"
}
```

Note: The response no longer includes `cf_service_token_id`/`cf_service_token_secret` fields — CF Access is removed from SSH routes, so no service token is needed.

**Backend logic:**
1. Validate Firebase JWT (existing middleware)
2. Check user is member of machine's account
3. Check machine is running
4. Parse the client-provided public key
5. Create SSH certificate:
   - Type: user certificate
   - Key: the client's public key
   - Principal: user's email (e.g., `mathewma@gmail.com`)
   - Also add `openclaw` as principal (the SSH username)
   - Validity: 8 hours
   - Signed by platform CA private key
6. Return signed certificate (no private key material)

**Implementation:** Use Go's `crypto/ssh` package — `ssh.Certificate` struct + `ssh.Signer` for signing. Parse the client's public key with `ssh.ParsePublicKey()`.

#### 3. VM sshd Configuration Change

Replace CF SSH CA with platform SSH CA in VM init:

**Current** (`scripts/init-openclaw.sh`):
```bash
CF_CA_PUBKEY=$(curl -sf "$METADATA_URL/v1/cf-ca-pubkey")
echo "$CF_CA_PUBKEY" > /etc/ssh/cf_ca.pub
```

**New:**
```bash
PLATFORM_CA_PUBKEY=$(curl -sf "$METADATA_URL/v1/ssh-ca-pubkey")
echo "$PLATFORM_CA_PUBKEY" > /etc/ssh/platform_ca.pub
```

sshd config drop-in (`/etc/ssh/sshd_config.d/cf-access.conf` → rename to `platform-ssh.conf`):
```
TrustedUserCAKeys /etc/ssh/platform_ca.pub
AuthorizedPrincipalsCommand /etc/ssh/cf-ssh-check %i
AuthorizedPrincipalsCommandUser nobody
GatewayPorts yes
```

The `cf-ssh-check` script stays — it queries the metadata service for authorized principals. The cert just has different principals (still email-based).

**Backwards compatibility:** During rollout, trust both CAs:
```
TrustedUserCAKeys /etc/ssh/platform_ca.pub /etc/ssh/cf_ca.pub
```

#### 4. Tunnel Auth: Remove CF Access from SSH Routes

Remove CF Access from `ssh-*.openclawmachines.com` routes in the Cloudflare Zero Trust dashboard. The Cloudflare Tunnel stays — it provides the network transport (WebSocket piping, auto-TLS, DDoS protection). Only the Access identity gate at the edge is removed for SSH routes.

**Why not keep CF Access with a service token?** This was evaluated as "Option A" and rejected for three reasons:
1. **Doesn't work** — cloudflared's `carrier` package ignores `CF-Access-Client-Id`/`CF-Access-Client-Secret` headers on WebSocket upgrades. It falls back to interactive browser auth, defeating the purpose.
2. **Leaks infrastructure credentials** — the service token is a static, shared credential that would be sent to every CLI user, creating a broad blast radius if leaked.
3. **Adds complexity for no security gain** — SSH cert validation at sshd already enforces user identity. Adding CF Access is redundant defense-in-depth against a threat (unauthenticated tunnel access) that sshd already handles.

**Cloudflare config change:**
- In Zero Trust dashboard → Access → Applications, exclude `ssh-*.openclawmachines.com` from the access policy
- HTTP routes (`m-*.openclawmachines.com`) retain CF Access — only SSH routes change

#### 5. CLI Changes

**`cli/internal/commands/machines_ssh.go`:**
- Remove `generateSSHCert()` (CF Access cert generation)
- Add `generateLocalKeypair()` — creates ephemeral Ed25519 key pair in memory
- Add `fetchSSHCert()` — sends public key to backend API with Firebase JWT, receives signed cert
- Use backend-returned `username` and `hostname` instead of deriving locally
- Quote `ProxyCommand` paths to handle spaces in executable paths
- Clean up temp keys before `syscall.Exec` (defers don't run after exec)

**`cli/internal/tunnel/tunnel.go`:**
- Remove CF Access service token header logic
- Remove browser auth fallback — connection should fail cleanly if tunnel is unreachable
- Improve error messages: distinguish tunnel-not-running from auth/network failures

**`cli/internal/commands/machines_browse.go`:**
- Same changes as SSH — use `fetchSSHCert()` instead of `generateSSHCert()`
- Remove CF service token env var passing

**`cli/internal/commands/machines_ssh_debug.go`:**
- Update diagnostics to check platform CA cert instead of CF Access cert
- Remove CF Access token checks and service token checks

#### 6. Metadata Service Changes

**`backend/internal/metadata/`:**
- Add `ssh-ca-pubkey` endpoint returning platform SSH CA public key
- Existing `cf-ca-pubkey` kept during transition, removed later

### What Gets Removed

| Component | Status |
|-----------|--------|
| `cloudflared/sshgen` import in CLI | Removed |
| `cloudflared/token` import in CLI | Removed |
| `generateSSHCert()` in `machines_ssh.go` | Removed |
| CF Access browser popup during SSH | Removed |
| `CF_SSH_CA_PUBKEY` env var on backend | Replaced by `OCM_SSH_CA_PUBLIC_KEY` |
| CF Access policy on SSH routes | Removed |
| CF service token fields in ssh-cert response | Removed |
| `CF_SERVICE_TOKEN_ID`/`CF_SERVICE_TOKEN_SECRET` env vars (for SSH) | Removed from ssh-cert response (still used for agent heartbeat) |

### What Gets Added

| Component | Purpose |
|-----------|---------|
| `OCM_SSH_CA_PRIVATE_KEY` secret | Signs SSH certs |
| `OCM_SSH_CA_PUBLIC_KEY` env var | Pushed to VMs via metadata |
| `POST .../machines/{id}/ssh-cert` endpoint | Issues SSH certs via Firebase auth |
| `generateLocalKeypair()` in CLI | Creates ephemeral Ed25519 keypair |
| `fetchSSHCert()` in CLI | Sends public key to backend, receives signed cert |
| Platform CA in VM sshd config | Validates platform-signed certs |

## Migration Plan

### Phase 1: Backend + VM (no user-facing changes) — DONE

1. ~~Generate platform SSH CA key pair, store in Secret Manager~~
2. ~~Add `ssh-cert` API endpoint~~
3. ~~Update VM init to trust both CAs (platform + CF Access)~~
4. ~~Deploy backend + rebuild rootfs~~
5. ~~Verify: manually curl the ssh-cert endpoint, use returned cert to SSH~~

### Phase 2: CLI + CF Access removal (user-facing) — DONE

1. ~~Update CLI to use `fetchSSHCert()` instead of `generateSSHCert()`~~
2. ~~Remove CF service token logic from tunnel.go and ssh-cert response~~
3. ~~Remove CF Access from SSH tunnel routes in Cloudflare dashboard~~
4. ~~Update ssh-debug diagnostics~~
5. ~~Fix temp key cleanup (clean up before `syscall.Exec`, not via defer)~~
6. ~~Quote ProxyCommand paths for spaces~~
7. ~~Upload new CLI binary~~
8. ~~Fix cloudflared auto-update crash in VMs (added `--no-autoupdate`)~~
9. Verify: `ocm machines ssh` works end-to-end (pending — cloudflared restart needed on running VMs)

### Phase 3: Cleanup

1. Remove CF SSH CA from VM sshd config (trust only platform CA)
2. Remove `cloudflared/sshgen` and `cloudflared/token` dependencies from CLI go.mod
3. Remove `CF_SSH_CA_PUBKEY` env var from backend
4. Rename `cf-ssh-check` script to `platform-ssh-check` (cosmetic)
5. Rebuild rootfs to pick up `--no-autoupdate` fix (new VMs will be stable)

## Security Considerations

- **SSH cert validation is equivalent** — sshd validates cert signature against CA public key regardless of who signed it (CF or platform). The security model doesn't change.
- **Private key never leaves the client** — the CLI generates the ephemeral keypair locally and sends only the public key to the backend. The backend signs it and returns the certificate. No private key material is ever transmitted over the network.
- **Principal checking stays** — `cf-ssh-check` still queries metadata for authorized emails. Only the CA that signed the cert changes.
- **Cert TTL** — 8 hours, same as CF Access SSH certs.
- **Cert revocation** — with 8-hour TTL and no persistent keys, revocation is handled by short cert lifetimes. Removing a user from an account immediately prevents new cert issuance; existing certs expire within 8 hours. For CA key compromise, see emergency rotation below.
- **Audit logging** — the `ssh-cert` endpoint logs every cert issuance with user email, machine ID, serial number, and timestamp. This provides a clear audit trail of who accessed which machine.
- **No shared infrastructure credentials sent to clients** — unlike the service token approach, no static credentials are distributed. Each SSH session uses a unique ephemeral keypair with a short-lived cert.
- **Tunnel endpoints are open at the network layer** — removing CF Access means anyone can open a WebSocket to `ssh-*.openclawmachines.com` and reach sshd. However, sshd immediately rejects connections without a valid platform-CA-signed certificate containing a matching principal. This is the standard security model for SSH servers worldwide (e.g., GitHub, every cloud VM with a public IP).

### Platform CA Key Rotation

**Routine rotation:**
1. Generate new CA keypair
2. Deploy new private key to Secret Manager
3. Rebuild rootfs with new public key (dual-trust: new + old CA)
4. Deploy backend with new key → new certs signed by new CA
5. Wait for all running VMs to cycle (old certs valid up to 8h)
6. Remove old CA from rootfs trust config

**Emergency rotation (CA compromise):**
1. Immediately rotate the private key in Secret Manager and redeploy backend → stops new certs with old key
2. Rebuild rootfs trusting ONLY the new CA → new VMs reject old certs
3. For running VMs: force-restart all machines (`ocm machines restart --all`) to pick up new rootfs with new CA. Old certs become invalid immediately on restarted VMs.
4. Running VMs that haven't restarted continue trusting the old CA for up to the agent self-update cycle (~5 min) + restart time. This is the window of exposure.

### Membership Changes on Running VMs

Owner email principals are fetched live from the metadata service on each SSH connection (via `AuthorizedPrincipalsCommand`). The metadata service reads from the VM's assembled config, which reflects account membership at VM start time. If a user is removed from an account:
- **New cert issuance** is blocked immediately (backend checks membership)
- **Existing certs** remain valid until expiry (up to 8h), but the metadata service's principal list won't include them if the config is refreshed. For immediate revocation on running VMs, push updated config via the admin API.

## Known Issues Found & Fixed

These bugs were discovered and fixed during implementation:

1. **Temp key cleanup** — `defer cleanupSSHCert()` doesn't run because `syscall.Exec` replaces the process. **Fixed:** `scheduleCleanup()` spawns a background `sh -c "sleep 5 && rm -f ..."` process before exec.
2. **ProxyCommand path quoting** — `os.Executable()` and `cfgFile` were interpolated without quoting. Paths with spaces broke SSH. **Fixed:** wrap paths with `%q` format verb.
3. **Backend username/hostname ignored** — CLI derived host and used `--user` flag instead of cert response fields. **Fixed:** use `certResult.Username` and `certResult.Hostname`.
4. **Misleading error messages** — all `bad handshake` errors were reported as "tunnel may not be running." **Fixed:** error now distinguishes CF Access still enabled vs tunnel not running.
5. **cloudflared auto-update crash in VMs** — cloudflared auto-updated inside Firecracker VMs but failed to restart because `/usr/local/bin` isn't in `$PATH` during the SysV restart. This caused cloudflared to exit, making VMs unreachable via tunnel. **Fixed:** added `--no-autoupdate` flag to the init script. The rootfs ships a known-good version; upgrades are controlled via rootfs rebuilds.

## Files Changed

| File | Change |
|------|--------|
| `backend/internal/api/ssh_cert.go` | New: SSH cert issuance endpoint; remove CF service token from response |
| `backend/internal/api/server.go` | Register route |
| `backend/internal/config/config.go` | Add `SSHCAPrivateKey`, `SSHCAPublicKey` |
| `backend/cmd/server/main.go` | Wire SSH CA config |
| `scripts/init-openclaw.sh` | Trust platform CA (+ CF CA during transition); disable cloudflared auto-update |
| `cli/internal/commands/machines_ssh.go` | Replace `generateSSHCert` with `fetchSSHCert`; fix cleanup + quoting |
| `cli/internal/commands/ssh_cert.go` | Remove CF service token fields from response struct |
| `cli/internal/commands/machines_browse.go` | Same as ssh; remove CF service token env passing |
| `cli/internal/commands/machines_ssh_debug.go` | Update diagnostics; remove CF Access checks |
| `cli/internal/tunnel/tunnel.go` | Remove CF Access service token logic; improve error handling |
| `cli/go.mod` | Remove cloudflared/sshgen, cloudflared/token (Phase 3) |

## Cloudflare Dashboard Change

In Cloudflare Zero Trust → Access → Applications:
- Find the application covering `*.openclawmachines.com` or SSH routes
- Exclude `ssh-*.openclawmachines.com` from the access policy
- HTTP routes (`m-*.openclawmachines.com`) remain protected by CF Access

## See Also

- [auth-flow.md](../auth-flow.md) — Current authentication architecture
- [tunnel-architecture.md](../tunnel-architecture.md) — Per-VM tunnel lifecycle
- [unified-auth-rearchitecture.md](../unified-auth-rearchitecture.md) — Original CF Access migration plan
- [cli-guide.md](../cli-guide.md) — CLI user documentation
