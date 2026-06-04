package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	clientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	redirectURI = "http://localhost:1455/auth/callback"
	authURL     = "https://auth.openai.com/oauth/authorize"
	tokenURL    = "https://auth.openai.com/oauth/token"
)

// pkceState holds verifier/state to finish the flow.
type pkceState struct {
	Verifier string `json:"verifier"`
	State    string `json:"state"`
}

// requestBody is the payload for the ChatGPT Codex Responses call.
type requestBody struct {
	Model        string      `json:"model"`
	Instructions string      `json:"instructions"`
	Input        interface{} `json:"input"`
	Store        bool        `json:"store"`
	Stream       bool        `json:"stream"`
}

// errorResponse captures upstream detail if present.
type errorResponse struct {
	Detail string `json:"detail"`
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func savePKCE(path string, p pkceState) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func loadPKCE(path string) (pkceState, error) {
	var p pkceState
	data, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	return p, nil
}

func main() {
	var (
		model     string
		modelsCSV string
		input     string
		base      string
		path      string
		timeout   time.Duration
		pkceFile  string
		token     string
		skipTest  bool
	)

	flag.StringVar(&model, "model", "gpt-5.5", "Model id to send (ignored if -models is set)")
	flag.StringVar(&modelsCSV, "models", "", "Comma-separated list of models to test")
	flag.StringVar(&input, "input", "hi", "Input text")
	flag.StringVar(&base, "base", "https://chatgpt.com/backend-api", "Base URL")
	flag.StringVar(&path, "path", "/codex/responses", "Path")
	flag.DurationVar(&timeout, "timeout", 15*time.Second, "HTTP timeout")
	flag.StringVar(&pkceFile, "pkce", ".codex-pkce.json", "Path to store PKCE state")
	flag.StringVar(&token, "token", os.Getenv("TOKEN"), "Bearer token (skip PKCE if set)")
	flag.BoolVar(&skipTest, "no-test", false, "Skip calling /codex/responses after exchange")
	flag.Parse()

	models := resolveModels(modelsCSV, model)

	// If token provided, skip PKCE and just call test (unless skipped).
	if token != "" {
		if !skipTest {
			runModelTests(token, models, input, base, path, timeout)
		}
		return
	}

	// Full flow: generate URL, prompt for callback, exchange, then test.
	pkce, err := generatePKCE(pkceFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pkce generate: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\nOpen this URL, sign in, then paste the callback URL:")
	fmt.Println(buildAuthURL(pkce))
	fmt.Printf("\nPKCE stored at %s\n", filepath.Clean(pkceFile))
	fmt.Print("\nCallback URL: ")
	cb := readLine()

	token, err = exchange(pkceFile, cb, timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exchange failed: %v\n", err)
		os.Exit(1)
	}

	if skipTest {
		return
	}

	runModelTests(token, models, input, base, path, timeout)
}

func resolveModels(csv, single string) []string {
	if strings.TrimSpace(csv) == "" {
		return []string{single}
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{single}
	}
	return out
}

func runModelTests(token string, models []string, input, base, path string, timeout time.Duration) {
	for _, m := range models {
		fmt.Printf("\n== Testing model: %s ==\n", m)
		if err := callResponses(token, m, input, base, path, timeout); err != nil {
			fmt.Fprintf(os.Stderr, "model %s failed: %v\n", m, err)
		}
	}
}

func generatePKCE(pkceFile string) (pkceState, error) {
	ver := make([]byte, 32)
	if _, err := rand.Read(ver); err != nil {
		return pkceState{}, err
	}
	verifier := b64url(ver)
	state := b64url([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))

	p := pkceState{Verifier: verifier, State: state}
	if err := savePKCE(pkceFile, p); err != nil {
		return pkceState{}, err
	}
	return p, nil
}

func buildAuthURL(p pkceState) string {
	v := url.Values{
		"response_type":  {"code"},
		"client_id":      {clientID},
		"redirect_uri":   {redirectURI},
		"scope":          {"openid profile email offline_access"},
		"code_challenge": {p.State[:len(p.State)/2]}, // placeholder to avoid logging verifier
	}
	// Use real challenge
	hash := sha256.Sum256([]byte(p.Verifier))
	v.Set("code_challenge", b64url(hash[:]))
	v.Set("code_challenge_method", "S256")
	v.Set("state", p.State)
	v.Set("codex_cli_simplified_flow", "true")
	v.Set("originator", "pi")
	return authURL + "?" + v.Encode()
}

func exchange(pkceFile, cbURL string, timeout time.Duration) (string, error) {
	pkce, err := loadPKCE(pkceFile)
	if err != nil {
		return "", fmt.Errorf("load pkce: %w", err)
	}

	u, err := url.Parse(cbURL)
	if err != nil {
		return "", fmt.Errorf("parse callback: %w", err)
	}
	q := u.Query()
	code := q.Get("code")
	state := q.Get("state")
	if code == "" {
		return "", fmt.Errorf("callback missing code")
	}
	if state != pkce.State {
		return "", fmt.Errorf("state mismatch")
	}

	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {pkce.Verifier},
	}

	req, _ := http.NewRequest("POST", tokenURL, strings.NewReader(data.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token http %d: %s", resp.StatusCode, string(body))
	}

	var tok map[string]interface{}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", err
	}
	access, _ := tok["access_token"].(string)
	fmt.Println("token response:")
	enc, _ := json.MarshalIndent(tok, "", "  ")
	fmt.Println(string(enc))
	if access == "" {
		return "", fmt.Errorf("access_token missing")
	}
	return access, nil
}

// stripProviderPrefix removes the "provider/" prefix from model IDs.
// ChatGPT's API expects "gpt-5.5", not "openai-codex/gpt-5.5".
func stripProviderPrefix(model string) string {
	if i := strings.Index(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
}

func callResponses(token, model, input, base, path string, timeout time.Duration) error {
	model = stripProviderPrefix(model)
	body := requestBody{
		Model:        model,
		Instructions: "You are a helpful assistant.",
		Input: []map[string]string{
			{"role": "user", "content": input},
		},
		Stream: true,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}
	url := base + path
	req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-pkce-tester/0.1")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)

	fmt.Printf("status: %d\n", resp.StatusCode)
	fmt.Printf("body: %s\n", string(bodyBytes))
	if resp.StatusCode >= 400 {
		var er errorResponse
		if err := json.Unmarshal(bodyBytes, &er); err == nil && er.Detail != "" {
			fmt.Printf("detail: %s\n", er.Detail)
		}
		return fmt.Errorf("upstream http %d", resp.StatusCode)
	}
	return nil
}

func readLine() string {
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}
