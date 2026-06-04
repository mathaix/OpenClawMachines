package store

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func compactSQL(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func requireSQLContains(t *testing.T, sql, fragment string) {
	t.Helper()
	if !strings.Contains(compactSQL(sql), compactSQL(fragment)) {
		t.Fatalf("SQL missing required fragment\nwant: %s\nsql:  %s", compactSQL(fragment), compactSQL(sql))
	}
}

func TestNormalizeOpikTagsCanonicalizesAndDedupes(t *testing.T) {
	tags := normalizeOpikTags([]string{" Needs Review ", "needs-review", "OCM", "", "  Eval   Fail  ", "env:Prod"})
	expected := []string{"needs-review", "ocm", "eval-fail", "env:prod"}
	if !reflect.DeepEqual(tags, expected) {
		t.Fatalf("unexpected normalized tags: %#v", tags)
	}

	if tags := normalizeOpikTags(nil); tags != nil {
		t.Fatalf("nil tags should stay nil, got %#v", tags)
	}
}

func TestOpikTracePanelReadSQLIsTenantScoped(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		fragments []string
	}{
		{
			name: "trace list",
			sql:  listOpikTracesByMachineSQL,
			fragments: []string{
				"LEFT JOIN opik_spans s ON s.trace_id = t.id AND s.account_id = $1",
				"WHERE t.account_id = $1",
				"t.machine_id = $2",
				"touched.account_id = $1",
				"touched.machine_id = $2",
			},
		},
		{
			name: "trace detail",
			sql:  getOpikTraceDetailSQL,
			fragments: []string{
				"LEFT JOIN opik_spans s ON s.trace_id = t.id AND s.account_id = $1",
				"WHERE t.account_id = $1",
				"AND t.id = $3",
				"t.machine_id = $2",
				"touched.account_id = $1",
				"touched.machine_id = $2",
			},
		},
		{
			name: "trace spans",
			sql:  listOpikTraceSpansSQL,
			fragments: []string{
				"LEFT JOIN machines m ON m.id::text = s.machine_id AND m.account_id = s.account_id",
				"WHERE s.account_id = $1 AND s.trace_id = $2",
				"ORDER BY s.start_time ASC, s.created_at ASC",
			},
		},
		{
			name: "account trace detail",
			sql:  getOpikTraceDetailByAccountSQL,
			fragments: []string{
				"LEFT JOIN opik_spans s ON s.trace_id = t.id AND s.account_id = $1",
				"WHERE t.account_id = $1",
				"AND t.id = $2",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, fragment := range tc.fragments {
				requireSQLContains(t, tc.sql, fragment)
			}
		})
	}
}

