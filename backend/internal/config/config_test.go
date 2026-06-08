package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoadDefaultsToLocalProfileWithoutHostedDefaults(t *testing.T) {
	unsetEnv(t, "CONTROL_PLANE_PROFILE")
	unsetEnv(t, "AUTH_MODE")
	unsetEnv(t, "GCP_PROJECT")
	unsetEnv(t, "GCP_ZONE")
	unsetEnv(t, "DATA_PLANE_DOMAIN")
	unsetEnv(t, "COOKIE_DOMAIN")
	unsetEnv(t, "BACKUP_GCS_BUCKET")
	unsetEnv(t, "BROWSER_ROOTFS_GCS_MANIFEST")
	unsetEnv(t, "BROWSER_ROOTFS_VERSION")
	unsetEnv(t, "HERMES_GCS_MANIFEST")
	unsetEnv(t, "HERMES_ROOTFS_GCS_MANIFEST")
	t.Setenv("DATABASE_URL", "postgres://test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ControlPlaneProfile != ProfileLocal {
		t.Fatalf("expected default profile %q, got %q", ProfileLocal, cfg.ControlPlaneProfile)
	}
	if cfg.AuthMode != "dev" {
		t.Fatalf("expected local auth mode dev, got %q", cfg.AuthMode)
	}
	if cfg.GCPProject != "" || cfg.GCPZone != "" || cfg.GCPRegion != "" {
		t.Fatalf("expected no local GCP defaults, got project=%q zone=%q region=%q", cfg.GCPProject, cfg.GCPZone, cfg.GCPRegion)
	}
	if cfg.DataPlaneDomain != "localhost" {
		t.Fatalf("expected local data-plane domain localhost, got %q", cfg.DataPlaneDomain)
	}
	if cfg.BackupGCSBucket != "" {
		t.Fatalf("expected no local backup bucket default, got %q", cfg.BackupGCSBucket)
	}
	if cfg.BrowserRootfsGCSManifest != "" || cfg.BrowserRootfsVersion != "" {
		t.Fatalf("expected local browser rootfs support to default off, got manifest=%q version=%q", cfg.BrowserRootfsGCSManifest, cfg.BrowserRootfsVersion)
	}
	if cfg.HermesManifestURI != "" || cfg.HermesRootfsManifestURI != "" {
		t.Fatalf("expected no local Hermes GCS defaults, got runtime=%q rootfs=%q", cfg.HermesManifestURI, cfg.HermesRootfsManifestURI)
	}
	if cfg.RequiresHostedIntegrations() {
		t.Fatalf("local profile must not require hosted integrations")
	}
}

