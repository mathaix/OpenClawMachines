package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/spf13/cobra"
)

var backupsCmd = &cobra.Command{
	Use:   "backups",
	Short: "Manage machine backups",
	RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
}

var backupsListCmd = &cobra.Command{
	Use:   "list [MACHINE]",
	Short: "List backups for a machine",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupsList,
}

var backupsCreateCmd = &cobra.Command{
	Use:   "create [MACHINE]",
	Short: "Create a backup (machine must be stopped)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupsCreate,
}

var backupsDownloadCmd = &cobra.Command{
	Use:   "download [MACHINE]",
	Short: "Download a backup",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupsDownload,
}

var backupsRestoreCmd = &cobra.Command{
	Use:   "restore [MACHINE]",
	Short: "Restore from a backup (machine must be stopped)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupsRestore,
}

var backupsEnableCmd = &cobra.Command{
	Use:   "enable [MACHINE]",
	Short: "Enable backups for a machine",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupsEnable,
}

var backupsDisableCmd = &cobra.Command{
	Use:   "disable [MACHINE]",
	Short: "Disable backups for a machine",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupsDisable,
}

func init() {
	machinesCmd.AddCommand(backupsCmd)
	backupsCmd.AddCommand(backupsListCmd)
	backupsCmd.AddCommand(backupsCreateCmd)
	backupsCmd.AddCommand(backupsDownloadCmd)
	backupsCmd.AddCommand(backupsRestoreCmd)
	backupsCmd.AddCommand(backupsEnableCmd)
	backupsCmd.AddCommand(backupsDisableCmd)

	backupsDownloadCmd.Flags().Int("id", 0, "Backup ID (defaults to latest)")
	backupsDownloadCmd.Flags().String("format", "tar.gz", "Download format: tar.gz or ext4")
	backupsDownloadCmd.Flags().StringP("output", "o", "", "Output file path")

	backupsRestoreCmd.Flags().Int("id", 0, "Backup ID to restore (required)")
	backupsRestoreCmd.MarkFlagRequired("id")
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func resolveMachineForBackups(args []string) (*api.Client, *api.Machine, error) {
	client := newAPIClient()
	var nameOrSlug string
	if len(args) > 0 {
		nameOrSlug = args[0]
	} else if cfg.DefaultMachineID != "" {
		nameOrSlug = cfg.DefaultMachineID
	} else {
		return nil, nil, fmt.Errorf("specify a machine name or use 'ocm machines use' to set a default")
	}
	machine, err := resolveMachine(client, nameOrSlug)
	if err != nil {
		return nil, nil, err
	}
	return client, machine, nil
}

func runBackupsList(cmd *cobra.Command, args []string) error {
	client, machine, err := resolveMachineForBackups(args)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/accounts/%d/machines/%s/backups", cfg.DefaultAccountID, machine.ID)
	resp, err := client.Get(path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var backups []api.Backup
	if err := readJSON(&resp.Body, resp.StatusCode, 200, &backups); err != nil {
		return err
	}

	if isJSONOutput(cmd) {
		printJSON("list_backups", backups)
		return nil
	}

	if len(backups) == 0 {
		fmt.Println("No backups found.")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTIMESTAMP\tSIZE\tTRIGGER\tAGE")
	for _, b := range backups {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n",
			b.ID,
			b.Timestamp.Format("2006-01-02 15:04:05"),
			formatBytes(b.CompressedBytes),
			b.Trigger,
			timeAgo(b.Timestamp),
		)
	}
	return tw.Flush()
}

func runBackupsCreate(cmd *cobra.Command, args []string) error {
	client, machine, err := resolveMachineForBackups(args)
	if err != nil {
		return err
	}

	fmt.Printf("Creating backup for %s...\n", machine.Name)

	path := fmt.Sprintf("/api/accounts/%d/machines/%s/backups", cfg.DefaultAccountID, machine.ID)
	resp, err := client.PostLong(path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var backup api.Backup
	if err := readJSON(&resp.Body, resp.StatusCode, 201, &backup); err != nil {
		return err
	}

	if isJSONOutput(cmd) {
		printJSON("create_backup", backup)
		return nil
	}

	fmt.Printf("Backup created (id: %d, size: %s)\n", backup.ID, formatBytes(backup.CompressedBytes))
	return nil
}

func runBackupsDownload(cmd *cobra.Command, args []string) error {
	client, machine, err := resolveMachineForBackups(args)
	if err != nil {
		return err
	}

	backupID, _ := cmd.Flags().GetInt("id")
	format, _ := cmd.Flags().GetString("format")
	output, _ := cmd.Flags().GetString("output")

	// If no ID specified, get latest
	if backupID == 0 {
		listPath := fmt.Sprintf("/api/accounts/%d/machines/%s/backups", cfg.DefaultAccountID, machine.ID)
		resp, err := client.Get(listPath)
		if err != nil {
			return err
		}
		var backups []api.Backup
		if err := readJSON(&resp.Body, resp.StatusCode, 200, &backups); err != nil {
			return err
		}
		resp.Body.Close()
		if len(backups) == 0 {
			return fmt.Errorf("no backups found for %s", machine.Name)
		}
		backupID = backups[0].ID
	}

	dlPath := fmt.Sprintf("/api/accounts/%d/machines/%s/backups/%d/download?format=%s",
		cfg.DefaultAccountID, machine.ID, backupID, format)

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := client.GetStream(ctx, dlPath)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return apiError(resp.StatusCode, string(body))
	}

	if output == "" {
		ext := "tar.gz"
		if format == "ext4" {
			ext = "ext4"
		}
		output = fmt.Sprintf("%s-backup-%d.%s", machine.Slug, backupID, ext)
	}

	f, err := os.Create(output)
	if err != nil {
		return err
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return err
	}

	fmt.Printf("Downloaded %s (%s)\n", output, formatBytes(n))
	return nil
}

func runBackupsRestore(cmd *cobra.Command, args []string) error {
	client, machine, err := resolveMachineForBackups(args)
	if err != nil {
		return err
	}

	backupID, _ := cmd.Flags().GetInt("id")

	fmt.Printf("Restoring backup %d for %s...\n", backupID, machine.Name)

	path := fmt.Sprintf("/api/accounts/%d/machines/%s/backups/%d/restore",
		cfg.DefaultAccountID, machine.ID, backupID)
	resp, err := client.PostLong(path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := readJSON(&resp.Body, resp.StatusCode, 200, nil); err != nil {
		return err
	}

	if isJSONOutput(cmd) {
		printJSON("restore_backup", map[string]int{"backup_id": backupID})
		return nil
	}

	fmt.Printf("Restored from backup %d\n", backupID)
	return nil
}

func runBackupsEnable(cmd *cobra.Command, args []string) error {
	client, machine, err := resolveMachineForBackups(args)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/accounts/%d/machines/%s", cfg.DefaultAccountID, machine.ID)
	body := []byte(`{"backups_enabled": true}`)
	resp, err := client.Put(path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := readJSON(&resp.Body, resp.StatusCode, 200, nil); err != nil {
		return err
	}

	if isJSONOutput(cmd) {
		printJSON("enable_backups", map[string]bool{"backups_enabled": true})
		return nil
	}

	fmt.Printf("Backups enabled for %s\n", machine.Name)
	return nil
}

func runBackupsDisable(cmd *cobra.Command, args []string) error {
	client, machine, err := resolveMachineForBackups(args)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/accounts/%d/machines/%s", cfg.DefaultAccountID, machine.ID)
	body := []byte(`{"backups_enabled": false}`)
	resp, err := client.Put(path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := readJSON(&resp.Body, resp.StatusCode, 200, nil); err != nil {
		return err
	}

	if isJSONOutput(cmd) {
		printJSON("disable_backups", map[string]bool{"backups_enabled": false})
		return nil
	}

	fmt.Printf("Backups disabled for %s\n", machine.Name)
	return nil
}
