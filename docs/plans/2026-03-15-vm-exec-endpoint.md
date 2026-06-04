# VM Exec Endpoint Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a structured exec endpoint so the dashboard can run OpenClaw CLI commands inside VMs and get structured responses (exit code, stdout, stderr). First use: fix the pairing bug.

**Architecture:** The PTY server inside the VM (port 7681) gets a new `/exec` HTTP endpoint that runs a command and returns JSON. The agent proxies this on the proxy API (port 9091). The backend API proxies to the agent. The frontend calls the backend API.

**Security:** Command allowlist restricts to specific openclaw subcommands (initially `pairing`). Output capped at 64KB. Backend route gated to owner/admin roles. All error responses use JSON envelope.

**Tech Stack:** Go (backend + agent + PTY server), TypeScript/React (frontend)

---

## File Structure

| File | Responsibility |
|------|---------------|
| `backend/cmd/agent/ptyserver.go` | Add `/exec` HTTP handler inside the VM |
| `backend/internal/agentapi/server.go` | Register agent-side exec route |
| `backend/internal/agentapi/proxy.go` | Add `handleExecProxy` — proxy POST to VM:7681/exec |
| `backend/internal/api/machine_exec.go` | **NEW** — backend API handler, proxies to agent |
| `backend/internal/api/server.go` | Register backend exec route |
| `frontend/src/lib/api.ts` | Add `machineExec()` helper |
| `frontend/src/components/ChannelSetup.tsx` | Fix pairing to use exec |

---

## Task 1: PTY Server `/exec` Endpoint

The PTY server already runs inside the VM on port 7681. Add an `/exec` endpoint that runs a command and returns structured JSON.

**Files:**
- Modify: `backend/cmd/agent/ptyserver.go`

- [ ] **Step 1: Add the exec handler to `runPTYServer`**

In `ptyserver.go`, add the handler registration alongside the existing `/health` and `/restart-gateway` handlers (around line 315):

