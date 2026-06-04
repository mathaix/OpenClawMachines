package commands

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// jsonEnvelope is the standard JSON output envelope for --json mode.
type jsonEnvelope struct {
	Status string `json:"status"`
	Action string `json:"action"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// isJSONOutput returns true if the --json flag is set on the command or any parent.
func isJSONOutput(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("json")
	if f == nil {
		return false
	}
	return f.Value.String() == "true"
}

// printJSON writes a success JSON envelope to stdout.
func printJSON(action string, result any) {
	env := jsonEnvelope{
		Status: "ok",
		Action: action,
		Result: result,
	}
	data, _ := json.Marshal(env)
	fmt.Println(string(data))
}

// printJSONError writes an error JSON envelope to stderr.
func printJSONError(action string, err error) {
	env := jsonEnvelope{
		Status: "error",
		Action: action,
		Error:  err.Error(),
	}
	data, _ := json.Marshal(env)
	fmt.Fprintln(os.Stderr, string(data))
}

// readSecret prompts for sensitive input using regular readline.
// Input is visible (like OpenClaw's approach) to ensure compatibility
// across all terminal emulators including VS Code, tmux, and screen.
func readSecret(prompt string) (string, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("reading input: %w", err)
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return "", fmt.Errorf("input cannot be empty")
	}
	return value, nil
}

// readSecretInput reads a secret value from --*-from-stdin or interactive prompt.
// If the stdinFlag is set, reads from stdin. Otherwise prompts interactively.
func readSecretInput(cmd *cobra.Command, prompt string, stdinFlagName string) (string, error) {
	fromStdin, _ := cmd.Flags().GetBool(stdinFlagName)
	if fromStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading from stdin: %w", err)
		}
		value := strings.TrimSpace(string(data))
		if value == "" {
			return "", fmt.Errorf("stdin input is empty")
		}
		return value, nil
	}

	return readSecret(prompt)
}

// printTokenBox prints a bordered instruction box telling the user to run a command.
// Output looks like:
//
//	┌──────────────────────────────────────────╮
//	│                                          │
//	│  Run `claude setup-token` in your        │
//	│  terminal. Then paste the generated       │
//	│  token below.                             │
//	│                                          │
//	├──────────────────────────────────────────╯
func printTokenBox(tokenCmd string) {
	line1 := fmt.Sprintf("Run `%s` in your terminal.", tokenCmd)
	line2 := "Then paste the generated token below."

	// Find the widest content line + 4 for padding (2 each side)
	w := len(line1)
	if len(line2) > w {
		w = len(line2)
	}
	w += 4 // 2 spaces padding on each side

	pad := func(s string) string {
		return fmt.Sprintf("  %-*s", w-2, s)
	}
	bar := strings.Repeat("─", w)
	empty := fmt.Sprintf("  │%s│", strings.Repeat(" ", w))

	fmt.Printf("  ┌%s╮\n", bar)
	fmt.Println(empty)
	fmt.Printf("  │%s│\n", pad(line1))
	fmt.Printf("  │%s│\n", pad(line2))
	fmt.Println(empty)
	fmt.Printf("  ├%s╯\n", bar)
}

// promptString reads a line of input from the user.
func promptString(prompt string) (string, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// promptYesNo asks a yes/no question and returns the answer.
func promptYesNo(prompt string, defaultYes bool) (bool, error) {
	suffix := " [y/N]: "
	if defaultYes {
		suffix = " [Y/n]: "
	}
	answer, err := promptString(prompt + suffix)
	if err != nil {
		return defaultYes, err
	}
	answer = strings.ToLower(answer)
	if answer == "" {
		return defaultYes, nil
	}
	return answer == "y" || answer == "yes", nil
}

// readMaskedSecret prompts for sensitive input with hidden echo.
// Uses term.ReadPassword when running in a terminal; falls back to readSecret
// when stdin is piped (CI/scripting).
func readMaskedSecret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return readSecret(prompt)
	}

	fmt.Print(prompt)
	raw, err := term.ReadPassword(fd)
	fmt.Println() // newline after hidden input
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", fmt.Errorf("input cannot be empty")
	}
	fmt.Printf("  Key: %s\n", maskSecret(value))
	return value, nil
}

// maskSecret returns a masked version of a secret showing only the last 4 characters.
// For secrets shorter than 8 characters, it masks all but the last 2.
// For secrets shorter than 4 characters, it masks everything.
func maskSecret(s string) string {
	n := len(s)
	if n == 0 {
		return ""
	}
	if n < 4 {
		return strings.Repeat("*", n)
	}
	visible := 4
	if n < 8 {
		visible = 2
	}
	return strings.Repeat("*", n-visible) + s[n-visible:]
}

// readSelection reads a comma-separated list of numbers from stdin.
// Returns 0-indexed selection indices. "none" or empty returns empty slice.
func readSelection(max int) ([]int, error) {
	answer, err := promptString("Selection: ")
	if err != nil {
		return nil, err
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "" || answer == "none" {
		return nil, nil
	}

	var selected []int
	for _, part := range strings.Split(answer, ",") {
		part = strings.TrimSpace(part)
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > max {
			return nil, fmt.Errorf("invalid selection: %q (must be 1-%d)", part, max)
		}
		selected = append(selected, n-1) // convert to 0-indexed
	}
	return selected, nil
}
