package commands

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/spf13/cobra"
)

var usageCmd = &cobra.Command{
	Use:               "usage [NAME]",
	Short:             "Show LLM usage",
	Long:              "Display LLM usage records for your account or a specific machine.",
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeMachineNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		machineName, _ := cmd.Flags().GetString("machine")
		if machineName == "" && len(args) > 0 {
			machineName = args[0]
		}
		client := newAPIClient()

		if machineName != "" {
			return showMachineUsage(cmd, client, machineName)
		}
		return showAccountUsage(cmd, client)
	},
}

// machineUsageResponse matches the backend's /machines/{id}/usage JSON shape.
type machineUsageResponse struct {
	MachineID         string            `json:"machine_id"`
	CurrentMonthSpend int64             `json:"current_month_spend"`
	BudgetMicrocents  *int64            `json:"budget_microcents,omitempty"`
	Since             string            `json:"since"`
	Records           []api.UsageRecord `json:"records"`
}

// accountUsageResponse matches the backend's /accounts/{id}/usage JSON shape.
type accountUsageResponse struct {
	AccountID         int                   `json:"account_id"`
	CurrentMonthSpend int64                 `json:"current_month_spend"`
	PerMachine        []accountMachineSpend `json:"per_machine"`
}

type accountMachineSpend struct {
	MachineID        string `json:"machine_id"`
	MachineName      string `json:"machine_name"`
	SpendMicrocents  int64  `json:"spend_microcents"`
	BudgetMicrocents *int64 `json:"budget_microcents,omitempty"`
}

func showMachineUsage(cmd *cobra.Command, client *api.Client, machineName string) error {
	machine, err := resolveMachine(client, machineName)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/api/accounts/%d/machines/%s/usage", cfg.DefaultAccountID, machine.ID)
	resp, err := client.Get(path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var usage machineUsageResponse
	if err := readJSON(&resp.Body, resp.StatusCode, 200, &usage); err != nil {
		return err
	}

	if isJSONOutput(cmd) {
		printJSON("usage", usage)
		return nil
	}

	totalSpend := float64(usage.CurrentMonthSpend) / 1_000_000.0
	fmt.Printf("Machine: %s\n", machine.Name)
	fmt.Printf("Current month spend: $%.2f\n", totalSpend)
	if usage.BudgetMicrocents != nil {
		budgetDollars := float64(*usage.BudgetMicrocents) / 1_000_000.0
		fmt.Printf("Budget: $%.2f\n", budgetDollars)
	}
	fmt.Println()

	if len(usage.Records) == 0 {
		fmt.Println("No usage records found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROVIDER\tMODEL\tINPUT TOKENS\tOUTPUT TOKENS\tCOST")
	for _, r := range usage.Records {
		cost := float64(r.CostMicrocents) / 1_000_000.0
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t$%.2f\n",
			r.Provider, r.Model,
			r.InputTokens, r.OutputTokens, cost)
	}
	w.Flush()
	return nil
}

func showAccountUsage(cmd *cobra.Command, client *api.Client) error {
	path := fmt.Sprintf("/api/accounts/%d/usage", cfg.DefaultAccountID)
	resp, err := client.Get(path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var usage accountUsageResponse
	if err := readJSON(&resp.Body, resp.StatusCode, 200, &usage); err != nil {
		return err
	}

	if isJSONOutput(cmd) {
		printJSON("usage", usage)
		return nil
	}

	totalSpend := float64(usage.CurrentMonthSpend) / 1_000_000.0
	fmt.Printf("Account total spend (current month): $%.2f\n\n", totalSpend)

	if len(usage.PerMachine) == 0 {
		fmt.Println("No usage recorded.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MACHINE\tSPEND\tBUDGET")
	for _, m := range usage.PerMachine {
		spend := fmt.Sprintf("$%.2f", float64(m.SpendMicrocents)/1_000_000.0)
		budget := "-"
		if m.BudgetMicrocents != nil {
			budget = fmt.Sprintf("$%.2f", float64(*m.BudgetMicrocents)/1_000_000.0)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", m.MachineName, spend, budget)
	}
	w.Flush()
	return nil
}

func init() {
	usageCmd.Flags().String("machine", "", "filter usage to a specific machine")
	registerMachineFlagCompletion(usageCmd)
	rootCmd.AddCommand(usageCmd)
}
