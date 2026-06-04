# OCM CLI Design Prototype — Feedback

**Source:** Review of `docs/cli-design-prototype.html`
**Date:** 2026-02-28

---

## What It Gets Right

### Step Progress Indicator
The `● Login ─── ● Machine ─── ○ Provider ─── ○ Channel ─── ○ Identity ─── ○ Verify` progress bar is the standout UX element. Gives users confidence about where they are and how much is left. Will reduce drop-off during setup.

### Masked Key Input
`····························sk4f` — shows enough to confirm the right key was pasted without exposing the full value. Good security hygiene in a terminal context.

### Verify Step (Scene 6)
Ties the entire flow together: push config → run health checks → show summary box with next steps. The "next steps" section (`Message your bot on Telegram`, `Run ocm status`, `Run ocm machines ssh`) is exactly what a user needs post-setup.

### Status Dashboard (Scene 7)
The per-model usage breakdown (`claude-sonnet-4-6: $1.89` vs `claude-haiku-4-5: $0.45`) gives users cost visibility they don't have today. Strong addition.

### Doctor Command (Scene 8)
Clean, sequential, actionable. Each check has a description + context on the right. Warnings include remediation instructions ("run `ocm login` to refresh") — tell users what to do, not just what's wrong.

---

## Refinements

### Scene 0 (First Run) — Command List Is Too Flat

14 commands shown equally is overwhelming for a first-time user. Consider grouping:

```
Getting started:
  setup        Guided setup wizard
  status       Machine dashboard
  doctor       Diagnose configuration problems

Machines:
  machines     Create, start, stop, ssh
  providers    Manage LLM credentials
  channels     Manage messaging channels
  ...
```

Or show fewer commands by default and point to `ocm --help` for the full list. The gold `First time? Run: ocm setup` callout is already doing the right thing — lean into that more aggressively by dimming or hiding the plumbing commands on first run.

### Scene 3 (Provider) — OpenAI Path Not Shown

The Anthropic auth choice (`setup-token` vs `API key`) is well-designed. But the prototype doesn't show what happens if the user selects OpenAI. Since `chatgpt setup-token` doesn't exist, OpenAI should only offer the API key path (no subscription option). The prototype should either:

- Add a scene showing the OpenAI flow (just API key, no subscription prompt)
- Or note that OpenAI subscription auth is not yet supported

### Scene 7 (Status) — Credential Type Is Implementation Detail

Shows `subscription_key` in the provider row. Users don't care about credential types — the proxy does. Simplify to:

```
● anthropic   ····sk4f   validated 3m ago
```

Drop the `subscription_key` / `api_key` label. If it becomes relevant for debugging, `ocm doctor` is the right place for that detail.

### Scene 8 (Doctor) — "No Skills" Warning Is Premature

A freshly created machine with no skills is expected. Warning about it can confuse first-time users who just completed setup. Consider:

- Make it INFO instead of WARN for machines less than 24h old
- Or only warn if the machine has been running >24h with no skills configured

---

## What's Missing

### 1. Error States

Every scene shows the happy path. The prototype needs scenes (or annotations) for:

- **Login timeout** — browser never completes auth
- **Key validation failure** — wrong key, quota exceeded, network error
- **Machine creation failure** — quota limit, billing issue
- **Config push failure** — machine not running, network error

Error UX is where users spend most of their time. The happy path is easy; the error path is where trust is built or lost.

### 2. Re-Run Behavior (`ocm setup` on Existing Machine)

What happens when a user runs `ocm setup` again? It should:

- Skip login (already authenticated)
- Offer to pick the existing machine or create a new one
- Show completed steps as pre-filled, let user add another provider or channel
- Not force the user through steps they've already done

This is the "I want to add OpenAI as a second provider" use case.

### 3. `ocm logs` Scene

Conspicuously absent. This is the #1 debugging tool after `doctor`. Should show:

```
$ ocm logs -f
2026-02-28T14:22:01Z [gateway] agent session started (channel=telegram)
2026-02-28T14:22:02Z [gateway] LLM request: anthropic/claude-sonnet-4-6
2026-02-28T14:22:04Z [gateway] LLM response: 847 tokens (1.2s)
```

Streaming logs with timestamps, component tags, and readable output.

### 4. `ocm machines ssh` Scene

Mentioned in the verify next-steps but never shown. It's one of the five commands users actually run day-to-day. Should show the CF Access auth flow and shell landing.

### 5. Non-Interactive Mode

The docs propose:

```
ocm setup --provider anthropic --key sk-ant-... --channel telegram --token 123:ABC --name "My Bot"
```

Critical for CI/CD and scripting. Not prototyped, but should be documented alongside the interactive flow so implementers know both paths are required.

### 6. Google Provider

Only Anthropic, OpenAI, and Google are listed as LLM providers. Google/Gemini proxy support exists in the codebase but hasn't been E2E tested yet. Should be the next provider wired up after OpenAI keys are fixed.

---

## Design Principles Validated by Prototype

1. **Match the user's mental model** — "set up my bot" not "manage credentials and link them"
2. **Fewer commands that do the complete job** — `setup`, `status`, `doctor` are the core three
3. **Auto-push on every mutation** — the verify step handles this; individual commands should too
4. **Show next steps, not just results** — every completion screen tells the user what to do next
5. **Observability built in** — `status` and `doctor` are first-class commands, not afterthoughts

---

## Summary

The prototype is a strong design direction. The wizard flow, visual polish, and information hierarchy are production-quality. The main work remaining:

1. Define error states for each step
2. Design re-run behavior for existing machines
3. Add scenes for `ocm logs` and `ocm machines ssh`
4. Decide which plumbing commands stay in top-level help vs get pushed behind subcommands
5. Document non-interactive mode for scripting
