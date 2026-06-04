# Browser VM lifecycle

The browser lives in a **separate VM** with its own Chrome and its own
state. Understanding that lifecycle helps you give accurate answers when
users ask what survives what.

## Pair, unpair, repair

- **Pair** — the admin attaches a browser VM to this machine. `bh --check`
  starts returning 0 within seconds.
- **Unpair** — the admin detaches it. `bh --check` starts failing
  immediately. The browser VM may continue running (paired to someone
  else) or be idle.
- **Repair** — unpair + pair again. New browser VM → fresh Chrome, no
  cookies, no prior tabs. Same browser VM → same Chrome, same state.

Users don't typically unpair/repair mid-session. If `bh --check` suddenly
fails partway through a session, most likely a browser-VM crash — surface
that to the user, don't assume it's transient.

## What survives a this-machine restart

- Browser VM pairing **is preserved** — the admin's pairing record lives in
  the control plane, not in this machine's state.
- Cookies, sessions, and local storage **in Chrome** are preserved as long
  as the browser VM itself keeps running. A restart of *this* machine does
  not restart Chrome.

## What survives a browser-VM restart

- **Nothing automatic.** Chrome restarts cold: empty profile, no cookies,
  no tabs. If a user had logged in manually earlier, they need to log in
  again after a browser-VM restart.
- The pairing itself survives — so `bh --check` keeps passing — but the
  underlying Chrome is new.

## What survives unpairing

- **Nothing.** Once unpaired, this machine has no way to reach that Chrome.
  If a new browser VM is paired, everything starts fresh.

## Restarting Chrome without losing the browser VM

Not supported from this machine. Chrome lifecycle is managed by the browser
VM itself. If Chrome wedges, the user restarts the browser VM from the
admin UI.

## Practical guidance for agents

- **Login flows:** if a task needs the user logged in, verify they're
  already logged in (`page_info()` on a site that shows login state) before
  trying to use authenticated features. Don't attempt to type their
  password — credentials are the user's responsibility and may be
  invalidated after a browser-VM restart.
- **Long-running jobs:** if a job takes many minutes and needs the browser
  mid-flight, it's fine as long as pairing holds. Re-check `bh --check`
  after long idle periods only if you suspect disruption.
- **Headless vs. headful:** the platform runs Chrome headful under Xvfb
  (for video capture / Neko streaming). You can't switch it.
