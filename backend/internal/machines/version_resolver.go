package machines

import (
	"strings"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

type RuntimeDefaults struct {
	DefaultRootfsVersion   string
	DefaultOpenClawVersion string
	MachineKind            string
}

type RuntimeResolution struct {
	ResolvedRootfsVersion   string
	ResolvedOpenClawVersion string
	MachineKind             string
	VersionSource           string
	RuntimeSource           string
}

// ResolveMachineRuntime computes the start-time runtime selection.
//
// Explicit desired pins win. Channel-resolved versions win when a desired
// channel is set. Otherwise restarts should prefer the last version that
// actually booted, because stale resolved/snapshot fields may reference
// garbage-collected artifacts.
func ResolveMachineRuntime(machine *store.Machine, defaults RuntimeDefaults) RuntimeResolution {
	hasChannel := strings.TrimSpace(firstNonEmptyPtr(machine.DesiredChannel)) != ""
	hasDefaultVersionSource := machine.VersionSource != nil && strings.TrimSpace(*machine.VersionSource) == "default"

	resolvedRootfs := firstNonEmptyPtr(machine.DesiredRootfsVersion)
	if resolvedRootfs == "" && hasChannel {
		resolvedRootfs = firstNonEmptyPtr(machine.ResolvedRootfsVersion)
	}
	if resolvedRootfs == "" {
		resolvedRootfs = firstNonEmptyPtr(machine.ActualRootfsVersion)
	}
	if resolvedRootfs == "" && !hasDefaultVersionSource {
		resolvedRootfs = firstNonEmptyPtr(machine.ResolvedRootfsVersion, machine.RootfsSnapshot)
	}
	if resolvedRootfs == "" {
		resolvedRootfs = strings.TrimSpace(defaults.DefaultRootfsVersion)
	}

	resolvedOpenClaw := firstNonEmptyPtr(machine.DesiredOpenclawVersion)
	if resolvedOpenClaw == "" && hasChannel {
		resolvedOpenClaw = firstNonEmptyPtr(machine.ResolvedOpenclawVersion)
	}
	if resolvedOpenClaw == "" {
		resolvedOpenClaw = firstNonEmptyPtr(machine.ActualOpenclawVersion)
	}
	if resolvedOpenClaw == "" && !hasDefaultVersionSource {
		resolvedOpenClaw = firstNonEmptyPtr(machine.ResolvedOpenclawVersion, machine.OpenclawVersion)
	}
	if resolvedOpenClaw == "" {
		resolvedOpenClaw = strings.TrimSpace(defaults.DefaultOpenClawVersion)
	}

	versionSource := deriveRuntimeVersionSource(
		machine.DesiredRootfsVersion,
		machine.DesiredOpenclawVersion,
		machine.DesiredChannel,
	)
	if versionSource == "default" && machine.VersionSource != nil && strings.TrimSpace(*machine.VersionSource) != "" {
		versionSource = strings.TrimSpace(*machine.VersionSource)
	}

	// Artifact is the only runtime source.
	runtimeSource := "artifact"

	return RuntimeResolution{
		ResolvedRootfsVersion:   resolvedRootfs,
		ResolvedOpenClawVersion: resolvedOpenClaw,
		MachineKind:             store.NormalizeMachineKind(defaults.MachineKind),
		VersionSource:           versionSource,
		RuntimeSource:           runtimeSource,
	}
}

func (r RuntimeResolution) MetadataSelection() *metadata.RuntimeSelection {
	selection := &metadata.RuntimeSelection{
		ResolvedRootfsVersion:   r.ResolvedRootfsVersion,
		ResolvedOpenClawVersion: r.ResolvedOpenClawVersion,
		VersionSource:           r.VersionSource,
		RuntimeSource:           r.RuntimeSource,
	}
	if store.NormalizeMachineKind(r.MachineKind) == store.MachineKindHermes {
		selection.Kind = store.MachineKindHermes
		selection.ResolvedHermesVersion = r.ResolvedOpenClawVersion
		selection.ResolvedOpenClawVersion = ""
	}
	return selection
}

func firstNonEmptyPtr(values ...*string) string {
	for _, value := range values {
		if value == nil {
			continue
		}
		trimmed := strings.TrimSpace(*value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func deriveRuntimeVersionSource(rootfsVersion, openclawVersion, channel *string) string {
	if strings.TrimSpace(firstNonEmptyPtr(rootfsVersion, openclawVersion)) != "" {
		return "pinned"
	}
	if channel != nil && strings.TrimSpace(*channel) != "" {
		return "channel"
	}
	return "default"
}
