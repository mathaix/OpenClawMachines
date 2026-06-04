package reconciler

import (
	"context"
	"testing"
)

func TestHeartbeatOnlyChecker_AlwaysReturnsTrue(t *testing.T) {
	tests := []struct {
		name    string
		project string
		zone    string
		vmName  string
	}{
		{name: "ovhcloud host", project: "any-project", zone: "external", vmName: "registered-12345"},
		{name: "hetzner host", project: "", zone: "", vmName: "hetzner-box-1"},
		{name: "empty fields", project: "", zone: "", vmName: ""},
		{name: "gcp-like fields", project: "my-project", zone: "us-central1-a", vmName: "gce-host-1"},
	}

	checker := &HeartbeatOnlyChecker{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exists, err := checker.InstanceExists(context.Background(), tt.project, tt.zone, tt.vmName)
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if !exists {
				t.Fatal("expected InstanceExists to return true, got false")
			}
		})
	}
}
