package commands

import (
	"strings"
	"testing"
)

func TestProvidersSetupInvalidProvider(t *testing.T) {
	// "providers setup" with an unknown provider should fail validation
	// before making any HTTP call.
	teardown := setupTestConfig(t, "http://localhost:0")
	defer teardown()

	_, err := executeCommand("providers", "setup", "invalid-provider")
	if err == nil {
		t.Fatal("expected error for invalid provider, got nil")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("expected 'unknown provider' in error, got: %v", err)
	}
}

func TestProvidersSetupMissingArg(t *testing.T) {
	// "providers setup" without any argument should fail (ExactArgs(1)).
	teardown := setupTestConfig(t, "http://localhost:0")
	defer teardown()

	_, err := executeCommand("providers", "setup")
	if err == nil {
		t.Fatal("expected error for missing argument, got nil")
	}
}

func TestProviderInstructionsMap(t *testing.T) {
	// Verify all valid provider names have entries in providerInstructions.
	for _, name := range validProviderNames {
		info, ok := providerInstructions[name]
		if !ok {
			t.Errorf("provider %q missing from providerInstructions map", name)
			continue
		}
		if info.DisplayName == "" {
			t.Errorf("provider %q has empty DisplayName", name)
		}
		if info.KeyURL == "" {
			t.Errorf("provider %q has empty KeyURL", name)
		}
		if len(info.Steps) == 0 {
			t.Errorf("provider %q has no Steps", name)
		}
	}
}
