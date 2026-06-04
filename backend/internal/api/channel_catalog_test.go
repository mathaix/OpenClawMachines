package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleListChannels_HermesUsesEnvTargets(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/channels?kind=hermes", nil)
	rec := httptest.NewRecorder()

	srv.handleListChannels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp []channelCatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp) == 0 {
		t.Fatal("expected channels")
	}

	var discord *channelCatalogResponse
	for i := range resp {
		if resp[i].ID == "discord" {
			discord = &resp[i]
			break
		}
	}
	if discord == nil {
		t.Fatal("missing discord channel")
	}
	if len(discord.TokenFields) != 1 {
		t.Fatalf("discord token fields = %d, want 1", len(discord.TokenFields))
	}
	if discord.TokenFields[0].Name != "DISCORD_BOT_TOKEN" || discord.TokenFields[0].Target != ".env" {
		t.Fatalf("discord token field = %+v, want DISCORD_BOT_TOKEN in .env", discord.TokenFields[0])
	}
}

func TestHandleListChannels_RejectsInvalidKind(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/channels?kind=browser", nil)
	rec := httptest.NewRecorder()

	srv.handleListChannels(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
