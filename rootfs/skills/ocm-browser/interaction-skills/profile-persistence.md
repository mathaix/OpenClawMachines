# Browser Profile Persistence

Use this when you need to save or restore browser cookies/localStorage from an
OpenClaw Machine browser session.

## Storage Path

Store profile blobs under:

```bash
/home/openclaw/.openclaw/browser-profiles/
```

This directory lives on the persistent data volume. Avoid generic
`~/.browser-harness/profiles/` paths unless the user explicitly wants a
non-OCM-local profile location.

## Save

```bash
bh <<'PY'
import gzip, json, pathlib

out = pathlib.Path("/home/openclaw/.openclaw/browser-profiles/default.json.gz")
out.parent.mkdir(parents=True, exist_ok=True)
out.parent.chmod(0o700)

blob = {
    "cookies": cdp("Network.getAllCookies")["cookies"],
    "origins": {},
}

for origin in ["https://www.linkedin.com", "https://x.com"]:
    new_tab(origin)
    wait_for_load()
    raw = js("JSON.stringify(Object.fromEntries(Object.entries(localStorage)))")
    blob["origins"][origin] = {"localStorage": json.loads(raw) if raw else {}}

out.write_bytes(gzip.compress(json.dumps(blob).encode()))
out.chmod(0o600)
print(f"saved {len(blob['cookies'])} cookies to {out}")
PY
```

Before saving, narrow the origin list to the sites the user actually needs.
Never print raw cookie or localStorage values.

## Restore

```bash
bh <<'PY'
import gzip, json, pathlib

path = pathlib.Path("/home/openclaw/.openclaw/browser-profiles/default.json.gz")
blob = json.loads(gzip.decompress(path.read_bytes()))

cdp("Network.setCookies", cookies=blob["cookies"])
print(f"restored {len(blob['cookies'])} cookies")

for origin, data in blob.get("origins", {}).items():
    new_tab(origin)
    wait_for_load()
    for key, value in data.get("localStorage", {}).items():
        js(f"localStorage.setItem({json.dumps(key)}, {json.dumps(value)})")
    goto(origin)
    wait_for_load()
    print(f"{origin}: {page_info().get('title', '?')}")
PY
```

Order matters: restore cookies before loading the site, then visit each origin,
write that origin's localStorage, and reload so the app initializes with both
state layers present.
