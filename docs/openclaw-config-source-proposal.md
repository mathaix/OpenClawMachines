# Feature Request: Pluggable Config Sources

> **Submission format:** This is a GitHub Discussion post (per CONTRIBUTING.md: "New features or architecture changes → Start a GitHub Discussion or ask in Discord first"). A PR will follow if there's interest from maintainers.

## Summary

Extract config loading in `server.impl.ts` behind a small `ConfigSource` interface so OpenClaw can load configuration from sources other than local files — starting with HTTP endpoints. The primary motivation is security: keeping secrets (API keys, bot tokens, OAuth credentials) out of the filesystem entirely by fetching them from a config server at runtime.

## Problem

OpenClaw's config loading is hardcoded to local files. This means API keys, bot tokens, and OAuth credentials must exist as files on disk — readable by any process in the environment. When running OpenClaw inside a VM, container, or any sandboxed context, this is a security concern: a compromised process or a malicious tool can read the config file and exfiltrate credentials.

The config server pattern eliminates this attack surface. Instead of writing secrets to disk, a trusted host-side service serves config over HTTP at runtime. Secrets exist only in memory after the fetch — never on the guest filesystem. This is the same pattern used by:

- **Spring Cloud Config Server** — Java/Spring applications fetch config (including encrypted secrets) from a centralized HTTP endpoint at boot. The config server decrypts on the fly; the application never sees the raw secret file. This is the standard approach for secret isolation in Spring Boot deployments.
- **HashiCorp Vault Agent** — Applications fetch secrets via HTTP API. Vault handles encryption, access control, and audit logging. The application process holds secrets in memory only.
- **Kubernetes Secrets with CSI Driver** — Secrets are projected into pods via tmpfs (memory-backed), never written to persistent disk. The kubelet fetches them from the API server over HTTPS.
- **AWS IMDSv2 / GCP Metadata Server** — Cloud VMs fetch instance credentials from a link-local HTTP endpoint (`169.254.169.254`). Credentials are never stored on disk — the metadata service injects them per-request with short TTLs.

The common principle: **secrets should be fetched, not stored.** OpenClaw currently requires storage. This proposal adds the fetch path.

Today the only workaround is patching `server.impl.ts` directly, which is fragile and creates merge conflicts on every upstream release.

## How this aligns with OpenClaw's vision

From VISION.md:

- **"Security and safe defaults"** (high priority) — This directly addresses credential exposure. Today, running OpenClaw with agent tools means API keys sit on disk where any tool or subprocess can read them. A config server source keeps secrets out of the filesystem entirely.
- **"Plugin-first approach, lean core"** — This adds one interface and extracts existing code. It makes the core *leaner* by pulling config-loading logic out of `server.impl.ts` into focused modules. `HttpConfigSource` could ship as a separate npm package if preferred.
- **"Setup reliability and first-run user experience"** — No impact on self-hosted setup. Default behavior is unchanged (file-based, no env vars needed).

### Why this belongs in core, not as a plugin

Config loads *before* plugins — the gateway needs config to know which plugins to load. A plugin can't provide the config source because the plugin system depends on config already existing.

```
Boot sequence:
1. resolveConfigSource()           ← picks source
2. configSource.read()             ← config loaded
3. loadGatewayPlugins(config)      ← plugins need config
4. configSource.startWatcher()     ← reload starts
```

The interface itself is tiny (two methods). The real code is the implementations, and `FileConfigSource` is just existing code extracted — not new logic.

## Architecture

This uses the **Strategy pattern** — the same approach used by Spring's `PropertySource`, Go's `io.Reader`, and database driver interfaces everywhere. The gateway doesn't know or care where config comes from; it talks to the `ConfigSource` interface.

```
                        ┌─────────────────────────────┐
                        │      Environment Variable     │
                        │   OPENCLAW_CONFIG_SOURCE=?    │
                        └──────────────┬──────────────┘
                                       │
                                       ▼
                        ┌──────────────────────────────┐
                        │     resolveConfigSource()     │
                        │         (factory)             │
                        └──────────────┬───────────────┘
                                       │
                          ┌────────────┴────────────┐
                          │                         │
                    "file" (default)              "http"
                          │                         │
                          ▼                         ▼
               ┌──────────────────┐     ┌───────────────────┐
               │ FileConfigSource │     │ HttpConfigSource   │
               │                  │     │                    │
               │ • readSnapshot() │     │ • fetch /config    │
               │ • chokidar watch │     │ • poll /version    │
               │ • legacy migrate │     │ • custom headers   │
               │ • plugin auto-   │     │                    │
               │   enable         │     │ Secrets fetched    │
               │                  │     │ over HTTP, held    │
               │ Config lives on  │     │ in memory only —   │
               │ disk as today    │     │ never on disk      │
               └────────┬─────────┘     └────────┬──────────┘
                        │                         │
                        └────────────┬────────────┘
                                     │
                          implements ConfigSource
                                     │
                                     ▼
                        ┌──────────────────────────┐
                        │    server.impl.ts         │
                        │                           │
                        │  source.read()            │  ← startup
                        │  loadGatewayPlugins(cfg)  │  ← plugins load AFTER config
                        │  source.startWatcher()    │  ← live reload
                        └───────────────────────────┘
```