```go
// allowedSubcommands restricts which openclaw subcommands can be run via exec.
// Expand this list as new use cases are validated.
var allowedExecSubcommands = map[string]bool{
    "pairing": true,
    "status":  true,
    "doctor":  true,
}

const maxExecOutputBytes = 64 * 1024 // 64KB cap per stream

// writeExecError writes a JSON error response (consistent envelope).
func writeExecError(w http.ResponseWriter, status int, msg string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "error": msg,
    })
}

http.HandleFunc("/exec", func(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        writeExecError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }

    var req struct {
        Command []string `json:"command"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeExecError(w, http.StatusBadRequest, "invalid request body")
        return
    }
    if len(req.Command) < 2 {
        writeExecError(w, http.StatusBadRequest, "command requires at least program and subcommand")
        return
    }

    // Only allow openclaw CLI with approved subcommands
    if req.Command[0] != "openclaw" {
        writeExecError(w, http.StatusForbidden, "only openclaw commands are allowed")
        return
    }
    if !allowedExecSubcommands[req.Command[1]] {
        writeExecError(w, http.StatusForbidden, fmt.Sprintf("subcommand %q is not allowed", req.Command[1]))
        return
    }

    slog.Info("exec.start", "command", req.Command)

    ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
    defer cancel()

    cmd := exec.CommandContext(ctx, req.Command[0], req.Command[1:]...)
    cmd.Env = os.Environ()

    // Use LimitedWriter to cap output size
    var stdoutBuf, stderrBuf bytes.Buffer
    cmd.Stdout = &io.LimitedReader{R: nil} // placeholder — see below
    cmd.Stderr = &io.LimitedReader{R: nil}
    // Actually: pipe through limited buffers
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

    // Truncate output if over limit
    stdoutStr := stdoutBuf.String()
    stderrStr := stderrBuf.String()
    if len(stdoutStr) > maxExecOutputBytes {
        stdoutStr = stdoutStr[:maxExecOutputBytes] + "\n... (truncated)"
    }
    if len(stderrStr) > maxExecOutputBytes {
        stderrStr = stderrStr[:maxExecOutputBytes] + "\n... (truncated)"
    }

    slog.Info("exec.done", "command", req.Command, "exit_code", exitCode)

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "exit_code": exitCode,
        "stdout":    stdoutStr,
        "stderr":    stderrStr,
    })
})
```

Add `"context"` to the imports if not already present.

- [ ] **Step 2: Verify ptyserver.go compiles**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./cmd/agent/...`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add backend/cmd/agent/ptyserver.go
git commit -m "feat: add /exec endpoint to PTY server for structured command execution"
```

---

## Task 2: Agent Proxy for Exec

The agent needs to proxy exec requests from the proxy API (port 9091) to the VM's PTY server (port 7681).

**Files:**
- Modify: `backend/internal/agentapi/proxy.go`
- Modify: `backend/internal/agentapi/server.go`

- [ ] **Step 1: Add `handleExecProxy` to `proxy.go`**

Follow the same pattern as `handleRestartGateway` (line 631) — resolve VM info, proxy HTTP POST:

```go
// handleExecProxy proxies structured command execution to the PTY server
// running inside the MicroVM. Commands run as the openclaw user with a 30s timeout.
func (s *Server) handleExecProxy(w http.ResponseWriter, r *http.Request) {
	mi, err := s.getMachineInfo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !validateProxyToken(w, r, mi.ProxyToken) {
		return
	}

	targetURL := fmt.Sprintf("http://%s:7681/exec", mi.VMIP)
	client := &http.Client{Timeout: 35 * time.Second} // slightly longer than VM-side 30s
	resp, err := client.Post(targetURL, "application/json", r.Body)
	if err != nil {
		http.Error(w, "failed to reach VM", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
```

- [ ] **Step 2: Register the route in `server.go`**

In `agentapi/server.go`, add the route inside the proxy router (`ProxyRouter`), around line 138:

```go
r.Post("/proxy/{machineID}/exec", s.handleExecProxy)
```

- [ ] **Step 3: Verify compilation**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`
Expected: No errors

- [ ] **Step 4: Commit**

```bash
git add backend/internal/agentapi/proxy.go backend/internal/agentapi/server.go
git commit -m "feat: add exec proxy route to agent API"
```

---

## Task 3: Backend API Exec Endpoint

The backend API needs to proxy exec requests from the dashboard to the agent.

**Files:**
- Create: `backend/internal/api/machine_exec.go`
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Create `machine_exec.go`**

Follow the pattern of existing machine handlers (ownership check, resolve host, proxy to agent):

```go
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleMachineExec(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	if machine.Status != "running" {
		writeError(w, http.StatusBadRequest, "machine is not running")
		return
	}

	if machine.HostID == nil {
		writeError(w, http.StatusBadRequest, "machine is not assigned to a host")
		return
	}

	host, err := s.store.GetHost(r.Context(), *machine.HostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "host not found")
		return
	}

	var hostIP string
	if host.ExternalIP != nil {
		hostIP = *host.ExternalIP
	} else if host.InternalIP != nil {
		hostIP = *host.InternalIP
	} else {
		writeError(w, http.StatusServiceUnavailable, "host has no reachable IP")
		return
	}

	// Read and forward the request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	agentURL := fmt.Sprintf("http://%s:9091/proxy/%s/exec", hostIP, machine.ID)

	slog.Info("machine.exec", "machine_id", id, "account_id", accountID)

	client := &http.Client{Timeout: 40 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, agentURL, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if machine.ProxyToken != nil {
		req.Header.Set("X-Proxy-Token", *machine.ProxyToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to reach agent")
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
```

- [ ] **Step 2: Register the route in `server.go`**

Add the route inside the owner/admin-only group (around line 373):

```go
// Inside r.Group(func(r chi.Router) { r.Use(requireRole("owner", "admin")) ... })
r.Post("/exec", srv.handleMachineExec)
```

- [ ] **Step 3: Verify compilation**

Run: `cd /home/mantiz/OpenClawMachines/backend && go build ./...`
Expected: No errors

- [ ] **Step 4: Write a unit test**

Create `backend/internal/api/machine_exec_test.go`:

```go
package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleMachineExec_MachineNotRunning(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(map[string]interface{}{
		"command": []string{"openclaw", "pairing", "approve", "telegram", "ABCD1234"},
	})

	r := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewReader(body))
	r = withTestAccount(r, 1)
	r = withURLParam(r, "id", testMachineID)
	w := httptest.NewRecorder()

	srv.handleMachineExec(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-running machine, got %d", w.Code)
	}
}
```

Note: Adapt test helpers (`newTestServer`, `withTestAccount`, `withURLParam`) to match existing test patterns in `machine_config_test.go`.

- [ ] **Step 5: Run tests**

Run: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/api/... -v -run TestHandleMachineExec`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add backend/internal/api/machine_exec.go backend/internal/api/machine_exec_test.go backend/internal/api/server.go
git commit -m "feat: add machine exec endpoint to backend API"
```

---

## Task 4: Frontend — API Helper and Pairing Fix

**Files:**
- Modify: `frontend/src/lib/api.ts`
- Modify: `frontend/src/components/ChannelSetup.tsx`

- [ ] **Step 1: Add `machineExec` helper to `api.ts`**

Add alongside the existing `gatewayRPC` function:

```typescript
export interface ExecResult {
  exit_code: number;
  stdout: string;
  stderr: string;
}

export const machineExec = (accountId: number, machineId: string, command: string[]) =>
  request<ExecResult>(`/accounts/${accountId}/machines/${machineId}/exec`, {
    method: "POST",
    body: JSON.stringify({ command }),
  });
```

- [ ] **Step 2: Fix pairing in `ChannelSetup.tsx`**

Replace the `gatewayRPC` call in `handlePairingSubmit` (around line 475):

```typescript
// Before:
await gatewayRPC(accountId, pairingMachineId, "node.pair.approve", {
  channel: pairingChannel,
  code: pairingCode.toUpperCase(),
});

// After:
const result = await machineExec(accountId, pairingMachineId, [
  "openclaw", "pairing", "approve", pairingChannel, pairingCode.toUpperCase(),
]);
if (result.exit_code !== 0) {
  throw new Error(result.stderr || result.stdout || "Pairing failed");
}
```

Update the import at the top of the file — add `machineExec`, remove `gatewayRPC` if unused elsewhere in the file.

- [ ] **Step 3: Verify frontend compiles**

Run: `cd /home/mantiz/OpenClawMachines && make typecheck`
Expected: No errors

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/api.ts frontend/src/components/ChannelSetup.tsx
git commit -m "fix: use exec endpoint for channel pairing instead of wrong gateway RPC"
```

---

## Task 5: Verification

- [ ] **Step 1: Run all Go tests**

Run: `make test-go`
Expected: All pass

- [ ] **Step 2: Run frontend typecheck**

Run: `make typecheck`
Expected: No errors

- [ ] **Step 3: Push**

```bash
git push
```
