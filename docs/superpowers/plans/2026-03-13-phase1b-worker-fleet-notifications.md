# Phase 1b: Worker Fleet + Invitation Email Workflow

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the backend binary into API and worker modes, add a notifications queue, and implement invitation email delivery as the first non-migration workflow.

**Architecture:** Cloud Run serves HTTP (API mode, no DBOS executor). Spot GCE workers run DBOS executors (worker mode, no HTTP server). Both connect to the same Postgres. The invitation email workflow proves the substrate works beyond machine operations.

**Tech Stack:** Go, DBOS Go v0.11.0, Resend HTTP API (no SDK), GCE managed instance group (spot), existing Neon Postgres.

**Spec:** `docs/designs/durable-workflows.md` (Deployment Model, Notification Workflows, Retry and Error Handling Policy sections)

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `backend/cmd/server/main.go` | Modify | Add `--mode` flag, split API vs worker startup paths |
| `backend/internal/config/config.go` | Modify | Add `RunMode`, `ResendAPIKey`, `WorkerExecutorID` fields |
| `backend/internal/workflows/runtime.go` | Modify | Pin `ApplicationVersion` to stable value |
| `backend/internal/workflows/types.go` | Modify | Add `QueueNotifications` constant, `KindNotification` |
| `backend/internal/email/resend.go` | Create | Thin Resend HTTP client |
| `backend/internal/email/resend_test.go` | Create | Tests for Resend client |
| `backend/internal/email/templates.go` | Create | Invitation email HTML template |
| `backend/internal/email/templates_test.go` | Create | Template rendering tests |
| `backend/internal/api/notification_workflow.go` | Create | Invitation email workflow + registration |
| `backend/internal/api/notification_workflow_test.go` | Create | Workflow tests |
| `backend/internal/api/invitations.go` | Modify | Trigger email workflow after creating invitation |
| `backend/internal/api/admin_migrate_workflow.go` | Modify | Add retry policies to existing steps |
| `backend/internal/api/server.go` | Modify | Add email client field, wire into server |

---

## Chunk 1: Binary Mode Split + Stable Version

### Task 1: Add run mode to config

**Files:**
- Modify: `backend/internal/config/config.go:10-48` (Config struct)
- Modify: `backend/internal/config/config.go:50-106` (Load function)

- [ ] **Step 1: Add RunMode and ResendAPIKey to Config struct**

In `backend/internal/config/config.go`, add after line 47 (`EnableDurableWorkflows`):

```go
RunMode       string // "api", "worker", or "" (default=both for backward compat)
ResendAPIKey  string
ExecutorID    string // unique worker ID (from GCE instance metadata or hostname)
```

- [ ] **Step 2: Load new config values in Load()**

After line 99 (`cfg.EnableDurableWorkflows = ...`), add:

```go
cfg.RunMode = getEnv("RUN_MODE", "")
cfg.ResendAPIKey = os.Getenv("RESEND_API_KEY")
cfg.ExecutorID = getEnv("EXECUTOR_ID", "")
if cfg.ExecutorID == "" {
    hostname, _ := os.Hostname()
    cfg.ExecutorID = hostname
}
```

- [ ] **Step 3: Commit**

```bash
git add backend/internal/config/config.go
git commit -m "feat: add RunMode, ResendAPIKey, ExecutorID to config"
```

### Task 2: Pin ApplicationVersion to stable value

**Files:**
- Modify: `backend/internal/workflows/runtime.go:30-35`

- [ ] **Step 1: Replace version.Version with stable constant**

In `backend/internal/workflows/runtime.go`, change line 33:

```go
// Before:
ApplicationVersion: version.Version,

// After:
ApplicationVersion: "v1", // Stable: only bump when workflow step signatures change
```

Remove the `version` import if it becomes unused.

- [ ] **Step 2: Run tests**

Run: `make test-go`
Expected: All pass (version was only used for DBOS app version, not functional)

- [ ] **Step 3: Commit**

```bash
git add backend/internal/workflows/runtime.go
git commit -m "fix: pin DBOS ApplicationVersion to stable value for deploy safety"
```

### Task 3: Split main.go into API and worker modes

**Files:**
- Modify: `backend/cmd/server/main.go`

- [ ] **Step 1: Add mode-aware startup logic**

The key change: when `RUN_MODE=worker`, skip HTTP server and only run DBOS executor. When `RUN_MODE=api`, skip DBOS executor. When empty (default), run both (backward compat).

