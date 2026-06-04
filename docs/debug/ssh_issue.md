# SSH Certificate Auth — Root Cause & Fix Plan

## Symptom

`ocm machines ssh` connects through the Cloudflare tunnel but falls back to password prompt instead of using SSH certificate auth.

## Root Cause

`Dockerfile.openclaw:165` does `chown -R openclaw:openclaw /usr/local/bin` so openclaw can install global npm packages. The `cf-ssh-check` helper lives at `/usr/local/bin/cf-ssh-check`.

sshd's `safe_path()` requires every parent directory of `AuthorizedPrincipalsCommand` to be owned by root (uid 0) or `AuthorizedPrincipalsCommandUser` (nobody, uid 65534). `/usr/local/bin` owned by openclaw fails this check. sshd silently refuses to run the command, cert auth fails, SSH falls back to password.

```
sshd (inside VM):
  ├─ Receives SSH cert
  ├─ Checks TrustedUserCAKeys → validates CA signature        ✓
  ├─ safe_path() on AuthorizedPrincipalsCommand path           ✗ FAILS
  │   └─ /usr/local/bin owned by openclaw, not root/nobody
  ├─ (never reached) AuthorizedPrincipalsCommand
  └─ Falls back to password
```

### Ruled out
- **CA mismatch**: GCP secret fingerprint matches cert signing CA (`SHA256:lO+Y4nRzMNG2NKnHADqeUOWscbz+OYoAJyNZ33X80Uo`)
- **sshd config not loaded**: `sshd -T` confirms all three directives active
- **Match block scoping**: Falsified by `sshd -T`
- **Cert format**: Valid user certificate, principal `mathewma` matches metadata server

## Fix Plan

### 1. Move helper to root-owned path (`scripts/init-openclaw.sh`)

Copy cf-ssh-check to `/etc/ssh/cf-ssh-check` at boot. Enforce ownership and permissions:
```bash
cp /usr/local/bin/cf-ssh-check /etc/ssh/cf-ssh-check
chown root:root /etc/ssh/cf-ssh-check
chmod 755 /etc/ssh/cf-ssh-check
```

Login user stays `openclaw`. Only the helper path changes.

### 2. Use sshd_config.d drop-in (`scripts/init-openclaw.sh`)

Stop appending to `sshd_config`. Write a drop-in file pointing to the new path:
```
/etc/ssh/sshd_config.d/cf-access.conf:
  TrustedUserCAKeys /etc/ssh/cf_ca.pub
  AuthorizedPrincipalsCommand /etc/ssh/cf-ssh-check %i
  AuthorizedPrincipalsCommandUser nobody
```

Remove stale legacy lines from `sshd_config` so old `/usr/local/bin/...` path doesn't interfere.

### 3. Replace HUP reload with validated restart (`scripts/init-openclaw.sh`)

- `sshd -t` before restart (catches config errors)
- Full stop/start instead of `kill -HUP` (HUP can silently fail)
- `sshd -T` post-check to verify effective config

### 4. Add diagnostic logging (`scripts/cf-ssh-check`)

Log each invocation (uid, user, key_id, curl result) to `/var/log/cf-ssh-check.log`. Init script creates log file with `chmod 666` so `nobody` can write.

### 5. Disable password fallback in CLI (`cli/internal/commands/machines_ssh.go`)

Add `-o BatchMode=yes -o PasswordAuthentication=no` to default SSH args. Cert auth either works or fails cleanly — no silent password prompt.

### 6. Rebuild and verify

```bash
make build-upload-rootfs
ocm machines stop <name>
ocm machines start <name>
ocm machines ssh <name>
```

## Optional Hardening

Avoid global `chown -R openclaw:openclaw /usr/local/bin` in `Dockerfile.openclaw`. Use a user-local npm prefix for openclaw tools instead, so `/usr/local/bin` stays root-owned and can safely host sshd auth helpers without the copy-at-boot workaround.

## Completed Work

| File | Change |
|------|--------|
| `cli/internal/commands/machines_ssh_debug.go` | Created — ssh-debug subcommand (9 checks, `--json`) |
| `cli/internal/commands/machines_ssh.go` | Fixed FetchToken URL mutation; added ssh-debug hint in help text |
| `Makefile` | Added `debug-ssh` target |
| `scripts/init-openclaw.sh` | Moved cf-ssh-check to `/etc/ssh/` (root-owned); sshd_config.d drop-in; validated restart; legacy line cleanup |
| `docs/debug/ssh_issue.md` | This document |

## Workaround (pre-fix)

`authorized_keys` over the CF tunnel. Works but lost on VM recreate. Should no longer be needed after rootfs rebuild.

```bash
# On VM (web terminal)
mkdir -p ~/.ssh && chmod 700 ~/.ssh
echo "ssh-ed25519 AAAA..." >> ~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys

# On Mac
ocm machines ssh <name> -- -i ~/.ssh/id_ed25519
```

## Machine Details

| Field | Value |
|-------|-------|
| Name | machine-b5c6 |
| Slug | 3ly03xw |
| OpenSSH | 9.2p1 Debian-2+deb12u7 |
| Rootfs | ocm-20260225-064227 |
