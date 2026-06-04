package events

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type mockActivityStore struct {
	mu     sync.Mutex
	events []store.ActivityLog
}

func (m *mockActivityStore) CreateActivity(_ context.Context, a *store.ActivityLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a.ID = int64(len(m.events) + 1)
	a.EventID = "test-event-id"
	m.events = append(m.events, *a)
	return nil
}

func (m *mockActivityStore) ListActivities(context.Context, int, store.ActivityFilter) ([]store.ActivityLog, error) {
	return m.events, nil
}

func (m *mockActivityStore) ListActivitiesByMachine(context.Context, string, store.ActivityFilter) ([]store.ActivityLog, error) {
	return m.events, nil
}

func (m *mockActivityStore) ListActivitiesAdmin(context.Context, store.ActivityFilter) ([]store.ActivityLog, error) {
	return m.events, nil
}

type mockResolver struct{}

func (m *mockResolver) GetUserName(_ context.Context, userID int) string {
	return "Alice"
}

func (m *mockResolver) GetAccountName(_ context.Context, accountID int) string {
	return "Acme Corp"
}

func (m *mockResolver) GetMachineName(_ context.Context, machineID string) string {
	return "support-bot"
}

func (m *mockResolver) GetHostContext(_ context.Context, hostID int) HostContext {
	return HostContext{
		Name:          "host-3",
		AgentVersion:  "v1.3.0",
		RootfsVersion: "v2.1.0",
	}
}

func ptr[T any](v T) *T { return &v }

func waitForEvents(ms *mockActivityStore, count int) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ms.mu.Lock()
		n := len(ms.events)
		ms.mu.Unlock()
		if n >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestActivityLog_BasicEvent(t *testing.T) {
	ms := &mockActivityStore{}
	a := New(ms, &mockResolver{})

	a.Log(context.Background(), LogParams{
		Category:  "machine",
		Action:    "machine.started",
		Status:    "success",
		ActorType: "user",
		ActorID:   ptr(42),
		AccountID: ptr(1),
		MachineID: ptr("abc-123"),
		HostID:    ptr(3),
		Summary:   "Alice started 'support-bot' on host-3",
	})

	waitForEvents(ms, 1)

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(ms.events))
	}

	e := ms.events[0]
	if e.Category != "machine" {
		t.Errorf("category = %q, want %q", e.Category, "machine")
	}
	if e.Action != "machine.started" {
		t.Errorf("action = %q, want %q", e.Action, "machine.started")
	}
	if e.Status != "success" {
		t.Errorf("status = %q, want %q", e.Status, "success")
	}
	if e.ActorName == nil || *e.ActorName != "Alice" {
		t.Errorf("actor_name = %v, want %q", e.ActorName, "Alice")
	}
	if e.AccountName == nil || *e.AccountName != "Acme Corp" {
		t.Errorf("account_name = %v, want %q", e.AccountName, "Acme Corp")
	}
	if e.MachineName == nil || *e.MachineName != "support-bot" {
		t.Errorf("machine_name = %v, want %q", e.MachineName, "support-bot")
	}
	if e.HostName == nil || *e.HostName != "host-3" {
		t.Errorf("host_name = %v, want %q", e.HostName, "host-3")
	}
	if e.AgentVersion == nil || *e.AgentVersion != "v1.3.0" {
		t.Errorf("agent_version = %v, want %q", e.AgentVersion, "v1.3.0")
	}
	if e.RootfsVersion == nil || *e.RootfsVersion != "v2.1.0" {
		t.Errorf("rootfs_version = %v, want %q", e.RootfsVersion, "v2.1.0")
	}
}

func TestActivityLog_FailureEvent(t *testing.T) {
	ms := &mockActivityStore{}
	a := New(ms, &mockResolver{})

	errMsg := "no capacity available"
	a.Log(context.Background(), LogParams{
		Category:  "machine",
		Action:    "machine.start_failed",
		Status:    "failure",
		ActorType: "user",
		ActorID:   ptr(42),
		AccountID: ptr(1),
		MachineID: ptr("abc-123"),
		Summary:   "Failed to start 'support-bot': no capacity",
		ErrorMsg:  &errMsg,
	})

	waitForEvents(ms, 1)

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(ms.events))
	}

	e := ms.events[0]
	if e.Status != "failure" {
		t.Errorf("status = %q, want %q", e.Status, "failure")
	}
	if e.ErrorMessage == nil || *e.ErrorMessage != errMsg {
		t.Errorf("error_message = %v, want %q", e.ErrorMessage, errMsg)
	}
}

