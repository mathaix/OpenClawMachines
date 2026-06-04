package commands

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/spf13/cobra"
)

var validProviderNames = []string{"anthropic", "openai", "google", "openrouter", "discord", "telegram", "whatsapp", "slack"}

var validCredentialTypes = []string{"api_key", "subscription_key", "token", "oauth"}

// inferCredentialType returns a sensible default credential type for the given provider.
// Channel providers use "token", LLM/unknown providers default to "api_key".
func inferCredentialType(provider string) string {
	switch provider {
	case "telegram", "discord", "whatsapp", "slack":
		return "token"
	default:
		return "api_key"
	}
}

var providersCmd = &cobra.Command{
	Use:   "providers",
	Short: "Manage API provider credentials and custom providers",
	Long:  "List, add, and remove API provider credentials on a machine, and register custom providers for your account.",
}

var providersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List credentials on a machine (use --all for built-in + custom providers)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}

		showAll, _ := cmd.Flags().GetBool("all")

		if showAll {
			// List built-in + custom providers (not credentials) — account-scoped
			client := api.NewClient(cfg.APIURL, cfg.Token)
			path := fmt.Sprintf("/api/accounts/%d/providers", cfg.DefaultAccountID)

			resp, err := client.Get(path)
			if err != nil {
				return fmt.Errorf("listing providers: %w", err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return fmt.Errorf("reading response: %w", err)
			}

			if resp.StatusCode != 200 {
				return apiError(resp.StatusCode, string(body))
			}

			var providers []api.ProviderEntry
			if err := json.Unmarshal(body, &providers); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			if isJSONOutput(cmd) {
				printJSON("providers.list", providers)
				return nil
			}

			if len(providers) == 0 {
				fmt.Println("No providers available.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tUPSTREAM HOST\tAUTH METHOD\tIS LLM")
			for _, p := range providers {
				upstreamHost := "-"
				authMethod := "-"
				isLLM := "-"
				if p.Type == "custom" {
					upstreamHost = p.UpstreamHost
					authMethod = p.AuthMethod
					if p.IsLLM {
						isLLM = "yes"
					} else {
						isLLM = "no"
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Type, upstreamHost, authMethod, isLLM)
			}
			w.Flush()
			return nil
		}

		// Default: list credentials on a machine
		name, _ := cmd.Flags().GetString("machine")
		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/credentials", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Get(path)
		if err != nil {
			return fmt.Errorf("listing credentials: %w", err)
		}
		defer resp.Body.Close()

		var creds []api.Credential
		if err := readJSON(&resp.Body, resp.StatusCode, 200, &creds); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("providers.list", creds)
			return nil
		}

		if len(creds) == 0 {
			fmt.Printf("No provider credentials on machine %s.\n", machine.Name)
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PROVIDER\tTYPE\tLABEL\tLAST FOUR\tSTATUS\tLAST VALIDATED")
		for _, c := range creds {
			credType := c.CredentialType
			if credType == "" {
				credType = "-"
			}
			lastFour := "-"
			if c.LastFour != nil {
				lastFour = "****" + *c.LastFour
			}
			status := c.Status
			if status == "" {
				status = "-"
			}
			lastValidated := "-"
			if c.LastValidated != nil {
				lastValidated = c.LastValidated.Format("2006-01-02 15:04")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", c.Provider, credType, c.Label, lastFour, status, lastValidated)
		}
		w.Flush()
		return nil
	},
}

var providersAddCmd = &cobra.Command{
	Use:               "add PROVIDER",
	Short:             "Add a provider credential to a machine",
	Long:              "Add an API key for a provider on a machine. Valid providers: " + strings.Join(validProviderNames, ", "),
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeProviderNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		provider := strings.ToLower(args[0])

		valid := false
		for _, p := range validProviderNames {
			if p == provider {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid provider %q: must be one of %s", provider, strings.Join(validProviderNames, ", "))
		}

		// Resolve credential type: explicit flag or inferred from provider
		credType, _ := cmd.Flags().GetString("type")
		if credType != "" {
			validType := false
			for _, ct := range validCredentialTypes {
				if ct == credType {
					validType = true
					break
				}
			}
			if !validType {
				return fmt.Errorf("invalid credential type %q: must be one of %s", credType, strings.Join(validCredentialTypes, ", "))
			}
		} else {
			credType = inferCredentialType(provider)
		}

		// Resolve machine
		machineName, _ := cmd.Flags().GetString("machine")
		client := newAPIClient()
		machine, err := resolveMachine(client, machineName)
		if err != nil {
			return err
		}

		key, err := readSecret(fmt.Sprintf("Enter API key for %s: ", provider))
		if err != nil {
			return fmt.Errorf("reading API key: %w", err)
		}

		fmt.Print("Enter label: ")
		reader := bufio.NewReader(os.Stdin)
		label, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading label: %w", err)
		}
		label = strings.TrimSpace(label)
		if label == "" {
			return fmt.Errorf("label cannot be empty")
		}

		reqBody := struct {
			Value          string `json:"value"`
			Label          string `json:"label"`
			CredentialType string `json:"credential_type"`
		}{
			Value:          key,
			Label:          label,
			CredentialType: credType,
		}
		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/credentials/%s", cfg.DefaultAccountID, machine.ID, provider)

		resp, err := client.Put(path, bodyBytes)
		if err != nil {
			return fmt.Errorf("adding credential: %w", err)
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading response: %w", err)
		}

		if resp.StatusCode != 200 {
			return apiError(resp.StatusCode, string(respBody))
		}

		if isJSONOutput(cmd) {
			printJSON("providers.add", map[string]string{"provider": provider, "credential_type": credType, "machine": machine.Name, "status": "added"})
			return nil
		}

		// Show last 4 chars of the key
		masked := key
		if len(key) > 4 {
			masked = strings.Repeat("*", len(key)-4) + key[len(key)-4:]
		}
		fmt.Printf("Credential added for %s on %s (type: %s): %s\n", provider, machine.Name, credType, masked)
		return nil
	},
}

var providersRemoveCmd = &cobra.Command{
	Use:               "remove PROVIDER",
	Short:             "Remove a provider credential from a machine",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeProviderNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		provider := strings.ToLower(args[0])

		machineName, _ := cmd.Flags().GetString("machine")
		client := newAPIClient()
		machine, err := resolveMachine(client, machineName)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/credentials/%s", cfg.DefaultAccountID, machine.ID, provider)

		resp, err := client.Delete(path)
		if err != nil {
			return fmt.Errorf("removing credential: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 204 {
			body, _ := io.ReadAll(resp.Body)
			return apiError(resp.StatusCode, string(body))
		}

		if isJSONOutput(cmd) {
			printJSON("providers.remove", map[string]string{"provider": provider, "machine": machine.Name, "status": "removed"})
			return nil
		}

		fmt.Printf("Credential for %s removed from %s.\n", provider, machine.Name)
		return nil
	},
}

var providersValidateCmd = &cobra.Command{
	Use:               "validate PROVIDER",
	Short:             "Test a provider credential against the provider API",
	Long:              "Test a stored API key by checking it against the provider's API.\nThis confirms the key is still active and updates the last_validated timestamp.",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeProviderNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		provider := strings.ToLower(args[0])

		machineName, _ := cmd.Flags().GetString("machine")
		client := newAPIClient()
		machine, err := resolveMachine(client, machineName)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/credentials/%s/test", cfg.DefaultAccountID, machine.ID, provider)
		resp, err := client.Post(path, nil)
		if err != nil {
			return fmt.Errorf("testing credential: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			return apiError(resp.StatusCode, string(body))
		}

		if isJSONOutput(cmd) {
			printJSON("providers.validate", map[string]string{
				"provider": provider,
				"machine":  machine.Name,
				"status":   "validated",
			})
			return nil
		}

		fmt.Printf("Credential for %s on %s validated successfully.\n", provider, machine.Name)
		return nil
	},
}

var (
	registerUpstreamHost string
	registerAuthMethod   string
	registerScheme       string
	registerAuthHeader   string
	registerPathPrefix   string
	registerIsLLM        bool
)

var providersRegisterCmd = &cobra.Command{
	Use:   "register NAME",
	Short: "Register a custom API provider",
	Long:  "Register a custom API provider with a name, upstream host, and auth method.\nAuth methods: bearer_header, api_key_header, query_param, none",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		name := strings.ToLower(args[0])

		if registerUpstreamHost == "" {
			return fmt.Errorf("--upstream-host is required")
		}
		if registerAuthMethod == "" {
			return fmt.Errorf("--auth-method is required")
		}

		reqBody := struct {
			Name         string  `json:"name"`
			UpstreamHost string  `json:"upstream_host"`
			Scheme       string  `json:"scheme,omitempty"`
			AuthMethod   string  `json:"auth_method"`
			AuthHeader   *string `json:"auth_header,omitempty"`
			PathPrefix   string  `json:"path_prefix,omitempty"`
			IsLLM        bool    `json:"is_llm"`
		}{
			Name:         name,
			UpstreamHost: registerUpstreamHost,
			Scheme:       registerScheme,
			AuthMethod:   registerAuthMethod,
			PathPrefix:   registerPathPrefix,
			IsLLM:        registerIsLLM,
		}
		if registerAuthHeader != "" {
			reqBody.AuthHeader = &registerAuthHeader
		}

		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}

		client := api.NewClient(cfg.APIURL, cfg.Token)
		path := fmt.Sprintf("/api/accounts/%d/providers", cfg.DefaultAccountID)

		resp, err := client.Post(path, bodyBytes)
		if err != nil {
			return fmt.Errorf("registering provider: %w", err)
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading response: %w", err)
		}

		if resp.StatusCode != 201 {
			return apiError(resp.StatusCode, string(respBody))
		}

		var provider api.CustomProvider
		if err := json.Unmarshal(respBody, &provider); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		if isJSONOutput(cmd) {
			printJSON("providers.register", provider)
			return nil
		}

		fmt.Printf("Custom provider %q registered (upstream: %s, auth: %s, is_llm: %v)\n",
			provider.Name, provider.UpstreamHost, provider.AuthMethod, provider.IsLLM)
		return nil
	},
}

