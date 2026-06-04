# Slack Channel Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable Slack as a fully supported messaging channel, with dual-token support (bot + app-level), end-to-end from UI through config assembly to runtime secret resolution.

**Architecture:** Extend the channel token framework from single-token (`ChannelTokenFieldName map[string]string`) to multi-token (`ChannelTokenFields map[string][]ChannelTokenMapping`). Store Slack's two tokens as separate credentials under providers `"slack"` and `"slack-app"`. A reverse lookup (`ProviderToChannel`) ensures secret IDs match between injection and resolution.

**Tech Stack:** Go (backend), TypeScript/React (frontend), PostgreSQL (migration), Cobra CLI

**Spec:** `docs/superpowers/specs/2026-03-30-slack-channel-integration-design.md`

---

## File Structure

| File | Responsibility |
|------|---------------|
| `backend/internal/configassembly/assembler.go` | Token field mapping types, `ChannelTokenFields`, `ProviderToChannel()`, exec ref injection |
| `backend/internal/configassembly/assembler_test.go` | Tests for Slack token assembly, backward compat for Telegram/Discord |
| `backend/migrations/055_slack_channel.sql` | Seed Slack registry entry |
| `backend/internal/api/credentials.go` | `validProviders`, Slack validation functions |
| `backend/internal/api/credentials_test.go` | Tests for Slack validation |
| `backend/internal/api/channel_config.go` | Connect/disconnect/update/settings with dual-token support |
| `backend/internal/api/agent_auth.go` | Secret ID mapping via `ProviderToChannel()` |
| `backend/internal/api/machine_config.go` | Config preview credential markers |
| `backend/internal/machines/runtime.go` | Cold-start seed config multi-token injection |
| `frontend/src/lib/types.ts` | `CredentialProvider` union, `CREDENTIAL_PROVIDERS` array |
| `frontend/src/lib/api.ts` | `connectChannel` signature update |
| `frontend/src/pages/machine-tabs/ChannelsTab.tsx` | Slack instructions, dual-token setup dialog |
| `cli/internal/commands/validate.go` | Slack validation functions |
| `cli/internal/commands/validate_test.go` | Tests for Slack CLI validation |
| `cli/internal/commands/channels_setup.go` | Slack channel instructions |
| `cli/internal/commands/channels_setup_test.go` | Update existing test |
| `cli/internal/commands/providers.go` | Provider lists update |

---

### Task 1: Refactor ChannelTokenFieldName to ChannelTokenFields

**Files:**
- Modify: `backend/internal/configassembly/assembler.go:116-122`
- Modify: `backend/internal/configassembly/assembler_test.go`

- [ ] **Step 1: Write tests for new mapping and reverse lookup**

Add to `backend/internal/configassembly/assembler_test.go`:

```go
func TestChannelTokenFields_TelegramMapping(t *testing.T) {
	fields, ok := ChannelTokenFields["telegram"]
	if !ok {
		t.Fatal("missing telegram in ChannelTokenFields")
	}
	if len(fields) != 1 {
		t.Fatalf("expected 1 field for telegram, got %d", len(fields))
	}
	if fields[0].FieldName != "botToken" || fields[0].Provider != "telegram" {
		t.Errorf("telegram field = %+v, want {botToken, telegram}", fields[0])
	}
}

func TestChannelTokenFields_DiscordMapping(t *testing.T) {
	fields, ok := ChannelTokenFields["discord"]
	if !ok {
		t.Fatal("missing discord in ChannelTokenFields")
	}
	if len(fields) != 1 {
		t.Fatalf("expected 1 field for discord, got %d", len(fields))
	}
	if fields[0].FieldName != "token" || fields[0].Provider != "discord" {
		t.Errorf("discord field = %+v, want {token, discord}", fields[0])
	}
}

func TestChannelTokenFields_SlackMapping(t *testing.T) {
	fields, ok := ChannelTokenFields["slack"]
	if !ok {
		t.Fatal("missing slack in ChannelTokenFields")
	}
	if len(fields) != 2 {
		t.Fatalf("expected 2 fields for slack, got %d", len(fields))
	}
	if fields[0].FieldName != "botToken" || fields[0].Provider != "slack" {
		t.Errorf("slack[0] = %+v, want {botToken, slack}", fields[0])
	}
	if fields[1].FieldName != "appToken" || fields[1].Provider != "slack-app" {
		t.Errorf("slack[1] = %+v, want {appToken, slack-app}", fields[1])
	}
}

func TestProviderToChannel_Telegram(t *testing.T) {
	chID, fieldName, ok := ProviderToChannel("telegram")
	if !ok || chID != "telegram" || fieldName != "botToken" {
		t.Errorf("ProviderToChannel(telegram) = (%q, %q, %v), want (telegram, botToken, true)", chID, fieldName, ok)
	}
}

func TestProviderToChannel_SlackApp(t *testing.T) {
	chID, fieldName, ok := ProviderToChannel("slack-app")
	if !ok || chID != "slack" || fieldName != "appToken" {
		t.Errorf("ProviderToChannel(slack-app) = (%q, %q, %v), want (slack, appToken, true)", chID, fieldName, ok)
	}
}

func TestProviderToChannel_Unknown(t *testing.T) {
	_, _, ok := ProviderToChannel("unknown-provider")
	if ok {
		t.Error("expected ok=false for unknown provider")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/configassembly/ -run "TestChannelTokenFields|TestProviderToChannel" -v`
