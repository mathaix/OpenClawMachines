package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/spf13/cobra"
)

var machineSecretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Manage machine secrets",
	Long:  "List, set, and delete secrets on an OpenClaw Machine.",
}

var machineSecretsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List secrets on a machine",
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

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/secrets", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		var secrets []api.SecretEntry
		if err := readJSON(&resp.Body, resp.StatusCode, 200, &secrets); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("machines.secrets.list", secrets)
			return nil
		}

		if len(secrets) == 0 {
			fmt.Println("No secrets configured on this machine.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "KEY\tCREATED\tUPDATED")
		for _, s := range secrets {
			fmt.Fprintf(w, "%s\t%s\t%s\n",
				s.Key,
				s.CreatedAt.Format("2006-01-02 15:04:05"),
				s.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
		w.Flush()
		return nil
	},
}

var machineSecretsSetCmd = &cobra.Command{
	Use:   "set KEY",
	Short: "Set a secret on a machine",
	Long:  "Set a secret value on a machine. Value is read from --value-from-stdin or interactive prompt.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}

		machineName, _ := cmd.Flags().GetString("machine")
		key := args[0]

		value, err := readSecretInput(cmd, "Enter secret value: ", "value-from-stdin")
		if err != nil {
			return err
		}

		client := newAPIClient()
		machine, err := resolveMachine(client, machineName)
		if err != nil {
			return err
		}

		reqBody, err := json.Marshal(map[string]string{"value": value})
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/secrets/%s", cfg.DefaultAccountID, machine.ID, key)
		resp, err := client.Put(path, reqBody)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if err := readJSON(&resp.Body, resp.StatusCode, 200, nil); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("machines.secrets.set", map[string]string{
				"key":     key,
				"machine": machine.Name,
				"status":  "ok",
			})
			return nil
		}

		fmt.Printf("Secret %q set on machine %s.\n", key, machine.Name)
		return nil
	},
}

var machineSecretsDeleteCmd = &cobra.Command{
	Use:   "delete KEY",
	Short: "Delete a secret from a machine",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}

		machineName, _ := cmd.Flags().GetString("machine")
		key := args[0]

		client := newAPIClient()
		machine, err := resolveMachine(client, machineName)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/secrets/%s", cfg.DefaultAccountID, machine.ID, key)
		resp, err := client.Delete(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != 204 {
			body, _ := io.ReadAll(resp.Body)
			return apiError(resp.StatusCode, string(body))
		}

		if isJSONOutput(cmd) {
			printJSON("machines.secrets.delete", map[string]string{
				"key":     key,
				"machine": machine.Name,
				"status":  "deleted",
			})
			return nil
		}

		fmt.Printf("Secret %q deleted from machine %s.\n", key, machine.Name)
		return nil
	},
}

func init() {
	machineSecretsListCmd.Flags().String("machine", "", "machine name")
	machineSecretsSetCmd.Flags().String("machine", "", "machine name")
	machineSecretsSetCmd.Flags().Bool("value-from-stdin", false, "read secret value from stdin")
	machineSecretsDeleteCmd.Flags().String("machine", "", "machine name")

	registerMachineFlagCompletion(machineSecretsListCmd)
	registerMachineFlagCompletion(machineSecretsSetCmd)
	registerMachineFlagCompletion(machineSecretsDeleteCmd)

	machineSecretsCmd.AddCommand(machineSecretsListCmd)
	machineSecretsCmd.AddCommand(machineSecretsSetCmd)
	machineSecretsCmd.AddCommand(machineSecretsDeleteCmd)

	machinesCmd.AddCommand(machineSecretsCmd)
}
