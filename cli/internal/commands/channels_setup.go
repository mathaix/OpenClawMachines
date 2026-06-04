package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/spf13/cobra"
)

var channelInstructions = map[string]struct {
	DisplayName string
	TokenURL    string
	Steps       []string
	Provider    string
}{
	"telegram": {
		DisplayName: "Telegram Bot",
		TokenURL:    "https://t.me/BotFather",
		Steps:       []string{"Message @BotFather on Telegram", "Use /newbot to create a bot", "Copy the token"},
		Provider:    "telegram",
	},
	"discord": {
		DisplayName: "Discord Bot",
		TokenURL:    "https://discord.com/developers/applications",
		Steps:       []string{"Go to discord.com/developers/applications", "Select your application", "Go to Bot → Copy token"},
		Provider:    "discord",
	},
	"whatsapp": {
		DisplayName: "WhatsApp Business",
		TokenURL:    "https://developers.facebook.com/apps",
		Steps:       []string{"Go to developers.facebook.com/apps", "Select your app → WhatsApp → API Setup", "Copy the access token"},
		Provider:    "whatsapp",
	},
	"slack": {
		DisplayName: "Slack Bot",
		TokenURL:    "https://api.slack.com/apps",
		Steps:       []string{"Go to api.slack.com/apps → Create New App → From an app manifest (not \"From scratch\")", "Paste the manifest from docs — it enables Socket Mode and sets required scopes", "Install to Workspace, copy Bot Token (xoxb-...) from OAuth & Permissions", "Go to Basic Information → App-Level Tokens → Generate Token with connections:write scope (xapp-...)"},
		Provider:    "slack",
	},
}

