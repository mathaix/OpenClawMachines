# Init Script Refactor Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add persistent state management to `scripts/init-openclaw.sh` so config files (device.json, auth-profiles.json, IDENTITY.md) survive VM reboots via timestamped config directories on the data volume.

**Architecture:** Versioned config directories under `/data/ocm/configs/<timestamp>/` with a stable `config-current` symlink. Gateway-expected paths become symlinks into the active config dir. `start_gateway()` writes auth-profiles atomically with checksum-based change detection.

**Design doc:** `docs/plans/init_sh_refactor.md` (fully reviewed, v3)

**Tech Stack:** Bash (shell script, PID 1 in Firecracker MicroVM)

---

### Task 1: Section 1b — Add `/data/ocm/configs` dir and dotfile seeding

**Files:**
- Modify: `scripts/init-openclaw.sh:73-79`

**Step 1: Edit section 1b (data volume mount)**

In the `if mountpoint -q /data` block, add `/data/ocm/configs` to the mkdir and add dotfile seeding before the symlink swap.

Replace lines 73-79:
```bash
    if mountpoint -q /data; then
        mkdir -p /data/home/openclaw /data/workspace
        chown openclaw:openclaw /data/home/openclaw /data/workspace
        # Symlink persistent dirs
        rm -rf /home/openclaw && ln -sf /data/home/openclaw /home/openclaw
        rm -rf /workspace && ln -sf /data/workspace /workspace
        echo "  [OK] Data volume mounted at /data"
```

With:
```bash
    if mountpoint -q /data; then
        mkdir -p /data/home/openclaw /data/workspace /data/ocm/configs
        chown openclaw:openclaw /data/home/openclaw /data/workspace
        # Seed default dotfiles from rootfs into data volume (first boot only).
        # On subsequent boots these already exist on the data volume and are preserved.
        if [ -d /home/openclaw ] && [ ! -L /home/openclaw ]; then
            for f in /home/openclaw/.bashrc /home/openclaw/.profile /home/openclaw/.bash_logout; do
                [ -f "$f" ] && [ ! -f "/data/home/openclaw/$(basename "$f")" ] && \
                    cp "$f" "/data/home/openclaw/$(basename "$f")"
            done
        fi
        # Symlink persistent dirs
        rm -rf /home/openclaw && ln -sf /data/home/openclaw /home/openclaw
        rm -rf /workspace && ln -sf /data/workspace /workspace
        echo "  [OK] Data volume mounted at /data"
```

**Step 2: Verify the edit**

Run: `grep -n 'ocm/configs\|dotfile\|bashrc\|! -L /home' scripts/init-openclaw.sh`
Expected: Lines showing the new mkdir, guard, and dotfile copy loop.

**Step 3: Commit**

```bash
git add scripts/init-openclaw.sh
git commit -m "feat(init): add /data/ocm/configs dir and dotfile seeding on first boot"
```

---

### Task 2: Section 8 — Extract identity fields earlier, remove inline IDENTITY.md write

**Files:**
- Modify: `scripts/init-openclaw.sh:286-305` (line numbers after Task 1 edits)

**Step 1: Edit post-metadata section**

Replace lines 286-305 (the IDENTITY.md write block after `phase_end "metadata"`):
```bash
# Write identity file so OpenClaw knows its own hostname for constructing URLs
MACHINE_SLUG=$(echo "$MACHINE_CONFIG" | jq -r '.machine_slug // empty')
VM_HOSTNAME=$(echo "$MACHINE_CONFIG" | jq -r '.vm_hostname // empty')

if [ -n "$VM_HOSTNAME" ] && [ -n "$MACHINE_SLUG" ]; then
    cat > /workspace/IDENTITY.md << IDEOF
# Machine Identity

- **Slug:** ${MACHINE_SLUG}
- **Hostname:** ${VM_HOSTNAME}
- **Preview URL pattern:** https://${VM_HOSTNAME}/port/{PORT}/

When you start a web server on a port (e.g., 3000), it is accessible at:
https://${VM_HOSTNAME}/port/3000/

Share this URL with the user so they can view the content in their browser.
IDEOF
    chown openclaw:openclaw /workspace/IDENTITY.md
    echo "  [OK] /workspace/IDENTITY.md written"
fi
```

With:
```bash
# Extract identity fields (used by config-dir setup in section 10)
MACHINE_SLUG=$(echo "$MACHINE_CONFIG" | jq -r '.machine_slug // empty')
VM_HOSTNAME=$(echo "$MACHINE_CONFIG" | jq -r '.vm_hostname // empty')
```

