package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mathaix/openclawmachines/cli/internal/api"
	"github.com/mathaix/openclawmachines/cli/internal/config"
	"github.com/spf13/cobra"
)

// checkResult records the outcome of a single doctor check.
type checkResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "pass", "warn", "fail"
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose configuration problems",
	Long:  "Run a series of diagnostic checks on your OCM CLI configuration, API connectivity, and machine state.",
	RunE: func(cmd *cobra.Command, args []string) error {
		machineName, _ := cmd.Flags().GetString("machine")
		var checks []checkResult

		// ── 1. Config file exists ──
		configPath := cfgFile
		if configPath == "" {
			var pathErr error
			configPath, pathErr = config.DefaultPath()
			if pathErr != nil {
				checks = append(checks, checkResult{
					Name:   "Configuration file exists",
					Status: "fail",
					Detail: pathErr.Error(),
				})
				configPath = "" // signal that we already recorded the failure
			}
		}
		if configPath != "" {
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				checks = append(checks, checkResult{
					Name:   "Configuration file exists",
					Status: "fail",
					Detail: configPath,
					Hint:   "run ocm login to create it",
				})
			} else {
				checks = append(checks, checkResult{
					Name:   "Configuration file exists",
					Status: "pass",
					Detail: configPath,
				})
			}
		}

		// ── 2. API is reachable ──
		if cfg == nil || cfg.APIURL == "" {
			checks = append(checks, checkResult{
				Name:   "API is reachable",
				Status: "fail",
				Detail: "API URL not configured",
				Hint:   "run ocm config set-api-url <url>",
			})
			return doctorOutput(cmd, checks)
		}

		client := api.NewClient(cfg.APIURL, cfg.Token)
		start := time.Now()
		meResp, meErr := client.Get("/api/auth/me")
		elapsed := time.Since(start)

		if meErr != nil {
			checks = append(checks, checkResult{
				Name:   "API is reachable",
				Status: "fail",
				Detail: meErr.Error(),
			})
			return doctorOutput(cmd, checks)
		}
		defer meResp.Body.Close()

		meBody, _ := io.ReadAll(meResp.Body)

		if meResp.StatusCode == 200 {
			checks = append(checks, checkResult{
				Name:   "API is reachable",
				Status: "pass",
				Detail: fmt.Sprintf("%d OK (%dms)", meResp.StatusCode, elapsed.Milliseconds()),
			})
		} else {
			checks = append(checks, checkResult{
				Name:   "API is reachable",
				Status: "warn",
				Detail: fmt.Sprintf("%d (%dms)", meResp.StatusCode, elapsed.Milliseconds()),
			})
		}

		// ── 3. Token is valid ──
		var user api.User
		if meResp.StatusCode != 200 {
			checks = append(checks, checkResult{
				Name:   "Token is valid",
				Status: "fail",
				Detail: fmt.Sprintf("HTTP %d", meResp.StatusCode),
				Hint:   "run ocm login to refresh",
			})
		} else {
			var meData struct {
				User api.User `json:"user"`
			}
			if err := json.Unmarshal(meBody, &meData); err != nil {
				checks = append(checks, checkResult{
					Name:   "Token is valid",
					Status: "fail",
					Detail: "could not parse auth response",
				})
			} else {
				user = meData.User
				checks = append(checks, checkResult{
					Name:   "Token is valid",
					Status: "pass",
					Detail: user.Email,
				})
			}
		}

		// ── 4. Token expiry ──
		if cfg.TokenExpires != "" {
			expiry, err := time.Parse(time.RFC3339, cfg.TokenExpires)
			if err != nil {
				checks = append(checks, checkResult{
					Name:   "Token expiry",
					Status: "warn",
					Detail: fmt.Sprintf("could not parse expiry %q", cfg.TokenExpires),
					Hint:   "run ocm login to refresh",
				})
			} else {
				remaining := time.Until(expiry)
				if remaining <= 0 {
					checks = append(checks, checkResult{
						Name:   "Token expiry",
						Status: "fail",
						Detail: "token has expired",
						Hint:   "run ocm login to refresh",
					})
				} else if remaining < 24*time.Hour {
					checks = append(checks, checkResult{
						Name:   "Token expiry",
						Status: "warn",
						Detail: fmt.Sprintf("expires in %s", friendlyRemaining(remaining)),
						Hint:   "run ocm login to refresh",
					})
				} else {
					checks = append(checks, checkResult{
						Name:   "Token expiry",
						Status: "pass",
						Detail: fmt.Sprintf("expires in %s", friendlyRemaining(remaining)),
					})
				}
			}
		}

		// ── 5. Account configured ──
		if cfg.DefaultAccountID == 0 {
			checks = append(checks, checkResult{
				Name:   "Account configured",
				Status: "fail",
				Detail: "no default account set",
				Hint:   "run ocm login",
			})
			return doctorOutput(cmd, checks)
		}
		acctDetail := cfg.DefaultAccountSlug
		if acctDetail == "" {
			acctDetail = fmt.Sprintf("ID: %d", cfg.DefaultAccountID)
		} else {
			acctDetail = fmt.Sprintf("%s (ID: %d)", acctDetail, cfg.DefaultAccountID)
		}
		checks = append(checks, checkResult{
			Name:   "Account configured",
			Status: "pass",
			Detail: acctDetail,
		})

		// ── 6. Machine exists ──
		machine, err := resolveMachine(client, machineName)
		if err != nil {
			checks = append(checks, checkResult{
				Name:   "Machine exists",
				Status: "fail",
				Detail: err.Error(),
				Hint:   "run ocm machines create --name <name>",
			})
			return doctorOutput(cmd, checks)
		}

		size := sizeLabel(machine.VCPUs, machine.MemoryMB)
		createdAgo := friendlyDuration(time.Since(machine.CreatedAt))
		checks = append(checks, checkResult{
			Name:   "Machine exists",
			Status: "pass",
			Detail: fmt.Sprintf("%q %s, created %s", machine.Name, size, createdAgo),
		})

		// ── 7. Machine is running ──
		if machine.Status == "running" {
			uptime := ""
			if machine.StartedAt != nil {
				uptime = "uptime " + friendlyUptime(time.Since(*machine.StartedAt))
			} else if machine.LastStartedAt != nil {
				uptime = "uptime " + friendlyUptime(time.Since(*machine.LastStartedAt))
			}
			checks = append(checks, checkResult{
				Name:   "Machine is running",
				Status: "pass",
				Detail: uptime,
			})
		} else {
			checks = append(checks, checkResult{
				Name:   "Machine is running",
				Status: "fail",
				Detail: fmt.Sprintf("status: %s", machine.Status),
				Hint:   "run ocm machines start",
			})
		}

		// ── 8. LLM credential linked ──
		credsPath := fmt.Sprintf("/api/accounts/%d/machines/%s/credentials", cfg.DefaultAccountID, machine.ID)
		credsResp, credsErr := client.Get(credsPath)
		var allCreds []api.Credential
		var llmCreds []api.Credential
		if credsErr != nil {
			checks = append(checks, checkResult{
				Name:   "LLM credential linked",
				Status: "fail",
				Detail: credsErr.Error(),
			})
		} else {
			defer credsResp.Body.Close()
			if err := readJSON(&credsResp.Body, credsResp.StatusCode, 200, &allCreds); err != nil {
				checks = append(checks, checkResult{
					Name:   "LLM credential linked",
					Status: "fail",
					Detail: err.Error(),
				})
			} else {
				// Filter to AI/LLM providers only
				for _, c := range allCreds {
					switch c.Provider {
					case "anthropic", "openai", "google":
						llmCreds = append(llmCreds, c)
					}
				}
				if len(llmCreds) == 0 {
					checks = append(checks, checkResult{
						Name:   "LLM credential linked",
						Status: "fail",
						Detail: "no LLM credentials linked",
						Hint:   "run ocm providers add",
					})
				} else {
					// Show first LLM credential
					cred := llmCreds[0]
					lastFour := ""
					if cred.LastFour != nil {
						lastFour = " (****" + *cred.LastFour + ")"
					}
					checks = append(checks, checkResult{
						Name:   "LLM credential linked",
						Status: "pass",
						Detail: cred.Provider + lastFour,
					})
				}
			}
		}

		// ── 9. Credential validated ──
		if len(llmCreds) > 0 {
			cred := llmCreds[0]
			if cred.LastValidated != nil {
				age := time.Since(*cred.LastValidated)
				checks = append(checks, checkResult{
					Name:   "Credential recently validated",
					Status: "pass",
					Detail: friendlyDuration(age),
				})
			} else {
				checks = append(checks, checkResult{
					Name:   "Credential recently validated",
					Status: "warn",
					Detail: "never validated",
					Hint:   "run ocm providers validate",
				})
			}
		}

		// ── 10. Channel enabled ──
		capsPath := fmt.Sprintf("/api/accounts/%d/machines/%s/capabilities", cfg.DefaultAccountID, machine.ID)
		capsResp, capsErr := client.Get(capsPath)
		var caps []api.MachineCapability
		if capsErr != nil {
			checks = append(checks, checkResult{
				Name:   "Channel enabled",
				Status: "fail",
				Detail: capsErr.Error(),
			})
		} else {
			defer capsResp.Body.Close()
			if err := readJSON(&capsResp.Body, capsResp.StatusCode, 200, &caps); err != nil {
				checks = append(checks, checkResult{
					Name:   "Channel enabled",
					Status: "fail",
					Detail: err.Error(),
				})
			} else {
				var channelNames []string
				var skillNames []string
				for _, c := range caps {
					if c.EntryType == "channel" && c.Enabled {
						channelNames = append(channelNames, c.EntryName)
					}
					if c.EntryType == "skill" && c.Enabled {
						skillNames = append(skillNames, c.EntryName)
					}
				}

				if len(channelNames) == 0 {
					checks = append(checks, checkResult{
						Name:   "Channel enabled",
						Status: "warn",
						Detail: "no channels enabled",
						Hint:   "run ocm channels enable <channel>",
					})
				} else {
					checks = append(checks, checkResult{
						Name:   "Channel enabled",
						Status: "pass",
						Detail: strings.Join(channelNames, ", "),
					})
				}

				// ── 11. Skills configured ──
				if len(skillNames) == 0 {
					checks = append(checks, checkResult{
						Name:   "Skills configured",
						Status: "warn",
						Detail: "no skills configured",
						Hint:   "run ocm skills available",
					})
				} else {
					checks = append(checks, checkResult{
						Name:   "Skills configured",
						Status: "pass",
						Detail: strings.Join(skillNames, ", "),
					})
				}
			}
		}

		// ── 12. Config version current ──
		cfgPath := fmt.Sprintf("/api/accounts/%d/machines/%s/assembled-config", cfg.DefaultAccountID, machine.ID)
		cfgResp, cfgErr := client.Get(cfgPath)
		if cfgErr != nil {
			checks = append(checks, checkResult{
				Name:   "Config version is current",
				Status: "fail",
				Detail: cfgErr.Error(),
			})
		} else {
			defer cfgResp.Body.Close()
			cfgBody, err := io.ReadAll(cfgResp.Body)
			if err != nil || cfgResp.StatusCode != 200 {
				checks = append(checks, checkResult{
					Name:   "Config version is current",
					Status: "fail",
					Detail: fmt.Sprintf("HTTP %d", cfgResp.StatusCode),
				})
			} else {
				ver, _ := extractConfigVersion(cfgBody)
				if ver == "-" {
					checks = append(checks, checkResult{
						Name:   "Config version is current",
						Status: "warn",
						Detail: "no config pushed yet",
						Hint:   "push config via the dashboard",
					})
				} else {
					checks = append(checks, checkResult{
						Name:   "Config version is current",
						Status: "pass",
						Detail: ver,
					})
				}
			}
		}

		return doctorOutput(cmd, checks)
	},
}

