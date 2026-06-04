package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/spf13/cobra"
)

// subscriptionInfo maps LLM providers to their subscription key details.
// Providers in this map get a "subscription key vs API key" prompt during setup.
// The subscription path instructs the user to run a local CLI command to generate a token.
var subscriptionInfo = map[string]struct {
	PlanNames string // e.g. "Claude Pro / Max / Team"
	AuthName  string // e.g. "Anthropic token" — displayed as auth method choice
	TokenName string // e.g. "setup-token" — the token type name
	TokenCmd  string // e.g. "claude setup-token" — command the user runs locally
}{
	"anthropic": {
		PlanNames: "Claude Pro / Max / Team",
		AuthName:  "Anthropic token",
		TokenName: "setup-token",
		TokenCmd:  "claude setup-token",
	},
	"openai": {
		PlanNames: "ChatGPT Plus / Pro / Team",
		AuthName:  "OpenAI token",
		TokenName: "setup-token",
		TokenCmd:  "chatgpt setup-token",
	},
}

// providerInstructions maps provider names to human-friendly setup instructions.
var providerInstructions = map[string]struct {
	DisplayName string
	KeyURL      string
	Steps       []string
}{
	"anthropic": {
		DisplayName: "Anthropic",
		KeyURL:      "https://console.anthropic.com/settings/keys",
		Steps: []string{
			"Go to console.anthropic.com/settings/keys",
			"Create a new key (works with both API billing and Claude subscription plans)",
			"Copy the key (starts with sk-ant-...)",
		},
	},
	"openai": {
		DisplayName: "OpenAI",
		KeyURL:      "https://platform.openai.com/api-keys",
		Steps: []string{
			"Go to platform.openai.com/api-keys",
			"Create a new secret key (works with both API billing and ChatGPT subscription plans)",
			"Copy the key (starts with sk-...)",
		},
	},
	"google": {
		DisplayName: "Google AI (Gemini)",
		KeyURL:      "https://aistudio.google.com/apikey",
		Steps:       []string{"Go to aistudio.google.com/apikey", "Create an API key", "Copy the key"},
	},
	"openrouter": {
		DisplayName: "OpenRouter",
		KeyURL:      "https://openrouter.ai/settings/keys",
		Steps: []string{
			"Go to openrouter.ai/settings/keys",
			"Create a new API key",
			"Copy the key (starts with sk-or-v1-...)",
		},
	},
	"discord": {
		DisplayName: "Discord Bot",
		KeyURL:      "https://discord.com/developers/applications",
		Steps:       []string{"Go to discord.com/developers/applications", "Select your application", "Go to Bot → Copy token"},
	},
	"telegram": {
		DisplayName: "Telegram Bot",
		KeyURL:      "https://t.me/BotFather",
		Steps:       []string{"Message @BotFather on Telegram", "Use /newbot to create a bot", "Copy the token"},
	},
	"whatsapp": {
		DisplayName: "WhatsApp Business",
		KeyURL:      "https://developers.facebook.com/apps",
		Steps:       []string{"Go to developers.facebook.com/apps", "Select your app → WhatsApp → API Setup", "Copy the access token"},
	},
	"slack": {
		DisplayName: "Slack Bot",
		KeyURL:      "https://api.slack.com/apps",
		Steps:       []string{"Go to api.slack.com/apps → Create New App → From manifest", "Install to Workspace, copy Bot Token (xoxb-...)", "Generate App-Level Token with connections:write scope (xapp-...)"},
	},
}

