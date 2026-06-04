# WebSocket Keep-Alive and Exec Preflight Reliability

**Date:** 2026-06-01 (revised after Codex review + native-hook-relay failure report)
**Status:** Proposed
**Branch:** `hermes2`
**Goal:** Stop long-running sub-agent invocations and PreToolUse hook calls from silently failing mid-session, and stop the openclaw exec preflight from rejecting safe `cd … && node X.js` commands.

## Background

Two failure modes are degrading routines that run inside OpenClaw Machines VMs. Both surfaced during the same investigation on Jordan's machine (`b2b56ac5-89c5-4ad8-b1e3-375875adc789`, 2026-06-01) and are documented together because they share an audience: anyone debugging "the routine ran but produced no result."

### User-visible impact

- Pipeline-fill (5/25–5/27) and the news-watcher fork of morning-briefing (6/01) repeatedly lost sub-agent invocations mid-run, falling back to degraded direct shell calls. The paid Apollo subscription (10k credits/month) went unused for ~1 week because the sub-agent that wraps the Apollo call could not stay alive long enough to return.
- A separate in-VM agent surface reports `PreToolUse hook error: Native hook relay unavailable` on `exec_command` and `apply_patch`. Once it fires, *every* tool call in that session is rejected upfront — the whole tool surface goes down until the agent reconnects. This is the same WS transport failure with a different surface: the hook bridge dispatches over `callGateway` (gateway WebSocket), and when that WS is broken the hook fails closed.
- Coordinator routines see no error on sub-agent drops; the gateway synthesises a `stop` with zero payloads, so the parent treats the run as "completed with empty output" and either silently degrades or hands an empty result to a downstream tool.
- A separate class of routines hits an opaque preflight error (`exec preflight: complex interpreter invocation detected; refusing to run`) on perfectly safe `cd <workspace> && node bin/<script>.js` invocations — the most common shape LLMs reach for.

Net effect: cron-driven and Slack-triggered routines look healthy in dashboards but produce no work product. Credit-burning APIs (Apollo, EODHD) get patched to skip the sub-agent layer entirely as a band-aid, which compounds technical debt.

## Issue 1 — Gateway WebSocket transport drops mid-session

### Symptom

Three surfaces, **one transport-layer root cause**:

| Surface | Log signature | What the agent sees |
|---|---|---|
| Sub-agent invocation drops mid-run (≥60 s wall time) | `[agent/embedded] incomplete turn detected: runId=… stopReason=stop payloads=0 — surfacing error to user` | Empty completion, parent silently degrades |
| Prompt cache continuity breaks across the same window | `[prompt-cache] cache read dropped … via session-custom; no tracked cache input change` | Higher cost, slower next turn, no visible error |
| PreToolUse hook fails for every tool call | `PreToolUse hook error: Native hook relay unavailable` (from `openclaw hooks native-relay` stderr) | All tool calls rejected upfront — exec, apply_patch, etc. |

Multi-day pattern, model-independent (observed under both `anthropic/claude-sonnet-4-6` and `openai-codex/gpt-5.4`). Correlated startup-time variant: `Subagent announce failed: chat.history unavailable during gateway startup`.

### Root cause

The OpenClaw Machines control path moves WebSocket frames through four hops:

```
Hermes / Browser ──WS──> Cloud Run control plane ──WS──> Host agent (OVH) ──WS──> Per-VM authproxy ──WS──> in-VM gateway (Node)
                          ✓ machine_gateway.go         ✓ agentapi/proxy.go     ✗ NO keep-alive          ? in-VM gateway
                            25 s ping / 10 s pong        25 s ping / 10 s pong  (this repo)              (openclaw upstream)
```

Three hops in this repo have proven keep-alive (`backend/internal/api/machine_gateway.go:16-17,164-180`, `backend/internal/api/machine_terminal.go:111-128`, `backend/internal/agentapi/proxy.go:431-444`, all 25 s ping / 10 s pong). The per-VM authproxy (`backend/cmd/authproxy/main.go`) does not — `proxyGatewayWebSocket`, `proxyDashboardWebSocket`, and `proxyPlainWebSocket` do a naive `ReadMessage` / `WriteMessage` pump with no ping ticker and no `SetPongHandler`. The PTY in-VM endpoint got the same fix in commit `ce1c3df` (2026-05-28) for its own server (`backend/internal/ptyd/server_linux.go:516-545`); the gateway and dashboard endpoints were not generalised.

