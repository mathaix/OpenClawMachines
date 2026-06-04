package agentclient

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigUnset_Success(t *testing.T) {
	var receivedCmd []string
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Command []string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		receivedCmd = req.Command
		_ = json.NewEncoder(w).Encode(ExecResult{ExitCode: 0, Stdout: ""})
	})
	defer srv.Close()

	err := client.ConfigUnset(context.Background(), host, "m-123", "tok", "channels.telegram")
	require.NoError(t, err)
	assert.Equal(t, []string{"openclaw", "config", "unset", "channels.telegram"}, receivedCmd)
}

func TestConfigUnset_NonZeroExit(t *testing.T) {
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ExecResult{ExitCode: 1, Stderr: "key not found"})
	})
	defer srv.Close()

	err := client.ConfigUnset(context.Background(), host, "m-123", "tok", "nonexistent.path")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key not found")
}

func TestConfigBatch_AllSets_SingleExecCall(t *testing.T) {
	var receivedCmds [][]string
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Command []string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		receivedCmds = append(receivedCmds, req.Command)
		_ = json.NewEncoder(w).Encode(ExecResult{ExitCode: 0})
	})
	defer srv.Close()

	ops := []ConfigOp{
		{Op: "set", Path: "models.providers.anthropic.baseUrl", Value: "http://proxy/anthropic/v1"},
		{Op: "set", Path: "models.providers.anthropic.apiKey", Value: `{"source":"exec"}`, StrictJSON: true},
		{Op: "set", Path: "models.providers.anthropic.models", Value: "[]", StrictJSON: true},
	}
	errs := client.ConfigBatch(context.Background(), host, "m-123", "tok", ops)
	assert.Empty(t, errs)
	require.Len(t, receivedCmds, 2, "set ops should be one --batch-json call plus one symlink repair call")
	receivedCmd := receivedCmds[0]
	assert.Equal(t, []string{"sh", "-c", repairOpenClawConfigSymlinkScript}, receivedCmds[1])

	// Verify the command structure
	require.Len(t, receivedCmd, 5)
	assert.Equal(t, "openclaw", receivedCmd[0])
	assert.Equal(t, "config", receivedCmd[1])
	assert.Equal(t, "set", receivedCmd[2])
	assert.Equal(t, "--batch-json", receivedCmd[3])

	// Verify the batch JSON payload
	var entries []batchEntry
	require.NoError(t, json.Unmarshal([]byte(receivedCmd[4]), &entries))
	assert.Len(t, entries, 3)

	assert.Equal(t, "models.providers.anthropic.baseUrl", entries[0].Path)
	assert.Equal(t, "http://proxy/anthropic/v1", entries[0].Value) // plain string

	assert.Equal(t, "models.providers.anthropic.apiKey", entries[1].Path)
	// StrictJSON value should be a parsed JSON object, not an escaped string
	apiKey, ok := entries[1].Value.(map[string]interface{})
	assert.True(t, ok, "apiKey should be a JSON object, got %T", entries[1].Value)
	assert.Equal(t, "exec", apiKey["source"])

	assert.Equal(t, "models.providers.anthropic.models", entries[2].Path)
	// StrictJSON "[]" should be a parsed empty array
	models, ok := entries[2].Value.([]interface{})
	assert.True(t, ok, "models should be a JSON array, got %T", entries[2].Value)
	assert.Empty(t, models)
}

func TestConfigBatch_MixedSetAndUnset(t *testing.T) {
	var receivedCmds [][]string
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Command []string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		receivedCmds = append(receivedCmds, req.Command)
		_ = json.NewEncoder(w).Encode(ExecResult{ExitCode: 0})
	})
	defer srv.Close()

	ops := []ConfigOp{
		{Op: "set", Path: "agents.defaults.model.primary", Value: "nebius/deepseek"},
		{Op: "set", Path: "agents.defaults.models", Value: `{"nebius/deepseek":{}}`, StrictJSON: true},
		{Op: "unset", Path: "channels.telegram"},
		{Op: "unset", Path: "models.providers.anthropic"},
	}
	errs := client.ConfigBatch(context.Background(), host, "m-123", "tok", ops)
	assert.Empty(t, errs)
	// 1 batch-json call for sets + 2 individual unset calls + 1 repair call.
	assert.Len(t, receivedCmds, 4)

	// First call should be the batch set
	assert.Equal(t, "--batch-json", receivedCmds[0][3])

	// Remaining calls should be individual unsets
	assert.Equal(t, []string{"openclaw", "config", "unset", "channels.telegram"}, receivedCmds[1])
	assert.Equal(t, []string{"openclaw", "config", "unset", "models.providers.anthropic"}, receivedCmds[2])
	assert.Equal(t, []string{"sh", "-c", repairOpenClawConfigSymlinkScript}, receivedCmds[3])
}

