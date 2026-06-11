package ptyd

import (
	"encoding/json"
	"fmt"

	"github.com/mathaix/openclawmachines/backend/internal/openclawver"
)

// authStoreGeneration identifies how the resolved OpenClaw runtime persists
// auth profiles.
type authStoreGeneration int

const (
	// authStoreJSON — runtimes ≤ 2026.5.x read auth-profiles.json directly.
	authStoreJSON authStoreGeneration = iota
	// authStoreSQLite — runtimes ≥ 2026.6 read openclaw-agent.sqlite and
	// ignore auth-profiles.json at runtime (it is only imported once by
	// `openclaw doctor --fix`).
	authStoreSQLite
)

// authStoreSQLiteCutoverYear/Month is the OpenClaw train that moved auth
// profiles into SQLite (2026.6.x, #89102 upstream).
const (
	authStoreSQLiteCutoverYear  = 2026
	authStoreSQLiteCutoverMonth = 6
)

// authStoreGenerationForVersion decides the auth store generation from the
// resolved runtime version (OCM_RESOLVED_OPENCLAW_VERSION). When the version
// is missing or unparseable, it falls back to checking whether the SQLite
// store exists on disk.
func authStoreGenerationForVersion(version string, sqliteExists func() bool) authStoreGeneration {
	if atLeast, ok := openclawver.AtLeast(version, authStoreSQLiteCutoverYear, authStoreSQLiteCutoverMonth); ok {
		if atLeast {
			return authStoreSQLite
		}
		return authStoreJSON
	}
	if sqliteExists != nil && sqliteExists() {
		return authStoreSQLite
	}
	return authStoreJSON
}

// parseAuthStoreJSON parses the auth profile store document. The same JSON
// shape is used by both generations: the legacy auth-profiles.json file and
// the store_json blob in the SQLite auth_profile_store table.
func parseAuthStoreJSON(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty auth store document")
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse auth store document: %w", err)
	}
	return doc, nil
}

// findCodexProfile locates the Codex OAuth profile in an auth store document.
// Legacy stores key it "openai-codex:default" (provider "openai-codex");
// 2026.6.x migrates it to "openai:default" (provider "openai"). The migrated
// key is only accepted when it is an OAuth credential — an "openai:default"
// API-key profile is a different thing entirely.
func findCodexProfile(doc map[string]any) (map[string]any, bool) {
	profiles, _ := doc["profiles"].(map[string]any)
	if profiles == nil {
		return nil, false
	}
	if p, ok := profiles["openai-codex:default"].(map[string]any); ok {
		return p, true
	}
	if p, ok := profiles["openai:default"].(map[string]any); ok {
		if typ, _ := p["type"].(string); typ == "oauth" {
			return p, true
		}
	}
	return nil, false
}
