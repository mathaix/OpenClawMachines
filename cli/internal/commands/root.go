package commands

import (
	"fmt"
	"os"

	"github.com/mathaix/openclawmachines/cli/internal/config"
	"github.com/spf13/cobra"
)

var (
	cfgFile string
	cfg     *config.Config
)

var rootCmd = &cobra.Command{
	Use:   "ocm",
	Short: "OCM CLI — manage OpenClaw Machines",
	Long:  "OCM CLI — create, manage, and monitor OpenClaw Machines from the command line.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Skip config loading for version command
		if cmd.Name() == "version" {
			return nil
		}
		var err error
		cfg, err = config.Load(cfgFile)
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}

		// Refresh platform config if stale (silently ignore errors)
		if cfg.APIURL != "" && cfg.PlatformConfigStale() {
			if err := config.FetchPlatformConfig(cfg); err == nil {
				_ = config.Save(cfg, cfgFile)
			}
		}

		return nil
	},
	Run: func(cmd *cobra.Command, args []string) {
		if !isJSONOutput(cmd) {
			playBanner(os.Stdout)
			printStyledHelp()
		} else {
			cmd.Help()
		}
	},
	SilenceUsage: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default ~/.config/ocm/config.json)")
	rootCmd.PersistentFlags().Bool("json", false, "Output results as JSON")
}

// requireLogin validates that the CLI is configured and authenticated.
func requireLogin() error {
	if cfg.APIURL == "" {
		return fmt.Errorf("API URL not configured — run 'ocm config set-api-url <url>'")
	}
	if cfg.Token == "" {
		return fmt.Errorf("not logged in — run 'ocm login' first")
	}
	if cfg.DefaultAccountID == 0 {
		return fmt.Errorf("no account selected — run 'ocm login'")
	}
	return nil
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