The **native-hook-relay failure surface** is the new data point. The hook bridge ships as part of the in-VM agent CLI (`src/cli/native-hook-relay-cli.ts` in openclaw, introduced 2026-04-24 by commit `7a958d920c`, first released in `v2026.4.24`). When a tool is about to run:

1. The CLI tries an in-process bridge (`invokeNativeHookRelayBridge`, 100 ms timeout). Fails when the agent CLI is in a separate process from the gateway — typical case.
2. Falls back to `callGateway` with method `nativeHook.invoke`. That uses the openclaw gateway WebSocket transport (`src/gateway/call.ts:794`, "gateway closed (${code}…)" error path at line 520).
3. If both fail, returns `Native hook relay unavailable` on stderr and the PreToolUse hook treats the call as denied.

That hook's WebSocket is **inside the VM** — local agent CLI process to local gateway Node process. It does not traverse authproxy or the host agent. So the fact that *this* WS also dies is strong evidence that the in-VM gateway WS server (openclaw runtime, Node) is the primary missing keep-alive, not (only) authproxy.

Time-correlation worth noting: the 5/25–5/27 "subagent paths were unavailable in-session" reports under gpt-5.4 are after the 2026-04-24 hook-relay rollout. Some or all of those reports may actually be the PreToolUse path failing, not sub-agent dispatch — same root cause, different surface. Worth checking the openclaw version pinned on the VM during that window.

### Fix

Two layers, in order of likely impact:

**A. Confirm and fix the in-VM gateway WS server (openclaw upstream)**

If the gateway's own WebSocket server doesn't ping its clients (the in-VM agent CLI for hook-relay, and the host-side authproxy that fronts it), no proxy keepalive downstream can save it. Local TCP idle reaping (Firecracker virtio net, in-VM iptables / conntrack, anything in the kernel side of the socket) will still kill the connection after the local idle threshold.

Verification before patching: from inside the VM during a failing run, `ss -i` on the gateway listening socket and the agent CLI's connection. If the idle timer climbs past ~30 s with no traffic, the gateway needs application-level pings. This is an openclaw upstream change; in this repo we just consume the fix.

**B. Add keep-alive to the per-VM authproxy (this repo, defense-in-depth)**

Port the pattern from `backend/internal/api/machine_gateway.go:164-180` into `backend/cmd/authproxy/main.go`. Three pieces:

1. Constants at top of file:
   ```go
   const (
       wsPingInterval = 25 * time.Second
       wsPongTimeout  = 10 * time.Second
   )
   ```
2. Per-connection setup — on each of `clientConn` and `targetConn`:
   - `SetReadDeadline(now + pingInterval + pongTimeout)`
   - `SetPongHandler` that extends the read deadline
3. Ping ticker goroutine running for the connection's lifetime:
   - Every 25 s, `WriteControl(PingMessage, nil, now + 5 s)` on both sockets
   - Exit when either pump goroutine signals done
4. Pump goroutines — on each `ReadMessage`, also extend that socket's read deadline.

Apply to `proxyGatewayWebSocket`, `proxyDashboardWebSocket`, and `proxyPlainWebSocket`.

Note on framing: this is defense-in-depth, not the *primary* root cause. The host-agent ↔ authproxy leg already has 25 s pings on the host-agent side (`agentapi/proxy.go:431-444`); a TCP middlebox between host agent and VM that respects keep-alive frames should keep that leg alive even without authproxy contributing its own pings. The reason to still do this fix:

- The authproxy hop is the one place WS traffic gets terminated and re-originated end-to-end in the path. Adding pings here makes the in-VM and out-of-VM legs independent: an in-VM authproxy ↔ gateway link going idle does not need an outside source to keep it warm.
- The fix is cheap (~30 lines, copied from a proven pattern in the same repo).
- It pairs with required verification logging below — both go into the same patch.

