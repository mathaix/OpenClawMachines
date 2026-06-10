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

	// Channels: managed by channel state machine (handleChannelConnect/Disconnect/Settings).
	// NOT diffed here — channel transitions are the only code path that produces
	// set/unset channels.* ops, preventing accidental wipes from other domains.

	// Plugins: plugins.entries.<name>
	ops = append(ops, diffKeyedSection(oldConfig, newConfig, "plugins.entries",
		[]string{"plugins", "entries"})...)

	// --- Scalar/object sections: set or unset the whole section ---
	// Plugin allowlist
	ops = append(ops, diffObjectSection(oldConfig, newConfig, "plugins.allow",
		[]string{"plugins", "allow"})...)

	// Models catalog
	if modelsMap := getNestedMap(newConfig, "agents", "defaults", "models"); modelsMap != nil {
		modelsJSON, _ := json.Marshal(modelsMap)
		// Always push this OCM-owned catalog with --replace. A previous live
		// update may have failed after the desired config was already saved, so
		// DB-vs-DB diffing is not enough to repair stale VM-local model entries.
		ops = append(ops, agentclient.ConfigOp{
			Op: "set", Path: "agents.defaults.models", Value: string(modelsJSON), StrictJSON: true, Replace: true,
		})
	}
	// Model defaults (primary + fallbacks). Always replace this OCM-owned object
	// after replacing the model catalog so stale VM-local fallback chains are
	// removed when the desired config no longer contains them.
	if modelMap := getNestedMap(newConfig, "agents", "defaults", "model"); modelMap != nil {
		modelJSON, _ := json.Marshal(modelMap)
		ops = append(ops, agentclient.ConfigOp{
			Op: "set", Path: "agents.defaults.model", Value: string(modelJSON), StrictJSON: true, Replace: true,
		})
	}

	// Identity: ui.assistant (set whole object or unset)
	ops = append(ops, diffObjectSection(oldConfig, newConfig, "ui.assistant",
		[]string{"ui", "assistant"})...)

	// Skills: skills.allowBundled
	ops = append(ops, diffObjectSection(oldConfig, newConfig, "skills.allowBundled",
		[]string{"skills", "allowBundled"})...)

	// Browser: whole object
	ops = append(ops, diffObjectSection(oldConfig, newConfig, "browser",
		[]string{"browser"})...)

	// Search tool selection
	ops = append(ops, diffObjectSection(oldConfig, newConfig, "tools.web.search",
		[]string{"tools", "web", "search"})...)

	// Auth profiles
	ops = append(ops, diffObjectSection(oldConfig, newConfig, "auth.profiles",
		[]string{"auth", "profiles"})...)

	// Agents list
	ops = append(ops, diffObjectSection(oldConfig, newConfig, "agents.list",
		[]string{"agents", "list"})...)

	return ops, nil
}

// diffKeyedSection compares a map-of-maps section between old and new configs.
// Keys in old but not new -> unset. Keys in new -> set.
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

	// Set keys that are new or changed
	for name, val := range newMap {
		valJSON, _ := json.Marshal(val)
		if oldVal, exists := oldMap[name]; exists {
			oldJSON, _ := json.Marshal(oldVal)
			if string(oldJSON) == string(valJSON) {
				continue // unchanged
			}
		}
		ops = append(ops, agentclient.ConfigOp{
			Op: "set", Path: pathPrefix + "." + name, Value: string(valJSON), StrictJSON: true,
		})
	}

	return ops
}

// diffObjectSection sets or unsets a whole object section.
// Present in new -> set. Absent in new but present in old -> unset.
func diffObjectSection(oldConfig, newConfig map[string]interface{}, path string, keys []string) []agentclient.ConfigOp {
	newVal := getNestedValue(newConfig, keys...)
	oldVal := getNestedValue(oldConfig, keys...)

	if newVal != nil {
		valJSON, _ := json.Marshal(newVal)
		if oldVal != nil {
			oldJSON, _ := json.Marshal(oldVal)
			if string(oldJSON) == string(valJSON) {
				return nil // unchanged
			}
		}
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
