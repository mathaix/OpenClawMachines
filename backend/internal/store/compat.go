package store

import (
	"fmt"
	"time"
)

// IncompatibleRootfsError signals that a requested openclaw release requires
// a newer rootfs than the machine currently runs. Handlers translate this into
// HTTP 422.
type IncompatibleRootfsError struct {
	OpenclawVersion      string
	MinRootfsVersion     string
	CurrentRootfsVersion string
}

func (e *IncompatibleRootfsError) Error() string {
	return fmt.Sprintf(
		"OpenClaw %s requires rootfs %s or newer; machine is on %s. Upgrade rootfs first, or upgrade both together.",
		e.OpenclawVersion, e.MinRootfsVersion, e.CurrentRootfsVersion,
	)
}

// CheckOpenClawRootfsCompat returns *IncompatibleRootfsError when the openclaw
// release requires a newer rootfs than currentRootfsVersion. Returns nil when
// there is no constraint to evaluate: nil release, NULL MinRootfsVersion, empty
// currentRootfsVersion, or current equals min. Otherwise the caller-supplied
// artifact_releases.created_at timestamps decide: if currentRootfsCreatedAt is
// before minRootfsCreatedAt, the pin is rejected.
//
// The function is pure; callers are responsible for looking up the two rootfs
// releases to obtain their created_at timestamps.
func CheckOpenClawRootfsCompat(
	release *ArtifactRelease,
	currentRootfsVersion string,
	minRootfsCreatedAt, currentRootfsCreatedAt time.Time,
) error {
	if release == nil || release.MinRootfsVersion == nil {
		return nil
	}
	min := *release.MinRootfsVersion
	if currentRootfsVersion == "" || currentRootfsVersion == min {
		return nil
	}
	if currentRootfsCreatedAt.Before(minRootfsCreatedAt) {
		return &IncompatibleRootfsError{
			OpenclawVersion:      release.Version,
			MinRootfsVersion:     min,
			CurrentRootfsVersion: currentRootfsVersion,
		}
	}
	return nil
}
