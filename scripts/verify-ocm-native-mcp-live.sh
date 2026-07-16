#!/usr/bin/env bash
# Verify the live native MCP workspace integrations surface from inside an
# OpenClaw machine. This captures the Phase 1 evidence required by
# docs/plans/2026-06-24-workspace-integrations-incremental-discovery-and-code-mode.md.
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage:
  WORKSPACE_INTEGRATIONS_SEARCH_QUERY="echo stable" \
  WORKSPACE_INTEGRATIONS_CALL_ARGS='{"message":"stable"}' \
  scripts/verify-ocm-native-mcp-live.sh

Run this inside a fresh or refreshed OpenClaw machine that has workspace
integrations enabled through mcp.servers.ocm.

Environment:
  WORKSPACE_INTEGRATIONS_SEARCH_QUERY
      Search query for the deterministic test tool. Default: "test mcp echo".
  WORKSPACE_INTEGRATIONS_SEARCH_INTEGRATION
      Optional integration/source filter passed to ocm.search_tools.
  WORKSPACE_INTEGRATIONS_EXPECTED_TOOL_ADDRESS
      Optional exact tool_address that search must return/select.
  WORKSPACE_INTEGRATIONS_CALL_ARGS
      JSON arguments for the selected tool. Default: {}.
  WORKSPACE_INTEGRATIONS_DENIED_TOOL_ADDRESS
      Optional denied/disabled tool_address to prove policy failure.
  WORKSPACE_INTEGRATIONS_DENIED_TOOL_ID
      Optional denied/disabled legacy tool_id to prove policy failure.
  WORKSPACE_INTEGRATIONS_DENIED_ARGS
      JSON arguments for the denied call. Default: {}.
  WORKSPACE_INTEGRATIONS_AMBIGUOUS_TOOL_ID
      Optional legacy tool_id expected to fail with ambiguity before dispatch.
  WORKSPACE_INTEGRATIONS_MCP_URL
      Optional override when openclaw config output redacts or omits the URL.
  WORKSPACE_INTEGRATIONS_MCP_AUTHORIZATION
      Optional "Bearer ..." override when openclaw config output redacts auth.
  WORKSPACE_INTEGRATIONS_ALLOWED_EXTRA_TOOLS
      Comma-separated extra native MCP tools allowed in tools/list, for future
      feature-flagged tools such as ocm.execute. Default: none.
  WORKSPACE_INTEGRATIONS_LIVE_STRICT
      Set to 1 to fail when denied or ambiguous checks are not configured.
USAGE
}

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

extract_json_object() {
  sed -n '/^[[:space:]]*{/,$p'
}

redact_authorization() {
  jq 'if .headers.Authorization then .headers.Authorization = "Bearer __redacted__" else . end'
}

require_json() {
  local value="$1"
  local label="$2"
  printf '%s' "$value" | jq -e . >/dev/null || fail "$label must be valid JSON"
}

mcp_request() {
  local id="$1"
  local method="$2"
  local params="$3"
  jq -nc \
    --argjson id "$id" \
    --arg method "$method" \
    --argjson params "$params" \
    '{jsonrpc:"2.0", id:$id, method:$method, params:$params}'
}

mcp_call() {
  local payload="$1"
  curl -fsS \
    --max-time "${CURL_MAX_TIME:-20}" \
    -H 'Accept: application/json, text/event-stream' \
    -H 'Content-Type: application/json' \
    -H "Authorization: ${mcp_authorization}" \
    --data "$payload" \
    "$mcp_url"
}

assert_mcp_success() {
  local response="$1"
  local label="$2"
  if printf '%s' "$response" | jq -e '.error != null' >/dev/null; then
    printf '%s\n' "$response" | jq . >&2 || true
    fail "$label returned JSON-RPC error"
  fi
}

is_allowed_tool() {
  local name="$1"
  case "$name" in
    ocm.search_tools|ocm.describe_tool|ocm.call_tool)
      return 0
      ;;
  esac
  local extra
  IFS=',' read -ra extras <<< "${WORKSPACE_INTEGRATIONS_ALLOWED_EXTRA_TOOLS:-}"
  for extra in "${extras[@]}"; do
    extra="${extra//[[:space:]]/}"
    if [[ -n "$extra" && "$name" == "$extra" ]]; then
      return 0
    fi
  done
  return 1
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ $# -ne 0 ]]; then
  usage
  exit 2
