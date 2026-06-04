//go:build linux

package ptyd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

const (
	sessionReaperInterval = 30 * time.Second
	ringBufferSize        = 64 * 1024
	ptyReadDeadline       = 60 * time.Second
	ptyPingInterval       = 25 * time.Second
	ptyWriteDeadline      = 10 * time.Second
	maxExecOutputBytes    = 64 * 1024
	ptyOutputFlushDelay   = 8 * time.Millisecond
	ptyOutputMaxFrame     = 32 * 1024
	ptyOutputQueueSize    = 64
)

var errStalePTYConnection = errors.New("stale pty websocket connection")

var allowedExecSubcommands = map[string]bool{
	"pairing": true,
	"status":  true,
	"doctor":  true,
	"gateway": true,
	"config":  true,
}

var allowedCommands = map[string][]string{
	"":         {"/bin/bash", "--login"},
	"bash":     {"/bin/bash", "--login"},
	"openclaw": {"openclaw", "tui"},
	"hermes":   {"/bin/bash", "-lc", `set -a; [ -f /opt/data/.env ] && . /opt/data/.env; set +a; exec "${HERMES_BIN:-hermes}" --tui`},
	"logs":     {"tail", "-f", "/var/log/openclaw-gateway.log"},
}

var (
	gatewayManualRestartPath = "/run/ocm-gateway-manual-restart"
	gatewayCrashLoopPath     = "/run/ocm-gateway-crash-loop"
	nowUnix                  = func() int64 { return time.Now().Unix() }
	writeFile                = os.WriteFile
	svRestartCommand         = exec.Command
)

type ringBuffer struct {
	buf  []byte
	pos  int
	full bool
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{buf: make([]byte, size)}
}

func (rb *ringBuffer) Write(p []byte) {
	for _, b := range p {
		rb.buf[rb.pos] = b
		rb.pos++
		if rb.pos >= len(rb.buf) {
			rb.pos = 0
			rb.full = true
		}
	}
}

func (rb *ringBuffer) Bytes() []byte {
	if !rb.full {
		return rb.buf[:rb.pos]
	}
	out := make([]byte, len(rb.buf))
	n := copy(out, rb.buf[rb.pos:])
	copy(out[n:], rb.buf[:rb.pos])
	return out
}

type ptySession struct {
	id           string
	commandName  string
	ptmx         *os.File
	cmd          *exec.Cmd
	pid          int
	startedAt    time.Time
	ring         *ringBuffer
	disconnected time.Time
	connected    bool

	mu   sync.Mutex
	conn *websocket.Conn
	done chan struct{}

	ringMu  sync.Mutex
	writeMu sync.Mutex
}

type sessionManager struct {
	mu       sync.Mutex
	sessions map[string]*ptySession
}

func newSessionManager() *sessionManager {
	sm := &sessionManager{
		sessions: make(map[string]*ptySession),
	}
	go sm.reaper()
	return sm
}

func (sm *sessionManager) reaper() {
	ticker := time.NewTicker(sessionReaperInterval)
	defer ticker.Stop()
	for range ticker.C {
		sm.mu.Lock()
		for id, sess := range sm.sessions {
			sess.mu.Lock()
			select {
			case <-sess.done:
				exitCode := -1
				if sess.cmd.ProcessState != nil {
					exitCode = sess.cmd.ProcessState.ExitCode()
				}
				slog.Info("pty.session_reaped", "session_id", id, "pid", sess.pid,
					"reason", "process_exit",
					"exit_code", exitCode,
					"uptime_s", time.Since(sess.startedAt).Seconds())
				sess.mu.Unlock()
				sm.destroySessionLocked(id, sess)
				continue
			default:
			}
			sess.mu.Unlock()
		}
		sm.mu.Unlock()
	}
}

func (sm *sessionManager) destroySessionLocked(id string, sess *ptySession) {
	_ = sess.ptmx.Close()
	select {
	case <-sess.done:
	default:
		_ = sess.cmd.Process.Kill()
	}
	_ = sess.cmd.Wait()
	delete(sm.sessions, id)
	slog.Info("pty.session_destroyed", "session_id", id, "pid", sess.pid,
		"duration_s", time.Since(sess.startedAt).Seconds())
}

