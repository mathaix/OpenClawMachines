#!/bin/bash
# Baseline test: openclaw gateway + opik plugin + mock receiver.
# Verifies traces flow from an LLM call through opik to an HTTP endpoint.
#
# Order: write config → install plugin → start gateway → send chat → check traces
#
# Usage:
#   bash scripts/test-opik-baseline.sh

set -euo pipefail

TEST_DIR="$(mktemp -d /tmp/opik-baseline-XXXXXX)"
OPIK_PLUGIN_SOURCE="${OPIK_PLUGIN_SOURCE:-github:mathaix/opik-openclaw#fix/opik-embedded-tracing}"
MOCK_PID=""
GATEWAY_PID=""
MOCK_PORT=19876
GATEWAY_PORT=18799
GATEWAY_TOKEN="baseline-test-token"

cleanup() {
    [ -n "$GATEWAY_PID" ] && kill "$GATEWAY_PID" 2>/dev/null || true
    [ -n "$MOCK_PID" ] && kill "$MOCK_PID" 2>/dev/null || true
    if [ "${TEST_PASSED:-0}" = "1" ]; then
        rm -rf "$TEST_DIR"
    else
        echo "Test dir preserved: $TEST_DIR"
    fi
}
trap cleanup EXIT

echo "=== Opik Baseline Test ==="
echo "Test dir: $TEST_DIR"
echo "OpenClaw: $(openclaw --version 2>&1)"

# Get API key
if [ -z "${E2E_ANTHROPIC_API_KEY:-}" ]; then
    E2E_ANTHROPIC_API_KEY=$(gcloud secrets versions access latest --secret=E2E_ANTHROPIC_API_KEY --project=clarateach 2>/dev/null || true)
fi
if [ -z "${E2E_ANTHROPIC_API_KEY:-}" ]; then
    echo "ERROR: no API key"
    exit 1
fi

export HOME="$TEST_DIR"
export OPENCLAW_STATE_DIR="$TEST_DIR/.openclaw"
mkdir -p "$OPENCLAW_STATE_DIR/workspace"

# 1. Mock receiver
echo ""
echo "=== 1. Mock receiver ==="
cat > "$TEST_DIR/mock-opik.js" << 'EOF'
const http = require("http");
let traces = 0, spans = 0;
const server = http.createServer((req, res) => {
    let body = "";
    req.on("data", c => body += c);
    req.on("end", () => {
        process.stderr.write(`[mock] ${req.method} ${req.url} (${body.length}b)\n`);
        if (req.url.includes("/traces") && req.method === "POST") traces++;
        if (req.url.includes("/spans") && req.method === "POST") spans++;
        if (req.url === "/status") {
            res.writeHead(200, {"Content-Type": "application/json"});
            res.end(JSON.stringify({traces, spans}));
            return;
        }
        res.writeHead(200, {"Content-Type": "application/json"});
        res.end("{}");
    });
});
server.listen(parseInt(process.env.MOCK_PORT), "127.0.0.1");
EOF
MOCK_PORT=$MOCK_PORT node "$TEST_DIR/mock-opik.js" 2>"$TEST_DIR/mock.log" &
MOCK_PID=$!
sleep 1
echo "Mock on :$MOCK_PORT (PID=$MOCK_PID)"

# 2. Write config (NO plugins section — let plugin install own that)
echo ""
echo "=== 2. Write base config ==="
cat > "$OPENCLAW_STATE_DIR/openclaw.json" << CFGEOF
{
  "agents": {
    "defaults": {
      "model": {"primary": "anthropic/claude-sonnet-4-6"},
      "models": {"anthropic/claude-sonnet-4-6": {}},
      "workspace": "$TEST_DIR/.openclaw/workspace"
    }
  },
  "gateway": {
    "auth": {"mode": "token"},
    "controlUi": {
      "enabled": true,
      "dangerouslyDisableDeviceAuth": true,
      "allowInsecureAuth": true
    },
    "reload": {"mode": "hot"}
  },
  "models": {
    "providers": {
      "anthropic": {
        "apiKey": "$E2E_ANTHROPIC_API_KEY",
        "baseUrl": "https://api.anthropic.com",
        "models": []
      }
    }
  }
}
CFGEOF
echo "Written (no plugins section)"

