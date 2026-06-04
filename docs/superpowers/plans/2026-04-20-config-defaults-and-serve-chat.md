# Config Defaults + `ocm machines serve chat` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable five OpenClaw gateway features by default (chatCompletions, cron, heartbeat, shellEnv, threadBindings) and ship `ocm machines serve chat <name>` — a local HTTP server that exposes a machine's chat-completions endpoint as an OpenAI-compatible API on `http://localhost:8787/v1`.

**Architecture:**
- **Config side:** additions to `platformDefaults` in `backend/internal/configassembly/assembler.go` plus a new `heartbeat.every` field injected in the `agents.defaults` block. Full table-driven test coverage in the existing assembler test suite.
- **CLI side:** new `ocm machines serve chat <name>` subcommand. The CLI fetches a short-lived machine JWT from the existing `GET /api/accounts/{account_id}/machines/{id}/token` endpoint, then runs a loopback HTTP reverse proxy that forwards `/v1/*` requests to `https://<machine-hostname>/gateway/v1/*` with the JWT in `X-Machine-Token`. Inbound auth: ephemeral `sk-ocm_<hex>` key generated per `serve` invocation, bound to `127.0.0.1`.
- **Subcommand shape:** `serve` is a parent — `serve chat` is the first child. `serve voice`, `serve realtime`, etc. slot in later without refactor.

**Tech Stack:** Go 1.25 (backend + CLI), Cobra (CLI), existing `backend/internal/configassembly`, `cli/internal/api` client. No new dependencies.

**Out of scope (deferred, not this plan):**
- Machine-scoped API keys for direct-connect without the CLI (separate control-plane feature — auth-proxy validation, key CRUD UI, rate limits).
- `serve voice` / `serve realtime` implementations — only the subcommand scaffolding ships here.

---

## File Structure

**Created:**
- `cli/internal/commands/machines_serve.go` — parent `serve` cobra command and helpers.
- `cli/internal/commands/machines_serve_chat.go` — `serve chat` subcommand: loopback HTTP server + reverse-proxy handler.
- `cli/internal/commands/machines_serve_chat_test.go` — unit tests for inbound auth, upstream forwarding, streaming pass-through.

**Modified:**
- `backend/internal/configassembly/assembler.go` — extend `platformDefaults` with five new keys and inject `agents.defaults.heartbeat.every`.
- `backend/internal/configassembly/assembler_test.go` — new test cases covering each new default.

---

## Task 1: Add `gateway.http.endpoints.chatCompletions.enabled = true` to platform defaults

