package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Set at build time via ldflags.
var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildTime = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the OCM CLI version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("ocm version %s\n", Version)
		fmt.Printf("  commit:  %s\n", GitCommit)
		fmt.Printf("  built:   %s\n", BuildTime)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
