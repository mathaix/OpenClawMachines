package machines

import (
	"testing"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

func TestResolveMachineRuntime_PrefersDesiredVersions(t *testing.T) {
	rootfs := "rootfs-2026.04.10"
	openclaw := "openclaw-2026.04.10"
	runtimeSource := "artifact"
	machine := &store.Machine{
		DesiredRootfsVersion:   &rootfs,
		DesiredOpenclawVersion: &openclaw,
		RuntimeSource:          &runtimeSource,
	}

	resolution := ResolveMachineRuntime(machine, RuntimeDefaults{
		DefaultRootfsVersion:   "rootfs-default",
		DefaultOpenClawVersion: "openclaw-default",
	})

	if resolution.ResolvedRootfsVersion != rootfs {
		t.Fatalf("resolved_rootfs_version = %q, want %q", resolution.ResolvedRootfsVersion, rootfs)
	}
	if resolution.ResolvedOpenClawVersion != openclaw {
		t.Fatalf("resolved_openclaw_version = %q, want %q", resolution.ResolvedOpenClawVersion, openclaw)
	}
	if resolution.VersionSource != "pinned" {
		t.Fatalf("version_source = %q, want pinned", resolution.VersionSource)
	}
	if resolution.RuntimeSource != "artifact" {
		t.Fatalf("runtime_source = %q, want artifact", resolution.RuntimeSource)
	}
}

func TestResolveMachineRuntime_UsesChannelWithResolvedVersions(t *testing.T) {
	channel := "stable"
	resolvedRootfs := "rootfs-from-channel"
	resolvedOpenclaw := "openclaw-from-channel"
	actualRootfs := "rootfs-current"
	actualOpenclaw := "openclaw-current"
	machine := &store.Machine{
		DesiredChannel:          &channel,
		ResolvedRootfsVersion:   &resolvedRootfs,
		ResolvedOpenclawVersion: &resolvedOpenclaw,
		ActualRootfsVersion:     &actualRootfs,
		ActualOpenclawVersion:   &actualOpenclaw,
	}

	resolution := ResolveMachineRuntime(machine, RuntimeDefaults{
		DefaultRootfsVersion:   "rootfs-default",
		DefaultOpenClawVersion: "openclaw-default",
	})

	if resolution.ResolvedRootfsVersion != resolvedRootfs {
		t.Fatalf("resolved_rootfs_version = %q, want %q", resolution.ResolvedRootfsVersion, resolvedRootfs)
	}
	if resolution.ResolvedOpenClawVersion != resolvedOpenclaw {
		t.Fatalf("resolved_openclaw_version = %q, want %q", resolution.ResolvedOpenClawVersion, resolvedOpenclaw)
	}
	if resolution.VersionSource != "channel" {
		t.Fatalf("version_source = %q, want channel", resolution.VersionSource)
	}
	if resolution.RuntimeSource != "artifact" {
		t.Fatalf("runtime_source = %q, want artifact", resolution.RuntimeSource)
	}
}

func TestResolveMachineRuntime_PrefersActualOverStaleResolvedWhenUnpinned(t *testing.T) {
	resolvedRootfs := "rootfs-stale"
	rootfsSnapshot := "rootfs-stale"
	actualRootfs := "rootfs-current"
	resolvedOpenclaw := "openclaw-stale"
	openclawVersion := "openclaw-stale"
	actualOpenclaw := "openclaw-current"
	machine := &store.Machine{
		ResolvedRootfsVersion:   &resolvedRootfs,
		RootfsSnapshot:          &rootfsSnapshot,
		ActualRootfsVersion:     &actualRootfs,
		ResolvedOpenclawVersion: &resolvedOpenclaw,
		OpenclawVersion:         &openclawVersion,
		ActualOpenclawVersion:   &actualOpenclaw,
	}

	resolution := ResolveMachineRuntime(machine, RuntimeDefaults{
		DefaultRootfsVersion:   "rootfs-default",
		DefaultOpenClawVersion: "openclaw-default",
	})

	if resolution.ResolvedRootfsVersion != actualRootfs {
		t.Fatalf("resolved_rootfs_version = %q, want %q", resolution.ResolvedRootfsVersion, actualRootfs)
	}
	if resolution.ResolvedOpenClawVersion != actualOpenclaw {
		t.Fatalf("resolved_openclaw_version = %q, want %q", resolution.ResolvedOpenClawVersion, actualOpenclaw)
	}
}

func TestResolveMachineRuntime_DefaultSourceIgnoresStaleResolvedWithoutActual(t *testing.T) {
	resolvedRootfs := "rootfs-stale"
	rootfsSnapshot := "rootfs-stale"
	resolvedOpenclaw := "openclaw-stale"
	openclawVersion := "openclaw-stale"
	versionSource := "default"
	machine := &store.Machine{
		ResolvedRootfsVersion:   &resolvedRootfs,
		RootfsSnapshot:          &rootfsSnapshot,
		ResolvedOpenclawVersion: &resolvedOpenclaw,
		OpenclawVersion:         &openclawVersion,
		VersionSource:           &versionSource,
	}

	resolution := ResolveMachineRuntime(machine, RuntimeDefaults{
		DefaultRootfsVersion:   "rootfs-default",
		DefaultOpenClawVersion: "openclaw-default",
	})

	if resolution.ResolvedRootfsVersion != "rootfs-default" {
		t.Fatalf("resolved_rootfs_version = %q, want rootfs-default", resolution.ResolvedRootfsVersion)
	}
	if resolution.ResolvedOpenClawVersion != "openclaw-default" {
		t.Fatalf("resolved_openclaw_version = %q, want openclaw-default", resolution.ResolvedOpenClawVersion)
	}
}

func TestResolveMachineRuntime_FallsBackToOldFieldsAndDefaults(t *testing.T) {
	rootfsSnapshot := "rootfs-old"
	openclawVersion := "openclaw-old"
	machine := &store.Machine{
		RootfsSnapshot:  &rootfsSnapshot,
		OpenclawVersion: &openclawVersion,
	}

	resolution := ResolveMachineRuntime(machine, RuntimeDefaults{
		DefaultRootfsVersion:   "rootfs-default",
		DefaultOpenClawVersion: "openclaw-default",
	})

	if resolution.ResolvedRootfsVersion != rootfsSnapshot {
		t.Fatalf("resolved_rootfs_version = %q, want %q", resolution.ResolvedRootfsVersion, rootfsSnapshot)
	}
	if resolution.ResolvedOpenClawVersion != openclawVersion {
		t.Fatalf("resolved_openclaw_version = %q, want %q", resolution.ResolvedOpenClawVersion, openclawVersion)
	}
	if resolution.VersionSource != "default" {
		t.Fatalf("version_source = %q, want default", resolution.VersionSource)
	}
	if resolution.RuntimeSource != "artifact" {
		t.Fatalf("runtime_source = %q, want artifact", resolution.RuntimeSource)
	}
}

func TestResolveMachineRuntime_UsesDefaultsWhenNoHistoryExists(t *testing.T) {
	resolution := ResolveMachineRuntime(&store.Machine{}, RuntimeDefaults{
		DefaultRootfsVersion:   "rootfs-default",
		DefaultOpenClawVersion: "openclaw-default",
	})

	if resolution.ResolvedRootfsVersion != "rootfs-default" {
		t.Fatalf("resolved_rootfs_version = %q, want rootfs-default", resolution.ResolvedRootfsVersion)
	}
	if resolution.ResolvedOpenClawVersion != "openclaw-default" {
		t.Fatalf("resolved_openclaw_version = %q, want openclaw-default", resolution.ResolvedOpenClawVersion)
	}
	if resolution.VersionSource != "default" {
		t.Fatalf("version_source = %q, want default", resolution.VersionSource)
	}
}