Replace the workflow init block (lines 136-148) and HTTP server block (lines 165-184) with mode-aware logic:

```go
// After srv := api.NewServer(...) on line 134:

isAPI := cfg.RunMode == "" || cfg.RunMode == "api"
isWorker := cfg.RunMode == "" || cfg.RunMode == "worker"

// Workflow service — always create for projection queries, but only enable runtime for workers
workflowSvc, err := workflows.NewService(ctx, db, workflows.Config{
    AppName:       "openclawmachines-control-plane",
    DatabaseURL:   cfg.DatabaseURL,
    EnableRuntime: cfg.EnableDurableWorkflows && isWorker,
    Register:      srv.RegisterWorkflows,
})
if err != nil {
    slog.Error("workflow.init_failed", "error", err)
    os.Exit(1)
}
defer workflowSvc.Close(10 * time.Second)

srv.SetWorkflowService(workflowSvc)

if isAPI {
    // Start background tasks that only make sense for the API server
    srv.StartOAuthRefreshLoop(ctx)

    // Start host liveness reconciler
    computeClient, err := compute.NewInstancesRESTClient(ctx)
    if err != nil {
        slog.Error("reconciler.compute_client.failed", "error", err)
    } else {
        instanceChecker := reconciler.NewGCPInstanceChecker(computeClient)
        hostReconciler := reconciler.New(db, instanceChecker, machineRuntime, cfg.GCPProject, 180*time.Second)
        go hostReconciler.Start(ctx, 60*time.Second)
        slog.Info("reconciler.started")
    }

    httpServer := &http.Server{
        Addr:    ":" + cfg.Port,
        Handler: srv,
    }

    go func() {
        sigCh := make(chan os.Signal, 1)
        signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
        <-sigCh
        slog.Info("server.shutdown_start")
        cancel()
        _ = httpServer.Shutdown(context.Background())
    }()

    slog.Info("server.listen", "port", cfg.Port, "mode", cfg.RunMode)
    if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
        slog.Error("server.error", "error", err)
        os.Exit(1)
    }
} else {
    // Worker mode: block until signal, let DBOS executor do the work
    slog.Info("worker.started", "executor_id", cfg.ExecutorID, "mode", "worker")
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    <-sigCh
    slog.Info("worker.shutdown_start")
    cancel()
}
```

- [ ] **Step 2: Run tests**

Run: `make test-go`
Expected: All pass. No functional change when `RUN_MODE` is empty (backward compat).

- [ ] **Step 3: Commit**

```bash
git add backend/cmd/server/main.go
git commit -m "feat: add --mode=api/worker binary split for spot worker fleet"
```

### Task 4: Add notifications queue

**Files:**
- Modify: `backend/internal/workflows/types.go:26-31`
- Modify: `backend/internal/workflows/runtime.go:40-43`

- [ ] **Step 1: Add queue constant and workflow kind**

In `backend/internal/workflows/types.go`, add to the queue constants (after line 30):

```go
QueueNotifications = "notifications"
```

Add to the kind constants (after line 23):

```go
KindNotification = "send_notification"
```

- [ ] **Step 2: Register the queue in runtime**

In `backend/internal/workflows/runtime.go`, add after line 43 (`QueueReconcile`):

```go
dbos.NewWorkflowQueue(dbosCtx, QueueNotifications)
```

- [ ] **Step 3: Run tests**

