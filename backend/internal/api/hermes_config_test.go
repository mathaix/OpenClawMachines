package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

func TestChannelSettingsFromCapabilityOverrides(t *testing.T) {
	raw := json.RawMessage(`{"channels":{"telegram":{"allowedUsers":"12345, 67890"},"discord":{"enabled":true}}}`)

	settings := channelSettingsFromCapabilityOverrides(raw, "telegram")
	if got := settings["allowedUsers"]; got != "12345, 67890" {
		t.Fatalf("allowedUsers = %v, want %q", got, "12345, 67890")
	}

	if got := channelSettingsFromCapabilityOverrides(raw, "slack"); got != nil {
		t.Fatalf("slack settings = %#v, want nil", got)
	}
}

func TestValidateHermesChannelSettingsRequiresTelegramAllowedUsers(t *testing.T) {
	w := httptest.NewRecorder()

	if validateHermesChannelSettings(w, store.MachineKindHermes, "telegram", map[string]interface{}{}) {
		t.Fatal("expected missing allowed users to fail")
	}
	if w.Code != 400 || !strings.Contains(w.Body.String(), "allowed Telegram user IDs") {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

func TestValidateHermesChannelSettingsRejectsNonNumericTelegramAllowedUsers(t *testing.T) {
	w := httptest.NewRecorder()

	if validateHermesChannelSettings(w, store.MachineKindHermes, "telegram", map[string]interface{}{"allowedUsers": "alice"}) {
		t.Fatal("expected non-numeric allowed users to fail")
	}
	if w.Code != 400 || !strings.Contains(w.Body.String(), "numeric IDs") {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

func TestValidateHermesChannelSettingsAcceptsTelegramAllowedUsers(t *testing.T) {
	w := httptest.NewRecorder()

	if !validateHermesChannelSettings(w, store.MachineKindHermes, "telegram", map[string]interface{}{"allowedUsers": "12345, 67890"}) {
		t.Fatalf("expected valid allowed users to pass: %d %s", w.Code, w.Body.String())
	}
}
