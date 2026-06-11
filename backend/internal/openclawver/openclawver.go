// Package openclawver parses OpenClaw runtime version strings as they appear
// across OCM: bare npm versions ("2026.6.5"), release-manifest versions
// ("v2026.6.5-r1"), and prerelease tags ("2026.6.5-beta.2").
package openclawver

import (
	"strconv"
	"strings"
)

// Parse extracts the year and month components from an OpenClaw version
// string. ok is false when the string does not start with a YYYY.M pair.
func Parse(version string) (year, month int, ok bool) {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	// Cut at the first prerelease/revision separator: "2026.6.5-r1" → "2026.6.5"
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil || year < 2000 {
		return 0, 0, false
	}
	month, err = strconv.Atoi(parts[1])
	if err != nil || month < 1 || month > 12 {
		return 0, 0, false
	}
	return year, month, true
}

// AtLeast reports whether version is at or past the given year.month train.
// ok is false when the version cannot be parsed (callers should fall back to
// runtime detection in that case).
func AtLeast(version string, wantYear, wantMonth int) (atLeast, ok bool) {
	year, month, ok := Parse(version)
	if !ok {
		return false, false
	}
	if year != wantYear {
		return year > wantYear, true
	}
	return month >= wantMonth, true
}