Expected: FAIL — `ChannelTokenFields` and `ProviderToChannel` undefined.

- [ ] **Step 3: Implement ChannelTokenFields and ProviderToChannel**

In `backend/internal/configassembly/assembler.go`, replace lines 116-122:

```go
// ChannelTokenMapping maps a config field name to its credential provider.
type ChannelTokenMapping struct {
	FieldName string // config field name in openclaw.json (e.g. "botToken")
	Provider  string // credential provider name in DB (e.g. "slack")
}

// ChannelTokenFields maps channel IDs to their token field configurations.
// Channels not in this map (e.g. WhatsApp) use session-based auth.
var ChannelTokenFields = map[string][]ChannelTokenMapping{
	"telegram": {{FieldName: "botToken", Provider: "telegram"}},
	"discord":  {{FieldName: "token", Provider: "discord"}},
	"slack": {
		{FieldName: "botToken", Provider: "slack"},
		{FieldName: "appToken", Provider: "slack-app"},
	},
}

// ChannelTokenFieldName is a backward-compatible accessor returning the primary
// token field name for a channel provider. Used by callsites not yet migrated.
// Deprecated: use ChannelTokenFields and ProviderToChannel instead.
var ChannelTokenFieldName = map[string]string{
	"telegram": "botToken",
	"discord":  "token",
	"slack":    "botToken",
}

// ProviderToChannel maps a credential provider name back to its channel ID and field name.
// Used by agent_auth.go and machine_config.go to build correct secret IDs.
func ProviderToChannel(provider string) (channelID, fieldName string, ok bool) {
	for chID, mappings := range ChannelTokenFields {
		for _, m := range mappings {
			if m.Provider == provider {
				return chID, m.FieldName, true
			}
		}
	}
	return "", "", false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/configassembly/ -run "TestChannelTokenFields|TestProviderToChannel" -v`
Expected: PASS

- [ ] **Step 5: Run full assembler tests to verify no regressions**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/configassembly/ -v`
Expected: All existing tests PASS (Telegram/Discord still work via `ChannelTokenFieldName` compat map).

- [ ] **Step 6: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/configassembly/assembler_test.go
git commit -m "feat(slack): add ChannelTokenFields struct and ProviderToChannel reverse lookup"
```

---

### Task 2: Migrate assembler exec ref injection to ChannelTokenFields

**Files:**
- Modify: `backend/internal/configassembly/assembler.go:664-683`
- Modify: `backend/internal/configassembly/assembler_test.go`

- [ ] **Step 1: Write test for Slack channel token assembly**

Add to `backend/internal/configassembly/assembler_test.go`:

