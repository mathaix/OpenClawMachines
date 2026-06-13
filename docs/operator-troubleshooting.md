# Operator Troubleshooting Runbook

This runbook covers day-to-day recovery for an operator who already has an
OCM control plane, at least one host, and one or more OpenClaw machines. It is
based on real operator failure modes: stale CLI sessions, confusing command
entry points, SSH reachability, gateway auth mismatches, chat-channel noise,
and agent runtime recovery.

Use it before changing database rows, replacing config wholesale, or restarting
platform services by hand.

## First: identify the failing layer

Most incidents are easier once you decide which layer is failing.

| Symptom | Likely layer | First check |
|---|---|---|
| `session expired` from `ocm` | Local CLI auth | `ocm doctor` and `ocm login` |
| `unknown command "list" for "ocm"` | CLI command shape | Use `ocm machines list`, not `ocm list` |
| SSH cannot connect to a running machine | OCM data plane or tunnel | `ocm machines ssh-debug <name>` |
| Browser workspace opens but chat fails | In-VM OpenClaw gateway | Machine logs, then gateway health |
| Slack pairing code is rejected | OpenClaw runtime pairing state | List pending requests on the machine |
| Slack receives tool/progress text | Chat transport/runtime streaming | Disable progress streaming or update channel config |
| Websocket closes with unauthorized / 1008 | Control UI token or gateway auth | Refresh session, then compare frontend/backend token domains |
| A local shell/tool call says native hook relay unavailable | Runtime hook bridge | Restart/recover the OpenClaw gateway, not the whole OCM host |

## Local CLI sanity check

Start outside the machine. Confirm the local CLI is authenticated and that you
are using the nested command groups:

```bash
ocm doctor
ocm machines list
ocm machines get "<machine-name>"
```

If the CLI says the session expired, re-authenticate before debugging the
remote host:

```bash
ocm login
# or, for non-interactive environments:
ocm login --token "$OCM_TOKEN"
```

Do not assume a failed SSH hop means the VM is broken. An expired local CLI
session fails before OCM reaches the control plane or machine.

## SSH into a machine

Use the machine command group explicitly:

```bash
ocm machines ssh "<machine-name>"
```

Pass raw SSH options after `--`:

```bash
ocm machines ssh "<machine-name>" -- -L 8080:localhost:8080
```

If SSH fails, run the debug command before changing routes or tunnels:

```bash
ocm machines ssh-debug "<machine-name>"
```

Collect the machine state at the same time:

```bash
ocm machines get "<machine-name>" --json
ocm machines logs "<machine-name>" --lines 200
```

## Pairing chat channels

Pairing codes are short-lived and bound to pending requests inside the
OpenClaw runtime. If a code fails with "no pending pairing request", do not
keep retrying the same code. SSH into the machine and list the current
requests:

```bash
openclaw pairing list --channel slack
```

Approve the current pending request, not the stale code copied earlier:

```bash
openclaw pairing approve <code>
```

If there are no pending requests, start a new pairing flow from the channel or
app that requested it.

## Recover the in-VM OpenClaw gateway

The OCM machine can be healthy while the OpenClaw runtime inside it is not.
Examples include:

- chat fails but SSH works;
- the control UI websocket is rejected as unauthorized;
- local tool execution is blocked by the runtime hook bridge;
- a channel integration is connected but every request returns a generic
  application error.

Prefer the runtime's supported gateway restart path when available:

```bash
curl -sf -X POST http://127.0.0.1:7681/restart-gateway
```

Then re-check the gateway log and send one small smoke request through the
same path that failed. Avoid killing platform daemons unless the supported
restart endpoint is unavailable and you understand the supervisor that will
restart them.

## Manage long-running helper processes

If you install a helper daemon inside a machine, make it an explicit service
instead of relying on an interactive shell. A common failure pattern is a
daemon that works during setup, then disappears after reboot because no
supervisor owns it.

Operator checklist:

1. Confirm the command works from an SSH shell.
2. Confirm the environment it needs (`HOME`, config directories, tokens) is
   present in the service context.
3. Run it under the machine's supported service manager.
4. Reboot or restart the machine and confirm it comes back without a human
   shell.

## Avoid leaking internal work into Slack

Slack or other chat channels should receive final user-facing replies, not raw
tool calls, shell commands, stack traces, or progress updates. If a channel is
showing internal runtime events:

1. Stop the channel noise first, if needed, by disabling or removing the bot
   from the noisy channel.
2. Check channel streaming/progress settings in the OpenClaw runtime config.
3. Add agent instructions that forbid Slack-visible tool/progress narration.
4. Treat continued leaks as a transport/runtime bug, not only a prompt issue.

For operational automations, route failures separately from summaries. A good
default is:

- errors, access issues, monitor failures, and requests for help go to a
  private operator alert;
- useful summaries go to the team channel;
- "nothing new" produces no channel message.

## Edit config safely

When a config view redacts tokens, do not push the redacted output back as live
config. That can replace real secrets with placeholders.

Safer pattern:

1. Read the specific live file or API field that needs to change.
2. Back it up.
3. Patch only the specific key.
4. Restart or reload only the service that consumes it.
5. Smoke test the exact session, channel, or machine path that failed.

For OpenClaw model/session issues, check both static config and persisted
session metadata. A static default may be correct while older sessions still
carry stale provider or model values.

## What to include in a bug report

When opening an OCM issue, include:

- OCM CLI version and `ocm doctor` result;
- control plane profile (`local`, `operator`, or `hosted`);
- whether the machine is reachable by `ocm machines ssh`;
- the failing command and the first non-secret error line;
- machine state from `ocm machines get <name> --json`;
- relevant gateway or agent log lines with tokens, domains, and customer data
  redacted;
- whether a supported gateway restart fixed it temporarily.

Do not include authentication tokens, machine secrets, customer data, raw
channel transcripts, or full redacted config dumps that might be copied back
into production by mistake.
