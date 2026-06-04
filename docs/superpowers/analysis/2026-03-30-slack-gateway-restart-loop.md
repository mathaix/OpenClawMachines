## Slack Gateway Restart Loop Analysis

### Context
After connecting Slack as a channel on a running VM, the gateway enters an infinite restart loop and messages are dropped. This document captures the root cause analysis and open questions.

### Question
Why does connecting Slack cause a gateway restart loop, and why are Slack messages dropped even when the gateway is running?

### Method
- Connected Slack on VM `6fa650ad-7443-4af7-999a-76b6bc0c5316` (OpenClaw v2026.3.28)
- Compared behavior with local OpenClaw (no plugins configured)
- Read gateway logs (`/tmp/openclaw/openclaw-*.log`) and stderr (`/var/log/openclaw-gateway.log`)
- Inspected config file changes via `openclaw config get`
- Tested manual config fixes on the running VM

### Findings

#### 1. Restart Loop Root Cause

When the gateway starts with `channels.slack` configured, it treats Slack as a **built-in plugin** and auto-writes two things to `openclaw.json`:
- `plugins.entries.slack` (registers the plugin)
- `plugins.allow` (adds "slack" to the trusted list)

The config file watcher detects these writes and logs:
```
[reload] config reload requires gateway restart; hot mode ignoring (plugins.allow, plugins.entries.slack)
```

The init script sees the restart signal, kills the gateway, and restarts it. The gateway auto-writes again on startup, creating an infinite loop.

**Why Telegram/Discord don't have this problem:** They are built-in channels handled natively by the gateway. They do not register as plugins and do not trigger auto-writes to the config file.

**Why it worked locally but not on the VM:** The local machine had no existing plugins configured. The VM had composio and opik-openclaw already in `plugins.entries` and `plugins.allow`. The gateway's auto-write of Slack into the existing plugins section produced a file change that triggered the watcher.

#### 2. Message Dropping

Even when the gateway was briefly stable, all Slack messages were dropped. Log line:
```
slack: drop channel C0AP8R99JJK (groupPolicy:allowList, matchKey:none matchSource:none)
slack: drop message (channel: not allowed)
```

**Cause:** The config template (migration 055) does not include `groupPolicy`. The gateway defaults to `"allowList"`. With no groups configured in the allow list, all channels are blocked. Setting `groupPolicy: "open"` manually resolved the message dropping.

#### 3. Self-Resolution Question (Open)

The restart loop should theoretically self-resolve after one cycle:
1. Gateway starts, writes `plugins.entries.slack` + `plugins.allow`
2. File watcher triggers restart
3. Gateway starts again, sees values already present from step 1, nothing to write, stable

**It does not self-resolve.** Root cause confirmed:

The gateway's own file watcher detects its own writes. The sequence is:
1. Gateway starts, reads `openclaw.json`
2. Gateway applies internal defaults (adds `plugins.entries.slack`, `plugins.allow`)
3. Gateway writes merged config back to `openclaw.json` (creates `.bak` backup first)
4. Gateway's file watcher detects the write on its own config file
5. Watcher flags `plugins.allow` and `plugins.entries.slack` as changed → "needs restart"
6. Init script sees the restart signal → kills gateway → restarts → back to step 1

**Evidence:**
- `sha256sum` of `.json` and `.json.bak` differ — the gateway IS modifying the file
- `plugins.allow` already contains "slack" and `plugins.entries.slack` already has `{enabled: true, config: {}}` — yet the gateway still rewrites the file
- `start_gateway()` does NOT restore from backup or regenerate config — the gateway itself is the writer
- This is a gateway bug: it does not distinguish between external config changes and its own writes

**Implication:** Pre-setting `plugins.entries.slack` and `plugins.allow` will NOT stop the loop because the gateway always rewrites the file regardless of whether the values are already present.

### Current State of the VM