func TestOpikTraceTagFilterSQLUsesIndexableContainment(t *testing.T) {
	path := filepath.Join("postgres.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(data)

	if strings.Contains(text, "lower(tag.value)") {
		t.Fatal("trace tag filters should not use lower(unnest(...)); normalize on write and use array containment")
	}

	cases := []string{
		`t\.tags @> `,
		`tag_span\.tags @> `,
	}
	for _, pattern := range cases {
		if !regexp.MustCompile(pattern).MatchString(text) {
			t.Fatalf("postgres.go is missing indexable tag containment pattern %q", pattern)
		}
	}
}

func TestOpikTraceWriteSQLIsTenantScoped(t *testing.T) {
	path := filepath.Join("postgres.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(data)

	cases := []struct {
		name    string
		pattern string
	}{
		{
			name:    "trace upsert conflict path checks account and machine",
			pattern: `WHERE\s+opik_traces\.account_id\s*=\s*EXCLUDED\.account_id\s+AND\s+\(opik_traces\.machine_id\s*=\s*EXCLUDED\.machine_id\s+OR\s+opik_traces\.is_placeholder\)`,
		},
		{
			name:    "trace update scopes by id account and machine",
			pattern: `WHERE\s+id\s*=\s*\$1\s+AND\s+account_id\s*=\s*\$2\s+AND\s+machine_id\s*=\s*\$3`,
		},
		{
			name:    "span insert creates same-account placeholder trace",
			pattern: `INSERT\s+INTO\s+opik_traces\s*\(id,\s*project_id,\s*account_id,\s*machine_id,\s*start_time,\s*is_placeholder\)\s*SELECT\s+\$1,\s*\$2,\s*\$3,\s*\$4,\s*\$5,\s*true`,
		},
		{
			name:    "span placeholder rejects existing other-account trace",
			pattern: `WHERE\s+NOT\s+EXISTS\s*\(\s*SELECT\s+1\s+FROM\s+opik_traces\s+WHERE\s+id\s*=\s*\$1\s+AND\s+account_id\s*<>\s*\$3\s*\)`,
		},
		{
			name:    "span insert validates same-account trace before writing span",
			pattern: `SELECT\s+id::text\s+FROM\s+opik_traces\s+WHERE\s+id\s*=\s*\$1\s+AND\s+account_id\s*=\s*\$2`,
		},
		{
			name:    "span upsert conflict path checks account and machine",
			pattern: `WHERE\s+opik_spans\.account_id\s*=\s*EXCLUDED\.account_id\s+AND\s+opik_spans\.machine_id\s*=\s*EXCLUDED\.machine_id`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !regexp.MustCompile(tc.pattern).MatchString(text) {
				t.Fatalf("postgres.go is missing required tenant-scope guard %q", tc.pattern)
			}
		})
	}
}

func TestMigration083NormalizesAndIndexesOpikTags(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "083_opik_trace_tag_indexes.sql")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration 083 at %s: %v", path, err)
	}
	text := string(data)

	fragments := []string{
		"WITH normalized_traces AS",
		"UPDATE opik_traces",
		"WITH normalized_spans AS",
		"UPDATE opik_spans",
		"regexp_replace(lower(btrim(raw_tag)), '[[:space:]]+', '-', 'g')",
	}
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Fatalf("migration 083 is missing required fragment %q", fragment)
		}
	}

	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_opik_traces_tags_gin\s+ON opik_traces USING GIN\(tags\)`,
		`CREATE INDEX IF NOT EXISTS idx_opik_spans_tags_gin\s+ON opik_spans USING GIN\(tags\)`,
	}
	for _, pattern := range indexes {
		if !regexp.MustCompile(pattern).MatchString(text) {
			t.Fatalf("migration 083 is missing required tag index pattern %q", pattern)
		}
	}
}

func TestMigration080HasTracePanelIndexes(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "080_opik_trace_panel_indexes.sql")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration 080 at %s: %v", path, err)
	}
	text := string(data)

	cases := []struct {
		name    string
		pattern string
	}{
		{
			name:    "trace list index",
			pattern: `CREATE INDEX IF NOT EXISTS idx_opik_traces_account_machine_start\s+ON opik_traces\(account_id, machine_id, start_time DESC\)`,
		},
		{
			name:    "machine span trace lookup index",
			pattern: `CREATE INDEX IF NOT EXISTS idx_opik_spans_account_machine_trace\s+ON opik_spans\(account_id, machine_id, trace_id\)`,
		},
		{
			name:    "trace detail span ordering index",
			pattern: `CREATE INDEX IF NOT EXISTS idx_opik_spans_account_trace_start\s+ON opik_spans\(account_id, trace_id, start_time ASC, created_at ASC\)`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !regexp.MustCompile(tc.pattern).MatchString(text) {
				t.Fatalf("migration 080 is missing required index pattern %q", tc.pattern)
			}
		})
	}
}

func TestMigration082HasFeedbackScopeIndexes(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "082_opik_feedback_scores.sql")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration 082 at %s: %v", path, err)
	}
	text := string(data)

	cases := []struct {
		name    string
		pattern string
	}{
		{
			name:    "feedback table is account scoped",
			pattern: `CREATE TABLE IF NOT EXISTS opik_feedback_scores\s*\([\s\S]*account_id INTEGER NOT NULL REFERENCES accounts\(id\) ON DELETE CASCADE[\s\S]*trace_id UUID NOT NULL REFERENCES opik_traces\(id\) ON DELETE CASCADE`,
		},
		{
			name:    "trace feedback lookup index",
			pattern: `CREATE INDEX IF NOT EXISTS idx_opik_feedback_account_trace_created\s+ON opik_feedback_scores\(account_id, trace_id, created_at DESC\)`,
		},
		{
			name:    "span feedback lookup index",
			pattern: `CREATE INDEX IF NOT EXISTS idx_opik_feedback_account_trace_span\s+ON opik_feedback_scores\(account_id, trace_id, span_id\)`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !regexp.MustCompile(tc.pattern).MatchString(text) {
				t.Fatalf("migration 082 is missing required feedback structure %q", tc.pattern)
			}
		})
	}
}

func TestMigration081HasOpikTracePlaceholderColumn(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "081_opik_trace_placeholders.sql")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration 081 at %s: %v", path, err)
	}
	text := string(data)

	pattern := `ALTER TABLE opik_traces\s+ADD COLUMN IF NOT EXISTS is_placeholder BOOLEAN NOT NULL DEFAULT false`
	if !regexp.MustCompile(pattern).MatchString(text) {
		t.Fatalf("migration 081 is missing placeholder column pattern %q", pattern)
	}
}
