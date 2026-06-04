package commands

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

var identityCmd = &cobra.Command{
	Use:   "identity",
	Short: "Manage machine identity",
	Long:  "Set and view the identity (name and avatar) for a machine.",
}

var identitySetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set the identity for a machine",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		machineName, _ := cmd.Flags().GetString("machine")
		name, _ := cmd.Flags().GetString("name")
		avatar, _ := cmd.Flags().GetString("avatar")

		if name == "" && avatar == "" {
			return fmt.Errorf("at least one of --name or --avatar is required")
		}

		client := newAPIClient()
		machine, err := resolveMachine(client, machineName)
		if err != nil {
			return err
		}

		body := map[string]string{}
		if name != "" {
			body["name"] = name
		}
		if avatar != "" {
			body["avatar"] = avatar
		}
		reqBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/identity", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Put(path, reqBody)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if err := readJSON(&resp.Body, resp.StatusCode, 200, nil); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			result := map[string]string{"machine": machine.Name, "status": "updated"}
			if name != "" {
				result["name"] = name
			}
			if avatar != "" {
				result["avatar"] = avatar
			}
			printJSON("identity.set", result)
			return nil
		}

		fmt.Printf("Identity updated for machine %s.\n", machine.Name)
		return nil
	},
}

var identityShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the identity for a machine",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		machineName, _ := cmd.Flags().GetString("machine")

		client := newAPIClient()
		machine, err := resolveMachine(client, machineName)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/assembled-config", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading response: %w", err)
		}
		if resp.StatusCode != 200 {
			return apiError(resp.StatusCode, string(body))
		}

		// The assembled-config response may be raw JSON or wrapped in {config, warnings}.
		// Extract identity from ui.assistant in the config.
		identityName, avatar := extractIdentityFromConfig(body)

		if isJSONOutput(cmd) {
			printJSON("identity.show", map[string]string{
				"machine": machine.Name,
				"name":    identityName,
				"avatar":  avatar,
			})
			return nil
		}

		fmt.Printf("Machine:  %s\n", machine.Name)
		fmt.Printf("Name:     %s\n", identityName)
		fmt.Printf("Avatar:   %s\n", avatar)
		return nil
	},
}

// extractIdentityFromConfig extracts ui.assistant.name and ui.assistant.avatar
// from an assembled config JSON response. The response may be raw config JSON
// or wrapped in {"config": ..., "warnings": ...}.
func extractIdentityFromConfig(body []byte) (name, avatar string) {
	name = "-"
	avatar = "-"

	// Try parsing as wrapped response first: {"config": {...}, "warnings": [...]}
	var wrapped struct {
		Config json.RawMessage `json:"config"`
	}
	configJSON := body
	if err := json.Unmarshal(body, &wrapped); err == nil && len(wrapped.Config) > 0 {
		configJSON = wrapped.Config
	}

	// Extract ui.assistant from the config
	var config struct {
		UI struct {
			Assistant struct {
				Name   string `json:"name"`
				Avatar string `json:"avatar"`
			} `json:"assistant"`
		} `json:"ui"`
	}
	if err := json.Unmarshal(configJSON, &config); err == nil {
		if config.UI.Assistant.Name != "" {
			name = config.UI.Assistant.Name
		}
		if config.UI.Assistant.Avatar != "" {
			avatar = config.UI.Assistant.Avatar
		}
	}
	return name, avatar
}

func init() {
	identitySetCmd.Flags().String("machine", "", "machine name")
	identitySetCmd.Flags().String("name", "", "identity name")
	identitySetCmd.Flags().String("avatar", "", "identity avatar URL")
	identityShowCmd.Flags().String("machine", "", "machine name")

	registerMachineFlagCompletion(identitySetCmd)
	registerMachineFlagCompletion(identityShowCmd)

	identityCmd.AddCommand(identitySetCmd)
	identityCmd.AddCommand(identityShowCmd)

	rootCmd.AddCommand(identityCmd)
}