Run: `make test-go`
Expected: All pass.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/workflows/types.go backend/internal/workflows/runtime.go
git commit -m "feat: add notifications queue and send_notification workflow kind"
```

---

## Chunk 2: Email Service (Resend Client)

### Task 5: Create Resend HTTP client

**Files:**
- Create: `backend/internal/email/resend.go`
- Create: `backend/internal/email/resend_test.go`

- [ ] **Step 1: Write failing test for SendEmail**

Create `backend/internal/email/resend_test.go`:

```go
package email

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendEmail_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/emails" {
			t.Errorf("expected /emails, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing or wrong auth header")
		}

		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["to"] != "alice@example.com" {
			t.Errorf("expected to=alice@example.com, got %v", body["to"])
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"id": "email_123"})
	}))
	defer server.Close()

	client := NewClient("test-key", WithBaseURL(server.URL))
	err := client.SendEmail(context.Background(), Email{
		From:    "noreply@openclawmachines.com",
		To:      "alice@example.com",
		Subject: "You're invited",
		HTML:    "<p>Join us</p>",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendEmail_Retryable(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient("test-key", WithBaseURL(server.URL))
	err := client.SendEmail(context.Background(), Email{
		From:    "noreply@openclawmachines.com",
		To:      "alice@example.com",
		Subject: "Test",
		HTML:    "<p>Test</p>",
	})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !IsRetryable(err) {
		t.Errorf("expected retryable error, got: %v", err)
	}
}

func TestSendEmail_NonRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"message": "invalid email"})
	}))
	defer server.Close()

	client := NewClient("test-key", WithBaseURL(server.URL))
	err := client.SendEmail(context.Background(), Email{
		From:    "noreply@openclawmachines.com",
		To:      "bad-email",
		Subject: "Test",
		HTML:    "<p>Test</p>",
	})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if IsRetryable(err) {
		t.Errorf("expected non-retryable error for 400, got: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/email/... -v`
Expected: FAIL — package does not exist

- [ ] **Step 3: Implement Resend client**

Create `backend/internal/email/resend.go`:

```go
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const defaultBaseURL = "https://api.resend.com"

type Email struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

type ClientOption func(*Client)

func WithBaseURL(url string) ClientOption {
	return func(c *Client) { c.baseURL = url }
}

func NewClient(apiKey string, opts ...ClientOption) *Client {
	c := &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		http:    &http.Client{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type SendError struct {
	StatusCode int
	Message    string
	Retryable  bool
}

func (e *SendError) Error() string {
	return fmt.Sprintf("email send failed (HTTP %d): %s", e.StatusCode, e.Message)
}

func IsRetryable(err error) bool {
	if se, ok := err.(*SendError); ok {
		return se.Retryable
	}
	return true // network errors are retryable
}

func (c *Client) SendEmail(ctx context.Context, e Email) error {
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal email: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/emails", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err // network error — retryable
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	msg := string(respBody)

	retryable := resp.StatusCode >= 500 || resp.StatusCode == 429
	return &SendError{
		StatusCode: resp.StatusCode,
		Message:    msg,
		Retryable:  retryable,
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/email/... -v`
Expected: All 3 tests PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/email/
git commit -m "feat: add Resend email client with retryable error classification"
```

### Task 6: Create invitation email template

**Files:**
- Create: `backend/internal/email/templates.go`
- Create: `backend/internal/email/templates_test.go`

- [ ] **Step 1: Write failing test for template rendering**

Create `backend/internal/email/templates_test.go`:

```go
package email

import (
	"strings"
	"testing"
)

func TestRenderInvitationEmail(t *testing.T) {
	html, err := RenderInvitationEmail(InvitationData{
		InviterEmail: "bob@example.com",
		AccountName:  "My Team",
		Role:         "member",
		AcceptURL:    "https://openclawmachines.com/invite/inv_abc123",
		ExpiryDays:   7,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(html, "bob@example.com") {
		t.Error("expected inviter email in output")
	}
	if !strings.Contains(html, "My Team") {
		t.Error("expected account name in output")
	}
	if !strings.Contains(html, "inv_abc123") {
		t.Error("expected accept link in output")
	}
	if !strings.Contains(html, "7 days") {
		t.Error("expected expiry in output")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/email/... -run TestRenderInvitation -v`
Expected: FAIL — function not defined

- [ ] **Step 3: Implement template**

Create `backend/internal/email/templates.go`:

```go
package email

import (
	"bytes"
	"fmt"
	"html/template"
)

type InvitationData struct {
	InviterEmail string
	AccountName  string
	Role         string
	AcceptURL    string
	ExpiryDays   int
}

var invitationTemplate = template.Must(template.New("invitation").Parse(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 600px; margin: 0 auto; padding: 20px;">
  <h2 style="color: #1a1a1a;">You've been invited to {{.AccountName}}</h2>
  <p style="color: #4a4a4a; font-size: 16px;">
    <strong>{{.InviterEmail}}</strong> has invited you to join
    <strong>{{.AccountName}}</strong> as a <strong>{{.Role}}</strong> on OpenClaw Machines.
  </p>
  <p style="margin: 30px 0;">
    <a href="{{.AcceptURL}}"
       style="background-color: #2563eb; color: white; padding: 12px 24px; text-decoration: none; border-radius: 6px; font-weight: 600;">
      Accept Invitation
    </a>
  </p>
  <p style="color: #6b7280; font-size: 14px;">
    This invitation expires in {{.ExpiryDays}} days. If you didn't expect this, you can ignore it.
  </p>
</body>
</html>`))

func RenderInvitationEmail(data InvitationData) (string, error) {
	var buf bytes.Buffer
	if err := invitationTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render invitation email: %w", err)
	}
	return buf.String(), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/email/... -v`
Expected: All tests PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/email/templates.go backend/internal/email/templates_test.go
git commit -m "feat: add invitation email HTML template"
```

---

## Chunk 3: Invitation Email Workflow

### Task 7: Wire email client into server

**Files:**
- Modify: `backend/internal/api/server.go` (add email client field)
- Modify: `backend/cmd/server/main.go` (create and inject email client)

- [ ] **Step 1: Add email client to Server struct**

In `backend/internal/api/server.go`, add to Server struct (after the `workflows` field):

```go
emailClient *email.Client
```

Add import for `"github.com/mathaix/openclawmachines/backend/internal/email"`.

Add setter method:

```go
func (s *Server) SetEmailClient(c *email.Client) {
    s.emailClient = c
}
```

- [ ] **Step 2: Create email client in main.go**

In `backend/cmd/server/main.go`, after `srv.SetWorkflowService(workflowSvc)`:

```go
if cfg.ResendAPIKey != "" {
    srv.SetEmailClient(email.NewClient(cfg.ResendAPIKey))
    slog.Info("email.configured", "provider", "resend")
}
```

Add import for `"github.com/mathaix/openclawmachines/backend/internal/email"`.

- [ ] **Step 3: Run tests**

Run: `make test-go`
Expected: All pass.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/server.go backend/cmd/server/main.go
git commit -m "feat: wire Resend email client into API server"
```

### Task 8: Implement invitation email workflow

**Files:**
- Create: `backend/internal/api/notification_workflow.go`

- [ ] **Step 1: Create the workflow file**

Create `backend/internal/api/notification_workflow.go`:

```go
package api

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/mathaix/openclawmachines/backend/internal/email"
	"github.com/mathaix/openclawmachines/backend/internal/workflows"
)

type invitationEmailInput struct {
	InvitationToken string `json:"invitation_token"`
	RecipientEmail  string `json:"recipient_email"`
	InviterEmail    string `json:"inviter_email"`
	AccountName     string `json:"account_name"`
	AccountID       int    `json:"account_id"`
	Role            string `json:"role"`
	FrontendURL     string `json:"frontend_url"`
	ExpiryDays      int    `json:"expiry_days"`
}

type invitationEmailResult struct {
	Delivered bool   `json:"delivered"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) runInvitationEmailWorkflow(ctx dbos.DBOSContext, input invitationEmailInput) (invitationEmailResult, error) {
	workflowID, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return invitationEmailResult{}, err
	}

	slog.Info("notification.invitation_email.start",
		"workflow_id", workflowID,
		"recipient", input.RecipientEmail,
		"account_id", input.AccountID,
	)

	// Step 1: Render the email
	html, err := dbos.RunAsStep(ctx, func(stepCtx context.Context) (string, error) {
		return email.RenderInvitationEmail(email.InvitationData{
			InviterEmail: input.InviterEmail,
			AccountName:  input.AccountName,
			Role:         input.Role,
			AcceptURL:    fmt.Sprintf("%s/invite/%s", input.FrontendURL, input.InvitationToken),
			ExpiryDays:   input.ExpiryDays,
		})
	}, dbos.WithStepName("notification.render_email"))
	if err != nil {
		return invitationEmailResult{Error: err.Error()}, err
	}

	// Step 2: Deliver via Resend (with retry)
	_, err = dbos.RunAsStep(ctx, func(stepCtx context.Context) (bool, error) {
		if s.emailClient == nil {
			return false, fmt.Errorf("email client not configured")
		}
		return true, s.emailClient.SendEmail(stepCtx, email.Email{
			From:    "OpenClaw Machines <noreply@openclawmachines.com>",
			To:      input.RecipientEmail,
			Subject: fmt.Sprintf("%s invited you to %s", input.InviterEmail, input.AccountName),
			HTML:    html,
		})
	},
		dbos.WithStepName("notification.deliver_email"),
		dbos.WithStepMaxRetries(5),
		dbos.WithBaseInterval(5*time.Second),
		dbos.WithBackoffFactor(3.0),
		dbos.WithMaxInterval(5*time.Minute),
	)
	if err != nil {
		slog.Error("notification.invitation_email.delivery_failed",
			"workflow_id", workflowID,
			"recipient", input.RecipientEmail,
			"error", err,
		)
		return invitationEmailResult{Error: err.Error()}, err
	}

	slog.Info("notification.invitation_email.delivered",
		"workflow_id", workflowID,
		"recipient", input.RecipientEmail,
	)
	return invitationEmailResult{Delivered: true}, nil
}

// enqueueInvitationEmail fires off the invitation email workflow.
// It is best-effort: failure to enqueue does not fail the invitation creation.
func (s *Server) enqueueInvitationEmail(ctx context.Context, input invitationEmailInput) {
	if s.workflows == nil || s.workflows.Context() == nil {
		slog.Debug("notification.skip", "reason", "workflows_not_enabled")
		return
	}
	if s.emailClient == nil {
		slog.Debug("notification.skip", "reason", "email_not_configured")
		return
	}

	workflowID := "notif_" + input.InvitationToken

	// Create projection row
	accountID := input.AccountID
	if _, err := s.workflows.CreateRun(ctx, workflows.CreateRunParams{
		ID:        workflowID,
		Kind:      workflows.KindNotification,
		ScopeType: "account",
		ScopeID:   fmt.Sprintf("%d", accountID),
		AccountID: &accountID,
		Priority:  "normal",
		InputJSON: mustRawJSON(map[string]any{
			"notification_type": "invitation",
			"recipient_email":   input.RecipientEmail,
			"account_name":      input.AccountName,
		}),
	}); err != nil {
		slog.Warn("notification.create_run_failed", "workflow_id", workflowID, "error", err)
		return
	}

	if _, err := dbos.RunWorkflow(
		s.workflows.Context(),
		s.runInvitationEmailWorkflow,
		input,
		dbos.WithWorkflowID(workflowID),
		dbos.WithQueue(workflows.QueueNotifications),
	); err != nil {
		slog.Warn("notification.enqueue_failed", "workflow_id", workflowID, "error", err)
	}
}
```

- [ ] **Step 2: Register the workflow**

In `backend/internal/api/admin_migrate_workflow.go`, update `RegisterWorkflows` (line 84-89) to also register the notification workflow:

```go
func (s *Server) RegisterWorkflows(dbosCtx dbos.DBOSContext) {
	if s == nil {
		return
	}
	dbos.RegisterWorkflow(dbosCtx, s.runMigrationWorkflow, dbos.WithWorkflowName(migrationWorkflowName))
	dbos.RegisterWorkflow(dbosCtx, s.runInvitationEmailWorkflow, dbos.WithWorkflowName("api.invitation_email"))
}
```

- [ ] **Step 3: Run tests**

Run: `make test-go`
Expected: All pass.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/notification_workflow.go backend/internal/api/admin_migrate_workflow.go
git commit -m "feat: implement invitation email workflow with durable retry"
```

### Task 9: Trigger email workflow from handleCreateInvitation

**Files:**
- Modify: `backend/internal/api/invitations.go:73-96`
- Modify: `backend/internal/api/server.go` (need `backendURL` or `frontendURL` access)

- [ ] **Step 1: Add email trigger after invitation creation**

In `backend/internal/api/invitations.go`, after `s.store.CreateInvitation(r.Context(), inv)` succeeds (after line 83), add:

```go
// Enqueue invitation email (best-effort, does not block response)
account, _ := s.store.GetAccount(r.Context(), accountID)
accountName := ""
if account != nil {
    accountName = account.Name
}
s.enqueueInvitationEmail(r.Context(), invitationEmailInput{
    InvitationToken: inv.Token,
    RecipientEmail:  inv.Email,
    InviterEmail:    claims.Email,
    AccountName:     accountName,
    AccountID:       accountID,
    Role:            inv.Role,
    FrontendURL:     s.frontendURL(),
    ExpiryDays:      7,
})
```

- [ ] **Step 2: Add frontendURL helper to server**

In `backend/internal/api/server.go`, add:

```go
func (s *Server) frontendURL() string {
	if s.backendURL != "" {
		// Backend URL is like https://api.openclawmachines.com
		// Frontend URL is https://openclawmachines.com
		// For now, derive or use a config value
		return "https://openclawmachines.com"
	}
	return "http://localhost:5173"
}
```

Note: This is a placeholder. The exact frontend URL should come from config. Add `FrontendURL string` to config if not already present.

- [ ] **Step 3: Run tests**

Run: `make test-go`
Expected: All pass. The enqueue is best-effort and the handler tests don't have workflows enabled.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/invitations.go backend/internal/api/server.go
git commit -m "feat: trigger invitation email workflow on invitation creation"
```

---

## Chunk 4: Migration Retry Policies

### Task 10: Add retry policies to migration workflow steps

**Files:**
- Modify: `backend/internal/api/admin_migrate_workflow.go`

- [ ] **Step 1: Add retry options to read-only steps**

For `migration.prepare` (the validate step), add retry options:

```go
// Change from:
}, dbos.WithStepName("migration.prepare"))

// To:
}, dbos.WithStepName("migration.prepare"),
   dbos.WithStepMaxRetries(3),
   dbos.WithBaseInterval(500*time.Millisecond),
   dbos.WithBackoffFactor(2.0),
)
```

- [ ] **Step 2: Add retry options to idempotent write steps**

For `migration.stop_source`, `migration.release_source`, `migration.stop_for_restore`:

```go
dbos.WithStepMaxRetries(3),
dbos.WithBaseInterval(1*time.Second),
dbos.WithBackoffFactor(2.0),
```

- [ ] **Step 3: Add retry options to backup step**

For `migration.backup_source`:

```go
dbos.WithStepMaxRetries(1),
dbos.WithBaseInterval(2*time.Second),
```

- [ ] **Step 4: Keep destructive steps at zero retries**

`migration.destroy_source_vm` — no retry options (default 0 retries). Already correct.

- [ ] **Step 5: Add import for time if not present**

Verify `"time"` is already imported (it is — used for other purposes).

- [ ] **Step 6: Run tests**

Run: `make test-go`
Expected: All pass.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/api/admin_migrate_workflow.go
git commit -m "feat: add step-level retry policies to migration workflow"
```

---

## Chunk 5: Update Docs and Deploy Config

### Task 11: Update CurrentFeature.md

**Files:**
- Modify: `docs/CurrentFeature.md`

- [ ] **Step 1: Check off completed Phase 2 email items**

Update the Phase 2 email delivery checklist to reflect what's been implemented.

- [ ] **Step 2: Commit**

```bash
git add docs/CurrentFeature.md
git commit -m "docs: update CurrentFeature with Phase 1b email workflow progress"
```

### Task 12: Update deploy script for new env vars

**Files:**
- Modify: `scripts/deploy-cloud-run.sh`

- [ ] **Step 1: Add RUN_MODE and RESEND_API_KEY**

In `scripts/deploy-cloud-run.sh`, add to the `--set-env-vars` line for backend:

```
RUN_MODE=api
```

Add to the `--set-secrets` line:

```
RESEND_API_KEY=RESEND_API_KEY:latest
```

Note: The `RESEND_API_KEY` secret must be created in GCP Secret Manager first:
```bash
echo -n "re_YOUR_KEY_HERE" | gcloud secrets create RESEND_API_KEY --data-file=-
```

- [ ] **Step 2: Commit**

```bash
git add scripts/deploy-cloud-run.sh
git commit -m "feat: add RUN_MODE and RESEND_API_KEY to Cloud Run deploy config"
```

---

## Verification

After all tasks are complete:

1. `make test-go` — all backend tests pass
2. `RUN_MODE="" go run ./backend/cmd/server/...` — backward compat, both API and worker
3. `RUN_MODE=api go run ./backend/cmd/server/...` — API only, no DBOS executor
4. `RUN_MODE=worker go run ./backend/cmd/server/...` — worker only, no HTTP server
5. Create an invitation via API — email workflow should be enqueued
6. Check `workflow_runs` table — notification workflow row exists

---

## Out of Scope (Future Tasks)

These are documented in the design but not part of this plan:

- GCE managed instance group setup (infrastructure, not code)
- Preemption signal handler (only needed when deploying to spot instances)
- Workflow dashboard frontend
- Account-scoped workflow list endpoint
- Stall detection / monitoring
- Resend account setup + DNS domain verification
