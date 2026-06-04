package store

import (
	"errors"
	"testing"
	"time"
)

func TestCheckOpenClawRootfsCompat(t *testing.T) {
	minV := "1ae2d37-20260418T234618Z"
	oldV := "cd60435-20260414T143154Z"
	newV := "abc1234-20260501T000000Z"

	tMin := time.Date(2026, 4, 18, 23, 46, 18, 0, time.UTC)
	tOld := time.Date(2026, 4, 14, 14, 31, 54, 0, time.UTC)
	tNew := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	withMin := func(v string) *ArtifactRelease {
		return &ArtifactRelease{
			Kind:             "openclaw",
			Version:          "v2026.4.15-r1",
			MinRootfsVersion: &v,
		}
	}
	withoutMin := &ArtifactRelease{
		Kind:    "openclaw",
		Version: "v2026.4.14-r1",
	}

	cases := []struct {
		name                string
		release             *ArtifactRelease
		currentRootfs       string
		minCreatedAt        time.Time
		currentCreatedAt    time.Time
		wantErrIncompatible bool
	}{
		{
			name:          "nil release → no error",
			release:       nil,
			currentRootfs: oldV,
		},
		{
			name:          "NULL min → no error",
			release:       withoutMin,
			currentRootfs: oldV,
		},
		{
			name:          "empty current rootfs → no error",
			release:       withMin(minV),
			currentRootfs: "",
		},
		{
			name:          "current equals min → no error",
			release:       withMin(minV),
			currentRootfs: minV,
		},
		{
			name:             "current newer than min → no error",
			release:          withMin(minV),
			currentRootfs:    newV,
			minCreatedAt:     tMin,
			currentCreatedAt: tNew,
		},
		{
			name:                "current older than min → IncompatibleRootfsError",
			release:             withMin(minV),
			currentRootfs:       oldV,
			minCreatedAt:        tMin,
			currentCreatedAt:    tOld,
			wantErrIncompatible: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckOpenClawRootfsCompat(tc.release, tc.currentRootfs, tc.minCreatedAt, tc.currentCreatedAt)
			if tc.wantErrIncompatible {
				var iErr *IncompatibleRootfsError
				if !errors.As(err, &iErr) {
					t.Fatalf("expected *IncompatibleRootfsError, got %T: %v", err, err)
				}
				if iErr.OpenclawVersion != tc.release.Version {
					t.Errorf("OpenclawVersion = %q, want %q", iErr.OpenclawVersion, tc.release.Version)
				}
				if iErr.MinRootfsVersion != *tc.release.MinRootfsVersion {
					t.Errorf("MinRootfsVersion = %q, want %q", iErr.MinRootfsVersion, *tc.release.MinRootfsVersion)
				}
				if iErr.CurrentRootfsVersion != tc.currentRootfs {
					t.Errorf("CurrentRootfsVersion = %q, want %q", iErr.CurrentRootfsVersion, tc.currentRootfs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestIncompatibleRootfsError_Message(t *testing.T) {
	e := &IncompatibleRootfsError{
		OpenclawVersion:      "v2026.4.15-r1",
		MinRootfsVersion:     "1ae2d37-20260418T234618Z",
		CurrentRootfsVersion: "cd60435-20260414T143154Z",
	}
	got := e.Error()
	want := "OpenClaw v2026.4.15-r1 requires rootfs 1ae2d37-20260418T234618Z or newer; machine is on cd60435-20260414T143154Z. Upgrade rootfs first, or upgrade both together."
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
