---
title: "CLI Reference"
slug: "cli-reference"
order: 6
excerpt: "The ocm command-line tool for managing OpenClaw Machines from your terminal."
---

# CLI Reference

The `ocm` CLI lets you manage OpenClaw Machines directly from your terminal. It's ideal for power users, scripting, and CI/CD integration.

## Installation

Download the latest version:

```bash
# macOS / Linux
curl -fsSL https://storage.googleapis.com/ocm-artifacts/cli/install.sh | bash

# Or download directly
# The binary is available at:
# https://storage.googleapis.com/ocm-artifacts/cli/latest/ocm-{os}-{arch}
```

After installation, authenticate:

```bash
ocm auth login
```

This opens a browser window for Cloudflare Access authentication.

## Commands

### Machine Management

```bash
# List all machines
ocm machines list

# Create a new machine
ocm machines create --name "Research Agent" --size medium

# Start a machine
ocm machines start <machine-id>

# Stop a machine
ocm machines stop <machine-id>

# Delete a machine
ocm machines delete <machine-id>

# Get machine details
ocm machines get <machine-id>
```

### Configuration

```bash
# Push configuration to a machine
ocm config push <machine-id> --file config.yaml

# Pull current configuration
ocm config pull <machine-id>

# Set a secret
ocm secrets set <machine-id> ANTHROPIC_API_KEY=sk-...

# List secrets (names only, values hidden)
ocm secrets list <machine-id>
```

### Workspace Access

```bash
# Open SSH session to a machine
ocm ssh <machine-id>

# Stream machine logs
ocm logs <machine-id> --follow

# Open workspace in browser
ocm open <machine-id>
```

### Status & Info

```bash
# Check CLI version
ocm version

# View account info
ocm auth whoami

# Check service health
ocm health
```

## Configuration File

The CLI reads configuration from `~/.ocm/config.yaml`:

```yaml
# API endpoint (default: production)
api_url: https://api.openclawmachines.com

# Default machine size for new machines
default_size: medium

# Output format: text, json, yaml
output: text
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `OCM_API_URL` | Override API endpoint |
| `OCM_TOKEN` | Authentication token (for CI/CD) |
| `OCM_OUTPUT` | Output format (text/json/yaml) |

## Scripting Examples

```bash
# Start a machine and wait for it to be ready
ocm machines start abc123
ocm machines wait abc123 --status running

# Create, configure, and launch in one flow
MACHINE_ID=$(ocm machines create --name "Nightly Bot" --size large --output json | jq -r '.id')
ocm secrets set $MACHINE_ID ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY
ocm machines start $MACHINE_ID
```