func TestConfigBatch_ReplaceSetRunsFirstAndUsesReplaceFlag(t *testing.T) {
	var receivedCmds [][]string
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Command []string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		receivedCmds = append(receivedCmds, req.Command)
		_ = json.NewEncoder(w).Encode(ExecResult{ExitCode: 0})
	})
	defer srv.Close()

	ops := []ConfigOp{
		{Op: "set", Path: "agents.defaults.model.primary", Value: "openai/gpt-5.5"},
		{Op: "set", Path: "agents.defaults.models", Value: `{"openai/gpt-5.5":{}}`, StrictJSON: true, Replace: true},
	}
	errs := client.ConfigBatch(context.Background(), host, "m-123", "tok", ops)
	assert.Empty(t, errs)
	require.Len(t, receivedCmds, 3)

	require.Len(t, receivedCmds[0], 6)
	assert.Equal(t, []string{"openclaw", "config", "set", "--replace", "--batch-json"}, receivedCmds[0][:5])
	var replaceEntries []batchEntry
	require.NoError(t, json.Unmarshal([]byte(receivedCmds[0][5]), &replaceEntries))
	require.Len(t, replaceEntries, 1)
	assert.Equal(t, "agents.defaults.models", replaceEntries[0].Path)

	require.Len(t, receivedCmds[1], 5)
	assert.Equal(t, "openclaw", receivedCmds[1][0])
	assert.Equal(t, "config", receivedCmds[1][1])
	assert.Equal(t, "set", receivedCmds[1][2])
	assert.Equal(t, "--batch-json", receivedCmds[1][3])
	assert.Equal(t, []string{"sh", "-c", repairOpenClawConfigSymlinkScript}, receivedCmds[2])
}

func TestConfigBatch_OnlyUnsets(t *testing.T) {
	var receivedCmds [][]string
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Command []string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		receivedCmds = append(receivedCmds, req.Command)
		_ = json.NewEncoder(w).Encode(ExecResult{ExitCode: 0})
	})
	defer srv.Close()

	ops := []ConfigOp{
		{Op: "unset", Path: "channels.telegram"},
	}
	errs := client.ConfigBatch(context.Background(), host, "m-123", "tok", ops)
	assert.Empty(t, errs)
	assert.Len(t, receivedCmds, 2)
	assert.Equal(t, []string{"openclaw", "config", "unset", "channels.telegram"}, receivedCmds[0])
	assert.Equal(t, []string{"sh", "-c", repairOpenClawConfigSymlinkScript}, receivedCmds[1])
}

func TestConfigBatch_BatchSetFailure(t *testing.T) {
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ExecResult{ExitCode: 1, Stderr: "invalid path"})
	})
	defer srv.Close()

	ops := []ConfigOp{
		{Op: "set", Path: "bad.path", Value: "val"},
	}
	errs := client.ConfigBatch(context.Background(), host, "m-123", "tok", ops)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "invalid path")
}

func TestRepairOpenClawConfigSymlinkScript_RestoresSplitBrainConfig(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".openclaw")
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "configs", "v1"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "configs", "v1", "openclaw.json"), []byte(`{"agents":{"defaults":{"model":{"primary":"old"}}}}`), 0600))
	require.NoError(t, os.Symlink(filepath.Join("configs", "v1"), filepath.Join(stateDir, "config-current")))

	// Reproduce the OpenClaw CLI bug: the stable symlink has been replaced by a
	// regular file containing the new config, while config-current is stale.
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "openclaw.json"), []byte(`{"agents":{"defaults":{"model":{"primary":"new"}}}}`), 0600))

	cmd := exec.Command("sh", "-c", repairOpenClawConfigSymlinkScript)
	cmd.Env = append(os.Environ(), "OPENCLAW_STATE_DIR="+stateDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	linkInfo, err := os.Lstat(filepath.Join(stateDir, "openclaw.json"))
	require.NoError(t, err)
	require.NotZero(t, linkInfo.Mode()&os.ModeSymlink, "openclaw.json should be restored as a symlink")
	target, err := os.Readlink(filepath.Join(stateDir, "openclaw.json"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("config-current", "openclaw.json"), target)

	data, err := os.ReadFile(filepath.Join(stateDir, "config-current", "openclaw.json"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"agents":{"defaults":{"model":{"primary":"new"}}}}`, string(data))

	orphans, err := filepath.Glob(filepath.Join(stateDir, "openclaw.json.orphan.*"))
	require.NoError(t, err)
	require.Len(t, orphans, 1, "orphan regular-file write should be preserved for forensics")
}

func TestConfigBatch_Empty(t *testing.T) {
	errs := (&Client{}).ConfigBatch(context.Background(), nil, "", "", nil)
	assert.Nil(t, errs)
}

func TestHermesConfigSync(t *testing.T) {
	var receivedCmd []string
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Command []string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		receivedCmd = req.Command
		_ = json.NewEncoder(w).Encode(ExecResult{ExitCode: 0})
	})
	defer srv.Close()

	err := client.HermesConfigSync(context.Background(), host, "m-123", "tok", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"ocm-hermes-config", "sync", "--restart-gateway"}, receivedCmd)
}

func TestHermesConfigSync_NonZeroExit(t *testing.T) {
	srv, host, client := newExecTestAgent(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ExecResult{ExitCode: 1, Stderr: "metadata response did not include HERMES_CONFIG_YAML"})
	})
	defer srv.Close()

	err := client.HermesConfigSync(context.Background(), host, "m-123", "tok", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata response did not include HERMES_CONFIG_YAML")
}