**C. Add close/duration logging to authproxy WS handlers (prerequisite for verification)**

Today `proxyTo` in authproxy logs `duration_ms`, but `proxyGatewayWebSocket` and `proxyPlainWebSocket` just close after `<-done` with no duration line. The verification plan below depends on observing this — fix the logging first or in the same patch:

- Record `start := time.Now()` at the top of each handler.
- On close, emit a structured log entry with: target URL, direction that closed first (client→target or target→client), close code if available, total duration_ms.

This is a prerequisite, not a nice-to-have — without it, the before/after measurement cannot distinguish "fix worked" from "logs lie."

### Verification

Before merge, capture one cron fire on Jordan's machine (06:00 UTC, news-watcher coordinator):

- In-VM gateway log: confirm `incomplete turn detected` lines correlate with sub-agent runIds, and `Native hook relay unavailable` lines correlate with tool-call rejections.
- Authproxy log: confirm WS `duration_ms` (once logging lands per above) on failing connections clusters at the idle-reap boundary on the path most likely to be biting (in-VM authproxy ↔ gateway, since the hook-relay failures imply in-VM idling).
- Inside the VM during the failing run: `ss -intp 'sport = :<gateway-port>'` snapshot to capture idle-time on the agent CLI's connection at the moment of the hook-relay failure.

After deploy (whichever layer ships first — A or B):

- Sub-agent run completes within its natural wall-clock budget (2–3 min).
- `incomplete turn detected` lines disappear from matching runIds.
- `Native hook relay unavailable` stops firing on long sessions.
- Authproxy WS `duration_ms` matches real session length, not the ~60 s clip.

If the authproxy fix (B) ships alone and the symptoms persist, that confirms the missing keep-alive is in-VM gateway upstream (A) — useful triage either way.

### Out of scope for this fix

- **Gateway↔LLM provider WebSocket** (Anthropic streaming, OpenAI streaming): lives in the openclaw runtime (Node), not this repo. If those connections also idle-drop on the public-internet leg, that is a separate upstream issue.
- **Sub-agent-aware retry semantics**: today the gateway surfaces a 1006-induced stream end as a normal `stop` with 0 payloads. A follow-up should make the gateway emit a structured `subagent_aborted: close_code=1006` event so coordinator routines can decide whether to retry. Tracked separately.
- **Bounded retry at SKILL/routine level** with an idempotency token, so re-running an Apollo credit-burning call twice does not double-charge. Application-layer follow-up, not part of transport fix.

## Issue 2 — Exec preflight rejects `cd <dir> && node <file>.js`

### Symptom

LLM agents inside the VM emit `exec` tool calls of the shape:

```
cd /home/openclaw/.openclaw/workspace && node bin/pipeline-temperature-check.js 2>&1
```

The openclaw exec preflight rejects them with:

```
exec preflight: complex interpreter invocation detected; refusing to run without script preflight validation.
Use a direct `python <file>.py` or `node <file>.js` command.
```

False positive — the command shape is safe and the preflight's file-content validation would pass if the script target were extracted correctly. Impact: routines that rely on LLM-emitted `cd && cmd` shapes fail at the first exec call and either retry into a different shape (good case) or loop on the same shape until the run gives up (bad case for smaller / older models).

This is *not* in this repo. The preflight lives in the openclaw runtime at `src/agents/bash-tools.exec.ts` (line 969 throws the error). OCM consumes openclaw from npm with no fork, so a true fix requires an upstream PR.

### Root cause

The preflight's script-target extractor (`extractScriptTargetFromCommand`, `src/agents/bash-tools.exec.ts:360-432`) parses the entire raw command as one argv list:

- For `cd X && node Y.js`, `splitShellArgs` returns `["cd", "X", "&&", "node", "Y.js", "2>&1"]`.
- The function reads the first token (`cd`) as the executable, finds it is neither `python` nor `node`, and returns `null`.
- A `null` target sends control to a fail-closed fallback (`shouldFailClosedInterpreterPreflight`) which checks three independent heuristics:
  1. `hasInterpreterInvocation` — any `&&`-separated segment leads with `python`/`node` → true.
  2. `hasComplexSyntax` — raw contains `&&` → true.
  3. `hasInterpreterSegmentScriptHint` — any segment is a script-executing interpreter command with a `.js`/`.py` arg → true.
