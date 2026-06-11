package ptyd

import (
	"encoding/json"
	"testing"
)

func TestAuthStoreGenerationForVersion(t *testing.T) {
	tests := []struct {
		name         string
		version      string
		sqliteExists bool
		want         authStoreGeneration
	}{
		// Release-manifest style versions (vYYYY.M.P-rN)
		{name: "deployed 5.28 release", version: "v2026.5.28-r4", want: authStoreJSON},
		{name: "6.5 release", version: "v2026.6.5-r1", want: authStoreSQLite},
		// Bare npm versions
		{name: "bare 5.28", version: "2026.5.28", want: authStoreJSON},
		{name: "bare 6.5", version: "2026.6.5", want: authStoreSQLite},
		{name: "bare 6.1", version: "2026.6.1", want: authStoreSQLite},
		// Prerelease suffixes
		{name: "beta suffix", version: "2026.6.5-beta.2", want: authStoreSQLite},
		// Later trains
		{name: "later month", version: "2026.12.1", want: authStoreSQLite},
		{name: "later year", version: "2027.1.0", want: authStoreSQLite},
		// Earlier trains
		{name: "early 2026", version: "2026.4.2", want: authStoreJSON},
		{name: "old 4.x scheme", version: "4.26.0", want: authStoreJSON},
		// Unknown versions fall back to filesystem detection
		{name: "empty with sqlite", version: "", sqliteExists: true, want: authStoreSQLite},
		{name: "empty without sqlite", version: "", sqliteExists: false, want: authStoreJSON},
		{name: "garbage with sqlite", version: "not-a-version", sqliteExists: true, want: authStoreSQLite},
		{name: "garbage without sqlite", version: "not-a-version", sqliteExists: false, want: authStoreJSON},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := authStoreGenerationForVersion(tt.version, func() bool { return tt.sqliteExists })
			if got != tt.want {
				t.Errorf("authStoreGenerationForVersion(%q, sqliteExists=%v) = %v, want %v",
					tt.version, tt.sqliteExists, got, tt.want)
			}
		})
	}
}

func mustDoc(t *testing.T, s string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatalf("parse doc: %v", err)
	}
	return doc
}

func TestFindCodexProfile(t *testing.T) {
	tests := []struct {
		name       string
		doc        string
		wantFound  bool
		wantAccess string
	}{
		{
			name: "legacy openai-codex key",
			doc: `{"version":1,"profiles":{"openai-codex:default":
				{"type":"oauth","provider":"openai-codex","access":"tok-legacy","refresh":"r","expires":1780000000000}}}`,
			wantFound:  true,
			wantAccess: "tok-legacy",
		},
		{
			name: "migrated openai key (provider unification)",
			doc: `{"version":1,"profiles":{"openai:default":
				{"type":"oauth","provider":"openai","access":"tok-migrated","refresh":"r","expires":1780000000000}}}`,
			wantFound:  true,
			wantAccess: "tok-migrated",
		},
		{
			name: "openai api-key profile is not a codex oauth profile",
			doc: `{"version":1,"profiles":{"openai:default":
				{"type":"api_key","provider":"openai","key":"sk-test"}}}`,
			wantFound: false,
		},
		{
			name: "legacy key wins over migrated key when both present",
			doc: `{"version":1,"profiles":{
				"openai-codex:default":{"type":"oauth","provider":"openai-codex","access":"tok-legacy","refresh":"r","expires":1},
				"openai:default":{"type":"oauth","provider":"openai","access":"tok-migrated","refresh":"r","expires":1}}}`,
			wantFound:  true,
			wantAccess: "tok-legacy",
		},
		{
			name:      "no profiles",
			doc:       `{"version":1,"profiles":{}}`,
			wantFound: false,
		},
		{
			name:      "missing profiles map",
			doc:       `{"version":1}`,
			wantFound: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, found := findCodexProfile(mustDoc(t, tt.doc))
			if found != tt.wantFound {
				t.Fatalf("findCodexProfile() found = %v, want %v", found, tt.wantFound)
			}
			if !found {
				return
			}
			access, _ := profile["access"].(string)
			if access != tt.wantAccess {
				t.Errorf("access = %q, want %q", access, tt.wantAccess)
			}
		})
	}
}

func TestParseAuthStoreJSON(t *testing.T) {
	// store_json blob exactly as written by openclaw's sqlite migration
	// (observed: auth_profile_store row, store_key=primary).
	storeJSON := `{"version":1,"profiles":{"openai:default":{"type":"oauth","provider":"openai","access":"fake-access-token","refresh":"fake-refresh-token","expires":1780000000000}}}`

	doc, err := parseAuthStoreJSON([]byte(storeJSON))
	if err != nil {
		t.Fatalf("parseAuthStoreJSON: %v", err)
	}
	profile, found := findCodexProfile(doc)
	if !found {
		t.Fatal("expected codex profile in parsed store doc")
	}
	if access, _ := profile["access"].(string); access != "fake-access-token" {
		t.Errorf("access = %q, want fake-access-token", access)
	}
	if expires, _ := profile["expires"].(float64); int64(expires) != 1780000000000 {
		t.Errorf("expires = %v, want 1780000000000", expires)
	}

	if _, err := parseAuthStoreJSON([]byte("not json")); err == nil {
		t.Error("expected error for invalid store JSON")
	}
	if _, err := parseAuthStoreJSON(nil); err == nil {
		t.Error("expected error for empty store JSON")
	}
}
