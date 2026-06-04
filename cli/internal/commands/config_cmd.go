package commands

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/mathaix/openclawmachines/cli/internal/config"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage CLI configuration",
	Long:  "View and update CLI configuration such as API URL, default account, and authentication token.",
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		if isJSONOutput(cmd) {
			printJSON("config.show", map[string]any{
				"api_url":      cfg.APIURL,
				"account_id":   cfg.DefaultAccountID,
				"account_slug": cfg.DefaultAccountSlug,
			})
			return nil
		}

		token := "not set"
		if cfg.Token != "" {
			if len(cfg.Token) > 10 {
				token = cfg.Token[:10] + "..."
			} else {
				token = cfg.Token
			}
		}

		account := "not set"
		if cfg.DefaultAccountSlug != "" {
			account = fmt.Sprintf("%s (ID: %d)", cfg.DefaultAccountSlug, cfg.DefaultAccountID)
		}

		machine := "not set"
		if cfg.DefaultMachineName != "" {
			machine = cfg.DefaultMachineName
		}

		fmt.Printf("API URL:  %s\n", valueOrDefault(cfg.APIURL, "not set"))
		fmt.Printf("Token:    %s\n", token)
		fmt.Printf("Account:  %s\n", account)
		fmt.Printf("Machine:  %s\n", machine)
		return nil
	},
}

var configSetAccountCmd = &cobra.Command{
	Use:   "set-account SLUG",
	Short: "Set the default account by slug",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug := args[0]

		if cfg.Token == "" {
			return fmt.Errorf("not logged in — run 'ocm login' first")
		}

		client := api.NewClient(cfg.APIURL, cfg.Token)
		resp, err := client.Get("/api/accounts")
		if err != nil {
			return fmt.Errorf("fetching accounts: %w", err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading response: %w", err)
		}
		if resp.StatusCode != 200 {
			return apiError(resp.StatusCode, string(body))
		}

		var accounts []api.Account
		if err := json.Unmarshal(body, &accounts); err != nil {
			return fmt.Errorf("parsing accounts: %w", err)
		}

		for _, a := range accounts {
			if a.Slug == slug {
				cfg.DefaultAccountID = a.ID
				cfg.DefaultAccountSlug = a.Slug
				if err := config.Save(cfg, cfgFile); err != nil {
					return fmt.Errorf("saving config: %w", err)
				}
				fmt.Printf("Default account set to %s (ID: %d)\n", a.Slug, a.ID)
				return nil
			}
		}

		return fmt.Errorf("account %q not found — available accounts:", slug)
	},
}

var configSetAPIURLCmd = &cobra.Command{
	Use:   "set-api-url URL",
	Short: "Set the API base URL",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg.APIURL = args[0]
		if err := config.Save(cfg, cfgFile); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}
		fmt.Printf("API URL set to %s\n", cfg.APIURL)
		return nil
	},
}

var configMachineShowCmd = &cobra.Command{
	Use:   "machine-show",
	Short: "Show the assembled machine configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("machine")

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/assembled-config", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading response: %w", err)
		}
		if resp.StatusCode != 200 {
			return apiError(resp.StatusCode, string(respBody))
		}

		var raw json.RawMessage
		if err := json.Unmarshal(respBody, &raw); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		if isJSONOutput(cmd) {
			printJSON("config.machine-show", raw)
			return nil
		}

		pretty, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return fmt.Errorf("formatting response: %w", err)
		}
		fmt.Println(string(pretty))
		return nil
	},
}

var configPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Push configuration to a running machine",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("machine")

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/config/push", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Post(path, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if err := readJSON(&resp.Body, resp.StatusCode, 200, nil); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("config.push", map[string]string{"machine": machine.Name, "status": "pushed"})
			return nil
		}

		fmt.Printf("Configuration pushed to machine %s.\n", machine.Name)
		return nil
	},
}

func valueOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func init() {
	configMachineShowCmd.Flags().String("machine", "", "machine name")
	configPushCmd.Flags().String("machine", "", "machine name")

	registerMachineFlagCompletion(configMachineShowCmd)
	registerMachineFlagCompletion(configPushCmd)

	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configSetAccountCmd)
	configCmd.AddCommand(configSetAPIURLCmd)
	configCmd.AddCommand(configMachineShowCmd)
	configCmd.AddCommand(configPushCmd)
	rootCmd.AddCommand(configCmd)
}
