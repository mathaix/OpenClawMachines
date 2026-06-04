package commands

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"
)

var machinesLogsCmd = &cobra.Command{
	Use:   "logs [NAME]",
	Short: "Stream logs from a running machine",
	Long: `Stream logs from a running machine.

Connects to the backend logs endpoint and prints log output to stdout.
Use --follow to keep the connection open and stream new logs as they arrive.

Examples:
  ocm machines logs
  ocm machines logs "My Bot"
  ocm machines logs --follow
  ocm machines logs "My Bot" -f --lines 50`,
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
		follow, _ := cmd.Flags().GetBool("follow")
		lines, _ := cmd.Flags().GetInt("lines")

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		if machine.Status != "running" {
			return fmt.Errorf("machine %q is not running (status: %s)", machine.Name, machine.Status)
		}

		// Build query parameters
		path := fmt.Sprintf("/api/accounts/%d/machines/%s/logs", cfg.DefaultAccountID, machine.ID)
		var params []string
		if lines > 0 {
			params = append(params, fmt.Sprintf("lines=%d", lines))
		}
		if follow {
			params = append(params, "follow=true")
		}
		if len(params) > 0 {
			path = path + "?" + strings.Join(params, "&")
		}

		// Set up a cancellable context for Ctrl+C handling.
		// For follow mode we use GetStream (no HTTP timeout) so the
		// long-lived connection is not killed by the default 30 s timeout.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		defer signal.Stop(sigCh)

		go func() {
			<-sigCh
			cancel()
		}()

		var resp *http.Response
		if follow {
			// Use streaming client (no timeout) for long-lived follow connections
			resp, err = client.GetStream(ctx, path)
		} else {
			resp, err = client.Get(path)
		}
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			return apiError(resp.StatusCode, string(body))
		}

		// The backend streams logs as text/event-stream (SSE).
		// Stream output to stdout until the connection closes or we get interrupted.

		if follow {
			// In follow mode, stream continuously until interrupted or EOF
			scanner := bufio.NewScanner(resp.Body)
			// Increase buffer size for potentially long log lines
			scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for scanner.Scan() {
				fmt.Fprintln(os.Stdout, scanner.Text())
			}
			if err := scanner.Err(); err != nil && ctx.Err() == nil {
				return fmt.Errorf("reading logs: %w", err)
			}
		} else {
			// Non-follow mode: read all available output and exit
			if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
				return fmt.Errorf("reading logs: %w", err)
			}
		}

		return nil
	},
}

func init() {
	machinesLogsCmd.Flags().BoolP("follow", "f", false, "follow log output (stream)")
	machinesLogsCmd.Flags().Int("lines", 100, "number of log lines to retrieve")

	machinesCmd.AddCommand(machinesLogsCmd)
}
