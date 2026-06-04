package commands

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var machinesSSHDebugCmd = &cobra.Command{
	Use:   "ssh-debug [NAME]",
	Short: "Diagnose SSH connectivity issues",
	Long: `Run diagnostic checks on the SSH connection chain for a machine.

Checks DNS resolution, tunnel reachability, SSH certificate issuance,
and cert validity. Useful when SSH prompts for a password instead of using
certificate auth.`,
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

		client := newAPIClient()
		var checks []checkResult

		// ── 1. Machine exists & running ──
		machine, err := resolveMachine(client, name)
		if err != nil {
			checks = append(checks, checkResult{
				Name:   "Machine exists",
				Status: "fail",
				Detail: err.Error(),
				Hint:   "run ocm machines list",
			})
			return sshDebugOutput(cmd, checks)
		}

		if machine.Status != "running" {
			checks = append(checks, checkResult{
				Name:   "Machine is running",
				Status: "fail",
				Detail: fmt.Sprintf("status: %s", machine.Status),
				Hint:   "run ocm machines start",
			})
			return sshDebugOutput(cmd, checks)
		}

		// Show machine info including versions
		detail := fmt.Sprintf("%q (%s)", machine.Name, machine.Slug)
		if machine.OpenclawVersion != nil {
			detail += fmt.Sprintf("  agent=%s", *machine.OpenclawVersion)
		}
		if machine.RootfsSnapshot != nil {
			detail += fmt.Sprintf("  rootfs=%s", *machine.RootfsSnapshot)
		}
		checks = append(checks, checkResult{
			Name:   "Machine is running",
			Status: "pass",
			Detail: detail,
		})

		// Derive hostnames — always derive from slug, don't depend on tunnel_hostname
		domain := cfg.GetDataPlaneDomain()
		sshHost := fmt.Sprintf("ssh-%s.%s", machine.Slug, domain)
		httpHost := fmt.Sprintf("m-%s.%s", machine.Slug, domain)

		// ── 2. Tunnel hostname ──
		if machine.TunnelHostname == nil || *machine.TunnelHostname == "" {
			checks = append(checks, checkResult{
				Name:   "Tunnel hostname in API",
				Status: "warn",
				Detail: "null (tunnel may still work)",
				Hint:   fmt.Sprintf("using derived: %s", httpHost),
			})
		} else {
			checks = append(checks, checkResult{
				Name:   "Tunnel hostname in API",
				Status: "pass",
				Detail: *machine.TunnelHostname,
			})
		}

		// ── 3. SSH hostname DNS resolves ──
		cname, dnsErr := net.LookupCNAME(sshHost)
		if dnsErr != nil {
			checks = append(checks, checkResult{
				Name:   "SSH DNS resolves",
				Status: "fail",
				Detail: fmt.Sprintf("%s: %s", sshHost, dnsErr.Error()),
				Hint:   "CNAME may not be configured in CF",
			})
		} else {
			checks = append(checks, checkResult{
				Name:   "SSH DNS resolves",
				Status: "pass",
				Detail: fmt.Sprintf("%s → %s", sshHost, strings.TrimSuffix(cname, ".")),
			})
		}

		// ── 4. HTTP tunnel reachable ──
		httpClient := &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		httpURL := fmt.Sprintf("https://%s/", httpHost)
		resp, httpErr := httpClient.Get(httpURL)
		if httpErr != nil {
			checks = append(checks, checkResult{
				Name:   "HTTP tunnel reachable",
				Status: "fail",
				Detail: fmt.Sprintf("%s: %s", httpHost, httpErr.Error()),
				Hint:   "tunnel may not be running inside VM",
			})
		} else {
			resp.Body.Close()
			if resp.StatusCode == 530 {
				checks = append(checks, checkResult{
					Name:   "HTTP tunnel reachable",
					Status: "fail",
					Detail: fmt.Sprintf("%s → HTTP %d (origin unreachable)", httpHost, resp.StatusCode),
					Hint:   "cloudflared may not be running inside VM",
				})
			} else {
				checks = append(checks, checkResult{
					Name:   "HTTP tunnel reachable",
					Status: "pass",
					Detail: fmt.Sprintf("%s → HTTP %d", httpHost, resp.StatusCode),
				})
			}
		}

		// ── 5. SSH tunnel TLS (CF edge) ──
		wsConn, wsErr := tls.DialWithDialer(
			&net.Dialer{Timeout: 5 * time.Second},
			"tcp", sshHost+":443", nil,
		)
		if wsErr != nil {
			checks = append(checks, checkResult{
				Name:   "SSH tunnel TLS (CF edge)",
				Status: "fail",
				Detail: fmt.Sprintf("%s:443: %s", sshHost, wsErr.Error()),
				Hint:   "DNS or Cloudflare edge not reachable",
			})
		} else {
			wsConn.Close()
			checks = append(checks, checkResult{
				Name:   "SSH tunnel TLS (CF edge)",
				Status: "pass",
				Detail: fmt.Sprintf("%s:443 connected", sshHost),
			})
		}

		// ── 6. Platform SSH cert issuance ──
		certResult, certErr := fetchSSHCert(machine.ID)
		if certErr != nil {
			checks = append(checks, checkResult{
				Name:   "SSH cert from backend",
				Status: "fail",
				Detail: certErr.Error(),
				Hint:   "check ocm login and machine status",
			})
		} else {
			defer cleanupSSHCert(certResult)

			checks = append(checks, checkResult{
				Name:   "SSH cert from backend",
				Status: "pass",
				Detail: fmt.Sprintf("cert issued for %s", certResult.Hostname),
			})

			// ── 7. Verify cert validity ──
			certCheck := inspectSSHCert(certResult.CertPath)
			checks = append(checks, certCheck)

			// ── 8. Tunnel auth (CF Access removed from SSH routes) ──
			checks = append(checks, checkResult{
				Name:   "Tunnel auth",
				Status: "pass",
				Detail: "no CF Access on SSH routes (auth via SSH cert)",
			})
		}

		return sshDebugOutput(cmd, checks)
	},
}