func (sm *sessionManager) get(id string) *ptySession {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.sessions[id]
}

func (sm *sessionManager) create(commandName string) (*ptySession, error) {
	argv, ok := resolveAllowedCommand(commandName)
	if !ok {
		return nil, fmt.Errorf("unknown command: %q", commandName)
	}

	id := generateSessionID()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = ptyCommandEnv(commandName, os.Environ())

	if commandName == "" || commandName == "bash" || commandName == "hermes" {
		dir := defaultShellWorkdir()
		cmd.Dir = dir
		slog.Info("pty.session_dir", "dir", dir, "command", commandName)
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}

	sess := &ptySession{
		id:          id,
		commandName: commandName,
		ptmx:        ptmx,
		cmd:         cmd,
		pid:         cmd.Process.Pid,
		startedAt:   time.Now(),
		ring:        newRingBuffer(ringBufferSize),
		connected:   false,
		done:        make(chan struct{}),
	}

	go func() {
		_ = cmd.Wait()
		close(sess.done)
	}()
	go pumpPTYOutput(sess)

	sm.mu.Lock()
	sm.sessions[id] = sess
	sm.mu.Unlock()

	slog.Info("pty.session_created", "session_id", id, "pid", sess.pid, "command", commandName)
	return sess, nil
}

func ptyCommandEnv(commandName string, base []string) []string {
	env := append([]string{}, base...)
	env = append(env, "TERM=xterm-256color")
	if commandName == "hermes" {
		env = append(env, "HERMES_TUI_DISABLE_MOUSE=1")
	}
	return env
}

func resolveAllowedCommand(commandName string) ([]string, bool) {
	argv, ok := allowedCommands[commandName]
	if !ok {
		return nil, false
	}
	if commandName == "logs" && strings.TrimSpace(os.Getenv("HERMES_HOME")) != "" {
		return []string{"tail", "-f", "/var/log/hermes-gateway.log", "/var/log/hermes-dashboard.log"}, true
	}
	return argv, true
}

func defaultShellWorkdir() string {
	candidates := []string{
		os.Getenv("OCM_PTY_WORKDIR"),
	}
	if hermesHome := strings.TrimSpace(os.Getenv("HERMES_HOME")); hermesHome != "" {
		candidates = append(candidates, filepath.Join(hermesHome, "workspace"))
	}
	candidates = append(candidates,
		"/home/openclaw/.openclaw/workspace",
		"/workspace",
		os.Getenv("HOME"),
		"/",
	)
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return "/"
}

func (sm *sessionManager) remove(id string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sess, ok := sm.sessions[id]; ok {
		_ = sess.ptmx.Close()
		select {
		case <-sess.done:
		default:
			_ = sess.cmd.Process.Kill()
		}
		_ = sess.cmd.Wait()
		delete(sm.sessions, id)
	}
}

func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return hex.EncodeToString(b)
}

func Run(port string) {
	sm := newSessionManager()

	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}

	handler := func(w http.ResponseWriter, r *http.Request) {
		if !websocket.IsWebSocketUpgrade(r) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("PTY server ready"))
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("pty.websocket_upgrade_failed", "error", err)
			return
		}
		conn.SetReadLimit(64 * 1024)

		sessionID := r.URL.Query().Get("session_id")
		var sess *ptySession
		if sessionID != "" {
			sess = sm.get(sessionID)
			if sess != nil {
				select {
				case <-sess.done:
					slog.Info("pty.session_reconnect_expired", "session_id", sessionID)
					sm.remove(sessionID)
					sess = nil
				default:
				}
			}
		}

		if sess == nil {
			commandName := r.URL.Query().Get("command")
			sess, err = sm.create(commandName)
			if err != nil {
				slog.Error("pty.session_create_failed", "error", err, "command", commandName)
				_ = conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, err.Error()))
				_ = conn.Close()
				return
			}
		} else {
			disconnectedFor := time.Duration(0)
			if !sess.disconnected.IsZero() {
				disconnectedFor = time.Since(sess.disconnected)
			}
			slog.Info("pty.session_reconnected", "session_id", sess.id, "pid", sess.pid,
				"disconnected_for", disconnectedFor.Round(time.Millisecond),
				"replay_bytes", len(sess.replayBytes()),
				"uptime_s", time.Since(sess.startedAt).Seconds())
		}

		handleSession(conn, sess, sm)
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		status := "unknown"
		data, err := os.ReadFile("/run/ocm-gateway-status")
		if err == nil {
			s := strings.TrimSpace(string(data))
			if s != "" {
				status = s
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"gateway": status})
	})
	http.HandleFunc("/restart-gateway", handleRestartGateway)
	http.HandleFunc("/exec", handleExec)
	http.HandleFunc("/write-file", handleWriteFile)
	http.HandleFunc("/ws", handler)
	http.HandleFunc("/", handler)

	addr := ":" + port
	slog.Info("pty.server_listening", "addr", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		slog.Error("pty.server_start_failed", "error", err)
		os.Exit(1)
	}
}

