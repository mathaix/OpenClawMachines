package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:               "status [NAME]",
	Short:             "Machine dashboard",
	Long:              "Display a comprehensive status overview for an OpenClaw Machine, including providers, channels, skills, and usage.",
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

		// 1. Resolve machine
		machine, err := resolveMachine(client, machineName)
		if err != nil {
			return err
		}

		// 2. Fetch full machine details
		machPath := fmt.Sprintf("/api/accounts/%d/machines/%s", cfg.DefaultAccountID, machine.ID)
		machResp, err := client.Get(machPath)
		if err != nil {
			return err
		}
		defer machResp.Body.Close()

		var m api.Machine
		if err := readJSON(&machResp.Body, machResp.StatusCode, 200, &m); err != nil {
			return err
		}

		// 3. Fetch credentials
		credsPath := fmt.Sprintf("/api/accounts/%d/machines/%s/credentials", cfg.DefaultAccountID, machine.ID)
		credsResp, err := client.Get(credsPath)
		if err != nil {
			return err
		}
		defer credsResp.Body.Close()

		var creds []api.Credential
		if err := readJSON(&credsResp.Body, credsResp.StatusCode, 200, &creds); err != nil {
			return err
		}

		// 4. Fetch capabilities
		capsPath := fmt.Sprintf("/api/accounts/%d/machines/%s/capabilities", cfg.DefaultAccountID, machine.ID)
		capsResp, err := client.Get(capsPath)
		if err != nil {
			return err
		}
		defer capsResp.Body.Close()

		var caps []api.MachineCapability
		if err := readJSON(&capsResp.Body, capsResp.StatusCode, 200, &caps); err != nil {
			return err
		}

		// 5. Fetch assembled config for identity + version
		cfgPath := fmt.Sprintf("/api/accounts/%d/machines/%s/assembled-config", cfg.DefaultAccountID, machine.ID)
		cfgResp, err := client.Get(cfgPath)
		if err != nil {
			return err
		}
		defer cfgResp.Body.Close()

		cfgBody, err := io.ReadAll(cfgResp.Body)
		if err != nil {
			return fmt.Errorf("reading config response: %w", err)
		}

		identityName := "-"
		configVersion := "-"
		configPushedAgo := ""
		if cfgResp.StatusCode == 200 {
			identityName, _ = extractIdentityFromConfig(cfgBody)
			configVersion, configPushedAgo = extractConfigVersion(cfgBody)
		}

		// 6. Fetch usage
		usagePath := fmt.Sprintf("/api/accounts/%d/machines/%s/usage", cfg.DefaultAccountID, machine.ID)
		usageResp, err := client.Get(usagePath)
		if err != nil {
			return err
		}
		defer usageResp.Body.Close()

		var usageData machineUsageResponse
		if err := readJSON(&usageResp.Body, usageResp.StatusCode, 200, &usageData); err != nil {
			return err
		}
		records := usageData.Records

		// Separate channels and skills from capabilities
		var channels []api.MachineCapability
		var skills []api.MachineCapability
		for _, c := range caps {
			if c.EntryType == "channel" && c.Enabled {
				channels = append(channels, c)
			}
			if c.EntryType == "skill" && c.Enabled {
				skills = append(skills, c)
			}
		}

		// JSON output
		if isJSONOutput(cmd) {
			printJSON("status", map[string]any{
				"machine":        m,
				"credentials":    creds,
				"channels":       channels,
				"skills":         skills,
				"identity":       identityName,
				"config_version": configVersion,
				"usage":          records,
			})
			return nil
		}

		// ── Machine info box ──
		statusDot := statusDotColored(m.Status)
		statusLine := fmt.Sprintf("%s%s%s", cBold(m.Name), statusPadding(m.Name, m.Status), statusDot)

		var boxLines []string
		boxLines = append(boxLines, statusLine)

		if m.TunnelHostname != nil {
			boxLines = append(boxLines, fmt.Sprintf("%-9s%s", cDim("URL"), cBlue("https://"+*m.TunnelHostname)))
		}

		size := sizeLabel(m.VCPUs, m.MemoryMB)
		sizeDetail := fmt.Sprintf("%s (%d vCPU / %d GB)", size, m.VCPUs, m.MemoryMB/1024)
		boxLines = append(boxLines, fmt.Sprintf("%-9s%s", cDim("Size"), cWhite(sizeDetail)))

		boxLines = append(boxLines, fmt.Sprintf("%-9s%s", cDim("Identity"), cWhite(identityName)))

		configDetail := configVersion
		if configPushedAgo != "" {
			configDetail += " " + cDim("(pushed "+configPushedAgo+")")
		}
		boxLines = append(boxLines, fmt.Sprintf("%-9s%s", cDim("Config"), cWhite(configDetail)))

		fmt.Println()
		fmt.Println(summaryBox(boxLines))

		// ── Providers section ──
		fmt.Printf("\n  %s\n  %s\n", cBold("Providers"), cDim(strings.Repeat("─", 9)))
		if len(creds) == 0 {
			fmt.Printf("  %s\n", cDim("(none linked)"))
		} else {
			for _, c := range creds {
				dot := cGreen("●")
				lastFour := "····"
				if c.LastFour != nil {
					lastFour = "····" + *c.LastFour
				}
				credType := c.CredentialType
				if credType == "" {
					credType = "-"
				}
				validated := ""
				if c.LastValidated != nil {
					validated = "validated " + friendlyDuration(time.Since(*c.LastValidated))
				}
				fmt.Printf("  %s %-12s %s   %s   %s\n",
					dot, c.Provider, cDim(lastFour), cDim(credType), cDim(validated))
			}
		}

		// ── Channels section ──
		fmt.Printf("\n  %s\n  %s\n", cBold("Channels"), cDim(strings.Repeat("─", 8)))
		if len(channels) == 0 {
			fmt.Printf("  %s\n", cDim("(none configured)"))
		} else {
			for _, ch := range channels {
				fmt.Printf("  %s %s    %s\n", cGreen("●"), ch.EntryName, cDim("enabled"))
			}
		}

		// ── Skills section ──
		fmt.Printf("\n  %s\n  %s\n", cBold("Skills"), cDim(strings.Repeat("─", 6)))
		if len(skills) == 0 {
			fmt.Printf("  %s\n", cDim("(none configured)"))
		} else {
			for _, sk := range skills {
				fmt.Printf("  %s %s    %s\n", cGreen("●"), sk.EntryName, cDim("enabled"))
			}
		}

		// ── Usage section ──
		fmt.Printf("\n  %s\n  %s\n", cBold("Usage"), cDim(strings.Repeat("─", 5)))
		if len(records) == 0 {
			fmt.Printf("  %s\n", cDim("No usage recorded."))
		} else {
			totalCost, totalRequests := summarizeUsage(records)
			fmt.Printf("  $%.2f across %d requests\n\n", totalCost, totalRequests)

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n",
				cDim("PROVIDER"), cDim("MODEL"), cDim("IN TOKENS"), cDim("OUT TOKENS"), cDim("COST"))
			for _, agg := range aggregateUsage(records) {
				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t$%.2f\n",
					agg.provider, agg.model,
					formatNumber(agg.inputTokens), formatNumber(agg.outputTokens),
					agg.cost)
			}
			w.Flush()
		}

		fmt.Println()
		return nil
	},
}

