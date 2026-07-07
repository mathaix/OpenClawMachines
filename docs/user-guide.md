# User Guide — Using an OpenClaw Machine

This guide is for **using** a machine on an OpenClaw Machines deployment someone
has already set up. If you're standing up the platform itself, start with
[getting-started.md](getting-started.md).

A **machine** is one OpenClaw agent running in its own Firecracker microVM. You
create it from the dashboard, give it a model, and then work with it through a
web workspace — chat, a live terminal, an optional paired browser, files, logs,
traces, and backups.

---

## Create a machine

1. Open the dashboard and log in. (On an evaluation/`dev`-auth deployment you're
   logged in automatically.) First login creates your user and a personal
   account.
2. **New Machine** → give it a name and pick a size (Basic / Standard / Pro) →
   **Create**. The control plane places it on a ready host and the machine page
   opens in the `stopped` state.
3. **Start** it. It moves through `provisioning → running`; when it's `running`
   the **Workspace** and **Chat** buttons go live.

The machine page has a row of tabs — **Overview, Resources, Traces, Model, Web
Search, Channels, Integrations, Browser, Files, Logs, Backups** — plus **Chat**,
**Workspace**, and **Stop** in the header. The Overview tab's **Setup** cards
(Choose AI Model, Connect Channels, Add Integrations, Enable Browser) are
shortcuts to the same things covered below.

---

## Give the agent a model (required for chat)

A fresh machine has no model provider, so chat will fail with
`no API key configured for provider …` until you add one.

1. Open the **Model** tab.
2. Under **bring-your-own-key**, pick a provider (Anthropic, OpenAI, Google,
   OpenRouter), paste your API key, and connect it. The key is stored encrypted
   and scoped to this machine.
3. Choose a default model from that provider and save. The change is pushed to
   the running machine live; a restart is only needed if the push reports it
   couldn't apply.

You can set **fallback** models too — if the primary is unavailable the agent
tries the fallbacks in order.

> **Model IDs must be current.** Providers retire model IDs; if you pin a
> retired one the runtime rejects it at config validation. Pick from the Model
> tab's list rather than typing an ID.

---

## Workspace integrations (external tools)

Integrations let the agent use external services — **GitHub**, **Google
Workspace**, or any endpoint you import from an **OpenAPI**, **GraphQL**, or
**remote-MCP** spec — without you wiring anything into the VM. They are
**workspace-scoped**: connect a tool once and it's available to every machine in
that workspace (each account starts with a `default` workspace).

**Connect one** from the workspace's **Integrations** view: pick a provider (or
paste an OpenAPI/GraphQL/MCP endpoint to import), complete the OAuth consent for
providers that need it, and enable it. Credentials are stored encrypted; you
never paste them into a machine.

**How the agent uses them.** Rather than a separate tool per integration, each
machine's agent gets one built-in tool server and three verbs:

- `search_tools` — find a tool by intent ("create a GitHub issue").
- `describe_tool` — load its exact input schema.
- `call_tool` — run it by address.

So you enable integrations at the workspace level and the agent discovers and
calls them on its own during chat. You can set a tool's policy to **require
approval** so a call pauses for your confirmation, and attach **guidance** notes
that the agent sees alongside a tool.

> If integrations don't appear for a machine, confirm the machine's workspace
> has them **enabled**, and that the control plane has a `JWT_SECRET` of at least
> 16 characters — without it the machine falls back to no native tool server (see
> Troubleshooting).

---

## Web chat

**Chat** (header button, or the sidebar in the chat view) opens a conversation
with the agent. The composer shows the active model and a live context-usage
meter. The agent can use tools — when it runs shell commands, browses, etc., you
see an **Activity** entry (e.g. "2 tools · Exec") above its reply.

The chat sidebar also exposes the agent's own surfaces: **Sessions**, **Usage**,
**Agents**, **Skills**, **Cron Jobs**, and more — these are the in-VM OpenClaw
runtime's features, scoped to this machine.

