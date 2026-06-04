# PR2 — RouteService & Projector (DB Truth)

Goal: make Postgres authoritative for routing; KV/tunnel become projections managed by a projector + reconciler.

Scope
- Add `internal/routing/service` and `internal/routing/projector`.
- Handlers and RuntimeService call RouteService only (no direct KV/tunnel writes).
- KV/tunnel writes moved into projector; reconciler repairs drift.

Checklist
- [ ] RouteService resolves routes from DB; KV read is cache-only.
- [ ] Projector writes KV + ensures tunnel/DNS; errors logged/metricized.
- [ ] Drift reconciler repairs stale KV/tunnel; test covers stale/delete.
- [ ] Handlers/runtime contain zero KV/tunnel writes.
- [ ] API contract unchanged (route resolve/start/stop/delete parity).

Cutover
- Shadow mode flag: run old+new, compare; then flip to new once drift=0.

Verification
- API tests for resolve/start/stop/delete pass with projector enabled.
- Metrics show drift count remains 0 under load test.