var providersSetupCmd = &cobra.Command{
	Use:   "setup PROVIDER",
	Short: "Guided setup wizard for a provider credential",
	Long: "Interactive setup wizard that shows instructions for obtaining an API key,\n" +
		"stores the credential, and optionally links it to a machine.\n\n" +
		"Valid providers: " + strings.Join(validProviderNames, ", "),
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}

		provider := strings.ToLower(args[0])

		// 1. Validate provider is in providerInstructions map
		info, ok := providerInstructions[provider]
		if !ok {
			return fmt.Errorf("unknown provider %q: must be one of %s", provider, strings.Join(validProviderNames, ", "))
		}

		jsonMode := isJSONOutput(cmd)

		// 2. Determine credential type — LLM providers support subscription keys
		credType, _ := cmd.Flags().GetString("type")
		keyPrompt := "Enter API key: "

		subInfo, hasSubscription := subscriptionInfo[provider]
		if hasSubscription && credType == "" && !jsonMode {
			fmt.Printf("\n  %s Setup\n\n", info.DisplayName)
			fmt.Println("  How do you want to authenticate?")
			fmt.Printf("  1. %s (paste %s)\n", subInfo.AuthName, subInfo.TokenName)
			fmt.Printf("  2. API key (pay-per-use from %s)\n", info.KeyURL)
			fmt.Println()
			choice, choiceErr := promptString("  Select (1 or 2): ")
			if choiceErr != nil {
				return choiceErr
			}
			switch strings.TrimSpace(choice) {
			case "1", "":
				credType = "subscription_key"
				keyPrompt = fmt.Sprintf("  Paste %s %s: ", info.DisplayName, subInfo.TokenName)
				fmt.Println()
				printTokenBox(subInfo.TokenCmd)
			case "2":
				credType = "api_key"
				keyPrompt = "Enter API key: "
				fmt.Println()
				for i, step := range info.Steps {
					fmt.Printf("  %d. %s\n", i+1, step)
				}
				fmt.Printf("\n  Key URL: %s\n\n", info.KeyURL)
			default:
				return fmt.Errorf("invalid choice %q: enter 1 or 2", choice)
			}
		} else {
			// Providers without subscription option, or JSON mode
			if credType == "" {
				credType = inferCredentialType(provider)
			}
			if !jsonMode {
				fmt.Printf("\n  %s Setup\n\n", info.DisplayName)
				for i, step := range info.Steps {
					fmt.Printf("  %d. %s\n", i+1, step)
				}
				fmt.Printf("\n  Key URL: %s\n\n", info.KeyURL)
			}
		}

		// 3. Read key
		key, err := readSecretInput(cmd, keyPrompt, "key-from-stdin")
		if err != nil {
			return fmt.Errorf("reading key: %w", err)
		}

		// 4. Validate key locally
		if !jsonMode {
			fmt.Print("  Validating key... ")
		}
		if err := validateCredential(provider, key); err != nil {
			if !jsonMode {
				fmt.Println("failed")
			}
			return fmt.Errorf("key validation failed: %w", err)
		}
		if !jsonMode {
			fmt.Println("ok")
		}

		// 5. Read label
		label, _ := cmd.Flags().GetString("label")
		if label == "" {
			if jsonMode {
				label = "CLI setup"
			} else {
				label, err = promptString("Enter label: ")
				if err != nil {
					return fmt.Errorf("reading label: %w", err)
				}
				if label == "" {
					return fmt.Errorf("label cannot be empty")
				}
			}
		}

		// 7. Store credential: PUT /api/accounts/{accountId}/credentials/{provider}
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

		client := newAPIClient()

		// 7. Resolve machine (required — credentials are machine-scoped)
		machineName, _ := cmd.Flags().GetString("machine")
		machine, err := resolveMachine(client, machineName)
		if err != nil {
			return err
		}

		// 8. Store credential on machine
		path := fmt.Sprintf("/api/accounts/%d/machines/%s/credentials/%s", cfg.DefaultAccountID, machine.ID, provider)

		resp, err := client.Put(path, bodyBytes)
		if err != nil {
			return fmt.Errorf("storing credential: %w", err)
		}
		defer resp.Body.Close()

		var cred api.Credential
		if err := readJSON(&resp.Body, resp.StatusCode, 200, &cred); err != nil {
			return err
		}

		// 9. Push config to running machine
		var pushed bool
		pushPath := fmt.Sprintf("/api/accounts/%d/machines/%s/config/push", cfg.DefaultAccountID, machine.ID)
		pushResp, err := client.Post(pushPath, nil)
		if err == nil {
			defer pushResp.Body.Close()
			if pushResp.StatusCode == 200 {
				pushed = true
			}
		}

		// 10. Output result
		if jsonMode {
			result := map[string]any{
				"provider":      provider,
				"credential_id": cred.ID,
				"machine":       machine.Name,
				"pushed":        pushed,
			}
			printJSON("providers.setup", result)
			return nil
		}

		fmt.Printf("Credential added for %s on %s (ID: %d)\n", provider, machine.Name, cred.ID)
		if pushed {
			fmt.Println("Configuration pushed.")
		}

		return nil
	},
}

func init() {
	providersSetupCmd.Flags().String("machine", "", "machine name to link the credential to")
	providersSetupCmd.Flags().Bool("key-from-stdin", false, "read key from stdin instead of interactive prompt")
	providersSetupCmd.Flags().String("label", "", "label for the credential")
	providersSetupCmd.Flags().String("type", "", "credential type (subscription_key, api_key); prompted if omitted for Anthropic")

	registerMachineFlagCompletion(providersSetupCmd)

	providersCmd.AddCommand(providersSetupCmd)
}