fi

need openclaw
need curl
need jq
need sed

search_query="${WORKSPACE_INTEGRATIONS_SEARCH_QUERY:-test mcp echo}"
call_args="${WORKSPACE_INTEGRATIONS_CALL_ARGS:-{}}"
denied_args="${WORKSPACE_INTEGRATIONS_DENIED_ARGS:-{}}"
strict="${WORKSPACE_INTEGRATIONS_LIVE_STRICT:-0}"

require_json "$call_args" "WORKSPACE_INTEGRATIONS_CALL_ARGS"
require_json "$denied_args" "WORKSPACE_INTEGRATIONS_DENIED_ARGS"

echo "==> Reading native MCP config"
mcp_config_raw="$(openclaw config get mcp.servers.ocm 2>&1)" || {
  printf '%s\n' "$mcp_config_raw" >&2
  fail "openclaw config get mcp.servers.ocm failed"
}
mcp_config_json="$(printf '%s\n' "$mcp_config_raw" | extract_json_object)"
if [[ -z "$mcp_config_json" ]]; then
  printf '%s\n' "$mcp_config_raw" >&2
  fail "could not parse mcp.servers.ocm JSON from openclaw output"
fi

transport="$(printf '%s' "$mcp_config_json" | jq -r '.transport // ""')"
if [[ "$transport" != "streamable-http" ]]; then
  fail "mcp.servers.ocm.transport expected streamable-http, got ${transport:-<empty>}"
fi

mcp_url="${WORKSPACE_INTEGRATIONS_MCP_URL:-$(printf '%s' "$mcp_config_json" | jq -r '.url // ""')}"
mcp_authorization="${WORKSPACE_INTEGRATIONS_MCP_AUTHORIZATION:-$(printf '%s' "$mcp_config_json" | jq -r '.headers.Authorization // ""')}"

case "$mcp_url" in
  https://*/api/workspace-integrations/mcp|http://*/api/workspace-integrations/mcp) ;;
  *) fail "mcp.servers.ocm.url must point at /api/workspace-integrations/mcp, got ${mcp_url:-<empty>}" ;;
esac

case "$mcp_authorization" in
  "Bearer "__OPENCLAW_REDACTED__*|"Bearer "__redacted__*|"Bearer REDACTED"*|"")
    fail "mcp.servers.ocm Authorization is redacted or missing; set WORKSPACE_INTEGRATIONS_MCP_AUTHORIZATION='Bearer ...' for live smoke"
    ;;
  "Bearer "*) ;;
  *) fail "mcp.servers.ocm Authorization must be a Bearer token" ;;
esac

printf '%s\n' "$mcp_config_json" | redact_authorization

echo
echo "==> Checking legacy REST plugin runtime config"
plugin_config_raw="$(openclaw config get plugins.entries.ocm-integrations 2>&1 || true)"
if printf '%s\n' "$plugin_config_raw" | grep -Fq 'Config path not found'; then
  echo "plugins.entries.ocm-integrations: absent"
else
  plugin_config_json="$(printf '%s\n' "$plugin_config_raw" | extract_json_object)"
  if [[ -z "$plugin_config_json" ]]; then
    printf '%s\n' "$plugin_config_raw" >&2
    fail "could not parse plugins.entries.ocm-integrations output"
  fi
  if ! printf '%s' "$plugin_config_json" | jq -e '(.enabled == false) or (.config.enabled == false)' >/dev/null; then
    printf '%s\n' "$plugin_config_json" | jq . >&2 || true
    fail "legacy ocm-integrations REST plugin runtime is still enabled"
  fi
  printf '%s\n' "$plugin_config_json" | jq 'del(.config.machineToken)'
fi

echo
echo "==> Initializing native MCP server"
initialize_params='{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"ocm-native-mcp-live-smoke","version":"0.1.0"}}'
initialize_response="$(mcp_call "$(mcp_request 1 initialize "$initialize_params")")"
assert_mcp_success "$initialize_response" "initialize"
printf '%s\n' "$initialize_response" | jq '{protocolVersion:.result.protocolVersion, capabilities:.result.capabilities, serverInfo:.result.serverInfo}'

