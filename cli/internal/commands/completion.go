package commands

import (
	"fmt"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/spf13/cobra"
)

// completeMachineNames returns machine names for shell completion.
func completeMachineNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if err := requireLogin(); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	client := newAPIClient()
	machines, err := listMachines(client)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, m := range machines {
		names = append(names, m.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeProviderNames returns the static list of known provider names.
func completeProviderNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return validProviderNames, cobra.ShellCompDirectiveNoFileComp
}

// completeCustomProviderNames returns custom provider names from the API.
func completeCustomProviderNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if err := requireLogin(); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	client := newAPIClient()
	path := fmt.Sprintf("/api/accounts/%d/providers", cfg.DefaultAccountID)
	resp, err := client.Get(path)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer resp.Body.Close()

	var providers []api.ProviderEntry
	if err := readJSON(&resp.Body, resp.StatusCode, 200, &providers); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, p := range providers {
		if p.Type == "custom" {
			names = append(names, p.Name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeRegistryEntries returns entry names from a registry endpoint.
func completeRegistryEntries(endpoint string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		client := newAPIClient()
		resp, err := client.Get(endpoint)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		defer resp.Body.Close()

		var entries []api.RegistryEntry
		if err := readJSON(&resp.Body, resp.StatusCode, 200, &entries); err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// registerMachineFlagCompletion registers shell completion for the --machine flag on the given command.
func registerMachineFlagCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("machine", completeMachineNames)
}
