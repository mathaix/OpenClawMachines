package commands

import (
	"fmt"
	"net/url"

	"github.com/mathaix/openclawmachines/cli/internal/browser"
	"github.com/spf13/cobra"
)

var machinesDashboardCmd = &cobra.Command{
	Use:   "dashboard [NAME]",
	Short: "Open a machine dashboard or webchat UI",
	Long: `Prints the authenticated browser URL for a running machine UI.

Hermes machines open the Hermes dashboard/webchat. OpenClaw machines open the
OpenClaw control UI. Pass --open to launch the URL in your default browser.`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeMachineNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}

		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		openBrowser, _ := cmd.Flags().GetBool("open")

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}
		if machine.Status != "running" {
			return fmt.Errorf("machine %q is not running (status: %s)", machine.Name, machine.Status)
		}

		tok, err := fetchMachineToken(client, cfg.DefaultAccountID, machine.ID)
		if err != nil {
			return err
		}

		uiPath := "gateway/"
		if machine.Kind == "hermes" {
			uiPath = "dashboard/"
		}
		uiURL := (&url.URL{
			Scheme: "https",
			Host:   tok.Hostname,
			Path:   "/" + uiPath,
		}).String()
		uiURL += "?token=" + url.QueryEscape(tok.Token)

		fmt.Println(uiURL)
		if openBrowser {
			if err := browser.Open(uiURL); err != nil {
				return fmt.Errorf("opening browser: %w", err)
			}
		}
		return nil
	},
}

func init() {
	machinesDashboardCmd.Flags().Bool("open", false, "open the URL in the default browser")
	machinesCmd.AddCommand(machinesDashboardCmd)
}
