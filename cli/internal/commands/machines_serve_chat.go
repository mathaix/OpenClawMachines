package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/spf13/cobra"
)

type machineTokenResp struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	Hostname  string `json:"hostname"`
}

func fetchMachineToken(client *api.Client, accountID int, machineID string) (*machineTokenResp, error) {
	path := fmt.Sprintf("/api/accounts/%d/machines/%s/token", accountID, machineID)
	resp, err := client.Get(path)
	if err != nil {
		return nil, fmt.Errorf("fetching machine token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetching machine token: %s", apiError(resp.StatusCode, string(body)))
	}
	var out machineTokenResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding machine token response: %w", err)
	}
	return &out, nil
}

func newEphemeralKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-ocm_" + hex.EncodeToString(b), nil
}

type serveChatConfig struct {
	upstreamBase string                 // e.g. "https://m-foo.openclawmachines.com"
	machineToken func() (string, error) // returns the current machine JWT; refreshes when close to expiry
	apiKey       string                 // ephemeral inbound key; empty disables inbound auth
}

// tokenRefresher holds a cached machine JWT and re-fetches it from the control
// plane when close to expiry. The backend issues tokens with a 5-minute TTL,
// so a long-running serve chat session has to refresh in-flight or every
// request after the first few minutes will 401 at the auth proxy.
type tokenRefresher struct {
	mu        sync.Mutex
	fetch     func() (*machineTokenResp, error)
	token     string
	expiresAt time.Time
	// refreshSkew is the buffer before expiresAt at which a token is considered
	// stale and must be refreshed on the next Get call.
	refreshSkew time.Duration
}

func newTokenRefresher(initial *machineTokenResp, fetch func() (*machineTokenResp, error)) *tokenRefresher {
	tr := &tokenRefresher{
		fetch:       fetch,
		refreshSkew: 30 * time.Second,
	}
	tr.adopt(initial)
	return tr
}

// adopt stores a freshly-fetched token and parses its expiry; must be called
// with mu held by the caller (or during construction before concurrent use).
func (tr *tokenRefresher) adopt(resp *machineTokenResp) {
	tr.token = resp.Token
	if exp, err := time.Parse(time.RFC3339, resp.ExpiresAt); err == nil {
		tr.expiresAt = exp
	} else {
		// Unparseable expiry — assume backend's documented 5-minute TTL so we
		// still refresh on a reasonable cadence instead of trusting forever.
		tr.expiresAt = time.Now().Add(5 * time.Minute)
	}
}

// Get returns the current valid token, refreshing it if we're within
// refreshSkew of the stored expiry.
func (tr *tokenRefresher) Get() (string, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if time.Until(tr.expiresAt) > tr.refreshSkew {
		return tr.token, nil
	}
	resp, err := tr.fetch()
	if err != nil {
		return "", fmt.Errorf("refreshing machine token: %w", err)
	}
	tr.adopt(resp)
	return tr.token, nil
}

func newServeChatHandler(c serveChatConfig) http.Handler {
	upstream, err := url.Parse(c.upstreamBase)
	if err != nil {
		// Parse errors surface only at construction time; panic here means a
		// caller passed a malformed base — a programming error, not runtime.
		panic(fmt.Sprintf("serve chat: invalid upstream base %q: %v", c.upstreamBase, err))
	}

	proxy := &httputil.ReverseProxy{
		// FlushInterval = -1 flushes after every Write on the ResponseWriter,
		// which gives us per-chunk SSE delivery without buffering.
		FlushInterval: -1,
		Director: func(req *http.Request) {
			req.URL.Scheme = upstream.Scheme
			req.URL.Host = upstream.Host
			// Map /v1/<rest> → /gateway/v1/<rest>
			req.URL.Path = "/gateway" + req.URL.Path
			req.Host = upstream.Host
			// The auth proxy authenticates on X-Machine-Token (set below in
			// the outer handler from a live tokenRefresher); the inbound
			// Bearer token is an ephemeral CLI key, not an upstream credential.
			req.Header.Del("Authorization")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"message":"upstream unreachable","type":"api_error"}}`)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.apiKey != "" {
			authz := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(authz, prefix) || authz[len(prefix):] != c.apiKey {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":{"message":"invalid api key","type":"invalid_request_error"}}`)
				return
			}
		}

		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"only /v1/* paths are served","type":"invalid_request_error"}}`)
			return
		}

		// Fetch (and refresh if needed) the machine JWT per request. Done
		// here rather than in Director so token-refresh failures surface
		// as a clean 502 with a JSON error body instead of a stripped header.
		tok, err := c.machineToken()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"message":"failed to refresh machine token","type":"api_error"}}`)
			return
		}
		r.Header.Set("X-Machine-Token", tok)

		proxy.ServeHTTP(w, r)
	})
}

