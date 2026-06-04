package commands

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

var machinesSSHCmd = &cobra.Command{
	Use:   "ssh [NAME] [-- SSH_ARGS...]",
	Short: "SSH into a running machine",
	Long: `SSH into a running machine via its Cloudflare tunnel.

Extra arguments after "--" are passed through to the ssh command.
If connection fails, run "ocm machines ssh-debug" to diagnose.

Examples:
  ocm machines ssh
  ocm machines ssh "My Bot"
  ocm machines ssh "My Bot" --user root
  ocm machines ssh "My Bot" -- -L 8080:localhost:8080`,
	Args:               cobra.ArbitraryArgs,
	ValidArgsFunction:  completeMachineNames,
	DisableFlagParsing: false,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireLogin(); err != nil {
			return err
		}

		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		user, _ := cmd.Flags().GetString("user")

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		if machine.Status != "running" {
			return fmt.Errorf("machine %q is not running (status: %s)", machine.Name, machine.Status)
		}

		// Generate ephemeral keypair and fetch signed SSH certificate from backend
		certResult, err := fetchSSHCert(machine.ID)
		if err != nil {
			return fmt.Errorf("obtaining SSH certificate: %w", err)
		}

		// Use backend-returned hostname, fall back to derived if empty
		sshHost := certResult.Hostname
		if sshHost == "" {
			sshHost = fmt.Sprintf("ssh-%s.%s", machine.Slug, cfg.GetDataPlaneDomain())
		}

		// Use backend-returned username if user didn't override via flag
		if user == "openclaw" && certResult.Username != "" {
			user = certResult.Username
		}

		// Use our own binary as the SSH ProxyCommand (replaces external cloudflared).
		self, err := os.Executable()
		if err != nil {
			cleanupSSHCert(certResult)
			return fmt.Errorf("resolving own executable path: %w", err)
		}

		// Quote paths to handle spaces in executable path or config file path
		proxyCmd := fmt.Sprintf("%q machines ssh-proxy %%h", self)
		if cfgFile != "" {
			proxyCmd = fmt.Sprintf("%q --config %q machines ssh-proxy %%h", self, cfgFile)
		}

		// Build ssh argument list with ProxyCommand and cert identity
		sshArgs := []string{"ssh"}
		sshArgs = append(sshArgs, "-l", user)
		sshArgs = append(sshArgs, "-i", certResult.KeyPath)
		sshArgs = append(sshArgs, "-o", "StrictHostKeyChecking=no")
		sshArgs = append(sshArgs, "-o", "UserKnownHostsFile=/dev/null")
		sshArgs = append(sshArgs, "-o", "IdentitiesOnly=yes")
		sshArgs = append(sshArgs, "-o", fmt.Sprintf("ProxyCommand=%s", proxyCmd))
		sshArgs = append(sshArgs, sshHost)

		// Append any extra args passed after "--"
		if dashIdx := cmd.ArgsLenAtDash(); dashIdx >= 0 {
			sshArgs = append(sshArgs, args[dashIdx:]...)
		}

		// Find the ssh binary
		sshBin, err := exec.LookPath("ssh")
		if err != nil {
			cleanupSSHCert(certResult)
			return fmt.Errorf("ssh not found in PATH: %w", err)
		}

		fmt.Fprintf(os.Stderr, "Connecting to %s@%s ...\n", user, sshHost)

		// Clean up temp keys before exec (defer won't run after syscall.Exec).
		// The ssh process only needs the key files during initial handshake,
		// but they must exist when ssh starts. We use a helper that removes
		// them after a short delay in a background goroutine.
		scheduleCleanup(certResult)

		// Replace process with ssh (standard CLI pattern)
		return syscall.Exec(sshBin, sshArgs, os.Environ())
	},
}

// scheduleCleanup removes temp key/cert files after the SSH process exits.
// syscall.Exec replaces the process so defers don't run — we fork a
// background process that watches our PID (which becomes the ssh process
// after exec) and cleans up when it exits.
func scheduleCleanup(result *sshCertResult) {
	if result == nil {
		return
	}
	pid := os.Getpid()
	// Wait for the SSH process (our PID after syscall.Exec) to exit, then
	// remove the temp files. The "kill -0" check is a no-op signal that
	// returns success while the process exists.
	cleanupCmd := exec.Command("sh", "-c",
		fmt.Sprintf("while kill -0 %d 2>/dev/null; do sleep 1; done; rm -f %s %s",
			pid,
			shellQuote(result.KeyPath),
			shellQuote(result.CertPath)))
	cleanupCmd.Stdout = nil
	cleanupCmd.Stderr = nil
	_ = cleanupCmd.Start()
	// Don't wait — the background process will outlive us after exec.
}

// shellQuote wraps a string in single quotes for safe shell interpolation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func init() {
	machinesSSHCmd.Flags().String("user", "openclaw", "SSH username")

	machinesCmd.AddCommand(machinesSSHCmd)
}
