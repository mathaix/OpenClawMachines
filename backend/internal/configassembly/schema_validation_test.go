package configassembly

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestAssembledConfig_PassesOpenClawSchema validates assembled configs against
// OpenClaw's Zod schema by calling `openclaw config validate --json`.
//
// This catches schema drift — when the Go assembler produces keys the gateway
// rejects (e.g., gateway.bind in JSON, renamed fields, new required keys).
//
// Requires: ~/openclaw built (`npm run build`). Skips if not available.
func TestAssembledConfig_PassesOpenClawSchema(t *testing.T) {
	openclawDir := os.Getenv("OPENCLAW_DIR")
	if openclawDir == "" {
		openclawDir = filepath.Join(os.Getenv("HOME"), "openclaw")
	}
	entryJS := filepath.Join(openclawDir, "dist", "entry.js")
	if _, err := os.Stat(entryJS); err != nil {
		t.Skipf("openclaw not built at %s — skipping schema validation", entryJS)
	}

	mc := testModelCatalog()
	tests := []struct {
		name   string
		params AssemblyParams
	}{
		{
			name:   "platform_defaults_only",
			params: AssemblyParams{MachineID: "m-test", ModelCatalog: mc},
		},
		{
			name: "with_anthropic_credential",
			params: AssemblyParams{
				MachineID:    "m-test",
				ModelCatalog: mc,
				Credentials:  map[string]string{"anthropic": "present"},
				ProxyBaseURL: "http://192.168.100.1:4000",
				DefaultModel: "anthropic/claude-sonnet-4-6",
			},
		},
		{
			name: "with_all_providers",
			params: AssemblyParams{
				MachineID:    "m-test",
				ModelCatalog: mc,
				Credentials:  map[string]string{"anthropic": "present", "openai": "present", "google": "present"},
				ProxyBaseURL: "http://192.168.100.1:4000",
				DefaultModel: "anthropic/claude-sonnet-4-6",
			},
		},
		{
			name: "with_identity",
			params: AssemblyParams{
				MachineID:    "m-test",
				ModelCatalog: mc,
				Identity:     &Identity{Name: strPtr("TestBot"), Avatar: strPtr("robot")},
			},
		},
		{
			name: "with_channel_capability",
			params: AssemblyParams{
				MachineID:    "m-test",
				ModelCatalog: mc,
				Capabilities: []CapabilityWithTemplate{{
					EntryID:        "telegram",
					EntryType:      "channel",
					ConfigTemplate: map[string]interface{}{"channels": map[string]interface{}{"telegram": map[string]interface{}{"enabled": true}}},
				}},
			},
		},
		{
			name: "with_skills",
			params: AssemblyParams{
				MachineID:    "m-test",
				ModelCatalog: mc,
				Capabilities: []CapabilityWithTemplate{
					{EntryID: "web-search", EntryType: "skill", ConfigTemplate: map[string]interface{}{"skills": map[string]interface{}{"entries": map[string]interface{}{"web-search": map[string]interface{}{"enabled": true}}}}},
					{EntryID: "code-interpreter", EntryType: "skill", ConfigTemplate: map[string]interface{}{"skills": map[string]interface{}{"entries": map[string]interface{}{"code-interpreter": map[string]interface{}{"enabled": true}}}}},
				},
			},
		},
		{
			name: "with_config_overrides",
			params: AssemblyParams{
				MachineID:    "m-test",
				ModelCatalog: mc,
				Capabilities: []CapabilityWithTemplate{{
					EntryID:         "telegram",
					EntryType:       "channel",
					ConfigTemplate:  map[string]interface{}{"channels": map[string]interface{}{"telegram": map[string]interface{}{"enabled": true}}},
					ConfigOverrides: map[string]interface{}{"channels": map[string]interface{}{"telegram": map[string]interface{}{"groupPolicy": "open"}}},
				}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := AssembleConfig(tt.params)
			if err != nil {
				t.Fatalf("AssembleConfig: %v", err)
			}

			// Write config to a temp file — v2026.3.8+ validates the config file,
			// not stdin. Point OPENCLAW_CONFIG_PATH at the temp file.
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "openclaw.json")
			if err := os.WriteFile(configPath, data, 0644); err != nil {
				t.Fatalf("write temp config: %v", err)
			}

			cmd := exec.Command("node", entryJS, "config", "validate", "--json")
			cmd.Dir = openclawDir
			cmd.Env = append(os.Environ(),
				"OPENCLAW_CONFIG_PATH="+configPath,
				"OPENCLAW_STATE_DIR="+tmpDir,
			)

			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("OpenClaw Zod schema rejected assembled config:\n%s\n\nConfig:\n%s", string(out), prettyJSON(data))
			}

			// Parse the validation result
			var result struct {
				Valid    bool              `json:"valid"`
				Issues   []json.RawMessage `json:"issues"`
				Warnings []json.RawMessage `json:"warnings"`
			}
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("parse validation output: %v\nraw: %s", err, string(out))
			}
			if !result.Valid {
				t.Fatalf("config not valid: %s", string(out))
			}
			if len(result.Warnings) > 0 {
				t.Logf("warnings: %s", string(out))
			}
		})
	}
}

func strPtr(s string) *string { return &s }

func prettyJSON(data []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		return string(data)
	}
	return buf.String()
}
