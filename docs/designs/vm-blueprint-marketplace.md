# VM Blueprint Marketplace

**Date:** 2026-04-08
**Status:** Concept design
**Scope:** Publishing configured OpenClaw VMs as reusable marketplace entries.

## Concept

A user configures an OpenClaw VM, then publishes it to a marketplace so another user can import it and start from the same setup.

The default artifact should be a portable **VM blueprint**, not a full mutable VM disk image.

Blueprints are derived from the VM's on-disk OpenClaw config and selected workspace files. They declare software, plugin, service, and secret requirements. They do not copy secrets, account identity, machine identity, billing state, private data, or proprietary assets unless the marketplace entry is explicitly a restricted private snapshot.

This design depends on `docs/designs/disk-config-source-of-truth.md`: after first boot, the VM's on-disk config is the source of truth. Marketplace export should read from that disk config, not reconstruct the VM from DB state alone.

## Product Shapes

### Public Blueprint

Portable and safe by default.

Contains:

- Sanitized `openclaw.json` sections.
- Agent definitions, prompts, model preferences, plugin entries, skill config, browser config, and channel templates.
- Optional starter workspace files such as `SOUL.md`, docs, templates, example scripts, and lockfiles.
- Required secrets as declarations.
- Required base tools and plugins as declarations.
- OpenClaw runtime compatibility.
- Marketplace metadata: name, description, author, categories, screenshots, changelog, version.

Does not contain:

- API keys or OAuth tokens.
- Gateway auth tokens.
- Device identity.
- Account IDs, machine IDs, host IDs, tunnel domains, proxy URLs.
- Billing or entitlement state.
- Customer data, production backups, private embeddings, or licensed datasets.
- Arbitrary global install scripts.
- Proprietary binaries unless separately licensed and distributed through an approved private artifact mechanism.

### Private Environment Snapshot

Restricted and heavier.

Useful for teams or verified partners that need installed software, local services, or prebuilt datasets that cannot be represented as a portable blueprint.

Requires:

- Publisher verification or private org scope.
- Secret and identity scrubbing.
- Malware scanning.
- License and entitlement checks.
- Provenance and signing.
- Storage lifecycle and revocation policy.
- Clear warning that import runs from a captured environment, not a declarative template.

This should not be the public marketplace default.

## Blueprint Bundle

Example:

```json
{
  "schemaVersion": "ocm.blueprint.v1",
  "name": "Research Analyst Agent",
  "description": "A VM setup for web research, citation drafting, and report generation.",
  "openclawVersion": ">=2026.4.8",
  "config": {
    "agents": {},
    "models": {},
    "plugins": {},
    "skills": {},
    "browser": {}
  },
  "requiredSecrets": [
    {
      "id": "openai-api-key",
      "label": "OpenAI API Key",
      "provider": "openai"
    }
  ],
  "requiredPlugins": [
    {
      "id": "opik-openclaw",
      "version": "0.2.9",
      "source": "bundled"
    }
  ],
  "software": [
    {
      "type": "base-tool",
      "name": "python3"
    }
  ],
  "workspaceFiles": [
    {
      "path": "SOUL.md",
      "contentRef": "files/SOUL.md"
    }
  ]
}
```

## Publish Flow

1. User clicks "Publish blueprint" from a configured VM.
2. Backend reads the current on-disk OpenClaw config from the VM.
3. Backend scans selected workspace files and installed software declarations.
4. Backend sanitizes the export:
   - remove secrets
   - remove machine/account identity
   - remove gateway auth and tunnel/proxy fields
   - remove device identity
   - validate allowed config paths
   - detect required credentials
   - detect proprietary or blocked assets
5. User chooses what to include:
   - agents
   - plugins
   - skills
   - model preferences
   - workspace starter files
   - channel templates
   - software dependency declarations
6. System validates schema and runs security checks.
7. Blueprint is stored with status `draft`, `private`, `unlisted`, `public`, or `verified`.

## Import Flow

1. User opens marketplace item.
2. System shows required secrets, plugins, software, service bindings, model access, runtime compatibility, and estimated cost/tier.
3. User chooses machine size and OpenClaw runtime version.
4. User supplies their own secrets and service connections.
5. Backend creates a new machine with the blueprint as seed input.
6. First boot writes the imported config to `/data/ocm/configs/<timestamp>/openclaw.json`.
7. The imported VM owns its disk config after boot.

## Software Handling

Software should be declared separately from OpenClaw config.

### Base Image Tools

For tools already present in the rootfs:

