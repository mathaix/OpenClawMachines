package api

import (
	"net/http"
	"strings"

	"github.com/mathaix/openclawmachines/backend/internal/configassembly"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type channelCatalogResponse struct {
	ID                 string                      `json:"id"`
	Label              string                      `json:"label"`
	ShortDesc          string                      `json:"short_desc"`
	CredentialProvider *string                     `json:"credential_provider,omitempty"`
	HasSettings        bool                        `json:"has_settings"`
	Status             string                      `json:"status"`
	TokenFields        []channelTokenFieldResponse `json:"token_fields,omitempty"`
}

type channelTokenFieldResponse struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Target   string `json:"target"`
}

type channelCatalogDef struct {
	ID                 string
	Label              string
	ShortDesc          string
	CredentialProvider string
	HasSettings        bool
	Status             string
	TargetsByKind      map[string][]channelTokenFieldResponse
}

var channelCatalog = []channelCatalogDef{
	{
		ID:                 "telegram",
		Label:              "Telegram",
		ShortDesc:          "Bot token - Telegram app",
		CredentialProvider: "telegram",
		HasSettings:        true,
		Status:             "available",
		TargetsByKind: map[string][]channelTokenFieldResponse{
			store.MachineKindOpenClaw: {{Name: "botToken", Provider: "telegram", Target: "channels.telegram.botToken"}},
			store.MachineKindHermes:   {{Name: "TELEGRAM_BOT_TOKEN", Provider: "telegram", Target: ".env"}},
		},
	},
	{
		ID:                 "discord",
		Label:              "Discord",
		ShortDesc:          "Bot token - Channels & DMs",
		CredentialProvider: "discord",
		HasSettings:        true,
		Status:             "available",
		TargetsByKind: map[string][]channelTokenFieldResponse{
			store.MachineKindOpenClaw: {{Name: "token", Provider: "discord", Target: "channels.discord.token"}},
			store.MachineKindHermes:   {{Name: "DISCORD_BOT_TOKEN", Provider: "discord", Target: ".env"}},
		},
	},
	{
		ID:                 "whatsapp",
		Label:              "WhatsApp",
		ShortDesc:          "WhatsApp Business",
		CredentialProvider: "",
		HasSettings:        false,
		Status:             "coming_soon",
	},
	{
		ID:                 "slack",
		Label:              "Slack",
		ShortDesc:          "Bot token - Socket Mode",
		CredentialProvider: "slack",
		HasSettings:        true,
		Status:             "available",
		TargetsByKind: map[string][]channelTokenFieldResponse{
			store.MachineKindOpenClaw: {
				{Name: "botToken", Provider: "slack", Target: "channels.slack.botToken"},
				{Name: "appToken", Provider: "slack-app", Target: "channels.slack.appToken"},
			},
			store.MachineKindHermes: {
				{Name: "SLACK_BOT_TOKEN", Provider: "slack", Target: ".env"},
				{Name: "SLACK_APP_TOKEN", Provider: "slack-app", Target: ".env"},
			},
		},
	},
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	kind := store.NormalizeMachineKind(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind"))))
	if !store.ValidMachineKind(kind) {
		writeError(w, http.StatusBadRequest, "kind must be one of: openclaw, hermes")
		return
	}

	resp := make([]channelCatalogResponse, 0, len(channelCatalog))
	for _, ch := range channelCatalog {
		entry := channelCatalogResponse{
			ID:          ch.ID,
			Label:       ch.Label,
			ShortDesc:   ch.ShortDesc,
			HasSettings: ch.HasSettings && kind == store.MachineKindOpenClaw,
			Status:      ch.Status,
		}
		if ch.CredentialProvider != "" {
			provider := ch.CredentialProvider
			entry.CredentialProvider = &provider
		}
		if fields := ch.TargetsByKind[kind]; len(fields) > 0 {
			entry.TokenFields = fields
		} else if fields, ok := configassembly.ChannelTokenFields[ch.ID]; ok {
			for _, field := range fields {
				entry.TokenFields = append(entry.TokenFields, channelTokenFieldResponse{
					Name:     field.FieldName,
					Provider: field.Provider,
					Target:   "channels." + ch.ID + "." + field.FieldName,
				})
			}
		}
		resp = append(resp, entry)
	}

	writeJSON(w, http.StatusOK, resp)
}