func handleRestartGateway(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := writeFile(gatewayCrashLoopPath, []byte("0\n"), 0o644); err != nil {
		slog.Error("pty.restart_gateway_signal_failed", "error", err, "step", "clear_crash_loop")
		http.Error(w, "failed to clear crash-loop latch", http.StatusInternalServerError)
		return
	}

	marker := []byte(strconv.FormatInt(nowUnix(), 10) + " 2\n")
	if err := writeFile(gatewayManualRestartPath, marker, 0o644); err != nil {
		slog.Error("pty.restart_gateway_signal_failed", "error", err, "step", "marker_write")
		http.Error(w, "failed to arm restart marker", http.StatusInternalServerError)
		return
	}

	// ocmptyd runs as openclaw; the dedicated root-owned helper preserves the
	// existing gateway restart control-plane path without reopening broad
	// platform sv access to the user shell.
	out, err := svRestartCommand("sudo", "-n", "/usr/local/bin/ocm-restart-gateway").CombinedOutput()
	if err != nil {
		slog.Error("pty.restart_gateway_signal_failed", "error", err, "output", string(out), "step", "sv_restart")
		http.Error(w, "failed to restart gateway", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "restart-signaled"})
}

func handleSession(conn *websocket.Conn, sess *ptySession, sm *sessionManager) {
	sess.writeMu.Lock()
	sess.mu.Lock()
	if sess.conn != nil {
		_ = sess.conn.Close()
	}
	sess.conn = conn
	sess.connected = true
	sess.disconnected = time.Time{}
	sess.mu.Unlock()

	handshakeErr := writeSessionMessageLocked(sess, conn, []byte("s"+sess.id))
	replay := sess.replayBytes()
	if handshakeErr == nil && len(replay) > 0 {
		handshakeErr = writeSessionMessageLocked(sess, conn, append([]byte("r"), replay...))
	}
	sess.writeMu.Unlock()
	if handshakeErr != nil {
		slog.Error("pty.session_handshake_send_failed", "session_id", sess.id, "error", handshakeErr)
		_ = conn.Close()
		markDisconnected(sess, conn)
		return
	}

	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(ptyReadDeadline))
		return nil
	})
	_ = conn.SetReadDeadline(time.Now().Add(ptyReadDeadline))

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err == nil {
				_ = conn.SetReadDeadline(time.Now().Add(ptyReadDeadline))
			}
			if err != nil {
				closeCode := 0
				closeText := ""
				reason := "connection_lost"
				if closeErr, ok := err.(*websocket.CloseError); ok {
					closeCode = closeErr.Code
					closeText = closeErr.Text
					switch closeErr.Code {
					case websocket.CloseNormalClosure:
						reason = "normal_close"
					case websocket.CloseGoingAway:
						reason = "going_away"
					default:
						reason = "abnormal_close"
					}
				} else if isTimeoutError(err) {
					reason = "read_deadline"
				}
				if reason != "normal_close" && reason != "going_away" {
					slog.Warn("pty.ws_disconnected",
						"session_id", sess.id, "pid", sess.pid,
						"reason", reason,
						"close_code", closeCode,
						"close_text", closeText,
						"error", err.Error())
				} else {
					slog.Info("pty.ws_disconnected",
						"session_id", sess.id, "pid", sess.pid,
						"reason", reason)
				}
				return
			}

			if len(msg) == 0 {
				continue
			}

			switch msg[0] {
			case '0':
				if _, err := sess.ptmx.Write(msg[1:]); err != nil {
					slog.Error("pty.write_error", "session_id", sess.id, "pid", sess.pid, "error", err)
					return
				}
			case '1':
				var size struct {
					Columns uint16 `json:"columns"`
					Rows    uint16 `json:"rows"`
				}
				if err := json.Unmarshal(msg[1:], &size); err != nil {
					slog.Warn("pty.invalid_resize_json", "session_id", sess.id, "error", err)
					continue
				}
				if err := pty.Setsize(sess.ptmx, &pty.Winsize{
					Cols: size.Columns,
					Rows: size.Rows,
				}); err != nil {
					slog.Error("pty.resize_failed", "session_id", sess.id, "error", err)
				}
			default:
				slog.Warn("pty.unknown_message_type", "session_id", sess.id, "type", string(msg[0]))
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(ptyPingInterval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-sess.done:
				return
			case <-ticker.C:
				sess.writeMu.Lock()
				sess.mu.Lock()
				current := sess.conn
				sess.mu.Unlock()
				if current != conn {
					sess.writeMu.Unlock()
					return
				}
				deadline := time.Now().Add(ptyWriteDeadline)
				_ = conn.SetWriteDeadline(deadline)
				if err := conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
					sess.writeMu.Unlock()
					slog.Warn("pty.ws_ping_failed", "session_id", sess.id, "pid", sess.pid, "error", err)
					_ = conn.Close()
					return
				}
				sess.writeMu.Unlock()
			}
		}
	}()

	go func() {
		select {
		case <-done:
		case <-sess.done:
			_ = conn.Close()
		}
	}()

	<-done
	_ = conn.Close()
	markDisconnected(sess, conn)
}

