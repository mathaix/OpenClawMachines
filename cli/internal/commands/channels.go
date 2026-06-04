package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/spf13/cobra"
)

var channelsCmd = &cobra.Command{
	Use:   "channels",
	Short: "Manage machine channels",
	Long:  "List available channels and manage which channels are enabled on a machine.",
}

var channelsAvailableCmd = &cobra.Command{
	Use:   "available",
	Short: "List all available channels in the registry",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		resp, err := client.Get("/api/registry/channels")
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		var entries []api.RegistryEntry
		if err := readJSON(&resp.Body, resp.StatusCode, 200, &entries); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("channels.available", entries)
			return nil
		}

		if len(entries) == 0 {
			fmt.Println("No channels available.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tDESCRIPTION\tTIER\tSTATUS")
		for _, e := range entries {
			desc := "-"
			if e.Description != nil {
				desc = *e.Description
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Name, desc, e.Tier, e.Status)
		}
		w.Flush()
		return nil
	},
}

var channelsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List channels enabled on a machine",
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

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/capabilities", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		var caps []api.MachineCapability
		if err := readJSON(&resp.Body, resp.StatusCode, 200, &caps); err != nil {
			return err
		}

		// Filter to channels only
		var channels []api.MachineCapability
		for _, c := range caps {
			if c.EntryType == "channel" {
				channels = append(channels, c)
			}
		}

		if isJSONOutput(cmd) {
			printJSON("channels.list", channels)
			return nil
		}

		if len(channels) == 0 {
			fmt.Println("No channels configured on this machine.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "CHANNEL\tMODE\tENABLED")
		for _, c := range channels {
			mode := "-"
			if c.Mode != nil {
				mode = *c.Mode
			}
			fmt.Fprintf(w, "%s\t%s\t%v\n", c.EntryName, mode, c.Enabled)
		}
		w.Flush()
		return nil
	},
}

var channelsEnableCmd = &cobra.Command{
	Use:               "enable CHANNEL",
	Short:             "Enable a channel on a machine",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRegistryEntries("/api/registry/channels"),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		channel := args[0]
		name, _ := cmd.Flags().GetString("machine")

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		reqBody, err := json.Marshal(map[string]string{"entry_id": channel})
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/capabilities", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Post(path, reqBody)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if err := readJSON(&resp.Body, resp.StatusCode, 201, nil); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("channels.enable", map[string]string{"channel": channel, "machine": machine.Name, "status": "enabled"})
			return nil
		}

		fmt.Printf("Channel %q enabled on machine %s.\n", channel, machine.Name)
		return nil
	},
}

var channelsDisableCmd = &cobra.Command{
	Use:               "disable CHANNEL",
	Short:             "Disable a channel on a machine",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRegistryEntries("/api/registry/channels"),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		channel := args[0]
		name, _ := cmd.Flags().GetString("machine")

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/capabilities/%s", cfg.DefaultAccountID, machine.ID, channel)
		resp, err := client.Delete(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if err := readJSON(&resp.Body, resp.StatusCode, 204, nil); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("channels.disable", map[string]string{"channel": channel, "machine": machine.Name, "status": "disabled"})
			return nil
		}

		fmt.Printf("Channel %q disabled on machine %s.\n", channel, machine.Name)
		return nil
	},
}

var channelsConfigureCmd = &cobra.Command{
	Use:   "configure CHANNEL KEY VALUE",
	Short: "Set a configuration override on a channel",
	Args:  cobra.ExactArgs(3),
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return completeRegistryEntries("/api/registry/channels")(cmd, args, toComplete)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		channel := args[0]
		key := args[1]
		value := args[2]
		name, _ := cmd.Flags().GetString("machine")

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		reqBody, err := json.Marshal(map[string]any{
			"config_overrides": map[string]string{key: value},
		})
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/capabilities/%s", cfg.DefaultAccountID, machine.ID, channel)
		resp, err := client.Put(path, reqBody)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if err := readJSON(&resp.Body, resp.StatusCode, 200, nil); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("channels.configure", map[string]string{"channel": channel, "machine": machine.Name, "key": key, "value": value})
			return nil
		}

		fmt.Printf("Channel %q configured: %s=%s\n", channel, key, value)
		return nil
	},
}

func init() {
	channelsListCmd.Flags().String("machine", "", "machine name")
	channelsEnableCmd.Flags().String("machine", "", "machine name")
	channelsDisableCmd.Flags().String("machine", "", "machine name")
	channelsConfigureCmd.Flags().String("machine", "", "machine name")

	registerMachineFlagCompletion(channelsListCmd)
	registerMachineFlagCompletion(channelsEnableCmd)
	registerMachineFlagCompletion(channelsDisableCmd)
	registerMachineFlagCompletion(channelsConfigureCmd)

	channelsCmd.AddCommand(channelsAvailableCmd)
	channelsCmd.AddCommand(channelsListCmd)
	channelsCmd.AddCommand(channelsEnableCmd)
	channelsCmd.AddCommand(channelsDisableCmd)
	channelsCmd.AddCommand(channelsConfigureCmd)

	rootCmd.AddCommand(channelsCmd)
}