- `channels.slack.groupPolicy` manually set to `"open"` -- messages are accepted
- `plugins.entries.slack.enabled` manually set to `true` -- but `plugins.allow` was NOT pre-set
- Restart loop continues because `plugins.allow` is still being auto-written
- Bot responded once ("4" to "what is 2+2?") during a brief stable window

### Fix Design

**Live push path (`handleChannelConnect` in `channel_config.go`):**

When connecting Slack on a running VM, push these config ops in a single batch BEFORE restarting the gateway:
1. `channels.slack` -- the channel config (already implemented)
2. `plugins.entries.slack.enabled = true` -- prevent auto-write (committed in `5ce84da`)
3. `plugins.allow` -- append "slack" to existing array (not yet implemented)

**Config template (migration 055):**

Add `"groupPolicy": "open"` to the Slack config template so messages aren't dropped by default.

**Seed path (`configassembly`):**

Not needed for the initial connection (VM is already running when user connects Slack). Only relevant if a VM is reprovisioned with Slack already enabled -- secondary concern.

### Caveats

- We do not control the gateway code (upstream `openclaw` npm package). The auto-write behavior is arguably a gateway bug -- built-in plugins should be activated in-memory without writing to the config file.
- The exact content the gateway writes to `plugins.entries.slack` is unknown. It may include more fields than just `enabled: true`. If so, the gateway may still detect a diff and write on startup. Need to verify by comparing the pre-set value with what the gateway writes.
- If OpenClaw adds more built-in channel plugins in the future, they will likely hit the same restart loop issue.
- The `plugins.allow` update requires reading the current array value and appending "slack" -- the current `openclaw config set` may not support array append, only full replacement.

### Future Design Consideration: Slack Behavior Settings UI

The gateway supports extensive per-channel Slack customization. Currently the connect dialog only asks for the two tokens (bot + app). All other settings use defaults. A future iteration could expose common settings in the UI.

**Default behavior (current template):**
- Channels: only responds when `@openclaw` is mentioned (`requireMention: true`)
- DMs: always responds (`dmPolicy: "pairing"`)
- Threads: once mentioned, follows the thread without needing further mentions (`thread.historyScope: "thread"`)
- Channel access: bot listens in any channel it's invited to (`groupPolicy: "open"`)
- No channel IDs need to be configured — invitation is automatic

**Customizable settings (candidates for UI exposure):**

| Setting | Config key | What it controls |
|---------|-----------|-----------------|
| Respond without mention | `channels.<id>.requireMention: false` | Bot responds to every message in channel |
| Disable DMs | `dmPolicy: "disabled"` | Ignore all direct messages |
| Open DMs | `dmPolicy: "open"` + `allowFrom: ["*"]` | Anyone can DM the bot |
| Restrict DMs | `dmPolicy: "allowlist"` + `allowFrom: [...]` | Only specific users can DM |
| Thread replies | `replyToMode: "off" / "all"` | Whether/how the bot threads replies |
| Parent context in threads | `thread.inheritParent: true` | Include channel context in thread |
| Per-channel system prompt | `channels.<id>.systemPrompt` | Different personality per channel |
| Per-channel tool restrictions | `channels.<id>.tools` | Limit tools available in a channel |
| Group DMs | `dm.groupEnabled: true` | Allow multi-person DM conversations |
| Streaming | `streaming: "partial" / "off"` | Live typing preview |

**Recommendation:** Start with sensible defaults (mention-only in channels, open DMs, thread follow-up). Add a "Slack Settings" panel to the channel card as a follow-up feature, exposing the most common toggles: `requireMention`, `dmPolicy`, and `replyToMode`.

### See Also

- `backend/internal/api/channel_config.go` -- connect/disconnect handlers
- `backend/migrations/055_slack_channel.sql` -- Slack registry entry
- `scripts/init-openclaw.sh` -- gateway restart mechanism (lines 841-897)
- `docs/superpowers/specs/2026-03-30-slack-channel-integration-design.md`