func pumpPTYOutput(sess *ptySession) {
	output := make(chan []byte, ptyOutputQueueSize)
	go readPTYOutput(sess, output)
	writePTYOutput(sess, output)
}

func readPTYOutput(sess *ptySession, output chan<- []byte) {
	defer close(output)
	buf := make([]byte, 4096)
	for {
		select {
		case <-sess.done:
			return
		default:
		}

		n, err := sess.ptmx.Read(buf)
		if err != nil {
			if err != io.EOF {
				slog.Error("pty.read_error", "session_id", sess.id, "pid", sess.pid, "error", err)
			}
			return
		}

		data := append([]byte(nil), buf[:n]...)
		sess.appendOutput(data)

		select {
		case output <- data:
		case <-sess.done:
			return
		}
	}
}

func writePTYOutput(sess *ptySession, output <-chan []byte) {
	pending := make([]byte, 0, 4096)
	var flushTimer *time.Timer
	var flushC <-chan time.Time

	startFlushTimer := func() {
		if flushTimer != nil {
			return
		}
		flushTimer = time.NewTimer(ptyOutputFlushDelay)
		flushC = flushTimer.C
	}
	stopFlushTimer := func() {
		if flushTimer == nil {
			return
		}
		if !flushTimer.Stop() {
			select {
			case <-flushTimer.C:
			default:
			}
		}
		flushTimer = nil
		flushC = nil
	}
	flush := func() {
		if len(pending) == 0 {
			stopFlushTimer()
			return
		}
		msg := make([]byte, len(pending)+1)
		msg[0] = '0'
		copy(msg[1:], pending)
		if err := writeSessionMessageToCurrent(sess, msg); err != nil && !errors.Is(err, errStalePTYConnection) {
			slog.Debug("pty.websocket_write_error", "session_id", sess.id, "error", err)
		}
		pending = pending[:0]
		stopFlushTimer()
	}
	defer stopFlushTimer()

	for {
		select {
		case <-sess.done:
			flush()
			return
		case data, ok := <-output:
			if !ok {
				flush()
				return
			}
			if len(pending) == 0 {
				startFlushTimer()
			}
			pending = append(pending, data...)
			if len(pending) >= ptyOutputMaxFrame {
				flush()
			}
		case <-flushC:
			flush()
		}
	}
}

