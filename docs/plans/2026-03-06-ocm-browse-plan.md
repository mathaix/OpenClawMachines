# `ocm browse` Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Let AI agents control the user's local Chrome browser via CDP, using a reverse SSH tunnel through the existing Cloudflare tunnel infrastructure.

**Architecture:** A TCP proxy on the bridge network (`bridgeGateway:9222`) routes CDP traffic by source IP to either the companion browser VM (default) or the user's local Chrome (via reverse SSH tunnel). The gateway never changes its `cdpUrl` — only the proxy target changes.

**Tech Stack:** Go (backend proxy, metadata, CLI), TCP proxying with `io.Copy`, Cobra CLI, `os/exec` for subprocess management.

---

## Task 1: CDP Proxy Package

**Files:**
- Create: `backend/internal/cdpproxy/proxy.go`

**Step 1: Write the proxy**

```go
package cdpproxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
)

// TargetResolver returns the CDP target address for a given source IP.
type TargetResolver interface {
	GetBrowserTarget(vmIP string) string
}

// Proxy is a TCP proxy that forwards CDP traffic from VMs to browser targets.
// It sits on the bridge network and routes by source IP.
type Proxy struct {
	resolver TargetResolver
	bindAddr string
	port     int
	listener net.Listener
}

// New creates a CDP proxy bound to the given address and port.
func New(resolver TargetResolver, bindAddr string, port int) *Proxy {
	return &Proxy{
		resolver: resolver,
		bindAddr: bindAddr,
		port:     port,
	}
}

// Start starts the TCP proxy. Blocks until context is cancelled.
func (p *Proxy) Start(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", p.bindAddr, p.port)
	var err error
	p.listener, err = net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cdpproxy: listen %s: %w", addr, err)
	}
	slog.Info("cdpproxy.started", "addr", addr)

	go func() {
		<-ctx.Done()
		p.listener.Close()
	}()

	for {
		conn, err := p.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("cdpproxy.accept_error", "error", err)
			continue
		}
		go p.handleConn(conn)
	}
}

func (p *Proxy) handleConn(src net.Conn) {
	defer src.Close()

	srcIP := extractIP(src.RemoteAddr().String())
	target := p.resolver.GetBrowserTarget(srcIP)
	if target == "" {
		slog.Warn("cdpproxy.no_target", "src_ip", srcIP)
		return
	}

	dst, err := net.Dial("tcp", target)
	if err != nil {
		slog.Warn("cdpproxy.dial_failed", "src_ip", srcIP, "target", target, "error", err)
		return
	}
	defer dst.Close()

	slog.Debug("cdpproxy.connected", "src_ip", srcIP, "target", target)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(dst, src) }()
	go func() { defer wg.Done(); io.Copy(src, dst) }()
	wg.Wait()
}

func extractIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
```

**Step 2: Run tests**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/internal/cdpproxy/`
Expected: builds cleanly

**Step 3: Commit**

```bash
git add backend/internal/cdpproxy/proxy.go
git commit -m "feat(cdpproxy): add TCP proxy for CDP traffic routing by source IP"
```

---

## Task 2: Metadata Server — Browser Target Tracking

**Files:**
- Modify: `backend/internal/metadata/metadata.go`

**Step 1: Add browser target map and methods**

Add a new field to `Server`:
```go
browserTargets map[string]string // vmIP -> override target address (e.g. "192.168.100.2:9222")
```

Initialize it in `New()`:
```go
browserTargets: make(map[string]string),
```

Add three methods after `UnregisterMachine`:

```go
// GetBrowserTarget returns the CDP target for a VM.
// Returns the override if set, otherwise "browserVMIP:9222" from config,
// or empty string if no browser is configured.
func (s *Server) GetBrowserTarget(vmIP string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if target, ok := s.browserTargets[vmIP]; ok {
		return target
	}
	cfg, ok := s.configs[vmIP]
	if !ok {
		return ""
	}
	if cfg.BrowserVMIP == "" {
		return ""
	}
	return cfg.BrowserVMIP + ":9222"
}

