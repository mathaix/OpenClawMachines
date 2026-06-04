# OCM CLI User Guide

The `ocm` command-line tool lets you create, configure, and manage OpenClaw Machines from your terminal. It's designed for both interactive use and scripted automation by LLMs.

## Installation

Download the latest binary for your platform:

```bash
# macOS (Apple Silicon)
curl -fsSL https://storage.googleapis.com/ocm-artifacts/cli/latest/ocm-darwin-arm64 -o ocm
chmod +x ocm && sudo mv ocm /usr/local/bin/

# macOS (Intel)
curl -fsSL https://storage.googleapis.com/ocm-artifacts/cli/latest/ocm-darwin-amd64 -o ocm
chmod +x ocm && sudo mv ocm /usr/local/bin/

# Linux (x86_64)
curl -fsSL https://storage.googleapis.com/ocm-artifacts/cli/latest/ocm-linux-amd64 -o ocm
chmod +x ocm && sudo mv ocm /usr/local/bin/
```

Verify the installation:

```bash
ocm version
```

## Authentication

Log in with your browser (opens Cloudflare Access):

```bash
ocm login
```

Or authenticate directly with a token:

```bash
ocm login --token YOUR_TOKEN
```

Log out:

```bash
ocm logout
```

## Quick Start

Create a machine, set up a provider, add a Telegram channel, and push config:

```bash
# Create and start a machine
ocm machines create --name my-bot --size standard
ocm machines start my-bot

# Set up Anthropic (guided wizard)
ocm providers setup anthropic --machine my-bot

# Set up Telegram (guided wizard)
ocm channels setup telegram --machine my-bot

# Verify
ocm machines get my-bot
```

Or use the all-in-one pair wizard:

```bash
ocm pair my-bot
```

---

## Machines

### Create a machine

```bash
ocm machines create --name my-bot --size standard
```

Sizes: `standard`, `large`, `xlarge`.

### List machines

```bash
ocm machines list
```

### Start / stop / delete

```bash
ocm machines start my-bot
ocm machines stop my-bot
ocm machines delete my-bot
```

### View machine details

```bash
ocm machines get my-bot
```

### SSH into a machine

```bash
ocm machines ssh my-bot
ocm machines ssh my-bot --user root
ocm machines ssh my-bot -- -L 8080:localhost:8080   # port forwarding
```

### Stream logs

```bash
ocm machines logs my-bot
ocm machines logs my-bot --follow --lines 50
```

---

## Providers

Providers are LLM or channel APIs (Anthropic, OpenAI, Google, Telegram, Discord, WhatsApp).

### Guided setup

The `setup` wizard shows step-by-step instructions, validates your key locally, stores it, and optionally links it to a machine:

```bash
ocm providers setup anthropic --machine my-bot
```

The wizard will:
1. Show where to get your API key
2. Prompt for the key (masked input)
3. Validate the key against the provider's API
4. Store the encrypted credential
5. Link it to the machine and push config

For scripted use, pipe the key from stdin:

```bash
echo "$ANTHROPIC_API_KEY" | ocm providers setup anthropic --machine my-bot --key-from-stdin --label "prod key" --json
```

### List credentials

```bash
ocm providers list          # your stored credentials
ocm providers list --all    # all available providers (built-in + custom)
```

### Add / remove credentials

```bash
ocm providers add anthropic
ocm providers remove 42
```

### Re-validate a credential

```bash
ocm providers validate anthropic
```

### Register a custom provider

```bash
ocm providers register my-llm \
  --upstream-host api.my-llm.com \
  --auth-method bearer_header \
  --is-llm
```

Auth methods: `bearer_header`, `api_key_header`, `query_param`, `none`.

```bash
ocm providers unregister my-llm
```

---

## Channels

Channels connect your machine to messaging platforms.

### Guided setup

```bash
ocm channels setup telegram --machine my-bot
```

Supported channels: `telegram`, `discord`, `whatsapp`.

The wizard will:
1. Show platform-specific instructions (e.g., how to create a Telegram bot via @BotFather)
2. Prompt for the bot token (masked input)
3. Validate the token against the platform API
4. Store the encrypted credential
5. Link to machine, enable the channel, and push config

For scripted use:

```bash
echo "$TELEGRAM_BOT_TOKEN" | ocm channels setup telegram --machine my-bot --token-from-stdin --label "main bot" --json
```

