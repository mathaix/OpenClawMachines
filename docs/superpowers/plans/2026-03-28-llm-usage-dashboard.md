# LLM Usage Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the zero-count usage display by switching read queries to `token_usage`, add time-bucketed aggregation, and build a session snapshot poller for future agent-level attribution.

**Architecture:** The proxy already writes per-request data to `token_usage` (437 records). Fix the read queries to use this table instead of the empty `llm_usage`. Add a `/usage/breakdown` endpoint with `GROUP BY date_trunc()` for hourly/daily aggregation. Start a background poller that calls the gateway's `sessions.list` via WebSocket to collect per-session token snapshots.

**Tech Stack:** Go 1.25 (backend), PostgreSQL (Neon), gorilla/websocket, React 18 + TypeScript (frontend), Tailwind CSS

---

## File Structure

| File | Responsibility |
|------|---------------|
| `backend/migrations/047_usage_dashboard.sql` | Create `session_snapshots` table, add `category` columns to `token_usage` |
| `backend/internal/store/store.go` | Add `UsageBucket`, `SessionSnapshot` types; add `GetUsageBreakdown`, `InsertSessionSnapshot` interface methods |
| `backend/internal/store/postgres.go` | Fix `GetLLMSpendByMachine`/`GetLLMUsageByMachine` to read `token_usage`; implement new query methods |
| `backend/internal/api/billing.go` | Add `handleGetMachineUsageBreakdown` handler |
| `backend/internal/api/server.go` | Register new route |
| `backend/internal/usage/poller.go` | `SessionPoller` — background goroutine that polls gateway sessions |
| `backend/internal/usage/poller_test.go` | Unit tests for session key parsing and poller logic |
| `backend/cmd/server/main.go` | Wire and start `SessionPoller` |
| `frontend/src/lib/types.ts` | Add `UsageBreakdown`, `UsageBucketEntry` types |
| `frontend/src/lib/api.ts` | Add `getMachineUsageBreakdown` function |
| `frontend/src/pages/MachineView.tsx` | Add "usage" tab to TabId and TABS |
| `frontend/src/pages/machine-tabs/UsageTab.tsx` | New usage tab with summary, hourly/daily breakdown tables |
| `frontend/src/pages/machine-tabs/OverviewTab.tsx` | Fix summary cards to use working data |

---

### Task 1: Database migration

**Files:**
- Create: `backend/migrations/047_usage_dashboard.sql`

- [ ] **Step 1: Write the migration**

```sql
-- 047_usage_dashboard.sql

-- Session snapshots from gateway polling
CREATE TABLE session_snapshots (
  id BIGSERIAL PRIMARY KEY,
  machine_id TEXT NOT NULL,
  session_key TEXT NOT NULL,
  agent_id TEXT NOT NULL DEFAULT 'unknown',
  channel TEXT NOT NULL DEFAULT 'chat',
  input_tokens INT NOT NULL DEFAULT 0,
  output_tokens INT NOT NULL DEFAULT 0,
  total_tokens INT NOT NULL DEFAULT 0,
  polled_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_session_snapshots_machine ON session_snapshots(machine_id, polled_at);
CREATE INDEX idx_session_snapshots_agent ON session_snapshots(machine_id, agent_id, polled_at);

-- Future-ready category columns on token_usage
ALTER TABLE token_usage ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE token_usage ADD COLUMN IF NOT EXISTS category_detail TEXT;
```

- [ ] **Step 2: Run the migration**

Run: `make deploy-backend` (runs migrations automatically) or manually:
```bash
cd backend && go run cmd/migrate/main.go
```

- [ ] **Step 3: Verify tables exist**

```bash
DB_URL=$(gcloud secrets versions access latest --secret=OCM_DATABASE_URL --project=clarateach)
psql "$DB_URL" -c "\d session_snapshots"
psql "$DB_URL" -c "\d token_usage" | grep category
```

Expected: `session_snapshots` table with all columns; `category` and `category_detail` columns on `token_usage`.

- [ ] **Step 4: Commit**

```bash
git add backend/migrations/047_usage_dashboard.sql
git commit -m "feat: add session_snapshots table and category columns on token_usage"
```

---

### Task 2: Fix read queries to use `token_usage`

**Files:**
- Modify: `backend/internal/store/store.go:152-163` (LLMUsage struct)
- Modify: `backend/internal/store/postgres.go:1041-1110` (GetLLMSpend*, GetLLMUsage* functions)

- [ ] **Step 1: Write the failing test**

Create a test that verifies `GetLLMSpendByMachine` returns non-zero when `token_usage` has records. This is an integration-level query, but we can verify the SQL logic is correct by checking the function is called and the struct fields match.

Since these are raw SQL queries against a real DB, we verify by reading the current code and confirming it references `token_usage`.

- [ ] **Step 2: Update `LLMUsage` struct in `store.go`**

Replace the `LLMUsage` struct at `backend/internal/store/store.go:152-163` with one that matches `token_usage` columns:

```go
type LLMUsage struct {
	ID            int64     `json:"id"`
	AccountID     int       `json:"account_id"`
	MachineID     string    `json:"machine_id"`
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	InputTokens   int       `json:"input_tokens"`
	OutputTokens  int       `json:"output_tokens"`
	CostMicrocents int64    `json:"cost_microcents"`
	Source        string    `json:"source"`
	CreatedAt     time.Time `json:"created_at"`
}
```

Note: `RequestID` removed (was always NULL), `Source` added. The `CostMicrocents` field is now computed from `cost_input_usd` and `cost_output_usd` in the SQL query.

- [ ] **Step 3: Update `GetLLMSpendByMachine` in `postgres.go`**

Replace the function at `backend/internal/store/postgres.go:1051-1061`:

