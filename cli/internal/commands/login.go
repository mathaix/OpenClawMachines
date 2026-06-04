package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/mathaix/openclawmachines/cli/internal/browser"
	"github.com/mathaix/openclawmachines/cli/internal/config"
	"github.com/spf13/cobra"
)

var loginToken string

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with the OCM API",
	Long:  "Log in by opening a browser for authentication, or pass --token directly.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if cfg.APIURL == "" {
			return fmt.Errorf("API URL not configured — run 'ocm config set-api-url <url>' first")
		}

		var token string

		if loginToken != "" {
			// Non-interactive: token provided via flag
			token = strings.TrimSpace(loginToken)
			if token == "" {
				return fmt.Errorf("token cannot be empty")
			}
		} else {
			// Browser-based flow
			var err error
			token, err = browserLogin()
			if err != nil {
				return err
			}
		}

		return validateAndSave(token)
	},
}

// browserLogin starts a local callback server, opens the browser for CF Access
// authentication, and waits for the token callback.
func browserLogin() (string, error) {
	// Start local server on a random port
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return "", fmt.Errorf("starting local server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	type result struct {
		token string
		err   error
	}
	done := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, callbackHTML("Login Failed", "No token received. Please try again."))
			done <- result{err: fmt.Errorf("callback received without token")}
			return
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, callbackHTML("Login Successful!", "You can close this tab and return to your terminal."))
		done <- result{token: token}
	})

	server := &http.Server{Handler: mux}
	go func() {
		if err := server.Serve(listener); err != http.ErrServerClosed {
			done <- result{err: fmt.Errorf("local server error: %w", err)}
		}
	}()

	// Build the auth URL
	authURL := buildAuthURL(port)

	// Try to open browser
	fmt.Printf("Opening browser for authentication...\n")
	if err := browser.Open(authURL); err != nil {
		fmt.Printf("Could not open browser automatically.\n")
	}
	fmt.Printf("If the browser didn't open, visit this URL:\n\n  %s\n\nWaiting for authentication...\n", authURL)

	// Wait for callback or timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		res = result{err: fmt.Errorf("login timed out after 2 minutes")}
	}

	// Shut down the local server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)

	if res.err != nil {
		return "", res.err
	}
	return res.token, nil
}

// buildAuthURL constructs the frontend URL for CLI authentication.
func buildAuthURL(port int) string {
	// Use FrontendURL from platform config if available
	frontendURL := cfg.GetFrontendURL()
	if frontendURL != "" {
		return fmt.Sprintf("%s/cli-auth?port=%d", strings.TrimRight(frontendURL, "/"), port)
	}

	// Fall back: derive from API URL or CfAppDomain
	appDomain := cfg.CfAppDomain
	if appDomain == "" {
		// Derive from API URL: strip "api." prefix to get the base domain
		// e.g. https://api.openclawmachines.com → https://openclawmachines.com
		appDomain = strings.Replace(cfg.APIURL, "://api.", "://", 1)
		// Strip trailing /api if present
		appDomain = strings.TrimSuffix(appDomain, "/api")
	}
	// Ensure https:// prefix
	if !strings.HasPrefix(appDomain, "http") {
		appDomain = "https://" + appDomain
	}
	return fmt.Sprintf("%s/cli-auth?port=%d", appDomain, port)
}

// validateAndSave validates a token and saves it with the default account.
func validateAndSave(token string) error {
	client := api.NewClient(cfg.APIURL, token)
	resp, err := client.Get("/api/auth/me")
	if err != nil {
		return fmt.Errorf("validating token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("authentication failed (%d): %s", resp.StatusCode, string(body))
	}

	var meResp struct {
		User api.User `json:"user"`
	}
	if err := json.Unmarshal(body, &meResp); err != nil {
		return fmt.Errorf("parsing auth response: %w", err)
	}

	// Save token
	cfg.Token = token
	if err := config.Save(cfg, cfgFile); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Printf("Authenticated as %s\n", meResp.User.Email)

	// Fetch default account
	resp2, err := client.Get("/api/accounts")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not fetch accounts: %v\n", err)
		fmt.Fprintln(os.Stderr, "Login succeeded but default account is not set. Run 'ocm login' again to retry.")
		return nil
	}
	defer resp2.Body.Close()

	body2, err := io.ReadAll(resp2.Body)
	if err != nil || resp2.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "Warning: could not fetch accounts (status %d)\n", resp2.StatusCode)
		fmt.Fprintln(os.Stderr, "Login succeeded but default account is not set. Run 'ocm login' again to retry.")
		return nil
	}

	var accounts []api.Account
	if err := json.Unmarshal(body2, &accounts); err != nil || len(accounts) == 0 {
		fmt.Fprintln(os.Stderr, "Warning: no accounts found. Login succeeded but default account is not set.")
		return nil
	}

	cfg.DefaultAccountID = accounts[0].ID
	cfg.DefaultAccountSlug = accounts[0].Slug
	if err := config.Save(cfg, cfgFile); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Printf("Default account: %s\n", accounts[0].Slug)
	return nil
}

// callbackHTML returns a simple HTML page for the browser callback.
func callbackHTML(title, message string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html><head><title>OCM CLI - %s</title>
<style>
  body { font-family: system-ui, sans-serif; background: #030712; color: #f9fafb;
         display: flex; align-items: center; justify-content: center; min-height: 100vh; margin: 0; }
  .card { text-align: center; max-width: 400px; padding: 2rem; }
  h1 { font-size: 1.5rem; margin-bottom: 0.5rem; }
  p { color: #9ca3af; font-size: 0.875rem; }
</style></head>
<body><div class="card"><h1>%s</h1><p>%s</p></div></body></html>`, title, title, message)
}

// ensureLogin checks auth state and triggers browser login inline if needed.
// Returns nil if already logged in with a valid token and account.
func ensureLogin() error {
	if cfg.APIURL == "" {
		return fmt.Errorf("API URL not configured — run 'ocm config set-api-url <url>'")
	}

	// If we have a token and account, validate it
	if cfg.Token != "" && cfg.DefaultAccountID > 0 {
		client := api.NewClient(cfg.APIURL, cfg.Token)
		resp, err := client.Get("/api/auth/me")
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil // already logged in
			}
		}
		// Token is invalid or expired — fall through to re-authenticate
		fmt.Println("Session expired. Re-authenticating...")
	} else {
		fmt.Println("Not logged in. Starting authentication...")
	}

	// Trigger browser login
	token, err := browserLogin()
	if err != nil {
		return fmt.Errorf("login failed: %w\nRun 'ocm login' manually to authenticate", err)
	}

	return validateAndSave(token)
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Clear stored authentication token",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg.Token = ""
		cfg.TokenExpires = ""
		if err := config.Save(cfg, cfgFile); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}
		fmt.Println("Logged out. Token cleared.")
		return nil
	},
}

func init() {
	loginCmd.Flags().StringVar(&loginToken, "token", "", "authenticate directly with a token (skips browser flow)")
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
}
