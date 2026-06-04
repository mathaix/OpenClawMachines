package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultNoncePath     = "/run/ocm-nonce"
	defaultGatewayIPPath = "/run/ocm-gateway-ip"
	defaultGatewayIP     = "192.168.100.1"
	defaultHermesHome    = "/opt/data"

	envBlockBegin = "# BEGIN OCM MANAGED HERMES ENV"
	envBlockEnd   = "# END OCM MANAGED HERMES ENV"
)

var managedPlatformKeys = map[string]bool{
	"api_server": true,
	"telegram":   true,
	"discord":    true,
	"slack":      true,
}

var defaultManagedEnvKeys = map[string]bool{
	"API_SERVER_ENABLED":        true,
	"API_SERVER_HOST":           true,
	"API_SERVER_PORT":           true,
	"API_SERVER_KEY":            true,
	"HERMES_DASHBOARD":          true,
	"HERMES_DASHBOARD_HOST":     true,
	"HERMES_DASHBOARD_PORT":     true,
	"HERMES_DASHBOARD_TUI":      true,
	"HERMES_INFERENCE_PROVIDER": true,
	"TELEGRAM_BOT_TOKEN":        true,
	"TELEGRAM_ALLOWED_USERS":    true,
	"DISCORD_BOT_TOKEN":         true,
	"SLACK_BOT_TOKEN":           true,
	"SLACK_APP_TOKEN":           true,
	"ANTHROPIC_API_KEY":         true,
	"OPENAI_API_KEY":            true,
	"GOOGLE_API_KEY":            true,
	"OPENROUTER_API_KEY":        true,
	"OLLAMA_API_KEY":            true,
	"GLM_API_KEY":               true,
	"KIMI_API_KEY":              true,
	"MINIMAX_API_KEY":           true,
	"ARCEEAI_API_KEY":           true,
	"HF_TOKEN":                  true,
	"OPENCODE_ZEN_API_KEY":      true,
	"OPENCODE_GO_API_KEY":       true,
	"NOUS_API_KEY":              true,
	"NEBIUS_API_KEY":            true,
}

type secretFetcher interface {
	FetchSecrets(gatewayIP, nonce string) (map[string]string, error)
}

type httpSecretFetcher struct {
	client *http.Client
}

func (f *httpSecretFetcher) FetchSecrets(gatewayIP, nonce string) (map[string]string, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/v1/secrets?fresh=1", gatewayIP), nil)
	if err != nil {
		return nil, fmt.Errorf("create metadata request: %w", err)
	}
	req.Header.Set("X-Metadata-Nonce", nonce)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metadata server unreachable at %s: %w", gatewayIP, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("metadata server returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var secrets map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&secrets); err != nil {
		return nil, fmt.Errorf("decode metadata secrets: %w", err)
	}
	return secrets, nil
}

type runConfig struct {
	noncePath     string
	gatewayIPPath string
	configPath    string
	envPath       string
	fetcher       secretFetcher
	restart       func() error
}

func run(args []string, cfg runConfig) error {
	if len(args) == 0 || args[0] != "sync" {
		return fmt.Errorf("usage: ocm-hermes-config sync [--restart-gateway]")
	}

	restartGateway := false
	for _, arg := range args[1:] {
		switch arg {
		case "--restart-gateway":
			restartGateway = true
		default:
			return fmt.Errorf("unknown argument %q", arg)
		}
	}

	nonce, err := readTrimmed(cfg.noncePath)
	if err != nil {
		return fmt.Errorf("read metadata nonce: %w", err)
	}
	if nonce == "" {
		return fmt.Errorf("metadata nonce is empty")
	}

	gatewayIP := readGatewayIP(cfg.gatewayIPPath)
	secrets, err := cfg.fetcher.FetchSecrets(gatewayIP, nonce)
	if err != nil {
		return err
	}

	desiredConfig := strings.TrimSpace(secrets["HERMES_CONFIG_YAML"])
	if desiredConfig == "" {
		return fmt.Errorf("metadata response did not include HERMES_CONFIG_YAML")
	}
	desiredConfig = substituteOCMPlaceholders(desiredConfig, gatewayIP, nonce)

	existingConfig, _ := os.ReadFile(cfg.configPath)
	mergedConfig, err := mergeHermesConfig(existingConfig, []byte(desiredConfig))
	if err != nil {
		return err
	}
	if err := atomicWriteFile(cfg.configPath, mergedConfig, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfg.configPath, err)
	}

	if desiredEnv := strings.TrimSpace(secrets["HERMES_ENV"]); desiredEnv != "" {
		existingEnv, _ := os.ReadFile(cfg.envPath)
		mergedEnv := mergeDotEnv(string(existingEnv), desiredEnv)
		if err := atomicWriteFile(cfg.envPath, []byte(mergedEnv), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", cfg.envPath, err)
		}
	}

	if restartGateway {
		if err := cfg.restart(); err != nil {
			return err
		}
	}
	return nil
}

