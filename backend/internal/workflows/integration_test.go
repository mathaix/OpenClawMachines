//go:build integration_db

package workflows_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mathaix/openclawmachines/backend/internal/api"
	"github.com/mathaix/openclawmachines/backend/internal/email"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/internal/workflows"
)

// ---------------------------------------------------------------------------
// Shared harness
// ---------------------------------------------------------------------------

var (
	testDB    *store.PostgresStore
	testPool  *pgxpool.Pool
	testDBURL string
)

func TestMain(m *testing.M) {
	testDBURL = os.Getenv("TEST_DATABASE_URL")
	if testDBURL == "" {
		fmt.Println("SKIP: TEST_DATABASE_URL not set")
		os.Exit(0)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, testDBURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	testPool = pool

	if err := runMinimalMigrations(ctx, pool); err != nil {
		fmt.Fprintf(os.Stderr, "migrations: %v\n", err)
		os.Exit(1)
	}

	db, err := store.NewPostgresStore(ctx, testDBURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "store: %v\n", err)
		os.Exit(1)
	}
	testDB = db

	code := m.Run()

	db.Close()
	pool.Close()
	os.Exit(code)
}

// runMinimalMigrations creates only the tables required by workflow tests.
// The workflow_runs table has FK references to users and accounts, so we
// create those first. IF NOT EXISTS keeps re-runs idempotent.
func runMinimalMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id              SERIAL PRIMARY KEY,
			email           TEXT NOT NULL UNIQUE,
			name            TEXT NOT NULL,
			avatar_url      TEXT,
			auth_provider   TEXT NOT NULL DEFAULT 'test',
			auth_provider_id TEXT NOT NULL DEFAULT '',
			cf_sub          TEXT DEFAULT '',
			status          TEXT NOT NULL DEFAULT 'active',
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS accounts (
			id              SERIAL PRIMARY KEY,
			name            TEXT NOT NULL,
			slug            TEXT NOT NULL UNIQUE,
			created_by      INT NOT NULL REFERENCES users(id),
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS workflow_runs (
			id              TEXT PRIMARY KEY,
			kind            TEXT NOT NULL,
			scope_type      TEXT NOT NULL,
			scope_id        TEXT NOT NULL,
			status          TEXT NOT NULL CHECK (status IN ('queued','running','waiting','completed','failed','cancelled','manual_action_required')),
			current_phase   TEXT,
			requested_by    INT REFERENCES users(id) ON DELETE SET NULL,
			account_id      INT REFERENCES accounts(id) ON DELETE CASCADE,
			priority        TEXT NOT NULL DEFAULT 'normal',
			input_json      JSONB NOT NULL DEFAULT '{}'::jsonb,
			output_json     JSONB,
			summary_json    JSONB,
			error_code      TEXT,
			error_message   TEXT,
			started_at      TIMESTAMPTZ,
			completed_at    TIMESTAMPTZ,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS workflow_events (
			id              BIGSERIAL PRIMARY KEY,
			workflow_id     TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
			phase           TEXT,
			level           TEXT NOT NULL CHECK (level IN ('info','warn','error')),
			event_type      TEXT NOT NULL,
			message         TEXT NOT NULL,
			details_json    JSONB,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS workflow_locks (
			resource_type      TEXT NOT NULL,
			resource_id        TEXT NOT NULL,
			lock_kind          TEXT NOT NULL,
			workflow_id        TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
			lease_expires_at   TIMESTAMPTZ NOT NULL,
			created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (resource_type, resource_id, lock_kind)
		)`,
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("exec %q: %w", s[:60], err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Per-test environment
// ---------------------------------------------------------------------------

type testEnv struct {
	t      *testing.T
	ctx    context.Context
	cancel context.CancelFunc
	svc    *workflows.Service
	srv    *api.Server
}

// newTestEnv creates a workflow Service (runtime + enqueue enabled) and an
// api.Server wired together. Pass a non-nil emailClient to enable email
// delivery, or nil for "email not configured" tests.
func newTestEnv(t *testing.T, emailClient *email.Client) *testEnv {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	svc, err := workflows.NewService(ctx, testDB, workflows.Config{
		AppName:       fmt.Sprintf("wftest-%d", time.Now().UnixNano()),
		DatabaseURL:   testDBURL,
		EnableRuntime: true,
		EnableEnqueue: true,
	})
	if err != nil {
		cancel()
		t.Fatalf("NewService: %v", err)
	}

	srv := api.NewWorkerServer(testDB, nil, nil, nil, nil, nil, "")

	// Register the test-export wrapper (same DBOS workflow name as production).
	svc.RegisterWorkflows(srv.RegisterWorkflowsForTest)
	srv.SetWorkflowService(svc)

	if emailClient != nil {
		srv.SetEmailClient(emailClient)
	}
	srv.SetFrontendURL("http://test.example.com")

	return &testEnv{
		t:      t,
		ctx:    ctx,
		cancel: cancel,
		svc:    svc,
		srv:    srv,
	}
}

func (e *testEnv) close() {
	e.svc.Close(5 * time.Second)
	e.cancel()
}

// ensureTestAccount creates a user + account for FK constraints.
func ensureTestAccount(t *testing.T, ctx context.Context) (userID int, accountID int) {
	t.Helper()
	slug := fmt.Sprintf("wftest-%d", time.Now().UnixNano())

	user := &store.User{
		Email:          slug + "@test.local",
		Name:           "Workflow Test User",
		AuthProvider:   "test",
		AuthProviderID: slug,
		Status:         "active",
	}
	if err := testDB.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	account := &store.Account{
		Name:      "Workflow Test Account",
		Slug:      slug,
		CreatedBy: user.ID,
	}
	if err := testDB.CreateAccount(ctx, account); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	return user.ID, account.ID
}

// ---------------------------------------------------------------------------
// Mock email server helpers
// ---------------------------------------------------------------------------

type emailCall struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

// newMockEmailServer returns an httptest.Server that records email sends.
// handler receives the parsed email and the 1-based call number, returning
// (httpStatusCode, responseBody).
func newMockEmailServer(handler func(call emailCall, callNum int) (int, string)) (*httptest.Server, *[]emailCall) {
	var (
		calls   []emailCall
		callsMu sync.Mutex
		callNum int32
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emails" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		var call emailCall
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		n := int(atomic.AddInt32(&callNum, 1))
		callsMu.Lock()
		calls = append(calls, call)
		callsMu.Unlock()

		code, body := handler(call, n)
		w.WriteHeader(code)
		w.Write([]byte(body))
	}))

	return ts, &calls
}

func newSuccessEmailServer() (*httptest.Server, *[]emailCall) {
	return newMockEmailServer(func(_ emailCall, _ int) (int, string) {
		return http.StatusOK, `{"id":"test-email-id"}`
	})
}

// ---------------------------------------------------------------------------
// NOTIFICATION WORKFLOW TESTS
// ---------------------------------------------------------------------------

func TestNotificationWorkflow_HappyPath(t *testing.T) {
	ts, calls := newSuccessEmailServer()
	defer ts.Close()

	emailClient := email.NewClient("test-api-key", email.WithBaseURL(ts.URL))
	env := newTestEnv(t, emailClient)
	defer env.close()

	_, accountID := ensureTestAccount(t, env.ctx)

	workflowID, err := workflows.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	// Create projection row.
	_, err = env.svc.CreateRun(env.ctx, workflows.CreateRunParams{
		ID:        workflowID,
		Kind:      workflows.KindNotification,
		ScopeType: "account",
		ScopeID:   fmt.Sprintf("%d", accountID),
		AccountID: &accountID,
		Priority:  "normal",
		InputJSON: mustJSON(map[string]any{
			"notification_type": "invitation",
			"recipient_email":   "invited@test.local",
			"account_name":      "Test Account",
		}),
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Enqueue via DBOS.
	handle, err := dbos.RunWorkflow(
		env.svc.Context(),
		env.srv.RunInvitationEmailWorkflowForTest,
		api.InvitationEmailInput{
			InvitationToken: "test-token-123",
			RecipientEmail:  "invited@test.local",
			InviterEmail:    "admin@test.local",
			AccountName:     "Test Account",
			AccountID:       accountID,
			Role:            "member",
			FrontendURL:     "http://test.example.com",
			ExpiryDays:      7,
		},
		dbos.WithWorkflowID(workflowID),
		dbos.WithQueue(workflows.QueueNotifications),
	)
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}

	result, err := handle.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}

	if !result.Delivered {
		t.Errorf("expected delivered=true, got false; error=%q", result.Error)
	}

	// Verify the mock email server was called once with the right recipient.
	if len(*calls) != 1 {
		t.Fatalf("expected 1 email call, got %d", len(*calls))
	}
	call := (*calls)[0]
	if call.To != "invited@test.local" {
		t.Errorf("expected To=invited@test.local, got %q", call.To)
	}
	if call.Subject == "" {
		t.Error("expected non-empty subject")
	}

	// Verify projection row exists.
	run, err := env.svc.GetRun(env.ctx, workflowID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Kind != workflows.KindNotification {
		t.Errorf("expected kind=%s, got %s", workflows.KindNotification, run.Kind)
	}
}

func TestNotificationWorkflow_TransientRetry(t *testing.T) {
	// First call returns 429 (retryable), subsequent calls succeed.
	ts, calls := newMockEmailServer(func(_ emailCall, callNum int) (int, string) {
		if callNum == 1 {
			return http.StatusTooManyRequests, `{"error":"rate limited"}`
		}
		return http.StatusOK, `{"id":"test-email-id"}`
	})
	defer ts.Close()

	emailClient := email.NewClient("test-api-key", email.WithBaseURL(ts.URL))
	env := newTestEnv(t, emailClient)
	defer env.close()

	_, accountID := ensureTestAccount(t, env.ctx)

	workflowID, err := workflows.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	_, err = env.svc.CreateRun(env.ctx, workflows.CreateRunParams{
		ID:        workflowID,
		Kind:      workflows.KindNotification,
		ScopeType: "account",
		ScopeID:   fmt.Sprintf("%d", accountID),
		AccountID: &accountID,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	handle, err := dbos.RunWorkflow(
		env.svc.Context(),
		env.srv.RunInvitationEmailWorkflowForTest,
		api.InvitationEmailInput{
			InvitationToken: "test-token-retry",
			RecipientEmail:  "retry@test.local",
			InviterEmail:    "admin@test.local",
			AccountName:     "Retry Account",
			AccountID:       accountID,
			Role:            "member",
			FrontendURL:     "http://test.example.com",
			ExpiryDays:      7,
		},
		dbos.WithWorkflowID(workflowID),
		dbos.WithQueue(workflows.QueueNotifications),
	)
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}

	result, err := handle.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if !result.Delivered {
		t.Errorf("expected delivered=true after retry, got false; error=%q", result.Error)
	}

	// The 429 on the first attempt triggers a retry, so at least 2 calls.
	if len(*calls) < 2 {
		t.Errorf("expected >= 2 email calls (retry), got %d", len(*calls))
	}
}

func TestNotificationWorkflow_PermanentFailure(t *testing.T) {
	// All calls return 400 (non-retryable).
	ts, _ := newMockEmailServer(func(_ emailCall, _ int) (int, string) {
		return http.StatusBadRequest, `{"error":"bad request"}`
	})
	defer ts.Close()

	emailClient := email.NewClient("test-api-key", email.WithBaseURL(ts.URL))
	env := newTestEnv(t, emailClient)
	defer env.close()

	_, accountID := ensureTestAccount(t, env.ctx)

	workflowID, err := workflows.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	_, err = env.svc.CreateRun(env.ctx, workflows.CreateRunParams{
		ID:        workflowID,
		Kind:      workflows.KindNotification,
		ScopeType: "account",
		ScopeID:   fmt.Sprintf("%d", accountID),
		AccountID: &accountID,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	handle, err := dbos.RunWorkflow(
		env.svc.Context(),
		env.srv.RunInvitationEmailWorkflowForTest,
		api.InvitationEmailInput{
			InvitationToken: "test-token-fail",
			RecipientEmail:  "fail@test.local",
			InviterEmail:    "admin@test.local",
			AccountName:     "Fail Account",
			AccountID:       accountID,
			Role:            "member",
			FrontendURL:     "http://test.example.com",
			ExpiryDays:      7,
		},
		dbos.WithWorkflowID(workflowID),
		dbos.WithQueue(workflows.QueueNotifications),
	)
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}

	// Permanent failure: workflow should return an error or a result with Error set.
	result, err := handle.GetResult()
	if err == nil && result.Delivered {
		t.Fatal("expected workflow to fail on permanent 400 error, but it reported delivered")
	}
	// Accept either an error from GetResult or a non-delivered result with an error message.
	if err == nil && result.Error == "" {
		t.Error("expected either GetResult error or result.Error for permanent failure")
	}
}

func TestNotificationWorkflow_NoEmailClient(t *testing.T) {
	// Server has nil email client -- workflow should skip delivery gracefully.
	env := newTestEnv(t, nil)
	defer env.close()

	_, accountID := ensureTestAccount(t, env.ctx)

	workflowID, err := workflows.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}

	_, err = env.svc.CreateRun(env.ctx, workflows.CreateRunParams{
		ID:        workflowID,
		Kind:      workflows.KindNotification,
		ScopeType: "account",
		ScopeID:   fmt.Sprintf("%d", accountID),
		AccountID: &accountID,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	handle, err := dbos.RunWorkflow(
		env.svc.Context(),
		env.srv.RunInvitationEmailWorkflowForTest,
		api.InvitationEmailInput{
			InvitationToken: "test-token-no-email",
			RecipientEmail:  "noemail@test.local",
			InviterEmail:    "admin@test.local",
			AccountName:     "NoEmail Account",
			AccountID:       accountID,
			Role:            "member",
			FrontendURL:     "http://test.example.com",
			ExpiryDays:      7,
		},
		dbos.WithWorkflowID(workflowID),
		dbos.WithQueue(workflows.QueueNotifications),
	)
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}

	result, err := handle.GetResult()
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if result.Delivered {
		t.Error("expected delivered=false when email client is nil")
	}
	if result.Error == "" {
		t.Error("expected result.Error to mention email client not configured")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
