//go:build linux

package ptyd

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	codexClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexRedirectURI = "http://localhost:1455/auth/callback"
	codexAuthURL     = "https://auth.openai.com/oauth/authorize"
	codexTokenURL    = "https://auth.openai.com/oauth/token"

	authProfilesPath = "/home/openclaw/.openclaw/agents/main/agent/auth-profiles.json"
)

var codexHTTPClient = &http.Client{Timeout: 15 * time.Second}

type pkceState struct {
	Verifier string `json:"verifier"`
	State    string `json:"state"`
}

func codexPKCEFilePath() string {
	machineID := os.Getenv("OCM_MACHINE_ID")
	if machineID == "" {
		return "/tmp/ocm-codex-pkce.json"
	}
	return fmt.Sprintf("/tmp/ocm-codex-pkce-%s.json", machineID)
}

func readAuthProfiles() (map[string]any, error) {
	data, err := os.ReadFile(authProfilesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{
				"version":  float64(1),
				"profiles": map[string]any{},
			}, nil
		}
		return nil, err
	}
	var profiles map[string]any
	if err := json.Unmarshal(data, &profiles); err != nil {
		return nil, fmt.Errorf("parse auth-profiles.json: %w", err)
	}
	return profiles, nil
}

func writeCodexAuthProfile(accessToken, refreshToken string, expiresAt time.Time) error {
	authProfiles, err := readAuthProfiles()
	if err != nil {
		return fmt.Errorf("read existing auth-profiles: %w", err)
	}

	profiles, ok := authProfiles["profiles"].(map[string]any)
	if !ok {
		profiles = map[string]any{}
	}
	profiles["openai-codex:default"] = map[string]any{
		"type":     "oauth",
		"provider": "openai-codex",
		"access":   accessToken,
		"refresh":  refreshToken,
		"expires":  expiresAt.UnixMilli(),
	}
	authProfiles["profiles"] = profiles

	data, err := json.MarshalIndent(authProfiles, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth-profiles: %w", err)
	}
	if err := os.WriteFile(authProfilesPath, data, 0o644); err != nil {
		return fmt.Errorf("write auth-profiles.json: %w", err)
	}
	fmt.Fprintf(os.Stderr, "codex-auth: wrote OAuth profile to %s (expires=%s)\n",
		authProfilesPath, expiresAt.Format(time.RFC3339))
	return nil
}

func codexAuthCheck() string {
	authProfiles, err := readAuthProfiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex-auth check: cannot read auth-profiles: %v\n", err)
		result, _ := json.Marshal(map[string]any{"connected": false, "error": err.Error()})
		return string(result)
	}

	profiles, _ := authProfiles["profiles"].(map[string]any)
	codexProfile, ok := profiles["openai-codex:default"].(map[string]any)
	if !ok {
		result, _ := json.Marshal(map[string]any{"connected": false})
		return string(result)
	}

	accessToken, _ := codexProfile["access"].(string)
	if accessToken == "" {
		result, _ := json.Marshal(map[string]any{"connected": false})
		return string(result)
	}
	if expiresMs, ok := codexProfile["expires"].(float64); ok {
		expiresAt := time.UnixMilli(int64(expiresMs))
		if time.Now().After(expiresAt) {
			fmt.Fprintf(os.Stderr, "codex-auth check: token expired at %s\n", expiresAt.Format(time.RFC3339))
			result, _ := json.Marshal(map[string]any{"connected": true, "expired": true})
			return string(result)
		}
	}

	result, _ := json.Marshal(map[string]any{"connected": true})
	return string(result)
}