IDENTITY.md will be written in section 10 (Task 3) inside the config dir system.

**Step 2: Verify**

Run: `grep -n 'IDENTITY.md\|MACHINE_SLUG\|VM_HOSTNAME' scripts/init-openclaw.sh`
Expected: MACHINE_SLUG/VM_HOSTNAME extraction present, no direct `/workspace/IDENTITY.md` write.

**Step 3: Commit**

```bash
git add scripts/init-openclaw.sh
git commit -m "refactor(init): extract identity fields earlier, defer IDENTITY.md to config dir"
```

---

### Task 3: Section 10 — Timestamped config directory system

This is the largest change. Replace the old section 10 (gateway config) with the full config versioning system.

**Files:**
- Modify: `scripts/init-openclaw.sh` — the section starting at `# 10. Write OpenClaw gateway config`

**Step 1: Replace section 10**

Find and replace the old section 10:
```bash
# ============================================
# 10. Write OpenClaw gateway config
# ============================================
OPENCLAW_DIR="/home/openclaw/.openclaw"
mkdir -p "$OPENCLAW_DIR"
mkdir -p "$OPENCLAW_DIR/workspace"
mkdir -p "$OPENCLAW_DIR/agents/main/agent" "$OPENCLAW_DIR/agents/main/sessions"

# Config is served by the metadata endpoint (OCM_CONFIG_SOURCE=metadata).
# loadConfig() in the openclaw fork detects OCM_CONFIG_SOURCE=metadata and
# returns a minimal config from env vars (OPENCLAW_GATEWAY_TOKEN) instead of
# reading a local file. No openclaw.json is needed.
echo "  Config source: metadata endpoint (no local openclaw.json)"
chown -R openclaw:openclaw "$OPENCLAW_DIR"
```