// SetBrowserTarget overrides the CDP target for a VM.
// New connections will route to this target. Existing connections are unaffected.
func (s *Server) SetBrowserTarget(vmIP, target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.browserTargets[vmIP] = target
	slog.Info("metadata.browser_target.set", "vm_ip", vmIP, "target", target)
}

// ResetBrowserTarget removes the CDP target override for a VM,
// reverting to the default companion browser VM.
func (s *Server) ResetBrowserTarget(vmIP string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.browserTargets, vmIP)
	slog.Info("metadata.browser_target.reset", "vm_ip", vmIP)
}
```

Also add `BrowserVMIP` to `MachineConfig`:
```go
BrowserVMIP string `json:"-"` // companion browser VM IP for CDP proxy default target
```

Clean up `browserTargets` in `UnregisterMachine`:
```go
delete(s.browserTargets, vmIP)
```

**Step 2: Verify build**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/internal/metadata/`

**Step 3: Verify metadata server satisfies TargetResolver interface**

The `*metadata.Server` now has `GetBrowserTarget(string) string`, matching the `cdpproxy.TargetResolver` interface.

**Step 4: Commit**

```bash
git add backend/internal/metadata/metadata.go
git commit -m "feat(metadata): add per-VM browser target tracking for CDP proxy"
```

---

## Task 3: Config Assembly — cdpUrl Points to Bridge

**Files:**
- Modify: `backend/internal/configassembly/assembler.go` (lines 213-223 and 267-279)
- Modify: `backend/internal/configassembly/assembler_test.go`

**Step 1: Add BridgeIP to AssemblyParams**

In `assembler.go`, add field to `AssemblyParams` struct:
```go
BridgeIP    string            // bridge gateway IP for CDP proxy (e.g. "192.168.100.1")
```

**Step 2: Change cdpUrl injection**

Replace the browser cdpUrl block (lines 267-279):

```go
// Before:
if params.BrowserVMIP != "" {
    browser := getOrCreateMap(result, "browser")
    if enabled, _ := browser["enabled"].(bool); enabled {
        browser["cdpUrl"] = fmt.Sprintf("http://%s:9222", params.BrowserVMIP)

// After:
browserCDPHost := params.BridgeIP
if browserCDPHost == "" {
    browserCDPHost = params.BrowserVMIP // fallback for tests without bridge
}
if browserCDPHost != "" {
    browser := getOrCreateMap(result, "browser")
    if enabled, _ := browser["enabled"].(bool); enabled {
        browser["cdpUrl"] = fmt.Sprintf("http://%s:9222", browserCDPHost)
```

**Step 3: Update tests**

In `assembler_test.go`, update the test at line 1065:
- Change `BrowserVMIP: "192.168.100.110"` to also set `BridgeIP: "192.168.100.1"`
- Update expected cdpUrl from `"http://192.168.100.110:9222"` to `"http://192.168.100.1:9222"`

For the `TestBrowserCdpUrlNotSetWithoutIP` test: keep as-is (both empty = no cdpUrl).

For the `TestBrowserCdpUrlNotSetWhenDisabled` test: add `BridgeIP: "192.168.100.1"` but expected behavior stays the same (no cdpUrl when browser not enabled).

**Step 4: Run tests**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/configassembly/ -v -run Browser`
Expected: all 3 browser tests pass

**Step 5: Update production call site**

In `backend/internal/api/machine_config.go` around line 350, add `BridgeIP` to the `AssemblyParams`. This needs the bridge IP passed to the server — but the Cloud Run backend doesn't have a bridge IP (it assembles config for preview, not for VMs). Leave `BridgeIP` empty here; the fallback to `BrowserVMIP` handles this case.

The real `BridgeIP` gets injected at the agent level where the orchestrator calls the assembler — check where `BrowserVMIP` is passed and add `BridgeIP` alongside it. This is in the orchestrator's config assembly path.

**Step 6: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/configassembly/assembler_test.go
git commit -m "feat(configassembly): route browser cdpUrl through bridge CDP proxy"
```

