package cgroup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const realCPUStat = `usage_usec 777619944
user_usec 458497702
system_usec 319122242
nr_periods 0
nr_throttled 0
throttled_usec 0
nr_bursts 0
burst_usec 0
`

func TestParseCPUStat_ValidFile(t *testing.T) {
	got, err := ParseCPUStat(strings.NewReader(realCPUStat))
	if err != nil {
		t.Fatalf("ParseCPUStat: %v", err)
	}
	if got != 777619944 {
		t.Errorf("usage_usec = %d, want 777619944", got)
	}
}

func TestParseCPUStat_OutOfOrderFields(t *testing.T) {
	in := "user_usec 5\nsystem_usec 7\nusage_usec 42\n"
	got, err := ParseCPUStat(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseCPUStat: %v", err)
	}
	if got != 42 {
		t.Errorf("usage_usec = %d, want 42", got)
	}
}

func TestParseCPUStat_MissingUsageUsec(t *testing.T) {
	in := "user_usec 5\nsystem_usec 7\n"
	_, err := ParseCPUStat(strings.NewReader(in))
	if !errors.Is(err, ErrMissingField) {
		t.Fatalf("error = %v, want ErrMissingField", err)
	}
}

func TestParseCPUStat_InvalidNumber(t *testing.T) {
	in := "usage_usec not-a-number\n"
	_, err := ParseCPUStat(strings.NewReader(in))
	if err == nil {
		t.Fatalf("expected parse error, got nil")
	}
}

func TestParseMemoryCurrent_PlainInteger(t *testing.T) {
	got, err := ParseMemoryCurrent(strings.NewReader("524288000"))
	if err != nil {
		t.Fatalf("ParseMemoryCurrent: %v", err)
	}
	if got != 524288000 {
		t.Errorf("got %d, want 524288000", got)
	}
}

func TestParseMemoryCurrent_TrailingNewline(t *testing.T) {
	got, err := ParseMemoryCurrent(strings.NewReader("12345\n"))
	if err != nil {
		t.Fatalf("ParseMemoryCurrent: %v", err)
	}
	if got != 12345 {
		t.Errorf("got %d, want 12345", got)
	}
}

func TestParseMemoryCurrent_Empty(t *testing.T) {
	_, err := ParseMemoryCurrent(strings.NewReader(""))
	if !errors.Is(err, ErrMissingField) {
		t.Fatalf("error = %v, want ErrMissingField", err)
	}
}

func TestParseMemoryCurrent_NotANumber(t *testing.T) {
	_, err := ParseMemoryCurrent(strings.NewReader("max"))
	if err == nil {
		t.Fatalf("expected parse error, got nil")
	}
}

func TestReadSample_UnitDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte(realCPUStat), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.current"), []byte("100\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 4, 29, 18, 33, 1, 0, time.UTC)
	s, err := ReadSample(dir, now)
	if err != nil {
		t.Fatalf("ReadSample: %v", err)
	}
	if s.UsageUsec != 777619944 || s.MemBytes != 100 || !s.Wall.Equal(now) {
		t.Errorf("Sample = %+v, want UsageUsec=777619944 MemBytes=100", s)
	}
}

func TestReadSample_MissingFiles(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadSample(dir, time.Now())
	if err == nil {
		t.Fatalf("expected error for missing cpu.stat, got nil")
	}
}

func TestCPUPercent_FirstSample(t *testing.T) {
	curr := Sample{Wall: time.Now(), UsageUsec: 100}
	pct, ok := CPUPercent(Sample{}, curr)
	if ok {
		t.Errorf("first sample: ok=true, want false; pct=%v", pct)
	}
}

func TestCPUPercent_SteadyOneCore(t *testing.T) {
	t0 := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	prev := Sample{Wall: t0, UsageUsec: 0}
	// One full second of CPU time over one wall second = 100%.
	curr := Sample{Wall: t0.Add(time.Second), UsageUsec: 1_000_000}
	pct, ok := CPUPercent(prev, curr)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if pct < 99.5 || pct > 100.5 {
		t.Errorf("pct = %v, want ~100", pct)
	}
}

func TestCPUPercent_MultiCoreOver100(t *testing.T) {
	t0 := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	prev := Sample{Wall: t0, UsageUsec: 0}
	// Two full seconds of CPU time over one wall second = 200% (2-vCPU saturated).
	curr := Sample{Wall: t0.Add(time.Second), UsageUsec: 2_000_000}
	pct, ok := CPUPercent(prev, curr)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if pct < 199.5 || pct > 200.5 {
		t.Errorf("pct = %v, want ~200", pct)
	}
}

func TestCPUPercent_ReturnsFalseOnReset(t *testing.T) {
	t0 := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	// curr.UsageUsec < prev.UsageUsec — cgroup destroyed and recreated under
	// the same unit name. Caller must treat as fresh first sample.
	prev := Sample{Wall: t0, UsageUsec: 1_000_000_000}
	curr := Sample{Wall: t0.Add(time.Second), UsageUsec: 50}
	_, ok := CPUPercent(prev, curr)
	if ok {
		t.Errorf("reset case: ok=true, want false")
	}
}

func TestCPUPercent_ZeroWallDelta(t *testing.T) {
	t0 := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	prev := Sample{Wall: t0, UsageUsec: 0}
	curr := Sample{Wall: t0, UsageUsec: 100}
	_, ok := CPUPercent(prev, curr)
	if ok {
		t.Errorf("zero wall delta: ok=true, want false")
	}
}

func TestDiscover_FixtureDir(t *testing.T) {
	root := t.TempDir()
	mkdir := func(name string) {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Two real ones, two distractors.
	machineUUID := "9f3c1234-5678-4abc-9def-0123456789ab"
	browserUUID := "00000000-1111-4222-8333-444444444444"
	mkdir("ocm-vm-" + machineUUID + ".service")
	mkdir("ocm-browser-vm-" + browserUUID + ".service")
	mkdir("docker.service")
	mkdir("ocm-agent.service")
	// Bad UUID shape — must be ignored.
	mkdir("ocm-vm-not-a-uuid.service")
	// File (not dir) — must be ignored.
	if err := os.WriteFile(filepath.Join(root, "stray.service"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d refs, want 2: %+v", len(got), got)
	}

	// Sorted by VMID — browser UUID starts with "0000", machine with "9f3c",
	// so browser comes first.
	if got[0].Kind != KindBrowser || got[0].VMID != browserUUID {
		t.Errorf("got[0] = %+v, want browser %s", got[0], browserUUID)
	}
	if got[1].Kind != KindMachine || got[1].VMID != machineUUID {
		t.Errorf("got[1] = %+v, want machine %s", got[1], machineUUID)
	}
}

func TestDiscover_MissingRoot(t *testing.T) {
	_, err := Discover(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatalf("expected error for missing root, got nil")
	}
}