**Key point:** Config loads *before* plugins. The plugin system depends on config to know which plugins to enable. This is why `ConfigSource` is a core abstraction (strategy pattern + env var), not a plugin. Same architectural layer as Spring's `PropertySource` — it's bootstrap infrastructure, not application-level extensibility.

Adding a new source (e.g., Consul, Vault) means: implement the interface, add a case to the factory, document the env var. No changes to `server.impl.ts` or the plugin system.

## Proposal

### Interface

One file: `src/config/config-source.ts`

```typescript
export interface ConfigSource {
  /** Read the current configuration snapshot. */
  read(): Promise<ConfigFileSnapshot>;

  /**
   * Start watching for config changes.
   * Calls onChanged() when the source detects a new version.
   * Returns a handle to stop watching.
   */
  startWatcher(opts: {
    onChanged: () => Promise<void>;
  }): { stop: () => void };
}
```

### Built-in Implementations

**`FileConfigSource`** — Extracted from existing code in `server.impl.ts`. Uses `readConfigFileSnapshot()` for reading and chokidar for file watching. This is the default. Zero behavior change for existing users. Legacy migration, plugin auto-enable, and config writing stay inside this implementation — they only apply to local files.

**`HttpConfigSource`** — Fetches config from an HTTP endpoint. Polls a version endpoint for changes. No new dependencies (`fetch()` is built-in since Node 18).

### Source Resolution

A factory function selects the source based on environment variables:

```typescript
export function resolveConfigSource(env: Record<string, string | undefined>): ConfigSource {
  const source = env.OPENCLAW_CONFIG_SOURCE ?? 'file';

  switch (source) {
    case 'file':
      return new FileConfigSource();
    case 'http': {
      const url = env.OPENCLAW_CONFIG_URL;
      if (!url) throw new Error('OPENCLAW_CONFIG_URL required when OPENCLAW_CONFIG_SOURCE=http');
      return new HttpConfigSource(url, {
        headers: env.OPENCLAW_CONFIG_HEADERS
          ? JSON.parse(env.OPENCLAW_CONFIG_HEADERS)
          : undefined,
        pollIntervalMs: env.OPENCLAW_CONFIG_POLL_MS
          ? parseInt(env.OPENCLAW_CONFIG_POLL_MS, 10)
          : 5000,
      });
    }
    default:
      throw new Error(`Unknown config source: ${source}. Expected 'file' or 'http'.`);
  }
}
```

### Changes to `server.impl.ts`

The diff to the gateway entry point becomes smaller, not larger:

```typescript
// Before (current — config loading inlined, ~100 lines)
let configSnapshot = await readConfigFileSnapshot();
// ... legacy migration, validation, plugin auto-enable ...
cfgAtStart = loadConfig();

// After (~10 lines)
const configSource = resolveConfigSource(process.env);
const configSnapshot = await configSource.read();
if (!configSnapshot.valid) {
  throw new Error(`Invalid config: ${configSnapshot.issues.map(i => i.message).join(', ')}`);
}
cfgAtStart = configSnapshot.config;

// ... later, for reload ...
configSource.startWatcher({ onChanged: reloadConfig });
```

## Design Decisions

### Why environment variables for source selection?

Environment variables are the standard for container configuration and are already how OpenClaw handles deployment-specific settings (`OPENCLAW_*`). They're set once at deployment time and don't depend on the config file existing (which would be circular for non-file sources).

### Why polling for HTTP reload (not WebSocket/SSE)?

Polling is simpler, more reliable across network boundaries, and matches established patterns (Spring Cloud Config, Kubernetes kubelet). WebSocket/SSE can be added later without changing the interface — `startWatcher` abstracts the mechanism.

## Prior Art

