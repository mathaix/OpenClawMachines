# OpenClaw Runtime Hardening — Phase 1

**Date:** 2026-04-28
**Status:** Design — implementation gated on review and on the launch-gate items below
**Goal:** Cut new-VM cold-boot time from ~68s back toward ~12s, plus apply the runtime-hygiene recommendations openclaw doctor surfaces on every VM.

## Why this matters to users

A user who clicks "Create VM" on the OpenClaw Machines dashboard waits for:

1. Firecracker boot + init script (~14s, unavoidable).
2. Gateway and channels/sidecars to come up before the browser UI loads.

On openclaw 2026.4.15 step 2 took ~12s and the UI loaded immediately. On 2026.4.26 (currently `v2026.4.26-r1` in GCS stable, deployed earlier today) the same boot takes ~68s — and during that 53-second post-boot gap, browser asset requests time out at the auth-proxy with 502s, so the user sees a broken UI until they refresh. Multi-second waits are tolerable; minute-long waits with broken-looking UIs feel like the product is failing.

Separately, every fresh VM also has a few easy hygiene gaps the upstream `openclaw doctor` command flags: missing V8 compile cache, missing self-respawn skip, world-readable state dir, missing plugin-install registry. Each is small in isolation; together they're worth folding into the same VM-build hardening pass.

## What changed in openclaw between 4.15 and 4.26

Two upstream behaviors regressed cold boot:

1. **Sidecar startup blocks `ready`.** In 4.15 the gateway emitted `ready` immediately after the HTTP server bound, then loaded sidecars in the background. Methods that were not yet safe returned `errorCode=UNAVAILABLE` (a behavior the gateway still has). In 4.26, `gateway ready` is held until *all* sidecars finish loading. The single `await runtimeDeps.startGatewaySidecars(...)` in `server-startup-post-attach.ts:537` accounts for the entire 53s delay.
2. **Bundled extensions are imported even when disabled.** OpenClaw bundles 121 extensions — channel adapters, AI provider integrations, speech engines, etc. Doctor reports `Loaded: 3 / Disabled: 114`, but Node still resolves and imports the module graph for many of those 114 during sidecar init because they're sibling modules of the eager imports.

A flag exists upstream (`deferStartupSidecars`) that would restore 4.15 behavior, but it isn't exposed via CLI and we can't set it from `init-openclaw.sh`. There is also no per-extension allowlist — only a blunt `OPENCLAW_SKIP_CHANNELS=1` that turns off messaging entirely.

## What OCM tenants actually use

From production config:

- **Channels (4 of 22 bundled):** telegram, slack, discord, whatsapp.
- **Plugins (3 of 121 bundled):** composio, memory-core, opik-openclaw.
- **LLM providers in active use:** anthropic, openai, openrouter, google, plus nebius (configured as a custom provider with `api: "openai-completions"` rather than a dedicated bundled extension).

The remaining ~110 bundled extensions are dead code paths for our fleet today.

## Approach

This design lands two kinds of changes in one Phase 1 hardening pass:

**Build-time (artifact):** When `make update-openclaw` runs `npm install -g openclaw@${OPENCLAW_VERSION}`, the resulting `dist/extensions/` directory contains all 121 extensions. After install and before tarball packaging, we delete the channel directories OCM tenants don't use. The remaining artifact ships a slimmer extensions surface, so openclaw imports fewer modules during sidecar init, so `ready` fires sooner.

**Runtime (init-openclaw.sh):** Add the env vars and permissions doctor recommends, plus repair the bundled-plugin install registry once per VM after the gateway is reachable.

### Phasing

**Phase 1 — this design (in progress):**

- **Build-time, channel scrub:** Delete 18 unused channel directories from `dist/extensions/`. Keep telegram, slack, discord, whatsapp.
- **Build-time, deferSidecars patch:** Modify openclaw's compiled `dist/server.impl-*.js` to flip the `deferSidecars` default from `false` to `true`, restoring 4.15 behavior. The gateway emits `ready` immediately after HTTP bind; channels and sidecars load asynchronously. Patch is a single string replacement with hash-style uniqueness + context-window verification (`patch-defer-sidecars.mjs`). Fail-closed if upstream refactors the surrounding code.
- **Runtime:** Set `NODE_COMPILE_CACHE`, `OPENCLAW_NO_RESPAWN=1`. Tighten `~/.openclaw` perms to 700 and `openclaw.json` to 600 from creation. Run `openclaw doctor --fix` once at boot (with 30s timeout) to repair the plugin install registry.