With:
```bash
# ============================================
# 10. Write OpenClaw gateway config (versioned, atomic)
# ============================================
OPENCLAW_DIR="/home/openclaw/.openclaw"
mkdir -p "$OPENCLAW_DIR"
mkdir -p "$OPENCLAW_DIR/workspace"
mkdir -p "$OPENCLAW_DIR/agents/main/agent" "$OPENCLAW_DIR/agents/main/sessions"
mkdir -p "$OPENCLAW_DIR/identity"

echo "  Config source: metadata endpoint (no local openclaw.json)"

# --- Timestamped config directory ---
# Each config version creates a new dir under /data/ocm/configs/<ts>/.
# A stable symlink (config-current) points to the active version.
# User edits stay in the timestamped dir; new boots copy forward missing files.
CONFIG_BASE="/data/ocm/configs"
CONFIG_LINK="$OPENCLAW_DIR/config-current"
# Bump when config format/defaults change in the rootfs
ROOTFS_CONFIG_VERSION=1

if mountpoint -q /data 2>/dev/null; then
    NEED_NEW_CONFIG=0
    PREV_CONFIG=""

    if [ -L "$CONFIG_LINK" ] && [ -d "$(readlink -f "$CONFIG_LINK" 2>/dev/null)" ]; then
        PREV_CONFIG=$(readlink -f "$CONFIG_LINK")
        PREV_VERSION=$(cat "$PREV_CONFIG/.config-version" 2>/dev/null || echo "0")
        if [ "$PREV_VERSION" != "$ROOTFS_CONFIG_VERSION" ]; then
            NEED_NEW_CONFIG=1
            echo "  Config version changed ($PREV_VERSION -> $ROOTFS_CONFIG_VERSION)"
        else
            echo "  Config version matches, reusing $PREV_CONFIG"
        fi
    else
        NEED_NEW_CONFIG=1
        echo "  No previous config found, creating initial"
    fi

    if [ "$NEED_NEW_CONFIG" -eq 1 ]; then
        ts=$(date -u +%Y%m%dT%H%M%SZ)
        NEW_CONFIG="$CONFIG_BASE/$ts"
        # Handle timestamp collision (near-impossible but defensive)
        if [ -d "$NEW_CONFIG" ]; then
            suffix=1
            while [ -d "${NEW_CONFIG}-${suffix}" ]; do
                suffix=$((suffix + 1))
            done
            NEW_CONFIG="${NEW_CONFIG}-${suffix}"
        fi
        mkdir -p "$NEW_CONFIG"

        # Copy forward from previous config (preserve user edits)
        if [ -n "$PREV_CONFIG" ] && [ -d "$PREV_CONFIG" ]; then
            for f in "$PREV_CONFIG"/*; do
                [ -e "$f" ] || continue
                local_base=$(basename "$f")
                [ "$local_base" = ".config-version" ] && continue
                [ -f "$NEW_CONFIG/$local_base" ] || cp "$f" "$NEW_CONFIG/$local_base"
            done
            echo "  Copied forward from $PREV_CONFIG"
        fi

        # Write version marker
        echo "$ROOTFS_CONFIG_VERSION" > "$NEW_CONFIG/.config-version"
        chown -R openclaw:openclaw "$NEW_CONFIG"

        # Atomic switch
        ln -sfn "$NEW_CONFIG" "$CONFIG_LINK"
        echo "  [OK] Config switched to $NEW_CONFIG"

        # GC: keep latest 5 config dirs, never delete config-current target
        CURRENT_TARGET=$(readlink -f "$CONFIG_LINK" 2>/dev/null || echo "")
        ls -dt "$CONFIG_BASE"/[0-9]* 2>/dev/null | tail -n +6 | while read -r old; do
            old_real=$(readlink -f "$old")
            [ "$old_real" = "$CURRENT_TARGET" ] && continue
            rm -rf "$old"
            echo "  [GC] Removed old config: $old"
        done
    fi

    # --- Migrate and symlink auth-profiles.json ---
    # On upgrade: copy existing real file into config dir before overwriting with symlink.
    # On fresh/reboot: start_gateway() will create the file and ensure symlink.
    config_real=$(readlink -f "$CONFIG_LINK")
    auth_src="$OPENCLAW_DIR/agents/main/agent/auth-profiles.json"
    if [ -f "$auth_src" ] && [ ! -L "$auth_src" ]; then
        cp "$auth_src" "$config_real/auth-profiles.json"
        chown openclaw:openclaw "$config_real/auth-profiles.json"
        echo "  [OK] Migrated auth-profiles.json to config dir"
    fi
    ln -sfn "$CONFIG_LINK/auth-profiles.json" "$auth_src"

    # --- Migrate and symlink device.json (existence guard: no dangling symlinks) ---
    device_src="$OPENCLAW_DIR/identity/device.json"
    if [ -f "$device_src" ] && [ ! -L "$device_src" ]; then
        mv "$device_src" "$config_real/device.json"
        chown openclaw:openclaw "$config_real/device.json"
        ln -sfn "$CONFIG_LINK/device.json" "$device_src"
        echo "  [OK] Migrated device.json to config dir"
    elif [ -f "$CONFIG_LINK/device.json" ]; then
        ln -sfn "$CONFIG_LINK/device.json" "$device_src"
        echo "  [OK] device.json symlinked (preserved from previous boot)"
    else
        echo "  [OK] device.json: will be created on first TUI connect"
    fi

    # --- Write IDENTITY.md (only if missing in config dir) ---
    if [ -n "$VM_HOSTNAME" ] && [ -n "$MACHINE_SLUG" ]; then
        if [ ! -f "$CONFIG_LINK/IDENTITY.md" ]; then
            cat > "$config_real/IDENTITY.md" << IDEOF
# Machine Identity

- **Slug:** ${MACHINE_SLUG}
- **Hostname:** ${VM_HOSTNAME}
- **Preview URL pattern:** https://${VM_HOSTNAME}/port/{PORT}/

When you start a web server on a port (e.g., 3000), it is accessible at:
https://${VM_HOSTNAME}/port/3000/

Share this URL with the user so they can view the content in their browser.
IDEOF
            chown openclaw:openclaw "$config_real/IDENTITY.md"
            echo "  [OK] IDENTITY.md written to config dir"
        else
            echo "  [OK] IDENTITY.md exists (preserved)"
        fi
        # Consumer symlink so /workspace/IDENTITY.md still works
        ln -sfn "$CONFIG_LINK/IDENTITY.md" /workspace/IDENTITY.md
    fi
else
    echo "  [WARN] No data volume — config versioning disabled"
    # Fallback: write IDENTITY.md directly (current behavior)
    if [ -n "$VM_HOSTNAME" ] && [ -n "$MACHINE_SLUG" ]; then
        cat > /workspace/IDENTITY.md << IDEOF
# Machine Identity

- **Slug:** ${MACHINE_SLUG}
- **Hostname:** ${VM_HOSTNAME}
- **Preview URL pattern:** https://${VM_HOSTNAME}/port/{PORT}/

When you start a web server on a port (e.g., 3000), it is accessible at:
https://${VM_HOSTNAME}/port/3000/

Share this URL with the user so they can view the content in their browser.
IDEOF
        chown openclaw:openclaw /workspace/IDENTITY.md
        echo "  [OK] /workspace/IDENTITY.md written (no data volume)"
    fi
fi

# Targeted chown on directories we created (not recursive over growing state)
chown openclaw:openclaw "$OPENCLAW_DIR" \
    "$OPENCLAW_DIR/workspace" \
    "$OPENCLAW_DIR/agents" \
    "$OPENCLAW_DIR/agents/main" \
    "$OPENCLAW_DIR/agents/main/agent" \
    "$OPENCLAW_DIR/agents/main/sessions" \
    "$OPENCLAW_DIR/identity" 2>/dev/null
```

