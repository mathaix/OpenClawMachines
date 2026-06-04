# Design: GCP Secret Manager for Master Encryption Key

**Status:** Proposed
**Branch:** openclaworking

## Problem

Account credentials (Anthropic API keys, Telegram bot tokens) and per-machine secrets are encrypted with AES-256-GCM and stored in Postgres. The 32-byte master encryption key arrives via `SECRET_ENCRYPTION_KEY` env var. Cloud Run injects this using `--set-secrets`, which pulls from GCP Secret Manager and exposes it as a plain env var.

This works, but the application has no awareness of where the key comes from. The key is opaque bytes in an env var — there's no way to rotate it without redeploying, no audit trail of access, and no path toward multi-version key support.

## Goal

Make the backend fetch the encryption key directly from GCP Secret Manager at startup. This gives us:
- Explicit control over key lifecycle
- Key rotation without redeployment (fetch `latest` version on next cold start)
- GCP IAM audit logging of key access
- A foundation for future multi-key rotation (versioned keys)

## How Encryption Works Today

### Data flow

```
User sets secret (API)
  -> handleSetSecret() encrypts with AES-256-GCM
    -> crypto.Encrypt(plaintext, 32-byte-key) -> base64 ciphertext
      -> stored in secrets.encrypted_value / account_credentials.encrypted_value

User starts machine (API)
  -> handleStartMachine() decrypts all machine secrets
    -> crypto.Decrypt(ciphertext, 32-byte-key) -> plaintext
      -> passed as map[string]string to agent CreateVM API
        -> injected into MicroVM environment
```

### What gets encrypted

| Table | Use | Encrypted Column |
|-------|-----|-----------------|
| `secrets` | Per-machine secrets (env vars injected into VM) | `encrypted_value` |
| `account_credentials` | Account-level API keys and bot tokens | `encrypted_value` |

### Key validation

- **Startup:** Server fatals if key is missing or not exactly 32 bytes
- **Runtime (encrypt):** `handleSetSecret()` returns 500 if key is empty
- **Runtime (decrypt):** `handleStartMachine()` logs error per failed secret, skips it, continues startup — machine still boots but without that secret

### Failure with wrong key

If the key changes between encrypt and decrypt, `gcm.Open()` returns `"cipher: message authentication failed"`. Every previously encrypted secret becomes unreadable. The machine still starts, but with no secrets injected.

## Use Cases

### UC1: Production startup (Cloud Run)

**Before:** Cloud Run pulls `OCM_SECRET_ENCRYPTION_KEY` from GCP SM, injects as `SECRET_ENCRYPTION_KEY` env var. App reads env var. No awareness of GCP SM.

**After:** Cloud Run sets `GCP_SECRET_NAME=OCM_SECRET_ENCRYPTION_KEY` as a plain env var. App calls GCP SM API directly at startup to fetch the key value. App logs that it fetched the key from GCP SM.

### UC2: Local development

**Before:** Developer sets `SECRET_ENCRYPTION_KEY=<32-byte-key>` in `.env`.

**After:** No change. `GCP_SECRET_NAME` is not set, so the app falls back to `SECRET_ENCRYPTION_KEY` env var.

### UC3: Key rotation (future, enabled by this change)

1. Ops creates a new version of `OCM_SECRET_ENCRYPTION_KEY` in GCP SM
2. Next Cloud Run cold start fetches `versions/latest` — gets the new key
3. New encryptions use the new key
4. Old ciphertexts still need the old key (re-encryption migration needed separately)

### UC4: User sets a machine secret

1. User calls `PUT /api/accounts/{id}/machines/{id}/secrets/{key}` with plaintext value
2. `handleSetSecret()` encrypts with the master key (however it was loaded)
3. Base64 ciphertext stored in `secrets.encrypted_value`
4. No change to this flow — encryption uses the same in-memory key regardless of source

### UC5: User starts a machine

1. User calls `POST /api/accounts/{id}/machines/{id}/start`
2. `handleStartMachine()` fetches all secrets for the machine
3. Each `encrypted_value` is decrypted with the master key
4. Failed decryptions are logged and skipped (machine still starts)
5. Decrypted secrets passed to agent as plaintext over HTTPS
6. No change to this flow