func TestLoadHostedProfileDoesNotInjectPrivateDefaults(t *testing.T) {
	t.Setenv("CONTROL_PLANE_PROFILE", ProfileHosted)
	unsetEnv(t, "AUTH_MODE")
	unsetEnv(t, "GCP_PROJECT")
	unsetEnv(t, "GCP_ZONE")
	unsetEnv(t, "DATA_PLANE_DOMAIN")
	unsetEnv(t, "COOKIE_DOMAIN")
	unsetEnv(t, "BACKUP_GCS_BUCKET")
	unsetEnv(t, "BROWSER_ROOTFS_GCS_MANIFEST")
	unsetEnv(t, "BROWSER_ROOTFS_VERSION")
	unsetEnv(t, "HERMES_GCS_MANIFEST")
	unsetEnv(t, "HERMES_ROOTFS_GCS_MANIFEST")
	t.Setenv("DATABASE_URL", "postgres://test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ControlPlaneProfile != ProfileHosted {
		t.Fatalf("expected hosted profile, got %q", cfg.ControlPlaneProfile)
	}
	if cfg.AuthMode != "cfaccess" {
		t.Fatalf("expected hosted auth mode cfaccess, got %q", cfg.AuthMode)
	}
	if cfg.GCPProject != "" || cfg.GCPZone != "" || cfg.GCPRegion != "" {
		t.Fatalf("expected no hosted GCP defaults, got project=%q zone=%q region=%q", cfg.GCPProject, cfg.GCPZone, cfg.GCPRegion)
	}
	if cfg.DataPlaneDomain != "" {
		t.Fatalf("expected no hosted data-plane domain default, got %q", cfg.DataPlaneDomain)
	}
	if cfg.BackupGCSBucket != "" {
		t.Fatalf("expected no hosted backup bucket default, got %q", cfg.BackupGCSBucket)
	}
	if cfg.BrowserRootfsGCSManifest != "" {
		t.Fatalf("expected hosted browser rootfs manifest to be explicit, got %q", cfg.BrowserRootfsGCSManifest)
	}
	if cfg.BrowserRootfsVersion != "" {
		t.Fatalf("expected hosted browser rootfs version to be explicit, got %q", cfg.BrowserRootfsVersion)
	}
	if cfg.HermesManifestURI != "" {
		t.Fatalf("expected hosted Hermes manifest to be explicit, got %q", cfg.HermesManifestURI)
	}
	if cfg.HermesRootfsManifestURI != "" {
		t.Fatalf("expected hosted Hermes rootfs manifest to be explicit, got %q", cfg.HermesRootfsManifestURI)
	}
	if !cfg.RequiresHostedIntegrations() {
		t.Fatalf("hosted profile should require hosted integrations")
	}
}

func TestLoadOperatorProfileRequiresExplicitAuthMode(t *testing.T) {
	t.Setenv("CONTROL_PLANE_PROFILE", ProfileOperator)
	unsetEnv(t, "AUTH_MODE")
	t.Setenv("DATABASE_URL", "postgres://test")

	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to reject operator profile without AUTH_MODE")
	}
	if !strings.Contains(err.Error(), "AUTH_MODE is required") {
		t.Fatalf("expected AUTH_MODE error, got %v", err)
	}
}

func TestLoadOperatorProfileUsesExplicitAuthModeAndNeutralDefaults(t *testing.T) {
	t.Setenv("CONTROL_PLANE_PROFILE", ProfileOperator)
	t.Setenv("AUTH_MODE", "firebase")
	unsetEnv(t, "GCP_PROJECT")
	unsetEnv(t, "GCP_ZONE")
	unsetEnv(t, "DATA_PLANE_DOMAIN")
	unsetEnv(t, "COOKIE_DOMAIN")
	unsetEnv(t, "BACKUP_GCS_BUCKET")
	unsetEnv(t, "BROWSER_ROOTFS_GCS_MANIFEST")
	unsetEnv(t, "HERMES_GCS_MANIFEST")
	unsetEnv(t, "HERMES_ROOTFS_GCS_MANIFEST")
	t.Setenv("DATABASE_URL", "postgres://test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.AuthMode != "firebase" {
		t.Fatalf("expected explicit operator auth mode firebase, got %q", cfg.AuthMode)
	}
	if cfg.GCPProject != "" || cfg.GCPZone != "" || cfg.DataPlaneDomain != "" {
		t.Fatalf("expected neutral operator defaults, got project=%q zone=%q data_plane_domain=%q", cfg.GCPProject, cfg.GCPZone, cfg.DataPlaneDomain)
	}
	if cfg.CookieDomain != "" {
		t.Fatalf("expected no operator cookie domain default, got %q", cfg.CookieDomain)
	}
	if cfg.RequiresHostedIntegrations() {
		t.Fatalf("operator profile must not require hosted integrations")
	}
}

func TestLoadCookieDomain(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("COOKIE_DOMAIN", ".example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.CookieDomain != ".example.com" {
		t.Fatalf("expected cookie domain from env, got %q", cfg.CookieDomain)
	}
}

func TestLoadRejectsInvalidControlPlaneProfile(t *testing.T) {
	t.Setenv("CONTROL_PLANE_PROFILE", "managed")
	t.Setenv("DATABASE_URL", "postgres://test")

	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to reject invalid profile")
	}
	if !strings.Contains(err.Error(), "invalid CONTROL_PLANE_PROFILE") {
		t.Fatalf("expected invalid profile error, got %v", err)
	}
}