- All three trip; the preflight throws.

The extractor never tries splitting on `&&`/`;` before tokenizing each segment, so the safe `cd dir && node file.js` shape gets bucketed with genuinely ambiguous shapes like `bash -c "python bad.py"` and `cat bad.py | python`.

### Fix

Two paths, both worth pursuing:

**Upstream PR to openclaw** (preferred, durable):

The naive shape "if any segment parses as a direct interpreter command, return its target" is **unsafe** for two distinct reasons that surfaced in review:

1. **Wrong path validated.** File validation in `validateScriptFileForShellBleed` (`src/agents/bash-tools.exec.ts:978`) resolves relative paths against `params.workdir`, not the shell's effective `cd` directory. If the extractor returns `script.js` from `cd /tmp && node script.js` without accounting for the `cd`, validation reads a different file than the one the shell actually executes. Security regression, not a fix.
2. **First-match bypass of fail-closed.** A shape like `cd /tmp && node safe.js && python bad.py` would extract `safe.js`, validate it cleanly, and skip the existing fail-closed check that catches `python bad.py`. The naive fix opens an injection path that the current preflight closes.

Tighten the extractor to handle exactly the case it needs to handle and nothing more:

- **Special-case shape.** Recognise only the strict form: optional `cd <literal-path>` `&&` exactly one direct interpreter invocation, optionally trailing redirects (`2>&1`, `>file`, etc.). No other `&&`/`;`/`|` segments allowed.
- **Workdir guard.** If a leading `cd` is present, require that the literal target resolves equal to `params.workdir` (or is a strict subdirectory of it). If it does not match, fail closed as today — do not silently re-base validation.
- **All-segments alternative** (broader, but more invasive): if any future shape with multiple `&&`-separated commands is supported, validate the script target of **every** direct interpreter segment, not just the first. Either choice closes the bypass; the special-case shape is the minimum needed to fix the reported pattern.

Add passing test cases in `src/agents/bash-tools.exec.script-preflight.test.ts`:

```ts
// passCases — must succeed
["cd-prefixed direct node invocation, workdir matches", "cd /tmp && node script.js"],
["cd-prefixed direct python invocation, workdir matches", "cd /tmp && python script.py"],
["cd-prefixed with stderr redirect, workdir matches", "cd /tmp && node script.js 2>&1"],
```

And new fail-closed cases that the tightened extractor must still reject:

```ts
// failClosedCases — must continue to reject
["cd-prefixed with trailing extra interpreter segment", "cd /tmp && node safe.js && python bad.py"],
["cd-prefixed where cd target does not match workdir", "cd /etc && node /tmp/script.js"],
["cd-prefixed with shell wrapper", "cd /tmp && bash -c 'python bad.py'"],
```

The existing `failClosedCases` list (`bash -c "python bad.py"`, `cat bad.py | python`, etc.) must still pass.

**Workaround in this repo** (immediate, while upstream lands):

Coordinator routines and SKILL.md files that emit exec calls can drop the `cd … &&` prefix entirely. The openclaw `exec` tool already accepts a `workdir` parameter (which is how the preflight gets one in the first place), so the `cd` is redundant on every modern openclaw version. Documenting this in the SKILL.md template prevents new routines from inheriting the pattern.

### Out of scope for this fix

- **Preflight behaviour for shell-wrapped interpreter invocations** (`bash -c "..."`): the existing fail-closed path correctly catches attacker-routing here. The tightened fix only narrows extraction for the `cd && cmd` shape; shell-wrap detection is unchanged.
- **A general OCM-side preflight bypass / patch layer**: tempting but fragile; ships divergence from upstream npm and breaks the "no fork" rule from `CLAUDE.md`. The workaround above is sufficient until the upstream PR merges.

## Rollout

### Issue 1 (gateway WS keep-alive)