```go
func TestChannelToken_SlackWithBothCredentials(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		ModelCatalog: testModelCatalog(),
		MachineID:    "m-1",
		Capabilities: []CapabilityWithTemplate{
			{
				EntryID:   "slack",
				EntryType: "channel",
				ConfigTemplate: map[string]interface{}{
					"channels": map[string]interface{}{
						"slack": map[string]interface{}{
							"enabled": true,
						},
					},
				},
			},
		},
		ChannelCredentialValues: map[string]string{
			"slack":     "xoxb-fake-bot-token",
			"slack-app": "xapp-fake-app-token",
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	m := mustUnmarshalMap(t, data)

	sl, ok := getNestedMap(m, "channels", "slack")
	if !ok {
		t.Fatal("missing channels.slack")
	}
	if sl["enabled"] != true {
		t.Errorf("channels.slack.enabled = %v, want true", sl["enabled"])
	}

	// Verify botToken exec ref
	botToken, ok := sl["botToken"].(map[string]interface{})
	if !ok {
		t.Fatalf("channels.slack.botToken is not an exec ref map: %v", sl["botToken"])
	}
	if botToken["source"] != "exec" || botToken["provider"] != "ocm" || botToken["id"] != "channel-slack-botToken" {
		t.Errorf("channels.slack.botToken exec ref = %v, want {source:exec provider:ocm id:channel-slack-botToken}", botToken)
	}

	// Verify appToken exec ref
	appToken, ok := sl["appToken"].(map[string]interface{})
	if !ok {
		t.Fatalf("channels.slack.appToken is not an exec ref map: %v", sl["appToken"])
	}
	if appToken["source"] != "exec" || appToken["provider"] != "ocm" || appToken["id"] != "channel-slack-appToken" {
		t.Errorf("channels.slack.appToken exec ref = %v, want {source:exec provider:ocm id:channel-slack-appToken}", appToken)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/configassembly/ -run "TestChannelToken_SlackWithBothCredentials" -v`
Expected: FAIL — `channels.slack.appToken` missing (only botToken injected via old code).

- [ ] **Step 3: Update exec ref injection loop in assembler.go**

Replace the injection loop at lines 664-683 in `backend/internal/configassembly/assembler.go`:

```go
	// 5e. Inject channel credential exec secret refs into the config.
	if len(params.ChannelCredentialValues) > 0 {
		channels, _ := result["channels"].(map[string]interface{})
		for provider := range params.ChannelCredentialValues {
			channelID, fieldName, ok := ProviderToChannel(provider)
			if !ok {
				continue
			}
			if channels == nil {
				continue
			}
			chConf, _ := channels[channelID].(map[string]interface{})
			if chConf == nil {
				continue
			}
			chConf[fieldName] = map[string]interface{}{
				"source":   "exec",
				"provider": "ocm",
				"id":       fmt.Sprintf("channel-%s-%s", channelID, fieldName),
			}
		}
	}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/configassembly/ -run "TestChannelToken" -v`
Expected: ALL PASS — Telegram, Discord, and Slack tests.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/configassembly/assembler_test.go
git commit -m "feat(slack): migrate assembler exec ref injection to ProviderToChannel"
```

---

### Task 3: Migrate agent_auth.go secret ID mapping

**Files:**
- Modify: `backend/internal/api/agent_auth.go:419-431`

- [ ] **Step 1: Update handleAgentAuthGetSecrets to use ProviderToChannel**

Replace the secret-building loop in `backend/internal/api/agent_auth.go` (lines 419-431):

```go
	secrets := make(map[string]string)
	for _, cred := range creds {
		channelID, fieldName, ok := configassembly.ProviderToChannel(cred.Provider)
		if !ok {
			continue // not a channel credential
		}
		val, err := crypto.Decrypt(cred.EncryptedValue, s.secretKey)
		if err != nil {
			slog.Warn("agent_auth.get_secrets.decrypt_failed", "machine_id", machineID, "provider", cred.Provider, "error", err)
			continue
		}
		secrets[fmt.Sprintf("channel-%s-%s", channelID, fieldName)] = val
	}
```

- [ ] **Step 2: Run backend tests**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/api/ -v -count=1`
Expected: PASS — no behavioral change for Telegram/Discord since `ProviderToChannel("telegram")` returns `("telegram", "botToken", true)`.

- [ ] **Step 3: Commit**

```bash
git add backend/internal/api/agent_auth.go
git commit -m "feat(slack): migrate agent_auth secret IDs to ProviderToChannel"
```

---

### Task 4: Migrate machine_config.go credential markers

**Files:**
- Modify: `backend/internal/api/machine_config.go:911-922`

- [ ] **Step 1: Update injectChannelCredentialMarkers to use ProviderToChannel**

Replace the marker injection loop in `backend/internal/api/machine_config.go` (lines 911-922):