### List channels

```bash
ocm channels available                  # all channels in the registry
ocm channels list --machine my-bot      # channels enabled on a machine
```

### Enable / disable

```bash
ocm channels enable telegram --machine my-bot
ocm channels disable telegram --machine my-bot
```

### Configure channel settings

```bash
ocm channels configure telegram dmPolicy open --machine my-bot
```

---

## Pairing (All-in-One Setup)

The `pair` command walks you through setting up a machine with everything it needs:

```bash
ocm pair my-bot
```

The interactive wizard covers:
1. LLM provider selection and key entry
2. Channel setup and token entry
3. Identity (name, avatar)
4. Budget limits
5. Config push and health check

For scripted setups, use the individual commands instead (see [Scripted Setup](#scripted-setup-for-llms) below).

---

## Identity

Set a display name and avatar for your machine's assistant:

```bash
ocm identity set --machine my-bot --name "PropertyBot" --avatar "https://example.com/avatar.png"
ocm identity show --machine my-bot
```

---

## Budget

Set spending limits to control LLM costs:

```bash
ocm budget set --machine my-bot --limit 50.00
ocm budget clear --machine my-bot
```

---

## Secrets

Store key-value secrets on a machine (e.g., system prompts, API keys for external services):

```bash
# Set a secret (interactive masked prompt)
ocm machines secrets set system_prompt --machine my-bot

# Set from stdin (for scripting)
echo "You are a helpful assistant." | ocm machines secrets set system_prompt --machine my-bot --value-from-stdin

# List secrets (keys only, values are hidden)
ocm machines secrets list --machine my-bot

# Delete a secret
ocm machines secrets delete system_prompt --machine my-bot
```

---

## Credentials

Manage which credentials are linked to which machines:

```bash
# List credentials on a machine
ocm machines credentials list --machine my-bot

# Link a credential (by ID from `ocm providers list`)
ocm machines credentials link --machine my-bot --credential 42

# Unlink
ocm machines credentials unlink --machine my-bot --credential 42
```

Credentials can be shared across machines. Linking a credential to multiple machines means they all use the same API key.

---

## Skills & Tools

Skills and tools extend what your machine's agent can do.

### Skills

```bash
ocm skills available                          # list all available skills
ocm skills list --machine my-bot              # skills enabled on a machine
ocm skills enable web-search --machine my-bot
ocm skills disable web-search --machine my-bot
```

### Tools

```bash
ocm tools available                           # list all available tools
ocm tools list --machine my-bot
ocm tools enable code-exec --machine my-bot --mode sandboxed
ocm tools disable code-exec --machine my-bot
```

---

## Configuration

### CLI config

```bash
ocm config show                         # show current CLI config
ocm config set-api-url https://...      # set the API base URL
ocm config set-account my-account       # set the default account
```

### Machine config

View the fully assembled configuration for a machine:

```bash
ocm config machine-show --machine my-bot
```

Push configuration to a running machine (triggers hot-reload):

```bash
ocm config push --machine my-bot
```

---

## Usage

View LLM usage for your account:

```bash
ocm usage
ocm usage --machine my-bot    # filter to one machine
```

---

## JSON Output

All commands support `--json` for structured output:

```bash
ocm machines list --json
ocm providers list --json
ocm channels list --machine my-bot --json
```

Output follows a consistent envelope:

```json
{
  "status": "ok",
  "action": "machines.list",
  "result": [...]
}
```

Errors go to stderr:

```json
{
  "status": "error",
  "action": "providers.setup",
  "error": "key validation failed: invalid API key"
}
```

---

## Scripted Setup for LLMs

The CLI is designed so AI coding assistants (Claude Code, Codex, etc.) can drive it without interactive prompts. Every operation has a flag-only equivalent:

```bash
# Set up Anthropic (key from env var)
echo "$ANTHROPIC_API_KEY" | ocm providers setup anthropic \
  --machine my-bot --key-from-stdin --label "prod" --json

# Set up Telegram (token from env var)
echo "$TELEGRAM_BOT_TOKEN" | ocm channels setup telegram \
  --machine my-bot --token-from-stdin --label "main" --json

# Set identity
ocm identity set --machine my-bot --name "MyClaw" --avatar robot --json

# Set system prompt
echo "You are a helpful assistant." | ocm machines secrets set system_prompt \
  --machine my-bot --value-from-stdin --json

# Set budget
ocm budget set --machine my-bot --limit 50 --json

# Push config
ocm config push --machine my-bot --json

# Verify
ocm machines get my-bot --json
```

### Fleet Bootstrapping Example

An LLM can create and configure an entire fleet:

```bash
# Create machines
ocm machines create --name support-bot --size standard --json
ocm machines create --name sales-bot --size standard --json
ocm machines start support-bot --json
ocm machines start sales-bot --json

# Share one Anthropic key across both
echo "$ANTHROPIC_API_KEY" | ocm providers setup anthropic \
  --key-from-stdin --label "shared" --json
# Parse credential_id from JSON output, then link to both machines
ocm machines credentials link --machine support-bot --credential 42 --json
ocm machines credentials link --machine sales-bot --credential 42 --json

# Different channels per machine
echo "$WHATSAPP_TOKEN" | ocm channels setup whatsapp \
  --machine support-bot --token-from-stdin --label "support" --json
echo "$DISCORD_TOKEN" | ocm channels setup discord \
  --machine sales-bot --token-from-stdin --label "sales" --json

# Different system prompts
echo "Handle customer support inquiries." | ocm machines secrets set system_prompt \
  --machine support-bot --value-from-stdin --json
echo "Help prospects with product questions." | ocm machines secrets set system_prompt \
  --machine sales-bot --value-from-stdin --json

# Push config to both
ocm config push --machine support-bot --json
ocm config push --machine sales-bot --json
```

---

## Command Reference

| Command | Description |
|---------|-------------|
| `ocm login` | Authenticate with the OCM API |
| `ocm logout` | Clear stored authentication |
| `ocm version` | Print CLI version |
| **Machines** | |
| `ocm machines list` | List all machines |
| `ocm machines create` | Create a new machine |
| `ocm machines get SLUG` | Show machine details |
| `ocm machines start SLUG` | Start a machine |
| `ocm machines stop SLUG` | Stop a machine |
| `ocm machines delete SLUG` | Delete a machine |
| `ocm machines ssh SLUG` | SSH into a machine |
| `ocm machines logs SLUG` | Stream machine logs |
| `ocm machines credentials list` | List linked credentials |
| `ocm machines credentials link` | Link a credential |
| `ocm machines credentials unlink` | Unlink a credential |
| `ocm machines secrets list` | List secrets |
| `ocm machines secrets set KEY` | Set a secret |
| `ocm machines secrets delete KEY` | Delete a secret |
| **Providers** | |
| `ocm providers list` | List credentials |
| `ocm providers add PROVIDER` | Add a credential |
| `ocm providers remove ID` | Remove a credential |
| `ocm providers validate PROVIDER` | Re-validate a credential |
| `ocm providers setup PROVIDER` | Guided setup wizard |
| `ocm providers register NAME` | Register custom provider |
| `ocm providers unregister NAME` | Remove custom provider |
| **Channels** | |
| `ocm channels available` | List registry channels |
| `ocm channels list` | List enabled channels |
| `ocm channels enable CHANNEL` | Enable a channel |
| `ocm channels disable CHANNEL` | Disable a channel |
| `ocm channels configure CHANNEL KEY VALUE` | Set channel config |
| `ocm channels setup CHANNEL` | Guided setup wizard |
| **Skills & Tools** | |
| `ocm skills available` | List available skills |
| `ocm skills list` | List enabled skills |
| `ocm skills enable SKILL` | Enable a skill |
| `ocm skills disable SKILL` | Disable a skill |
| `ocm tools available` | List available tools |
| `ocm tools list` | List enabled tools |
| `ocm tools enable TOOL` | Enable a tool |
| `ocm tools disable TOOL` | Disable a tool |
| **Config** | |
| `ocm config show` | Show CLI config |
| `ocm config set-api-url URL` | Set API URL |
| `ocm config set-account SLUG` | Set default account |
| `ocm config machine-show` | Show assembled machine config |
| `ocm config push` | Push config to running machine |
| **Other** | |
| `ocm pair SLUG` | All-in-one setup wizard |
| `ocm identity set` | Set machine identity |
| `ocm identity show` | Show machine identity |
| `ocm budget set` | Set spending budget |
| `ocm budget clear` | Clear spending budget |
| `ocm usage` | Show LLM usage |