var providersUnregisterCmd = &cobra.Command{
	Use:               "unregister NAME",
	Short:             "Remove a custom provider registration",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeCustomProviderNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		name := strings.ToLower(args[0])

		client := api.NewClient(cfg.APIURL, cfg.Token)
		path := fmt.Sprintf("/api/accounts/%d/providers/%s", cfg.DefaultAccountID, name)

		resp, err := client.Delete(path)
		if err != nil {
			return fmt.Errorf("unregistering provider: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 204 {
			body, _ := io.ReadAll(resp.Body)
			return apiError(resp.StatusCode, string(body))
		}

		if isJSONOutput(cmd) {
			printJSON("providers.unregister", map[string]string{"name": name, "status": "unregistered"})
			return nil
		}

		fmt.Printf("Custom provider %q unregistered.\n", name)
		return nil
	},
}

func init() {
	// List flags
	providersListCmd.Flags().Bool("all", false, "Show all providers (built-in + custom) instead of credentials")
	providersListCmd.Flags().String("machine", "", "machine name (uses default if omitted)")
	registerMachineFlagCompletion(providersListCmd)

	// Add flags
	providersAddCmd.Flags().String("type", "", "credential type (api_key, token, oauth); inferred from provider if omitted")
	providersAddCmd.Flags().String("machine", "", "machine name (uses default if omitted)")
	registerMachineFlagCompletion(providersAddCmd)

	// Remove flags
	providersRemoveCmd.Flags().String("machine", "", "machine name (uses default if omitted)")
	registerMachineFlagCompletion(providersRemoveCmd)

	// Validate flags
	providersValidateCmd.Flags().String("machine", "", "machine name (uses default if omitted)")
	registerMachineFlagCompletion(providersValidateCmd)

	// Register flags
	providersRegisterCmd.Flags().StringVar(&registerUpstreamHost, "upstream-host", "", "Upstream host (e.g. api.my-llm.com)")
	providersRegisterCmd.Flags().StringVar(&registerAuthMethod, "auth-method", "", "Auth method: bearer_header, api_key_header, query_param, none")
	providersRegisterCmd.Flags().StringVar(&registerScheme, "scheme", "https", "URL scheme (default: https)")
	providersRegisterCmd.Flags().StringVar(&registerAuthHeader, "auth-header", "", "Custom auth header name (optional)")
	providersRegisterCmd.Flags().StringVar(&registerPathPrefix, "path-prefix", "", "Path prefix for upstream requests (optional)")
	providersRegisterCmd.Flags().BoolVar(&registerIsLLM, "is-llm", false, "Whether this provider should be subject to budget gating")

	providersCmd.AddCommand(providersListCmd)
	providersCmd.AddCommand(providersAddCmd)
	providersCmd.AddCommand(providersRemoveCmd)
	providersCmd.AddCommand(providersValidateCmd)
	providersCmd.AddCommand(providersRegisterCmd)
	providersCmd.AddCommand(providersUnregisterCmd)
	rootCmd.AddCommand(providersCmd)
}
