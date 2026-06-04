# Config Lifecycle Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `pushNativeConfig` (config.patch RPC) with direct `openclaw config set/unset` calls via agent exec, so UI config changes follow the same path as terminal changes.

**Architecture:** Add `ConfigSet`/`ConfigUnset` helpers to agentclient that call `openclaw config set/unset` via the existing `ExecCommand` method. Replace `pushNativeConfig` and `handleSetMachineModel`'s live update path with these new helpers. Add `"config"` to `allowedExecSubcommands`.

**Tech Stack:** Go, Chi router, agent exec endpoint

**Spec:** `docs/superpowers/specs/2026-03-20-config-lifecycle-fix-design.md`

---

### Task 1: Add `config` to allowedExecSubcommands

**Files:**
- Modify: `backend/cmd/agent/ptyserver.go:36-41`
- Test: `backend/cmd/agent/ptyserver_test.go` (if exists, otherwise manual verification)

- [ ] **Step 1: Add `"config": true` to allowedExecSubcommands**

In `backend/cmd/agent/ptyserver.go` at line 36-41, add the new entry:

```go
var allowedExecSubcommands = map[string]bool{
	"pairing": true,
	"status":  true,
	"doctor":  true,
	"gateway": true,
	"config":  true, // for openclaw config set/unset live updates
}
```

- [ ] **Step 2: Verify build**

Run: `cd backend && go build ./cmd/agent/`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add backend/cmd/agent/ptyserver.go
git commit -m "feat: allow openclaw config subcommand via exec endpoint"
```

---

### Task 2: Add `ConfigSet` and `ConfigUnset` helpers to agentclient

**Files:**
- Create: `backend/internal/agentclient/config.go`
- Create: `backend/internal/agentclient/config_test.go`

- [ ] **Step 1: Write failing tests for ConfigSet**

Create `backend/internal/agentclient/config_test.go`:

```go
package agentclient

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigSet_Success(t *testing.T) {
	var receivedCmd []string
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Command []string }
		json.NewDecoder(r.Body).Decode(&req)
		receivedCmd = req.Command
		json.NewEncoder(w).Encode(ExecResult{ExitCode: 0, Stdout: ""})
	})
	defer srv.Close()

	err := client.ConfigSet(context.Background(), host, "m-123", "tok", "agents.defaults.model.primary", "nebius/deepseek-ai/DeepSeek-V3-0324", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"openclaw", "config", "set", "agents.defaults.model.primary", "nebius/deepseek-ai/DeepSeek-V3-0324"}, receivedCmd)
}

func TestConfigSet_StrictJSON(t *testing.T) {
	var receivedCmd []string
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Command []string }
		json.NewDecoder(r.Body).Decode(&req)
		receivedCmd = req.Command
		json.NewEncoder(w).Encode(ExecResult{ExitCode: 0, Stdout: ""})
	})
	defer srv.Close()

	err := client.ConfigSet(context.Background(), host, "m-123", "tok", "models.providers.anthropic.apiKey", `{"source":"exec","provider":"ocm","id":"anthropic-key"}`, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"openclaw", "config", "set", "models.providers.anthropic.apiKey", `{"source":"exec","provider":"ocm","id":"anthropic-key"}`, "--strict-json"}, receivedCmd)
}

func TestConfigSet_NonZeroExit(t *testing.T) {
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ExecResult{ExitCode: 1, Stderr: "path not found"})
	})
	defer srv.Close()

	err := client.ConfigSet(context.Background(), host, "m-123", "tok", "bad.path", "val", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path not found")
}

func TestConfigUnset_Success(t *testing.T) {
	var receivedCmd []string
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Command []string }
		json.NewDecoder(r.Body).Decode(&req)
		receivedCmd = req.Command
		json.NewEncoder(w).Encode(ExecResult{ExitCode: 0, Stdout: ""})
	})
	defer srv.Close()

	err := client.ConfigUnset(context.Background(), host, "m-123", "tok", "channels.telegram")
	require.NoError(t, err)
	assert.Equal(t, []string{"openclaw", "config", "unset", "channels.telegram"}, receivedCmd)
}