```json
{
  "requires": {
    "baseTools": ["node", "python3", "ffmpeg"]
  }
}
```

Import checks compatibility. It does not reinstall them.

### OpenClaw Plugins

Catalog or bundled plugins are the safest software dependency type.

The blueprint declares plugin IDs, versions, and required secrets. Import enables the plugins and prompts for credentials.

### Workspace Dependencies

Project-local dependencies are allowed through workspace files:

- `package.json`
- `pnpm-lock.yaml`
- `requirements.txt`
- `uv.lock`
- `pyproject.toml`

Import can either leave a setup task for the user or run a controlled dependency install step with explicit approval.

### Declarative Provisioning Recipe

Allowed for later MVP stages, but should be declarative rather than arbitrary shell:

```json
{
  "software": [
    {
      "type": "apt",
      "name": "ripgrep",
      "version": ">=14"
    },
    {
      "type": "npm",
      "name": "playwright",
      "scope": "workspace"
    }
  ]
}
```

The backend or agent translates allowed recipe entries into approved install commands. The import UI should show exactly what will be installed.

### Full Image or Disk Snapshot

Full snapshots capture installed software and local services, but they carry much higher security, storage, and legal risk.

Allow only for private org marketplaces or verified publishers.

## Proprietary Databases and Licensed Software

Proprietary databases, licensed runtimes, commercial datasets, and private indexes must not be copied into a public blueprint by default.

Represent them as one of three models.

### External Service Binding

Default for proprietary databases.

The blueprint declares that it needs a database connection. The importer provides their own entitlement and connection string.

Example:

```json
{
  "services": [
    {
      "name": "acme-vector-db",
      "mode": "external",
      "requiresSecrets": ["acme-connection-string"]
    }
  ]
}
```

### Private Artifact Install

For private/org marketplaces.

The blueprint references a private installer or artifact, but OCM does not redistribute the proprietary binary.

Example:

```json
{
  "software": [
    {
      "type": "vendor",
      "name": "AcmeVectorDB",
      "installMode": "private-artifact",
      "requiresSecrets": ["acme-license-key", "acme-artifact-token"]
    }
  ]
}
```

The importer must have access and provide the required license or artifact token.

### Private Snapshot

For cases where the proprietary software is already installed and configured locally.

Only allow in restricted scopes. Require license verification, secret scrubbing, scanning, and publisher attestation.

## Data Handling

Separate schema from data.

Allowed:

- Schema and migrations owned by the publisher.
- Synthetic sample data.
- Public datasets with compatible licenses.
- Small demo indexes generated from allowed data.

Blocked by default:

- Customer data.
- Production database backups.
- Private embeddings.
- Licensed datasets without importer's entitlement.
- Data containing secrets or personal information.

Licensed seed data can be supported only if the importer provides entitlement and the data is fetched from an approved source during import.

## Security Policy

Public blueprint exports should block or heavily review:

- `gateway.*`
- auth tokens and secret provider commands
- tunnel/proxy config
- account, billing, or entitlement config
- arbitrary plugin install sources
- shell startup hooks
- systemd services
- files outside approved workspace/config paths
- global package installs without an allowed recipe
- binaries and archives
- private data directories

Marketplace import should show:

- what config paths will be written
- what secrets are required
- what software will be installed or checked
- what services must be connected
- whether the item is public blueprint, private blueprint, or private snapshot

## Architecture Size

MVP public blueprints are medium-sized:

- Add blueprint schema and storage.
- Export sanitized disk config.
- Include selected workspace files.
- Declare required secrets/plugins/base tools.
- Import blueprint as first-boot seed config.
- Add marketplace listing and import UI.

Private snapshots are large:

- Snapshot capture and restore pipeline.
- Scanning and signing.
- License/entitlement workflow.
- Secret and data scrubbing.
- Storage and lifecycle management.
- Stronger publisher trust model.

Recommended sequence:

1. Public/private blueprint MVP with no arbitrary software installs.
2. Workspace files and required-secret prompts.
3. Catalog/bundled plugin dependencies.
4. Base tool compatibility checks.
5. Declarative provisioning recipes with explicit import approval.
6. Private artifact installs for org marketplaces.
7. Verified private snapshots only if the product need is strong enough.

## Open Questions

- Should blueprint export be allowed from a stopped VM, or only from a running VM where current disk config can be read?
- Should marketplace items be versioned independently from OpenClaw runtime versions?
- Should imports pin OpenClaw runtime version or allow compatible upgrades?
- Should private snapshots be a separate product SKU from blueprints?
- How much installed software detection should the first export flow attempt?
