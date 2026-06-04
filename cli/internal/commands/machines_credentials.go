package commands

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/spf13/cobra"
)

var machineCredsCmd = &cobra.Command{
	Use:   "credentials",
	Short: "List credentials on a machine",
	Long:  "List provider credentials configured on a specific machine.",
}

var machineCredsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List credentials on a machine",
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

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/credentials", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		var creds []api.Credential
		if err := readJSON(&resp.Body, resp.StatusCode, 200, &creds); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("machines.credentials.list", creds)
			return nil
		}

		if len(creds) == 0 {
			fmt.Printf("No credentials on machine %s.\n", machine.Name)
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PROVIDER\tTYPE\tLABEL\tLAST FOUR\tSTATUS")
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
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Provider, credType, c.Label, lastFour, status)
		}
		w.Flush()
		return nil
	},
}

func init() {
	machineCredsListCmd.Flags().String("machine", "", "machine name")
	registerMachineFlagCompletion(machineCredsListCmd)

	machineCredsCmd.AddCommand(machineCredsListCmd)
	machinesCmd.AddCommand(machineCredsCmd)
}
