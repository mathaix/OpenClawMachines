package metadata

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// httpSecretFetcher fetches secrets from the backend API.
type httpSecretFetcher struct {
	backendURL string
	agentToken string
	client     *http.Client
}

func (f *httpSecretFetcher) FetchSecrets(machineID string) (map[string]string, error) {
	url := fmt.Sprintf("%s/api/agent/machines/%s/secrets", f.backendURL, machineID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.agentToken)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch secrets: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("backend returned HTTP %d", resp.StatusCode)
	}

	var secrets map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&secrets); err != nil {
		return nil, fmt.Errorf("decode secrets: %w", err)
	}
	return secrets, nil
}

// NewHTTPSecretFetcher creates a SecretFetcher that pulls secrets from the backend API.
func NewHTTPSecretFetcher(backendURL, agentToken string) SecretFetcher {
	return &httpSecretFetcher{
		backendURL: backendURL,
		agentToken: agentToken,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}
