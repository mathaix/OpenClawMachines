# Workspace Integrations And Native MCP

Workspace integrations let an OpenClaw machine use external tools without
installing one plugin per provider inside each VM. Connect a tool once at the
workspace level, then every machine in that workspace can discover and call it
through the built-in OCM MCP server.

This covers GitHub, Google Workspace, imported OpenAPI tools, imported GraphQL
tools, and remote MCP endpoints. Provider credentials stay in the control plane,
encrypted at rest, and are never pasted into a machine.

## User Model

- Every account has at least one workspace, usually `default`.
- Each machine belongs to exactly one workspace.
- Integrations are enabled on a workspace, not on an individual machine.
- A running machine receives a native MCP server named `ocm` in its OpenClaw
  config when the control plane can mint the per-machine token.
- The agent sees OCM facade tools rather than direct provider tools.

In the UI, users connect integrations from the workspace's **Integrations** page.
After a machine starts or receives a pushed config, the agent can use the enabled
workspace tools from chat.

## Agent Tool Flow

The native server exposes a small facade:

| Tool | Purpose |
| --- | --- |
| `ocm.search_tools` | Search enabled workspace tools by natural-language intent or keywords. |
| `ocm.describe_tool` | Load the selected tool's full schema and policy metadata. |
| `ocm.call_tool` | Execute the selected tool through OCM policy, logging, and redaction. |

Agents should use this flow:

1. Search with `ocm.search_tools`, usually with `method: "semantic"`.
2. Select one result and keep its `tool_address`.
3. Describe that exact address with `ocm.describe_tool`.
4. Call the same address with `ocm.call_tool` and arguments matching the schema.

Prefer `tool_address` over legacy `tool_id` values. A workspace can connect the
same provider more than once, so direct provider names can be ambiguous.

Example addresses look like:

```text
wi.<workspace_id>.github.github-main.search_code
wi.<workspace_id>.google-workspace.team-drive.list_files
```

## MCP Configuration

Config assembly injects one MCP server into the machine's OpenClaw config:

```text
config.mcp.servers.ocm = {
  transport: "streamable-http",
  url:       "<BACKEND_URL>/api/workspace-integrations/mcp",
  headers:   { Authorization: "Bearer <per-machine workspace-integration JWT>" }
}
```

The token is scoped to the machine and workspace integration gateway. The gateway
resolves tool addresses against the machine's enabled workspace integrations at
call time, so the machine does not need provider credentials in its filesystem.

The MCP server also exposes agent guidance through:

- resource `ocm://workspace-integrations/agent-guidance`
- prompt `ocm_workspace_integrations`

Clients that surface MCP resources or prompts can use those as runtime reminders
of the search, describe, and call flow.

## Policy And Guidance

Each workspace tool can carry policy and guidance:

- `allow`: the tool can be called normally.
- `require_approval`: the agent should pause and ask for human approval before
  the side effect is performed.
- guidance overlays: operator- or user-authored notes shown alongside the tool,
  such as repository naming rules or account-specific safety constraints.

Write-capable tools should be treated as side effects. Agents should confirm
intent when the requested action is ambiguous, destructive, externally visible,
or policy-controlled.

## Operator Requirements

Native MCP needs the standard workspace integration configuration plus a working
token signer:

- `JWT_SECRET`: at least 16 characters, used to mint the per-machine MCP token.
- `SECRET_ENCRYPTION_KEY`: exactly 32 bytes, used for encrypted integration
  credentials.
- `BACKEND_URL`: public control-plane URL used to derive the MCP endpoint.
- `WORKSPACE_INTEGRATIONS_API_URL`: optional override for the MCP token
  audience; leave unset to derive it from `BACKEND_URL`.
- Provider OAuth credentials such as `GOOGLE_WORKSPACE_OAUTH_CLIENT_ID` and
  `GOOGLE_WORKSPACE_OAUTH_CLIENT_SECRET` when enabling OAuth providers.

Keep `OCM_ALLOW_INSECURE_WORKSPACE_INTEGRATIONS` unset outside trusted local
tests. Keep `OCM_WORKSPACE_INTEGRATIONS_EXECUTE_ENABLED` and
`OCM_WORKSPACE_INTEGRATIONS_WORKFLOWS_ENABLED` disabled unless the operator has
explicitly chosen to expose those experimental facade tools.

## Degraded Mode

If `JWT_SECRET` is missing or too short, config assembly skips native MCP
injection instead of blocking machine provisioning. In that mode the machine can
still boot, but the agent will not see the native `ocm.*` tools. Fix the secret,
then push config or restart the machine.

If an integration backend is temporarily unavailable, config assembly degrades to
an empty integration list and the gateway resolves the live tool set later. A
workspace integration outage should not prevent unrelated machines from starting.

## Troubleshooting

| Symptom | Likely cause / fix |
| --- | --- |
| Agent cannot see any `ocm.*` tools | Check `JWT_SECRET` length and confirm the machine received `mcp.servers.ocm` in config. |
| `ocm.search_tools` returns no matches | Confirm the integration is enabled on the machine's workspace and the connected account exposes the expected tool. |
| A `tool_id` call is ambiguous | Use `tool_address` from `ocm.search_tools` instead. |
| Google returns 401 | Reconnect the Google account. |
| Google returns 403 with missing scopes | Reconnect and grant the required Gmail, Drive, Calendar, Sheets, or Docs scope. |
| Google returns 403 with admin policy | Ask a Google Workspace admin to allow the app or API. |
| Google returns 429 | Retry later unless the attempted call was a write operation. |

## Related Docs

- [User guide](user-guide.md#workspace-integrations-external-tools)
- [Architecture: workspace integrations and native MCP](architecture.md#workspace-integrations--native-mcp)
- [Self-hosted control plane prerequisites](self-hosted-control-plane.md#ocm-secrets-and-config)
