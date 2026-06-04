package store

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestMigration084VMMetricsHasRequiredShape locks in the schema invariants
// for the VM resource instrumentation feature.
//
// These checks run without a database so every CI invocation catches
// regressions in the migration file itself. Behavioral round-trip tests
// live in postgres_vm_metrics_test.go (integration, real Postgres).
func TestMigration084VMMetricsHasRequiredShape(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "084_vm_metrics.sql")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration 084 at %s: %v", path, err)
	}
	text := string(data)

	cases := []struct {
		name    string
		pattern string
	}{
		// vm_metrics_1s
		{"vm_metrics_1s declared as PARTITION BY RANGE on ts",
			`CREATE TABLE vm_metrics_1s\b[^;]*PARTITION BY RANGE\s*\(\s*ts\s*\)`},
		{"vm_metrics_1s.account_id is NOT NULL int",
			`account_id\s+INT\s+NOT NULL`},
		{"vm_metrics_1s.host_id is NOT NULL int",
			`host_id\s+INT\s+NOT NULL`},
		{"vm_metrics_1s.vm_id is NOT NULL UUID",
			`vm_id\s+UUID\s+NOT NULL`},
		{"vm_metrics_1s.kind has CHECK (machine|browser)",
			`kind\s+TEXT\s+NOT NULL[^,]*CHECK\s*\(\s*kind\s+IN\s*\(\s*'machine'\s*,\s*'browser'\s*\)\s*\)`},
		{"vm_metrics_1s.cpu_pct nullable (no NOT NULL on cpu_pct)",
			`cpu_pct\s+REAL\s*,`}, // REAL followed by comma — no NOT NULL
		{"vm_metrics_1s.mem_bytes is NOT NULL with non-negative CHECK",
			`mem_bytes\s+BIGINT\s+NOT NULL[^,]*CHECK\s*\(\s*mem_bytes\s*>=\s*0\s*\)`},
		{"vm_metrics_1s primary key includes vm_id, kind, ts",
			`PRIMARY KEY\s*\(\s*vm_id\s*,\s*kind\s*,\s*ts\s*\)`},
		{"vm_metrics_1s access-path index covers (account_id, vm_id, kind, ts)",
			`CREATE INDEX\s+\S+\s+ON\s+vm_metrics_1s\s*\(\s*account_id\s*,\s*vm_id\s*,\s*kind\s*,\s*ts\s*\)`},

		// vm_metrics_1m
		{"vm_metrics_1m declared as PARTITION BY RANGE on ts",
			`CREATE TABLE vm_metrics_1m\b[^;]*PARTITION BY RANGE\s*\(\s*ts\s*\)`},
		{"vm_metrics_1m has sample_count NOT NULL int",
			`sample_count\s+INT\s+NOT NULL`},
		{"vm_metrics_1m has finalized BOOLEAN NOT NULL DEFAULT FALSE",
			`finalized\s+BOOLEAN\s+NOT NULL\s+DEFAULT\s+FALSE`},
		{"vm_metrics_1m access-path index covers (account_id, vm_id, kind, ts)",
			`CREATE INDEX\s+\S+\s+ON\s+vm_metrics_1m\s*\(\s*account_id\s*,\s*vm_id\s*,\s*kind\s*,\s*ts\s*\)`},

		// Bootstrap partitions — migration must create today + a few future
		// days so the maintenance cron has a runway. CURRENT_DATE keeps it
		// portable across deploys.
		{"bootstrap partitions created via DO block referencing CURRENT_DATE",
			`DO\s*\$\$[^$]*CURRENT_DATE`},
		{"bootstrap creates partitions for vm_metrics_1s",
			`PARTITION OF vm_metrics_1s`},
		{"bootstrap creates partitions for vm_metrics_1m",
			`PARTITION OF vm_metrics_1m`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			re := regexp.MustCompile(`(?is)` + tc.pattern)
			if !re.MatchString(text) {
				t.Errorf("migration 084 missing required pattern %q", tc.pattern)
			}
		})
	}
}
