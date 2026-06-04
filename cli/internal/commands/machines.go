package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/mathaix/openclawmachines/cli/internal/config"
	"github.com/spf13/cobra"
)

func newAPIClient() *api.Client {
	return api.NewClient(cfg.APIURL, cfg.Token)
}

// cachedSizes holds sizes fetched from the API.
var cachedSizes []api.MachineSize

// fetchSizes retrieves available machine sizes from the API, caching the result.
func fetchSizes() ([]api.MachineSize, error) {
	if cachedSizes != nil {
		return cachedSizes, nil
	}
	client := newAPIClient()
	resp, err := client.Get("/api/sizes")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var sizes []api.MachineSize
	if err := readJSON(&resp.Body, resp.StatusCode, 200, &sizes); err != nil {
		return nil, fmt.Errorf("fetching sizes: %w", err)
	}
	cachedSizes = sizes
	return sizes, nil
}

// validateSize checks that a size ID exists in the server's size catalog.
func validateSize(size string) error {
	sizes, err := fetchSizes()
	if err != nil {
		return fmt.Errorf("could not fetch sizes: %w", err)
	}
	for _, s := range sizes {
		if s.ID == size {
			return nil
		}
	}
	var valid []string
	for _, s := range sizes {
		valid = append(valid, s.ID)
	}
	return fmt.Errorf("unknown size %q (valid: %s)", size, strings.Join(valid, ", "))
}

// sizeLabel derives a size label from vcpus/memory by matching against API sizes.
func sizeLabel(vcpus, memoryMB int) string {
	sizes, err := fetchSizes()
	if err == nil {
		for _, s := range sizes {
			if s.VCPUs == vcpus && s.MemoryMB == memoryMB {
				return s.Label
			}
		}
	}
	return fmt.Sprintf("%dvcpu/%dMB", vcpus, memoryMB)
}

// apiError formats an API error, adding a helpful hint for 401 (expired token).
func apiError(statusCode int, body string) error {
	if statusCode == 401 {
		return fmt.Errorf("session expired — run 'ocm login' to re-authenticate")
	}
	return fmt.Errorf("API error (%d): %s", statusCode, body)
}

// readJSON reads the response body and unmarshals it. Returns an error if status is not expected.
func readJSON(resp *io.ReadCloser, statusCode int, expectedStatus int, v any) error {
	body, err := io.ReadAll(*resp)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if statusCode != expectedStatus {
		return apiError(statusCode, string(body))
	}
	if v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	}
	return nil
}

