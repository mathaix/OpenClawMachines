package commands

import (
	"github.com/mathaix/openclawmachines/cli/internal/tunnel"
	"github.com/spf13/cobra"
)

var machinesSSHProxyCmd = &cobra.Command{
	Use:    "ssh-proxy HOSTNAME",
	Short:  "Proxy stdin/stdout over a Cloudflare Access WebSocket tunnel",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return tunnel.Connect(args[0])
	},
}

func init() {
	machinesCmd.AddCommand(machinesSSHProxyCmd)
}
