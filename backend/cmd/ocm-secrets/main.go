package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultNoncePath     = "/run/ocm-nonce"
	defaultGatewayIPPath = "/run/ocm-gateway-ip"
	defaultGatewayIP     = "192.168.100.1"
)

// isProxyCredential returns true if the secret ID is a provider credential
// that should resolve to the VM nonce for proxy-based key injection.
// Convention: proxied model providers use the "{provider}-key" naming pattern
// (e.g., "anthropic-key", "openai-key"). Search tool/plugin secrets use the
// "search-*" namespace and must resolve to the real provider API key instead.
//
// Channel tokens use "channel-{provider}-{field}" and are fetched from
// metadata, not resolved via nonce. We exclude them explicitly to avoid
// false matches on IDs like "channel-some-service-key". Search secrets are
// also excluded so web-search plugins receive the real API key from metadata.
func isProxyCredential(id string) bool {
	return strings.HasSuffix(id, "-key") &&
		!strings.HasPrefix(id, "channel-") &&
		!strings.HasPrefix(id, "search-")
}

type execRequest struct {
	ProtocolVersion int      `json:"protocolVersion"`
	Provider        string   `json:"provider"`
	IDs             []string `json:"ids"`
}

type execResponse struct {
	ProtocolVersion int                    `json:"protocolVersion"`
	Values          map[string]interface{} `json:"values"`
	Errors          map[string]interface{} `json:"errors,omitempty"`
}

// secretFetcher abstracts the metadata service HTTP call for testability.
type secretFetcher interface {
	FetchSecrets(gatewayIP, nonce string) (map[string]string, error)
}

// httpSecretFetcher fetches platform secrets from the host metadata service.
type httpSecretFetcher struct {
	client *http.Client
}

func (f *httpSecretFetcher) FetchSecrets(gatewayIP, nonce string) (map[string]string, error) {
	url := fmt.Sprintf("http://%s/v1/secrets", gatewayIP)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-Metadata-Nonce", nonce)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metadata server unreachable at %s: %w", gatewayIP, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("metadata server returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var secrets map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&secrets); err != nil {
		return nil, fmt.Errorf("decode secrets response: %w", err)
	}
	return secrets, nil
}

// runConfig holds all dependencies for the run function.
type runConfig struct {
	noncePath     string
	gatewayIPPath string
	fetcher       secretFetcher
}

func logInfo(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ocm-secrets: "+format+"\n", args...)
}

func logError(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ocm-secrets: ERROR: "+format+"\n", args...)
}

func run(stdin io.Reader, stdout io.Writer, cfg runConfig) int {
	input, err := io.ReadAll(stdin)
	if err != nil {
		logError("failed to read stdin: %v", err)
		return 1
	}

	var req execRequest
	if err := json.Unmarshal(input, &req); err != nil {
		logError("invalid JSON input: %v (raw: %q)", err, string(input))
		return 1
	}

	logInfo("resolving %d secret(s): %v", len(req.IDs), req.IDs)

	// Read the VM nonce — used as the API proxy auth token for provider keys
	// and as the metadata server auth credential for platform secrets.
	nonceBytes, err := os.ReadFile(cfg.noncePath)
	if err != nil {
		logError("cannot read nonce from %s: %v", cfg.noncePath, err)
		return 1
	}
	nonce := strings.TrimSpace(string(nonceBytes))
	if nonce == "" {
		logError("nonce file %s is empty", cfg.noncePath)
		return 1
	}

	if len(req.IDs) == 0 {
		logError("no secret IDs requested")
		return 1
	}

	out := execResponse{
		ProtocolVersion: 1,
		Values:          make(map[string]interface{}, len(req.IDs)),
	}

	// Separate proxy keys from platform secrets.
	var platformIDs []string
	for _, id := range req.IDs {
		if isProxyCredential(id) {
			out.Values[id] = nonce
			logInfo("resolved %q via proxy nonce (convention: ends with -key)", id)
		} else {
			platformIDs = append(platformIDs, id)
			logInfo("classified %q as platform secret (will fetch from metadata)", id)
		}
	}

	// Resolve platform secrets from the metadata service.
	if len(platformIDs) > 0 {
		gatewayIP := readGatewayIP(cfg.gatewayIPPath)
		logInfo("fetching %d platform secret(s) from metadata server at %s: %v", len(platformIDs), gatewayIP, platformIDs)

		secrets, fetchErr := cfg.fetcher.FetchSecrets(gatewayIP, nonce)
		if fetchErr != nil {
			logError("metadata fetch failed: %v", fetchErr)
			if out.Errors == nil {
				out.Errors = make(map[string]interface{})
			}
			for _, id := range platformIDs {
				out.Errors[id] = fmt.Sprintf("fetch failed: %v", fetchErr)
			}
		} else {
			logInfo("metadata returned %d secret(s)", len(secrets))
			for _, id := range platformIDs {
				if val, ok := secrets[id]; ok {
					out.Values[id] = val
					logInfo("resolved %q from metadata (%d chars)", id, len(val))
				} else {
					logError("secret %q not found in metadata response (available keys: %v)", id, mapKeys(secrets))
					if out.Errors == nil {
						out.Errors = make(map[string]interface{})
					}
					out.Errors[id] = fmt.Sprintf("secret %q not found in metadata", id)
				}
			}
		}
	}

	if len(out.Errors) > 0 {
		logError("completed with %d error(s): %v", len(out.Errors), out.Errors)
	} else {
		logInfo("all %d secret(s) resolved successfully", len(out.Values))
	}

	if err := json.NewEncoder(stdout).Encode(out); err != nil {
		logError("failed to encode response: %v", err)
		return 1
	}
	return 0
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// readGatewayIP reads the gateway/bridge IP from a file, falling back to the default.
func readGatewayIP(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultGatewayIP
	}
	ip := strings.TrimSpace(string(data))
	if ip == "" {
		return defaultGatewayIP
	}
	return ip
}

func main() {
	noncePath := os.Getenv("OCM_NONCE_PATH")
	if noncePath == "" {
		noncePath = defaultNoncePath
	}

	gatewayIPPath := os.Getenv("OCM_GATEWAY_IP_PATH")
	if gatewayIPPath == "" {
		gatewayIPPath = defaultGatewayIPPath
	}

	cfg := runConfig{
		noncePath:     noncePath,
		gatewayIPPath: gatewayIPPath,
		fetcher: &httpSecretFetcher{
			client: &http.Client{Timeout: 10 * time.Second},
		},
	}

	os.Exit(run(os.Stdin, os.Stdout, cfg))
}
