# PR5 — Runtime Surface Tightening & Config Typing

Goal: reduce runtime attack surface and make config assembly typed/predictable.

Scope
- Move internal VM services (metadata, gateway) to Unix sockets; expose only user app ports via preview policy.
- Per-VM nonce/local credential auth replaces source-IP identity; tighten bridge anti-spoofing.
- Typed config assembly: capabilities → typed intermediate → rendered JSON; remove ad-hoc merges in handlers/runtime.

Checklist
- [ ] Internal services bound to Unix sockets; port preview cannot reach them (test covers).
- [ ] Metadata/proxy auth uses nonce/credential; source-IP treated as hint only.
- [ ] Bridge rules enforce per-TAP anti-spoofing.
- [ ] Config assembly uses typed structs; tests for fixture capabilities and regression cases.
- [ ] Handlers/runtime no longer perform JSON merge logic.

Cutover
- Feature flags: `runtime.surface_hardening`, `config.typed_pipeline`.
- Enable in staging first; verify terminals/proxy still function for OVH/Hetzner/GCP machines.

Verification
- Tests: preview path isolation, spoofing prevention, config regression suite.
- Load: synthetic traffic with terminals + port preview; ensure latency within SLOs.

