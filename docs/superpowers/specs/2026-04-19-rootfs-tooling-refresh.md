# Rootfs Tooling Refresh — Package Managers, CLI Tools, Service Supervision, Agent Discovery

## Problem

1. **No Python package manager** — `python3` is installed but agents can't `pip install` anything. No `uv` either.
2. **No agent-friendly CLI tools** — agents use `grep`/`find`/`curl` which produce output that's hard for LLMs to parse. Modern alternatives (ripgrep, fd, xh) are missing.
3. **No service supervision** — PID 1 is a bash script with a `while true` loop. authproxy and cloudflared are not supervised (known hardening gap). Agents cannot create their own daemons.
4. **No tool discovery** — agents don't know what's installed unless they guess or run `which`. IDENTITY.md mixes identity with instructions.
5. **IDENTITY.md is in the wrong location** — stored in versioned config dir (`/data/ocm/configs/<ts>/`) which is for gateway config snapshots, not static reference files.
6. **Machine slugs are random** — `k3xm9p2` instead of `my-email-bot`. URLs are not human-readable.

## Changes

### 1. Add pip, uv, and python3-venv to rootfs

**Dockerfile.openclaw:**

Add to apt-get block:
```
python3-pip
python3-venv
```

Add uv as a static binary (pinned version):
```dockerfile
ARG UV_VERSION=0.7.12
RUN curl -fsSL "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-x86_64-unknown-linux-gnu.tar.gz" \
    | tar -xz -C /usr/local/bin --strip-components=1 uv-x86_64-unknown-linux-gnu/uv
```

**init-openclaw.sh** — extend `/etc/profile.d/ocm-user-installs.sh`:
```bash
export NPM_CONFIG_PREFIX="$HOME/.local"        # existing
export PATH="$HOME/.local/bin:$PATH"            # existing
export PYTHONUSERBASE="$HOME/.local"            # add
export PIP_USER=1                               # add — makes --user the default
export UV_PYTHON_INSTALL_DIR="$HOME/.local/python"  # add
```

All Python packages install to `/data/home/openclaw/.local/` and persist across VM restarts, matching npm behavior.

Size impact: ~40MB (pip ~15MB, uv ~15MB, venv ~10MB).

### 2. Bundle CLI tools

Add to Dockerfile as static binary downloads (same pattern as existing gog, himalaya, etc.):

| Tool | What | Size |
|------|------|------|
| ripgrep (`rg`) | Fast recursive search | ~5MB |
| fd | Fast find replacement | ~4MB |
| bat | cat with syntax highlighting | ~6MB |
| yq | YAML/XML/TOML processor | ~10MB |
| xsv | CSV toolkit | ~5MB |
| xh | HTTP client (HTTPie-compatible) | ~5MB |
| delta | Syntax-highlighted git diffs | ~10MB |
| ast-grep | Structural code search/rewrite | ~10MB |

Total: ~55MB. All are single static binaries with no dependencies.

### 3. Add runit for service supervision

**Dockerfile.openclaw:**
```
apt-get install -y runit
```

Size: ~1MB.

**Service definitions** — create `/etc/sv/{service}/run` scripts:

```
/etc/sv/
├── gateway/run
├── pty-server/run
├── cloudflared/run
├── authproxy/run
└── filebrowser/run
```

Each `run` script is ~3-5 lines: source env vars, exec the binary.

Example (`/etc/sv/gateway/run`):
```bash
#!/bin/sh
exec chpst -u openclaw /bin/bash -c '
  source /run/ocm-runtime-env
  exec "$OCM_EFFECTIVE_BIN" gateway --config "$OPENCLAW_DIR/openclaw.json"
'
```

**init-openclaw.sh changes:**

- Remove: ~40 lines of `&` background job launches
- Remove: ~60 lines of `while true` supervisor loop
- Add: symlink active services to `/var/service/` based on config (e.g., skip filebrowser in quick-start mode)
- Change last line: `exec runsvdir /var/service` (replaces the while-true loop)

