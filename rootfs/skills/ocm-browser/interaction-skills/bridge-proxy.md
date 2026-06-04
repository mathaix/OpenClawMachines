# Bridge CDP proxy

From inside an OpenClaw machine, Chrome lives in a separate **browser VM**.
The machine reaches it through a managed CDP proxy at
`http://192.168.100.1:9222`. That address is the VM-to-bridge gateway IP on
the internal bridge network — not a public IP, not a DNS name, not
something you configure.

## What the bridge does

- Listens at `192.168.100.1:9222` for CDP traffic from this machine.
- Looks up the source IP of the request, maps it to whichever browser VM
  is currently paired with this machine, and forwards TCP to that VM's
  Chrome on the same port.
- Returns 404 / refuses connection when no browser VM is paired.

The practical effect: `curl http://192.168.100.1:9222/json/version` from
this machine returns Chrome's real `webSocketDebuggerUrl` (when paired),
and a WebSocket to that URL gets you a real CDP session. The `bh` wrapper
handles this handoff for you.

## Why you care

- **Don't hardcode a different IP.** The bridge IP is the only supported
  way to reach Chrome. There is no public-internet CDP endpoint.
- **Don't assume `localhost:9222`.** Chrome is not on this machine — you
  cannot `--remote-debugging-port=9222` a local Chrome and expect
  `browser-harness` to find it.
- **`webSocketDebuggerUrl` is dynamic.** Every Chrome restart gives you a
  new UUID. `bh` resolves this at invocation time; do not cache the URL
  across `bh` calls in a long script.

## SSRF policy

The browser's CDP endpoint is behind a fail-closed SSRF policy. The bridge
gateway IP (192.168.100.1) is explicitly allowlisted by the platform in the
gateway config so the browser plugin can hit it. **Arbitrary RFC1918
addresses are blocked** — you cannot ask Chrome to fetch `http://10.x`
resources to exfiltrate internal state from other machines.

If a user asks you to screenshot, download, or scrape something that would
require reaching an internal-only service, refuse or explain the constraint
rather than assuming you can bypass it.

## What NOT to do

- Don't try to hit `http://192.168.100.1:9222` from a remote host — it's
  only reachable from inside this machine.
- Don't write code that opens WebSockets directly. Use `bh` / the
  `browser-harness` helpers.
- Don't try to "fix" the SSRF guard by setting `ssrfPolicy.allowedHostnames`
  in the gateway config — that config is platform-protected.

## For diagnostics only

```sh
curl -sf http://192.168.100.1:9222/json/version | jq .
```

returns Chrome version + `webSocketDebuggerUrl`. Useful for debugging a
suspected bridge issue. Don't build real features on top of raw curl —
use the harness.