The deferSidecars patch was originally rejected for Phase 1 as too invasive; we revisited that decision when the channel scrub alone proved insufficient (≈3s wall-clock improvement against the 53s sidecar window). The patch is the smallest change that actually addresses the user-visible regression — single line, deterministic, reversible.

**Phase 2 — separate design, gated on Phase 1 measurements:**

- Upstream PR for `--defer-startup-sidecars` CLI flag, deletes our compiled-JS patch if it lands.
- Broader extension scrub (AI providers, speech, image/video) if Phase 1 doesn't reach the 4.15 cold-boot floor.
- Pre-warm `NODE_COMPILE_CACHE` during rootfs build so first-boot also benefits.
- Audit and trim the 36 skills with missing requirements — either ship the requirements in rootfs or remove the skills.

### Alternatives considered for Phase 1

- **Roll back to 2026.4.15-r1:** Loses 4.16–4.26 fixes (plugin metadata scoping, opik tracing fixes, Feishu cards, LSP cleanup, ACP improvements). Acceptable as a panic-rollback target; not the long-term answer.
- **Channel scrub alone:** Empirically insufficient. Our cold-boot test against the scrub-only candidate showed no meaningful improvement vs unscrubbed. The bottleneck is architectural (gateway awaits sidecar init before ready), not module-count.
- **`OPENCLAW_SKIP_CHANNELS=1` for all VMs:** All-or-nothing. Breaks every tenant with Slack/Telegram/Discord/WhatsApp — when they enable a channel, the gateway logs *"skipping channel reload"* and never connects.
- **Pre-warmed VM pool:** Large infra investment. Doesn't solve the boot problem so much as hide it. Out of scope.

## Implementation

### Build-time scrub

