package config

import (
	"testing"
)

func TestGetDataPlaneDomain_Default(t *testing.T) {
	c := &Config{}
	if got := c.GetDataPlaneDomain(); got != "openclawmachines.com" {
		t.Errorf("GetDataPlaneDomain() = %q, want default", got)
	}
}

func TestGetDataPlaneDomain_Set(t *testing.T) {
	c := &Config{DataPlaneDomain: "custom.com"}
	if got := c.GetDataPlaneDomain(); got != "custom.com" {
		t.Errorf("GetDataPlaneDomain() = %q, want %q", got, "custom.com")
	}
}

func TestGetCfAccessAuthDomain_Default(t *testing.T) {
	c := &Config{}
	if got := c.GetCfAccessAuthDomain(); got != "openclawmachines.cloudflareaccess.com" {
		t.Errorf("GetCfAccessAuthDomain() = %q, want default", got)
	}
}

func TestGetCfAccessAuthDomain_Set(t *testing.T) {
	c := &Config{CfAccessAuthDomain: "custom.cloudflareaccess.com"}
	if got := c.GetCfAccessAuthDomain(); got != "custom.cloudflareaccess.com" {
		t.Errorf("GetCfAccessAuthDomain() = %q, want %q", got, "custom.cloudflareaccess.com")
	}
}

func TestPlatformConfigStale_Empty(t *testing.T) {
	c := &Config{}
	if !c.PlatformConfigStale() {
		t.Error("expected stale when PlatformConfigAt is empty")
	}
}

func TestPlatformConfigStale_Recent(t *testing.T) {
	c := &Config{PlatformConfigAt: "2099-01-01T00:00:00Z"}
	// A timestamp in the far future should not be stale
	if c.PlatformConfigStale() {
		t.Error("expected not stale for future timestamp")
	}
}

func TestMigrateCfAppDomain(t *testing.T) {
	// Simulate loading a config with old CfAppDomain but no DataPlaneDomain
	c := &Config{CfAppDomain: "old-domain.com", APIURL: DefaultAPIURL}
	// Simulate what Load does after unmarshalling
	if c.CfAppDomain != "" && c.DataPlaneDomain == "" {
		c.DataPlaneDomain = c.CfAppDomain
	}
	if c.DataPlaneDomain != "old-domain.com" {
		t.Errorf("migration failed: DataPlaneDomain = %q", c.DataPlaneDomain)
	}
}