notification_payload='{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'
curl -fsS \
  --max-time "${CURL_MAX_TIME:-20}" \
  -o /dev/null \
  -H 'Accept: application/json, text/event-stream' \
  -H 'Content-Type: application/json' \
  -H "Authorization: ${mcp_authorization}" \
  --data "$notification_payload" \
  "$mcp_url"

echo
echo "==> Checking facade-only tools/list"
tools_response="$(mcp_call "$(mcp_request 2 tools/list '{}')")"
assert_mcp_success "$tools_response" "tools/list"
tool_names="$(printf '%s' "$tools_response" | jq -r '.result.tools[].name')"
printf '%s\n' "$tool_names"

for required in ocm.search_tools ocm.describe_tool ocm.call_tool; do
  if ! printf '%s\n' "$tool_names" | grep -Fxq "$required"; then
    fail "tools/list missing $required"
  fi
done

while IFS= read -r tool_name; do
  [[ -z "$tool_name" ]] && continue
  if ! is_allowed_tool "$tool_name"; then
    fail "tools/list exposed non-facade tool $tool_name"
  fi
done <<< "$tool_names"

echo
echo "==> Checking skill or MCP guidance"
skill_path="${OPENCLAW_WORKSPACE:-$HOME/.openclaw/workspace}/skills/ocm-integrations/SKILL.md"
if [[ -f "$skill_path" ]] && grep -Fq 'ocm.search_tools' "$skill_path" && grep -Fq 'tool_address' "$skill_path"; then
  echo "skill: $skill_path"
else
  resources_response="$(mcp_call "$(mcp_request 3 resources/list '{}')")"
  assert_mcp_success "$resources_response" "resources/list"
  guidance_uri="$(printf '%s' "$resources_response" | jq -r '.result.resources[]? | select(.uri == "ocm://workspace-integrations/agent-guidance") | .uri' | head -n 1)"
  if [[ -z "$guidance_uri" ]]; then
    fail "no ocm-integrations skill found and MCP guidance resource is missing"
  fi
  read_params="$(jq -nc --arg uri "$guidance_uri" '{uri:$uri}')"
  read_response="$(mcp_call "$(mcp_request 4 resources/read "$read_params")")"
  assert_mcp_success "$read_response" "resources/read"
  if ! printf '%s' "$read_response" | jq -e '.result.contents[].text | contains("ocm.search_tools") and contains("tool_address")' >/dev/null; then
    fail "MCP guidance resource does not teach search_tools and tool_address"
  fi
  echo "guidance_resource: $guidance_uri"
fi

echo
echo "==> Searching deterministic tool"
search_args="$(jq -nc \
  --arg query "$search_query" \
  --arg integration "${WORKSPACE_INTEGRATIONS_SEARCH_INTEGRATION:-}" \
  '{query:$query, method:"semantic", limit:10} + (if $integration != "" then {integration:$integration} else {} end)')"
search_params="$(jq -nc --arg name 'ocm.search_tools' --argjson arguments "$search_args" '{name:$name, arguments:$arguments}')"
search_response="$(mcp_call "$(mcp_request 5 tools/call "$search_params")")"
assert_mcp_success "$search_response" "ocm.search_tools"
selected_tool="$(printf '%s' "$search_response" | jq -c --arg expected "${WORKSPACE_INTEGRATIONS_EXPECTED_TOOL_ADDRESS:-}" '
  .result.structuredContent.items as $items
  | if $expected != "" then
      [$items[] | select(.tool_address == $expected)][0]
    else
      $items[0]
    end
')"
if [[ -z "$selected_tool" || "$selected_tool" == "null" ]]; then
  printf '%s\n' "$search_response" | jq . >&2 || true
  fail "search did not return a selectable tool"
fi
tool_address="$(printf '%s' "$selected_tool" | jq -r '.tool_address // ""')"
if [[ -z "$tool_address" || "$tool_address" == "null" ]]; then
  printf '%s\n' "$selected_tool" | jq . >&2 || true
  fail "selected search result did not include tool_address"
fi
printf '%s\n' "$selected_tool" | jq '{tool_id, tool_address, integration_slug, integration_name, connection_slug, connection_label, name, access, policy, source, score}'

