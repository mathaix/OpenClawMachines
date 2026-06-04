package routing

import (
	"context"
	"testing"
)

func TestReconciler_DeletesStaleRoutes(t *testing.T) {
	// DB has only "acme:alpha".
	reader := &mockRouteReader{
		routes: []RouteSnapshot{
			{AccountSlug: "acme", MachineSlug: "alpha", MachineID: "m1"},
		},
	}
	kv := &mockKVWriter{}
	rec := NewReconciler(reader, kv)

	// KV has "acme:alpha" (valid) and "acme:beta" (stale).
	kvRoutes := map[string]KVRouteEntry{
		"acme:alpha": {MachineID: "m1"},
		"acme:beta":  {MachineID: "m2"},
	}

	stale, err := rec.ReconcileOnce(context.Background(), kvRoutes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale != 1 {
		t.Errorf("expected 1 stale deletion, got %d", stale)
	}
	if len(kv.deletes) != 1 {
		t.Fatalf("expected 1 KV delete, got %d", len(kv.deletes))
	}
	if kv.deletes[0].accountSlug != "acme" || kv.deletes[0].machineSlug != "beta" {
		t.Errorf("unexpected delete call: %+v", kv.deletes[0])
	}
}

func TestReconciler_NoStaleRoutes(t *testing.T) {
	// DB and KV are in sync.
	reader := &mockRouteReader{
		routes: []RouteSnapshot{
			{AccountSlug: "acme", MachineSlug: "alpha", MachineID: "m1"},
			{AccountSlug: "acme", MachineSlug: "beta", MachineID: "m2"},
		},
	}
	kv := &mockKVWriter{}
	rec := NewReconciler(reader, kv)

	kvRoutes := map[string]KVRouteEntry{
		"acme:alpha": {MachineID: "m1"},
		"acme:beta":  {MachineID: "m2"},
	}

	stale, err := rec.ReconcileOnce(context.Background(), kvRoutes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale != 0 {
		t.Errorf("expected 0 stale deletions, got %d", stale)
	}
	if len(kv.deletes) != 0 {
		t.Errorf("expected no KV deletes, got %d", len(kv.deletes))
	}
}

func TestReconciler_EmptyKV(t *testing.T) {
	reader := &mockRouteReader{
		routes: []RouteSnapshot{
			{AccountSlug: "acme", MachineSlug: "alpha", MachineID: "m1"},
		},
	}
	kv := &mockKVWriter{}
	rec := NewReconciler(reader, kv)

	stale, err := rec.ReconcileOnce(context.Background(), map[string]KVRouteEntry{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale != 0 {
		t.Errorf("expected 0 stale, got %d", stale)
	}
}
