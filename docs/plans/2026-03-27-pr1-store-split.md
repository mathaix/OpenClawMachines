# PR1 — Store Split & Handler Delegation

Goal: replace `internal/store.Store` usage in handlers with narrow repos and services; shrink `internal/api/server.go`.

Scope
- Carve repos: MachineRepo, HostRepo, PlacementRepo, RouteRepo, ConfigRepo, CredentialRepo, EventRepo (reuse existing interfaces).
- Update `RuntimeService` and `PlacementService` constructors to take only needed repos.
- Refactor handlers to call services (no direct store calls).
- Drop dead event tables/queries and remove unused EventRepo methods while reorganizing store.

Checklist
- [ ] New repo structs wired (DI) in server bootstrap.
- [ ] `server.go` no longer imports full `store.Store`.
- [ ] RuntimeService tests updated/added for start/stop/delete.
- [ ] Handler API parity tests (start/stop/delete) passing.
- [ ] Legacy event tables removed or confirmed in use; EventRepo trimmed accordingly.
- [ ] No change to DB schema or API shapes.

Cutover
- Feature-gate not required; code path replaces old, parity-tested.

Verification
- Run unit/service/API tests for machines.
- Confirm `go vet`/`go test ./...` in backend succeeds.
