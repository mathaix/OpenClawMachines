# OCM Integrations Plugin

Legacy workspace-scoped OpenClaw plugin for OCM integrations.

The product runtime path for workspace integrations is the native
`mcp.servers.ocm` server assembled into machine config. This package remains as
a short-lived compatibility/debug fallback and as a home for the bundled
`ocm-integrations` skill while runtime packaging is being removed.

Production config assembly should not inject `apiUrl`, `toolsUrl`,
`manifestUrl`, or `machineToken` for normal workspace integration delivery. If
the legacy plugin is intentionally enabled for debugging, it sends the machine
token as `Authorization: Bearer` on backend calls; the backend derives machine
and workspace scope from the token.

The canonical agent flow is:

1. Call `ocm.search_tools` with `method: "semantic"` to find candidate
   workspace tools from the user's intent.
2. Call `ocm.describe_tool` with the selected `tool_address` to load the
   selected tool schema. Use `tool_id` only when the search result is
   unambiguous.
3. Call `ocm.call_tool` with the same `tool_address` to execute the selected tool
   through OCM policy, logging, and redaction.

Agents should learn the OCM discovery facade, not direct provider tool names. If
the facade tools are missing in a session, treat that as a runtime/config issue
and verify native `mcp.servers.ocm` delivery before relying on this fallback.

Semantic search expands common provider, object, and action terms. For example,
"list inbox messages" can find Gmail message-list tools, "schedule meetings" can
find Calendar event tools, and "send email" can find write-capable Gmail tools
when policy allows them.

Search results may include `policy_state: "require_approval"`. Agents should
surface that approval requirement instead of attempting the call.
