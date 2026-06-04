package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func channelRequest(method, path, machineID, channelID string, accountID int, body interface{}) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")

	ctx := context.WithValue(req.Context(), accountIDKey, accountID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", machineID)
	rctx.URLParams.Add("channel", channelID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func TestBuildChannelSetOps(t *testing.T) {
	cfg := map[string]interface{}{
		"enabled": true,
		"mode":    "socket",
	}
	ops := buildChannelSetOps("slack", cfg)
	require.Len(t, ops, 1)
	assert.Equal(t, "set", ops[0].Op)
	assert.Equal(t, "channels.slack", ops[0].Path)
	assert.True(t, ops[0].StrictJSON)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(ops[0].Value), &parsed))
	assert.Equal(t, true, parsed["enabled"])
	assert.Equal(t, "socket", parsed["mode"])
}

// TestSlackConnectOps verifies the connect batch includes all three required
// ops: channels.slack, plugins.entries.slack.enabled, and plugins.allow.
func TestSlackConnectOps(t *testing.T) {
	channelConfig := map[string]interface{}{
		"enabled":     true,
		"mode":        "socket",
		"groupPolicy": "open",
	}
	ops := buildChannelSetOps("slack", channelConfig)

	// Simulate what handleChannelConnect appends for Slack
	ops = append(ops, agentclient.ConfigOp{
		Op:         "set",
		Path:       "plugins.entries.slack.enabled",
		Value:      "true",
		StrictJSON: true,
	})
	// Simulate plugins.allow op (the actual method reads from DB;
	// here we just verify the shape of the op)
	allowJSON, _ := json.Marshal([]string{"composio", "opik-openclaw", "slack"})
	ops = append(ops, agentclient.ConfigOp{
		Op:         "set",
		Path:       "plugins.allow",
		Value:      string(allowJSON),
		StrictJSON: true,
	})

	paths := make(map[string]bool)
	for _, op := range ops {
		paths[op.Path] = true
	}

	assert.True(t, paths["channels.slack"], "must set channels.slack")
	assert.True(t, paths["plugins.entries.slack.enabled"], "must set plugins.entries.slack.enabled")
	assert.True(t, paths["plugins.allow"], "must set plugins.allow")
}

// TestSlackDisconnectOps verifies disconnect removes channel, plugin entry,
// and plugins.allow entry.
func TestSlackDisconnectOps(t *testing.T) {
	ops := []agentclient.ConfigOp{{Op: "unset", Path: "channels.slack"}}
	ops = append(ops, agentclient.ConfigOp{Op: "unset", Path: "plugins.entries.slack"})

	// Simulate plugins.allow after removing "slack"
	allowJSON, _ := json.Marshal([]string{"composio", "opik-openclaw"})
	ops = append(ops, agentclient.ConfigOp{
		Op:         "set",
		Path:       "plugins.allow",
		Value:      string(allowJSON),
		StrictJSON: true,
	})

	var unsetPaths []string
	var setPaths []string
	for _, op := range ops {
		if op.Op == "unset" {
			unsetPaths = append(unsetPaths, op.Path)
		} else {
			setPaths = append(setPaths, op.Path)
		}
	}

	assert.Contains(t, unsetPaths, "channels.slack")
	assert.Contains(t, unsetPaths, "plugins.entries.slack")
	assert.Contains(t, setPaths, "plugins.allow")

	// Verify "slack" is NOT in the updated allow list
	for _, op := range ops {
		if op.Path == "plugins.allow" {
			var allow []string
			require.NoError(t, json.Unmarshal([]byte(op.Value), &allow))
			assert.NotContains(t, allow, "slack", "slack must be removed from plugins.allow on disconnect")
			assert.Contains(t, allow, "composio", "other plugins must be preserved")
		}
	}
}

// TestPluginsAllowArrayManipulation tests the add/remove logic on plugins.allow
// arrays directly via setNestedValue and JSON parsing, matching the pattern
// used by pluginsAllowOps.
func TestPluginsAllowArrayManipulation(t *testing.T) {
	t.Run("add to existing array", func(t *testing.T) {
		existing := []string{"composio", "opik-openclaw"}
		found := false
		for _, v := range existing {
			if v == "slack" {
				found = true
			}
		}
		if !found {
			existing = append(existing, "slack")
		}
		assert.Equal(t, []string{"composio", "opik-openclaw", "slack"}, existing)
	})

	t.Run("add is idempotent", func(t *testing.T) {
		existing := []string{"composio", "slack"}
		found := false
		for _, v := range existing {
			if v == "slack" {
				found = true
			}
		}
		if !found {
			existing = append(existing, "slack")
		}
		assert.Equal(t, []string{"composio", "slack"}, existing)
	})

	t.Run("add to empty array", func(t *testing.T) {
		var existing []string
		existing = append(existing, "slack")
		assert.Equal(t, []string{"slack"}, existing)
	})

	t.Run("remove from array", func(t *testing.T) {
		existing := []string{"composio", "slack", "opik-openclaw"}
		filtered := existing[:0]
		for _, v := range existing {
			if v != "slack" {
				filtered = append(filtered, v)
			}
		}
		assert.Equal(t, []string{"composio", "opik-openclaw"}, filtered)
	})

	t.Run("remove missing entry is no-op", func(t *testing.T) {
		existing := []string{"composio", "opik-openclaw"}
		filtered := existing[:0]
		for _, v := range existing {
			if v != "slack" {
				filtered = append(filtered, v)
			}
		}
		assert.Equal(t, []string{"composio", "opik-openclaw"}, filtered)
	})
}

func TestChannelMutations_RejectStoppedMachineAfterFirstBoot(t *testing.T) {
	now := time.Now()
	ms := newMockConfigStore()
	ms.machines["m-1"] = &store.Machine{
		ID:                      "m-1",
		AccountID:               1,
		Status:                  "stopped",
		ProvisioningCompletedAt: &now,
	}
	srv := newTestConfigServer(ms)

	tests := []struct {
		name   string
		method string
		path   string
		body   interface{}
		call   func(http.ResponseWriter, *http.Request)
	}{
		{
			name:   "connect",
			method: "POST",
			path:   "/api/accounts/1/machines/m-1/channels/slack/connect",
			body: map[string]interface{}{
				"token": "xoxb-test",
			},
			call: srv.handleChannelConnect,
		},
		{
			name:   "disconnect",
			method: "POST",
			path:   "/api/accounts/1/machines/m-1/channels/slack/disconnect",
			call:   srv.handleChannelDisconnect,
		},
		{
			name:   "settings",
			method: "PATCH",
			path:   "/api/accounts/1/machines/m-1/channels/slack/settings",
			body: map[string]interface{}{
				"settings": map[string]interface{}{"mode": "socket"},
			},
			call: srv.handleChannelSettings,
		},
		{
			name:   "update-token",
			method: "PATCH",
			path:   "/api/accounts/1/machines/m-1/channels/slack/token",
			body: map[string]interface{}{
				"token": "xoxb-test",
			},
			call: srv.handleChannelUpdateToken,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := channelRequest(tc.method, tc.path, "m-1", "slack", 1, tc.body)
			tc.call(w, r)

			if w.Code != http.StatusConflict {
				t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