var channelsSetupCmd = &cobra.Command{
	Use:   "setup CHANNEL",
	Short: "Interactive setup wizard for a channel",
	Long:  "Walk through adding a credential, linking it to a machine, and enabling the channel.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}

		channel := strings.ToLower(args[0])
		info, ok := channelInstructions[channel]
		if !ok {
			supported := make([]string, 0, len(channelInstructions))
			for k := range channelInstructions {
				supported = append(supported, k)
			}
			sort.Strings(supported)
			return fmt.Errorf("unsupported channel %q: must be one of %s", channel, strings.Join(supported, ", "))
		}

		machineName, _ := cmd.Flags().GetString("machine")

		jsonMode := isJSONOutput(cmd)

		// Step 1: Show instructions (human mode only)
		if !jsonMode {
			fmt.Printf("\n  %s Setup\n\n", info.DisplayName)
			fmt.Printf("  Get your bot token from: %s\n\n", info.TokenURL)
			for i, step := range info.Steps {
				fmt.Printf("  %d. %s\n", i+1, step)
			}
			fmt.Println()
		}

		// Step 2: Read token
		token, err := readSecretInput(cmd, "Enter bot token: ", "token-from-stdin")
		if err != nil {
			return err
		}

		// Step 3: Validate token locally
		if !jsonMode {
			fmt.Print("  Validating token... ")
		}
		if err := validateCredential(info.Provider, token); err != nil {
			if !jsonMode {
				fmt.Println("failed")
			}
			return fmt.Errorf("token validation failed: %w", err)
		}
		if !jsonMode {
			fmt.Println("ok")
		}

		// Step 3b: Read and validate app token (Slack dual-token)
		var appToken string
		if channel == "slack" {
			appToken, err = readSecretInput(cmd, "Enter app-level token (xapp-...): ", "app-token-from-stdin")
			if err != nil {
				return err
			}
			if !jsonMode {
				fmt.Print("  Validating app token... ")
			}
			if err := validateSlackAppToken(appToken); err != nil {
				if !jsonMode {
					fmt.Println("failed")
				}
				return fmt.Errorf("app token validation failed: %w", err)
			}
			if !jsonMode {
				fmt.Println("ok")
			}
		}

		// Step 4: Read label
		label, _ := cmd.Flags().GetString("label")
		if label == "" {
			if jsonMode {
				label = "CLI setup"
			} else {
				label, err = promptString("Enter label: ")
				if err != nil {
					return err
				}
				if label == "" {
					return fmt.Errorf("label cannot be empty")
				}
			}
		}

		// Step 4: Determine credential type
		credType := inferCredentialType(info.Provider)

		client := newAPIClient()

		// Step 5: Resolve machine (validate before storing credential)
		machine, err := resolveMachine(client, machineName)
		if err != nil {
			return err
		}

		// Step 6: Save credential
		credReqBody, err := json.Marshal(map[string]string{
			"value":           token,
			"label":           label,
			"credential_type": credType,
		})
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}

		credPath := fmt.Sprintf("/api/accounts/%d/credentials/%s", cfg.DefaultAccountID, info.Provider)
		credResp, err := client.Put(credPath, credReqBody)
		if err != nil {
			return err
		}
		defer credResp.Body.Close()

		var cred api.Credential
		if err := readJSON(&credResp.Body, credResp.StatusCode, 200, &cred); err != nil {
			return err
		}

		if !jsonMode {
			fmt.Printf("  Credential added (ID: %d)\n", cred.ID)
		}

		// Step 6b: Save app token credential (Slack dual-token)
		var appCred api.Credential
		if appToken != "" {
			appCredReqBody, err := json.Marshal(map[string]string{
				"value":           appToken,
				"label":           label + " (app token)",
				"credential_type": "token",
			})
			if err != nil {
				return fmt.Errorf("encoding app token request: %w", err)
			}

			appCredPath := fmt.Sprintf("/api/accounts/%d/credentials/%s", cfg.DefaultAccountID, info.Provider+"-app")
			appCredResp, err := client.Put(appCredPath, appCredReqBody)
			if err != nil {
				return err
			}
			defer appCredResp.Body.Close()

			if err := readJSON(&appCredResp.Body, appCredResp.StatusCode, 200, &appCred); err != nil {
				return err
			}

			if !jsonMode {
				fmt.Printf("  App token credential added (ID: %d)\n", appCred.ID)
			}
		}

		// Step 7: Link credential to machine
		linkPath := fmt.Sprintf("/api/accounts/%d/machines/%s/credentials/%d", cfg.DefaultAccountID, machine.ID, cred.ID)
		linkResp, err := client.Post(linkPath, nil)
		if err != nil {
			return err
		}
		defer linkResp.Body.Close()

		if _, err := io.ReadAll(linkResp.Body); err != nil {
			return fmt.Errorf("reading response: %w", err)
		}
		if linkResp.StatusCode != 200 {
			return apiError(linkResp.StatusCode, "linking credential")
		}

		if !jsonMode {
			fmt.Printf("  Credential linked to machine %s\n", machine.Name)
		}

		// Step 7b: Link app token credential to machine (Slack dual-token)
		if appToken != "" {
			appLinkPath := fmt.Sprintf("/api/accounts/%d/machines/%s/credentials/%d", cfg.DefaultAccountID, machine.ID, appCred.ID)
			appLinkResp, err := client.Post(appLinkPath, nil)
			if err != nil {
				return err
			}
			defer appLinkResp.Body.Close()

			if _, err := io.ReadAll(appLinkResp.Body); err != nil {
				return fmt.Errorf("reading response: %w", err)
			}
			if appLinkResp.StatusCode != 200 {
				return apiError(appLinkResp.StatusCode, "linking app token credential")
			}

			if !jsonMode {
				fmt.Printf("  App token credential linked to machine %s\n", machine.Name)
			}
		}

		// Step 8: Enable channel capability
		capReqBody, err := json.Marshal(map[string]string{
			"entry_id": channel,
		})
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}

		capPath := fmt.Sprintf("/api/accounts/%d/machines/%s/capabilities", cfg.DefaultAccountID, machine.ID)
		capResp, err := client.Post(capPath, capReqBody)
		if err != nil {
			return err
		}
		defer capResp.Body.Close()

		if _, err := io.ReadAll(capResp.Body); err != nil {
			return fmt.Errorf("reading response: %w", err)
		}
		if capResp.StatusCode != 201 {
			return apiError(capResp.StatusCode, "enabling channel")
		}

		if !jsonMode {
			fmt.Printf("  Channel %q enabled\n", channel)
		}

		// Step 9: Push config
		pushPath := fmt.Sprintf("/api/accounts/%d/machines/%s/config/push", cfg.DefaultAccountID, machine.ID)
		pushResp, err := client.Post(pushPath, nil)
		if err != nil {
			return err
		}
		defer pushResp.Body.Close()

		if _, err := io.ReadAll(pushResp.Body); err != nil {
			return fmt.Errorf("reading response: %w", err)
		}
		if pushResp.StatusCode != 200 && pushResp.StatusCode != 204 {
			return apiError(pushResp.StatusCode, "pushing config")
		}

		if !jsonMode {
			fmt.Printf("  Config pushed\n\n")
			fmt.Printf("  %s channel is ready on %s.\n", info.DisplayName, machine.Name)
		}

		if jsonMode {
			printJSON("channels.setup", map[string]any{
				"channel":       channel,
				"credential_id": cred.ID,
				"machine":       machine.Name,
				"steps": []string{
					"credential_added",
					"credential_linked",
					"channel_enabled",
					"config_pushed",
				},
			})
		}

		return nil
	},
}

func init() {
	channelsSetupCmd.Flags().String("machine", "", "machine name")
	channelsSetupCmd.Flags().Bool("token-from-stdin", false, "read bot token from stdin")
	channelsSetupCmd.Flags().Bool("app-token-from-stdin", false, "read app-level token from stdin (Slack)")
	channelsSetupCmd.Flags().String("label", "", "credential label")

	registerMachineFlagCompletion(channelsSetupCmd)

	channelsCmd.AddCommand(channelsSetupCmd)
}