1. **Authproxy patch (this repo):**
   - Implement constants, ping ticker, pong handler, and close-duration logging in `backend/cmd/authproxy/main.go`.
   - Capture baseline before deploy: gateway log + authproxy log + `ss -i` snapshot from inside Jordan's VM during the 2026-06-02 06:00 UTC cron fire.
   - Authproxy ships in the rootfs (`backend/authproxy` is built and copied during `make build-rootfs`); existing VMs are unaffected until recycled. Deploy via `make build-upload-rootfs`; recycle a small subset of VMs first to validate.
   - Capture a post-deploy 06:00 UTC fire on the recycled VMs; compare.

2. **In-VM gateway keep-alive (openclaw upstream), if Step 1 alone does not resolve:**
   - File issue + PR against `openclaw/openclaw` with the `ss -i` evidence showing in-VM idle on the gateway WS server socket.
   - Once the fix ships in an openclaw release, bump via `make update-openclaw OPENCLAW_VERSION=…` (build) followed by `make build-upload-openclaw` (upload to GCS + register in DB). New VMs and reboots pick it up automatically per the artifact-delivery chain in `CLAUDE.md`.

### Issue 2 (exec preflight)

1. File upstream PR at `openclaw/openclaw` with the tightened extractor (special-case shape + workdir guard) and the expanded test fixtures.
2. In parallel, update SKILL.md templates and the morning-briefing / pipeline-fill routines to emit `node bin/foo.js` (with `workdir` set on the tool call) rather than `cd … && node bin/foo.js`.
3. When the upstream PR ships, bump openclaw via `make update-openclaw` + `make build-upload-openclaw` and remove the workaround note from the templates.

## Open questions

- Is there a Cloudflare-side idle reap on the host-agent ↔ Cloud Run leg that the existing keep-alive at `machine_gateway.go` already covers, or should the ping interval be tuned tighter than 25 s for that hop? Out of scope for this fix but worth tracking if 1006s reappear post-deploy on the host leg.
- Were the 5/25–5/27 pipeline-fill failures actually the new hook-relay surface (which shipped 2026-04-24) rather than sub-agent dispatch? Confirmable by checking the openclaw version pinned on the affected VM(s) for that window. If yes, that's another data point that the in-VM gateway WS is the primary culprit and the authproxy fix is genuinely defense-in-depth.

## References

### This repo

- `backend/cmd/authproxy/main.go` — site of the fix for Issue 1.
- `backend/internal/api/machine_gateway.go:16-17,164-180` — reference implementation of the keep-alive pattern.
- `backend/internal/api/machine_terminal.go:111-128` — same pattern on the PTY hop.
- `backend/internal/agentapi/proxy.go:431-444` — same pattern on the host-agent hop (already pings into authproxy every 25 s).
- `backend/internal/ptyd/server_linux.go:516-545` — recent in-VM PTY keep-alive (commit `ce1c3df`, 2026-05-28).
- `Makefile:559, 1115` — `update-openclaw` (build) vs `build-upload-openclaw` (build + upload + register); both are needed to ship an openclaw bump.
- `Makefile:581` — `build-rootfs` copies `backend/authproxy` into the image.
- `CLAUDE.md:53` — rootfs ships via `make build-upload-rootfs`; existing VMs unaffected until recycled.

### Openclaw upstream (`/home/mantiz/openclaw`, read-only reference)

- `src/agents/bash-tools.exec.ts:360-432` — `extractScriptTargetFromCommand` (Issue 2 fix site).
- `src/agents/bash-tools.exec.ts:949-973` — fail-closed throw site.
- `src/agents/bash-tools.exec.ts:978` — `validateScriptFileForShellBleed`, the function whose workdir-vs-cd mismatch motivates the workdir guard.
- `src/agents/bash-tools.exec.script-preflight.test.ts:497-555` — preflight test fixtures to extend.
- `src/cli/native-hook-relay-cli.ts` — emits the "Native hook relay unavailable" string. Introduced 2026-04-24 by commit `7a958d920c` ("Bridge Codex native hooks into OpenClaw"), first released in `v2026.4.24`.
- `src/gateway/call.ts:794` — `callGateway` (gateway WebSocket transport used by the hook-relay fallback).
- `src/gateway/call.ts:520` — "gateway closed (${code}…)" error path; the signal that confirms WS-transport failure when hook-relay fires.
