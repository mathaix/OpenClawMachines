---
name: ocm-integrations
description: Use OCM workspace integrations exposed by the native `mcp.servers.ocm` MCP server. Trigger when a user asks to use connected workspace apps or external SaaS tools such as Google Workspace, GitHub, Jira, Linear, Slack, Notion, custom REST APIs, OpenAPI tools, or remote MCP servers through OCM.
---

# ocm-integrations

OCM workspace integrations are configured at the workspace level and surfaced
inside the machine by the native `mcp.servers.ocm` MCP server. The backend keeps
upstream credentials server-side; the machine only receives a per-machine token
and policy-filtered OCM facade tools.

## Required workflow

1. Search first with `ocm.search_tools` when that tool is available. Use
   `method: "semantic"` unless the user asks for an exact tool name. Search with
   the user's intent, not guessed provider function names.
2. If the provider or access level is known, include `integration` and `access`
   filters to keep the result set small. Use `access: "read"` for lookups and
   `access: "write"` for create, send, update, upload, or delete actions.
3. Describe exactly one selected result with `ocm.describe_tool` before calling
   it. Use the `tool_address` returned by search.
4. Execute with `ocm.call_tool` using that same `tool_address` and arguments
   that match the described schema.

Prefer the OCM search, describe, and call flow because it keeps tool discovery
compact and routes execution through OCM policy, logging, and redaction.

## MCP guidance surface

When the MCP client exposes resources or prompts, the same guidance is available
from resource `ocm://workspace-integrations/agent-guidance` and prompt
`ocm_workspace_integrations`. Treat those as runtime reminders of this skill;
they do not replace the required search, describe, and call workflow.

## Addresses and policy

Search results include legacy `tool_id` values such as `github.search_code` and
structured `tool_address` values such as
`wi.<workspace_id>.github.github-main.search_code`. Use `tool_address` because it
distinguishes multiple connections for the same app, such as two Gmail accounts
or two GitHub repos. Use `tool_id` only when there is no `tool_address` and the
tool is clearly unambiguous. If a result has `policy_state: "require_approval"`,
report that the tool needs human approval instead of trying to call it.

## Semantic discovery

`ocm.search_tools` accepts natural language queries and expands common provider,
object, and action terms. Examples:

- "list inbox messages" can find Gmail message-list tools.
- "schedule meetings" can find calendar event tools.
- "send email" can find write-capable Gmail send tools when allowed.
- "create ticket" can find Jira or Linear issue tools when those integrations
  are connected.

If semantic search returns no useful result, retry once with a simpler query:
provider name plus object plus action, such as "gmail list messages" or
"github search code". Use `method: "keyword"` only for exact IDs or literal
metadata matching.

## If tools are missing

- If no `ocm.*` tools are visible, say that OCM integrations are not loaded in
  this session and ask the user to verify `mcp.servers.ocm` is configured and
  the gateway has restarted.
- If `ocm.search_tools` returns no matches, say that the workspace integration
  is not connected, not authorized, or not allowed by policy.
- Do not invent provider-specific tool names or call provider tools directly by
  name. Use `ocm.call_tool` with a `tool_address` returned by
  `ocm.search_tools`.

## Google Workspace recovery

Google Workspace is service-specific. Treat Gmail, Google Drive, and Google
Calendar as separate connected services, even if they share one Google OAuth
account.

When a Google tool is missing or returns an auth error, explain the likely user
action:

- Gmail not visible or 403: reconnect Google and grant Gmail scopes, or ask a
  Google Workspace admin if org policy blocks Gmail API access.
- Drive not visible or 403: reconnect Google and grant Drive scopes, or ask an
  admin if Drive API access is blocked.
- Calendar not visible or 403: reconnect Google and grant Calendar scopes, or
  ask an admin if Calendar API access is blocked.
- 401/token revoked: reconnect the Google account.
- Rate limited: tell the user to retry later.

Do not ask the user for raw OAuth tokens or client secrets in chat. OCM stores
credentials server-side.

## Multi-step work

For one-off single actions, use the required search, describe, call flow.
If `ocm.execute` is visible, use it only for one-off multi-step work that needs
local filtering, joining, pagination, or batching; do not use it for a single
tool call. If saved workflow tools are visible, use them only for repeated, scheduled, shared, durable, or approval-heavy work.
Do not claim execute or workflow support unless those tools are visible in the
current session.

## Safety

- Do not reveal backend URLs, machine tokens, OAuth tokens, API keys, or
  integration credentials.
- Treat write-capable tools as policy-controlled side effects. Confirm intent
  when the user asks for destructive or externally visible actions.
