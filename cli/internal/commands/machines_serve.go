package commands

import (
	"github.com/spf13/cobra"
)

// machinesServeCmd is the parent for all `ocm machines serve <protocol>` commands.
// Each subcommand (chat, voice, realtime, ...) exposes a local endpoint that
// forwards to the machine's gateway in a protocol-specific shape.
var machinesServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Expose a machine's gateway endpoint locally (chat, voice, ...)",
	Long: `Serve a local endpoint backed by a running machine's gateway.

Subcommands:
  chat      OpenAI-compatible chat completions on http://localhost:<port>/v1

Additional protocol subcommands (voice, realtime, vision) will be added as
they land. Use 'ocm machines serve <protocol> --help' for flags.`,
}

func init() {
	machinesCmd.AddCommand(machinesServeCmd)
}