echo
echo "==> Describing selected tool_address"
describe_args="$(jq -nc --arg tool_address "$tool_address" '{tool_address:$tool_address}')"
describe_params="$(jq -nc --arg name 'ocm.describe_tool' --argjson arguments "$describe_args" '{name:$name, arguments:$arguments}')"
describe_response="$(mcp_call "$(mcp_request 6 tools/call "$describe_params")")"
assert_mcp_success "$describe_response" "ocm.describe_tool"
printf '%s\n' "$describe_response" | jq '.result.structuredContent | {tool_id, tool_address, integration_slug, connection_slug, connection_label, name, access, policy, auth_state, source, input_schema}'

echo
echo "==> Calling selected tool_address"
call_args_object="$(jq -nc --arg tool_address "$tool_address" --argjson arguments "$call_args" '{tool_address:$tool_address, arguments:$arguments}')"
call_params="$(jq -nc --arg name 'ocm.call_tool' --argjson arguments "$call_args_object" '{name:$name, arguments:$arguments}')"
call_response="$(mcp_call "$(mcp_request 7 tools/call "$call_params")")"
assert_mcp_success "$call_response" "ocm.call_tool"
printf '%s\n' "$call_response" | jq '.result.structuredContent'

echo
echo "==> Checking denied or disabled tool behavior"
if [[ -n "${WORKSPACE_INTEGRATIONS_DENIED_TOOL_ADDRESS:-}" || -n "${WORKSPACE_INTEGRATIONS_DENIED_TOOL_ID:-}" ]]; then
  denied_selector="$(jq -nc \
    --arg tool_address "${WORKSPACE_INTEGRATIONS_DENIED_TOOL_ADDRESS:-}" \
    --arg tool_id "${WORKSPACE_INTEGRATIONS_DENIED_TOOL_ID:-}" \
    --argjson arguments "$denied_args" \
    '(if $tool_address != "" then {tool_address:$tool_address} else {tool_id:$tool_id} end) + {arguments:$arguments}')"
  denied_params="$(jq -nc --arg name 'ocm.call_tool' --argjson arguments "$denied_selector" '{name:$name, arguments:$arguments}')"
  set +e
  denied_response="$(mcp_call "$(mcp_request 8 tools/call "$denied_params")" 2>&1)"
  denied_status=$?
  set -e
  if [[ $denied_status -ne 0 ]]; then
    printf '%s\n' "$denied_response"
  elif printf '%s' "$denied_response" | jq -e '.error != null' >/dev/null; then
    printf '%s\n' "$denied_response" | jq '.error'
  else
    printf '%s\n' "$denied_response" | jq . >&2 || true
    fail "denied/disabled tool call unexpectedly succeeded"
  fi
else
  echo "not verified: set WORKSPACE_INTEGRATIONS_DENIED_TOOL_ADDRESS or WORKSPACE_INTEGRATIONS_DENIED_TOOL_ID"
  [[ "$strict" == "1" ]] && fail "strict mode requires denied/disabled tool evidence"
fi

echo
echo "==> Checking ambiguous legacy tool_id behavior"
if [[ -n "${WORKSPACE_INTEGRATIONS_AMBIGUOUS_TOOL_ID:-}" ]]; then
  ambiguous_args="$(jq -nc --arg tool_id "$WORKSPACE_INTEGRATIONS_AMBIGUOUS_TOOL_ID" '{tool_id:$tool_id}')"
  ambiguous_params="$(jq -nc --arg name 'ocm.describe_tool' --argjson arguments "$ambiguous_args" '{name:$name, arguments:$arguments}')"
  ambiguous_response="$(mcp_call "$(mcp_request 9 tools/call "$ambiguous_params")")"
  if ! printf '%s' "$ambiguous_response" | jq -e '.error != null' >/dev/null; then
    printf '%s\n' "$ambiguous_response" | jq . >&2 || true
    fail "ambiguous legacy tool_id describe unexpectedly succeeded"
  fi
  printf '%s\n' "$ambiguous_response" | jq '.error'
else
  echo "not verified: set WORKSPACE_INTEGRATIONS_AMBIGUOUS_TOOL_ID when fixture data supports duplicate tool_id"
  [[ "$strict" == "1" ]] && fail "strict mode requires ambiguous tool_id evidence"
fi

echo
echo "OK: native MCP workspace integrations live smoke completed"