func TestConfigUnset_NonZeroExit(t *testing.T) {
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ExecResult{ExitCode: 1, Stderr: "key not found"})
	})
	defer srv.Close()

	err := client.ConfigUnset(context.Background(), host, "m-123", "tok", "nonexistent.path")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key not found")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/agentclient/ -run TestConfig -v`
Expected: FAIL — `ConfigSet` and `ConfigUnset` undefined

- [ ] **Step 3: Implement ConfigSet and ConfigUnset**

Create `backend/internal/agentclient/config.go`:

```go
package agentclient

import (
	"context"
	"fmt"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// ConfigSet calls `openclaw config set <path> <value>` on a running machine
// via the agent exec endpoint. If strictJSON is true, appends --strict-json
// to force JSON5 parsing of the value.
func (c *Client) ConfigSet(ctx context.Context, host *store.Host, machineID, proxyToken, path, value string, strictJSON bool) error {
	cmd := []string{"openclaw", "config", "set", path, value}
	if strictJSON {
		cmd = append(cmd, "--strict-json")
	}
	result, err := c.ExecCommand(ctx, host, machineID, proxyToken, cmd)
	if err != nil {
		return fmt.Errorf("config set %s: %w", path, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("config set %s exit %d: %s", path, result.ExitCode, result.Stderr)
	}
	return nil
}

// ConfigUnset calls `openclaw config unset <path>` on a running machine
// via the agent exec endpoint. This removes the key entirely from openclaw.json.
func (c *Client) ConfigUnset(ctx context.Context, host *store.Host, machineID, proxyToken, path string) error {
	cmd := []string{"openclaw", "config", "unset", path}
	result, err := c.ExecCommand(ctx, host, machineID, proxyToken, cmd)
	if err != nil {
		return fmt.Errorf("config unset %s: %w", path, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("config unset %s exit %d: %s", path, result.ExitCode, result.Stderr)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/agentclient/ -run TestConfig -v`
Expected: All 5 tests PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/agentclient/config.go backend/internal/agentclient/config_test.go
git commit -m "feat: add ConfigSet/ConfigUnset helpers for openclaw config CLI"
```

---

### Task 3: Add ConfigBatch helper

**Files:**
- Modify: `backend/internal/agentclient/config.go`
- Modify: `backend/internal/agentclient/config_test.go`

- [ ] **Step 1: Write failing test for ConfigBatch**

Add to `backend/internal/agentclient/config_test.go`:

```go
func TestConfigBatch_AllSucceed(t *testing.T) {
	var callCount int
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(ExecResult{ExitCode: 0})
	})
	defer srv.Close()

	ops := []ConfigOp{
		{Op: "set", Path: "models.providers.anthropic.baseUrl", Value: "http://proxy/anthropic/v1"},
		{Op: "set", Path: "models.providers.anthropic.apiKey", Value: `{"source":"exec"}`, StrictJSON: true},
		{Op: "set", Path: "models.providers.anthropic.models", Value: "[]", StrictJSON: true},
	}
	errs := client.ConfigBatch(context.Background(), host, "m-123", "tok", ops)
	assert.Empty(t, errs)
	assert.Equal(t, 3, callCount)
}

func TestConfigBatch_PartialFailure(t *testing.T) {
	var callCount int
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 2 {
			json.NewEncoder(w).Encode(ExecResult{ExitCode: 1, Stderr: "fail"})
		} else {
			json.NewEncoder(w).Encode(ExecResult{ExitCode: 0})
		}
	})
	defer srv.Close()

	ops := []ConfigOp{
		{Op: "set", Path: "a", Value: "1"},
		{Op: "set", Path: "b", Value: "2"},
		{Op: "set", Path: "c", Value: "3"},
	}
	errs := client.ConfigBatch(context.Background(), host, "m-123", "tok", ops)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "fail")
	assert.Equal(t, 3, callCount, "should continue after failure")
}

