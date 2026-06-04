package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/spf13/cobra"
)

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Manage machine skills",
	Long:  "List available skills and manage which skills are enabled on a machine.",
}

var skillsAvailableCmd = &cobra.Command{
	Use:   "available",
	Short: "List all available skills in the registry",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		resp, err := client.Get("/api/registry/skills")
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		var entries []api.RegistryEntry
		if err := readJSON(&resp.Body, resp.StatusCode, 200, &entries); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("skills.available", entries)
			return nil
		}

		if len(entries) == 0 {
			fmt.Println("No skills available.")
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

var skillsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List skills enabled on a machine",
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

		var skills []api.MachineCapability
		for _, c := range caps {
			if c.EntryType == "skill" {
				skills = append(skills, c)
			}
		}

		if isJSONOutput(cmd) {
			printJSON("skills.list", skills)
			return nil
		}

		if len(skills) == 0 {
			fmt.Println("No skills configured on this machine.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SKILL\tENABLED")
		for _, s := range skills {
			fmt.Fprintf(w, "%s\t%v\n", s.EntryName, s.Enabled)
		}
		w.Flush()
		return nil
	},
}

var skillsEnableCmd = &cobra.Command{
	Use:               "enable SKILL",
	Short:             "Enable a skill on a machine",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRegistryEntries("/api/registry/skills"),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		skill := args[0]
		name, _ := cmd.Flags().GetString("machine")

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		reqBody, err := json.Marshal(map[string]string{"entry_id": skill})
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
			printJSON("skills.enable", map[string]string{"skill": skill, "machine": machine.Name, "status": "enabled"})
			return nil
		}

		fmt.Printf("Skill %q enabled on machine %s.\n", skill, machine.Name)
		return nil
	},
}

var skillsDisableCmd = &cobra.Command{
	Use:               "disable SKILL",
	Short:             "Disable a skill on a machine",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRegistryEntries("/api/registry/skills"),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		skill := args[0]
		name, _ := cmd.Flags().GetString("machine")

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/capabilities/%s", cfg.DefaultAccountID, machine.ID, skill)
		resp, err := client.Delete(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if err := readJSON(&resp.Body, resp.StatusCode, 204, nil); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("skills.disable", map[string]string{"skill": skill, "machine": machine.Name, "status": "disabled"})
			return nil
		}

		fmt.Printf("Skill %q disabled on machine %s.\n", skill, machine.Name)
		return nil
	},
}

func init() {
	skillsListCmd.Flags().String("machine", "", "machine name")
	skillsEnableCmd.Flags().String("machine", "", "machine name")
	skillsDisableCmd.Flags().String("machine", "", "machine name")

	registerMachineFlagCompletion(skillsListCmd)
	registerMachineFlagCompletion(skillsEnableCmd)
	registerMachineFlagCompletion(skillsDisableCmd)

	skillsCmd.AddCommand(skillsAvailableCmd)
	skillsCmd.AddCommand(skillsListCmd)
	skillsCmd.AddCommand(skillsEnableCmd)
	skillsCmd.AddCommand(skillsDisableCmd)

	rootCmd.AddCommand(skillsCmd)
}