**Step 2: Verify structure**

Run: `grep -n 'CONFIG_LINK\|CONFIG_BASE\|ROOTFS_CONFIG_VERSION\|config-current\|GC\|Migrated\|IDENTITY' scripts/init-openclaw.sh`
Expected: All config versioning variables, GC logic, migration code, and IDENTITY.md write present.

**Step 3: Commit**

```bash
git add scripts/init-openclaw.sh
git commit -m "feat(init): add timestamped config dirs with versioning, migration, and GC"
```

---

### Task 4: Section 9b — Replace device.json deletion with preservation

**Files:**
- Modify: `scripts/init-openclaw.sh` — the `9b. Clean stale device identity` section

**Step 1: Replace section 9b**

Find:
```bash
# ============================================
# 9b. Clean stale device identity
# ============================================
# Remove stale device.json from previous boots so the TUI generates a fresh
# identity each boot. The gateway's shouldAllowSilentLocalPairing auto-approves
# local device pairing requests (isLocalClient=true, no browser Origin header).
rm -f /home/openclaw/.openclaw/identity/device.json 2>/dev/null
```

Replace with:
```bash
# ============================================
# 9b. Device identity
# ============================================
# device.json is preserved across reboots in config-current/ (set up in section 10).
# The gateway's shouldAllowSilentLocalPairing auto-approves local device pairing
# requests, so a fresh identity would also auto-pair — but preserving avoids
# unnecessary re-pairing delays.
# To force re-pairing, set RESET_DEVICE_IDENTITY=1 on the kernel cmdline.
if echo "$CMDLINE" | grep -q "RESET_DEVICE_IDENTITY=1"; then
    rm -f "$CONFIG_LINK/device.json" 2>/dev/null
    rm -f "$OPENCLAW_DIR/identity/device.json" 2>/dev/null
    echo "  [OK] device.json reset (RESET_DEVICE_IDENTITY=1)"
elif [ -f "$CONFIG_LINK/device.json" ] 2>/dev/null; then
    echo "  [OK] device.json preserved from previous boot"
fi
```

**Step 2: Verify**

Run: `grep -n 'RESET_DEVICE_IDENTITY\|9b\.\|device.json' scripts/init-openclaw.sh`
Expected: No `rm -f` unconditional delete. Reset only on kernel cmdline flag. Preservation message on normal boot.

**Step 3: Commit**

```bash
git add scripts/init-openclaw.sh
git commit -m "feat(init): preserve device.json across reboots, add RESET_DEVICE_IDENTITY escape hatch"
```

---

### Task 5: `start_gateway()` — Atomic auth-profiles with checksum

**Files:**
- Modify: `scripts/init-openclaw.sh` — inside the `start_gateway()` function

**Step 1: Replace auth-profiles write block**

Inside `start_gateway()`, find the auth-profiles write block:
```bash
    # Write auth-profiles.json for the gateway's auth store.
    # The gateway reads this file to resolve API keys for LLM providers.
    # Keys are set to the metadata nonce (proxy authenticates via nonce, not real key).
    local auth_dir="/home/openclaw/.openclaw/agents/main/agent"
    local profiles="{}"
    for pname in $(echo "$prov_json" | jq -r '.llm // {} | keys[]' 2>/dev/null); do
        local profile_id="${pname}-proxy"
        profiles=$(echo "$profiles" | jq --arg id "$profile_id" --arg prov "$pname" --arg key "$METADATA_NONCE" \
            '.[$id] = {"type":"api_key","provider":$prov,"key":$key}')
    done
    echo "{\"version\":1,\"profiles\":${profiles}}" | jq . > "$auth_dir/auth-profiles.json"
    chown openclaw:openclaw "$auth_dir/auth-profiles.json"
```

