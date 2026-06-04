package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/mathaix/openclawmachines/cli/internal/config"
	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Guided setup wizard for a machine",
	Long: `Interactive wizard that walks you through setting up a machine with
LLM providers, channels, and identity.

Re-run safe: already-configured providers and channels are shown with [*] markers.
Skip them or add new ones.

Non-interactive mode (for scripting/CI):
  ocm setup --json --provider anthropic --key sk-ant-xxx --channel telegram --token 123:ABC`,
	RunE: runSetup,
}

func init() {
	setupCmd.Flags().String("machine", "", "machine name (skip selection)")
	setupCmd.Flags().StringSlice("provider", nil, "provider(s) to configure (comma-separated)")
	setupCmd.Flags().StringSlice("key", nil, "API key(s), same order as --provider")
	setupCmd.Flags().StringSlice("key-type", nil, "credential type per provider (api_key/subscription_key)")
	setupCmd.Flags().StringSlice("channel", nil, "channel(s) to enable (comma-separated)")
	setupCmd.Flags().StringSlice("token", nil, "bot token(s), same order as --channel")
	setupCmd.Flags().String("name", "", "identity name")
	setupCmd.Flags().String("avatar", "", "identity avatar URL")
	setupCmd.Flags().Bool("skip-identity", false, "skip identity step")
	setupCmd.Flags().String("label", "", "label for credentials (default: \"ocm setup\")")

	registerMachineFlagCompletion(setupCmd)
	rootCmd.AddCommand(setupCmd)
}

// llmProvider describes an LLM provider option.
type llmProvider struct {
	key  string
	name string
}

// channelOption describes a channel option.
type channelOption struct {
	key  string
	name string
}

var llmProviders = []llmProvider{
	{"anthropic", "Anthropic (Claude)"},
	{"openai", "OpenAI (GPT)"},
	{"google", "Google AI (Gemini)"},
}

var channelOptions = []channelOption{
	{"telegram", "Telegram"},
	{"discord", "Discord"},
	{"whatsapp", "WhatsApp"},
}