```go
func (s *PostgresStore) GetLLMSpendByMachine(ctx context.Context, machineID string) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(
			(input_tokens * COALESCE(cost_input_usd, 0) + output_tokens * COALESCE(cost_output_usd, 0)) * 100000000
		)::bigint, 0)
		 FROM token_usage
		 WHERE machine_id = $1
		   AND created_at >= date_trunc('month', now())`,
		machineID,
	).Scan(&total)
	return total, err
}
```

- [ ] **Step 4: Update `GetLLMUsageByMachine` in `postgres.go`**

Replace the function at `backend/internal/store/postgres.go:1063-1086`:

```go
func (s *PostgresStore) GetLLMUsageByMachine(ctx context.Context, machineID string, since time.Time, limit int) ([]LLMUsage, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, account_id, machine_id, provider, model, input_tokens, output_tokens,
		        (input_tokens * COALESCE(cost_input_usd, 0) + output_tokens * COALESCE(cost_output_usd, 0)) * 100000000 AS cost_microcents,
		        source, created_at
		 FROM token_usage
		 WHERE machine_id = $1 AND created_at >= $2
		 ORDER BY created_at DESC
		 LIMIT $3`,
		machineID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var usages []LLMUsage
	for rows.Next() {
		var u LLMUsage
		if err := rows.Scan(&u.ID, &u.AccountID, &u.MachineID, &u.Provider, &u.Model,
			&u.InputTokens, &u.OutputTokens, &u.CostMicrocents, &u.Source, &u.CreatedAt); err != nil {
			return nil, err
		}
		usages = append(usages, u)
	}
	return usages, rows.Err()
}
```

- [ ] **Step 5: Update `GetLLMSpendByAccount` in `postgres.go`**

Replace the function at `backend/internal/store/postgres.go:1043-1049`:

```go
func (s *PostgresStore) GetLLMSpendByAccount(ctx context.Context, accountID int) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(
			(input_tokens * COALESCE(cost_input_usd, 0) + output_tokens * COALESCE(cost_output_usd, 0)) * 100000000
		)::bigint, 0)
		 FROM token_usage
		 WHERE account_id = $1`,
		accountID,
	).Scan(&total)
	return total, err
}
```

- [ ] **Step 6: Update `GetLLMUsageByAccount` in `postgres.go`**

Replace the function at `backend/internal/store/postgres.go:1088-1110`:

```go
func (s *PostgresStore) GetLLMUsageByAccount(ctx context.Context, accountID int, since time.Time, limit int) ([]LLMUsage, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, account_id, machine_id, provider, model, input_tokens, output_tokens,
		        (input_tokens * COALESCE(cost_input_usd, 0) + output_tokens * COALESCE(cost_output_usd, 0)) * 100000000 AS cost_microcents,
		        source, created_at
		 FROM token_usage
		 WHERE account_id = $1 AND created_at >= $2
		 ORDER BY created_at DESC
		 LIMIT $3`,
		accountID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var usages []LLMUsage
	for rows.Next() {
		var u LLMUsage
		if err := rows.Scan(&u.ID, &u.AccountID, &u.MachineID, &u.Provider, &u.Model,
			&u.InputTokens, &u.OutputTokens, &u.CostMicrocents, &u.Source, &u.CreatedAt); err != nil {
			return nil, err
		}
		usages = append(usages, u)
	}
	return usages, rows.Err()
}
```

- [ ] **Step 7: Update `handleGetMachineUsage` response in `billing.go`**

The handler at `backend/internal/api/billing.go:151-204` already returns `records` as `[]store.LLMUsage`. The struct change (adding `Source`, removing `RequestID`) will automatically flow through to the JSON response. No handler changes needed — just verify the response shape is compatible with the frontend type.

- [ ] **Step 8: Run tests**

```bash
make test-go
```

Expected: All tests pass. The `LLMUsage` struct change may break any test that references `RequestID` — fix by removing those fields from test assertions.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/store/store.go backend/internal/store/postgres.go
git commit -m "fix: switch usage read queries from llm_usage to token_usage"
```

---

### Task 3: Add usage breakdown aggregation endpoint

**Files:**
- Modify: `backend/internal/store/store.go` (add types + interface method)
- Modify: `backend/internal/store/postgres.go` (add query)
- Modify: `backend/internal/api/billing.go` (add handler)
- Modify: `backend/internal/api/server.go:399` (add route)

- [ ] **Step 1: Add types to `store.go`**

Add after the `LLMUsage` struct in `backend/internal/store/store.go`:

```go
type UsageBucketEntry struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Source        string `json:"source"`
	InputTokens   int    `json:"input_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	CostMicrocents int64 `json:"cost_microcents"`
	RequestCount  int    `json:"request_count"`
}

type UsageBucket struct {
	Timestamp time.Time          `json:"timestamp"`
	Entries   []UsageBucketEntry `json:"entries"`
}
```

- [ ] **Step 2: Add interface method to `store.go`**

Add to the store interface (near the existing `GetLLMUsageByMachine` at line 622):

```go
GetUsageBreakdown(ctx context.Context, machineID string, period string, since time.Time) ([]UsageBucket, error)
```

- [ ] **Step 3: Implement `GetUsageBreakdown` in `postgres.go`**

Add after the `GetLLMUsageByAccount` function:

```go
func (s *PostgresStore) GetUsageBreakdown(ctx context.Context, machineID string, period string, since time.Time) ([]store.UsageBucket, error) {
	// Validate period to prevent SQL injection (date_trunc accepts specific values)
	switch period {
	case "hour", "day":
	default:
		return nil, fmt.Errorf("invalid period: %s (must be 'hour' or 'day')", period)
	}

	rows, err := s.pool.Query(ctx,
		fmt.Sprintf(`SELECT
			date_trunc('%s', created_at) AS bucket,
			provider,
			model,
			source,
			SUM(input_tokens)::int AS input_tokens,
			SUM(output_tokens)::int AS output_tokens,
			COALESCE(SUM(
				(input_tokens * COALESCE(cost_input_usd, 0) + output_tokens * COALESCE(cost_output_usd, 0)) * 100000000
			)::bigint, 0) AS cost_microcents,
			COUNT(*)::int AS request_count
		FROM token_usage
		WHERE machine_id = $1 AND created_at >= $2
		GROUP BY bucket, provider, model, source
		ORDER BY bucket, provider, model`, period),
		machineID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bucketMap := make(map[time.Time]*store.UsageBucket)
	var bucketOrder []time.Time

	for rows.Next() {
		var ts time.Time
		var e store.UsageBucketEntry
		if err := rows.Scan(&ts, &e.Provider, &e.Model, &e.Source,
			&e.InputTokens, &e.OutputTokens, &e.CostMicrocents, &e.RequestCount); err != nil {
			return nil, err
		}
		b, exists := bucketMap[ts]
		if !exists {
			b = &store.UsageBucket{Timestamp: ts}
			bucketMap[ts] = b
			bucketOrder = append(bucketOrder, ts)
		}
		b.Entries = append(b.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	buckets := make([]store.UsageBucket, len(bucketOrder))
	for i, ts := range bucketOrder {
		buckets[i] = *bucketMap[ts]
	}
	return buckets, nil
}
```

- [ ] **Step 4: Add handler in `billing.go`**

Add after `handleGetMachineUsage` in `backend/internal/api/billing.go`:

```go
func (s *Server) handleGetMachineUsageBreakdown(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	period := r.URL.Query().Get("period")
	if period == "" {
		period = "hour"
	}
	if period != "hour" && period != "day" {
		writeError(w, http.StatusBadRequest, "period must be 'hour' or 'day'")
		return
	}

	// Default since: start of today for hourly, start of month for daily
	var since time.Time
	if sinceParam := r.URL.Query().Get("since"); sinceParam != "" {
		parsed, err := time.Parse(time.RFC3339, sinceParam)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since parameter: use RFC3339 format")
			return
		}
		since = parsed
	} else {
		now := time.Now().UTC()
		if period == "hour" {
			since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		} else {
			since = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		}
	}

	buckets, err := s.store.GetUsageBreakdown(r.Context(), machineID, period, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get usage breakdown")
		return
	}
	if buckets == nil {
		buckets = []store.UsageBucket{}
	}

	// Compute totals
	var totalIn, totalOut int
	var totalCost int64
	var totalReqs int
	for _, b := range buckets {
		for _, e := range b.Entries {
			totalIn += e.InputTokens
			totalOut += e.OutputTokens
			totalCost += e.CostMicrocents
			totalReqs += e.RequestCount
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"period": period,
		"since":  since.Format(time.RFC3339),
		"buckets": buckets,
		"totals": map[string]interface{}{
			"input_tokens":   totalIn,
			"output_tokens":  totalOut,
			"cost_microcents": totalCost,
			"request_count":  totalReqs,
		},
	})
}
```

- [ ] **Step 5: Register route in `server.go`**

In `backend/internal/api/server.go`, find line 399 where `r.Get("/usage", srv.handleGetMachineUsage)` is registered. Add below it:

```go
r.Get("/usage/breakdown", srv.handleGetMachineUsageBreakdown)
```

- [ ] **Step 6: Run tests**

```bash
make test-go
```

Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/store/store.go backend/internal/store/postgres.go backend/internal/api/billing.go backend/internal/api/server.go
git commit -m "feat: add /usage/breakdown endpoint with hourly/daily aggregation"
```

---

### Task 4: Session snapshot poller

**Files:**
- Create: `backend/internal/usage/poller.go`
- Create: `backend/internal/usage/poller_test.go`
- Modify: `backend/internal/store/store.go` (add interface methods)
- Modify: `backend/internal/store/postgres.go` (add implementations)

- [ ] **Step 1: Add store types and interface methods**

Add to `backend/internal/store/store.go` (types section):

```go
type SessionSnapshot struct {
	ID           int64     `json:"id"`
	MachineID    string    `json:"machine_id"`
	SessionKey   string    `json:"session_key"`
	AgentID      string    `json:"agent_id"`
	Channel      string    `json:"channel"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	TotalTokens  int       `json:"total_tokens"`
	PolledAt     time.Time `json:"polled_at"`
}
```

Add to the store interface:

```go
InsertSessionSnapshot(ctx context.Context, snap *SessionSnapshot) error
```

- [ ] **Step 2: Implement `InsertSessionSnapshot` in `postgres.go`**

Add to `backend/internal/store/postgres.go`:

```go
func (s *PostgresStore) InsertSessionSnapshot(ctx context.Context, snap *SessionSnapshot) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO session_snapshots (machine_id, session_key, agent_id, channel, input_tokens, output_tokens, total_tokens, polled_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		snap.MachineID, snap.SessionKey, snap.AgentID, snap.Channel,
		snap.InputTokens, snap.OutputTokens, snap.TotalTokens, snap.PolledAt)
	return err
}
```

- [ ] **Step 3: Write the poller test**

Create `backend/internal/usage/poller_test.go`:

```go
package usage

import (
	"testing"
)

func TestParseSessionKey(t *testing.T) {
	tests := []struct {
		name       string
		sessionKey string
		wantAgent  string
		wantChannel string
	}{
		{
			name:        "direct chat",
			sessionKey:  "agent:default:main",
			wantAgent:   "default",
			wantChannel: "chat",
		},
		{
			name:        "named agent",
			sessionKey:  "agent:coder:main",
			wantAgent:   "coder",
			wantChannel: "chat",
		},
		{
			name:        "telegram group",
			sessionKey:  "agent:default:telegram:group:123",
			wantAgent:   "default",
			wantChannel: "telegram",
		},
		{
			name:        "discord channel",
			sessionKey:  "agent:helper:discord:group:456",
			wantAgent:   "helper",
			wantChannel: "discord",
		},
		{
			name:        "cron job",
			sessionKey:  "cron:daily-report",
			wantAgent:   "cron",
			wantChannel: "cron",
		},
		{
			name:        "webhook",
			sessionKey:  "hook:abc-123",
			wantAgent:   "hook",
			wantChannel: "hook",
		},
		{
			name:        "unknown format",
			sessionKey:  "something-else",
			wantAgent:   "unknown",
			wantChannel: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, channel := ParseSessionKey(tt.sessionKey)
			if agent != tt.wantAgent {
				t.Errorf("agent = %q, want %q", agent, tt.wantAgent)
			}
			if channel != tt.wantChannel {
				t.Errorf("channel = %q, want %q", channel, tt.wantChannel)
			}
		})
	}
}

func TestParseSessionsResponse(t *testing.T) {
	// Gateway response for sessions.list
	raw := `{
		"type": "res",
		"id": "sess-1",
		"ok": true,
		"payload": {
			"agent:default:main": {
				"sessionId": "abc",
				"updatedAt": 1774673432387,
				"inputTokens": 1200,
				"outputTokens": 4500,
				"totalTokens": 5700
			},
			"agent:coder:telegram:group:123": {
				"sessionId": "def",
				"updatedAt": 1774673432387,
				"inputTokens": 800,
				"outputTokens": 2100,
				"totalTokens": 2900
			}
		}
	}`

	sessions, err := ParseSessionsResponse([]byte(raw))
	if err != nil {
		t.Fatalf("ParseSessionsResponse: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}

	// Find the default agent session
	var found bool
	for _, s := range sessions {
		if s.SessionKey == "agent:default:main" {
			found = true
			if s.AgentID != "default" {
				t.Errorf("agent = %q, want %q", s.AgentID, "default")
			}
			if s.Channel != "chat" {
				t.Errorf("channel = %q, want %q", s.Channel, "chat")
			}
			if s.InputTokens != 1200 {
				t.Errorf("input_tokens = %d, want 1200", s.InputTokens)
			}
			if s.OutputTokens != 4500 {
				t.Errorf("output_tokens = %d, want 4500", s.OutputTokens)
			}
		}
	}
	if !found {
		t.Error("agent:default:main session not found")
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

```bash
cd backend && go test ./internal/usage/... -v
```

Expected: FAIL — package does not exist yet.

- [ ] **Step 5: Write the poller implementation**

Create `backend/internal/usage/poller.go`:

```go
package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// SessionData represents a parsed session from the gateway.
type SessionData struct {
	SessionKey   string
	AgentID      string
	Channel      string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// MachineInfo provides the data the poller needs to connect to a machine's gateway.
type MachineInfo struct {
	MachineID    string
	HostIP       string
	GatewayToken string
}

// SnapshotStore is the subset of the database needed by the poller.
type SnapshotStore interface {
	InsertSessionSnapshot(ctx context.Context, snap *SessionSnapshot) error
}

// SessionSnapshot mirrors the database row.
type SessionSnapshot struct {
	MachineID    string
	SessionKey   string
	AgentID      string
	Channel      string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	PolledAt     time.Time
}

// MachineResolver returns the list of running machines with their gateway connection info.
type MachineResolver interface {
	ListRunningMachinesForPolling(ctx context.Context) ([]MachineInfo, error)
}

// SessionPoller polls gateway sessions on a timer and stores snapshots.
type SessionPoller struct {
	resolver MachineResolver
	store    SnapshotStore
}

// NewSessionPoller creates a new poller.
func NewSessionPoller(resolver MachineResolver, store SnapshotStore) *SessionPoller {
	return &SessionPoller{resolver: resolver, store: store}
}

// Start runs the poller loop until ctx is cancelled.
func (p *SessionPoller) Start(ctx context.Context, interval time.Duration) {
	slog.Info("session_poller.started", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("session_poller.stopped")
			return
		case <-ticker.C:
			p.PollOnce(ctx)
		}
	}
}

// PollOnce polls all running machines for session data.
func (p *SessionPoller) PollOnce(ctx context.Context) {
	machines, err := p.resolver.ListRunningMachinesForPolling(ctx)
	if err != nil {
		slog.Error("session_poller.list_machines_failed", "error", err)
		return
	}

	now := time.Now().UTC()
	var totalSnapshots int

	for _, m := range machines {
		sessions, err := pollGatewaySessions(ctx, m)
		if err != nil {
			slog.Warn("session_poller.poll_failed", "machine_id", m.MachineID, "error", err)
			continue
		}

		for _, s := range sessions {
			snap := &SessionSnapshot{
				MachineID:    m.MachineID,
				SessionKey:   s.SessionKey,
				AgentID:      s.AgentID,
				Channel:      s.Channel,
				InputTokens:  s.InputTokens,
				OutputTokens: s.OutputTokens,
				TotalTokens:  s.TotalTokens,
				PolledAt:     now,
			}
			if err := p.store.InsertSessionSnapshot(ctx, snap); err != nil {
				slog.Error("session_poller.insert_failed", "machine_id", m.MachineID, "session_key", s.SessionKey, "error", err)
			} else {
				totalSnapshots++
			}
		}
	}

	slog.Debug("session_poller.poll_complete", "machines", len(machines), "snapshots", totalSnapshots)
}

// pollGatewaySessions connects to a machine's gateway via the agent proxy,
// sends sessions.list, and parses the response.
func pollGatewaySessions(ctx context.Context, m MachineInfo) ([]SessionData, error) {
	wsURL := fmt.Sprintf("ws://%s:9091/proxy/%s/gateway/ws", m.HostIP, m.MachineID)

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Read connect.challenge
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, _, err = conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read challenge: %w", err)
	}

	// Send connect
	connectMsg := map[string]interface{}{
		"type":   "req",
		"id":     "poller-connect",
		"method": "connect",
		"params": map[string]interface{}{
			"minProtocol": 3,
			"maxProtocol": 3,
			"client": map[string]interface{}{
				"id":       "ocm-session-poller",
				"version":  "1.0.0",
				"platform": "linux",
				"mode":     "ui",
			},
			"auth": map[string]interface{}{
				"token": m.GatewayToken,
			},
			"scopes": []string{"operator.admin"},
		},
	}
	if err := conn.WriteJSON(connectMsg); err != nil {
		return nil, fmt.Errorf("send connect: %w", err)
	}

	// Read connect response
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, respData, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read connect response: %w", err)
	}

	var connectResp struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(respData, &connectResp); err != nil || !connectResp.OK {
		return nil, fmt.Errorf("connect failed: ok=%v", connectResp.OK)
	}

	// Send sessions.list
	sessReq := map[string]interface{}{
		"type":   "req",
		"id":     "poller-sessions",
		"method": "sessions.list",
		"params": map[string]interface{}{},
	}
	if err := conn.WriteJSON(sessReq); err != nil {
		return nil, fmt.Errorf("send sessions.list: %w", err)
	}

	// Read sessions.list response
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, sessData, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read sessions response: %w", err)
	}

	// Close cleanly
	_ = conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))

	return ParseSessionsResponse(sessData)
}