func codexAuthTest(model string) string {
	authProfiles, err := readAuthProfiles()
	if err != nil {
		result, _ := json.Marshal(map[string]any{"ok": false, "error": "cannot read auth-profiles: " + err.Error()})
		return string(result)
	}

	profiles, _ := authProfiles["profiles"].(map[string]any)
	codexProfile, ok := profiles["openai-codex:default"].(map[string]any)
	if !ok {
		result, _ := json.Marshal(map[string]any{"ok": false, "error": "no codex profile in auth-profiles.json"})
		return string(result)
	}

	accessToken, _ := codexProfile["access"].(string)
	if accessToken == "" {
		result, _ := json.Marshal(map[string]any{"ok": false, "error": "no access token in codex profile"})
		return string(result)
	}

	fmt.Fprintf(os.Stderr, "codex-auth test: model=%s, token=%s...\n", model, truncateStr(accessToken, 12))

	payload, _ := json.Marshal(map[string]any{
		"model":        model,
		"instructions": "Reply OK.",
		"input":        []map[string]string{{"role": "user", "content": "hi"}},
		"stream":       true,
		"store":        false,
	})

	testURL := "https://chatgpt.com/backend-api/codex/responses"
	fmt.Fprintf(os.Stderr, "codex-auth test: POST %s\n", testURL)
	req, _ := http.NewRequest("POST", testURL, bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := codexHTTPClient.Do(req)
	if err != nil {
		result, _ := json.Marshal(map[string]any{"ok": false, "error": "request failed: " + err.Error()})
		return string(result)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	fmt.Fprintf(os.Stderr, "codex-auth test: HTTP %d, body=%s\n", resp.StatusCode, truncateStr(string(body), 200))

	if resp.StatusCode == http.StatusUnauthorized {
		result, _ := json.Marshal(map[string]any{"ok": false, "error": "invalid or expired token"})
		return string(result)
	}
	if resp.StatusCode >= 400 {
		var apiErr struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Detail != "" {
			result, _ := json.Marshal(map[string]any{"ok": false, "error": apiErr.Detail})
			return string(result)
		}
		result, _ := json.Marshal(map[string]any{"ok": false, "error": fmt.Sprintf("HTTP %d", resp.StatusCode)})
		return string(result)
	}

	result, _ := json.Marshal(map[string]any{"ok": true})
	return string(result)
}

func codexAuthGenerateURL() (string, error) {
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", fmt.Errorf("generating verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])

	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("generating state: %w", err)
	}
	state := fmt.Sprintf("%x", stateBytes)

	pkce := pkceState{Verifier: verifier, State: state}
	pkceJSON, _ := json.Marshal(pkce)
	if err := os.WriteFile(codexPKCEFilePath(), pkceJSON, 0o600); err != nil {
		return "", fmt.Errorf("saving PKCE state: %w", err)
	}

	params := url.Values{
		"response_type":              {"code"},
		"client_id":                  {codexClientID},
		"redirect_uri":               {codexRedirectURI},
		"scope":                      {"openid profile email offline_access"},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"state":                      {state},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"originator":                 {"pi"},
	}
	authURL := codexAuthURL + "?" + params.Encode()

	result, _ := json.Marshal(map[string]string{"url": authURL, "state": state})
	return string(result), nil
}

func codexAuthExchange(callbackInput string) (string, error) {
	fmt.Fprintf(os.Stderr, "codex-auth exchange: starting with input length=%d\n", len(callbackInput))

	code := callbackInput
	if strings.Contains(callbackInput, "://") {
		u, err := url.Parse(callbackInput)
		if err != nil {
			return "", fmt.Errorf("parse callback URL: %w", err)
		}
		code = u.Query().Get("code")
		if code == "" {
			return "", fmt.Errorf("callback URL missing code")
		}
		fmt.Fprintf(os.Stderr, "codex-auth exchange: extracted code from URL (code=%s...)\n", truncateStr(code, 12))
	} else {
		fmt.Fprintf(os.Stderr, "codex-auth exchange: using raw code (code=%s...)\n", truncateStr(code, 12))
	}

	pkceFile := codexPKCEFilePath()
	fmt.Fprintf(os.Stderr, "codex-auth exchange: reading PKCE from %s\n", pkceFile)
	pkceJSON, err := os.ReadFile(pkceFile)
	if err != nil {
		return "", fmt.Errorf("read PKCE state: %w", err)
	}

	var pkce pkceState
	if err := json.Unmarshal(pkceJSON, &pkce); err != nil {
		return "", fmt.Errorf("parse PKCE state: %w", err)
	}
	fmt.Fprintf(os.Stderr, "codex-auth exchange: PKCE verifier=%s..., state=%s...\n",
		truncateStr(pkce.Verifier, 12), truncateStr(pkce.State, 12))

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {codexRedirectURI},
		"client_id":     {codexClientID},
		"code_verifier": {pkce.Verifier},
	}

	fmt.Fprintf(os.Stderr, "codex-auth exchange: POST %s\n", codexTokenURL)
	req, _ := http.NewRequest("POST", codexTokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := codexHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	fmt.Fprintf(os.Stderr, "codex-auth exchange: token response HTTP %d, body_length=%d\n", resp.StatusCode, len(body))
	if resp.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "codex-auth exchange: token exchange failed: %s\n", truncateStr(string(body), 500))
		return "", fmt.Errorf("token exchange HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if tokenResp.AccessToken == "" || tokenResp.RefreshToken == "" {
		fmt.Fprintf(os.Stderr, "codex-auth exchange: missing tokens (access=%v, refresh=%v)\n",
			tokenResp.AccessToken != "", tokenResp.RefreshToken != "")
		return "", fmt.Errorf("token response missing access_token or refresh_token")
	}

	fmt.Fprintf(os.Stderr, "codex-auth exchange: got tokens (access=%s..., refresh=%s..., expires_in=%d)\n",
		truncateStr(tokenResp.AccessToken, 12), truncateStr(tokenResp.RefreshToken, 12), tokenResp.ExpiresIn)

	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	if err := writeCodexAuthProfile(tokenResp.AccessToken, tokenResp.RefreshToken, expiresAt); err != nil {
		return "", err
	}
	_ = os.Remove(pkceFile)

	fmt.Fprintln(os.Stderr, "codex-auth exchange: success")
	result, _ := json.Marshal(map[string]any{"ok": true, "expires": expiresAt.UnixMilli()})
	return string(result), nil
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