var machinesServeChatCmd = &cobra.Command{
	Use:   "chat [NAME]",
	Short: "Run a local OpenAI-compatible chat-completions endpoint backed by a machine",
	Long: `Starts a local HTTP server that forwards /v1/chat/completions requests
to the machine's gateway. Point any OpenAI SDK, Open WebUI, Continue.dev, or
similar tool at the printed OPENAI_BASE_URL to use your machine as a provider.

Authentication is scoped to this run: an ephemeral sk-ocm_<hex> API key is
generated on start and printed to stderr. Pass --no-auth to accept unauthenticated
loopback traffic (only safe on single-user machines).`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeMachineNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		port, _ := cmd.Flags().GetInt("port")
		noAuth, _ := cmd.Flags().GetBool("no-auth")

		name := ""
		if len(args) > 0 {
			name = args[0]
		}

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}
		if machine.Status != "running" {
			return fmt.Errorf("machine %q is not running (status: %s)", machine.Name, machine.Status)
		}

		tok, err := fetchMachineToken(client, cfg.DefaultAccountID, machine.ID)
		if err != nil {
			return err
		}
		// Refresh machine JWTs in place for long-running sessions. The backend
		// issues 5-minute tokens; a single bake-in would 401 every request after
		// the first few minutes. fetchMachineToken closure captures client +
		// account + machine so the handler doesn't need them.
		refresher := newTokenRefresher(tok, func() (*machineTokenResp, error) {
			return fetchMachineToken(client, cfg.DefaultAccountID, machine.ID)
		})

		apiKey := ""
		if !noAuth {
			apiKey, err = newEphemeralKey()
			if err != nil {
				return fmt.Errorf("generating API key: %w", err)
			}
		}

		fmt.Fprintf(os.Stderr, "\nServing chat-completions for machine %q via %s\n", machine.Name, tok.Hostname)
		fmt.Fprintf(os.Stderr, "Local endpoint: http://localhost:%d/v1\n\n", port)
		if apiKey != "" {
			fmt.Fprintf(os.Stderr, "  export OPENAI_BASE_URL=http://localhost:%d/v1\n", port)
			fmt.Fprintf(os.Stderr, "  export OPENAI_API_KEY=%s\n\n", apiKey)
		} else {
			fmt.Fprintf(os.Stderr, "  export OPENAI_BASE_URL=http://localhost:%d/v1\n", port)
			fmt.Fprintf(os.Stderr, "  (auth disabled — clients may send any OPENAI_API_KEY)\n\n")
		}

		handler := newServeChatHandler(serveChatConfig{
			upstreamBase: "https://" + tok.Hostname,
			machineToken: refresher.Get,
			apiKey:       apiKey,
		})

		addr := fmt.Sprintf("127.0.0.1:%d", port)
		srv := &http.Server{Addr: addr, Handler: handler}

		errCh := make(chan error, 1)
		go func() { errCh <- srv.ListenAndServe() }()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

		select {
		case <-sigCh:
			fmt.Fprintf(os.Stderr, "\nShutting down...\n")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
			return nil
		case err := <-errCh:
			if err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("local server: %w", err)
			}
			return nil
		}
	},
}

func init() {
	machinesServeChatCmd.Flags().Int("port", 8787, "Local port to bind (loopback only)")
	machinesServeChatCmd.Flags().Bool("no-auth", false, "Accept unauthenticated loopback requests (no inbound API key check)")
	machinesServeCmd.AddCommand(machinesServeChatCmd)
}
