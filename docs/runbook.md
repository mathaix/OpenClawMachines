# Operations Runbook

## 1. Quick Reference

Health check: `curl -s https://<backend-url>/health | jq .`

| Action | Command |
|--------|---------|
| Deploy everything | `make deploy-all` |
| Deploy backend only | `make deploy-backend` |
| Deploy frontend only | `make deploy-frontend` |
| Deploy worker only | `make deploy-worker` |
| View backend logs | `make logs-backend` |

Always commit before deploying -- version info is injected at build time.

## 2. Host Provisioning

Worker hosts are GCP Compute VMs running the orchestrator agent. Each host manages
multiple Firecracker MicroVMs. Hosts register themselves with the control plane on boot
and are assigned machines by the scheduler.

-> See [vm-provisioning.md](vm-provisioning.md) for setup steps and instance templates.

## 3. Machine Lifecycle

**CLI commands:**

```bash
ocm machines create --name my-machine --size standard   # sizes: standard, large, xlarge
ocm machines start SLUG
ocm machines stop SLUG
ocm machines delete SLUG
ocm machines list
ocm machines get SLUG
```

**API endpoints** (authenticated with Bearer token):

| Method | Path | Action |
|--------|------|--------|
| POST | `/api/machines` | Create (supports `auto_start`, `secrets` fields) |
| POST | `/api/machines/:id/start` | Start |
| POST | `/api/machines/:id/stop` | Stop |
| DELETE | `/api/machines/:id` | Destroy |

VMs boot in ~125ms via Firecracker. Running machines are accessible at `/workspace/{id}`.

## 4. Config Management

Config is assembled server-side from registry entries, account overrides, and machine-level
settings, then pushed live to running VMs via the agent API (`PATCH /vms/{machineID}/config`).

```bash
# Push config to a running VM (done automatically on capability enable/disable)
# Manual push: hit the control-plane endpoint which fans out to the agent
POST /api/machines/:id/config/push
```

-> See [CurrentFeature.md](CurrentFeature.md) for the config assembly pipeline design.

## 5. Build Artifacts & Rootfs

All build artifacts (rootfs, agent, CLI) are stored in `gs://openclawmachines/` and pulled by host VMs at runtime. Agent self-updates from GCS on boot and periodically.

- **Build everything:** `make build-components` (must run on Linux)
- **Rootfs only:** `make build-upload-rootfs` (must run on Linux)
- **List versions:** `make list-rootfs` / `make list-agent`
- **Rollback rootfs:** `make rollback-rootfs VERSION=xxx`
- **Rollback agent:** `make rollback-agent VERSION=xxx`
- **View current:** `make show-rootfs-manifest`

-> See [build-process.md](build-process.md) for full pipeline design, GCS layout, and host bootstrap sequence.

## 6. Credential Rotation

Credentials are encrypted at rest (`crypto.Encrypt`) and pushed to VMs as decrypted secrets
via the agent API. To rotate:

1. Update credential via `POST /api/credentials` (or `ocm` CLI)
2. Push updated secrets to running VMs: `POST /api/machines/:id/secrets/push`
3. Verify via agent logs (`secrets.push.completed`)

-> See [designs/account-credentials-and-billing.md](designs/account-credentials-and-billing.md).

## 7. Troubleshooting

**VM not provisioning**
Check GCP Console VM logs. Verify host is registered and has capacity. Look for
`vm.creating` / `vm.boot.completed` in agent logs.

**Gateway not starting**
Core gateway startup takes ~55s due to Node.js module loading. The `ocm_quick_start=1`
kernel arg skips sidecars but does not reduce core startup. Wait 90s before diagnosing.

**Tunnel issues**
The tunnel reaper automatically cleans orphaned tunnels (`tunnel.reaper.deleted_orphan`).
For manual debugging, check `tunnel.created` / `tunnel.deleted` log keys.
-> See [tunnel-architecture.md](tunnel-architecture.md), [debugging-tips.md](debugging-tips.md).

**Config not reloading**
Verify the config push endpoint returned 200. Check agent logs for `vm.config.updated`
and metadata logs for `metadata.config.updated`. Confirm the VM's metadata nonce matches.

**Stale rootfs / agent**
Agent changes require a new snapshot. Use the `/snapshot` skill. Verify the running version
via the version sidecar file checked at `vm.health.ok`.

## 8. Disaster Recovery

**Agent restart:**
The orchestrator persists VM state to a state file (`state.loaded`, `state.save.failed`).
On restart it reattaches to running VMs and cleans orphans (`vm.orphan.cleanup`).
Data volumes are preserved during orphan cleanup (`vm.orphan.data_volume.preserved`).

**Data volume rollback:**
Pre-upgrade backups are created automatically (`vm.data_volume.backup.creating`).
Rollback restores the `.pre-upgrade` snapshot (`vm.data_volume.rollback.completed`).
-> See [RECOVERY_AND_PERSISTENCE.md](RECOVERY_AND_PERSISTENCE.md).

**Host failure:**
The scheduler detects unresponsive hosts and re-places VMs on healthy hosts.
Data volumes on the failed host are lost unless backed up externally.

## 9. Monitoring

**Key structured log patterns** (Go `slog` dot-namespaced keys):

| Prefix | Subsystem | Key events |
|--------|-----------|------------|
| `vm.*` | VM lifecycle | `vm.creating`, `vm.boot.completed`, `vm.started`, `vm.destroyed` |
| `vm.health.*` | Health checks | `vm.health.ok`, `vm.health.failed` |
| `vm.orphan.*` | Orphan cleanup | `vm.orphan.cleanup`, `vm.orphan.kill_failed` |
| `tunnel.*` | CF Tunnels | `tunnel.created`, `tunnel.deleted`, `tunnel.reaper.*` |
| `rootfs.*` | Rootfs mgmt | `rootfs.gcs.staged`, `rootfs.gcs.download.completed` |
| `secrets.*` | Secret push | `secrets.push.completed`, `secrets.push.agent.failed` |
| `metadata.*` | Metadata svc | `metadata.registered`, `metadata.config.updated` |
| `state.*` | State persist | `state.loaded`, `state.save.failed` |
| `proxy.*` | WS proxy | `proxy.ws.closed`, `proxy.gateway.ws.upgrade` |

**Resource leak indicators:**
- Orphan VMs: rising count of `vm.orphan.cleanup` without corresponding `vm.destroyed`
- Stale tunnels: `tunnel.reaper.deleted_orphan` count increasing across sweeps
- Tap device leaks: `vm.cleanup.tap_remove_failed` warnings accumulating
- Failed rootfs cleanup: `vm.cleanup.rootfs_remove_failed` warnings