func TestConfigBatch_WithUnset(t *testing.T) {
	var receivedCmds [][]string
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Command []string }
		json.NewDecoder(r.Body).Decode(&req)
		receivedCmds = append(receivedCmds, req.Command)
		json.NewEncoder(w).Encode(ExecResult{ExitCode: 0})
	})
	defer srv.Close()

	ops := []ConfigOp{
		{Op: "unset", Path: "channels.telegram"},
	}
	errs := client.ConfigBatch(context.Background(), host, "m-123", "tok", ops)
	assert.Empty(t, errs)
	assert.Equal(t, []string{"openclaw", "config", "unset", "channels.telegram"}, receivedCmds[0])
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/agentclient/ -run TestConfigBatch -v`
Expected: FAIL — `ConfigOp` and `ConfigBatch` undefined

- [ ] **Step 3: Implement ConfigBatch**

Add to `backend/internal/agentclient/config.go`:

```go
// ConfigOp represents a single config set or unset operation.
type ConfigOp struct {
	Op         string // "set" or "unset"
	Path       string
	Value      string // only for "set"
	StrictJSON bool   // only for "set"
}

// ConfigBatch executes a sequence of config set/unset operations.
// It does NOT stop on failure — all operations are attempted.
// Returns a slice of errors (empty if all succeeded).
func (c *Client) ConfigBatch(ctx context.Context, host *store.Host, machineID, proxyToken string, ops []ConfigOp) []error {
	var errs []error
	for _, op := range ops {
		var err error
		switch op.Op {
		case "set":
			err = c.ConfigSet(ctx, host, machineID, proxyToken, op.Path, op.Value, op.StrictJSON)
		case "unset":
			err = c.ConfigUnset(ctx, host, machineID, proxyToken, op.Path)
		default:
			err = fmt.Errorf("unknown config op: %s", op.Op)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/agentclient/ -run TestConfigBatch -v`
Expected: All 3 tests PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/agentclient/config.go backend/internal/agentclient/config_test.go
git commit -m "feat: add ConfigBatch for multi-step config operations"
```

---

### Task 4: Replace model push with ConfigSet

**Files:**
- Modify: `backend/internal/api/machine_config.go:730-757`

- [ ] **Step 1: Read the current handleSetMachineModel live update section**

Read `backend/internal/api/machine_config.go` lines 720-765 to understand the current flow.

- [ ] **Step 2: Replace the pushNativeConfig call with ConfigSet calls**

In `handleSetMachineModel`, replace the live update section (around lines 730-757) that builds a JSON patch and calls `pushNativeConfig` with:

Note: The current code does NOT check `machine.Status == "running"` — it only checks `machine.HostID != nil`. Preserve this existing behavior. Also, the current code uses `gatewayModel` as the variable name and does not have a `liveUpdateError` variable. Match existing patterns.

```go
	// Push model change to running VM via openclaw config set
	// (also sets the models catalog entry — gateway rejects unknown models)
	liveUpdate := "skipped"
	gatewayModel := configassembly.MapPlatformModel(req.Model)
	if machine.HostID != nil && s.agentClient != nil {
		host, err := s.store.GetHost(r.Context(), *machine.HostID)
		if err != nil || host == nil {
			slog.Warn("machine.model.push_host_failed", "machine_id", machineID, "error", err)
			liveUpdate = "failed"
		} else if machine.ProxyToken == nil {
			slog.Warn("machine.model.push_no_token", "machine_id", machineID)
			liveUpdate = "failed"
		} else {
			ops := []agentclient.ConfigOp{
				{Op: "set", Path: "agents.defaults.model.primary", Value: gatewayModel},
				{Op: "set", Path: "agents.defaults.models." + gatewayModel, Value: "{}", StrictJSON: true},
			}
			errs := s.agentClient.ConfigBatch(r.Context(), host, machineID, *machine.ProxyToken, ops)
			if len(errs) > 0 {
				slog.Error("machine.model.push_failed", "machine_id", machineID, "errors", errs)
				liveUpdate = "failed"
			} else {
				slog.Info("machine.model.push_ok", "machine_id", machineID, "gateway_model", gatewayModel)
				liveUpdate = "sent"
			}
		}
	}
```

- [ ] **Step 3: Verify build**

Run: `cd backend && go build ./cmd/server/`
Expected: No errors

- [ ] **Step 4: Run existing tests**

Run: `cd backend && go test ./internal/api/ -run TestSetMachineModel -v`
Expected: PASS (existing tests should still pass — they mock the agent client)

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/machine_config.go
git commit -m "feat: use openclaw config set for model push instead of config.patch"
```

---

### Task 5: Add `buildConfigOps` with diff-based unset support

**Files:**
- Create: `backend/internal/api/config_ops.go`
- Create: `backend/internal/api/config_ops_test.go`

This is the most complex task. The function must compare old vs new assembled configs to generate both `set` (for added/changed sections) and `unset` (for removed sections). Without `unset`, removed providers/channels would persist — the spec's core problem.

**Managed sections** (keys the UI controls):
- `models.providers.<name>` — each provider individually
- `channels.<name>` — each channel individually
- `agents.defaults.model.primary` — model selection
- `agents.defaults.models` — models catalog
- `ui.assistant` — identity
- `skills.allowBundled` — skill allowlist
- `browser` — browser config
- `plugins.entries.<name>` — each plugin individually
- `agents.list` — agent list

- [ ] **Step 1: Write failing tests for buildConfigOps**

Create `backend/internal/api/config_ops_test.go`:

```go
package api

import (
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildConfigOps_AddProvider(t *testing.T) {
	oldConfig := []byte(`{"models":{"providers":{"nebius":{"baseUrl":"http://proxy/nebius/v1"}}}}`)
	newConfig := []byte(`{"models":{"providers":{"nebius":{"baseUrl":"http://proxy/nebius/v1"},"anthropic":{"baseUrl":"http://proxy/anthropic/v1","apiKey":{"source":"exec"}}}}}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	// Should have set ops for both providers (nebius unchanged, anthropic added)
	var setPaths []string
	for _, op := range ops {
		if op.Op == "set" {
			setPaths = append(setPaths, op.Path)
		}
	}
	assert.Contains(t, setPaths, "models.providers.anthropic")
	// No unset ops
	for _, op := range ops {
		assert.NotEqual(t, "unset", op.Op, "should not unset anything")
	}
}

func TestBuildConfigOps_RemoveProvider(t *testing.T) {
	oldConfig := []byte(`{"models":{"providers":{"nebius":{},"anthropic":{"baseUrl":"http://proxy/anthropic/v1"}}}}`)
	newConfig := []byte(`{"models":{"providers":{"nebius":{}}}}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	// Should have unset for anthropic
	var unsetPaths []string
	for _, op := range ops {
		if op.Op == "unset" {
			unsetPaths = append(unsetPaths, op.Path)
		}
	}
	assert.Contains(t, unsetPaths, "models.providers.anthropic")
}

func TestBuildConfigOps_RemoveChannel(t *testing.T) {
	oldConfig := []byte(`{"channels":{"telegram":{"enabled":true},"discord":{"enabled":true}}}`)
	newConfig := []byte(`{"channels":{"telegram":{"enabled":true}}}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	var unsetPaths []string
	for _, op := range ops {
		if op.Op == "unset" {
			unsetPaths = append(unsetPaths, op.Path)
		}
	}
	assert.Contains(t, unsetPaths, "channels.discord")
}

func TestBuildConfigOps_AddChannel(t *testing.T) {
	oldConfig := []byte(`{}`)
	newConfig := []byte(`{"channels":{"telegram":{"enabled":true,"botToken":{"source":"exec"}}}}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	var setPaths []string
	for _, op := range ops {
		if op.Op == "set" {
			setPaths = append(setPaths, op.Path)
		}
	}
	assert.Contains(t, setPaths, "channels.telegram")
}

func TestBuildConfigOps_ModelAndIdentity(t *testing.T) {
	oldConfig := []byte(`{}`)
	newConfig := []byte(`{
		"agents":{"defaults":{"model":{"primary":"nebius/deepseek"},"models":{"nebius/deepseek":{}}}},
		"ui":{"assistant":{"name":"MyBot","avatar":"https://example.com/img.png"}}
	}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	pathSet := make(map[string]bool)
	for _, op := range ops {
		pathSet[op.Path] = true
	}
	assert.True(t, pathSet["agents.defaults.model.primary"])
	assert.True(t, pathSet["agents.defaults.models"])
	assert.True(t, pathSet["ui.assistant"])
}

func TestBuildConfigOps_RemoveBrowser(t *testing.T) {
	oldConfig := []byte(`{"browser":{"enabled":true,"cdpUrl":"http://bridge:9222"}}`)
	newConfig := []byte(`{}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	var unsetPaths []string
	for _, op := range ops {
		if op.Op == "unset" {
			unsetPaths = append(unsetPaths, op.Path)
		}
	}
	assert.Contains(t, unsetPaths, "browser")
}

func TestBuildConfigOps_RemovePlugin(t *testing.T) {
	oldConfig := []byte(`{"plugins":{"entries":{"myplugin":{"enabled":true}}}}`)
	newConfig := []byte(`{"plugins":{"entries":{}}}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	var unsetPaths []string
	for _, op := range ops {
		if op.Op == "unset" {
			unsetPaths = append(unsetPaths, op.Path)
		}
	}
	assert.Contains(t, unsetPaths, "plugins.entries.myplugin")
}

func TestBuildConfigOps_EmptyToEmpty(t *testing.T) {
	ops, err := buildConfigOps([]byte(`{}`), []byte(`{}`))
	require.NoError(t, err)
	assert.Empty(t, ops)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/api/ -run TestBuildConfigOps -v`
Expected: FAIL — `buildConfigOps` undefined

- [ ] **Step 3: Implement buildConfigOps with diff-based unset**

Create `backend/internal/api/config_ops.go`:

```go
package api

import (
	"encoding/json"
	"fmt"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
)

// buildConfigOps compares old and new assembled configs to generate
// openclaw config set/unset operations. Keys present in new but not old
// get "set". Keys present in old but not new get "unset".
//
// This is the core mechanism for fixing stale key deletion: when a provider
// or channel is removed in the UI, it appears in old but not new, generating
// an "unset" operation that removes it from openclaw.json.
func buildConfigOps(oldConfigJSON, newConfigJSON []byte) ([]agentclient.ConfigOp, error) {
	var oldConfig, newConfig map[string]interface{}
	if err := json.Unmarshal(oldConfigJSON, &oldConfig); err != nil {
		return nil, fmt.Errorf("parse old config: %w", err)
	}
	if err := json.Unmarshal(newConfigJSON, &newConfig); err != nil {
		return nil, fmt.Errorf("parse new config: %w", err)
	}

	var ops []agentclient.ConfigOp

	// --- Keyed sections: each sub-key is managed individually ---
	// Providers: models.providers.<name>
	ops = append(ops, diffKeyedSection(oldConfig, newConfig, "models.providers",
		[]string{"models", "providers"})...)

	// Channels: channels.<name>
	ops = append(ops, diffKeyedSection(oldConfig, newConfig, "channels",
		[]string{"channels"})...)

	// Plugins: plugins.entries.<name>
	ops = append(ops, diffKeyedSection(oldConfig, newConfig, "plugins.entries",
		[]string{"plugins", "entries"})...)

	// --- Scalar/object sections: set or unset the whole section ---
	// Model
	if primary := getNestedString(newConfig, "agents", "defaults", "model", "primary"); primary != "" {
		ops = append(ops, agentclient.ConfigOp{
			Op: "set", Path: "agents.defaults.model.primary", Value: primary,
		})
	}
	if modelsMap := getNestedMap(newConfig, "agents", "defaults", "models"); modelsMap != nil {
		modelsJSON, _ := json.Marshal(modelsMap)
		ops = append(ops, agentclient.ConfigOp{
			Op: "set", Path: "agents.defaults.models", Value: string(modelsJSON), StrictJSON: true,
		})
	}

	// Identity: ui.assistant (set whole object or unset)
	ops = append(ops, diffObjectSection(oldConfig, newConfig, "ui.assistant",
		[]string{"ui", "assistant"})...)

	// Skills: skills.allowBundled
	if allowBundled := getNestedValue(newConfig, "skills", "allowBundled"); allowBundled != nil {
		skillsJSON, _ := json.Marshal(allowBundled)
		ops = append(ops, agentclient.ConfigOp{
			Op: "set", Path: "skills.allowBundled", Value: string(skillsJSON), StrictJSON: true,
		})
	}

	// Browser: whole object
	ops = append(ops, diffObjectSection(oldConfig, newConfig, "browser",
		[]string{"browser"})...)

	// Agents list
	if list := getNestedValue(newConfig, "agents", "list"); list != nil {
		listJSON, _ := json.Marshal(list)
		ops = append(ops, agentclient.ConfigOp{
			Op: "set", Path: "agents.list", Value: string(listJSON), StrictJSON: true,
		})
	}

	return ops, nil
}

// diffKeyedSection compares a map-of-maps section between old and new configs.
// Keys in old but not new → unset. Keys in new → set.
func diffKeyedSection(oldConfig, newConfig map[string]interface{}, pathPrefix string, keys []string) []agentclient.ConfigOp {
	oldMap := getNestedMap(oldConfig, keys...)
	newMap := getNestedMap(newConfig, keys...)

	var ops []agentclient.ConfigOp

	// Unset keys removed from new
	for name := range oldMap {
		if _, exists := newMap[name]; !exists {
			ops = append(ops, agentclient.ConfigOp{
				Op: "unset", Path: pathPrefix + "." + name,
			})
		}
	}

	// Set keys in new (added or updated)
	for name, val := range newMap {
		valJSON, _ := json.Marshal(val)
		ops = append(ops, agentclient.ConfigOp{
			Op: "set", Path: pathPrefix + "." + name, Value: string(valJSON), StrictJSON: true,
		})
	}

	return ops
}

// diffObjectSection sets or unsets a whole object section.
// Present in new → set. Absent in new but present in old → unset.
func diffObjectSection(oldConfig, newConfig map[string]interface{}, path string, keys []string) []agentclient.ConfigOp {
	newVal := getNestedValue(newConfig, keys...)
	oldVal := getNestedValue(oldConfig, keys...)

	if newVal != nil {
		valJSON, _ := json.Marshal(newVal)
		return []agentclient.ConfigOp{{
			Op: "set", Path: path, Value: string(valJSON), StrictJSON: true,
		}}
	}
	if oldVal != nil {
		return []agentclient.ConfigOp{{Op: "unset", Path: path}}
	}
	return nil
}

// getNestedMap traverses a nested map by keys and returns the map at that path.
func getNestedMap(m map[string]interface{}, keys ...string) map[string]interface{} {
	current := m
	for _, k := range keys {
		next, ok := current[k].(map[string]interface{})
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

// getNestedString traverses a nested map and returns the string at that path.
func getNestedString(m map[string]interface{}, keys ...string) string {
	if len(keys) == 0 {
		return ""
	}
	parent := getNestedMap(m, keys[:len(keys)-1]...)
	if parent == nil {
		return ""
	}
	s, _ := parent[keys[len(keys)-1]].(string)
	return s
}

// getNestedValue traverses a nested map and returns the value at that path.
func getNestedValue(m map[string]interface{}, keys ...string) interface{} {
	if len(keys) == 0 {
		return nil
	}
	if len(keys) == 1 {
		return m[keys[0]]
	}
	parent := getNestedMap(m, keys[:len(keys)-1]...)
	if parent == nil {
		return nil
	}
	return parent[keys[len(keys)-1]]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/api/ -run TestBuildConfigOps -v`
Expected: All 8 tests PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/config_ops.go backend/internal/api/config_ops_test.go
git commit -m "feat: add buildConfigOps with diff-based set/unset generation"
```

---

### Task 6: Replace pushMachineConfigInternal live update with ConfigBatch

**Files:**
- Modify: `backend/internal/api/machine_config.go:565-592`

- [ ] **Step 1: Read the current pushMachineConfigInternal flow**

Read `backend/internal/api/machine_config.go` lines 515-606 to understand the full flow.

- [ ] **Step 2: Replace pushNativeConfig call with diff-based ConfigBatch**

In `pushMachineConfigInternal` (around lines 565-592), replace the `pushNativeConfig` call. The key change is fetching the *previous* assembled config to diff against the new one:

```go
	// If the machine is running, push config to the live VM via openclaw config set/unset
	liveUpdate := "not_running"
	liveUpdateError := ""
	if machine.Status == "running" && machine.HostID != nil {
		host, err := s.store.GetHost(r.Context(), *machine.HostID)
		if err != nil {
			slog.Warn("config.push.get_host_failed", "machine_id", machineID, "host_id", *machine.HostID, "error", err)
		} else if host == nil {
			slog.Warn("config.push.host_not_found", "machine_id", machineID, "host_id", *machine.HostID)
		} else if s.agentClient != nil {
			if machine.ProxyToken == nil {
				slog.Warn("config.push.no_proxy_token", "machine_id", machineID)
				liveUpdate = "failed"
				liveUpdateError = "no proxy token for config push"
			} else {
				// Get previous config for diffing (to detect removed keys)
				oldData := []byte("{}")
				if prevRecord, prevErr := s.store.GetMachineConfig(r.Context(), machineID); prevErr == nil && prevRecord != nil && prevRecord.AssembledConfig != nil {
					oldData = prevRecord.AssembledConfig
				}

				ops, buildErr := buildConfigOps(oldData, data)
				if buildErr != nil {
					slog.Error("config.push.build_ops_failed", "machine_id", machineID, "error", buildErr)
					liveUpdate = "failed"
					liveUpdateError = buildErr.Error()
				} else {
					errs := s.agentClient.ConfigBatch(r.Context(), host, machineID, *machine.ProxyToken, ops)
					if len(errs) > 0 {
						slog.Error("config.push.config_set_failed", "machine_id", machineID, "error_count", len(errs), "first_error", errs[0])
						liveUpdate = "failed"
						liveUpdateError = errs[0].Error()
					} else {
						slog.Info("config.push.config_set_ok", "machine_id", machineID, "op_count", len(ops))
						liveUpdate = "sent"
					}
				}
			}
		} else {
			liveUpdate = "skipped"
		}
	}
```

Note: `GetMachineConfig` is called AFTER `SetMachineAssembledConfig` (line 535) stores the new config. However, the live update section runs after the DB save, so we need the *previous* version. Solution: move the `GetMachineConfig` call to BEFORE `SetMachineAssembledConfig`, or read from the already-fetched `configVersion` context. Since `configVersion` is fetched at line 519 via `assembleConfigForMachine`, and the old assembled config is in the DB at that point, read it before the `SetMachineAssembledConfig` call. Restructure the live update section to capture `oldData` early.

Actually, looking at the code flow more carefully: `pushMachineConfigInternal` calls `assembleConfigForMachine` which internally calls `GetMachineConfig` to get the config version. The simplest fix is to fetch `oldData` at the top of `pushMachineConfigInternal`, before `SetMachineAssembledConfig` overwrites it:

```go
// At top of pushMachineConfigInternal, after assembleConfigForMachine:
oldData := []byte("{}")
if prevRecord, prevErr := s.store.GetMachineConfig(r.Context(), machineID); prevErr == nil && prevRecord != nil && prevRecord.AssembledConfig != nil {
    oldData = prevRecord.AssembledConfig
}
```

Then use `oldData` in the live update section below. If no previous config exists (first push), `oldData` defaults to `{}` and all sections generate `set` ops only.

- [ ] **Step 3: Verify build**

Run: `cd backend && go build ./cmd/server/`
Expected: No errors

- [ ] **Step 4: Run full test suite**

Run: `cd backend && go test ./internal/api/ -v -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/machine_config.go
git commit -m "feat: replace pushNativeConfig with diff-based config set/unset ops"
```

---

### Task 7: Clean up pushNativeConfig

**Files:**
- Modify: `backend/internal/api/machine_config.go`

- [ ] **Step 1: Remove pushNativeConfig function**

Delete the `pushNativeConfig` function (lines 611-652) since it's no longer called.

- [ ] **Step 2: Remove any unused imports**

Check if `config.patch` or `config.get` related code is still referenced anywhere.

- [ ] **Step 3: Verify build and tests**

Run: `cd backend && go build ./cmd/server/ && go test ./internal/api/ -v -count=1`
Expected: Build succeeds, all tests pass

- [ ] **Step 4: Run gateway E2E tests**

Run: `make test-gateway-e2e`
Expected: PASS (except pre-existing OpenAI key issue)

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/machine_config.go
git commit -m "chore: remove pushNativeConfig (replaced by config set/unset)"
```

---

### Task 8: Run full test suite

- [ ] **Step 1: Run all Go tests**

Run: `make test-go`
Expected: PASS

- [ ] **Step 2: Run gateway E2E tests**

Run: `make test-gateway-e2e`
Expected: PASS (except pre-existing OpenAI key issue)