Insert one block in `scripts/build-openclaw-runtime.sh` between bundled-plugin install (line 207) and the import-drift check (line 215). The keep list is the source of truth (allowlist, not denylist — see Codex finding #8 below). The build fails if any unknown channel directory appears in `dist/extensions/`, which forces a human review on each openclaw bump.

```bash
# Allowlist — the only channel adapters OCM ships.
OCM_KEEP_CHANNELS="${OCM_KEEP_CHANNELS:-telegram slack discord whatsapp}"

# Discover what channel adapters this openclaw version actually ships.
SHIPPED_CHANNELS=$(node -e '
  const fs = require("fs"), path = require("path");
  const ext = "'"${OC_PKG}"'/dist/extensions";
  const out = [];
  for (const dir of fs.readdirSync(ext)) {
    const manifest = path.join(ext, dir, "openclaw.plugin.json");
    if (!fs.existsSync(manifest)) continue;
    try {
      const m = JSON.parse(fs.readFileSync(manifest, "utf8"));
      if (Array.isArray(m.channels) && m.channels.length) out.push(dir);
    } catch {}
  }
  console.log(out.sort().join(" "));
')

# Compute scrub list = shipped - keep. Anything in shipped but not in keep
# is either intended scrub or a new channel we have not classified yet.
EXPECTED_KNOWN="bluebubbles discord feishu googlechat imessage irc line matrix \
  mattermost msteams nextcloud-talk nostr qqbot signal slack synology-chat \
  telegram tlon twitch whatsapp zalo zalouser"
for ch in ${SHIPPED_CHANNELS}; do
    case " ${EXPECTED_KNOWN} " in
        *" ${ch} "*) ;;
        *)
            echo "ERROR: unknown channel adapter '${ch}' in openclaw dist/extensions."
            echo "  Either add it to the keep list (OCM_KEEP_CHANNELS) or to EXPECTED_KNOWN."
            echo "  This protects against new upstream channels silently bypassing the scrub."
            exit 1
            ;;
    esac
done

SCRUB_COUNT=0
SCRUB_LIST=""
for ch in ${SHIPPED_CHANNELS}; do
    keep=0
    for k in ${OCM_KEEP_CHANNELS}; do
        [ "$k" = "$ch" ] && keep=1 && break
    done
    [ "$keep" = "1" ] && continue
    rm -rf "${OC_PKG}/dist/extensions/${ch}"
    SCRUB_COUNT=$((SCRUB_COUNT + 1))
    SCRUB_LIST="${SCRUB_LIST} ${ch}"
done
echo "Scrubbed ${SCRUB_COUNT} unused channel adapter(s):${SCRUB_LIST}"

# Persist the decision into the build manifest so we can compare across builds.
SCRUB_DECISION_JSON=$(node -e '
  const k = process.argv[1].trim().split(/\s+/).filter(Boolean);
  const s = process.argv[2].trim().split(/\s+/).filter(Boolean);
  console.log(JSON.stringify({kept: k.sort(), scrubbed: s.sort()}));
' "${OCM_KEEP_CHANNELS}" "${SCRUB_LIST}")
```

`SCRUB_DECISION_JSON` is then woven into `openclaw-build-info.json` alongside the existing fields, so each artifact records what was kept/scrubbed for forensic comparison.

The existing import-drift check runs *after* the scrub. **Codex correctly flagged that the existing check ignores relative imports** — if openclaw has a static `./matrix` style barrel import, the build passes and runtime fails. To close that gap without rewriting the checker, we add a complementary post-scrub assertion: `node -e 'require("openclaw")'` from a tmpdir using the bundled `node_modules`. If a kept module silently references a scrubbed sibling, this fails loudly at build time. (We can't import the gateway module without a config, but a top-level `require` exercises the eager parts of the import graph.) The existing allowlist entries for scrubbed packages (Matrix, Feishu, Nostr, QQ, etc.) are removed from `scripts/build-openclaw-runtime.sh:243-252` in the same change.

The runtime externals declared for scrubbed channels — `@microsoft/teams.api`, `@microsoft/teams.apps`, `jsonwebtoken`, `jwks-rsa`, `@matrix-org/matrix-sdk-crypto-nodejs`, `nostr-tools`, etc. — are removed from `scripts/openclaw-runtime-externals/package.json`. The `npm ci` in the build step picks up the slimmer dep set and the artifact size shrinks accordingly.

### Runtime hardening in init-openclaw.sh

The gateway env block at `scripts/init-openclaw.sh:1160-1176` gains:

```
"NODE_COMPILE_CACHE=/var/cache/openclaw-compile-cache"
"OPENCLAW_NO_RESPAWN=1"
```

Earlier in the script we `mkdir -p /var/cache/openclaw-compile-cache && chown openclaw:openclaw /var/cache/openclaw-compile-cache`. The path is on the persistent data volume, so subsequent gateway restarts on the same VM benefit. First-boot doesn't (V8 bytecode cache is content-addressed and starts empty); pre-warming during rootfs build is Phase 2.

Permissions: where init creates `~/.openclaw` and writes `openclaw.json`, set 700/600 directly rather than relying on doctor to find them later. The auto-permissions logic in doctor stays useful as a safety net but shouldn't be doing first-time fixes on every fresh VM.

Plugin install registry repair: after the gateway responds to its health check (existing wait loop in init), shell out one-time to `openclaw doctor --fix --no-prompt` from the openclaw user to rebuild `~/.openclaw/plugins/installs.json` for the bundled plugins. Capture stdout to the init log. If doctor exits non-zero, log a warning but don't fail boot — this is a hygiene fix, not a correctness gate.

## Verification (launch gates)

Codex's review correctly identified that our existing smoke test would not validate this change as written. Pre-promotion, the candidate artifact must pass these gates **explicitly against the new artifact, not against `manifest-stable.json`**, and **not in quick-start mode**:

1. **Build passes.** `make update-openclaw OPENCLAW_VERSION=2026.4.26` completes; the post-scrub `require("openclaw")` assertion succeeds; the import-drift check (with scrubbed-channel allowlist entries removed) is green; tarball is meaningfully smaller than r1.

2. **Channel-surface invariant (LAUNCH GATE).** OCM's product surface only exposes telegram, slack, discord, and whatsapp. The `ChannelTokenFields` map at `backend/internal/configassembly/assembler.go:166-173` is the complete set of channels the platform plumbs credentials for; whatsapp uses session-based auth and is configured through the same UI surface. There is no tenant path to enable Matrix, MS Teams, Signal, Feishu, or any of the other 18 scrub candidates — config assembly will not generate a `channels.{name}` block for them, and the credentials table has no provider entries for them. This gate is satisfied by the product invariant, not by a DB audit. If that invariant ever changes (someone adds Matrix to `ChannelTokenFields` or builds a raw-config-push admin path), the keep list must be updated in the same change that introduces the new channel.

3. **Candidate-artifact smoke test.** `make smoke-test` is parametrized to point at the candidate artifact path on disk (the just-built tarball under `/var/lib/ocm/openclaw-artifacts/`), not `manifest-stable.json`. The test VM is booted **without** `ocm_quick_start=1`, so the gateway exercises the full sidecar path and the channel-load code is actually run.

4. **Per-channel runtime check.** Configure each of the 4 kept channels (telegram, slack, discord, whatsapp) with stub credentials in the test VM's openclaw.json and confirm:
   - The adapter loads without error in gateway logs.
   - `openclaw doctor` reports the channel as recognized.
   - At least one channel-bound config push (`openclaw config set channels.X = {…}`) survives a hot reload without crashing the gateway.

5. **Cold-boot timing.** Boot one fresh VM with the candidate artifact, capture wall-clock from VM start to `[gateway] ready`. Compare to the current `v2026.4.26-r1` baseline (~68s) and the historical `v2026.4.15-r1` baseline (~12s). Record the number in the rollout PR description.

6. **Auth-proxy 502 rate during boot.** Walk the candidate's boot log; count `authproxy.proxy_error` lines with `error="context canceled"` during the first 60s. Phase 1 expectation: lower, possibly zero. If still ~4 like today, that's a signal that channel scrub alone isn't enough and we need to revisit the deferSidecars patch.

If gates 2–6 don't all pass, the artifact does not get promoted to GCS stable, regardless of build success.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| A scrubbed channel turns out to be implicitly required by a kept module via relative import | Post-scrub `require("openclaw")` assertion at build time; gate 4 (per-channel runtime check) catches it before promotion. |
| Phase 1 cold-boot win is small (<20s improvement) | A/B timing in gate 5 makes this measurable. If win is small, Phase 2 (broader scrub or deferSidecars patch) is justified rather than speculative. |
| New upstream channel directory appears in 4.27 (e.g. some new messenger) and silently slips through | Build script fails on unknown channel dirs (allowlist + EXPECTED_KNOWN both required). Forces a human classification at version-bump time. |
| Tenant later wants Matrix / MS Teams / etc. | Restoring a channel: add to `OCM_KEEP_CHANNELS`, add back to the externals package.json, rebuild. Documented clearly so support has the knob. |
| `NODE_COMPILE_CACHE` directory grows unbounded over VM lifetime | Path lives on data volume; bounded by VM disk; no cleanup needed at this scale. Phase 2 may add a max-size guard. |
| `openclaw doctor --fix` at boot has unintended side effects | Run with `--no-prompt`; init script captures output and continues even on non-zero exit. Effects are scoped to `~/.openclaw/`. |

## Rollout

1. **Confirm channel-surface invariant (launch gate 2)** — re-read `ChannelTokenFields` in `assembler.go` to verify only telegram/slack/discord/whatsapp are exposed. Note in the rollout PR description that this is satisfied by product invariant, not by tenant data.
2. **Build candidate artifact:** branch off `openclaw25`, land scrub + init changes, run `make update-openclaw OPENCLAW_VERSION=2026.4.26`. Resulting artifact is `v2026.4.26-r2`.
3. **Run launch gates 1, 3, 4, 5, 6** against the candidate. Record numbers in PR description.
4. **If all gates pass:** `make upload-openclaw OPENCLAW_VERSION=2026.4.26` promotes the candidate to GCS stable. New VMs immediately pull `v2026.4.26-r2`. Existing VMs unaffected (they keep the artifact they were provisioned with).
5. **24h watch:** monitor first-boot latency on new provisions, channel-related plugin error logs, doctor `--fix` exit codes from init logs.
6. **Rollback path:** `gsutil cp gs://…/v2026.4.26-r1/manifest.json gs://openclawmachines/openclaw/manifest-stable.json` reverts to today's known-good (slow but functional). Existing VMs unaffected; new VMs go back to the unscrubbed artifact.

## Open questions (not launch gates)

- Whether to invest in a Phase 2 `NODE_COMPILE_CACHE` pre-warm during rootfs build (would benefit first-boot too). Worth doing if Phase 1's first-boot number is still ~30s+ after scrub.
- Whether to also Phase-1-scrub the 9 speech extensions (azure-speech, deepgram, elevenlabs, etc.) which are all unused in our fleet today. Decision gated on Phase 1 timing — if we hit ~12s with channels-only, no need.
- Whether to file an upstream `--defer-startup-sidecars` PR now or after measuring Phase 1. Filing now starts the upstream clock; doing it later means Phase 1 numbers can inform whether this is even worth pursuing.
