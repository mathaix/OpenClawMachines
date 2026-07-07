#!/bin/bash
# Verify the ocm-rootfs Docker image has all expected binaries and paths.
# Usage: make test-rootfs (or: bash scripts/test-rootfs.sh)
set -uo pipefail

IMAGE="${1:-ocm-rootfs}"
PASS=0
FAIL=0

run() {
    docker run --rm --user root --entrypoint sh "$IMAGE" -c "$1" 2>/dev/null
}

check() {
    local desc="$1" cmd="$2"
    if run "$cmd" >/dev/null 2>&1; then
        echo "  ✓ $desc"
        PASS=$((PASS + 1))
    else
        echo "  ✗ $desc"
        FAIL=$((FAIL + 1))
    fi
}

check_output() {
    local desc="$1" cmd="$2" expected="$3"
    local output
    output=$(run "$cmd" 2>/dev/null || true)
    if echo "$output" | grep -q "$expected"; then
        echo "  ✓ $desc"
        PASS=$((PASS + 1))
    else
        echo "  ✗ $desc (expected '$expected', got '$output')"
        FAIL=$((FAIL + 1))
    fi
}

echo "Testing rootfs image: $IMAGE"
echo ""

# --- Core binaries ---
echo "Core binaries:"
check "node exists"          "node --version"
check "pnpm exists"          "pnpm --version"
check "git exists"           "git --version"
check "curl exists"          "curl --version"
check "jq exists"            "jq --version"
check "gh exists"            "gh --version"
check "cloudflared exists"   "cloudflared --version"
check "vim exists"           "test -L /usr/local/bin/vim || test -x /usr/local/bin/vim"
check "tmux exists"          "tmux -V"
check "ffmpeg exists"        "ffmpeg -version"
echo ""

# --- Pre-baked skill binaries ---
echo "Skill binaries:"
check "gog exists"           "test -x /usr/local/bin/gog"
check "goplaces exists"      "test -x /usr/local/bin/goplaces"
check "gifgrep exists"       "test -x /usr/local/bin/gifgrep"
check "himalaya exists"      "test -x /usr/local/bin/himalaya"
check "filebrowser exists"   "test -x /usr/local/bin/filebrowser"
check "gws exists"           "test -x /usr/local/bin/gws"
check "op exists"            "test -x /usr/local/bin/op"
check "seed-entropy exists"  "test -x /usr/local/bin/seed-entropy"
echo ""

# --- OCM companion skills ---
echo "OCM companion skills:"
check "ocm-browser skill baked" "test -f /opt/ocm-browser/SKILL.md"
check "ocm-integrations skill baked" "test -f /opt/ocm-integrations-skill/SKILL.md"
check "ocm-integrations skill teaches native MCP" "grep -q 'mcp.servers.ocm' /opt/ocm-integrations-skill/SKILL.md"
check "ocm-integrations skill documents MCP guidance resource" "grep -q 'ocm://workspace-integrations/agent-guidance' /opt/ocm-integrations-skill/SKILL.md"
check "installer syncs ocm-integrations skill" "grep -q 'OCM_INTEGRATIONS_SKILL_SRC=\"/opt/ocm-integrations-skill\"' /usr/local/libexec/ocm/install-browser-harness"
check "installer refreshes ocm-integrations skill on rerun" "tmp=\$(mktemp -d) && WORKSPACE_DIR=\$tmp /usr/local/libexec/ocm/install-browser-harness >/tmp/ocm-install-1.log 2>&1 && echo stale > \$tmp/skills/ocm-integrations/stale.txt && printf 'stale\n' > \$tmp/skills/ocm-integrations/SKILL.md && WORKSPACE_DIR=\$tmp /usr/local/libexec/ocm/install-browser-harness >/tmp/ocm-install-2.log 2>&1 && grep -q 'mcp.servers.ocm' \$tmp/skills/ocm-integrations/SKILL.md && test ! -e \$tmp/skills/ocm-integrations/stale.txt"
echo ""

# --- User and workspace ---
echo "User and workspace:"
check "openclaw user exists"        "id openclaw"
check "/home/openclaw exists"       "test -d /home/openclaw"
check "/workspace exists"           "test -d /workspace"
check "/workspace owned by openclaw" "test \$(stat -c %U /workspace) = openclaw"
check_output "/usr/local/bin is root-owned" "stat -c '%U:%G' /usr/local/bin" "^root:root$"
check "gateway restart helper exists" "test -x /usr/local/bin/ocm-restart-gateway"
check "ocm CLI exists" "test -x /usr/local/bin/ocm"
check "ocm service admin helper exists" "test -x /usr/local/bin/ocm-service-admin"
check "sudoers narrows mutating sv to user services" "grep -q '/usr/bin/sv restart /var/service/u-\\*' /etc/sudoers.d/ocm-sv-restart"
check "sudoers preserves dedicated gateway restart helper" "grep -q '/usr/local/bin/ocm-restart-gateway' /etc/sudoers.d/ocm-sv-restart"
check "sudoers allows ocm-service-admin helper" "grep -q '/usr/local/bin/ocm-service-admin \\*' /etc/sudoers.d/ocm-sv-restart"
check "ocm service create materializes a user service" "su - openclaw -c 'ocm service create smoke --cmd \"exec sleep 60\" >/tmp/ocm-service-create.out && test -f /home/openclaw/.openclaw/services/smoke/run && test -f /home/openclaw/.openclaw/services/smoke/meta.json && test -x /etc/sv/u-smoke/run && test -L /var/service/u-smoke'"
echo ""

# --- Summary ---
TOTAL=$((PASS + FAIL))
echo "=========================================="
if [ "$FAIL" -eq 0 ]; then
    echo "ALL $TOTAL TESTS PASSED"
else
    echo "$FAIL/$TOTAL TESTS FAILED"
    exit 1
fi
echo "=========================================="