// ParseSessionKey extracts agent ID and channel from a gateway session key.
//
// Formats:
//   - "agent:<agentId>:main"              → agent=<agentId>, channel="chat"
//   - "agent:<agentId>:<channel>:group:…"  → agent=<agentId>, channel=<channel>
//   - "cron:<jobId>"                       → agent="cron", channel="cron"
//   - "hook:<uuid>"                        → agent="hook", channel="hook"
func ParseSessionKey(key string) (agent, channel string) {
	parts := strings.SplitN(key, ":", 4)
	if len(parts) < 2 {
		return "unknown", "unknown"
	}

	switch parts[0] {
	case "agent":
		agent = parts[1]
		if len(parts) >= 3 && parts[2] != "main" {
			channel = parts[2]
		} else {
			channel = "chat"
		}
	case "cron":
		agent = "cron"
		channel = "cron"
	case "hook":
		agent = "hook"
		channel = "hook"
	default:
		agent = "unknown"
		channel = "unknown"
	}
	return
}

// ParseSessionsResponse parses the gateway's sessions.list response into SessionData.
func ParseSessionsResponse(data []byte) ([]SessionData, error) {
	var resp struct {
		OK      bool                   `json:"ok"`
		Payload map[string]json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("sessions.list returned ok=false")
	}

	var sessions []SessionData
	for key, raw := range resp.Payload {
		var entry struct {
			InputTokens  int `json:"inputTokens"`
			OutputTokens int `json:"outputTokens"`
			TotalTokens  int `json:"totalTokens"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			slog.Warn("session_poller.parse_entry_failed", "key", key, "error", err)
			continue
		}

		agent, channel := ParseSessionKey(key)
		sessions = append(sessions, SessionData{
			SessionKey:   key,
			AgentID:      agent,
			Channel:      channel,
			InputTokens:  entry.InputTokens,
			OutputTokens: entry.OutputTokens,
			TotalTokens:  entry.TotalTokens,
		})
	}
	return sessions, nil
}

// ListRunningMachinesForPolling is a convenience interface required by the poller.
// Implement this via a store adapter that queries running machines with their host IPs.
// This keeps the poller decoupled from the store package.

// StoreAdapter implements MachineResolver using the database.
type StoreAdapter struct {
	pool interface {
		Query(ctx context.Context, sql string, args ...interface{}) (interface{ Close(); Next() bool; Scan(dest ...interface{}) error; Err() error }, error)
	}
}
```

Wait — for the `StoreAdapter` we need to use the actual pgx pool type. Let me use a simpler approach: define the resolver as a function type or use the store directly.

Actually, let me simplify. The poller's `MachineResolver` interface can be implemented directly in `main.go` with a simple wrapper around the store. Let me remove the `StoreAdapter` from the poller file and just keep the clean interface.

Remove the `StoreAdapter` section at the bottom of the file (everything after the `ListRunningMachinesForPolling` comment).

- [ ] **Step 6: Add `ListRunningMachinesForPolling` query to `postgres.go`**

Add to `backend/internal/store/postgres.go`:

```go
type RunningMachineInfo struct {
	MachineID    string
	HostIP       string
	GatewayToken string
}

func (s *PostgresStore) ListRunningMachinesForPolling(ctx context.Context) ([]RunningMachineInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, COALESCE(h.external_ip, h.internal_ip, ''), m.gateway_token
		FROM machines m
		JOIN hosts h ON m.host_id = h.id
		WHERE m.status = 'running'
		  AND m.gateway_token IS NOT NULL
		  AND h.status = 'active'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var machines []RunningMachineInfo
	for rows.Next() {
		var m RunningMachineInfo
		if err := rows.Scan(&m.MachineID, &m.HostIP, &m.GatewayToken); err != nil {
			return nil, err
		}
		if m.HostIP != "" {
			machines = append(machines, m)
		}
	}
	return machines, rows.Err()
}
```

Add to the store interface in `store.go`:

```go
ListRunningMachinesForPolling(ctx context.Context) ([]RunningMachineInfo, error)
```

- [ ] **Step 7: Run tests**

```bash
cd backend && go test ./internal/usage/... -v
```

Expected: All 2 test functions pass (TestParseSessionKey with 7 subtests, TestParseSessionsResponse).

- [ ] **Step 8: Commit**

```bash
git add backend/internal/usage/ backend/internal/store/store.go backend/internal/store/postgres.go
git commit -m "feat: add session snapshot poller with gateway WebSocket integration"
```

---

### Task 5: Wire poller into main.go

**Files:**
- Modify: `backend/cmd/server/main.go`

- [ ] **Step 1: Create a store adapter for the poller's MachineResolver interface**

The poller expects `MachineResolver` interface, but the store returns `[]store.RunningMachineInfo`. Create a simple adapter in `main.go` or inline. The simplest approach: make the poller accept a function instead, but since we already have the interface, create a small wrapper.

Add to `backend/cmd/server/main.go`, after the projector setup (around line 267):

```go
// Session poller — polls gateway sessions every 5 minutes
type pollerStoreAdapter struct {
	db *store.PostgresStore
}

func (a *pollerStoreAdapter) ListRunningMachinesForPolling(ctx context.Context) ([]usage.MachineInfo, error) {
	machines, err := a.db.ListRunningMachinesForPolling(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]usage.MachineInfo, len(machines))
	for i, m := range machines {
		result[i] = usage.MachineInfo{
			MachineID:    m.MachineID,
			HostIP:       m.HostIP,
			GatewayToken: m.GatewayToken,
		}
	}
	return result, nil
}
```

Actually, this is getting complex with type conversion. Simpler: make `usage.MachineInfo` match `store.RunningMachineInfo` and use the store directly. Let me revise the approach.

Update `poller.go`: change `MachineResolver` to accept the store type directly:

```go
// MachineResolver returns running machines for polling.
type MachineResolver func(ctx context.Context) ([]MachineInfo, error)
```

And update `SessionPoller`:

```go
type SessionPoller struct {
	resolver MachineResolver
	store    SnapshotStore
}

func NewSessionPoller(resolver MachineResolver, store SnapshotStore) *SessionPoller {
	return &SessionPoller{resolver: resolver, store: store}
}
```

Then in `PollOnce`:
```go
machines, err := p.resolver(ctx)
```

This way `main.go` just passes a closure:

```go
sessionPoller := usage.NewSessionPoller(
	func(ctx context.Context) ([]usage.MachineInfo, error) {
		machines, err := db.ListRunningMachinesForPolling(ctx)
		if err != nil {
			return nil, err
		}
		result := make([]usage.MachineInfo, len(machines))
		for i, m := range machines {
			result[i] = usage.MachineInfo{
				MachineID:    m.MachineID,
				HostIP:       m.HostIP,
				GatewayToken: m.GatewayToken,
			}
		}
		return result, nil
	},
	db,
)
go sessionPoller.Start(ctx, 5*time.Minute)
```

- [ ] **Step 2: Add the import and wiring to `main.go`**

Add import:
```go
"github.com/mathaix/openclawmachines/backend/internal/usage"
```

Add the poller setup code from Step 1 after the projector start (around line 267), before the `machineRuntime` creation.

- [ ] **Step 3: Run tests**

```bash
make test-go
```

Expected: All tests pass.

- [ ] **Step 4: Commit**

```bash
git add backend/cmd/server/main.go backend/internal/usage/poller.go
git commit -m "feat: wire session poller into main.go (5-min interval)"
```

---

### Task 6: Frontend types and API function

**Files:**
- Modify: `frontend/src/lib/types.ts`
- Modify: `frontend/src/lib/api.ts`

- [ ] **Step 1: Add TypeScript types**

Add to `frontend/src/lib/types.ts` after the `MachineUsageDetail` interface:

```typescript
export interface UsageBucketEntry {
  provider: string;
  model: string;
  source: string;
  input_tokens: number;
  output_tokens: number;
  cost_microcents: number;
  request_count: number;
}

export interface UsageBucket {
  timestamp: string;
  entries: UsageBucketEntry[];
}

export interface UsageBreakdown {
  period: string;
  since: string;
  buckets: UsageBucket[];
  totals: {
    input_tokens: number;
    output_tokens: number;
    cost_microcents: number;
    request_count: number;
  };
}
```

- [ ] **Step 2: Update `UsageRecord` type**

Update the existing `UsageRecord` in `frontend/src/lib/types.ts:173-181` to match the new backend response (added `source`, removed `request_id` which was never present in the type anyway):

```typescript
export interface UsageRecord {
  id: number;
  provider: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  cost_microcents: number;
  source: string;
  created_at: string;
}
```

- [ ] **Step 3: Add API function**

Add to `frontend/src/lib/api.ts` after the `getMachineUsage` function:

```typescript
export const getMachineUsageBreakdown = (
  accountId: number,
  machineId: string,
  period: "hour" | "day",
  since?: string,
) => {
  const params = new URLSearchParams({ period });
  if (since) params.set("since", since);
  return request<UsageBreakdown>(`/accounts/${accountId}/machines/${machineId}/usage/breakdown?${params}`);
};
```

Add the import for `UsageBreakdown` if the types file uses named imports.

- [ ] **Step 4: Run typecheck**

```bash
make typecheck
```

Expected: No errors.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/types.ts frontend/src/lib/api.ts
git commit -m "feat: add UsageBreakdown types and API function"
```

---

### Task 7: Usage tab component

**Files:**
- Create: `frontend/src/pages/machine-tabs/UsageTab.tsx`
- Modify: `frontend/src/pages/MachineView.tsx:19-30` (add tab)

- [ ] **Step 1: Create the UsageTab component**

Create `frontend/src/pages/machine-tabs/UsageTab.tsx`:

```tsx
import { useEffect, useState } from "react";
import { DollarSign, Zap, TrendingUp, BarChart3 } from "lucide-react";
import type { Machine, UsageBreakdown, UsageBucketEntry } from "../../lib/types";
import { getMachineUsageBreakdown } from "../../lib/api";

interface UsageTabProps {
  machine: Machine;
  accountId: number;
}

function formatMicrocents(mc: number): string {
  const dollars = mc / 100000000;
  return `$${dollars.toFixed(4)}`;
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}K`;
  return String(n);
}

function formatHour(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function formatDay(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

type Period = "hour" | "day";

export function UsageTab({ machine, accountId }: UsageTabProps) {
  const [period, setPeriod] = useState<Period>("hour");
  const [breakdown, setBreakdown] = useState<UsageBreakdown | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    getMachineUsageBreakdown(accountId, machine.id, period)
      .then((b) => setBreakdown(b ?? null))
      .catch(() => setBreakdown(null))
      .finally(() => setLoading(false));
  }, [accountId, machine.id, period]);

  const totals = breakdown?.totals ?? { input_tokens: 0, output_tokens: 0, cost_microcents: 0, request_count: 0 };

  // Aggregate entries across all buckets by model for the summary table
  const modelTotals = new Map<string, UsageBucketEntry & { key: string }>();
  for (const bucket of breakdown?.buckets ?? []) {
    for (const e of bucket.entries) {
      const key = `${e.provider}/${e.model}`;
      const existing = modelTotals.get(key);
      if (existing) {
        existing.input_tokens += e.input_tokens;
        existing.output_tokens += e.output_tokens;
        existing.cost_microcents += e.cost_microcents;
        existing.request_count += e.request_count;
      } else {
        modelTotals.set(key, { ...e, key });
      }
    }
  }
  const sortedModels = [...modelTotals.values()].sort((a, b) => b.cost_microcents - a.cost_microcents);

  // Find max cost per bucket for the bar chart scaling
  const bucketCosts = (breakdown?.buckets ?? []).map((b) =>
    b.entries.reduce((sum, e) => sum + e.cost_microcents, 0)
  );
  const maxBucketCost = Math.max(...bucketCosts, 1);

  return (
    <div className="space-y-6">
      {/* Period toggle */}
      <div className="flex items-center gap-2">
        <button
          onClick={() => setPeriod("hour")}
          className={`px-3 py-1.5 text-sm font-medium rounded-[var(--radius-sm)] transition-colors ${
            period === "hour"
              ? "bg-brand-600 text-white"
              : "text-text-secondary border border-border hover:bg-[rgba(255,255,255,0.04)]"
          }`}
        >
          Today (Hourly)
        </button>
        <button
          onClick={() => setPeriod("day")}
          className={`px-3 py-1.5 text-sm font-medium rounded-[var(--radius-sm)] transition-colors ${
            period === "day"
              ? "bg-brand-600 text-white"
              : "text-text-secondary border border-border hover:bg-[rgba(255,255,255,0.04)]"
          }`}
        >
          This Month (Daily)
        </button>
      </div>

      {/* Summary cards */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <SummaryCard icon={<DollarSign className="w-4 h-4 text-green-400" />} label="Total Cost" value={formatMicrocents(totals.cost_microcents)} />
        <SummaryCard icon={<Zap className="w-4 h-4 text-yellow-400" />} label="Input Tokens" value={formatTokens(totals.input_tokens)} />
        <SummaryCard icon={<TrendingUp className="w-4 h-4 text-blue-400" />} label="Output Tokens" value={formatTokens(totals.output_tokens)} />
        <SummaryCard icon={<BarChart3 className="w-4 h-4 text-purple-400" />} label="Requests" value={totals.request_count.toLocaleString()} />
      </div>

      {loading ? (
        <div className="h-48 bg-card border border-border rounded-[var(--radius-lg)] animate-pulse" />
      ) : (
        <>
          {/* Bar chart */}
          <div className="bg-card border border-border rounded-[var(--radius-lg)] p-4 md:p-6">
            <h3 className="text-sm font-semibold text-text-primary mb-4">
              {period === "hour" ? "Hourly" : "Daily"} Cost
            </h3>
            {(breakdown?.buckets ?? []).length === 0 ? (
              <p className="text-sm text-text-tertiary">No usage data for this period.</p>
            ) : (
              <div className="flex items-end gap-1 h-32">
                {(breakdown?.buckets ?? []).map((bucket, i) => {
                  const cost = bucket.entries.reduce((s, e) => s + e.cost_microcents, 0);
                  const pct = (cost / maxBucketCost) * 100;
                  return (
                    <div key={i} className="flex-1 flex flex-col items-center gap-1 min-w-0">
                      <div
                        className="w-full bg-brand-600 rounded-t-sm transition-all"
                        style={{ height: `${Math.max(pct, 2)}%` }}
                        title={`${period === "hour" ? formatHour(bucket.timestamp) : formatDay(bucket.timestamp)}: ${formatMicrocents(cost)}`}
                      />
                      {(breakdown?.buckets ?? []).length <= 24 && (
                        <span className="text-[9px] text-text-muted truncate w-full text-center">
                          {period === "hour" ? formatHour(bucket.timestamp) : formatDay(bucket.timestamp)}
                        </span>
                      )}
                    </div>
                  );
                })}
              </div>
            )}
          </div>

          {/* Model breakdown table */}
          <div className="bg-card border border-border rounded-[var(--radius-lg)] overflow-hidden">
            <div className="p-4 md:p-6 pb-0">
              <h3 className="text-sm font-semibold text-text-primary mb-3">By Model</h3>
            </div>
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border text-text-tertiary">
                    <th className="text-left px-4 md:px-6 py-2 font-medium">Model</th>
                    <th className="text-right px-4 py-2 font-medium">Input</th>
                    <th className="text-right px-4 py-2 font-medium">Output</th>
                    <th className="text-right px-4 py-2 font-medium">Cost</th>
                    <th className="text-right px-4 md:px-6 py-2 font-medium">Requests</th>
                  </tr>
                </thead>
                <tbody>
                  {sortedModels.length === 0 ? (
                    <tr>
                      <td colSpan={5} className="px-4 md:px-6 py-4 text-text-tertiary text-center">
                        No usage data
                      </td>
                    </tr>
                  ) : (
                    sortedModels.map((m) => (
                      <tr key={m.key} className="border-b border-border-subtle hover:bg-[rgba(255,255,255,0.02)]">
                        <td className="px-4 md:px-6 py-2.5">
                          <div className="font-medium text-text-primary">{m.model}</div>
                          <div className="text-xs text-text-muted">{m.provider}</div>
                        </td>
                        <td className="text-right px-4 py-2.5 tabular-nums text-text-secondary">{formatTokens(m.input_tokens)}</td>
                        <td className="text-right px-4 py-2.5 tabular-nums text-text-secondary">{formatTokens(m.output_tokens)}</td>
                        <td className="text-right px-4 py-2.5 tabular-nums text-text-primary font-medium">{formatMicrocents(m.cost_microcents)}</td>
                        <td className="text-right px-4 md:px-6 py-2.5 tabular-nums text-text-secondary">{m.request_count}</td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
          </div>
        </>
      )}
    </div>
  );
}

function SummaryCard({ icon, label, value }: { icon: React.ReactNode; label: string; value: string }) {
  return (
    <div className="bg-card border border-border rounded-[var(--radius-lg)] p-3 md:p-4">
      <div className="flex items-center gap-2 mb-1">
        {icon}
        <span className="text-xs text-text-tertiary">{label}</span>
      </div>
      <div className="text-lg md:text-xl font-bold tabular-nums text-text-primary">{value}</div>
    </div>
  );
}
```

- [ ] **Step 2: Add "usage" tab to MachineView**

In `frontend/src/pages/MachineView.tsx`:

Update the `TabId` type at line 19:

```typescript
type TabId = "overview" | "usage" | "model" | "channels" | "integrations" | "browser" | "files" | "logs" | "backups";
```

Add the tab entry to `TABS` array at line 21, after the overview entry:

```typescript
const TABS: { id: TabId; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "usage", label: "Usage" },
  { id: "model", label: "Model" },
  // ... rest unchanged
];
```

Add the import at the top:

```typescript
import { UsageTab } from "./machine-tabs/UsageTab";
```

Add the tab rendering in the switch/conditional that renders tab content (find where `activeTab === "overview"` renders `<OverviewTab>`):

```tsx
{activeTab === "usage" && (
  <UsageTab machine={machine} accountId={accountId} />
)}
```

- [ ] **Step 3: Run typecheck**

```bash
make typecheck
```

Expected: No errors.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/pages/machine-tabs/UsageTab.tsx frontend/src/pages/MachineView.tsx
git commit -m "feat: add Usage tab with hourly/daily model breakdown"
```

---

### Task 8: Fix OverviewTab to use working data

**Files:**
- Modify: `frontend/src/pages/machine-tabs/OverviewTab.tsx`

- [ ] **Step 1: Verify OverviewTab works with fixed backend**

The OverviewTab at `frontend/src/pages/machine-tabs/OverviewTab.tsx:57-64` fetches usage via `getMachineUsage()` which calls `GetLLMSpendByMachine` and `GetLLMUsageByMachine`. After Task 2, those queries read from `token_usage` — so the summary cards will automatically show correct data.

Verify by:
1. Deploy the backend (Task 2 must be committed and deployed)
2. Open a machine's Overview tab in the browser
3. Confirm token counts and spend are non-zero

No code changes needed here — the backend fix in Task 2 is sufficient.

- [ ] **Step 2: Commit (if any adjustments needed)**

If the `UsageRecord` type change in Task 6 (adding `source`) causes issues with the OverviewTab's `reduce()` calls, those references (`r.input_tokens`, `r.output_tokens`) are unchanged so no fix is needed.

---

### Task 9: Investigate and fix Nebius zero tokens

**Files:**
- Modify: `backend/internal/apiproxy/providers.go:282-331` (Nebius provider)
- Modify: `backend/internal/apiproxy/streaming.go` (if SSE accumulation is the issue)

- [ ] **Step 1: Check if Nebius returns usage in streaming responses**

Many OpenAI-compatible providers don't return `usage` in streaming chunks — they only include it in the final chunk or not at all. The Nebius `ParseSSEEvent` function at `providers.go:309-329` checks `chunk.Usage` but if Nebius never sends it in SSE mode, all records will have zero tokens.

Check by looking at the `ParseJSONUsage` function at `providers.go:296-308` — this parses non-streaming responses. If Nebius requests always use streaming (SSE), the JSON parser never runs.

- [ ] **Step 2: Check the SSE accumulation logic**

In `streaming.go:66-76`, the copier accumulates tokens:

```go
if input > 0 {
    c.usage.inputTokens = input  // OVERWRITES, doesn't accumulate!
}
```

For Nebius (OpenAI-compatible), the usage is typically in the **last** chunk only. The `ParseSSEEvent` function returns `(model, input, output, isFinal)`. If the last chunk contains usage, it should work. But if Nebius sends the `[DONE]` marker as a separate event after the usage chunk, the `isFinal` return causes early tracking stop.

Looking at the Nebius `ParseSSEEvent`:
- Line 310-312: `[DONE]` → returns `("", 0, 0, true)` — this is the final event
- Line 325-326: If `chunk.Usage != nil` → returns tokens with `isFinal=true`
- Line 328: Otherwise → returns `(model, 0, 0, false)`

The issue: Nebius may send usage in the response **only in non-streaming mode**. Many providers using the OpenAI protocol don't include `usage` in streaming responses unless `stream_options.include_usage: true` is set.

- [ ] **Step 3: Fix by requesting usage in streaming mode**

Add body mutation to the Nebius provider to inject `stream_options` when the request is streaming.

Add `MutateBody` to the Nebius provider in `providers.go`:

```go
MutateBody: func(body []byte, credentialType string) []byte {
	// Request usage in streaming responses (OpenAI-compatible extension)
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	if _, hasStream := req["stream"]; hasStream {
		req["stream_options"] = map[string]interface{}{
			"include_usage": true,
		}
		modified, err := json.Marshal(req)
		if err != nil {
			return body
		}
		return modified
	}
	return body
},
```

- [ ] **Step 4: Run tests**

```bash
make test-go
```

Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/apiproxy/providers.go
git commit -m "fix: request token usage in Nebius streaming responses"
```

---

### Task 10: Deploy and verify

**Files:** None (deployment task)

- [ ] **Step 1: Run all tests**

```bash
make test-go
make typecheck
make test-gateway-e2e
```

Expected: All pass (OpenAI E2E failures are pre-existing stale keys).

- [ ] **Step 2: Deploy backend**

```bash
make deploy-backend
```

- [ ] **Step 3: Deploy frontend**

```bash
make deploy-frontend
```

- [ ] **Step 4: Verify migration ran**

```bash
DB_URL=$(gcloud secrets versions access latest --secret=OCM_DATABASE_URL --project=clarateach)
psql "$DB_URL" -c "\d session_snapshots"
psql "$DB_URL" -c "SELECT column_name FROM information_schema.columns WHERE table_name = 'token_usage' AND column_name IN ('category', 'category_detail');"
```

- [ ] **Step 5: Verify usage endpoint returns data**

```bash
curl -s https://api.openclawmachines.com/api/accounts/21/machines/{MACHINE_ID}/usage | python3 -m json.tool | head -20
curl -s "https://api.openclawmachines.com/api/accounts/21/machines/{MACHINE_ID}/usage/breakdown?period=day" | python3 -m json.tool | head -30
```

Expected: Non-zero token counts and cost values.

- [ ] **Step 6: Verify poller is running**

```bash
gcloud logging read 'resource.type="cloud_run_revision" AND resource.labels.service_name="ocm-backend" AND jsonPayload.msg=~"session_poller"' --project=clarateach --limit=10 --format='value(timestamp,jsonPayload.level,jsonPayload.msg)'
```

Expected: `session_poller.started` log entry.

- [ ] **Step 7: Verify frontend shows data**

Open a machine in the browser. Check:
1. Overview tab shows non-zero token counts and spend
2. Usage tab shows hourly/daily breakdown with model table
3. No console errors

- [ ] **Step 8: Update CurrentFeature.md**

Update `docs/CurrentFeature.md` with the changes made.

- [ ] **Step 9: Commit and push**

```bash
git add docs/CurrentFeature.md
git commit -m "docs: update CurrentFeature.md for usage dashboard"
git push
```
