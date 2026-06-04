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

// Channels are managed by the channel state machine, NOT by buildConfigOps.
// Verify that buildConfigOps never generates channel ops.
func TestBuildConfigOps_ChannelsIgnored(t *testing.T) {
	oldConfig := []byte(`{"channels":{"telegram":{"enabled":true},"discord":{"enabled":true}}}`)
	newConfig := []byte(`{"channels":{"telegram":{"enabled":true}}}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	for _, op := range ops {
		assert.NotContains(t, op.Path, "channels.", "buildConfigOps must not generate channel ops")
	}
}

func TestBuildConfigOps_AddChannelIgnored(t *testing.T) {
	oldConfig := []byte(`{}`)
	newConfig := []byte(`{"channels":{"telegram":{"enabled":true,"botToken":{"source":"exec"}}}}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	for _, op := range ops {
		assert.NotContains(t, op.Path, "channels.", "buildConfigOps must not generate channel ops")
	}
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
	replaceSet := make(map[string]bool)
	for _, op := range ops {
		pathSet[op.Path] = true
		if op.Replace {
			replaceSet[op.Path] = true
		}
	}
	assert.True(t, pathSet["agents.defaults.model"])
	assert.True(t, pathSet["agents.defaults.models"])
	assert.True(t, replaceSet["agents.defaults.model"], "model defaults must replace VM-local stale fallbacks")
	assert.True(t, replaceSet["agents.defaults.models"], "model catalog must replace VM-local stale entries")
	assert.True(t, pathSet["ui.assistant"])
}

func TestBuildConfigOps_ModelsCatalogReplaceRemovesStaleVMEntries(t *testing.T) {
	oldConfig := []byte(`{
		"agents":{"defaults":{"models":{
			"openai/gpt-5.4":{},
			"openai/gpt-5.5":{}
		}}}
	}`)
	newConfig := []byte(`{
		"agents":{"defaults":{"models":{
			"openai/gpt-5.5":{}
		}}}
	}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	require.Len(t, ops, 1)
	assert.Equal(t, "set", ops[0].Op)
	assert.Equal(t, "agents.defaults.models", ops[0].Path)
	assert.True(t, ops[0].StrictJSON)
	assert.True(t, ops[0].Replace)
	assert.JSONEq(t, `{"openai/gpt-5.5":{}}`, ops[0].Value)
}

func TestBuildConfigOps_ModelsCatalogReplaceEvenWhenSavedConfigMatches(t *testing.T) {
	config := []byte(`{
		"agents":{"defaults":{"models":{
			"openai/gpt-5.5":{}
		}}}
	}`)

	ops, err := buildConfigOps(config, config)
	require.NoError(t, err)

	require.Len(t, ops, 1)
	assert.Equal(t, "agents.defaults.models", ops[0].Path)
	assert.True(t, ops[0].Replace)
	assert.JSONEq(t, `{"openai/gpt-5.5":{}}`, ops[0].Value)
}

func TestBuildConfigOps_PrimaryModelSetEvenWhenSavedConfigMatches(t *testing.T) {
	config := []byte(`{
		"agents":{"defaults":{
			"model":{"primary":"codex/gpt-5.5"},
			"models":{"codex/gpt-5.5":{"agentRuntime":{"id":"codex"}}}
		}}
	}`)

	ops, err := buildConfigOps(config, config)
	require.NoError(t, err)

	pathSet := make(map[string]agentclient.ConfigOp)
	for _, op := range ops {
		pathSet[op.Path] = op
	}
	model, ok := pathSet["agents.defaults.model"]
	require.True(t, ok, "model defaults must be pushed even when DB snapshots match")
	assert.Equal(t, "set", model.Op)
	assert.True(t, model.Replace)
	assert.JSONEq(t, `{"primary":"codex/gpt-5.5"}`, model.Value)

	models, ok := pathSet["agents.defaults.models"]
	require.True(t, ok, "models catalog should still be force replaced")
	assert.True(t, models.Replace)
	assert.JSONEq(t, `{"codex/gpt-5.5":{"agentRuntime":{"id":"codex"}}}`, models.Value)
}

func TestBuildConfigOps_ModelFallbacksReplaceWholeModelObject(t *testing.T) {
	config := []byte(`{
		"agents":{"defaults":{
			"model":{"primary":"codex/gpt-5.5","fallbacks":["anthropic/claude-opus-4-6","nebius/Qwen/Qwen3.5-397B-A17B"]},
			"models":{"codex/gpt-5.5":{"agentRuntime":{"id":"codex"}}}
		}}
	}`)

	ops, err := buildConfigOps(config, config)
	require.NoError(t, err)

	pathSet := make(map[string]agentclient.ConfigOp)
	for _, op := range ops {
		pathSet[op.Path] = op
	}
	model, ok := pathSet["agents.defaults.model"]
	require.True(t, ok)
	assert.True(t, model.Replace)
	assert.JSONEq(t, `{"primary":"codex/gpt-5.5","fallbacks":["anthropic/claude-opus-4-6","nebius/Qwen/Qwen3.5-397B-A17B"]}`, model.Value)
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

func TestBuildConfigOps_RemoveSkillsAllowBundled(t *testing.T) {
	oldConfig := []byte(`{"skills":{"allowBundled":["skill-a","skill-b"]}}`)
	newConfig := []byte(`{}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	var unsetPaths []string
	for _, op := range ops {
		if op.Op == "unset" {
			unsetPaths = append(unsetPaths, op.Path)
		}
	}
	assert.Contains(t, unsetPaths, "skills.allowBundled", "removing all bundled skills should generate an unset op")
}

func TestBuildConfigOps_RemoveAgentsList(t *testing.T) {
	oldConfig := []byte(`{"agents":{"list":[{"name":"agent1"},{"name":"agent2"}]}}`)
	newConfig := []byte(`{}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	var unsetPaths []string
	for _, op := range ops {
		if op.Op == "unset" {
			unsetPaths = append(unsetPaths, op.Path)
		}
	}
	assert.Contains(t, unsetPaths, "agents.list", "removing agents list should generate an unset op")
}

func TestBuildConfigOps_UpdateSkillsAllowBundled(t *testing.T) {
	oldConfig := []byte(`{"skills":{"allowBundled":["skill-a"]}}`)
	newConfig := []byte(`{"skills":{"allowBundled":["skill-a","skill-b"]}}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	var setPaths []string
	for _, op := range ops {
		if op.Op == "set" {
			setPaths = append(setPaths, op.Path)
		}
	}
	assert.Contains(t, setPaths, "skills.allowBundled")
}

func TestBuildConfigOps_UpdateAgentsList(t *testing.T) {
	oldConfig := []byte(`{"agents":{"list":[{"name":"agent1"}]}}`)
	newConfig := []byte(`{"agents":{"list":[{"name":"agent1"},{"name":"agent2"}]}}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	var setPaths []string
	for _, op := range ops {
		if op.Op == "set" {
			setPaths = append(setPaths, op.Path)
		}
	}
	assert.Contains(t, setPaths, "agents.list")
}

func TestBuildConfigOps_UpdatePluginsAllow(t *testing.T) {
	oldConfig := []byte(`{"plugins":{"allow":["memory-core"]}}`)
	newConfig := []byte(`{"plugins":{"allow":["memory-core","search-core"]}}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	var setPaths []string
	for _, op := range ops {
		if op.Op == "set" {
			setPaths = append(setPaths, op.Path)
		}
	}
	assert.Contains(t, setPaths, "plugins.allow")
}

func TestBuildConfigOps_UpdateSearchToolConfig(t *testing.T) {
	oldConfig := []byte(`{"tools":{"web":{"search":{"enabled":true,"provider":"google"}}}}`)
	newConfig := []byte(`{"tools":{"web":{"search":{"enabled":true,"provider":"tavily"}}}}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	var setPaths []string
	for _, op := range ops {
		if op.Op == "set" {
			setPaths = append(setPaths, op.Path)
		}
	}
	assert.Contains(t, setPaths, "tools.web.search")
}

func TestBuildConfigOps_UpdateAuthProfiles(t *testing.T) {
	oldConfig := []byte(`{"auth":{"profiles":{"openai-codex:default":{"provider":"openai-codex","mode":"oauth"}}}}`)
	newConfig := []byte(`{"auth":{"profiles":{}}}`)

	ops, err := buildConfigOps(oldConfig, newConfig)
	require.NoError(t, err)

	var setPaths []string
	for _, op := range ops {
		if op.Op == "set" {
			setPaths = append(setPaths, op.Path)
		}
	}
	assert.Contains(t, setPaths, "auth.profiles")
}