// listMachines fetches all machines for the current account.
func listMachines(client *api.Client) ([]api.Machine, error) {
	path := fmt.Sprintf("/api/accounts/%d/machines", cfg.DefaultAccountID)
	resp, err := client.Get(path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var machines []api.Machine
	if err := readJSON(&resp.Body, resp.StatusCode, 200, &machines); err != nil {
		return nil, err
	}
	return machines, nil
}

// findMachineByName searches a list for a machine by name (case-insensitive) or slug.
func findMachineByName(machines []api.Machine, nameOrSlug string) *api.Machine {
	lower := strings.ToLower(nameOrSlug)

	// Name match first (case-insensitive)
	for i := range machines {
		if strings.ToLower(machines[i].Name) == lower {
			return &machines[i]
		}
	}
	// Slug fallback for scripting/backward compat
	for i := range machines {
		if machines[i].Slug == nameOrSlug {
			return &machines[i]
		}
	}
	return nil
}

// machineNotFoundError builds a helpful error showing available machines.
func machineNotFoundError(nameOrSlug string, machines []api.Machine) error {
	if len(machines) == 0 {
		return fmt.Errorf("no machines found — create one with 'ocm machines create --name <name>'")
	}
	names := make([]string, len(machines))
	for i, m := range machines {
		names[i] = fmt.Sprintf("%q", m.Name)
	}
	return fmt.Errorf("machine %q not found — available: %s", nameOrSlug, strings.Join(names, ", "))
}

// resolveMachine finds a machine using contextual resolution:
//  1. If nameOrSlug is provided, match by name (case-insensitive) then slug
//  2. If empty, use the default machine from config
//  3. If no default and only one machine exists, use it automatically
//  4. Otherwise, error with available machines
func resolveMachine(client *api.Client, nameOrSlug string) (*api.Machine, error) {
	machines, err := listMachines(client)
	if err != nil {
		return nil, err
	}

	// Explicit name provided
	if nameOrSlug != "" {
		m := findMachineByName(machines, nameOrSlug)
		if m == nil {
			return nil, machineNotFoundError(nameOrSlug, machines)
		}
		return m, nil
	}

	// Try default from config
	if cfg.DefaultMachineID != "" {
		for i := range machines {
			if machines[i].ID == cfg.DefaultMachineID {
				return &machines[i], nil
			}
		}
		// Default machine was deleted — clear it silently
		cfg.DefaultMachineID = ""
		cfg.DefaultMachineName = ""
		_ = config.Save(cfg, cfgFile)
	}

	// Auto-select if only one machine
	if len(machines) == 1 {
		return &machines[0], nil
	}

	// Can't auto-resolve
	if len(machines) == 0 {
		return nil, fmt.Errorf("no machines found — create one with 'ocm machines create --name <name>'")
	}
	names := make([]string, len(machines))
	for i, m := range machines {
		names[i] = fmt.Sprintf("%q", m.Name)
	}
	return nil, fmt.Errorf("multiple machines found — specify with --machine or run 'ocm machines use <name>'\n  available: %s", strings.Join(names, ", "))
}

// setDefaultMachine saves a machine as the default in config.
func setDefaultMachine(m *api.Machine) error {
	cfg.DefaultMachineID = m.ID
	cfg.DefaultMachineName = m.Name
	return config.Save(cfg, cfgFile)
}

func printMachineDetails(m *api.Machine) {
	fmt.Printf("Name:    %s\n", m.Name)
	fmt.Printf("Status:  %s\n", m.Status)
	fmt.Printf("Size:    %s\n", sizeLabel(m.VCPUs, m.MemoryMB))
	if m.TunnelHostname != nil {
		fmt.Printf("URL:     https://%s\n", *m.TunnelHostname)
	}
	fmt.Printf("Created: %s\n", m.CreatedAt.Format("2006-01-02 15:04:05"))
}

var machinesCmd = &cobra.Command{
	Use:   "machines",
	Short: "Manage OpenClaw Machines",
	Long:  "Create, list, start, stop, and delete OpenClaw Machines.",
}

var machinesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all machines",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		client := newAPIClient()
		path := fmt.Sprintf("/api/accounts/%d/machines", cfg.DefaultAccountID)
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		var machines []api.Machine
		if err := readJSON(&resp.Body, resp.StatusCode, 200, &machines); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("machines.list", machines)
			return nil
		}

		if len(machines) == 0 {
			fmt.Println("No machines found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATUS\tSIZE\tCREATED")
		for _, m := range machines {
			defaultMarker := ""
			if m.ID == cfg.DefaultMachineID {
				defaultMarker = " *"
			}
			fmt.Fprintf(w, "%s%s\t%s\t%s\t%s\n",
				m.Name, defaultMarker, m.Status,
				sizeLabel(m.VCPUs, m.MemoryMB),
				m.CreatedAt.Format("2006-01-02"))
		}
		w.Flush()
		return nil
	},
}

var machinesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new machine",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("name")
		size, _ := cmd.Flags().GetString("size")

		if name == "" {
			return fmt.Errorf("--name is required")
		}

		req := map[string]any{
			"name":       name,
			"auto_start": true,
		}
		if size != "" {
			if err := validateSize(size); err != nil {
				return err
			}
			req["size"] = size
		}

		reqBody, err := json.Marshal(req)
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}

		client := newAPIClient()
		path := fmt.Sprintf("/api/accounts/%d/machines", cfg.DefaultAccountID)
		resp, err := client.PostLong(path, reqBody)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		var machine api.Machine
		if err := readJSON(&resp.Body, resp.StatusCode, 201, &machine); err != nil {
			return err
		}

		// Auto-set as default machine
		if err := setDefaultMachine(&machine); err != nil {
			fmt.Printf("Warning: could not save default machine: %v\n", err)
		}

		if isJSONOutput(cmd) {
			printJSON("machines.create", machine)
			return nil
		}

		fmt.Println("Machine created successfully.")
		printMachineDetails(&machine)
		return nil
	},
}

var machinesGetCmd = &cobra.Command{
	Use:               "get [NAME]",
	Short:             "Get machine details",
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
		client := newAPIClient()

		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		// Fetch full details by ID
		path := fmt.Sprintf("/api/accounts/%d/machines/%s", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		var m api.Machine
		if err := readJSON(&resp.Body, resp.StatusCode, 200, &m); err != nil {
			return err
		}

		if isJSONOutput(cmd) {
			printJSON("machines.get", m)
			return nil
		}

		printMachineDetails(&m)
		return nil
	},
}