// inspectSSHCert parses an SSH certificate file and checks validity + principals.
func inspectSSHCert(certPath string) checkResult {
	out, err := exec.Command("ssh-keygen", "-L", "-f", certPath).Output()
	if err != nil {
		return checkResult{
			Name:   "SSH certificate",
			Status: "warn",
			Detail: fmt.Sprintf("cannot parse cert: %v", err),
		}
	}

	output := string(out)

	// Extract validity window
	var validFrom, validTo time.Time
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Valid:") {
			// "Valid: from 2026-03-05T08:53:43 to 2026-03-05T08:57:43"
			parts := strings.Fields(line)
			if len(parts) >= 5 {
				validFrom, _ = time.ParseInLocation("2006-01-02T15:04:05", parts[2], time.Local)
				validTo, _ = time.ParseInLocation("2006-01-02T15:04:05", parts[4], time.Local)
			}
		}
	}

	// Extract principals
	var principals []string
	inPrincipals := false
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "Principals:" {
			inPrincipals = true
			continue
		}
		if inPrincipals {
			if trimmed == "" || strings.Contains(trimmed, ":") {
				break
			}
			principals = append(principals, trimmed)
		}
	}

	now := time.Now()

	if validTo.IsZero() {
		return checkResult{
			Name:   "SSH certificate",
			Status: "warn",
			Detail: "could not parse validity window",
		}
	}

	if now.After(validTo) {
		expired := time.Since(validTo)
		return checkResult{
			Name:   "SSH certificate",
			Status: "fail",
			Detail: fmt.Sprintf("EXPIRED %s (was valid %s→%s, principals: %s)",
				friendlyDuration(expired),
				validFrom.Format("15:04:05"),
				validTo.Format("15:04:05"),
				strings.Join(principals, ",")),
			Hint: "cert should be regenerated on next ssh",
		}
	}

	remaining := time.Until(validTo)
	return checkResult{
		Name:   "SSH certificate",
		Status: "pass",
		Detail: fmt.Sprintf("valid %s→%s (%s left, principals: %s)",
			validFrom.Format("15:04:05"),
			validTo.Format("15:04:05"),
			friendlyRemaining(remaining),
			strings.Join(principals, ",")),
	}
}

// sshDebugOutput renders ssh-debug check results, reusing the doctor pattern.
func sshDebugOutput(cmd *cobra.Command, checks []checkResult) error {
	if isJSONOutput(cmd) {
		printJSON("machines.ssh-debug", checks)
		return nil
	}

	fmt.Println()
	fmt.Println(sshDebugHeader())
	fmt.Println()

	passed, warned, failed := 0, 0, 0

	for _, c := range checks {
		var mark string
		switch c.Status {
		case "pass":
			mark = checkMark()
			passed++
		case "warn":
			mark = warnMark()
			warned++
		default:
			mark = failMark()
			failed++
		}

		left := fmt.Sprintf("%s %-33s", mark, c.Name)
		right := cDim(c.Detail)
		if c.Hint != "" {
			right += "  " + cDim(c.Hint)
		}
		fmt.Printf("  %s %s\n", left, right)
	}

	fmt.Printf("\n  %s\n", cDim(strings.Repeat("─", 55)))
	summary := fmt.Sprintf("  %s, %s, %s",
		cGreen(fmt.Sprintf("%d passed", passed)),
		cAmber(fmt.Sprintf("%d warnings", warned)),
		cRed(fmt.Sprintf("%d failures", failed)),
	)
	fmt.Printf("%s\n\n", summary)

	return nil
}

// sshDebugHeader prints the header box for ssh-debug.
func sshDebugHeader() string {
	const w = 55
	bar := strings.Repeat("─", w)
	return fmt.Sprintf(
		"  %s\n  %s  %s  %s\n  %s",
		cBox("╭"+bar+"╮"),
		cBox("│"), cBrand(centerPadStr("SSH Diagnostics", w-2)), cBox("│"),
		cBox("╰"+bar+"╯"),
	)
}

func init() {
	machinesSSHDebugCmd.Flags().String("user", "openclaw", "SSH username")
	machinesCmd.AddCommand(machinesSSHDebugCmd)
}