# 3. Install opik plugin (merges into existing config)
echo ""
echo "=== 3. Install opik plugin ==="
cd /tmp
echo "Installing from: $OPIK_PLUGIN_SOURCE"
OPIK_TGZ=$(npm pack "$OPIK_PLUGIN_SOURCE" --quiet 2>/dev/null | tail -1)
openclaw plugins install "/tmp/$OPIK_TGZ" 2>&1
rm -f "/tmp/$OPIK_TGZ"

# 4. Patch opik config with mock receiver URL
echo ""
echo "=== 4. Patch opik config ==="
python3 << PYEOF
import json
p = "$OPENCLAW_STATE_DIR/openclaw.json"
with open(p) as f:
    cfg = json.load(f)
plugins = cfg.setdefault("plugins", {})
entries = plugins.setdefault("entries", {})
opik = entries.setdefault("opik-openclaw", {})
opik["enabled"] = True
opik.setdefault("config", {}).update({
    "enabled": True,
    "apiUrl": "http://127.0.0.1:$MOCK_PORT",
    "apiKey": "baseline-test-key",
    "projectName": "baseline-test",
    "workspaceName": "default",
    "tags": ["baseline"]
})
allow = plugins.setdefault("allow", [])
if "opik-openclaw" not in allow:
    allow.append("opik-openclaw")
with open(p, "w") as f:
    json.dump(cfg, f, indent=2)
print("Patched apiUrl=http://127.0.0.1:$MOCK_PORT")
PYEOF

echo "Final plugins config:"
python3 -c "import json; print(json.dumps(json.load(open('$OPENCLAW_STATE_DIR/openclaw.json')).get('plugins',{}), indent=2))"

# 5. Verify discovery
echo ""
echo "=== 5. Plugin discovery ==="
openclaw plugins list 2>&1 | grep -i opik || echo "WARNING: opik not found"

# 6. Start gateway
echo ""
echo "=== 6. Start gateway ==="
OPENCLAW_GATEWAY_TOKEN="$GATEWAY_TOKEN" \
OPENCLAW_DISABLE_BONJOUR=1 \
OCM_MACHINE_ID="baseline-test" \
  openclaw gateway --port "$GATEWAY_PORT" --bind loopback --verbose --allow-unconfigured \
  > "$TEST_DIR/gateway.log" 2>&1 &
GATEWAY_PID=$!

for i in $(seq 1 60); do
    curl -sf "http://127.0.0.1:$GATEWAY_PORT/health" > /dev/null 2>&1 && break
    kill -0 "$GATEWAY_PID" 2>/dev/null || { echo "Gateway died"; cat "$TEST_DIR/gateway.log"; exit 1; }
    sleep 1
done
echo "Gateway healthy"

# 7. Check plugin loading in gateway log
echo ""
echo "=== 7. Gateway plugin log ==="
grep -i "plugin\|opik" "$TEST_DIR/gateway.log" | head -10 || echo "(no plugin lines)"

# 8. Send chat
echo ""
echo "=== 8. Chat ==="
OPENCLAW_GATEWAY_TOKEN="$GATEWAY_TOKEN" \
OPENCLAW_GATEWAY_PORT="$GATEWAY_PORT" \
  timeout 30 openclaw agent --session-id "opik-test" --message "Reply with exactly one word: OK" 2>&1 | tail -5 || true

sleep 10

# 9. Results
echo ""
echo "=== 9. Results ==="
RESULT=$(curl -sf "http://127.0.0.1:$MOCK_PORT/status" 2>/dev/null || echo '{"traces":0,"spans":0}')
echo "Mock receiver: $RESULT"

echo ""
echo "Mock log:"
cat "$TEST_DIR/mock.log"

echo ""
echo "Gateway plugin/opik lines:"
grep -i "plugin\|opik" "$TEST_DIR/gateway.log" | head -20 || echo "(none)"

TRACES=$(echo "$RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('traces',0))")
if [ "$TRACES" -gt 0 ]; then
    echo ""
    echo "PASS — traces flowing"
    TEST_PASSED=1
else
    echo ""
    echo "FAIL — no traces"
    echo ""
    echo "Full gateway log:"
    cat "$TEST_DIR/gateway.log"
fi
