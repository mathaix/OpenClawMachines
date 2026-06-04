package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var machinesBrowseCmd = &cobra.Command{
	Use:   "browse [NAME]",
	Short: "Connect a local Chrome browser to a machine's CDP endpoint",
	Long: `Launch a local Chrome browser with remote debugging enabled, then tunnel
the Chrome DevTools Protocol (CDP) port into the machine via SSH reverse tunnel.

This allows the machine's gateway to control your local Chrome instance for
browser automation tasks.

Examples:
  ocm machines browse
  ocm machines browse "My Bot"
  ocm machines browse --port 9223
  ocm machines browse --no-launch --chrome-path /usr/bin/chromium`,
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

		port, _ := cmd.Flags().GetInt("port")
		noLaunch, _ := cmd.Flags().GetBool("no-launch")
		chromePath, _ := cmd.Flags().GetString("chrome-path")

		// launchedChrome tracks whether we launched Chrome (for cleanup on macOS).
		launchedChrome := false

		client := newAPIClient()
		machine, err := resolveMachine(client, name)
		if err != nil {
			return err
		}

		if machine.Status != "running" {
			return fmt.Errorf("machine %q is not running (status: %s)", machine.Name, machine.Status)
		}

		// --- Launch Chrome (unless --no-launch) ---
		var chromeProc *os.Process
		if !noLaunch {
			chromeBin, err := findChrome(chromePath)
			if err != nil {
				return err
			}

			// Use a dedicated user data dir under the OCM config path so
			// Chrome starts a NEW instance with remote debugging enabled,
			// even if the user's main Chrome is already running.
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("getting home directory: %w", err)
			}
			userDataDir := filepath.Join(home, ".config", "ocm", "chrome-profile")
			if err := os.MkdirAll(userDataDir, 0o755); err != nil {
				return fmt.Errorf("creating Chrome profile dir: %w", err)
			}
			// Remove stale lock files from previous sessions
			_ = os.Remove(filepath.Join(userDataDir, "SingletonLock"))
			_ = os.Remove(filepath.Join(userDataDir, "SingletonSocket"))
			_ = os.Remove(filepath.Join(userDataDir, "SingletonCookie"))
			chromeFlags := []string{
				fmt.Sprintf("--remote-debugging-port=%d", port),
				fmt.Sprintf("--user-data-dir=%s", userDataDir),
				"--no-first-run",
				"--no-default-browser-check",
				"--disable-blink-features=AutomationControlled",
			}

			var chromeCmd *exec.Cmd
			if runtime.GOOS == "darwin" {
				// On macOS, use "open -n -a" via sh -c to force a new Chrome
				// instance. Direct binary execution delegates to the existing
				// Chrome process and drops flags like --user-data-dir.
				shellCmd := fmt.Sprintf("open -n -a %q --args", chromeBin)
				for _, f := range chromeFlags {
					shellCmd += fmt.Sprintf(" %s", f)
				}
				fmt.Fprintf(os.Stderr, "Launching: %s\n", shellCmd)
				chromeCmd = exec.Command("sh", "-c", shellCmd)
			} else {
				chromeCmd = exec.Command(chromeBin, chromeFlags...)
			}
			chromeCmd.Stdout = os.Stderr
			chromeCmd.Stderr = os.Stderr
			if err := chromeCmd.Start(); err != nil {
				return fmt.Errorf("starting Chrome: %w", err)
			}
			if runtime.GOOS != "darwin" {
				chromeProc = chromeCmd.Process
			}
			launchedChrome = true
			fmt.Fprintf(os.Stderr, "Chrome launching with remote debugging on port %d\n", port)
		} else {
			fmt.Fprintf(os.Stderr, "Skipping Chrome launch (--no-launch). Ensure Chrome is running with --remote-debugging-port=%d\n", port)
		}

		// --- Verify Chrome CDP is reachable locally ---
		fmt.Fprintf(os.Stderr, "Waiting for Chrome CDP on localhost:%d ", port)
		cdpReady := false
		for i := 0; i < 30; i++ {
			resp, err := http.Get(fmt.Sprintf("http://localhost:%d/json/version", port))
			if err == nil {
				versionBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				fmt.Fprintf(os.Stderr, "\n")
				var vInfo map[string]interface{}
				if json.Unmarshal(versionBody, &vInfo) == nil {
					if wsURL, ok := vInfo["webSocketDebuggerUrl"].(string); ok {
						fmt.Fprintf(os.Stderr, "  DevTools listening on %s\n", wsURL)
					}
				}
				fmt.Fprintf(os.Stderr, "  CDP ready: %s\n", string(versionBody))
				cdpReady = true
				break
			}
			fmt.Fprintf(os.Stderr, ".")
			time.Sleep(500 * time.Millisecond)
		}
		if !cdpReady {
			fmt.Fprintf(os.Stderr, "\n")
			killChrome(chromeProc, launchedChrome)
			return fmt.Errorf("Chrome CDP not responding on localhost:%d after 15s.\n  Chrome may have joined an existing session instead of starting with remote debugging.\n  Close all Chrome windows and try again, or use --chrome-path to specify a different browser", port)
		}

		// --- Fetch SSH cert from backend and start reverse tunnel ---
		certResult, err := fetchSSHCert(machine.ID)
		if err != nil {
			killChrome(chromeProc, launchedChrome)
			return fmt.Errorf("obtaining SSH certificate: %w", err)
		}
		defer cleanupSSHCert(certResult)

		// Use backend-returned hostname, fall back to derived if empty
		sshHost := certResult.Hostname
		if sshHost == "" {
			sshHost = fmt.Sprintf("ssh-%s.%s", machine.Slug, cfg.GetDataPlaneDomain())
		}

		self, err := os.Executable()
		if err != nil {
			killChrome(chromeProc, launchedChrome)
			return fmt.Errorf("resolving own executable path: %w", err)
		}

		// Quote paths to handle spaces
		proxyCmd := fmt.Sprintf("%q machines ssh-proxy %%h", self)
		if cfgFile != "" {
			proxyCmd = fmt.Sprintf("%q --config %q machines ssh-proxy %%h", self, cfgFile)
		}

		sshBin, err := exec.LookPath("ssh")
		if err != nil {
			killChrome(chromeProc, launchedChrome)
			return fmt.Errorf("ssh not found in PATH: %w", err)
		}

		// Use backend-returned username
		sshUser := "openclaw"
		if certResult.Username != "" {
			sshUser = certResult.Username
		}

		sshArgs := []string{
			"ssh",
			"-l", sshUser,
			"-i", certResult.KeyPath,
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "IdentitiesOnly=yes",
			"-o", fmt.Sprintf("ProxyCommand=%s", proxyCmd),
			"-R", fmt.Sprintf("0.0.0.0:9222:localhost:%d", port),
			"-N",
			sshHost,
		}

		fmt.Fprintf(os.Stderr, "SSH args: %v\n", sshArgs[1:])
		sshCmd := exec.Command(sshBin, sshArgs[1:]...)
		sshCmd.Stdout = os.Stderr
		sshCmd.Stderr = os.Stderr
		if err := sshCmd.Start(); err != nil {
			killChrome(chromeProc, launchedChrome)
			return fmt.Errorf("starting SSH tunnel: %w", err)
		}
		fmt.Fprintf(os.Stderr, "SSH reverse tunnel started (PID %d): remote 0.0.0.0:9222 -> localhost:%d\n", sshCmd.Process.Pid, port)

		// Wait for tunnel to establish
		fmt.Fprintf(os.Stderr, "Waiting 3s for tunnel to establish...\n")
		time.Sleep(3 * time.Second)

		// Check SSH is still alive after the wait
		if sshCmd.ProcessState != nil {
			killChrome(chromeProc, launchedChrome)
			return fmt.Errorf("SSH tunnel exited prematurely")
		}
		fmt.Fprintf(os.Stderr, "SSH tunnel alive (PID %d)\n", sshCmd.Process.Pid)

		// --- Start browse session (CDP target + browser config in one call) ---
		browsePath := fmt.Sprintf("/api/accounts/%d/machines/%s/browse-session", cfg.DefaultAccountID, machine.ID)
		fmt.Fprintf(os.Stderr, "Starting browse session...\n")
		// CDP target is always 9222 inside the VM (the SSH reverse tunnel
		// binds -R 0.0.0.0:9222:localhost:<local-port>), regardless of --port.
		reqBody, err := json.Marshal(map[string]string{
			"cdp_target": "127.0.0.1:9222",
		})
		if err != nil {
			killProc(sshCmd.Process)
			killChrome(chromeProc, launchedChrome)
			return fmt.Errorf("encoding request: %w", err)
		}

		browseResp, err := client.Post(browsePath, reqBody)
		if err != nil {
			killProc(sshCmd.Process)
			killChrome(chromeProc, launchedChrome)
			return fmt.Errorf("starting browse session: %w", err)
		}
		body, _ := io.ReadAll(browseResp.Body)
		browseResp.Body.Close()
		if browseResp.StatusCode != 201 {
			killProc(sshCmd.Process)
			killChrome(chromeProc, launchedChrome)
			return fmt.Errorf("starting browse session: %s", apiError(browseResp.StatusCode, string(body)))
		}

		var browseResult struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(body, &browseResult)
		if browseResult.SessionID == "" {
			killProc(sshCmd.Process)
			killChrome(chromeProc, launchedChrome)
			return fmt.Errorf("browse session created but response missing session_id")
		}
		sessionPath := fmt.Sprintf("%s/%s", browsePath, browseResult.SessionID)
		fmt.Fprintf(os.Stderr, "Browse session started: %s\n", browseResult.SessionID)

		fmt.Fprintf(os.Stderr, "\nBrowser connected to machine %q.\n", machine.Name)
		fmt.Fprintf(os.Stderr, "Press Ctrl+C to disconnect.\n\n")

		// --- Wait for signal, with heartbeat ---
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

		sshDone := make(chan error, 1)
		go func() {
			sshDone <- sshCmd.Wait()
		}()

		heartbeat := time.NewTicker(60 * time.Second)
		defer heartbeat.Stop()

	waitLoop:
		for {
			select {
			case sig := <-sigCh:
				fmt.Fprintf(os.Stderr, "\nReceived %s, cleaning up...\n", sig)
				break waitLoop
			case err := <-sshDone:
				if err != nil {
					fmt.Fprintf(os.Stderr, "\nSSH tunnel exited: %v\n", err)
				} else {
					fmt.Fprintf(os.Stderr, "\nSSH tunnel exited.\n")
				}
				break waitLoop
			case <-heartbeat.C:
				resp, hbErr := client.Put(sessionPath+"/heartbeat", nil)
				if hbErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: heartbeat failed: %v\n", hbErr)
				} else {
					resp.Body.Close()
				}
			}
		}

		// --- Cleanup: delete browse session (handles CDP + config) ---
		delResp, err := client.Delete(sessionPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to end browse session: %v\n", err)
		} else {
			delResp.Body.Close()
		}

		// Kill SSH (if still running)
		killProc(sshCmd.Process)

		// Kill Chrome (if we launched it)
		killChrome(chromeProc, launchedChrome)
		if launchedChrome {
			fmt.Fprintf(os.Stderr, "Chrome stopped.\n")
		}

		fmt.Fprintf(os.Stderr, "Disconnected.\n")
		return nil
	},
}