func runSetup(cmd *cobra.Command, args []string) error {
	jsonMode := isJSONOutput(cmd)
	completed := map[int]bool{}

	// Determine if running in non-interactive mode
	providerFlags, _ := cmd.Flags().GetStringSlice("provider")
	keyFlags, _ := cmd.Flags().GetStringSlice("key")
	channelFlags, _ := cmd.Flags().GetStringSlice("channel")
	tokenFlags, _ := cmd.Flags().GetStringSlice("token")
	nonInteractive := jsonMode || len(providerFlags) > 0 || len(channelFlags) > 0

	// Validate flag counts up front
	if len(providerFlags) > 0 && len(keyFlags) != len(providerFlags) {
		return fmt.Errorf("--provider and --key must have the same number of values (got %d providers and %d keys)", len(providerFlags), len(keyFlags))
	}
	if len(channelFlags) > 0 && len(tokenFlags) != len(channelFlags) {
		return fmt.Errorf("--channel and --token must have the same number of values (got %d channels and %d tokens)", len(channelFlags), len(tokenFlags))
	}

	// ── Step 0: Login ──
	if !jsonMode {
		fmt.Println()
		fmt.Println(wizardHeader())
		printStyledProgress(0, completed)
		sectionHeader(1, "LOGIN", false)
	}

	if err := ensureLogin(); err != nil {
		return err
	}
	completed[0] = true

	client := newAPIClient()

	// ── Step 1: Machine ──
	if !jsonMode {
		fmt.Printf("  %s %s\n", checkMark(), cGreen("Authenticated as ")+cWhite(cfg.DefaultAccountSlug))
		printStyledProgress(1, completed)
		sectionHeader(2, "MACHINE", false)
	}

	machineFlag, _ := cmd.Flags().GetString("machine")
	machine, err := setupResolveMachine(cmd, client, machineFlag, jsonMode)
	if err != nil {
		return err
	}
	completed[1] = true

	if !jsonMode {
		fmt.Printf("  %s %s %s\n", checkMark(), cGreen("Selected machine"), cWhite(machine.Name))
	}

	// ── Step 2: Providers ──
	if !jsonMode {
		printStyledProgress(2, completed)
		sectionHeader(3, "LLM PROVIDER", false)
	}

	label, _ := cmd.Flags().GetString("label")
	if label == "" {
		label = "ocm setup"
	}

	var configuredProviders []string

	if nonInteractive {
		// Non-interactive: use flags
		keyTypeFlags, _ := cmd.Flags().GetStringSlice("key-type")
		for i, provider := range providerFlags {
			provider = strings.ToLower(provider)
			credType := inferCredentialType(provider)
			if i < len(keyTypeFlags) && keyTypeFlags[i] != "" {
				credType = keyTypeFlags[i]
			}

			// Validate credential type
			switch credType {
			case "api_key", "subscription_key", "token", "oauth":
				// valid
			default:
				return fmt.Errorf("invalid --key-type %q for %s (valid: api_key, subscription_key, token, oauth)", credType, provider)
			}

			if !jsonMode {
				fmt.Printf("\n  Validating %s key... ", provider)
			}
			if err := validateCredential(provider, keyFlags[i]); err != nil {
				if !jsonMode {
					fmt.Printf("  %s %s\n", failMark(), cRed("Validation failed"))
				}
				return fmt.Errorf("%s key validation failed: %w", provider, err)
			}
			if !jsonMode {
				fmt.Printf("  %s %s\n", checkMark(), cGreen("Key validated"))
			}

			if err := storeAndLinkCredential(client, machine, provider, keyFlags[i], credType, label); err != nil {
				return fmt.Errorf("setting up %s: %w", provider, err)
			}
			configuredProviders = append(configuredProviders, provider)
		}
	} else {
		// Interactive: show existing + prompt for new
		existingCreds := fetchMachineCredentials(client, machine)
		existingProviderSet := map[string]bool{}
		for _, c := range existingCreds {
			existingProviderSet[c.Provider] = true
		}

		for i, p := range llmProviders {
			num := fmt.Sprintf("%d.", i+1)
			if existingProviderSet[p.key] {
				configuredProviders = append(configuredProviders, p.key)
				fmt.Printf("  %s %s %s\n", cBold(num), p.name, cGreen("✓"))
			} else if i == 0 {
				fmt.Printf("  %s %s\n", cBold(num), p.name)
			} else {
				fmt.Printf("  %s %s\n", cMuted(num), p.name)
			}
		}

		fmt.Println("\n  Select providers to configure (comma-separated numbers, or 'none'):")
		selected, err := readSelection(len(llmProviders))
		if err != nil {
			return err
		}

		for _, idx := range selected {
			p := llmProviders[idx]
			if existingProviderSet[p.key] {
				reconfigure, err := promptYesNo(fmt.Sprintf("  %s is already configured. Reconfigure?", p.name), false)
				if err != nil {
					return err
				}
				if !reconfigure {
					continue
				}
			}

			fmt.Printf("\n  --- %s ---\n", p.name)
			if err := setupProviderInteractive(cmd, client, machine, p.key, label); err != nil {
				return fmt.Errorf("failed to set up %s: %w", p.name, err)
			} else if !existingProviderSet[p.key] {
				configuredProviders = append(configuredProviders, p.key)
			}
		}
	}
	completed[2] = true

	// ── Step 3: Channels ──
	if !jsonMode {
		printStyledProgress(3, completed)
		sectionHeader(4, "CHANNEL", true)
	}

	var enabledChannels []string

	if nonInteractive {
		for i, channel := range channelFlags {
			channel = strings.ToLower(channel)

			if !jsonMode {
				fmt.Printf("\n  Validating %s token... ", channel)
			}
			if err := validateCredential(channel, tokenFlags[i]); err != nil {
				if !jsonMode {
					fmt.Printf("  %s %s\n", failMark(), cRed("Validation failed"))
				}
				return fmt.Errorf("%s token validation failed: %w", channel, err)
			}
			if !jsonMode {
				fmt.Printf("  %s %s\n", checkMark(), cGreen("Key validated"))
			}

			credType := inferCredentialType(channel)
			if err := storeAndLinkCredential(client, machine, channel, tokenFlags[i], credType, label); err != nil {
				return fmt.Errorf("setting up %s credential: %w", channel, err)
			}
			if err := enableChannel(client, machine, channel); err != nil {
				return fmt.Errorf("enabling %s: %w", channel, err)
			}
			enabledChannels = append(enabledChannels, channel)
		}
	} else {
		existingCaps := fetchMachineCapabilities(client, machine)
		existingChannelSet := map[string]bool{}
		for _, cap := range existingCaps {
			if cap.Enabled {
				existingChannelSet[cap.EntryID] = true
			}
		}

		for i, ch := range channelOptions {
			num := fmt.Sprintf("%d.", i+1)
			if existingChannelSet[ch.key] {
				enabledChannels = append(enabledChannels, ch.key)
				fmt.Printf("  %s %s %s\n", cBold(num), ch.name, cGreen("✓"))
			} else if i == 0 {
				fmt.Printf("  %s %s\n", cBold(num), ch.name)
			} else {
				fmt.Printf("  %s %s\n", cMuted(num), ch.name)
			}
		}

		fmt.Println("\n  Select channels to enable (comma-separated numbers, or 'none'):")
		selected, err := readSelection(len(channelOptions))
		if err != nil {
			return err
		}

		for _, idx := range selected {
			ch := channelOptions[idx]
			if existingChannelSet[ch.key] {
				reconfigure, err := promptYesNo(fmt.Sprintf("  %s is already enabled. Reconfigure?", ch.name), false)
				if err != nil {
					return err
				}
				if !reconfigure {
					continue
				}
			}

			fmt.Printf("\n  --- %s ---\n", ch.name)
			if err := setupChannelInteractive(cmd, client, machine, ch.key, label); err != nil {
				return fmt.Errorf("failed to set up %s: %w", ch.name, err)
			} else if !existingChannelSet[ch.key] {
				enabledChannels = append(enabledChannels, ch.key)
			}
		}
	}
	completed[3] = true

	// ── Step 4: Identity ──
	skipIdentity, _ := cmd.Flags().GetBool("skip-identity")
	if !skipIdentity {
		if !jsonMode {
			printStyledProgress(4, completed)
			sectionHeader(5, "IDENTITY", true)
		}

		nameFlag, _ := cmd.Flags().GetString("name")
		avatarFlag, _ := cmd.Flags().GetString("avatar")

		if nonInteractive {
			if nameFlag != "" || avatarFlag != "" {
				if err := setMachineIdentity(client, machine, nameFlag, avatarFlag); err != nil {
					return fmt.Errorf("setting identity: %w", err)
				}
			}
		} else {
			setIdentity, err := promptYesNo("  Set an identity (name/avatar) for this machine?", false)
			if err != nil {
				return err
			}
			if setIdentity {
				if err := setupIdentityInteractive(client, machine); err != nil {
					fmt.Printf("  Warning: failed to set identity: %v\n", err)
				}
			}
		}
	}
	completed[4] = true

	// ── Step 5: Verify ──
	if !jsonMode {
		printStyledProgress(5, completed)
		sectionHeader(6, "VERIFY", false)
	}

	// Push config
	configPushed := false
	pushPath := fmt.Sprintf("/api/accounts/%d/machines/%s/config/push", cfg.DefaultAccountID, machine.ID)
	pushResp, err := client.Post(pushPath, nil)
	if err == nil {
		defer pushResp.Body.Close()
		if err := readJSON(&pushResp.Body, pushResp.StatusCode, 200, nil); err != nil {
			if !jsonMode {
				fmt.Printf("  Warning: config push failed: %v\n", err)
			}
		} else {
			configPushed = true
			if !jsonMode {
				fmt.Printf("  %s Configuration pushed\n", checkMark())
			}
		}
	} else if !jsonMode {
		fmt.Printf("  Warning: config push failed: %v\n", err)
	}

	// Health check if machine has a tunnel hostname
	healthOk := false
	if machine.TunnelHostname != nil {
		healthOk = checkMachineHealth(*machine.TunnelHostname)
		if !jsonMode {
			if healthOk {
				fmt.Printf("  %s Machine %s is running\n", checkMark(), cWhite(machine.Name))
			} else {
				fmt.Printf("  %s Machine not responding (may still be starting)\n", warnMark())
			}
		}
	}

	completed[5] = true

	// ── Output ──
	if jsonMode {
		printJSON("setup", map[string]any{
			"machine":       machine.Name,
			"providers":     configuredProviders,
			"channels":      enabledChannels,
			"config_pushed": configPushed,
			"health_ok":     healthOk,
		})
		return nil
	}

	printStyledSummary(machine, configuredProviders, enabledChannels, completed)
	return nil
}