func TestActivityLog_WithDuration(t *testing.T) {
	ms := &mockActivityStore{}
	a := New(ms, &mockResolver{})

	dur := 1234
	a.Log(context.Background(), LogParams{
		Category:   "machine",
		Action:     "machine.started",
		Status:     "success",
		ActorType:  "user",
		ActorID:    ptr(42),
		Summary:    "Alice started 'support-bot'",
		DurationMS: &dur,
	})

	waitForEvents(ms, 1)

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(ms.events))
	}
	if ms.events[0].DurationMS == nil || *ms.events[0].DurationMS != 1234 {
		t.Errorf("duration_ms = %v, want 1234", ms.events[0].DurationMS)
	}
}

func TestActivityLog_WithDetail(t *testing.T) {
	ms := &mockActivityStore{}
	a := New(ms, &mockResolver{})

	a.Log(context.Background(), LogParams{
		Category:  "config",
		Action:    "config.channel_added",
		Status:    "success",
		ActorType: "user",
		ActorID:   ptr(42),
		Summary:   "Alice configured Telegram",
		Detail:    map[string]any{"channel": "telegram", "provider": "telegram"},
	})

	waitForEvents(ms, 1)

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(ms.events))
	}
	if ms.events[0].Detail == nil {
		t.Error("detail should not be nil")
	}
}

func TestActivityLog_NilActivitySafe(t *testing.T) {
	var a *Activity
	// Should not panic
	a.Log(context.Background(), LogParams{
		Category: "test",
		Action:   "test.action",
		Status:   "success",
		Summary:  "should be a no-op",
	})
}

func TestActivityLog_SystemEvent(t *testing.T) {
	ms := &mockActivityStore{}
	a := New(ms, &mockResolver{})

	a.Log(context.Background(), LogParams{
		Category:  "machine",
		Action:    "machine.stall_detected",
		Status:    "failure",
		ActorType: "system",
		MachineID: ptr("abc-123"),
		AccountID: ptr(1),
		Summary:   "Machine 'support-bot' stalled during provisioning, reset",
	})

	waitForEvents(ms, 1)

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(ms.events))
	}
	e := ms.events[0]
	if e.ActorType != "system" {
		t.Errorf("actor_type = %q, want %q", e.ActorType, "system")
	}
	if e.ActorName == nil || *e.ActorName != "system" {
		t.Errorf("actor_name = %v, want %q", e.ActorName, "system")
	}
}

func TestActivityLog_AgentEvent(t *testing.T) {
	ms := &mockActivityStore{}
	a := New(ms, &mockResolver{})

	a.Log(context.Background(), LogParams{
		Category:  "agent",
		Action:    "agent.self_updated",
		Status:    "success",
		ActorType: "agent",
		ActorID:   ptr(3), // host_id
		HostID:    ptr(3),
		Summary:   "Agent on host-3 updated from v1.2.0 to v1.3.0",
	})

	waitForEvents(ms, 1)

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(ms.events))
	}
	e := ms.events[0]
	if e.ActorName == nil || *e.ActorName != "agent-v1.3.0" {
		t.Errorf("actor_name = %v, want %q", e.ActorName, "agent-v1.3.0")
	}
}

func TestActivityLog_NoResolver(t *testing.T) {
	ms := &mockActivityStore{}
	a := New(ms, nil)

	a.Log(context.Background(), LogParams{
		Category:  "machine",
		Action:    "machine.created",
		Status:    "success",
		ActorType: "user",
		ActorID:   ptr(42),
		Summary:   "Created machine",
	})

	waitForEvents(ms, 1)

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(ms.events))
	}
	// Without resolver, names should be nil
	if ms.events[0].ActorName != nil {
		t.Errorf("actor_name should be nil without resolver, got %v", ms.events[0].ActorName)
	}
}