Environment: write all service env vars to `/run/ocm-service-env/` as individual files (runit's `chpst -e` convention), or source from `/run/ocm-runtime-env` which already exists.

**Agent-created daemons:**
```bash
mkdir -p /etc/sv/my-cron
cat > /etc/sv/my-cron/run << 'EOF'
#!/bin/sh
exec python3 /data/workspace/my-cron.py
EOF
chmod +x /etc/sv/my-cron/run
ln -s /etc/sv/my-cron /var/service/
# runit auto-starts within 5 seconds
```

Fixes the hardening gap: authproxy and cloudflared now supervised alongside gateway and pty-server.

### 4. Split IDENTITY.md → IDENTITY.md + TOOLS.md

**IDENTITY.md** — pure identity (generated at boot):
```markdown
# Machine Identity

- **Name:** My Email Bot
- **Slug:** my-email-bot
- **Hostname:** m-my-email-bot.openclawmachines.com
- **Account:** acme
```

**TOOLS.md** — capabilities and how to use them (generated at boot):
```markdown
# Available Tools

## Web Preview
Start a web server on any port → accessible at:
https://m-my-email-bot.openclawmachines.com/port/{PORT}/

## Package Managers
- `npm` / `pnpm` — Node packages (persists across restarts)
- `pip` / `uv` — Python packages (persists across restarts)

## CLI Tools
- `rg` — fast recursive search (use instead of grep)
- `fd` — fast file finder (use instead of find)
- `bat` — file viewer with syntax highlighting (use instead of cat)
- `xh` — HTTP client with JSON support (use instead of curl for APIs)
- `yq` — YAML/XML/TOML processor (like jq for non-JSON)
- `xsv` — CSV toolkit (select, join, stats, sort)
- `ast-grep` — structural code search/rewrite using AST patterns
- `markitdown` — convert PDF/DOCX/XLSX/PPTX to Markdown (install: pip install markitdown)

## Services
- `sv status <name>` — check service status
- `sv restart <name>` — restart a service
- Create daemons: add run script to /etc/sv/<name>/, symlink to /var/service/

## Integrations
- `composio` — 980+ app integrations. Run `composio search <query>` to find tools.

## Browser
- CDP endpoint at 192.168.100.1:9222 (when browser companion is paired)
```

TOOLS.md is **generated dynamically** — browser section omitted if no companion VM, composio section omitted if not configured, etc. The init script knows the machine state.

### 5. Fix IDENTITY.md location

Move from `/data/ocm/configs/<ts>/IDENTITY.md` to `/data/home/openclaw/.openclaw/IDENTITY.md`.

Stable path, not tied to config versioning. Symlink at `/workspace/IDENTITY.md` still works.

TOOLS.md goes to `/data/home/openclaw/.openclaw/TOOLS.md` with symlink at `/workspace/TOOLS.md`.

### 6. Name-based machine slugs

**`machines.go` — replace `generateShortID()` with `slugifyName()`:**

```go
func slugifyName(name string) string {
    // Lowercase, replace spaces/special chars with hyphens
    // Trim to 30 chars
    // Append 3-char random suffix for uniqueness: "my-email-bot-a3k"
    // Fallback to pure random if name is empty
}
```

Validation: lowercase, `[a-z0-9-]`, 3-30 chars, no leading/trailing hyphens, no double hyphens.

Existing machines keep their random slugs. Only new machines get name-based slugs. No migration needed.

URLs become: `https://acme.openclawmachines.com/my-email-bot/...` instead of `.../k3xm9p2/...`

## What Does NOT Change

- Gateway config assembly pipeline
- Config versioning system (just no longer stores IDENTITY.md)
- SOUL.md location and behavior
- Metadata service
- Agent binary
- Cloudflare tunnel/worker routing
- Frontend (except minor slug display improvements)

## Rootfs Size Impact

| Addition | Size |
|----------|------|
| pip + venv | ~25MB |
| uv | ~15MB |
| CLI tools (8 binaries) | ~55MB |
| runit | ~1MB |
| **Total** | **~96MB** |

## Build & Deploy

Rootfs-only change. No agent rebuild needed.

```bash
make build-upload-rootfs   # new VMs get it immediately
```

Existing running VMs are unaffected. New VMs (and restarts) pick up the new rootfs from GCS.

## Sequencing

1. **PR 1: pip + uv + CLI tools** — Dockerfile changes only, no init script changes
2. **PR 2: runit** — Dockerfile + init script refactor (service extraction + exec runsvdir)
3. **PR 3: IDENTITY.md + TOOLS.md** — init script changes for file generation and new locations
4. **PR 4: name-based slugs** — backend-only change in machines.go
