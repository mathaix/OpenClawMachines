package routing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// mockRouteReader is a test double for RouteReader.
type mockRouteReader struct {
	routes []RouteSnapshot
	err    error
}

func (m *mockRouteReader) ListRunningRoutes(_ context.Context) ([]RouteSnapshot, error) {
	return m.routes, m.err
}

// mockKVWriter is a test double for KVWriter.
type mockKVWriter struct {
	mu      sync.Mutex
	puts    []kvPutCall
	deletes []kvDeleteCall
	putErr  error
}

type kvPutCall struct {
	accountSlug string
	machineSlug string
	entry       KVRouteEntry
}

type kvDeleteCall struct {
	accountSlug string
	machineSlug string
}

func (m *mockKVWriter) PutRouteSync(_ context.Context, accountSlug, machineSlug string, entry KVRouteEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts = append(m.puts, kvPutCall{accountSlug, machineSlug, entry})
	return m.putErr
}

func (m *mockKVWriter) DeleteRouteSync(_ context.Context, accountSlug, machineSlug string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes = append(m.deletes, kvDeleteCall{accountSlug, machineSlug})
	return nil
}

func (m *mockKVWriter) PutAccount(_ context.Context, _ string, _ KVAccountEntry) error {
	return nil
}

func TestProjector_SyncsAllRunningRoutes(t *testing.T) {
	reader := &mockRouteReader{
		routes: []RouteSnapshot{
			{AccountSlug: "acme", MachineSlug: "alpha", MachineID: "m1", HostHostname: "h1.example.com", ProxyToken: "tok1"},
			{AccountSlug: "acme", MachineSlug: "beta", MachineID: "m2", HostHostname: "h2.example.com", ProxyToken: "tok2"},
		},
	}
	kv := &mockKVWriter{}
	p := NewProjector(reader, kv)

	count, err := p.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 synced, got %d", count)
	}
	if len(kv.puts) != 2 {
		t.Fatalf("expected 2 KV puts, got %d", len(kv.puts))
	}

	// Verify first route.
	if kv.puts[0].accountSlug != "acme" || kv.puts[0].machineSlug != "alpha" {
		t.Errorf("unexpected first put: %+v", kv.puts[0])
	}
	if kv.puts[0].entry.MachineID != "m1" || kv.puts[0].entry.HostHostname != "h1.example.com" {
		t.Errorf("unexpected entry for first put: %+v", kv.puts[0].entry)
	}

	// Verify second route.
	if kv.puts[1].accountSlug != "acme" || kv.puts[1].machineSlug != "beta" {
		t.Errorf("unexpected second put: %+v", kv.puts[1])
	}
	if kv.puts[1].entry.MachineID != "m2" {
		t.Errorf("unexpected entry for second put: %+v", kv.puts[1].entry)
	}
}

func TestProjector_SyncOnce_ReaderError(t *testing.T) {
	reader := &mockRouteReader{err: errors.New("db down")}
	kv := &mockKVWriter{}
	p := NewProjector(reader, kv)

	_, err := p.SyncOnce(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(kv.puts) != 0 {
		t.Errorf("expected no KV puts on reader error, got %d", len(kv.puts))
	}
}

func TestProjector_StartStop(t *testing.T) {
	reader := &mockRouteReader{routes: []RouteSnapshot{
		{AccountSlug: "x", MachineSlug: "y", MachineID: "m1"},
	}}
	kv := &mockKVWriter{}
	p := NewProjector(reader, kv)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		p.Start(ctx, 10*time.Millisecond)
		close(done)
	}()

	// Wait for at least one tick.
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// OK
	case <-time.After(time.Second):
		t.Fatal("Start did not stop after context cancellation")
	}

	kv.mu.Lock()
	n := len(kv.puts)
	kv.mu.Unlock()
	if n == 0 {
		t.Error("expected at least one KV put from ticker")
	}
}