Replace with:
```bash
    # Write auth-profiles.json to config-current/ (atomic: write .tmp then mv).
    # The gateway reads this via symlink from agents/main/agent/.
    # Keys are set to the metadata nonce (proxy authenticates via nonce, not real key).
    local config_dir auth_file
    config_dir=$(readlink -f "$CONFIG_LINK" 2>/dev/null || echo "$OPENCLAW_DIR")
    auth_file="$config_dir/auth-profiles.json"
    local profiles="{}"
    for pname in $(echo "$prov_json" | jq -r '.llm // {} | keys[]' 2>/dev/null); do
        local profile_id="${pname}-proxy"
        profiles=$(echo "$profiles" | jq --arg id "$profile_id" --arg prov "$pname" --arg key "$METADATA_NONCE" \
            '.[$id] = {"type":"api_key","provider":$prov,"key":$key}')
    done
    local new_profiles
    new_profiles=$(echo "{\"version\":1,\"profiles\":${profiles}}" | jq .)
    # Only write if content changed (avoids churn on crash restarts)
    local old_checksum="" new_checksum=""
    new_checksum=$(echo "$new_profiles" | md5sum 2>/dev/null | cut -d' ' -f1)
    [ -f "$auth_file" ] && old_checksum=$(md5sum "$auth_file" 2>/dev/null | cut -d' ' -f1)
    if [ "$new_checksum" != "$old_checksum" ]; then
        echo "$new_profiles" > "$auth_file.tmp" && mv "$auth_file.tmp" "$auth_file"
        chown openclaw:openclaw "$auth_file"
        log INFO "auth-profiles.json updated in $config_dir"
    fi
    # Ensure consumer symlink exists (may be missing on first start after upgrade)
    ln -sfn "$CONFIG_LINK/auth-profiles.json" "$OPENCLAW_DIR/agents/main/agent/auth-profiles.json" 2>/dev/null
```

**Step 2: Also remove the stale comment in the gateway su block**

Find inside the `su -s /bin/bash openclaw -c "..."` block:
```bash
      # Device identity cleanup is handled in step 9b (before PTY server starts)
```

Remove this line (device identity is now preserved, not cleaned up).

**Step 3: Verify**

Run: `grep -n 'md5sum\|atomic\|auth_file\|config_dir.*readlink\|consumer symlink' scripts/init-openclaw.sh`
Expected: Checksum comparison, atomic write pattern, readlink fallback, consumer symlink maintenance.

**Step 4: Commit**

```bash
git add scripts/init-openclaw.sh
git commit -m "feat(init): atomic auth-profiles writes with checksum-based change detection"
```

---

### Task 6: Final verification and push

**Step 1: Full script syntax check**

Run: `bash -n scripts/init-openclaw.sh`
Expected: No syntax errors (exit 0, no output).

**Step 2: Verify all design requirements are met**

Run a series of greps to confirm each feature:

```bash
echo "=== Config versioning ==="
grep -c 'ROOTFS_CONFIG_VERSION' scripts/init-openclaw.sh

echo "=== Dotfile seeding ==="
grep -c 'bashrc.*bash_logout' scripts/init-openclaw.sh

echo "=== Timestamp collision ==="
grep -c 'suffix' scripts/init-openclaw.sh

echo "=== GC safety ==="
grep -c 'CURRENT_TARGET' scripts/init-openclaw.sh

echo "=== Migrate auth-profiles ==="
grep -c 'Migrated auth-profiles' scripts/init-openclaw.sh

echo "=== Device.json preservation ==="
grep -c 'RESET_DEVICE_IDENTITY' scripts/init-openclaw.sh

echo "=== Atomic write ==="
grep -c 'auth_file.tmp' scripts/init-openclaw.sh

echo "=== Targeted chown ==="
grep 'chown openclaw.*OPENCLAW_DIR.*identity' scripts/init-openclaw.sh

echo "=== No-data-volume fallback ==="
grep -c 'config versioning disabled' scripts/init-openclaw.sh
```

Expected: All counts >= 1.

**Step 3: Push**

```bash
git push
```