// doctorOutput renders all check results as styled terminal output or JSON.
func doctorOutput(cmd *cobra.Command, checks []checkResult) error {
	if isJSONOutput(cmd) {
		printJSON("doctor", checks)
		return nil
	}

	fmt.Println()
	fmt.Println(doctorHeader())
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

		// Left side: mark + name (pad to 35 chars)
		left := fmt.Sprintf("%s %-33s", mark, c.Name)

		// Right side: detail + optional hint
		right := cDim(c.Detail)
		if c.Hint != "" {
			right += "  " + cDim(c.Hint)
		}

		fmt.Printf("  %s %s\n", left, right)
	}

	// Summary line
	fmt.Printf("\n  %s\n", cDim(strings.Repeat("─", 55)))
	summary := fmt.Sprintf("  %s, %s, %s",
		cGreen(fmt.Sprintf("%d passed", passed)),
		cAmber(fmt.Sprintf("%d warnings", warned)),
		cRed(fmt.Sprintf("%d failures", failed)),
	)
	fmt.Printf("%s\n\n", summary)

	return nil
}

// friendlyRemaining formats a duration as a human-readable remaining time.
func friendlyRemaining(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	h := int(d.Hours())
	if h < 24 {
		return fmt.Sprintf("%d hours", h)
	}
	days := h / 24
	if days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}

// friendlyUptime formats a duration as compact uptime (e.g. "2h 14m").
func friendlyUptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

func init() {
	doctorCmd.Flags().String("machine", "", "machine name")
	registerMachineFlagCompletion(doctorCmd)
	rootCmd.AddCommand(doctorCmd)
}
