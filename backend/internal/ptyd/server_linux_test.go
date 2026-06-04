//go:build linux

package ptyd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestHandleRestartGatewaySuccess(t *testing.T) {
	tmp := t.TempDir()
	manualPath := tmp + "/manual"
	crashLoopPath := tmp + "/crash-loop"

	restore := setRestartGatewayTestHooks(t, manualPath, crashLoopPath)
	defer restore()

	nowUnix = func() int64 { return 1700000000 }
	svRestartCommand = helperSVRestartCommand(t, 0, "ok", "")

	req := httptest.NewRequest(http.MethodPost, "/restart-gateway", nil)
	rec := httptest.NewRecorder()
	handleRestartGateway(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := strings.TrimSpace(readFile(t, crashLoopPath)); got != "0" {
		t.Fatalf("crash-loop marker = %q, want %q", got, "0")
	}
	if got := strings.TrimSpace(readFile(t, manualPath)); got != "1700000000 2" {
		t.Fatalf("manual marker = %q, want %q", got, "1700000000 2")
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"status":"restart-signaled"}` {
		t.Fatalf("body = %q", body)
	}
}

func TestHandleRestartGatewayCrashLoopClearFailure(t *testing.T) {
	tmp := t.TempDir()
	manualPath := tmp + "/manual"
	crashLoopPath := tmp + "/crash-loop"

	restore := setRestartGatewayTestHooks(t, manualPath, crashLoopPath)
	defer restore()

	writeFile = func(name string, data []byte, perm os.FileMode) error {
		if name == crashLoopPath {
			return errors.New("clear failed")
		}
		return os.WriteFile(name, data, perm)
	}

	req := httptest.NewRequest(http.MethodPost, "/restart-gateway", nil)
	rec := httptest.NewRecorder()
	handleRestartGateway(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if strings.Contains(rec.Body.String(), "method not allowed") {
		t.Fatalf("unexpected body %q", rec.Body.String())
	}
}

func TestHandleRestartGatewayMarkerWriteFailure(t *testing.T) {
	tmp := t.TempDir()
	manualPath := tmp + "/manual"
	crashLoopPath := tmp + "/crash-loop"

	restore := setRestartGatewayTestHooks(t, manualPath, crashLoopPath)
	defer restore()

	writeFile = func(name string, data []byte, perm os.FileMode) error {
		if name == manualPath {
			return errors.New("marker failed")
		}
		return os.WriteFile(name, data, perm)
	}

	req := httptest.NewRequest(http.MethodPost, "/restart-gateway", nil)
	rec := httptest.NewRecorder()
	handleRestartGateway(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandleRestartGatewaySVRestartFailure(t *testing.T) {
	tmp := t.TempDir()
	manualPath := tmp + "/manual"
	crashLoopPath := tmp + "/crash-loop"

	restore := setRestartGatewayTestHooks(t, manualPath, crashLoopPath)
	defer restore()

	nowUnix = func() int64 { return 1700000000 }
	svRestartCommand = helperSVRestartCommand(t, 1, "", "restart failed")

	req := httptest.NewRequest(http.MethodPost, "/restart-gateway", nil)
	rec := httptest.NewRecorder()
	handleRestartGateway(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := strings.TrimSpace(readFile(t, manualPath)); got != "1700000000 2" {
		t.Fatalf("manual marker = %q, want %q", got, "1700000000 2")
	}
}

func TestHandleRestartGatewayMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/restart-gateway", nil)
	rec := httptest.NewRecorder()
	handleRestartGateway(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestIsAllowedHermesConfigCommand(t *testing.T) {
	tests := []struct {
		name    string
		command []string
		want    bool
	}{
		{name: "sync", command: []string{"ocm-hermes-config", "sync"}, want: true},
		{name: "sync restart", command: []string{"ocm-hermes-config", "sync", "--restart-gateway"}, want: true},
		{name: "wrong subcommand", command: []string{"ocm-hermes-config", "write"}, want: false},
		{name: "extra arg", command: []string{"ocm-hermes-config", "sync", "--config", "/tmp/x"}, want: false},
		{name: "wrong program", command: []string{"openclaw", "config"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAllowedHermesConfigCommand(tt.command); got != tt.want {
				t.Fatalf("isAllowedHermesConfigCommand(%v) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestResolveAllowedCommandHermesUsesConfiguredRuntime(t *testing.T) {
	argv, ok := resolveAllowedCommand("hermes")
	if !ok {
		t.Fatal("resolveAllowedCommand(hermes) returned !ok")
	}
	if len(argv) != 3 || argv[0] != "/bin/bash" || argv[1] != "-lc" {
		t.Fatalf("Hermes command argv = %#v, want bash -lc wrapper", argv)
	}
	script := argv[2]
	for _, want := range []string{"/opt/data/.env", "HERMES_BIN:-hermes", "--tui"} {
		if !strings.Contains(script, want) {
			t.Fatalf("Hermes command script %q missing %q", script, want)
		}
	}
}

func TestPTYCommandEnvSetsHermesBrowserDefaults(t *testing.T) {
	env := ptyCommandEnv("hermes", []string{"PATH=/usr/bin"})

	if got := envValue(env, "TERM"); got != "xterm-256color" {
		t.Fatalf("TERM = %q, want xterm-256color", got)
	}
	if got := envValue(env, "HERMES_TUI_DISABLE_MOUSE"); got != "1" {
		t.Fatalf("HERMES_TUI_DISABLE_MOUSE = %q, want 1", got)
	}
}

func TestPTYCommandEnvDoesNotForceHermesOptionsForShell(t *testing.T) {
	env := ptyCommandEnv("bash", []string{"PATH=/usr/bin"})

	if got := envValue(env, "TERM"); got != "xterm-256color" {
		t.Fatalf("TERM = %q, want xterm-256color", got)
	}
	if got := envValue(env, "HERMES_TUI_DISABLE_MOUSE"); got != "" {
		t.Fatalf("HERMES_TUI_DISABLE_MOUSE = %q, want unset", got)
	}
}

func setRestartGatewayTestHooks(t *testing.T, manualPath, crashLoopPath string) func() {
	t.Helper()

	oldManual := gatewayManualRestartPath
	oldCrash := gatewayCrashLoopPath
	oldNow := nowUnix
	oldWrite := writeFile
	oldSV := svRestartCommand

	gatewayManualRestartPath = manualPath
	gatewayCrashLoopPath = crashLoopPath
	nowUnix = func() int64 { return 1700000000 }
	writeFile = os.WriteFile
	svRestartCommand = helperSVRestartCommand(t, 0, "", "")

	return func() {
		gatewayManualRestartPath = oldManual
		gatewayCrashLoopPath = oldCrash
		nowUnix = oldNow
		writeFile = oldWrite
		svRestartCommand = oldSV
	}
}

func helperSVRestartCommand(t *testing.T, exitCode int, stdout, stderr string) func(string, ...string) *exec.Cmd {
	t.Helper()

	return func(name string, args ...string) *exec.Cmd {
		t.Helper()
		if name != "sudo" {
			t.Fatalf("unexpected command name %q", name)
		}
		if strings.Join(args, " ") != "-n /usr/local/bin/ocm-restart-gateway" {
			t.Fatalf("unexpected args %q", strings.Join(args, " "))
		}

		cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcessSVRestart", "--", strconv.Itoa(exitCode), stdout, stderr)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		return cmd
	}
}

func TestHelperProcessSVRestart(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	idx := 0
	for i, arg := range os.Args {
		if arg == "--" {
			idx = i + 1
			break
		}
	}
	if idx == 0 || len(os.Args) < idx+3 {
		os.Exit(2)
	}

	code, err := strconv.Atoi(os.Args[idx])
	if err != nil {
		os.Exit(2)
	}
	_, _ = os.Stdout.WriteString(os.Args[idx+1])
	_, _ = os.Stderr.WriteString(os.Args[idx+2])
	os.Exit(code)
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