func (sess *ptySession) appendOutput(data []byte) {
	sess.ringMu.Lock()
	sess.ring.Write(data)
	sess.ringMu.Unlock()
}

func (sess *ptySession) replayBytes() []byte {
	sess.ringMu.Lock()
	defer sess.ringMu.Unlock()
	return append([]byte(nil), sess.ring.Bytes()...)
}

func writeSessionMessageToCurrent(sess *ptySession, data []byte) error {
	sess.mu.Lock()
	conn := sess.conn
	sess.mu.Unlock()
	if conn == nil {
		return nil
	}
	return writeSessionMessage(sess, conn, data)
}

func writeSessionMessage(sess *ptySession, conn *websocket.Conn, data []byte) error {
	if conn == nil {
		return nil
	}
	sess.writeMu.Lock()
	defer sess.writeMu.Unlock()
	return writeSessionMessageLocked(sess, conn, data)
}

func writeSessionMessageLocked(sess *ptySession, conn *websocket.Conn, data []byte) error {
	if conn == nil {
		return nil
	}
	sess.mu.Lock()
	current := sess.conn
	sess.mu.Unlock()
	if current != conn {
		return errStalePTYConnection
	}
	_ = conn.SetWriteDeadline(time.Now().Add(ptyWriteDeadline))
	return conn.WriteMessage(websocket.TextMessage, data)
}

func markDisconnected(sess *ptySession, conn *websocket.Conn) {
	sess.mu.Lock()
	if sess.conn != conn {
		sess.mu.Unlock()
		slog.Debug("pty.stale_disconnect_ignored", "session_id", sess.id, "pid", sess.pid)
		return
	}
	sess.conn = nil
	sess.connected = false
	sess.disconnected = time.Now()
	sess.mu.Unlock()
	slog.Info("pty.session_disconnected", "session_id", sess.id, "pid", sess.pid,
		"uptime_s", time.Since(sess.startedAt).Seconds())
}

func isTimeoutError(err error) bool {
	if ne, ok := err.(interface{ Timeout() bool }); ok {
		return ne.Timeout()
	}
	return false
}

