package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/spf13/cobra"
)

var toolsCmd = &cobra.Command{
	Use:   "tools",
	Short: "Manage machine tools",
	Long:  "List available tools and manage which tools are enabled on a machine.",
}

var toolsAvailableCmd = &cobra.Command{
	Use:   "available",
	Short: "List all available tools in the registry",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		resp, err := client.Get("/api/registry/tools")
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		var entries []api.RegistryEntry
		if err := readJSON(&resp.Body, resp.StatusCode, 200, &entries); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("tools.available", entries)
			return nil
		}

		if len(entries) == 0 {
			fmt.Println("No tools available.")
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

var toolsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tools enabled on a machine",
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

		var tools []api.MachineCapability
		for _, c := range caps {
			if c.EntryType == "tool" {
				tools = append(tools, c)
			}
		}

		if isJSONOutput(cmd) {
			printJSON("tools.list", tools)
			return nil
		}

		if len(tools) == 0 {
			fmt.Println("No tools configured on this machine.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TOOL\tMODE\tENABLED")
		for _, t := range tools {
			mode := "-"
			if t.Mode != nil {
				mode = *t.Mode
			}
			fmt.Fprintf(w, "%s\t%s\t%v\n", t.EntryName, mode, t.Enabled)
		}
		w.Flush()
		return nil
	},
}

var toolsEnableCmd = &cobra.Command{
	Use:               "enable TOOL",
	Short:             "Enable a tool on a machine",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRegistryEntries("/api/registry/tools"),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		tool := args[0]
		name, _ := cmd.Flags().GetString("machine")
		mode, _ := cmd.Flags().GetString("mode")

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		body := map[string]string{"entry_id": tool}
		if mode != "" {
			body["mode"] = mode
		}
		reqBody, err := json.Marshal(body)
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
			result := map[string]string{"tool": tool, "machine": machine.Name, "status": "enabled"}
			if mode != "" {
				result["mode"] = mode
			}
			printJSON("tools.enable", result)
			return nil
		}

		if mode != "" {
			fmt.Printf("Tool %q enabled on machine %s (mode: %s).\n", tool, machine.Name, mode)
		} else {
			fmt.Printf("Tool %q enabled on machine %s.\n", tool, machine.Name)
		}
		return nil
	},
}

var toolsDisableCmd = &cobra.Command{
	Use:               "disable TOOL",
	Short:             "Disable a tool on a machine",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRegistryEntries("/api/registry/tools"),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		tool := args[0]
		name, _ := cmd.Flags().GetString("machine")

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/capabilities/%s", cfg.DefaultAccountID, machine.ID, tool)
		resp, err := client.Delete(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if err := readJSON(&resp.Body, resp.StatusCode, 204, nil); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("tools.disable", map[string]string{"tool": tool, "machine": machine.Name, "status": "disabled"})
			return nil
		}

		fmt.Printf("Tool %q disabled on machine %s.\n", tool, machine.Name)
		return nil
	},
}

func init() {
	toolsListCmd.Flags().String("machine", "", "machine name")
	toolsEnableCmd.Flags().String("machine", "", "machine name")
	toolsEnableCmd.Flags().String("mode", "", "tool mode (optional)")
	toolsDisableCmd.Flags().String("machine", "", "machine name")

	registerMachineFlagCompletion(toolsListCmd)
	registerMachineFlagCompletion(toolsEnableCmd)
	registerMachineFlagCompletion(toolsDisableCmd)

	toolsCmd.AddCommand(toolsAvailableCmd)
	toolsCmd.AddCommand(toolsListCmd)
	toolsCmd.AddCommand(toolsEnableCmd)
	toolsCmd.AddCommand(toolsDisableCmd)

	rootCmd.AddCommand(toolsCmd)
}
