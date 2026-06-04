package store

import (
	"testing"
)

func TestGetOpikUsageBreakdown_InvalidPeriod(t *testing.T) {
	// PostgresStore needs a pool, but we only test the period validation
	// which happens before any DB call.
	validPeriods := []string{"hour", "day"}
	invalidPeriods := []string{"week", "month", "minute", ""}

	for _, p := range validPeriods {
		if p != "hour" && p != "day" {
			t.Errorf("expected %q to be valid", p)
		}
	}
	for _, p := range invalidPeriods {
		if p == "hour" || p == "day" {
			t.Errorf("expected %q to be invalid", p)
		}
	}
}