## Startup Flow (After)

```
main.go startup
  |
  +-- config.Load() reads GCP_SECRET_NAME env var
  |
  +-- Is GCP_SECRET_NAME set?
  |     |
  |     YES -> Call GCP SM: projects/{project}/secrets/{name}/versions/latest
  |     |       |
  |     |       +-- Success -> use returned value as encryption key
  |     |       +-- Failure -> log.Fatalf (server won't start)
  |     |
  |     NO -> Is SECRET_ENCRYPTION_KEY set?
  |            |
  |            YES -> use it (existing behavior)
  |            NO  -> log.Fatalf (server won't start)
  |
  +-- Validate key is exactly 32 bytes (unchanged)
  |
  +-- Pass key to api.NewServer() (unchanged)
```

## Failure Modes

| Failure | When | Impact | Mitigation |
|---------|------|--------|------------|
| GCP SM unreachable at startup | Cold start, network issue | Server won't start, Cloud Run retries | Cloud Run retry + `SECRET_ENCRYPTION_KEY` fallback for emergencies |
| GCP SM returns empty secret | Misconfigured secret version | Server fatals (32-byte check fails) | Validate secret content in GCP SM before deploying |
| Wrong IAM permissions | Service account missing `secretmanager.versions.access` | Server won't start | Documented in deployment checklist |
| Key rotated, old ciphertexts | After key rotation | Decryption fails, secrets skipped at machine start | Future: re-encryption migration; for now, don't rotate without migration |
| GCP SM latency at cold start | High load / region issues | Slower cold start (~100-200ms added) | Acceptable; one-shot call, not on hot path |

## Security Considerations

- **No change to encryption algorithm** — AES-256-GCM, random 12-byte nonce per encryption
- **Key in memory** — After fetch, key lives in process memory for lifetime of instance (same as today)
- **GCP IAM audit** — Every `AccessSecretVersion` call logged in Cloud Audit Logs (new benefit)
- **Service account scoping** — Cloud Run service account needs `roles/secretmanager.secretAccessor` on the specific secret (not project-wide)
- **Env var no longer contains the key** — `SECRET_ENCRYPTION_KEY` removed from `--set-secrets`, so it won't appear in Cloud Run revision metadata

## Technical Changes

### 1. New file: `backend/internal/secrets/secrets.go`

Single function `FetchSecret(ctx, resourceName) (string, error)`:
- Creates a GCP Secret Manager client
- Calls `AccessSecretVersion` with the full resource name
- Returns the payload as a string
- Closes the client (one-shot)

### 2. New dependency

`cloud.google.com/go/secretmanager/apiv1` — GCP auth libraries already present as indirect deps.

### 3. Config change: `backend/internal/config/config.go`

Add `GCPSecretName string` field, loaded from `GCP_SECRET_NAME` env var.

### 4. Startup change: `backend/cmd/server/main.go`

Replace lines 68-73 with the startup flow described above.

### 5. Deploy script: `scripts/deploy-cloud-run.sh`

- Add `GCP_SECRET_NAME=OCM_SECRET_ENCRYPTION_KEY` to `--set-env-vars`
- Remove `SECRET_ENCRYPTION_KEY=OCM_SECRET_ENCRYPTION_KEY:latest` from `--set-secrets`

## What Does NOT Change

- `backend/pkg/crypto/crypto.go` — untouched
- `backend/internal/api/` — all handlers untouched
- `backend/internal/store/` — untouched
- Database schema — untouched
- Machine start flow — untouched
- Secret set/delete flow — untouched
- Local dev workflow — `.env` with `SECRET_ENCRYPTION_KEY` still works

## Verification

1. `make test-go` — existing tests pass (env var path unchanged)
2. Local dev: backend with `SECRET_ENCRYPTION_KEY` set, no `GCP_SECRET_NAME` — works as before
3. Cloud Run deploy with `GCP_SECRET_NAME=OCM_SECRET_ENCRYPTION_KEY`:
   - Startup log shows key fetched from GCP SM
   - Set a secret on a machine, start the machine, verify secret is injected
4. Negative test: unset both env vars — server fatals with clear error message
