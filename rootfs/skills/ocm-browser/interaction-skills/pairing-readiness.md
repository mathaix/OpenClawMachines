# Pairing readiness

`bh --check` is the ground truth. If it exits 0, a browser VM is paired and
Chrome is reachable through the bridge. If it exits 1, there is no browser
to drive — stop, surface the issue to the user, don't fake a result.

## What failure looks like

```
$ bh --check
bh: no CDP reachable at http://192.168.100.1:9222/json/version
    A browser VM must be paired with this machine before bh can connect.
    Pair one from the OpenClaw admin UI, then rerun `bh --check`.
    Set BU_CDP_WS or OCM_CDP_BRIDGE_URL to target a different endpoint.
```

The probe URL printed in the message is the exact endpoint `bh` would use
at invocation time — a 1 exit here is reliable, not a flake.

## How pairing happens (user-side)

1. User visits the OpenClaw admin UI.
2. Picks or creates a browser VM and pairs it with this machine.
3. The control plane wires the CDP bridge — nothing to configure on the
   machine side.

This takes seconds to ~a minute for a new browser VM. Once paired, `bh
--check` starts returning 0.

## What to do when it fails

1. Tell the user plainly: "There's no browser VM paired with this machine.
   Pair one from the OpenClaw admin UI first."
2. Do **not** attempt to proxy, curl, or otherwise reach Chrome through a
   different route — there isn't one.
3. Do **not** pretend a result. Don't generate "example" outputs that look
   like real browser data.

## When `bh --check` passes but a subsequent `bh <<PY ... PY` fails

Rare but possible — the browser VM was unpaired between the readiness check
and the code call. Re-run `bh --check`; if it now fails, the pairing was
torn down. Tell the user.

## Automating the check in longer scripts

If you're running multiple `bh` commands back-to-back, a single `bh --check`
up front is enough — don't re-probe before every call (it's a TCP round-trip
to the bridge). If you're writing a long-lived script, gate the whole run on
one check at the top:

```sh
if ! bh --check; then
    echo "ocm-browser: no browser paired, aborting" >&2
    exit 1
fi
```