---

## Task 4: Register BrowserVMIP in Metadata on VM Create

**Files:**
- Modify: `backend/internal/agentapi/handlers.go` (handleCreateVM, around line 266-291)

**Step 1: Pass BrowserVMIP into MachineConfig on RegisterMachine**

The orchestrator already passes `BrowserVMIP` through `VMConfig`. Check where `RegisterMachine` is called and ensure `BrowserVMIP` is included in the `MachineConfig`.

Search for where `metadata.MachineConfig` is constructed in the orchestrator to verify `BrowserVMIP` is set.

**Step 2: Verify**

Run: `cd /home/mantiz/OpenClawMachines && grep -n "BrowserVMIP" backend/internal/orchestrator/*.go`

If `BrowserVMIP` is already being passed through, this task is already done. If not, add it to the `metadata.MachineConfig` construction in the orchestrator.

**Step 3: Commit (if changes needed)**

```bash
git commit -m "feat(orchestrator): pass BrowserVMIP to metadata for CDP proxy routing"
```

---

## Task 5: Agent Wiring — Start CDP Proxy

**Files:**
- Modify: `backend/cmd/agent/main.go` (after line 164, before line 167)

**Step 1: Add import**

Add to imports:
```go
"github.com/mathaix/openclawmachines/backend/internal/cdpproxy"
```

**Step 2: Start CDP proxy after API proxy**

After line 164 (`slog.Info("apiproxy.starting", ...)`), add:

```go
// 5b. Start CDP proxy on bridge IP:9222
cdpProxy := cdpproxy.New(metaSrv, bridgeGateway, 9222)
go func() {
    if err := cdpProxy.Start(ctx); err != nil && ctx.Err() == nil {
        if runtime.GOOS != "linux" {
            slog.Warn("cdpproxy.not_available", "error", err, "note", "expected on non-Linux")
        } else {
            slog.Error("cdpproxy.failed", "error", err)
            os.Exit(1)
        }
    }
}()
slog.Info("cdpproxy.starting", "addr", bridgeGateway+":9222")
```

**Step 3: Verify build**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/cmd/agent/`
Expected: builds cleanly

**Step 4: Commit**

```bash
git add backend/cmd/agent/main.go
git commit -m "feat(agent): start CDP proxy on bridge network at port 9222"
```

---

## Task 6: Agent API — Remap Endpoint

**Files:**
- Modify: `backend/internal/agentapi/server.go`
- Modify: `backend/internal/agentapi/handlers.go`

**Step 1: Add metadata server reference to Server**

In `server.go`, add field to `Server`:
```go
metaSrv *metadata.Server
```

Update `NewServer` signature and body to accept and store it:
```go
func NewServer(agentToken string, orch orchestrator.Orchestrator, allowedCIDRs string, proxy *apiproxy.Proxy, metadataAddr string, updater *selfupdate.Updater, metaSrv *metadata.Server) *Server {
```

Add import for metadata package.

**Step 2: Register routes in ControlRouter**

In `server.go` `ControlRouter()`, inside the authenticated group (after line 74), add:

```go
r.Post("/vms/{machineID}/cdp-target", s.handleSetCDPTarget)
r.Delete("/vms/{machineID}/cdp-target", s.handleResetCDPTarget)
```

**Step 3: Add handlers in handlers.go**

```go
// handleSetCDPTarget remaps the CDP proxy target for a VM.
func (s *Server) handleSetCDPTarget(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	vm, err := s.orchestrator.Get(r.Context(), machineID)
	if err != nil {
		http.Error(w, "vm not found", http.StatusNotFound)
		return
	}

	var req struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Target == "" {
		http.Error(w, "target is required", http.StatusBadRequest)
		return
	}

	if s.metaSrv == nil {
		http.Error(w, "metadata server not available", http.StatusServiceUnavailable)
		return
	}

	s.metaSrv.SetBrowserTarget(vm.VMIP, req.Target)
	slog.Info("cdp.target.set", "machine_id", machineID, "vm_ip", vm.VMIP, "target", req.Target)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "target": req.Target})
}