func mergeHermesConfig(existingBytes, desiredBytes []byte) ([]byte, error) {
	existing, err := parseYAMLMap(existingBytes)
	if err != nil {
		return nil, fmt.Errorf("parse existing config.yaml: %w", err)
	}
	desired, err := parseYAMLMap(desiredBytes)
	if err != nil {
		return nil, fmt.Errorf("parse desired config.yaml: %w", err)
	}

	for _, key := range []string{"model", "terminal", "dashboard", "browser"} {
		if value, ok := desired[key]; ok {
			existing[key] = value
		}
	}

	existingPlatforms := mapFromAny(existing["platforms"])
	for key := range managedPlatformKeys {
		delete(existingPlatforms, key)
	}
	for key, value := range mapFromAny(desired["platforms"]) {
		existingPlatforms[key] = value
	}
	if len(existingPlatforms) > 0 {
		existing["platforms"] = existingPlatforms
	} else {
		delete(existing, "platforms")
	}

	out, err := yaml.Marshal(existing)
	if err != nil {
		return nil, fmt.Errorf("marshal merged config.yaml: %w", err)
	}
	return out, nil
}

func parseYAMLMap(data []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

func mapFromAny(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok && typed != nil {
		return typed
	}
	return map[string]any{}
}

func mergeDotEnv(existing, desired string) string {
	desiredValues := parseDotEnvAssignments(desired)
	managed := make(map[string]bool, len(defaultManagedEnvKeys)+len(desiredValues))
	for key := range defaultManagedEnvKeys {
		managed[key] = true
	}
	for key := range desiredValues {
		managed[key] = true
	}

	var kept []string
	inManagedBlock := false
	scanner := bufio.NewScanner(strings.NewReader(existing))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case envBlockBegin:
			inManagedBlock = true
			continue
		case envBlockEnd:
			inManagedBlock = false
			continue
		}
		if inManagedBlock {
			continue
		}
		if key, ok := dotenvLineKey(line); ok && managed[key] {
			continue
		}
		kept = append(kept, line)
	}

	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}

	keys := make([]string, 0, len(desiredValues))
	for key := range desiredValues {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	if len(keys) > 0 {
		if len(kept) > 0 {
			kept = append(kept, "")
		}
		kept = append(kept, envBlockBegin)
		for _, key := range keys {
			kept = append(kept, key+"="+desiredValues[key])
		}
		kept = append(kept, envBlockEnd)
	}

	return strings.Join(kept, "\n") + "\n"
}

func parseDotEnvAssignments(input string) map[string]string {
	out := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(input))
	for scanner.Scan() {
		line := scanner.Text()
		key, ok := dotenvLineKey(line)
		if !ok {
			continue
		}
		idx := strings.Index(line, "=")
		out[key] = strings.TrimSpace(line[idx+1:])
	}
	return out
}

func dotenvLineKey(line string) (string, bool) {
	trimmedLeft := strings.TrimLeft(line, " \t")
	if trimmedLeft == "" || strings.HasPrefix(trimmedLeft, "#") {
		return "", false
	}
	idx := strings.Index(trimmedLeft, "=")
	if idx <= 0 {
		return "", false
	}
	key := strings.TrimSpace(trimmedLeft[:idx])
	if !validEnvKey(key) {
		return "", false
	}
	return key, true
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if i == 0 {
			if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && r != '_' {
				return false
			}
			continue
		}
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func substituteOCMPlaceholders(input, gatewayIP, nonce string) string {
	replacer := strings.NewReplacer(
		"__OCM_API_PROXY_BASE_URL__", "http://"+gatewayIP+":4000",
		"__OCM_API_PROXY_KEY__", nonce,
	)
	return replacer.Replace(input)
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ocm-hermes-config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func readTrimmed(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func readGatewayIP(path string) string {
	gatewayIP, err := readTrimmed(path)
	if err != nil || gatewayIP == "" {
		return defaultGatewayIP
	}
	return gatewayIP
}

func restartGateway() error {
	out, err := exec.Command("sudo", "-n", "/usr/local/bin/ocm-restart-gateway").CombinedOutput()
	if err != nil {
		return fmt.Errorf("restart Hermes gateway: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func main() {
	home := os.Getenv("HERMES_HOME")
	if home == "" {
		home = defaultHermesHome
	}
	cfg := runConfig{
		noncePath:     envOrDefault("OCM_NONCE_PATH", defaultNoncePath),
		gatewayIPPath: envOrDefault("OCM_GATEWAY_IP_PATH", defaultGatewayIPPath),
		configPath:    envOrDefault("OCM_HERMES_CONFIG_PATH", filepath.Join(home, "config.yaml")),
		envPath:       envOrDefault("OCM_HERMES_ENV_PATH", filepath.Join(home, ".env")),
		fetcher:       &httpSecretFetcher{client: &http.Client{Timeout: 10 * time.Second}},
		restart:       restartGateway,
	}
	if err := run(os.Args[1:], cfg); err != nil {
		fmt.Fprintf(os.Stderr, "ocm-hermes-config: ERROR: %v\n", err)
		os.Exit(1)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