func TestCloudflareConfigDetection(t *testing.T) {
	cfg := &Config{ControlPlaneProfile: ProfileOperator}
	if cfg.HasCloudflareConfig() {
		t.Fatal("expected empty config to have no Cloudflare config")
	}
	if cfg.CloudflareTunnelConfigured() || cfg.CloudflareKVConfigured() {
		t.Fatal("expected empty config to disable Cloudflare tunnel and KV")
	}

	cfg.CloudflareAPIToken = "token"
	cfg.CloudflareAccountID = "account"
	if !cfg.HasCloudflareConfig() {
		t.Fatal("expected partial Cloudflare config to be detected")
	}
	if cfg.CloudflareTunnelConfigured() {
		t.Fatal("expected tunnel config to require zone ID")
	}
	if cfg.CloudflareKVConfigured() {
		t.Fatal("expected KV config to require namespace ID")
	}

	cfg.CloudflareZoneID = "zone"
	if !cfg.CloudflareTunnelConfigured() {
		t.Fatal("expected complete tunnel config")
	}
	cfg.CloudflareKVNamespaceID = "namespace"
	if !cfg.CloudflareKVConfigured() {
		t.Fatal("expected complete KV config")
	}
}

func TestLoadAgentDefaultsRuntimeOwnerToSystemdUnit(t *testing.T) {
	t.Setenv("VM_RUNTIME_OWNER", "")

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent returned error: %v", err)
	}
	if cfg.RuntimeOwnerKind != "systemd-unit" {
		t.Fatalf("expected default runtime owner kind systemd-unit, got %q", cfg.RuntimeOwnerKind)
	}
}

func TestLoadHostedDefaultsHermesArtifactManifests(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("CONTROL_PLANE_PROFILE", ProfileHosted)
	t.Setenv("HERMES_GCS_MANIFEST", "")
	t.Setenv("HERMES_ROOTFS_GCS_MANIFEST", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.HermesManifestURI != defaultHermesManifestURI {
		t.Fatalf("expected default Hermes manifest %q, got %q", defaultHermesManifestURI, cfg.HermesManifestURI)
	}
	if cfg.HermesRootfsManifestURI != defaultHermesRootfsManifestURI {
		t.Fatalf("expected default Hermes rootfs manifest %q, got %q", defaultHermesRootfsManifestURI, cfg.HermesRootfsManifestURI)
	}
}

func TestLoadDefaultsBrowserRootfsToKernelStable(t *testing.T) {
	t.Setenv("CONTROL_PLANE_PROFILE", ProfileHosted)
	unsetEnv(t, "BROWSER_ROOTFS_GCS_MANIFEST")
	unsetEnv(t, "BROWSER_ROOTFS_VERSION")
	t.Setenv("DATABASE_URL", "postgres://test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.BrowserRootfsGCSManifest != "" {
		t.Fatalf("expected no browser rootfs manifest default, got %q", cfg.BrowserRootfsGCSManifest)
	}
	if cfg.BrowserRootfsVersion != "" {
		t.Fatalf("expected no browser rootfs version default, got %q", cfg.BrowserRootfsVersion)
	}
}

func TestLoadAgentDefaultsBrowserRootfsToKernelStable(t *testing.T) {
	unsetEnv(t, "BROWSER_ROOTFS_GCS_MANIFEST")
	unsetEnv(t, "BROWSER_ROOTFS_VERSION")
	unsetEnv(t, "OCM_ALLOW_KERNEL_BROWSER_FULL_COPY")
	unsetEnv(t, "BROWSER_STATE_DIR")

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent returned error: %v", err)
	}
	if cfg.BrowserRootfsGCSManifest != "" {
		t.Fatalf("expected no browser rootfs manifest default, got %q", cfg.BrowserRootfsGCSManifest)
	}
	if cfg.BrowserRootfsVersion != "" {
		t.Fatalf("expected no browser rootfs version default, got %q", cfg.BrowserRootfsVersion)
	}
	if cfg.AllowKernelBrowserFullCopy {
		t.Fatalf("expected kernel browser full-copy fallback to be disabled by default")
	}
	if cfg.BrowserStateDir != cfg.StateDir {
		t.Fatalf("expected browser state dir to default to state dir %q, got %q", cfg.StateDir, cfg.BrowserStateDir)
	}
}