```go
	for _, provider := range channelProviders {
		channelID, fieldName, ok := configassembly.ProviderToChannel(provider)
		if !ok {
			continue // provider doesn't use a token field (e.g. WhatsApp)
		}
		chConf, _ := channels[channelID].(map[string]interface{})
		if chConf == nil {
			chConf = make(map[string]interface{})
		}
		chConf[fieldName] = "••••••••"
		channels[channelID] = chConf
	}
```

- [ ] **Step 2: Run backend tests**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/api/ -v -count=1`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add backend/internal/api/machine_config.go
git commit -m "feat(slack): migrate config preview markers to ProviderToChannel"
```

---

### Task 5: Migrate runtime.go cold-start seed config

**Files:**
- Modify: `backend/internal/machines/runtime.go:448-455`

- [ ] **Step 1: Update seed config exec ref injection**

Replace the single-field injection at `backend/internal/machines/runtime.go` (lines 448-455):

```go
				// Inject exec secret refs — resolved at runtime by ocm-secrets via metadata service
				if fields, ok := configassembly.ChannelTokenFields[cap.EntryID]; ok {
					for _, field := range fields {
						chConf[field.FieldName] = map[string]interface{}{
							"source":   "exec",
							"provider": "ocm",
							"id":       fmt.Sprintf("channel-%s-%s", cap.EntryID, field.FieldName),
						}
					}
				}
```

- [ ] **Step 2: Run backend tests**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/machines/ -v -count=1`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add backend/internal/machines/runtime.go
git commit -m "feat(slack): migrate cold-start seed config to ChannelTokenFields"
```

---

### Task 6: Add Slack registry migration

**Files:**
- Create: `backend/migrations/055_slack_channel.sql`

- [ ] **Step 1: Create the migration file**

Create `backend/migrations/055_slack_channel.sql`:

```sql
-- Add Slack channel to registry.
-- Slack uses Socket Mode (two tokens: bot xoxb- + app-level xapp-).
-- Config template follows the Telegram pattern: enabled, dmPolicy, groups.
INSERT INTO registry_entries (id, type, name, description, config_template, required_credentials, status, sort_order)
VALUES
    ('slack', 'channel', 'Slack', 'Slack messaging integration',
     '{"channels":{"slack":{"enabled":true,"dmPolicy":"pairing","groups":{"*":{"requireMention":true}}}}}'::jsonb,
     ARRAY['slack'], 'active', 3)
ON CONFLICT (id) DO NOTHING;
```

- [ ] **Step 2: Commit**

```bash
git add backend/migrations/055_slack_channel.sql
git commit -m "feat(slack): add Slack channel registry migration"
```

---

### Task 7: Add Slack token validation (backend)

**Files:**
- Modify: `backend/internal/api/credentials.go:19-28, 345-366`
- Modify: `backend/internal/api/credentials_test.go`

- [ ] **Step 1: Write tests for Slack validation**

Add to `backend/internal/api/credentials_test.go`:

```go
var slackBaseURL = "https://slack.com"

func TestValidateSlackBotToken_Valid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth.test" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer xoxb-valid-token" {
			t.Errorf("unexpected Authorization header: %s", auth)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"team":"Test Team"}`))
	}))
	defer ts.Close()

	old := slackBotBaseURL
	slackBotBaseURL = ts.URL
	defer func() { slackBotBaseURL = old }()

	if err := validateSlackBotToken(context.Background(), "xoxb-valid-token"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateSlackBotToken_Invalid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
	}))
	defer ts.Close()

	old := slackBotBaseURL
	slackBotBaseURL = ts.URL
	defer func() { slackBotBaseURL = old }()

	err := validateSlackBotToken(context.Background(), "xoxb-bad-token")
	if err == nil {
		t.Fatal("expected error for invalid token, got nil")
	}
	if err.Error() != "Slack API rejected token: invalid_auth" {
		t.Fatalf("expected Slack API error, got %q", err.Error())
	}
}

func TestValidateSlackBotToken_BadPrefix(t *testing.T) {
	err := validateSlackBotToken(context.Background(), "not-a-slack-token")
	if err == nil {
		t.Fatal("expected error for bad prefix")
	}
}

