package config

import (
	"os"
	"testing"
)

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

func TestLoadDefaultsHermesArtifactManifests(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
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
	unsetEnv(t, "BROWSER_ROOTFS_GCS_MANIFEST")
	unsetEnv(t, "BROWSER_ROOTFS_VERSION")
	t.Setenv("DATABASE_URL", "postgres://test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.BrowserRootfsGCSManifest != ExperimentalKernelBrowserManifestURI {
		t.Fatalf("expected kernel browser rootfs manifest %q, got %q", ExperimentalKernelBrowserManifestURI, cfg.BrowserRootfsGCSManifest)
	}
	if cfg.BrowserRootfsVersion != StableKernelBrowserRootfsVersion {
		t.Fatalf("expected stable kernel browser rootfs version %q, got %q", StableKernelBrowserRootfsVersion, cfg.BrowserRootfsVersion)
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
	if cfg.BrowserRootfsGCSManifest != ExperimentalKernelBrowserManifestURI {
		t.Fatalf("expected kernel browser rootfs manifest %q, got %q", ExperimentalKernelBrowserManifestURI, cfg.BrowserRootfsGCSManifest)
	}
	if cfg.BrowserRootfsVersion != StableKernelBrowserRootfsVersion {
		t.Fatalf("expected stable kernel browser rootfs version %q, got %q", StableKernelBrowserRootfsVersion, cfg.BrowserRootfsVersion)
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

func TestLoadBrowserRootfsManifestDefaultsExperimentalToStableVersion(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("BROWSER_ROOTFS_GCS_MANIFEST", ExperimentalKernelBrowserManifestURI)
	unsetEnv(t, "BROWSER_ROOTFS_VERSION")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.BrowserRootfsGCSManifest != ExperimentalKernelBrowserManifestURI {
		t.Fatalf("expected experimental browser rootfs manifest %q, got %q", ExperimentalKernelBrowserManifestURI, cfg.BrowserRootfsGCSManifest)
	}
	if cfg.BrowserRootfsVersion != StableKernelBrowserRootfsVersion {
		t.Fatalf("expected stable kernel browser rootfs version %q, got %q", StableKernelBrowserRootfsVersion, cfg.BrowserRootfsVersion)
	}
}

func TestLoadBrowserRootfsVersionExplicitEmptyUsesManifestLatest(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("BROWSER_ROOTFS_GCS_MANIFEST", ExperimentalKernelBrowserManifestURI)
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
	t.Setenv("BROWSER_ROOTFS_VERSION", StableKernelBrowserRootfsVersion)

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatalf("LoadAgent returned error: %v", err)
	}
	if cfg.BrowserRootfsVersion != StableKernelBrowserRootfsVersion {
		t.Fatalf("expected browser rootfs version pin %q, got %q", StableKernelBrowserRootfsVersion, cfg.BrowserRootfsVersion)
	}
}

func TestLoadBrowserRootfsManifestExplicitEmptyDisables(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
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
