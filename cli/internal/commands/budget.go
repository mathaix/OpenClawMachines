package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var budgetCmd = &cobra.Command{
	Use:   "budget",
	Short: "Manage machine budgets",
	Long:  "Set or clear spending budgets for OpenClaw Machines.",
}

var budgetSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set a machine budget",
	Long:  "Set a spending budget (in dollars) for a machine.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		machineName, _ := cmd.Flags().GetString("machine")
		limit, _ := cmd.Flags().GetFloat64("limit")

		if limit <= 0 {
			return fmt.Errorf("--limit must be a positive dollar amount")
		}

		client := newAPIClient()
		machine, err := resolveMachine(client, machineName)
		if err != nil {
			return err
		}

		microcents := int64(limit * 1_000_000)
		reqBody, err := json.Marshal(map[string]any{
			"budget_microcents": microcents,
		})
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/budget", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Put(path, reqBody)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if err := readJSON(&resp.Body, resp.StatusCode, 200, nil); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("budget.set", map[string]any{"machine": machine.Name, "limit_dollars": limit, "status": "set"})
			return nil
		}

		fmt.Printf("Budget set to $%.2f for machine %q.\n", limit, machine.Name)
		return nil
	},
}

var budgetClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear a machine budget",
	Long:  "Remove the spending budget for a machine.",
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

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/budget", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Delete(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 && resp.StatusCode != 204 {
			if err := readJSON(&resp.Body, resp.StatusCode, 200, nil); err != nil {
				return err
			}
		}

		if isJSONOutput(cmd) {
			printJSON("budget.clear", map[string]string{"machine": machine.Name, "status": "cleared"})
			return nil
		}

		fmt.Printf("Budget cleared for machine %q.\n", machine.Name)
		return nil
	},
}

func init() {
	budgetSetCmd.Flags().String("machine", "", "machine name")
	budgetSetCmd.Flags().Float64("limit", 0, "budget limit in dollars (required)")

	budgetClearCmd.Flags().String("machine", "", "machine name")

	registerMachineFlagCompletion(budgetSetCmd)
	registerMachineFlagCompletion(budgetClearCmd)

	budgetCmd.AddCommand(budgetSetCmd)
	budgetCmd.AddCommand(budgetClearCmd)

	rootCmd.AddCommand(budgetCmd)
}