// statusDotColored returns a colored status dot for the given machine status.
func statusDotColored(status string) string {
	switch status {
	case "running":
		return cGreen("● running")
	case "provisioning", "starting":
		return cAmber("● " + status)
	default:
		return cRed("● " + status)
	}
}

// statusPadding returns spacing between the machine name and the status dot
// to right-align the status in the summary box.
func statusPadding(name, status string) string {
	// Target total width of content inside box (excluding box padding)
	const targetWidth = 51
	nameLen := len(name)
	// status dot + space + status word
	statusLen := 2 + len(status)
	pad := targetWidth - nameLen - statusLen
	if pad < 4 {
		pad = 4
	}
	return strings.Repeat(" ", pad)
}

// extractConfigVersion extracts the config version and last-pushed time
// from an assembled config JSON response.
func extractConfigVersion(body []byte) (version string, pushedAgo string) {
	version = "-"

	var wrapped struct {
		Config   json.RawMessage `json:"config"`
		Version  *int            `json:"version,omitempty"`
		PushedAt *time.Time      `json:"pushed_at,omitempty"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil {
		if wrapped.Version != nil {
			version = fmt.Sprintf("v%d", *wrapped.Version)
		}
		if wrapped.PushedAt != nil {
			pushedAgo = friendlyDuration(time.Since(*wrapped.PushedAt))
		}
	}

	return version, pushedAgo
}

// friendlyDuration returns a human-friendly relative time string.
func friendlyDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			if h == 1 {
				return "1h ago"
			}
			return fmt.Sprintf("%dh ago", h)
		}
		return fmt.Sprintf("%dh %dm ago", h, m)
	default:
		days := int(d.Hours()) / 24
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

// summarizeUsage computes total cost and request count from usage records.
func summarizeUsage(records []api.UsageRecord) (totalCost float64, totalRequests int) {
	for _, r := range records {
		totalCost += float64(r.CostMicrocents) / 1_000_000.0
		totalRequests++
	}
	return totalCost, totalRequests
}

type usageAggregate struct {
	provider     string
	model        string
	inputTokens  int
	outputTokens int
	cost         float64
}

// aggregateUsage groups usage records by provider+model and sums tokens/cost.
func aggregateUsage(records []api.UsageRecord) []usageAggregate {
	type key struct {
		provider string
		model    string
	}
	m := make(map[key]*usageAggregate)
	var order []key

	for _, r := range records {
		k := key{r.Provider, r.Model}
		if _, ok := m[k]; !ok {
			m[k] = &usageAggregate{provider: r.Provider, model: r.Model}
			order = append(order, k)
		}
		agg := m[k]
		agg.inputTokens += r.InputTokens
		agg.outputTokens += r.OutputTokens
		agg.cost += float64(r.CostMicrocents) / 1_000_000.0
	}

	result := make([]usageAggregate, 0, len(order))
	for _, k := range order {
		result = append(result, *m[k])
	}
	return result
}

// formatNumber formats an integer with comma separators (e.g. 84210 -> "84,210").
func formatNumber(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}

	var b strings.Builder
	remainder := len(s) % 3
	if remainder > 0 {
		b.WriteString(s[:remainder])
	}
	for i := remainder; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func init() {
	statusCmd.Flags().String("machine", "", "machine name")
	registerMachineFlagCompletion(statusCmd)
	rootCmd.AddCommand(statusCmd)
}