**Files:**
- Modify: `backend/internal/configassembly/assembler.go:199-267` (the `platformDefaults` map)
- Test: `backend/internal/configassembly/assembler_test.go` (append new test)

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/configassembly/assembler_test.go`:

```go
func TestPlatformDefaults_ChatCompletionsEnabled(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)

	endpoints, ok := getNestedMap(m, "gateway", "http", "endpoints")
	if !ok {
		t.Fatal("missing gateway.http.endpoints")
	}
	chat, ok := endpoints["chatCompletions"].(map[string]interface{})
	if !ok {
		t.Fatalf("gateway.http.endpoints.chatCompletions missing or wrong type: %T", endpoints["chatCompletions"])
	}
	if chat["enabled"] != true {
		t.Errorf("gateway.http.endpoints.chatCompletions.enabled = %v, want true", chat["enabled"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/configassembly/ -run TestPlatformDefaults_ChatCompletionsEnabled -v`
Expected: FAIL — `missing gateway.http.endpoints`.

- [ ] **Step 3: Extend platformDefaults**

In `backend/internal/configassembly/assembler.go`, find the `"gateway"` sub-map inside `platformDefaults` (currently ends around line 254 with the `"nodes"` block). Add a new `"http"` sibling key. The full `gateway` block becomes:

```go
"gateway": map[string]interface{}{
    "mode": "local",
    "auth": map[string]interface{}{
        "mode": "token",
    },
    "controlUi": map[string]interface{}{
        "enabled":                       true,
        "allowInsecureAuth":             true,
        "dangerouslyDisableDeviceAuth":  true,
    },
    "reload": map[string]interface{}{
        "mode": "hot",
    },
    "nodes": map[string]interface{}{
        "denyCommands": []interface{}{
            "camera.snap", "camera.clip", "screen.record",
            "calendar.add", "contacts.add", "reminders.add",
        },
    },
    // OpenAI-compatible chat completions endpoint. Lets external tools
    // (Open WebUI, SDKs, `ocm machines serve chat`) talk to the machine
    // via the standard POST /v1/chat/completions shape. Auth is enforced
    // upstream (machine JWT at auth proxy, gateway token at the gateway).
    "http": map[string]interface{}{
        "endpoints": map[string]interface{}{
            "chatCompletions": map[string]interface{}{
                "enabled": true,
            },
        },
    },
},
```

Preserve the existing comments in the file — the snippet above omits them for readability, but the actual edit must leave the original comments in place and only add the new `"http"` entry.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/configassembly/ -run TestPlatformDefaults_ChatCompletionsEnabled -v`
Expected: PASS.

- [ ] **Step 5: Run the full assembler test suite to catch regressions**

Run: `cd backend && go test ./internal/configassembly/ -v`
Expected: all tests pass. In particular, `TestEmptyCapabilities_PlatformDefaults` and `TestPlatformDefaultsNotMutated` must still pass.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/configassembly/assembler_test.go
git commit -m "feat(config): enable gateway chat-completions endpoint by default"
```

---

## Task 2: Add `cron.enabled = true` to platform defaults

**Files:**
- Modify: `backend/internal/configassembly/assembler.go` (platformDefaults map)
- Test: `backend/internal/configassembly/assembler_test.go`

- [ ] **Step 1: Write the failing test**

Append to `assembler_test.go`:

```go
func TestPlatformDefaults_CronEnabled(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)

	cron, ok := getNestedMap(m, "cron")
	if !ok {
		t.Fatal("missing cron")
	}
	if cron["enabled"] != true {
		t.Errorf("cron.enabled = %v, want true", cron["enabled"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/configassembly/ -run TestPlatformDefaults_CronEnabled -v`
Expected: FAIL — `missing cron`.

- [ ] **Step 3: Add `cron` block to platformDefaults**

In `backend/internal/configassembly/assembler.go`, add a top-level `"cron"` key to the `platformDefaults` map (sibling of `"gateway"`, `"skills"`, `"commands"`, `"session"`):

```go
// Cron is the agent scheduler. Enabling it unlocks recurring tasks
// (monitoring bots, daily reports, periodic pulls). Individual schedules
// are still user-defined; this flag only turns the feature on.
"cron": map[string]interface{}{
    "enabled": true,
},
```

Note: `cron` is NOT in `ProtectedConfigKeys` / `protectedPrefixes`, so users can still override `cron.enabled=false` from their config.

- [ ] **Step 4: Run the specific test to verify it passes**

Run: `cd backend && go test ./internal/configassembly/ -run TestPlatformDefaults_CronEnabled -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/configassembly/assembler_test.go
git commit -m "feat(config): enable cron scheduler by default"
```

---

## Task 3: Add `env.shellEnv.enabled = true` to platform defaults

**Files:**
- Modify: `backend/internal/configassembly/assembler.go` (platformDefaults map)
- Test: `backend/internal/configassembly/assembler_test.go`

- [ ] **Step 1: Write the failing test**

Append to `assembler_test.go`:

```go
func TestPlatformDefaults_ShellEnvEnabled(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)

	shellEnv, ok := getNestedMap(m, "env", "shellEnv")
	if !ok {
		t.Fatal("missing env.shellEnv")
	}
	if shellEnv["enabled"] != true {
		t.Errorf("env.shellEnv.enabled = %v, want true", shellEnv["enabled"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/configassembly/ -run TestPlatformDefaults_ShellEnvEnabled -v`
Expected: FAIL — `missing env.shellEnv`.

- [ ] **Step 3: Add `env` block to platformDefaults**

In `backend/internal/configassembly/assembler.go`, add a top-level `"env"` key (sibling of `"cron"` from Task 2):

```go
// Let the gateway inherit shell env vars. Our init-openclaw.sh already
// exports provider URLs and identity via /etc/profile.d/*.sh and the
// PTY-server env block, so enabling this makes those visible to gateway
// plugins without per-field duplication in config.
"env": map[string]interface{}{
    "shellEnv": map[string]interface{}{
        "enabled": true,
    },
},
```

- [ ] **Step 4: Run the specific test to verify it passes**

Run: `cd backend && go test ./internal/configassembly/ -run TestPlatformDefaults_ShellEnvEnabled -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/configassembly/assembler_test.go
git commit -m "feat(config): enable gateway shell-env import by default"
```

---

## Task 4: Add `session.threadBindings.enabled = true` to platform defaults

**Files:**
- Modify: `backend/internal/configassembly/assembler.go` (platformDefaults — existing `session` block)
- Test: `backend/internal/configassembly/assembler_test.go`

- [ ] **Step 1: Write the failing test**

Append to `assembler_test.go`:

```go
func TestPlatformDefaults_ThreadBindingsEnabled(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)

	threadBindings, ok := getNestedMap(m, "session", "threadBindings")
	if !ok {
		t.Fatal("missing session.threadBindings")
	}
	if threadBindings["enabled"] != true {
		t.Errorf("session.threadBindings.enabled = %v, want true", threadBindings["enabled"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/configassembly/ -run TestPlatformDefaults_ThreadBindingsEnabled -v`
Expected: FAIL — `missing session.threadBindings`.

- [ ] **Step 3: Extend the existing `session` block**

In `backend/internal/configassembly/assembler.go` the `session` block currently reads:

```go
"session": map[string]interface{}{
    "dmScope": "per-channel-peer",
},
```

Replace it with:

```go
"session": map[string]interface{}{
    "dmScope": "per-channel-peer",
    // threadBindings keeps conversation state scoped to the channel thread
    // (Slack/Discord) so a bot stays coherent across a multi-turn thread
    // without leaking context into sibling threads.
    "threadBindings": map[string]interface{}{
        "enabled": true,
    },
},
```

- [ ] **Step 4: Run the specific test to verify it passes**

Run: `cd backend && go test ./internal/configassembly/ -run TestPlatformDefaults_ThreadBindingsEnabled -v`
Expected: PASS.

- [ ] **Step 5: Run `TestPlatformDefaults_Session` to confirm no regression**

Run: `cd backend && go test ./internal/configassembly/ -run TestPlatformDefaults_Session -v`
Expected: PASS — `session.dmScope` is unchanged.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/configassembly/assembler_test.go
git commit -m "feat(config): enable session thread bindings by default"
```

---

## Task 5: Inject `agents.defaults.heartbeat.every = "15m"`

**Files:**
- Modify: `backend/internal/configassembly/assembler.go` (the block that currently injects `agents.defaults.model.primary`, `models`, `workspace`)
- Test: `backend/internal/configassembly/assembler_test.go`

**Context for the engineer:** `agents.defaults` is not in `platformDefaults` — it's built dynamically in `AssembleConfig` near the end of the function. Grep for `agents.defaults.model.primary` in the existing test file (`assembler_test.go:1039`) and follow the reference to the source injection site in `assembler.go`. That's where `heartbeat.every` belongs so all dynamic `agents.defaults` fields live together.

- [ ] **Step 1: Write the failing test**

Append to `assembler_test.go`:

```go
func TestAgentsDefaults_Heartbeat(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: nil,
		DefaultModel: "anthropic/claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)

	defaults, ok := getNestedMap(m, "agents", "defaults")
	if !ok {
		t.Fatal("missing agents.defaults")
	}
	hb, ok := defaults["heartbeat"].(map[string]interface{})
	if !ok {
		t.Fatalf("agents.defaults.heartbeat missing or wrong type: %T", defaults["heartbeat"])
	}
	if hb["every"] != "15m" {
		t.Errorf("agents.defaults.heartbeat.every = %v, want \"15m\"", hb["every"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/configassembly/ -run TestAgentsDefaults_Heartbeat -v`
Expected: FAIL — `agents.defaults.heartbeat missing or wrong type: <nil>`.

- [ ] **Step 3: Locate the injection site**

Run: `grep -n "defaults\[\"workspace\"\]\|agents.*defaults\|DefaultModel" backend/internal/configassembly/assembler.go`

Find the block that sets `defaults["model"]`, `defaults["models"]`, and `defaults["workspace"]`. That's where `heartbeat` must be added. (Expect it near the end of `AssembleConfig`.)

- [ ] **Step 4: Add the heartbeat injection**

In the same block where `defaults["workspace"] = "/home/openclaw/.openclaw/workspace"` is set, immediately below that line add:

```go
// Heartbeat fires every 15 minutes so monitoring/alerting agents have
// a default wake-up cadence. Users can override via account config.
defaults["heartbeat"] = map[string]interface{}{
    "every": "15m",
}
```

The `defaults` local variable is already the `agents.defaults` sub-map in that block — reuse it, do not introduce a new lookup.

- [ ] **Step 5: Run the specific test to verify it passes**

Run: `cd backend && go test ./internal/configassembly/ -run TestAgentsDefaults_Heartbeat -v`
Expected: PASS.

- [ ] **Step 6: Run the full assembler test suite**

Run: `cd backend && go test ./internal/configassembly/ -v`
Expected: all tests pass (in particular `TestAgentsDefaults` must still pass — heartbeat is additive, not a replacement).

- [ ] **Step 7: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/configassembly/assembler_test.go
git commit -m "feat(config): default heartbeat to 15m for agents"
```

---

## Task 6: Scaffold `ocm machines serve` parent command

**Files:**
- Create: `cli/internal/commands/machines_serve.go`

- [ ] **Step 1: Write the file**

```go
package commands

import (
	"github.com/spf13/cobra"
)

// machinesServeCmd is the parent for all `ocm machines serve <protocol>` commands.
// Each subcommand (chat, voice, realtime, ...) exposes a local endpoint that
// forwards to the machine's gateway in a protocol-specific shape.
var machinesServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Expose a machine's gateway endpoint locally (chat, voice, ...)",
	Long: `Serve a local endpoint backed by a running machine's gateway.

Subcommands:
  chat      OpenAI-compatible chat completions on http://localhost:<port>/v1

Additional protocol subcommands (voice, realtime, vision) will be added as
they land. Use 'ocm machines serve <protocol> --help' for flags.`,
}

func init() {
	machinesCmd.AddCommand(machinesServeCmd)
}
```

- [ ] **Step 2: Confirm it compiles**

Run: `cd cli && go build ./...`
Expected: build succeeds, no errors.

- [ ] **Step 3: Confirm it registers in the CLI**

Run: `cd cli && go run . machines serve --help 2>&1 | head -15`
Expected: output includes `Serve a local endpoint backed by a running machine's gateway.`

- [ ] **Step 4: Commit**

```bash
git add cli/internal/commands/machines_serve.go
git commit -m "feat(cli): scaffold 'machines serve' parent command"
```

---

## Task 7: Implement `ocm machines serve chat` — token fetch + startup output

**Files:**
- Create: `cli/internal/commands/machines_serve_chat.go`

**Context:** The existing `GET /api/accounts/{account_id}/machines/{id}/token` endpoint (implemented at `backend/internal/api/machines.go:1951`, registered at `backend/internal/api/server.go:445`) returns `{"token": "...", "expires_at": "...", "hostname": "..."}`. We call that to get a short-lived machine JWT with scope `all` (which includes `gateway`). Then later tasks proxy requests to `https://<hostname>/gateway/v1/*` with `X-Machine-Token: <jwt>`.

- [ ] **Step 1: Write the subcommand skeleton (auth + startup banner only; no HTTP server yet)**

```go
package commands

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

type machineTokenResp struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	Hostname  string `json:"hostname"`
}

func fetchMachineToken(client apiClient, accountID int, machineID string) (*machineTokenResp, error) {
	path := fmt.Sprintf("/api/accounts/%d/machines/%s/token", accountID, machineID)
	resp, err := client.Get(path)
	if err != nil {
		return nil, fmt.Errorf("fetching machine token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetching machine token: %s", apiError(resp.StatusCode, string(body)))
	}
	var out machineTokenResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding machine token response: %w", err)
	}
	return &out, nil
}

func newEphemeralKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-ocm_" + hex.EncodeToString(b), nil
}

var machinesServeChatCmd = &cobra.Command{
	Use:   "chat [NAME]",
	Short: "Run a local OpenAI-compatible chat-completions endpoint backed by a machine",
	Long: `Starts a local HTTP server that forwards /v1/chat/completions requests
to the machine's gateway. Point any OpenAI SDK, Open WebUI, Continue.dev, or
similar tool at the printed OPENAI_BASE_URL to use your machine as a provider.

Authentication is scoped to this run: an ephemeral sk-ocm_<hex> API key is
generated on start and printed to stderr. Pass --no-auth to accept unauthenticated
loopback traffic (only safe on single-user machines).`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeMachineNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		port, _ := cmd.Flags().GetInt("port")
		noAuth, _ := cmd.Flags().GetBool("no-auth")

		name := ""
		if len(args) > 0 {
			name = args[0]
		}

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}
		if machine.Status != "running" {
			return fmt.Errorf("machine %q is not running (status: %s)", machine.Name, machine.Status)
		}

		tok, err := fetchMachineToken(client, cfg.DefaultAccountID, machine.ID)
		if err != nil {
			return err
		}

		apiKey := ""
		if !noAuth {
			apiKey, err = newEphemeralKey()
			if err != nil {
				return fmt.Errorf("generating API key: %w", err)
			}
		}

		fmt.Fprintf(os.Stderr, "\nServing chat-completions for machine %q via %s\n", machine.Name, tok.Hostname)
		fmt.Fprintf(os.Stderr, "Local endpoint: http://localhost:%d/v1\n\n", port)
		if apiKey != "" {
			fmt.Fprintf(os.Stderr, "  export OPENAI_BASE_URL=http://localhost:%d/v1\n", port)
			fmt.Fprintf(os.Stderr, "  export OPENAI_API_KEY=%s\n\n", apiKey)
		} else {
			fmt.Fprintf(os.Stderr, "  export OPENAI_BASE_URL=http://localhost:%d/v1\n", port)
			fmt.Fprintf(os.Stderr, "  (auth disabled — clients may send any OPENAI_API_KEY)\n\n")
		}

		// HTTP server starts in Task 8.
		return fmt.Errorf("serve chat: HTTP proxy not yet implemented")
	},
}

func init() {
	machinesServeChatCmd.Flags().Int("port", 8787, "Local port to bind (loopback only)")
	machinesServeChatCmd.Flags().Bool("no-auth", false, "Accept unauthenticated loopback requests (no inbound API key check)")
	machinesServeCmd.AddCommand(machinesServeChatCmd)
}
```

The `apiClient` interface above is the same one used by other commands — it already has `Get(path string) (*http.Response, error)`. If the existing codebase names the type differently (e.g., `*api.Client`), match that name exactly.

- [ ] **Step 2: Resolve any naming mismatch against existing code**

Run: `grep -n "func (.*) Get(\|type Client \|newAPIClient\|completeMachineNames" cli/internal/api/*.go cli/internal/commands/machines_backups.go | head -20`

Adjust the `apiClient` parameter type and `client.Get(path)` call signature in `fetchMachineToken` to match the real API client in `cli/internal/api`. Do not invent new names.

- [ ] **Step 3: Confirm it compiles**

Run: `cd cli && go build ./...`
Expected: build succeeds.

- [ ] **Step 4: Confirm the subcommand registers**

Run: `cd cli && go run . machines serve chat --help 2>&1 | head -20`
Expected: `--port` and `--no-auth` flags visible.

- [ ] **Step 5: Commit**

```bash
git add cli/internal/commands/machines_serve_chat.go
git commit -m "feat(cli): scaffold 'machines serve chat' with ephemeral key + token fetch"
```

---

## Task 8: Implement the HTTP reverse proxy handler (happy path, no streaming yet)

**Files:**
- Modify: `cli/internal/commands/machines_serve_chat.go`
- Create: `cli/internal/commands/machines_serve_chat_test.go`

- [ ] **Step 1: Write the failing test**

Create `cli/internal/commands/machines_serve_chat_test.go`:

```go
package commands

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Fake upstream that mimics the auth proxy's /gateway/v1/... route.
func fakeUpstream(t *testing.T, wantToken string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Machine-Token") != wantToken {
			t.Errorf("upstream: X-Machine-Token = %q, want %q", r.Header.Get("X-Machine-Token"), wantToken)
		}
		if !strings.HasPrefix(r.URL.Path, "/gateway/v1/") {
			t.Errorf("upstream: path = %q, want prefix /gateway/v1/", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"test","choices":[]}`)
	}))
}

func TestServeChatHandler_ForwardsWithMachineToken(t *testing.T) {
	upstream := fakeUpstream(t, "JWT_ABC")
	defer upstream.Close()

	h := newServeChatHandler(serveChatConfig{
		upstreamBase: upstream.URL,
		machineToken: "JWT_ABC",
		apiKey:       "sk-ocm_test",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"x"}`))
	req.Header.Set("Authorization", "Bearer sk-ocm_test")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"id":"test"`) {
		t.Errorf("body = %q, want upstream body", rr.Body.String())
	}
}

func TestServeChatHandler_RejectsMissingAPIKey(t *testing.T) {
	upstream := fakeUpstream(t, "JWT_ABC")
	defer upstream.Close()

	h := newServeChatHandler(serveChatConfig{
		upstreamBase: upstream.URL,
		machineToken: "JWT_ABC",
		apiKey:       "sk-ocm_test",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestServeChatHandler_RejectsWrongAPIKey(t *testing.T) {
	upstream := fakeUpstream(t, "JWT_ABC")
	defer upstream.Close()

	h := newServeChatHandler(serveChatConfig{
		upstreamBase: upstream.URL,
		machineToken: "JWT_ABC",
		apiKey:       "sk-ocm_correct",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer sk-ocm_wrong")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestServeChatHandler_NoAuthSkipsInboundCheck(t *testing.T) {
	upstream := fakeUpstream(t, "JWT_ABC")
	defer upstream.Close()

	h := newServeChatHandler(serveChatConfig{
		upstreamBase: upstream.URL,
		machineToken: "JWT_ABC",
		apiKey:       "", // disabled
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cli && go test ./internal/commands/ -run TestServeChatHandler -v`
Expected: FAIL — `undefined: newServeChatHandler` / `undefined: serveChatConfig`.

- [ ] **Step 3: Implement the handler**

Add to `cli/internal/commands/machines_serve_chat.go` (above the `var machinesServeChatCmd` declaration):

```go
type serveChatConfig struct {
	upstreamBase string // e.g. "https://m-foo.openclawmachines.com"
	machineToken string // machine JWT (goes in X-Machine-Token)
	apiKey       string // ephemeral inbound key; empty disables inbound auth
}

func newServeChatHandler(cfg serveChatConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.apiKey != "" {
			authz := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(authz, prefix) || authz[len(prefix):] != cfg.apiKey {
				http.Error(w, `{"error":{"message":"invalid api key","type":"invalid_request_error"}}`, http.StatusUnauthorized)
				return
			}
		}

		// Map /v1/<rest> → <upstreamBase>/gateway/v1/<rest>
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			http.Error(w, `{"error":{"message":"only /v1/* paths are served","type":"invalid_request_error"}}`, http.StatusNotFound)
			return
		}
		targetURL := cfg.upstreamBase + "/gateway" + r.URL.Path
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}

		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
		if err != nil {
			http.Error(w, `{"error":{"message":"bad upstream url"}}`, http.StatusInternalServerError)
			return
		}
		// Copy headers except hop-by-hop + our inbound Authorization.
		for k, vv := range r.Header {
			if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Host") {
				continue
			}
			for _, v := range vv {
				outReq.Header.Add(k, v)
			}
		}
		outReq.Header.Set("X-Machine-Token", cfg.machineToken)

		resp, err := http.DefaultClient.Do(outReq)
		if err != nil {
			http.Error(w, `{"error":{"message":"upstream unreachable"}}`, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}
```

Add the necessary imports to the file's import block if not already present: `"net/http"`, `"strings"`, `"io"`.

- [ ] **Step 4: Wire the handler into the RunE block**

In `machinesServeChatCmd.RunE`, replace the terminal `return fmt.Errorf("serve chat: HTTP proxy not yet implemented")` line with:

```go
handler := newServeChatHandler(serveChatConfig{
    upstreamBase: "https://" + tok.Hostname,
    machineToken: tok.Token,
    apiKey:       apiKey,
})

addr := fmt.Sprintf("127.0.0.1:%d", port)
srv := &http.Server{Addr: addr, Handler: handler}

errCh := make(chan error, 1)
go func() { errCh <- srv.ListenAndServe() }()

sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

select {
case <-sigCh:
    fmt.Fprintf(os.Stderr, "\nShutting down...\n")
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()
    _ = srv.Shutdown(ctx)
    return nil
case err := <-errCh:
    if err != nil && err != http.ErrServerClosed {
        return fmt.Errorf("local server: %w", err)
    }
    return nil
}
```

Add the missing imports to the file: `"context"`, `"net/http"`, `"os/signal"`, `"syscall"`, `"time"`.

- [ ] **Step 5: Run the unit tests to verify they pass**

Run: `cd cli && go test ./internal/commands/ -run TestServeChatHandler -v`
Expected: all four subtests PASS.

- [ ] **Step 6: Confirm the binary still builds**

Run: `cd cli && go build ./...`
Expected: build succeeds.

- [ ] **Step 7: Commit**

```bash
git add cli/internal/commands/machines_serve_chat.go cli/internal/commands/machines_serve_chat_test.go
git commit -m "feat(cli): implement 'machines serve chat' HTTP reverse proxy"
```

---

## Task 9: Verify streaming (SSE) passes through unmodified

**Files:**
- Modify: `cli/internal/commands/machines_serve_chat_test.go` (add one test)

**Context:** Chat-completion streaming responses use `text/event-stream` + `Transfer-Encoding: chunked`. Go's `http.DefaultClient` + `io.Copy` already handles chunked bodies, but the response writer must flush between chunks or clients wait for the whole response. The handler from Task 8 writes the upstream body directly — we just need to confirm chunks aren't buffered.

- [ ] **Step 1: Add the streaming test**

Append to `cli/internal/commands/machines_serve_chat_test.go`:

```go
func TestServeChatHandler_StreamingPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test upstream: no flusher")
		}
		for _, chunk := range []string{"data: one\n\n", "data: two\n\n", "data: [DONE]\n\n"} {
			_, _ = io.WriteString(w, chunk)
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	h := newServeChatHandler(serveChatConfig{
		upstreamBase: upstream.URL,
		machineToken: "JWT",
		apiKey:       "",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"data: one", "data: two", "[DONE]"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: got %q", want, body)
		}
	}
}
```

- [ ] **Step 2: Run the test**

Run: `cd cli && go test ./internal/commands/ -run TestServeChatHandler_StreamingPassthrough -v`
Expected: PASS (the handler already copies upstream body chunk-by-chunk via `io.Copy`).

If it fails because `httptest.ResponseRecorder` buffers writes, the chunks will still arrive in the recorder's body — the assertion only checks full content, not timing. So this test is a content check, not a real-time check. That is intentional: real-time flush behavior is verified manually in Task 10.

- [ ] **Step 3: Commit**

```bash
git add cli/internal/commands/machines_serve_chat_test.go
git commit -m "test(cli): assert SSE streaming passes through 'serve chat' handler"
```

---

## Task 10: Manual verification against a running machine

**Files:** none (manual test).

**Pre-reqs:** a running machine owned by the logged-in CLI user, and `make deploy-backend` has deployed the five config-default changes so the gateway in that VM exposes `/v1/chat/completions`.

- [ ] **Step 1: Deploy the config changes**

Run: `make deploy-backend`
Wait for deploy to finish (prints Cloud Run revision). New VMs will now get the updated platformDefaults. Existing VMs reload config on next connect.

- [ ] **Step 2: Build the CLI**

Run: `cd cli && go build -o /tmp/ocm .`
Expected: produces `/tmp/ocm`.

- [ ] **Step 3: Start `serve chat` against a running machine**

Replace `<MACHINE_NAME>` with a real machine name.

Run: `/tmp/ocm machines serve chat <MACHINE_NAME>`

Expected stderr output:
```
Serving chat-completions for machine "<MACHINE_NAME>" via m-<slug>.openclawmachines.com
Local endpoint: http://localhost:8787/v1

  export OPENAI_BASE_URL=http://localhost:8787/v1
  export OPENAI_API_KEY=sk-ocm_<hex>
```

Leave this process running.

- [ ] **Step 4: Send a non-streaming request from another terminal**

```bash
export OPENAI_BASE_URL=http://localhost:8787/v1
export OPENAI_API_KEY=sk-ocm_<paste>
curl -sS -X POST "$OPENAI_BASE_URL/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"anthropic/claude-sonnet-4-6","messages":[{"role":"user","content":"Say hi in 3 words."}]}'
```

Expected: HTTP 200 with an OpenAI-shaped JSON body containing a `choices[0].message.content` field. Evidence to capture: the full response body.

- [ ] **Step 5: Send a streaming request**

```bash
curl -N -X POST "$OPENAI_BASE_URL/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"anthropic/claude-sonnet-4-6","stream":true,"messages":[{"role":"user","content":"Count 1 to 3."}]}'
```

Expected: `data: ...` lines stream in real time, terminating with `data: [DONE]`. If output arrives all-at-once at the end, the local server is buffering — that's a bug; file a follow-up rather than shipping.

- [ ] **Step 6: Verify auth rejection**

```bash
curl -sS -o /dev/null -w "%{http_code}\n" -X POST "$OPENAI_BASE_URL/chat/completions" \
  -H "Authorization: Bearer sk-wrong" \
  -H "Content-Type: application/json" \
  -d '{}'
```

Expected: `401`.

- [ ] **Step 7: Shut down the server with Ctrl+C**

Expected: `Shutting down...` prints and the process exits cleanly.

- [ ] **Step 8: Commit the verification note**

Append a short block to `docs/CurrentFeature.md` recording which machine was used, the revision deployed, and paste the captured 200 response body (redact the JWT).

```bash
git add docs/CurrentFeature.md
git commit -m "docs: manual verification of 'machines serve chat'"
```

---

## Task 11: End-to-end regression check

- [ ] **Step 1: Run full Go test suite**

Run: `make test-go`
Expected: all pass.

- [ ] **Step 2: Run gateway e2e**

Run: `make test-gateway-e2e`
Expected: all pass (~12s, no VMs needed).

- [ ] **Step 3: Run CLI tests**

Run: `cd cli && go test ./...`
Expected: all pass.

- [ ] **Step 4: Update `docs/CurrentFeature.md` with final checklist summary**

Add a short section listing which defaults now ship (chatCompletions, cron, heartbeat=15m, shellEnv, threadBindings) and that `ocm machines serve chat` is live. Keep it to ~10 lines.

- [ ] **Step 5: Commit and open PR**

Run: `/pr` (or commit + push + `gh pr create` manually). Title: `feat: gateway defaults + 'ocm machines serve chat'`.

---

## Self-review notes

- All six config flags from the original ask are covered: chatCompletions (Task 1), cron (Task 2), shellEnv (Task 3), threadBindings (Task 4), heartbeat (Task 5). Hooks is intentionally excluded per user instruction.
- CLI scope is chat-only; `voice`/`realtime` are scaffolded via the `serve` parent but not implemented (documented out of scope).
- Machine-scoped direct API keys are out of scope and called out explicitly in the header.
- Type names are consistent across tasks: `serveChatConfig` / `newServeChatHandler` / `machinesServeChatCmd` / `machineTokenResp` / `fetchMachineToken`.
- No placeholders — every test body and implementation snippet is concrete.
- Each task ends in a commit and every code step has a verification command with an expected outcome.