// setupResolveMachine handles machine selection/creation for the setup wizard.
func setupResolveMachine(_ *cobra.Command, client *api.Client, nameFlag string, jsonMode bool) (*api.Machine, error) {
	// If flag provided, resolve directly
	if nameFlag != "" {
		m, err := resolveMachine(client, nameFlag)
		if err != nil {
			return nil, err
		}
		if err := setDefaultMachine(m); err != nil {
			return nil, fmt.Errorf("saving default machine: %w", err)
		}
		return m, nil
	}

	// In JSON mode, require --machine flag or auto-resolve
	if jsonMode {
		m, err := resolveMachine(client, "")
		if err != nil {
			return nil, fmt.Errorf("--machine flag required in non-interactive mode (or set a default with 'ocm machines use'): %w", err)
		}
		return m, nil
	}

	// Interactive: list machines and let user choose
	machines, err := listMachines(client)
	if err != nil {
		return nil, err
	}

	if len(machines) == 0 {
		// No machines — prompt to create one
		fmt.Println("  No machines found. Let's create one.")
		name, err := promptString("  Machine name: ")
		if err != nil {
			return nil, err
		}
		if name == "" {
			return nil, fmt.Errorf("machine name cannot be empty")
		}

		return createMachineForSetup(client, name)
	}

	// Show existing machines
	fmt.Println("  Select a machine:")
	for i, m := range machines {
		num := fmt.Sprintf("%d.", i+1)
		status := m.Status
		defaultMarker := ""
		if m.ID == cfg.DefaultMachineID {
			defaultMarker = " " + cGreen("*")
		}
		if i == 0 {
			fmt.Printf("  %s %s %s%s\n", cBold(num), cWhite(m.Name), cDim("("+status+")"), defaultMarker)
		} else {
			fmt.Printf("  %s %s %s%s\n", cMuted(num), m.Name, cDim("("+status+")"), defaultMarker)
		}
	}
	fmt.Printf("  %s %s\n", cMuted(fmt.Sprintf("%d.", len(machines)+1)), "Create new machine")

	answer, err := promptString(fmt.Sprintf("  Select (1-%d): ", len(machines)+1))
	if err != nil {
		return nil, err
	}

	choice := strings.TrimSpace(answer)
	idx := 0
	for i, c := range choice {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("invalid selection: %q", choice)
		}
		idx = idx*10 + int(c-'0')
		_ = i
	}

	if idx < 1 || idx > len(machines)+1 {
		return nil, fmt.Errorf("invalid selection: %q (must be 1-%d)", choice, len(machines)+1)
	}

	if idx == len(machines)+1 {
		// Create new
		name, err := promptString("  Machine name: ")
		if err != nil {
			return nil, err
		}
		if name == "" {
			return nil, fmt.Errorf("machine name cannot be empty")
		}
		return createMachineForSetup(client, name)
	}

	// Selected existing machine
	m := &machines[idx-1]
	if err := setDefaultMachine(m); err != nil {
		return nil, fmt.Errorf("saving default machine: %w", err)
	}
	return m, nil
}

