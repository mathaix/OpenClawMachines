//go:build linux && e2e

package gatewaye2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/openclawver"
)

// TestCodexAuthStore_DoctorImport verifies the mechanism ptyd's codex-auth
// exchange relies on for OpenClaw ≥2026.6: a freshly written
// auth-profiles.json is imported into the agent SQLite store by
// `openclaw doctor --fix`, after which the credential is visible in the
// store (with the openai-codex → openai provider unification applied) and
// the JSON file is retired.
//
// Skipped on runtimes <2026.6 where the gateway reads auth-profiles.json
// directly and no import happens (today's production behavior).
func TestCodexAuthStore_DoctorImport(t *testing.T) {
	if env == nil || env.openclawBin == "" {
		t.Skip("openclaw binary not available")
	}

	version := openclawBinaryVersion(t, env.openclawBin)
	if atLeast, ok := openclawver.AtLeast(version, 2026, 6); !ok || !atLeast {
		t.Skipf("openclaw %s uses the legacy JSON auth store — no import to test", version)
	}

	home := t.TempDir()
	agentDir := filepath.Join(home, ".openclaw", "agents", "main", "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}

	// Same document shape ptyd's writeCodexAuthProfile produces.
	authJSON := `{
  "version": 1,
  "profiles": {
    "openai-codex:default": {
      "type": "oauth",
      "provider": "openai-codex",
      "access": "e2e-fake-access-token",
      "refresh": "e2e-fake-refresh-token",
      "expires": 1900000000000
    }
  }
}`
	jsonPath := filepath.Join(agentDir, "auth-profiles.json")
	if err := os.WriteFile(jsonPath, []byte(authJSON), 0o600); err != nil {
		t.Fatalf("write auth-profiles.json: %v", err)
	}

	cmd := exec.Command(env.openclawBin, "doctor", "--fix", "--non-interactive")
	cmd.Dir = home
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
		"NODE_ENV=production",
		"TERM=dumb",
	}
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("doctor --fix timed out after 120s")
	}
	if err != nil {
		t.Fatalf("doctor --fix: %v\n%s", err, truncateForLog(string(out), 2000))
	}

	sqlitePath := filepath.Join(agentDir, "openclaw-agent.sqlite")
	if _, statErr := os.Stat(sqlitePath); statErr != nil {
		t.Fatalf("expected sqlite auth store at %s after doctor --fix: %v\ndoctor output:\n%s",
			sqlitePath, statErr, truncateForLog(string(out), 2000))
	}

	// Read the store the same way ptyd does (node:sqlite, read-only).
	storeJSON := readAuthStoreJSON(t, sqlitePath)
	var doc struct {
		Profiles map[string]struct {
			Type     string `json:"type"`
			Provider string `json:"provider"`
			Access   string `json:"access"`
		} `json:"profiles"`
	}
	if jsonErr := json.Unmarshal([]byte(storeJSON), &doc); jsonErr != nil {
		t.Fatalf("parse store_json: %v\nraw: %s", jsonErr, storeJSON)
	}

	// Accept either key: 2026.6.x migrates openai-codex:default → openai:default.
	profile, found := doc.Profiles["openai:default"]
	if !found {
		profile, found = doc.Profiles["openai-codex:default"]
	}
	if !found {
		t.Fatalf("codex profile missing from sqlite store; profiles: %s", storeJSON)
	}
	if profile.Type != "oauth" {
		t.Errorf("profile type = %q, want oauth", profile.Type)
	}
	if profile.Access != "e2e-fake-access-token" {
		t.Errorf("access token = %q, want e2e-fake-access-token", profile.Access)
	}

	// The imported JSON is retired (renamed *.bak) — init must not re-link a
	// stale copy or the old token would be re-imported on every boot.
	if fi, statErr := os.Lstat(jsonPath); statErr == nil && fi.Mode().IsRegular() {
		t.Errorf("auth-profiles.json still present as a regular file after import — expected it renamed to *.bak")
	}

	// openclaw's own CLI resolves the imported profile.
	listCmd := exec.Command(env.openclawBin, "models", "auth", "list")
	listCmd.Dir = home
	listCmd.Env = cmd.Env
	listOut, listErr := listCmd.CombinedOutput()
	if listErr != nil {
		t.Fatalf("models auth list: %v\n%s", listErr, string(listOut))
	}
	if !strings.Contains(string(listOut), "oauth") {
		t.Errorf("models auth list does not show the imported oauth profile:\n%s", string(listOut))
	}
}

// openclawBinaryVersion extracts the version from `openclaw --version`.
func openclawBinaryVersion(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("openclaw --version: %v\n%s", err, string(out))
	}
	m := regexp.MustCompile(`\d{4}\.\d+\.\d+(?:-[\w.]+)?`).FindString(string(out))
	if m == "" {
		t.Fatalf("could not parse version from: %s", string(out))
	}
	return m
}

// readAuthStoreJSON reads the auth_profile_store row via node:sqlite — the
// same access path ptyd uses in production (VMs run Node 24 where
// node:sqlite needs no flag; older local Node may need --experimental-sqlite).
func readAuthStoreJSON(t *testing.T, sqlitePath string) string {
	t.Helper()
	script := `const {DatabaseSync}=require("node:sqlite");` +
		`const db=new DatabaseSync(process.argv[1],{readOnly:true});` +
		`const row=db.prepare("SELECT store_json FROM auth_profile_store WHERE store_key='primary'").get();` +
		`if(row&&row.store_json)process.stdout.write(String(row.store_json));`
	out, err := exec.Command("node", "-e", script, sqlitePath).Output()
	if err != nil {
		out, err = exec.Command("node", "--experimental-sqlite", "-e", script, sqlitePath).Output()
	}
	if err != nil {
		t.Fatalf("read sqlite store via node: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("auth_profile_store row missing or empty")
	}
	return string(out)
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
