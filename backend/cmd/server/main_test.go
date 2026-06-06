package main

import (
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/config"
)

func TestHostedProvisionerRequiresGCPProject(t *testing.T) {
	if got := hostedProvisionerMissingConfig(&config.Config{ControlPlaneProfile: config.ProfileHosted}); got != "GCP_PROJECT" {
		t.Fatalf("missing config = %q, want GCP_PROJECT", got)
	}
	if got := hostedProvisionerMissingConfig(&config.Config{
		ControlPlaneProfile: config.ProfileHosted,
		GCPProject:          "project",
	}); got != "" {
		t.Fatalf("missing config = %q, want empty", got)
	}
	if got := hostedProvisionerMissingConfig(&config.Config{ControlPlaneProfile: config.ProfileLocal}); got != "" {
		t.Fatalf("local missing config = %q, want empty", got)
	}
}
