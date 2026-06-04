package commands

import (
	"strings"
	"testing"
)

func TestChannelsSetupSlackSupported(t *testing.T) {
	_, ok := channelInstructions["slack"]
	if !ok {
		t.Fatal("expected slack in channelInstructions")
	}
	info := channelInstructions["slack"]
	if info.Provider != "slack" {
		t.Errorf("expected provider 'slack', got %q", info.Provider)
	}
}

func TestChannelsSetupInvalidChannel(t *testing.T) {
	teardown := setupTestConfig(t, "http://localhost:0")
	defer teardown()

	_, err := executeCommand("channels", "setup", "foobar", "--machine", "my-machine")
	if err == nil {
		t.Fatal("expected error for invalid channel, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported channel") {
		t.Errorf("expected 'unsupported channel' in error, got: %v", err)
	}
}

func TestChannelsSetupMissingArg(t *testing.T) {
	teardown := setupTestConfig(t, "http://localhost:0")
	defer teardown()

	_, err := executeCommand("channels", "setup")
	if err == nil {
		t.Fatal("expected error for missing channel argument, got nil")
	}
}