// findChrome locates the Chrome/Chromium binary.
// On macOS it returns the .app bundle path (used with "open -n -a" to force a new instance).
// On Linux it returns the binary path directly.
func findChrome(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("chrome not found at %q: %w", explicit, err)
		}
		return explicit, nil
	}

	if runtime.GOOS == "darwin" {
		macPaths := []string{
			"/Applications/Google Chrome.app",
			"/Applications/Chromium.app",
		}
		for _, p := range macPaths {
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}

	// Linux / fallback: search PATH
	candidates := []string{"google-chrome", "google-chrome-stable", "chromium-browser", "chromium"}
	for _, name := range candidates {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("Chrome/Chromium not found. Install Chrome or specify --chrome-path")
}

// killChrome stops Chrome, handling the macOS case where we don't have
// a direct process handle (launched via "open -n -a").
func killChrome(proc *os.Process, launched bool) {
	if proc != nil {
		killProc(proc)
		return
	}
	if launched && runtime.GOOS == "darwin" {
		_ = exec.Command("pkill", "-f", "user-data-dir=.*ocm.*chrome-profile").Run()
	}
}

// killProc sends SIGTERM, waits 3s, then SIGKILL if needed. Nil-safe.
func killProc(p *os.Process) {
	if p == nil {
		return
	}

	_ = p.Signal(syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		p.Wait() //nolint:errcheck
		close(done)
	}()

	select {
	case <-done:
		return
	case <-time.After(3 * time.Second):
		_ = p.Signal(syscall.SIGKILL)
		<-done
	}
}

func init() {
	machinesBrowseCmd.Flags().Int("port", 9222, "Chrome remote debugging port")
	machinesBrowseCmd.Flags().Bool("no-launch", false, "Don't launch Chrome (assume it's already running)")
	machinesBrowseCmd.Flags().String("chrome-path", "", "Path to Chrome/Chromium binary")

	machinesCmd.AddCommand(machinesBrowseCmd)
}