// createMachineForSetup creates a new machine and sets it as default.
func createMachineForSetup(client *api.Client, name string) (*api.Machine, error) {
	reqBody, err := json.Marshal(map[string]any{
		"name":       name,
		"auto_start": true,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}

	path := fmt.Sprintf("/api/accounts/%d/machines", cfg.DefaultAccountID)
	resp, err := client.PostLong(path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("creating machine: %w", err)
	}
	defer resp.Body.Close()

	var machine api.Machine
	if err := readJSON(&resp.Body, resp.StatusCode, 201, &machine); err != nil {
		return nil, err
	}

	if err := setDefaultMachine(&machine); err != nil {
		return nil, fmt.Errorf("saving default machine: %w", err)
	}

	fmt.Printf("  Machine %q created.\n", machine.Name)
	return &machine, nil
}

// setupProviderInteractive runs the interactive provider setup for a single provider.
func setupProviderInteractive(_ *cobra.Command, client *api.Client, machine *api.Machine, provider, label string) error {
	credType := inferCredentialType(provider)
	keyPrompt := fmt.Sprintf("  Enter API key for %s: ", provider)

	// LLM providers with subscription plans get a choice
	if subInfo, ok := subscriptionInfo[provider]; ok {
		info := providerInstructions[provider]
		fmt.Println("  How do you want to authenticate?")
		fmt.Printf("  1. %s (paste %s)\n", subInfo.AuthName, subInfo.TokenName)
		fmt.Println("  2. API key (pay-per-use)")
		choice, err := promptString("  Select (1 or 2): ")
		if err != nil {
			return err
		}
		switch strings.TrimSpace(choice) {
		case "1", "":
			credType = "subscription_key"
			keyPrompt = fmt.Sprintf("  Paste %s %s: ", info.DisplayName, subInfo.TokenName)
			fmt.Println()
			printStyledTokenBox(subInfo.TokenCmd)
		case "2":
			credType = "api_key"
			keyPrompt = "  Enter API key: "
		default:
			return fmt.Errorf("invalid choice %q: enter 1 or 2", choice)
		}
	}

	key, err := readMaskedSecret(keyPrompt)
	if err != nil {
		return fmt.Errorf("reading key: %w", err)
	}

	// Validate locally
	fmt.Print("  Validating... ")
	if err := validateCredential(provider, key); err != nil {
		fmt.Printf("  %s %s\n", failMark(), cRed("Validation failed"))
		return fmt.Errorf("key validation failed: %w", err)
	}
	fmt.Printf("  %s %s\n", checkMark(), cGreen("Key validated"))

	return storeAndLinkCredential(client, machine, provider, key, credType, label)
}

// setupChannelInteractive runs the interactive channel setup for a single channel.
func setupChannelInteractive(_ *cobra.Command, client *api.Client, machine *api.Machine, channel, label string) error {
	info, ok := channelInstructions[channel]
	if !ok {
		return fmt.Errorf("unknown channel %q", channel)
	}

	// Show setup instructions
	fmt.Printf("  Get your bot token from: %s\n\n", cBlue(info.TokenURL))
	for i, step := range info.Steps {
		fmt.Printf("  %s %s\n", cDim(fmt.Sprintf("%d.", i+1)), step)
	}
	fmt.Println()

	token, err := readMaskedSecret(fmt.Sprintf("  Enter bot token for %s: ", channel))
	if err != nil {
		return fmt.Errorf("reading token: %w", err)
	}

	// Validate locally
	fmt.Print("  Validating... ")
	if err := validateCredential(info.Provider, token); err != nil {
		fmt.Printf("  %s %s\n", failMark(), cRed("Validation failed"))
		return fmt.Errorf("token validation failed: %w", err)
	}
	fmt.Printf("  %s %s\n", checkMark(), cGreen("Key validated"))

	credType := inferCredentialType(channel)
	if err := storeAndLinkCredential(client, machine, channel, token, credType, label); err != nil {
		return err
	}

	return enableChannel(client, machine, channel)
}

// setupIdentityInteractive prompts for and sets the machine identity.
func setupIdentityInteractive(client *api.Client, machine *api.Machine) error {
	name, err := promptString("  Enter identity name: ")
	if err != nil {
		return err
	}
	avatar, err := promptString("  Enter avatar URL (or leave blank): ")
	if err != nil {
		return err
	}

	if name == "" && avatar == "" {
		fmt.Println("  Skipped (no values entered).")
		return nil
	}

	return setMachineIdentity(client, machine, name, avatar)
}

// setMachineIdentity sets the identity (name/avatar) on a machine.
func setMachineIdentity(client *api.Client, machine *api.Machine, name, avatar string) error {
	body := map[string]string{}
	if name != "" {
		body["name"] = name
	}
	if avatar != "" {
		body["avatar"] = avatar
	}
	reqBody, _ := json.Marshal(body)

	path := fmt.Sprintf("/api/accounts/%d/machines/%s/identity", cfg.DefaultAccountID, machine.ID)
	resp, err := client.Put(path, reqBody)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := readJSON(&resp.Body, resp.StatusCode, 200, nil); err != nil {
		return err
	}
	displayName := name
	if displayName == "" {
		displayName = "custom"
	}
	fmt.Printf("  %s %s %s\n", checkMark(), cGreen("Identity set:"), cWhite(displayName))
	return nil
}

// storeAndLinkCredential stores a credential via PUT and links it to a machine.
func storeAndLinkCredential(client *api.Client, machine *api.Machine, provider, key, credType, label string) error {
	reqBody, err := json.Marshal(map[string]string{
		"value":           key,
		"label":           label,
		"credential_type": credType,
	})
	if err != nil {
		return err
	}

	// Store credential
	path := fmt.Sprintf("/api/accounts/%d/credentials/%s", cfg.DefaultAccountID, provider)
	resp, err := client.Put(path, reqBody)
	if err != nil {
		return fmt.Errorf("storing credential: %w", err)
	}
	defer resp.Body.Close()

	var cred api.Credential
	if err := readJSON(&resp.Body, resp.StatusCode, 200, &cred); err != nil {
		return err
	}

	// Link to machine
	linkPath := fmt.Sprintf("/api/accounts/%d/machines/%s/credentials/%d", cfg.DefaultAccountID, machine.ID, cred.ID)
	linkResp, err := client.Post(linkPath, nil)
	if err != nil {
		return fmt.Errorf("linking credential: %w", err)
	}
	defer linkResp.Body.Close()

	if linkResp.StatusCode != 200 && linkResp.StatusCode != 201 {
		body, _ := io.ReadAll(linkResp.Body)
		return fmt.Errorf("linking credential: %w", apiError(linkResp.StatusCode, string(body)))
	}

	fmt.Printf("  %s %s\n", checkMark(), cGreen(provider+" credential added"))
	fmt.Printf("  %s %s %s\n", checkMark(), cGreen("Linked to machine"), cWhite(machine.Name))
	return nil
}

// enableChannel enables a channel capability on a machine.
func enableChannel(client *api.Client, machine *api.Machine, channel string) error {
	capBody, _ := json.Marshal(map[string]string{"entry_id": channel})
	capPath := fmt.Sprintf("/api/accounts/%d/machines/%s/capabilities", cfg.DefaultAccountID, machine.ID)
	capResp, err := client.Post(capPath, capBody)
	if err != nil {
		return fmt.Errorf("enabling channel: %w", err)
	}
	defer capResp.Body.Close()

	if capResp.StatusCode != 201 {
		body, _ := io.ReadAll(capResp.Body)
		return fmt.Errorf("enabling channel: %w", apiError(capResp.StatusCode, string(body)))
	}

	fmt.Printf("  %s %s enabled on %s\n", checkMark(), cGreen(channel+" channel"), cWhite(machine.Name))
	return nil
}

// fetchMachineCredentials returns credentials linked to a machine (best effort).
func fetchMachineCredentials(client *api.Client, machine *api.Machine) []api.Credential {
	path := fmt.Sprintf("/api/accounts/%d/machines/%s/credentials", cfg.DefaultAccountID, machine.ID)
	resp, err := client.Get(path)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var creds []api.Credential
	if err := readJSON(&resp.Body, resp.StatusCode, 200, &creds); err != nil {
		return nil
	}
	return creds
}

// fetchMachineCapabilities returns capabilities on a machine (best effort).
func fetchMachineCapabilities(client *api.Client, machine *api.Machine) []api.MachineCapability {
	path := fmt.Sprintf("/api/accounts/%d/machines/%s/capabilities", cfg.DefaultAccountID, machine.ID)
	resp, err := client.Get(path)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var caps []api.MachineCapability
	if err := readJSON(&resp.Body, resp.StatusCode, 200, &caps); err != nil {
		return nil
	}
	return caps
}

// checkMachineHealth probes a machine's health endpoint.
func checkMachineHealth(hostname string) bool {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("https://%s/health", hostname))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// printStyledSummary prints the final styled summary after setup completes.
func printStyledSummary(machine *api.Machine, providers, channels []string, completed map[int]bool) {
	lines := []string{
		cGreen("Your machine is ready!"),
		"",
		cDim("Machine:   ") + cWhite(machine.Name) + " " + cDim("("+sizeLabel(machine.VCPUs, machine.MemoryMB)+")"),
	}
	if machine.TunnelHostname != nil {
		lines = append(lines, cDim("URL:       ")+cBlue("https://"+*machine.TunnelHostname))
	}
	if len(providers) > 0 {
		lines = append(lines, cDim("Providers: ")+cWhite(strings.Join(providers, ", ")))
	}
	if len(channels) > 0 {
		lines = append(lines, cDim("Channels:  ")+cWhite(strings.Join(channels, ", ")))
	}

	lines = append(lines, "", cDim("Next steps:"))
	for _, ch := range channels {
		switch ch {
		case "telegram":
			lines = append(lines, cDim("  - ")+cWhite("Message your bot on Telegram"))
		case "discord":
			lines = append(lines, cDim("  - ")+cWhite("Add the bot to your Discord server"))
		case "whatsapp":
			lines = append(lines, cDim("  - ")+cWhite("Send a message on WhatsApp"))
		}
	}
	lines = append(lines, cDim("  - ")+cWhite("Run ")+cBold("ocm status")+cWhite(" to check machine status"))
	lines = append(lines, cDim("  - ")+cWhite("Run ")+cBold("ocm machines ssh")+cWhite(" to access the machine"))

	fmt.Println()
	fmt.Println(summaryBox(lines))

	// Show final progress with all steps complete
	printStyledProgress(len(setupSteps), completed)

	// Set default machine in config
	if err := setDefaultMachine(machine); err != nil {
		fmt.Printf("\n  Warning: could not save default machine: %v\n", err)
	} else if err := config.Save(cfg, cfgFile); err != nil {
		fmt.Printf("\n  Warning: could not save config: %v\n", err)
	}
	fmt.Println()
}
