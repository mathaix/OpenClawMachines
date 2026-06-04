package maint

import (
	"testing"
	"time"
)

func TestPartitionName(t *testing.T) {
	d := time.Date(2026, 4, 29, 12, 34, 56, 0, time.UTC)
	got := partitionName("vm_metrics_1s", d)
	want := "vm_metrics_1s_y2026m04d29"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPartitionName_Padding(t *testing.T) {
	d := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	got := partitionName("vm_metrics_1m", d)
	want := "vm_metrics_1m_y2026m01d05"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParsePartitionDate(t *testing.T) {
	d, ok := parsePartitionDate("vm_metrics_1s_y2026m04d29")
	if !ok {
		t.Fatal("ok = false; want true")
	}
	want := time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)
	if !d.Equal(want) {
		t.Errorf("got %v, want %v", d, want)
	}
}

func TestParsePartitionDate_RejectsNonMatching(t *testing.T) {
	// Parser is pattern-only; table prefix filtering happens via the
	// pg_inherits join in DropOldPartitions, not here.
	cases := []string{
		"vm_metrics_1s",             // parent table itself
		"vm_metrics_other",          // unrelated
		"vm_metrics_1s_y2026m13d29", // bad month
		"vm_metrics_1s_y2026m04d32", // bad day
		"vm_metrics_1s_y0000m04d29", // bad year
	}
	for _, c := range cases {
		if _, ok := parsePartitionDate(c); ok {
			t.Errorf("parsePartitionDate(%q) ok = true, want false", c)
		}
	}
}

func TestParsePartitionDate_AcceptsAnyTablePrefix(t *testing.T) {
	// Pin the contract: parser doesn't care about the table prefix. The
	// caller filters by pg_inherits to restrict to vm_metrics_*.
	d, ok := parsePartitionDate("random_table_y2026m04d29")
	if !ok {
		t.Fatal("parser should accept any prefix with valid date suffix")
	}
	if !d.Equal(time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("got %v, want 2026-04-29 UTC", d)
	}
}

func TestDayBounds(t *testing.T) {
	d := time.Date(2026, 4, 29, 13, 14, 15, 0, time.UTC)
	start, end := dayBounds(d)
	if !start.Equal(time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v", start)
	}
	if !end.Equal(time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %v", end)
	}
}

func TestDayBounds_HandlesNonUTC(t *testing.T) {
	pst := time.FixedZone("PST", -8*3600)
	d := time.Date(2026, 4, 29, 23, 30, 0, 0, pst) // 2026-04-30 07:30 UTC
	start, _ := dayBounds(d)
	want := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	if !start.Equal(want) {
		t.Errorf("start = %v, want %v (must convert to UTC first)", start, want)
	}
}