func handleExec(w http.ResponseWriter, r *http.Request) {
	writeExecError := func(status int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}

	if r.Method != http.MethodPost {
		writeExecError(http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		Command []string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeExecError(http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Command) < 2 {
		writeExecError(http.StatusBadRequest, "command requires at least program and subcommand")
		return
	}

	if req.Command[0] == "codex-auth" {
		slog.Info("exec.codex-auth", "subcommand", req.Command[1])
		w.Header().Set("Content-Type", "application/json")
		switch req.Command[1] {
		case "check":
			_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": 0, "stdout": codexAuthCheck(), "stderr": ""})
		case "test":
			model := "gpt-5.5"
			if len(req.Command) >= 3 && req.Command[2] != "" {
				model = req.Command[2]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": 0, "stdout": codexAuthTest(model), "stderr": ""})
		case "generate-url":
			result, err := codexAuthGenerateURL()
			if err != nil {
				_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": 1, "stdout": "", "stderr": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": 0, "stdout": result, "stderr": ""})
		case "exchange":
			if len(req.Command) < 3 {
				_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": 1, "stdout": "", "stderr": "missing callback URL argument"})
				return
			}
			result, err := codexAuthExchange(req.Command[2])
			if err != nil {
				_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": 1, "stdout": "", "stderr": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": 0, "stdout": result, "stderr": ""})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": 1, "stdout": "", "stderr": fmt.Sprintf("unknown codex-auth subcommand: %s", req.Command[1])})
		}
		return
	}

	if req.Command[0] == "ocm-hermes-config" {
		if !isAllowedHermesConfigCommand(req.Command) {
			writeExecError(http.StatusForbidden, "ocm-hermes-config command is not allowed")
			return
		}
		runExecCommand(w, r, req.Command)
		return
	}

	if req.Command[0] != "openclaw" {
		writeExecError(http.StatusForbidden, "only openclaw and ocm-hermes-config commands are allowed")
		return
	}
	if !allowedExecSubcommands[req.Command[1]] {
		writeExecError(http.StatusForbidden, fmt.Sprintf("subcommand %q is not allowed", req.Command[1]))
		return
	}

	runExecCommand(w, r, req.Command)
}

func isAllowedHermesConfigCommand(command []string) bool {
	if len(command) < 2 || command[0] != "ocm-hermes-config" || command[1] != "sync" {
		return false
	}
	for _, arg := range command[2:] {
		if arg != "--restart-gateway" {
			return false
		}
	}
	return true
}

func runExecCommand(w http.ResponseWriter, r *http.Request, command []string) {
	slog.Info("exec.start", "command", command)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Env = os.Environ()

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			exitCode = -1
			stderrBuf.WriteString("command timed out after 30s")
		} else {
			exitCode = -1
			stderrBuf.WriteString(err.Error())
		}
	}

	stdoutStr := stdoutBuf.String()
	stderrStr := stderrBuf.String()
	if len(stdoutStr) > maxExecOutputBytes {
		stdoutStr = stdoutStr[:maxExecOutputBytes] + "\n... (truncated)"
	}
	if len(stderrStr) > maxExecOutputBytes {
		stderrStr = stderrStr[:maxExecOutputBytes] + "\n... (truncated)"
	}

	slog.Info("exec.done", "command", command, "exit_code", exitCode)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"exit_code": exitCode,
		"stdout":    stdoutStr,
		"stderr":    stderrStr,
	})
}

func handleWriteFile(w http.ResponseWriter, r *http.Request) {
	writeErr := func(status int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}

	if r.Method != http.MethodPost {
		writeErr(http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" {
		writeErr(http.StatusBadRequest, "path is required")
		return
	}

	allowedBase := "/home/openclaw/.openclaw/"
	if resolved, err := filepath.EvalSymlinks("/home/openclaw/.openclaw"); err == nil {
		allowedBase = resolved + "/"
	}
	allowedFiles := map[string]bool{"SOUL.md": true}
	const maxSize = 1024 * 1024

	if len(req.Content) > maxSize {
		writeErr(http.StatusBadRequest, "content too large (max 1MB)")
		return
	}

	cleanPath := filepath.Clean(req.Path)
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		writeErr(http.StatusBadRequest, "invalid path")
		return
	}
	if !allowedFiles[filepath.Base(absPath)] {
		writeErr(http.StatusForbidden, "filename not allowed")
		return
	}

	parentDir := filepath.Dir(absPath)
	realParent, err := filepath.EvalSymlinks(parentDir)
	if err != nil {
		ancestor := parentDir
		for ancestor != "/" {
			if rp, e := filepath.EvalSymlinks(ancestor); e == nil {
				if !strings.HasPrefix(rp+"/", allowedBase) {
					writeErr(http.StatusForbidden, "path outside allowed directory")
					return
				}
				break
			}
			ancestor = filepath.Dir(ancestor)
		}
		realParent = parentDir
	}
	realPath := filepath.Join(realParent, filepath.Base(absPath))
	if !strings.HasPrefix(realPath, allowedBase) {
		writeErr(http.StatusForbidden, "path outside allowed directory")
		return
	}
	if err := os.MkdirAll(filepath.Dir(realPath), 0o755); err != nil {
		writeErr(http.StatusInternalServerError, "failed to create directory")
		return
	}
	if info, err := os.Lstat(realPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		writeErr(http.StatusForbidden, "target is a symlink")
		return
	}
	if err := os.WriteFile(realPath, []byte(req.Content), 0o644); err != nil {
		writeErr(http.StatusInternalServerError, "failed to write file")
		return
	}
	_ = os.Chown(filepath.Dir(realPath), 1000, 1000)
	_ = os.Chown(realPath, 1000, 1000)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