// handleResetCDPTarget reverts the CDP proxy target to the companion browser VM.
func (s *Server) handleResetCDPTarget(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "machineID")

	if s.orchestrator == nil {
		http.Error(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	vm, err := s.orchestrator.Get(r.Context(), machineID)
	if err != nil {
		http.Error(w, "vm not found", http.StatusNotFound)
		return
	}

	if s.metaSrv == nil {
		http.Error(w, "metadata server not available", http.StatusServiceUnavailable)
		return
	}

	s.metaSrv.ResetBrowserTarget(vm.VMIP)
	slog.Info("cdp.target.reset", "machine_id", machineID, "vm_ip", vm.VMIP)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

**Step 4: Update agent main.go call site**

In `backend/cmd/agent/main.go` line 194, update `NewServer` call to pass `metaSrv`:

```go
srv := agentapi.NewServer(cfg.AgentToken, orch, cfg.ControlAllowedCIDRs, proxy, metadataAddr, updater, metaSrv)
```

**Step 5: Verify build**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/cmd/agent/`

**Step 6: Run existing tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`

**Step 7: Commit**

```bash
git add backend/internal/agentapi/server.go backend/internal/agentapi/handlers.go backend/cmd/agent/main.go
git commit -m "feat(agentapi): add CDP target remap endpoints for ocm browse"
```

---

## Task 7: Backend API — Proxy Endpoint

**Files:**
- Modify: `backend/internal/api/server.go` (or wherever machine routes are defined)

**Step 1: Find machine route registration**

Search for where `/machines/{id}/stop` or similar routes are registered in the backend API server.

**Step 2: Add proxy route**

```go
r.Post("/api/accounts/{accountID}/machines/{machineID}/cdp-target", s.handleSetCDPTargetProxy)
r.Delete("/api/accounts/{accountID}/machines/{machineID}/cdp-target", s.handleResetCDPTargetProxy)
```

**Step 3: Add handler that proxies to agent**

The handler resolves machine → host agent URL, then forwards the request. Follow the same pattern as existing machine management endpoints that call the agent.

```go
func (s *Server) handleSetCDPTargetProxy(w http.ResponseWriter, r *http.Request) {
	// Authenticate, resolve machine → host
	// Forward POST to agent: http://host:9090/vms/{machineID}/cdp-target
}

func (s *Server) handleResetCDPTargetProxy(w http.ResponseWriter, r *http.Request) {
	// Authenticate, resolve machine → host
	// Forward DELETE to agent: http://host:9090/vms/{machineID}/cdp-target
}
```

**Step 4: Verify build**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/cmd/server/`

**Step 5: Commit**

```bash
git commit -m "feat(api): add CDP target proxy endpoints for ocm browse"
```

---

## Task 8: CLI Command — `ocm machines browse`

**Files:**
- Create: `cli/internal/commands/machines_browse.go`

**Step 1: Write the command**

```go
package commands

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var machinesBrowseCmd = &cobra.Command{
	Use:   "browse [NAME]",
	Short: "Let AI control your local Chrome browser",
	Long: `Launch local Chrome with remote debugging and tunnel it to the AI agent.

The AI agent will control your local browser — visible on your screen, using your
logins and cookies. Press Ctrl-C to stop and revert to the companion browser VM.

Requirements:
  - Google Chrome or Chromium installed locally
  - Port 9222 available (or use --port to specify)

Examples:
  ocm machines browse
  ocm machines browse "My Bot"
  ocm machines browse --no-launch       # attach to already-running Chrome
  ocm machines browse --port 9223`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeMachineNames,
	RunE:              runBrowse,
}

func runBrowse(cmd *cobra.Command, args []string) error {
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

	client := newAPIClient()
	machine, err := resolveMachine(client, name)
	if err != nil {
		return err
	}

	if machine.Status != "running" {
		return fmt.Errorf("machine %q is not running (status: %s)", machine.Name, machine.Status)
	}

	// 1. Launch Chrome (unless --no-launch)
	var chromeCmd *exec.Cmd
	if !noLaunch {
		chromeBin := chromePath
		if chromeBin == "" {
			chromeBin = findChrome()
		}
		if chromeBin == "" {
			return fmt.Errorf("Chrome not found. Install Chrome or use --chrome-path")
		}

		chromeCmd = exec.Command(chromeBin,
			fmt.Sprintf("--remote-debugging-port=%d", port),
			"--no-first-run",
			"--no-default-browser-check",
		)
		chromeCmd.Stdout = os.Stderr
		chromeCmd.Stderr = os.Stderr
		if err := chromeCmd.Start(); err != nil {
			return fmt.Errorf("starting Chrome: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Chrome started with remote debugging on port %d\n", port)
		time.Sleep(2 * time.Second) // wait for Chrome to initialize
	}

	// 2. SSH cert + ProxyCommand (reuse from machines_ssh.go)
	domain := cfg.CfAppDomain
	if domain == "" {
		domain = defaultDataPlaneDomain
	}
	sshHost := fmt.Sprintf("ssh-%s.%s", machine.Slug, domain)

	certKeyPath, err := generateSSHCert(sshHost)
	if err != nil {
		killProc(chromeCmd)
		return fmt.Errorf("generating SSH certificate: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		killProc(chromeCmd)
		return fmt.Errorf("resolving executable path: %w", err)
	}

	proxyCmd := fmt.Sprintf("%s machines ssh-proxy %s", self, "%h")
	if cfgFile != "" {
		proxyCmd = fmt.Sprintf("%s --config %s machines ssh-proxy %s", self, cfgFile, "%h")
	}

	// 3. Start SSH reverse tunnel
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		killProc(chromeCmd)
		return fmt.Errorf("ssh not found in PATH: %w", err)
	}

	sshArgs := []string{
		"-l", "openclaw",
		"-i", certKeyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-o", fmt.Sprintf("ProxyCommand=%s", proxyCmd),
		"-R", fmt.Sprintf("0.0.0.0:9222:localhost:%d", port),
		"-N",
		sshHost,
	}

	sshCmd := exec.Command(sshBin, sshArgs...)
	sshCmd.Stdout = os.Stderr
	sshCmd.Stderr = os.Stderr
	if err := sshCmd.Start(); err != nil {
		killProc(chromeCmd)
		return fmt.Errorf("starting SSH tunnel: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Reverse tunnel established (%s → localhost:%d)\n", sshHost, port)

	// Wait for tunnel to establish
	time.Sleep(3 * time.Second)

	// 4. Remap CDP target
	remapPath := fmt.Sprintf("/api/accounts/%d/machines/%s/cdp-target", cfg.DefaultAccountID, machine.ID)
	remapBody, _ := json.Marshal(map[string]string{"target": fmt.Sprintf("127.0.0.1:%d", 9222)})
	resp, err := client.Post(remapPath, remapBody)
	if err != nil {
		killProc(sshCmd)
		killProc(chromeCmd)
		return fmt.Errorf("remapping CDP target: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		killProc(sshCmd)
		killProc(chromeCmd)
		return fmt.Errorf("remap failed (status %d)", resp.StatusCode)
	}

	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "AI is now controlling your local Chrome.\n")
	fmt.Fprintf(os.Stderr, "Your cookies and logins are available to the AI.\n")
	fmt.Fprintf(os.Stderr, "Press Ctrl-C to stop.\n")

	// 5. Wait for signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	// 6. Cleanup
	fmt.Fprintf(os.Stderr, "\nCleaning up...\n")

	// Revert remap
	resp, err = client.Delete(remapPath)
	if err == nil {
		resp.Body.Close()
	}

	// Kill SSH tunnel
	killProc(sshCmd)

	// Kill Chrome if we launched it
	if !noLaunch {
		killProc(chromeCmd)
	}

	fmt.Fprintf(os.Stderr, "Reverted to companion browser VM.\n")
	return nil
}

func findChrome() string {
	switch runtime.GOOS {
	case "darwin":
		path := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if _, err := os.Stat(path); err == nil {
			return path
		}
	case "linux":
		for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium-browser", "chromium"} {
			if p, err := exec.LookPath(name); err == nil {
				return p
			}
		}
	}
	return ""
}

func killProc(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
		}
	}
}

func init() {
	machinesBrowseCmd.Flags().Int("port", 9222, "Local Chrome debugging port")
	machinesBrowseCmd.Flags().Bool("no-launch", false, "Don't launch Chrome (attach to existing)")
	machinesBrowseCmd.Flags().String("chrome-path", "", "Path to Chrome binary")

	machinesCmd.AddCommand(machinesBrowseCmd)
}
```

**Step 2: Check CLI API client has needed methods**

Verify `api.Client` has `Post` and `Delete` methods. If only `PostLong` and `DeleteLong` exist, use those instead.

**Step 3: Verify build**

Run: `cd /home/mantiz/OpenClawMachines && go build ./cli/...`

**Step 4: Commit**

```bash
git add cli/internal/commands/machines_browse.go
git commit -m "feat(cli): add 'ocm machines browse' command for local browser control"
```

---

## Task 9: sshd Config — GatewayPorts

**Files:**
- Modify: `scripts/init-openclaw.sh`

**Step 1: Add GatewayPorts to SSH cert config**

In Phase 12 (SSH cert auth), add `GatewayPorts yes` to the sshd drop-in config at `/etc/ssh/sshd_config.d/cf-access.conf`. This allows the reverse tunnel to bind on `0.0.0.0:9222` (reachable from the host via the bridge network), not just `127.0.0.1:9222`.

Add to the drop-in file content:
```
GatewayPorts yes
```

**Step 2: Verify init script still passes sshd -t**

This will be verified next time a rootfs is built and a VM boots.

**Step 3: Commit**

```bash
git add scripts/init-openclaw.sh
git commit -m "feat(init): enable GatewayPorts for reverse SSH tunnel binding"
```

---

## Task 10: Gateway E2E Tests

**Step 1: Run existing tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-gateway-e2e`
Expected: all pass (config assembly change should be backward-compatible)

**Step 2: Run full Go tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: all pass

---

## Implementation Notes

### What changes for existing VMs (no browse):
- `cdpUrl` now points to `bridgeIP:9222` instead of `browserVMIP:9222`
- CDP proxy forwards transparently to `browserVMIP:9222` (default target)
- Behavior is identical — just one extra TCP hop through the proxy

### What `ocm browse` does differently:
- Calls `SetBrowserTarget(vmIP, "vmIP:9222")` — proxy now routes to the VM's tunnel endpoint
- Reverse SSH tunnel inside VM listens on `0.0.0.0:9222` → user's local Chrome
- On Ctrl-C, calls `ResetBrowserTarget(vmIP)` — reverts to companion VM

### API client methods to verify:
The CLI `api.Client` may need `Post(path, body)` and `Delete(path)` methods. Check what exists and use the appropriate ones (may be `PostLong`/`DeleteLong` for longer timeouts, but remap calls are instant).

### Subprocess management:
`ocm browse` uses `os/exec.Command` (not `syscall.Exec`) because it must supervise Chrome + SSH tunnel + signal handling simultaneously. `syscall.Exec` replaces the process — can't manage children after that.
