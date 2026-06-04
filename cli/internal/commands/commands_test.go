package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mathaix/openclawmachines/cli/internal/config"
)

// setupTestConfig writes a config file pointing at the mock server URL and sets
// the package-level cfgFile so that PersistentPreRunE loads it correctly.
// Returns a teardown function that cleans up.
func setupTestConfig(t *testing.T, baseURL string) func() {
	t.Helper()

	oldCfg := cfg
	oldCfgFile := cfgFile

	// Write a temporary config file
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	testCfg := &config.Config{
		APIURL:             baseURL,
		Token:              "test-token",
		DefaultAccountID:   1,
		DefaultAccountSlug: "test-account",
	}

	data, err := json.Marshal(testCfg)
	if err != nil {
		t.Fatalf("marshalling test config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	cfgFile = path

	return func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
	}
}

// setupTestConfigWithMachine is like setupTestConfig but also sets a default machine.
func setupTestConfigWithMachine(t *testing.T, baseURL, machineID, machineName string) func() {
	t.Helper()

	oldCfg := cfg
	oldCfgFile := cfgFile

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	testCfg := &config.Config{
		APIURL:             baseURL,
		Token:              "test-token",
		DefaultAccountID:   1,
		DefaultAccountSlug: "test-account",
		DefaultMachineID:   machineID,
		DefaultMachineName: machineName,
	}

	data, err := json.Marshal(testCfg)
	if err != nil {
		t.Fatalf("marshalling test config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	cfgFile = path

	return func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
	}
}

// captureStdout captures stdout output during the execution of fn.
func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// executeCommand runs a root command with the given args and returns the output and error.
func executeCommand(args ...string) (string, error) {
	// Reset the root command's output buffer
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(args)

	// Capture stdout (commands use fmt.Printf, not cmd.Print)
	var stdout string
	var cmdErr error
	stdout = captureStdout(func() {
		cmdErr = rootCmd.Execute()
	})

	// Combine cobra output buffer and stdout
	combined := buf.String() + stdout
	return combined, cmdErr
}