---

## Live terminal

**Workspace** opens the terminal view. **Shell** is an interactive shell inside
the VM; **TUI** launches the agent's terminal UI. Sessions persist across
reconnects — closing the tab and returning re-attaches to the same shell. The
**LOGS** panel below streams the in-VM gateway logs live.

The **Gateway** status chip (top right) shows the in-VM agent gateway health;
**Restart Gateway** bounces it without restarting the whole machine.

---

## Browser VM

Open the **Browser** tab (or the Overview "Enable Browser" card) to pair a
separate, account-scoped microVM running headful Chromium. The agent drives it
over CDP while you watch the **live view**. Browser VMs have their own lifecycle,
so one can be created ahead of time and reused across machine restarts.

---

## Files

The **Files** tab is a full file browser (File Browser) into the VM's guest
filesystem — navigate, view, upload, download, create folders. It's the same
filesystem the agent's shell and tools operate on.

---

## Logs

The **Logs** tab streams the machine's gateway/plugin/runtime logs (server-sent
events). Use it to watch what the agent is doing, or to diagnose a model/provider
error (e.g. a `no API key configured` line points you back to the Model tab).

---

## Traces

The **Traces** tab shows Opik traces for agent runs on this machine — each trace
has its input, output, token counts, and a span tree you can expand. Select a
trace to inspect its spans. Traces populate a few seconds after an agent task
runs.

> On a pure-local (Stage-1) evaluation deployment, Traces stay empty unless the
> operator configured a trace endpoint (`OPIK_API_URL`). They work out of the box
> on a Stage-2 (Cloudflare) deployment.

---

## Resources

The **Resources** tab shows live CPU and memory for the machine — a **Live** view
with the last few minutes charted, and a **History** view.

> Live metrics are sampled from each VM's systemd cgroup, so they appear only when
> the host runs the default `systemd-unit` runtime owner. The simplified local
> evaluation script uses the `direct` owner, which leaves these charts empty.

---

## Backups

Per-machine backups are built in. From the **Backups** tab (or the API) you can
**create**, **restore**, **download**, and **delete** backups. Retention is
enforced by the server.

---

## Lifecycle

From the machine page (or the API) you can **Start / Stop / Restart / Destroy**:

- **Stop** frees host capacity but keeps the machine's disk and config; **Start**
  brings it back where it was.
- **Restart** reboots the microVM, reusing its existing on-disk config.
- **Destroy** releases the machine's resources and its route.

Where a running machine is reachable depends on the deployment: a Stage-1 local
setup serves it through the control plane; a Stage-2 Cloudflare deployment gives
it `m-<name>.your-domain.com` behind edge auth.

---

## Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| Chat replies `no API key configured for provider …` | No model credential — add one on the **Model** tab. |
| A model save fails with "Unknown model … Use …" | The pinned model ID was retired; pick a current one from the Model tab. |
| Terminal stuck on **Connecting** | The gateway isn't ready yet, or was just restarted — wait, or use **Restart Gateway**. If it persists, check the **Logs** tab. |
| **Traces** empty after running a task | On a local deployment, tracing needs the operator to set `OPIK_API_URL`. On Stage-2 it should populate within ~10s. |
| **Resources** charts show "Waiting for first sample…" | The host is running the `direct` runtime owner (local eval); live metrics need the `systemd-unit` owner. |
| Machine won't leave **provisioning** / returns to `stopped` | The VM failed to boot — the operator should check the host agent logs (`journalctl -u ocm-agent`). |
| Agent has no workspace tools even though integrations are enabled | The control plane's `JWT_SECRET` is unset or shorter than 16 characters, so it can't mint the per-machine tool-server token — the machine still boots, but with no native MCP server. The operator should set a proper `JWT_SECRET` and re-push config. |

For anything host- or platform-level, see
[getting-started.md](getting-started.md) and
[architecture.md](architecture.md).
