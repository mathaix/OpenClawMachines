// Package cgroup reads cgroup v2 files for per-VM resource sampling.
//
// Each VM on a host runs in its own systemd unit:
//   - ocm-vm-<machine-id>.service           for OpenClaw VMs
//   - ocm-browser-vm-<vm-id>.service        for Browser VMs
//
// These map to cgroup v2 paths under /sys/fs/cgroup/system.slice/.
// We sample two files:
//   - cpu.stat:        usage_usec is cumulative CPU time (microseconds, monotonic)
//   - memory.current:  current resident bytes
//
// Design: docs/plans/2026-04-29-vm-instrumentation-design.md (rev. 3).
package cgroup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kind discriminates between the two VM kinds. Mirrors store.VMMetricKind
// values but kept as a local type to avoid the store dependency in this leaf
// package.
type Kind string

const (
	KindMachine Kind = "machine"
	KindBrowser Kind = "browser"
)

// Sample is one read of a VM's cgroup files at a moment in time.
type Sample struct {
	Wall      time.Time
	UsageUsec int64
	MemBytes  int64
}

// VMRef is one discovered cgroup unit.
type VMRef struct {
	UnitName string // e.g. "ocm-vm-9f3c....service" — exact directory name under system.slice
	VMID     string // UUID extracted from UnitName
	Kind     Kind
}

// ErrMissingField is returned when a required field is absent from cpu.stat.
var ErrMissingField = errors.New("required cgroup field missing")

// Match systemd unit names that represent OpenClaw VMs. UUID shape is enforced
// so unrelated unit names with similar prefixes are rejected.
//
// Group 1 captures "browser-" or empty; group 2 captures the UUID.
var unitRe = regexp.MustCompile(
	`^ocm-(browser-)?vm-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.service$`,
)

// ParseCPUStat extracts usage_usec from a cgroup v2 cpu.stat reader.
// File format is a list of "<name> <value>" lines; we only care about
// usage_usec.
func ParseCPUStat(r io.Reader) (int64, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		key, val, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if key != "usage_usec" {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse usage_usec %q: %w", val, err)
		}
		return n, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan cpu.stat: %w", err)
	}
	return 0, fmt.Errorf("%w: usage_usec", ErrMissingField)
}

// ParseMemoryCurrent reads a cgroup v2 memory.current file. The file holds
// a single integer; trailing whitespace and an optional newline are tolerated.
func ParseMemoryCurrent(r io.Reader) (int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, fmt.Errorf("read memory.current: %w", err)
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0, fmt.Errorf("%w: memory.current empty", ErrMissingField)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse memory.current %q: %w", s, err)
	}
	return n, nil
}

// ReadSample reads cpu.stat and memory.current from a unit's cgroup directory.
// `now` is the wall clock at the moment of sampling — captured by the caller
// so all samples in a tick share a coherent timeline.
func ReadSample(unitDir string, now time.Time) (Sample, error) {
	cpuFile, err := os.Open(filepath.Join(unitDir, "cpu.stat"))
	if err != nil {
		return Sample{}, fmt.Errorf("open cpu.stat: %w", err)
	}
	defer func() { _ = cpuFile.Close() }()
	usage, err := ParseCPUStat(cpuFile)
	if err != nil {
		return Sample{}, err
	}

	memFile, err := os.Open(filepath.Join(unitDir, "memory.current"))
	if err != nil {
		return Sample{}, fmt.Errorf("open memory.current: %w", err)
	}
	defer func() { _ = memFile.Close() }()
	mem, err := ParseMemoryCurrent(memFile)
	if err != nil {
		return Sample{}, err
	}

	return Sample{Wall: now, UsageUsec: usage, MemBytes: mem}, nil
}

// CPUPercent computes the % CPU between two samples. The second return value
// (`ok`) is false when no valid percentage can be produced — either because
// `prev` is zero (first sample after discovery) or because `curr.UsageUsec`
// went backwards (cgroup destroyed and the unit name was reused for a fresh
// VM with the same UUID — rare but possible). In both cases the caller should
// drop the prev pointer, store curr as the new prev, and emit no cpu_pct
// point this tick.
func CPUPercent(prev, curr Sample) (float32, bool) {
	if prev.Wall.IsZero() {
		return 0, false
	}
	if curr.UsageUsec < prev.UsageUsec {
		return 0, false // cgroup reset; treat as a fresh first sample
	}
	wallDelta := curr.Wall.Sub(prev.Wall).Microseconds()
	if wallDelta <= 0 {
		return 0, false // duplicate sample at the same instant
	}
	usageDelta := curr.UsageUsec - prev.UsageUsec
	return float32(float64(usageDelta) / float64(wallDelta) * 100), true
}

// Discover scans the system.slice cgroup root for VM units. It returns
// the units sorted by VMID for stable ordering across ticks. Unit names
// that don't match the OpenClaw VM pattern are silently ignored.
func Discover(root string) ([]VMRef, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	var refs []VMRef
	for _, e := range entries {
		// Both .service unit dirs and `.scope` dirs live under system.slice.
		// We only care about the .service dirs that match the unit pattern.
		if !e.IsDir() {
			continue
		}
		m := unitRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		kind := KindMachine
		if m[1] == "browser-" {
			kind = KindBrowser
		}
		refs = append(refs, VMRef{
			UnitName: e.Name(),
			VMID:     m[2],
			Kind:     kind,
		})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].VMID < refs[j].VMID })
	return refs, nil
}
