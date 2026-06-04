package configassembly

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestChannelKeepListMatchesBuildScript guards against drift between the
// channels OCM's product surface exposes (ChannelTokenFields + session-based
// whatsapp) and the OCM_KEEP_CHANNELS default in scripts/build-openclaw-runtime.sh.
//
// If someone adds a new channel to ChannelTokenFields, this test fails until
// they also update the build script's keep list. The build script's allowlist
// is the source of truth at artifact-build time; this test enforces that it
// stays in sync with the runtime channel surface.
//
// See docs/plans/2026-04-28-cold-boot-extension-scrub-design.md
func TestChannelKeepListMatchesBuildScript(t *testing.T) {
	// Channels OCM's product plumbing actually supports today.
	// Token-based: from ChannelTokenFields.
	// Session-based (no token field): whatsapp.
	expected := map[string]bool{"whatsapp": true}
	for chID := range ChannelTokenFields {
		expected[chID] = true
	}

	// Walk up to find repo root, then read the build script.
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("could not locate repo root: %v", err)
	}
	scriptPath := filepath.Join(repoRoot, "scripts", "build-openclaw-runtime.sh")
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("could not read %s: %v", scriptPath, err)
	}

	// Match: OCM_KEEP_CHANNELS_DEFAULT="<space-separated channel ids>"
	re := regexp.MustCompile(`(?m)^OCM_KEEP_CHANNELS_DEFAULT="([^"]+)"`)
	m := re.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("could not find OCM_KEEP_CHANNELS_DEFAULT in %s — has the variable been renamed?", scriptPath)
	}
	scriptList := strings.Fields(m[1])
	scriptSet := make(map[string]bool, len(scriptList))
	for _, ch := range scriptList {
		scriptSet[ch] = true
	}

	// Anything in expected but missing from the script: build would scrub
	// a real channel, breaking it for tenants.
	var missingFromScript []string
	for ch := range expected {
		if !scriptSet[ch] {
			missingFromScript = append(missingFromScript, ch)
		}
	}
	// Anything in the script but not in expected: stale entry, harmless but
	// flag it so the lists don't drift apart silently.
	var extraInScript []string
	for ch := range scriptSet {
		if !expected[ch] {
			extraInScript = append(extraInScript, ch)
		}
	}
	sort.Strings(missingFromScript)
	sort.Strings(extraInScript)

	if len(missingFromScript) > 0 || len(extraInScript) > 0 {
		var b strings.Builder
		b.WriteString("Channel keep-list out of sync between code and build script.\n")
		if len(missingFromScript) > 0 {
			b.WriteString("  Missing from scripts/build-openclaw-runtime.sh OCM_KEEP_CHANNELS_DEFAULT: ")
			b.WriteString(strings.Join(missingFromScript, ", "))
			b.WriteString("\n  → Add these to the keep list, or the build will scrub a channel OCM exposes.\n")
		}
		if len(extraInScript) > 0 {
			b.WriteString("  In script but not in code (ChannelTokenFields ∪ {whatsapp}): ")
			b.WriteString(strings.Join(extraInScript, ", "))
			b.WriteString("\n  → Remove from script if no longer supported, or wire it into ChannelTokenFields if newly supported.\n")
		}
		t.Fatal(b.String())
	}
}

// findRepoRoot walks up from the current test file location until it finds
// a directory containing go.mod with the OCM module path.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		gomod := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(gomod); err == nil {
			if strings.Contains(string(data), "github.com/mathaix/openclawmachines") {
				// One level up from backend/go.mod is the repo root.
				return filepath.Dir(dir), nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