The `ConfigSource` interface follows the same pattern as Spring Boot's `PropertySource` abstraction — arguably the most battle-tested config source pattern in production software:

**Spring Cloud Config Server** (`spring-cloud-config`):
- Applications declare a `spring.config.import=configserver:http://config-server:8888` bootstrap property
- At startup, the Spring `Environment` fetches config from the server via `GET /{application}/{profile}`
- Secrets are encrypted at rest on the server (`{cipher}` prefix) and decrypted on fetch — the application never sees the encrypted form
- Live reload via `POST /actuator/refresh` or Spring Cloud Bus (AMQP/Kafka broadcast)
- The `PropertySource` interface is what makes this pluggable — file, HTTP, Git, Consul, Vault, and JDBC are all implementations of the same abstraction
- Used in production by Netflix, Alibaba, and most Spring Boot microservice deployments

**Other systems using the same pattern:**

| System | Secret isolation method | Reload |
|--------|------------------------|--------|
| HashiCorp Vault | HTTP API with lease-based TTLs, audit logging | Lease renewal + polling |
| AWS IMDSv2 | Link-local HTTP (`169.254.169.254`), session tokens, short TTLs | Per-request fetch |
| GCP Metadata Server | Link-local HTTP (`169.254.169.254`), identity tokens | Per-request fetch |
| K8s Secrets + CSI | tmpfs mount (memory-backed), never persistent disk | kubelet watch API |

## Backward Compatibility

- **Default behavior is unchanged.** `OPENCLAW_CONFIG_SOURCE` unset or `file` → existing file-based loading. No env var needed. Self-hosted users notice nothing.
- **No config schema changes.** The same `OpenClawConfig` object is produced regardless of source.
- **No new dependencies.** `fetch()` is built-in (Node 18+). Polling uses `setInterval`.
- **File-specific features (legacy migration, plugin auto-enable, config writing) stay in `FileConfigSource`** — they don't apply to remote sources and don't leak into the interface.
- **Security improvement is opt-in.** Users who want to keep secrets off disk set one env var. Users who don't care continue as-is.

## HTTP Config Source Protocol

For anyone building a config server compatible with `HttpConfigSource`:

### `GET {url}/config`

Returns the full OpenClaw configuration as JSON.

```
Response: 200 OK
Content-Type: application/json

{ /* OpenClawConfig object */ }
```

### `GET {url}/version`

Returns the current config version number. The client polls this and refetches `/config` when the version changes.

```
Response: 200 OK
Content-Type: application/json

{ "version": 42 }
```

Version is an opaque integer — any change triggers a reload.

### Authentication

Clients pass custom headers via `OPENCLAW_CONFIG_HEADERS` (JSON object). Supports bearer tokens, API keys, or any header-based auth.

## Scope

**In this PR (if greenlit):**
- `ConfigSource` interface (~15 lines)
- `FileConfigSource` (extracted from existing code, not new logic)
- `HttpConfigSource` (new, ~120 lines)
- `resolveConfigSource()` factory (~20 lines)
- `server.impl.ts` refactored to use `ConfigSource` (net reduction in lines)
- Tests for both implementations
- Docs for env vars and HTTP protocol

**Not in this PR:**
- Other source types (Consul, Vault, etcd) — community can add via the interface
- Config write-back for remote sources
- WebSocket/SSE-based live reload
- Changes to the plugin system

## Testing

- `pnpm build && pnpm check && pnpm test` passes — `FileConfigSource` is extracted code, not new logic
- New unit tests for `HttpConfigSource`: happy path, network error, HTTP error, invalid JSON, version polling, watcher lifecycle
- New unit tests for `resolveConfigSource()`: default → file, `http` → HttpConfigSource, missing URL → error
- Integration test: boot gateway with `OPENCLAW_CONFIG_SOURCE=http` pointing at a local test server, verify startup + live reload

## Open Questions for Maintainers

1. **Naming**: `OPENCLAW_CONFIG_SOURCE` or prefer a different env var convention?
2. **HttpConfigSource in core or separate package?** Happy to ship it as `@openclaw/config-source-http` if the team prefers keeping core minimal, with only the interface + `FileConfigSource` in core.
3. **Any concerns about the `ConfigFileSnapshot` type being the interface contract?** It's already the internal type — reusing it avoids a new abstraction layer, but if the team wants a cleaner boundary type, happy to discuss.

---

*I have a reference implementation PR that shows the full diff: [PR #25769](https://github.com/openclaw/openclaw/pull/25769). Happy to iterate on design feedback and do the implementation work.*
