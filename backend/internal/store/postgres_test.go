package store

import (
	"strings"
	"testing"
	"time"
)

func TestScanMachine_MapsRuntimeSelectionFields(t *testing.T) {
	now := time.Unix(1775347200, 0).UTC()
	accountID := 42
	vcpus := 4
	memoryMB := 8192
	hostID := 7
	dataVolumeGB := 25
	homeHostID := 9

	row := []any{
		"machine-1",
		accountID,
		MachineKindOpenClaw,
		"Test Machine",
		"test-machine",
		"us-central1",
		"running",
		"healthy",
		vcpus,
		memoryMB,
		hostID,
		"192.168.1.10",
		"machine.example.com",
		"custom.example.com",
		"gateway-token",
		"proxy-token",
		"ready",
		now,
		now,
		now,
		now,
		now,
		dataVolumeGB,
		"rootfs-2026.04.05",
		"openclaw-2026.04.05",
		"stable",
		"rootfs-2026.04.06",
		"openclaw-2026.04.06",
		"rootfs-actual-2026.04.06",
		"openclaw-actual-2026.04.06",
		"pinned",
		"artifact",
		"rootfs-snapshot",
		"openclaw-legacy",
		now,
		"tunnel-id",
		"signing-key",
		homeHostID,
		"persistent",
		true,
		[]byte("backup-key"),
		"browser-vm-1",
	}

	m, err := scanMachine(func(dest ...any) error {
		if len(dest) != len(row) {
			t.Fatalf("scanMachine destinations = %d, want %d", len(dest), len(row))
		}
		for i, value := range row {
			assignScanValue(t, dest[i], value)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanMachine: %v", err)
	}

	if m.DesiredRootfsVersion == nil || *m.DesiredRootfsVersion != "rootfs-2026.04.05" {
		t.Fatalf("DesiredRootfsVersion = %v", m.DesiredRootfsVersion)
	}
	if m.Kind != MachineKindOpenClaw {
		t.Fatalf("Kind = %q, want %q", m.Kind, MachineKindOpenClaw)
	}
	if m.DesiredOpenclawVersion == nil || *m.DesiredOpenclawVersion != "openclaw-2026.04.05" {
		t.Fatalf("DesiredOpenclawVersion = %v", m.DesiredOpenclawVersion)
	}
	if m.DesiredChannel == nil || *m.DesiredChannel != "stable" {
		t.Fatalf("DesiredChannel = %v", m.DesiredChannel)
	}
	if m.ResolvedRootfsVersion == nil || *m.ResolvedRootfsVersion != "rootfs-2026.04.06" {
		t.Fatalf("ResolvedRootfsVersion = %v", m.ResolvedRootfsVersion)
	}
	if m.ResolvedOpenclawVersion == nil || *m.ResolvedOpenclawVersion != "openclaw-2026.04.06" {
		t.Fatalf("ResolvedOpenclawVersion = %v", m.ResolvedOpenclawVersion)
	}
	if m.ActualRootfsVersion == nil || *m.ActualRootfsVersion != "rootfs-actual-2026.04.06" {
		t.Fatalf("ActualRootfsVersion = %v", m.ActualRootfsVersion)
	}
	if m.ActualOpenclawVersion == nil || *m.ActualOpenclawVersion != "openclaw-actual-2026.04.06" {
		t.Fatalf("ActualOpenclawVersion = %v", m.ActualOpenclawVersion)
	}
	if m.VersionSource == nil || *m.VersionSource != "pinned" {
		t.Fatalf("VersionSource = %v", m.VersionSource)
	}
	if m.RuntimeSource == nil || *m.RuntimeSource != "artifact" {
		t.Fatalf("RuntimeSource = %v", m.RuntimeSource)
	}
}

func TestValidateBrowserVMTargetHostEligibility(t *testing.T) {
	now := time.Unix(1775347200, 0).UTC()
	freshHeartbeat := now.Add(-time.Minute)
	staleHeartbeat := now.Add(-4 * time.Minute)

	tests := []struct {
		name            string
		status          string
		maintenanceMode bool
		lastHeartbeat   *time.Time
		wantErr         string
	}{
		{
			name:          "ready fresh host",
			status:        "ready",
			lastHeartbeat: &freshHeartbeat,
		},
		{
			name:          "draining host rejected",
			status:        "draining",
			lastHeartbeat: &freshHeartbeat,
			wantErr:       "not ready",
		},
		{
			name:            "maintenance host rejected",
			status:          "ready",
			maintenanceMode: true,
			lastHeartbeat:   &freshHeartbeat,
			wantErr:         "maintenance mode",
		},
		{
			name:    "missing heartbeat rejected",
			status:  "ready",
			wantErr: "no heartbeat",
		},
		{
			name:          "stale heartbeat rejected",
			status:        "ready",
			lastHeartbeat: &staleHeartbeat,
			wantErr:       "heartbeat is stale",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBrowserVMTargetHostEligibility(105, tt.status, tt.maintenanceMode, tt.lastHeartbeat, now)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func assignScanValue(t *testing.T, dest any, value any) {
	t.Helper()

	switch d := dest.(type) {
	case *string:
		*d = value.(string)
	case **string:
		if value == nil {
			*d = nil
			return
		}
		v := value.(string)
		*d = &v
	case *int:
		*d = value.(int)
	case **int:
		if value == nil {
			*d = nil
			return
		}
		v := value.(int)
		*d = &v
	case *int64:
		*d = value.(int64)
	case **int64:
		if value == nil {
			*d = nil
			return
		}
		v := value.(int64)
		*d = &v
	case *bool:
		*d = value.(bool)
	case *time.Time:
		*d = value.(time.Time)
	case **time.Time:
		if value == nil {
			*d = nil
			return
		}
		v := value.(time.Time)
		*d = &v
	case *[]byte:
		if value == nil {
			*d = nil
			return
		}
		v := value.([]byte)
		*d = append([]byte(nil), v...)
	default:
		t.Fatalf("unsupported scan destination type %T", dest)
	}
}
