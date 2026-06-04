---
name: ocm-browser
description: Drive a browser from an OpenClaw machine. Companion to the browser-harness skill — explains the OCM-specific bridge proxy, the readiness check you must run first, and how pairing works. Start here before writing any browser code.
---

# ocm-browser

OpenClaw machines don't run Chrome themselves. When you need a browser, a
separate **browser VM** is paired with your machine and its Chrome is reached
via a managed CDP bridge at `http://192.168.100.1:9222`. The `browser-harness`
skill gives you the Python helpers (`click`, `js`, `screenshot`, `new_tab`,
`page_info`, `cdp`) that talk to that Chrome.

**Read this skill before writing browser code. Nothing below the readiness
check below is usable unless the check passes.**

## Readiness check — ALWAYS run first

```sh
bh --check
```

Exit 0 means a browser VM is paired and reachable. Exit 1 means there is no
browser to drive — stop, tell the user, do not claim you ran code you did
not run.

The probe mirrors the endpoint `bh` will actually use, so a 0 exit here is a
reliable green light.

## Minimum happy-path example

```sh
bh <<'PY'
new_tab("https://example.com")
wait_for_load()
info = page_info()
print(info["url"], info["title"])
PY
```

All helpers come from the `browser-harness` skill. `bh` wires up the bridge
CDP URL for you — you don't need to set `BU_CDP_WS` yourself.

## When to consult interaction-skills

- `interaction-skills/pairing-readiness.md` — if `bh --check` fails, or if
  the user asks "can I use a browser right now?"
- `interaction-skills/bridge-proxy.md` — if you need to understand what the
  bridge does, what network it exposes, SSRF policy, or why a request is
  blocked.
- `interaction-skills/browser-vm-lifecycle.md` — if the user asks about
  pairing/unpairing, what survives a VM restart, or how to restart Chrome
  without losing session state.
- `interaction-skills/profile-persistence.md` — if you need to **save** a
  logged-in browser session (LinkedIn, X/Twitter, Instagram, Facebook, most
  SaaS) and **restore** it after the browser VM gets recreated or a new one is
  paired.
- `browser-harness/interaction-skills/cookies.md` — generic Browser Harness
  cookie guidance.

## Important constraints

- Cookies and session state live **in the browser VM**, not this machine.
  They persist while the browser VM runs; they vanish if the user unpairs
  or recreates it. To keep them across unpair/recreate, save the cookie
  jar first — see `interaction-skills/profile-persistence.md`.
- The bridge has a fail-closed SSRF policy. Arbitrary internal IPs are
  blocked; the bridge's own RFC1918 address is allowlisted by the platform.
- You do **not** have direct TCP access to the browser VM's Chrome. Always
  go through `bh` / `browser-harness`, not through raw `curl` / `websockets`.

## Environment overrides (advanced)

- `OCM_CDP_BRIDGE_URL` — override the managed bridge URL used by `bh --check`
  when `BU_CDP_WS` isn't set. Default: `http://192.168.100.1:9222`.
- `BU_CDP_WS` — explicit CDP WebSocket URL. If set, `bh` uses it verbatim
  and `bh --check` probes the matching `/json/version` on the same host:port.

Use these only to target a custom remote Chrome (debugging, dev VMs). In
normal operation the defaults are correct.