var machinesStartCmd = &cobra.Command{
	Use:               "start [NAME]",
	Short:             "Start a machine",
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
		client := newAPIClient()

		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/start", cfg.DefaultAccountID, machine.ID)
		resp, err := client.PostLong(path, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 && resp.StatusCode != 202 {
			body, _ := io.ReadAll(resp.Body)
			return apiError(resp.StatusCode, string(body))
		}

		if isJSONOutput(cmd) {
			printJSON("machines.start", map[string]string{"machine": machine.Name, "status": "starting"})
			return nil
		}

		fmt.Printf("Starting machine: %s\n", machine.Name)
		return nil
	},
}

var machinesStopCmd = &cobra.Command{
	Use:               "stop [NAME]",
	Short:             "Stop a machine",
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
		client := newAPIClient()

		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s/stop", cfg.DefaultAccountID, machine.ID)
		resp, err := client.PostLong(path, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 && resp.StatusCode != 202 {
			body, _ := io.ReadAll(resp.Body)
			return apiError(resp.StatusCode, string(body))
		}

		if isJSONOutput(cmd) {
			printJSON("machines.stop", map[string]string{"machine": machine.Name, "status": "stopping"})
			return nil
		}

		fmt.Printf("Stopping machine: %s\n", machine.Name)
		return nil
	},
}

var machinesDeleteCmd = &cobra.Command{
	Use:               "delete [NAME]",
	Short:             "Delete a machine",
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
		client := newAPIClient()

		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		// Clear default if deleting the default machine
		if machine.ID == cfg.DefaultMachineID {
			cfg.DefaultMachineID = ""
			cfg.DefaultMachineName = ""
			_ = config.Save(cfg, cfgFile)
		}

		path := fmt.Sprintf("/api/accounts/%d/machines/%s", cfg.DefaultAccountID, machine.ID)
		resp, err := client.DeleteLong(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 && resp.StatusCode != 204 {
			body, _ := io.ReadAll(resp.Body)
			return apiError(resp.StatusCode, string(body))
		}

		if isJSONOutput(cmd) {
			printJSON("machines.delete", map[string]string{"machine": machine.Name, "status": "deleted"})
			return nil
		}

		fmt.Printf("Deleted machine: %s\n", machine.Name)
		return nil
	},
}

var machinesConfigCmd = &cobra.Command{
	Use:               "config [NAME]",
	Short:             "Show assembled gateway config for a machine",
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

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading response: %w", err)
		}
		if resp.StatusCode != 200 {
			return apiError(resp.StatusCode, string(body))
		}

		if isJSONOutput(cmd) {
			// Raw JSON for piping
			fmt.Println(string(body))
			return nil
		}

		// Pretty-print
		var parsed json.RawMessage
		if err := json.Unmarshal(body, &parsed); err != nil {
			// Not valid JSON, print as-is
			fmt.Println(string(body))
			return nil
		}
		pretty, err := json.MarshalIndent(parsed, "", "  ")
		if err != nil {
			fmt.Println(string(body))
			return nil
		}
		fmt.Println(string(pretty))
		return nil
	},
}

var machinesUseCmd = &cobra.Command{
	Use:               "use NAME",
	Short:             "Set the default machine",
	Long:              "Set the default machine for all commands. Other machines can still be targeted with --machine.",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeMachineNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}
		client := newAPIClient()
		machine, err := resolveMachine(client, args[0])
		if err != nil {
			return err
		}

		if err := setDefaultMachine(machine); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}

		if isJSONOutput(cmd) {
			printJSON("machines.use", map[string]string{"machine": machine.Name, "status": "default"})
			return nil
		}

		fmt.Printf("Default machine set to %q\n", machine.Name)
		return nil
	},
}

func init() {
	machinesCreateCmd.Flags().String("name", "", "machine name (required)")
	machinesCreateCmd.Flags().String("size", "", "machine size (omit for default; see 'ocm sizes' for options)")

	machinesCmd.AddCommand(machinesListCmd)
	machinesCmd.AddCommand(machinesCreateCmd)
	machinesCmd.AddCommand(machinesGetCmd)
	machinesCmd.AddCommand(machinesStartCmd)
	machinesCmd.AddCommand(machinesStopCmd)
	machinesCmd.AddCommand(machinesDeleteCmd)
	machinesCmd.AddCommand(machinesConfigCmd)
	machinesCmd.AddCommand(machinesUseCmd)

	rootCmd.AddCommand(machinesCmd)
}