func TestLoadAgentAllowsSeparateBrowserStateDir(t *testing.T) {
	t.Setenv("STATE_DIR", "/var/lib/ocm/vms")
	t.Setenv("BROWSER_STATE_DIR", "/var/lib/ocm-browser")

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent returned error: %v", err)
	}
	if cfg.StateDir != "/var/lib/ocm/vms" {
		t.Fatalf("expected state dir to stay unchanged, got %q", cfg.StateDir)
	}
	if cfg.BrowserStateDir != "/var/lib/ocm-browser" {
		t.Fatalf("expected separate browser state dir, got %q", cfg.BrowserStateDir)
	}
}

func TestLoadBrowserRootfsManifestDoesNotInjectHostedVersion(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("CONTROL_PLANE_PROFILE", ProfileLocal)
	t.Setenv("BROWSER_ROOTFS_GCS_MANIFEST", "gs://example-ocm-artifacts/kernel-browser-rootfs/manifest.json")
	unsetEnv(t, "BROWSER_ROOTFS_VERSION")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.BrowserRootfsGCSManifest != "gs://example-ocm-artifacts/kernel-browser-rootfs/manifest.json" {
		t.Fatalf("expected configured browser rootfs manifest, got %q", cfg.BrowserRootfsGCSManifest)
	}
	if cfg.BrowserRootfsVersion != "" {
		t.Fatalf("expected no browser rootfs version default, got %q", cfg.BrowserRootfsVersion)
	}
}

func TestLoadBrowserRootfsVersionExplicitEmptyUsesManifestLatest(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("CONTROL_PLANE_PROFILE", ProfileLocal)
	t.Setenv("BROWSER_ROOTFS_GCS_MANIFEST", "gs://example-ocm-artifacts/kernel-browser-rootfs/manifest.json")
	t.Setenv("BROWSER_ROOTFS_VERSION", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.BrowserRootfsVersion != "" {
		t.Fatalf("expected explicit empty browser rootfs version to use manifest latest, got %q", cfg.BrowserRootfsVersion)
	}
}

func TestLoadAgentAllowsExplicitKernelBrowserFullCopyEscapeHatch(t *testing.T) {
	t.Setenv("OCM_ALLOW_KERNEL_BROWSER_FULL_COPY", "1")

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent returned error: %v", err)
	}
	if !cfg.AllowKernelBrowserFullCopy {
		t.Fatalf("expected explicit kernel browser full-copy escape hatch to be enabled")
	}
}

func TestLoadAgentAllowsBrowserRootfsVersionPin(t *testing.T) {
	const version = "kernel-browser-rootfs-v1"
	t.Setenv("BROWSER_ROOTFS_VERSION", version)

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent returned error: %v", err)
	}
	if cfg.BrowserRootfsVersion != version {
		t.Fatalf("expected browser rootfs version pin %q, got %q", version, cfg.BrowserRootfsVersion)
	}
}

func TestLoadBrowserRootfsManifestExplicitEmptyDisables(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("CONTROL_PLANE_PROFILE", ProfileHosted)
	t.Setenv("BROWSER_ROOTFS_GCS_MANIFEST", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.BrowserRootfsGCSManifest != "" {
		t.Fatalf("expected explicit empty browser rootfs manifest to disable browser support, got %q", cfg.BrowserRootfsGCSManifest)
	}
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, old)
			return
		}
		_ = os.Unsetenv(key)
	})
}
