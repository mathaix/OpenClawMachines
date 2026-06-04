// Package maint owns the periodic database maintenance jobs for the VM
// resource metrics tables: partition lifecycle, downsampling, retention.
//
// Each job is wrapped in a transaction-scoped advisory lock
// (pg_try_advisory_xact_lock) so multiple Cloud Run instances can run the
// scheduler safely — only one instance runs each job at a time.
package maint

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Per-day partitions follow the same naming convention as migration 084's
// bootstrap DO block: <table>_y<YYYY>m<MM>d<DD>.
//
// Pure helpers here so they're easy to unit-test without a database.

// partitionName returns the deterministic partition name for a given table
// and UTC date. Matches what migration 084 generates.
func partitionName(table string, date time.Time) string {
	d := date.UTC()
	return fmt.Sprintf("%s_y%04dm%02dd%02d", table, d.Year(), int(d.Month()), d.Day())
}

// partitionDateRe captures the YYYY/MM/DD trailing the parent table name.
// Anchored to end-of-string so we don't match suffixed names accidentally.
var partitionDateRe = regexp.MustCompile(`_y(\d{4})m(\d{2})d(\d{2})$`)

// parsePartitionDate extracts the partition's nominal date from its name.
// Returns false if the name doesn't match the expected pattern (e.g.
// it's the parent table itself or a non-OCM partition that snuck in).
func parsePartitionDate(name string) (time.Time, bool) {
	m := partitionDateRe.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	if year < 2000 || year > 9999 || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
}

// dayBounds returns the [start, end) UTC timestamps for a partition covering
// the given date.
func dayBounds(date time.Time) (start, end time.Time) {
	d := date.UTC().Truncate(24 * time.Hour)
	return d, d.AddDate(0, 0, 1)
}