func TestValidateSlackAppToken_Valid(t *testing.T) {
	if err := validateSlackAppToken("xapp-1-valid-token"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestValidateSlackAppToken_BadPrefix(t *testing.T) {
	if err := validateSlackAppToken("not-xapp-token"); err == nil {
		t.Fatal("expected error for bad prefix")
	}
}

func TestValidProviders_IncludesSlack(t *testing.T) {
	for _, provider := range []string{"slack", "slack-app"} {
		if !validProviders[provider] {
			t.Errorf("expected %q in validProviders", provider)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/api/ -run "TestValidateSlack|TestValidProviders_IncludesSlack" -v`
Expected: FAIL — functions not defined.

- [ ] **Step 3: Implement Slack validation**

Add to `backend/internal/api/credentials.go`:

In the `validProviders` map (line 19-28), add:
```go
"slack":     true,
"slack-app": true,
```

Add the base URL var (near line 39-41):
```go
slackBotBaseURL = "https://slack.com"
```

In the `validateProviderKey` switch (line 345-366), add before `default`:
```go
case "slack":
	return validateSlackBotToken(ctx, key)
case "slack-app":
	return validateSlackAppToken(key)
```

Add the validation functions:
```go
func validateSlackBotToken(ctx context.Context, key string) error {
	if !strings.HasPrefix(key, "xoxb-") {
		return fmt.Errorf("invalid bot token format: must start with xoxb-")
	}

	url := fmt.Sprintf("%s/api/auth.test", slackBotBaseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("invalid response from Slack API")
	}
	if !result.OK {
		return fmt.Errorf("Slack API rejected token: %s", result.Error)
	}
	return nil
}

func validateSlackAppToken(key string) error {
	if !strings.HasPrefix(key, "xapp-") {
		return fmt.Errorf("invalid app token format: must start with xapp-")
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/api/ -run "TestValidateSlack|TestValidProviders_IncludesSlack" -v`
Expected: PASS

- [ ] **Step 5: Run full api tests for regressions**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/api/ -v -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add backend/internal/api/credentials.go backend/internal/api/credentials_test.go
git commit -m "feat(slack): add Slack bot token and app token validation"
```

---

### Task 8: Extend channel_config.go for dual-token connect/disconnect/update

**Files:**
- Modify: `backend/internal/api/channel_config.go:36-43, 60-84, 162-185, 270-277, 308-341, 414-471`

- [ ] **Step 1: Add app_token to connect request struct**

In `handleChannelConnect` (line 36-43), change the request struct:

```go
	var req struct {
		Token    string                 `json:"token"`
		AppToken string                 `json:"app_token,omitempty"`
		Settings map[string]interface{} `json:"settings,omitempty"`
	}
```

- [ ] **Step 2: Add app token validation and credential storage in handleChannelConnect**

After the existing token validation (line 50-53), add app token handling:

```go
	// Validate app token if provided (required for Slack)
	if req.AppToken != "" {
		if err := validateProviderKey(r.Context(), channelID+"-app", req.AppToken); err != nil {
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("app token validation failed: %v", err))
			return
		}
	}
```

After the primary credential save (line 60-84), add the app token save:

```go
	// Save app token as separate credential (Slack dual-token)
	if req.AppToken != "" {
		appProvider := channelID + "-app"
		appEncrypted, err := crypto.Encrypt(req.AppToken, s.secretKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "app token encryption failed")
			return
		}
		var appLastFour *string
		if len(req.AppToken) >= 4 {
			l4 := req.AppToken[len(req.AppToken)-4:]
			appLastFour = &l4
		}
		appCred := &store.Credential{
			AccountID:      machine.AccountID,
			MachineID:      machineID,
			Provider:       appProvider,
			EncryptedValue: appEncrypted,
			CredentialType: "token",
			Label:          channelID + " app token",
			LastFour:       appLastFour,
		}
		if err := s.store.SetMachineCredential(r.Context(), machineID, appCred); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save app token credential")
			return
		}
	}
```

- [ ] **Step 3: Update handleChannelDisconnect for dual credentials**

After the existing credential delete (line 182-185), add:

```go
	// Delete companion credential (e.g. slack-app for slack)
	appProvider := channelID + "-app"
	if _, _, ok := configassembly.ProviderToChannel(appProvider); ok {
		if err := s.store.DeleteMachineCredential(r.Context(), machineID, appProvider); err != nil {
			slog.Warn("channel.disconnect.delete_app_cred_failed", "machine_id", machineID, "channel", channelID, "provider", appProvider, "error", err)
		}
	}
```

Add the import for `configassembly` if not already present.

- [ ] **Step 4: Update handleChannelUpdateToken for dual tokens**

Add `AppToken` to the request struct (line 324-328):

```go
	var req struct {
		Token    string `json:"token"`
		AppToken string `json:"app_token,omitempty"`
		Label    string `json:"label,omitempty"`
	}
```

After the primary credential update, add app token update (after line 375):

```go
	// Update app token if provided
	if req.AppToken != "" {
		if err := validateProviderKey(r.Context(), channelID+"-app", req.AppToken); err != nil {
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("app token validation failed: %v", err))
			return
		}
		appEncrypted, err := crypto.Encrypt(req.AppToken, s.secretKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "app token encryption failed")
			return
		}
		var appLastFour *string
		if len(req.AppToken) >= 4 {
			l4 := req.AppToken[len(req.AppToken)-4:]
			appLastFour = &l4
		}
		appCred := &store.Credential{
			AccountID:      machine.AccountID,
			MachineID:      machineID,
			Provider:       channelID + "-app",
			EncryptedValue: appEncrypted,
			CredentialType: "token",
			Label:          channelID + " app token",
			LastFour:       appLastFour,
		}
		if err := s.store.SetMachineCredential(r.Context(), machineID, appCred); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save app token credential")
			return
		}
	}
```

- [ ] **Step 5: Update buildChannelConfig to inject all token field refs**

Replace the single-field exec ref injection at lines 462-468:

```go
	// Inject exec secret refs for all token fields
	if fields, ok := configassembly.ChannelTokenFields[channelID]; ok {
		for _, field := range fields {
			if token != "" || field.Provider == channelID {
				channelConfig[field.FieldName] = map[string]interface{}{
					"source":   "exec",
					"provider": "ocm",
					"id":       fmt.Sprintf("channel-%s-%s", channelID, field.FieldName),
				}
			}
		}
	}
```

- [ ] **Step 6: Run backend tests**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/api/ -v -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add backend/internal/api/channel_config.go
git commit -m "feat(slack): extend channel connect/disconnect/update for dual-token support"
```

---

### Task 9: Add Slack validation (CLI)

**Files:**
- Modify: `cli/internal/commands/validate.go:14-33`
- Modify: `cli/internal/commands/validate_test.go`
- Modify: `cli/internal/commands/providers.go:16, 24`

- [ ] **Step 1: Write CLI validation tests**

Add to `cli/internal/commands/validate_test.go`:

```go
func TestValidateSlackBotToken_Valid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true,"team":"Test"}`))
	}))
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/auth.test", nil)
	req.Header.Set("Authorization", "Bearer xoxb-test")
	if err := doValidation(req, "Slack"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestValidateSlackBotToken_Invalid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/auth.test", nil)
	err := doValidation(req, "Slack")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestValidateSlackAppToken_Prefix(t *testing.T) {
	if err := validateSlackAppToken("xapp-1-valid"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if err := validateSlackAppToken("not-xapp"); err == nil {
		t.Fatal("expected error for bad prefix")
	}
}

func TestValidateCredential_Slack(t *testing.T) {
	// Slack routes to validateSlackBotToken which needs a server, so just test the routing exists.
	// The prefix check will fail before any HTTP call.
	err := validateCredential("slack", "not-xoxb-token")
	if err == nil {
		t.Fatal("expected error for bad prefix")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/mantiz/OpenClawMachines && cd cli && go test ./internal/commands/ -run "TestValidateSlack|TestValidateCredential_Slack" -v`
Expected: FAIL — `validateSlackAppToken` not defined.

- [ ] **Step 3: Implement CLI Slack validation**

In `cli/internal/commands/validate.go`, add cases in the `validateCredential` switch (line 18-33):

```go
case "slack":
	return validateSlackBotToken(ctx, key)
case "slack-app":
	return validateSlackAppToken(key)
```

Add the functions:

```go
func validateSlackBotToken(ctx context.Context, key string) error {
	if !strings.HasPrefix(key, "xoxb-") {
		return fmt.Errorf("invalid bot token format: must start with xoxb-")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://slack.com/api/auth.test", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)

	return doValidation(req, "Slack")
}

func validateSlackAppToken(key string) error {
	if !strings.HasPrefix(key, "xapp-") {
		return fmt.Errorf("invalid app token format: must start with xapp-")
	}
	return nil
}
```

In `cli/internal/commands/providers.go`, update `validProviderNames` (line 16):

```go
var validProviderNames = []string{"anthropic", "openai", "google", "openrouter", "discord", "telegram", "whatsapp", "slack"}
```

Update `inferCredentialType` (line 22-28) — add `"slack"` to the channel case:

```go
case "telegram", "discord", "whatsapp", "slack":
	return "token"
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/mantiz/OpenClawMachines && cd cli && go test ./internal/commands/ -run "TestValidateSlack|TestValidateCredential_Slack" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cli/internal/commands/validate.go cli/internal/commands/validate_test.go cli/internal/commands/providers.go
git commit -m "feat(slack): add Slack validation to CLI"
```

---

### Task 10: Add Slack to CLI channel setup

**Files:**
- Modify: `cli/internal/commands/channels_setup.go:14-38`
- Modify: `cli/internal/commands/channels_setup_test.go`

- [ ] **Step 1: Update test to expect Slack is supported**

Replace the test in `cli/internal/commands/channels_setup_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/mantiz/OpenClawMachines && cd cli && go test ./internal/commands/ -run "TestChannelsSetupSlackSupported" -v`
Expected: FAIL — slack not in channelInstructions.

- [ ] **Step 3: Add Slack to channelInstructions**

In `cli/internal/commands/channels_setup.go`, add to the `channelInstructions` map (line 14-38):

```go
"slack": {
	DisplayName: "Slack Bot",
	TokenURL:    "https://api.slack.com/apps",
	Steps:       []string{"Go to api.slack.com/apps → Create New App → From manifest", "Install to Workspace, copy Bot Token (xoxb-...)", "Generate App-Level Token with connections:write scope (xapp-...)"},
	Provider:    "slack",
},
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/mantiz/OpenClawMachines && cd cli && go test ./internal/commands/ -run "TestChannelsSetup" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cli/internal/commands/channels_setup.go cli/internal/commands/channels_setup_test.go
git commit -m "feat(slack): add Slack to CLI channel setup wizard"
```

---

### Task 11: Frontend types and API

**Files:**
- Modify: `frontend/src/lib/types.ts:69, 90-98`
- Modify: `frontend/src/lib/api.ts:580-584`

- [ ] **Step 1: Update CredentialProvider type**

In `frontend/src/lib/types.ts` (line 69), update the union:

```typescript
export type CredentialProvider = "anthropic" | "openai" | "openai-codex" | "google" | "openrouter" | "discord" | "telegram" | "whatsapp" | "slack" | "slack-app";
```

- [ ] **Step 2: Add Slack to CREDENTIAL_PROVIDERS (not slack-app)**

In `frontend/src/lib/types.ts` (after line 97, the whatsapp entry), add:

```typescript
  { id: "slack", label: "Slack Bot", placeholder: "Bot token (xoxb-...)...", category: "automation", iconLetter: "S", iconBg: "bg-purple-700", defaultCredentialType: "token" },
```

Do NOT add `"slack-app"` here — it's internal.

- [ ] **Step 3: Update connectChannel API to support app_token**

In `frontend/src/lib/api.ts` (line 580), update the function signature:

```typescript
export const connectChannel = (accountId: number, machineId: string, channel: string, data: { token: string; app_token?: string; settings?: Record<string, unknown> }) =>
  request<ChannelResponse>(`/accounts/${accountId}/machines/${machineId}/channels/${channel}/connect`, {
    method: "POST",
    body: JSON.stringify(data),
  });
```

- [ ] **Step 4: Run typecheck**

Run: `cd /home/mantiz/OpenClawMachines && make typecheck`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/types.ts frontend/src/lib/api.ts
git commit -m "feat(slack): add Slack to frontend types and API"
```

---

### Task 12: Frontend ChannelsTab — Slack instructions and dual-token dialog

**Files:**
- Modify: `frontend/src/pages/machine-tabs/ChannelsTab.tsx:74-78, 149-153, 659-712`

- [ ] **Step 1: Update Slack channel definition**

In `frontend/src/pages/machine-tabs/ChannelsTab.tsx`, replace the Slack entry (lines 74-78):

```typescript
  {
    id: "slack",
    label: "Slack",
    shortDesc: "Bot token · Socket Mode",
    credentialProvider: "slack",
    hasSettings: true,
    instructions: {
      title: "Connect Slack",
      steps: [
        "Go to api.slack.com/apps and create a New App from manifest",
        "Install the app to your workspace (Install App → Install to Workspace)",
        "Copy the Bot User OAuth Token (xoxb-...) from OAuth & Permissions",
        "Generate an App-Level Token with connections:write scope from Basic Information",
      ],
      link: "https://api.slack.com/apps",
      linkLabel: "Open Slack API",
    },
  },
```

- [ ] **Step 2: Add app token state**

Near the existing `tokenInput` state (around line 90-91), add:

```typescript
const [appTokenInput, setAppTokenInput] = useState("");
```

- [ ] **Step 3: Update handleConnect to send app_token**

In the `handleConnect` function, update the `connectChannel` call to include the app token:

```typescript
    const res = await connectChannel(accountId, machine.id, setupChannel.id, {
      token: tokenInput.trim(),
      ...(appTokenInput.trim() ? { app_token: appTokenInput.trim() } : {}),
    });
```

Also reset `appTokenInput` on close:

```typescript
    setAppTokenInput("");
```

- [ ] **Step 4: Update setup dialog reset**

In the button that opens the dialog (around line 405), also reset the app token:

```typescript
onClick={() => { setSetupChannel(channel); setTokenInput(""); setAppTokenInput(""); setValidationResult(null); }}
```

- [ ] **Step 5: Add app token input to setup dialog**

In the setup dialog, after the existing "Bot Token" input section and before the validation result section, add the app token input for Slack channels. Find the token input `<div className="space-y-2">` block (around line 660) and after it, add:

```tsx
                {/* App Token input (Slack only) */}
                {setupChannel.id === "slack" && (
                  <div className="space-y-2 mt-3">
                    <label className="text-xs font-medium text-text-secondary">
                      App Token
                    </label>
                    <input
                      type="password"
                      value={appTokenInput}
                      onChange={(e) => setAppTokenInput(e.target.value)}
                      placeholder="Paste your app-level token (xapp-...)..."
                      className="w-full px-3 py-2 text-sm bg-input border border-border rounded-[var(--radius-sm)] font-mono text-text-primary placeholder:text-text-muted outline-none focus:border-brand-500"
                    />
                  </div>
                )}
```

- [ ] **Step 6: Update Connect button disabled logic**

Update the Connect button's disabled condition (around line 703) to also require the app token for Slack:

```tsx
                  <button
                    onClick={handleConnect}
                    disabled={!validationResult?.ok || saving || (setupChannel?.id === "slack" && !appTokenInput.trim())}
                    className="text-sm font-medium px-3 py-1.5 rounded-[var(--radius-sm)] bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50 transition-colors"
                  >
```

- [ ] **Step 7: Run typecheck and dev build**

Run: `cd /home/mantiz/OpenClawMachines && make typecheck`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add frontend/src/pages/machine-tabs/ChannelsTab.tsx
git commit -m "feat(slack): add Slack setup instructions and dual-token dialog"
```

---

### Task 13: Run full test suite

- [ ] **Step 1: Run Go backend tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: PASS

- [ ] **Step 2: Run frontend typecheck**

Run: `cd /home/mantiz/OpenClawMachines && make typecheck`
Expected: PASS

- [ ] **Step 3: Run gateway E2E tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-gateway-e2e`
Expected: PASS (no regressions in channel config assembly)

- [ ] **Step 4: Commit any test fixes if needed**

---

### Task 14: Update CurrentFeature.md

**Files:**
- Modify: `docs/CurrentFeature.md`

- [ ] **Step 1: Update CurrentFeature.md with implementation summary**

```markdown
# Current Feature: slackchannel

## Summary
Enable Slack as a fully supported messaging channel with dual-token support (bot + app-level token).

## Changes
- Refactored `ChannelTokenFieldName` to `ChannelTokenFields` with struct-based multi-token mapping
- Added `ProviderToChannel()` reverse lookup for correct secret ID resolution
- Migrated 5 callsites: assembler, agent_auth, machine_config, runtime, channel_config
- Added Slack registry migration (055)
- Backend + CLI validation: bot token via auth.test API, app token via xapp- prefix
- Frontend: Slack setup dialog with dual-token input
- CLI: `ocm channels setup slack` support
```

- [ ] **Step 2: Commit**

```bash
git add docs/CurrentFeature.md
git commit -m "docs: update CurrentFeature.md with Slack channel implementation"
```
