package store

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestMigration072HasCascadeClauses locks in the ON DELETE FK clauses added
// in the browser-branch review (H8 + M1). Without these, deleting an account,
// host, or browser VM hits a FK constraint violation from dependent rows:
//   - machines.browser_vm_id → blocks DeleteBrowserVM when cleanupBrowserPairing's
//     agent-side unpair failed and left the pairing pointer set.
//   - browser_vms.account_id / host_id, browser_vm_placements.host_id →
//     block account/host teardown.
//
// This covers fresh installs (migration 072). For databases that already
// applied 072 before the fix landed, migration 073 backfills the same
// actions via ALTER TABLE — see TestMigration073BackfillsFKActions.
//
// Runs without a DB so every CI invocation catches regressions in the file.
func TestMigration072HasCascadeClauses(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "072_browser_vms.sql")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration 072 at %s: %v", path, err)
	}
	text := string(data)

	cases := []struct {
		name    string
		pattern string
	}{
		{
			name:    "browser_vms.account_id has ON DELETE CASCADE",
			pattern: `account_id\s+INT\s+NOT NULL\s+REFERENCES\s+accounts\(id\)\s+ON DELETE CASCADE`,
		},
		{
			name:    "browser_vms.host_id has ON DELETE SET NULL",
			pattern: `host_id\s+INT\s+REFERENCES\s+hosts\(id\)\s+ON DELETE SET NULL`,
		},
		{
			name:    "browser_vm_placements.host_id has ON DELETE CASCADE",
			pattern: `host_id\s+INT\s+NOT NULL\s+REFERENCES\s+hosts\(id\)\s+ON DELETE CASCADE`,
		},
		{
			name:    "machines.browser_vm_id has ON DELETE SET NULL",
			pattern: `ADD COLUMN browser_vm_id UUID REFERENCES browser_vms\(id\) ON DELETE SET NULL`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			re := regexp.MustCompile(tc.pattern)
			if !re.MatchString(text) {
				t.Fatalf("migration 072 is missing required clause — pattern %q not found", tc.pattern)
			}
		})
	}
}

// TestMigration073BackfillsFKActions guards the forward migration that
// repairs databases which applied 072 before the ON DELETE actions were
// added. Without 073, any environment that already ran 072 would still
// FK-error on DeleteBrowserVM because the edited 072 file is only used by
// fresh installs.
func TestMigration073BackfillsFKActions(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "073_browser_vm_fk_actions.sql")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration 073 at %s: %v", path, err)
	}
	text := string(data)

	cases := []struct {
		name    string
		pattern string
	}{
		{
			name:    "drops and re-adds machines_browser_vm_id_fkey with SET NULL",
			pattern: `(?s)DROP CONSTRAINT IF EXISTS machines_browser_vm_id_fkey.*ADD CONSTRAINT machines_browser_vm_id_fkey\s+FOREIGN KEY \(browser_vm_id\) REFERENCES browser_vms\(id\) ON DELETE SET NULL`,
		},
		{
			name:    "drops and re-adds browser_vms_account_id_fkey with CASCADE",
			pattern: `(?s)DROP CONSTRAINT IF EXISTS browser_vms_account_id_fkey.*ADD CONSTRAINT browser_vms_account_id_fkey\s+FOREIGN KEY \(account_id\) REFERENCES accounts\(id\) ON DELETE CASCADE`,
		},
		{
			name:    "drops and re-adds browser_vms_host_id_fkey with SET NULL",
			pattern: `(?s)DROP CONSTRAINT IF EXISTS browser_vms_host_id_fkey.*ADD CONSTRAINT browser_vms_host_id_fkey\s+FOREIGN KEY \(host_id\) REFERENCES hosts\(id\) ON DELETE SET NULL`,
		},
		{
			name:    "drops and re-adds browser_vm_placements_host_id_fkey with CASCADE",
			pattern: `(?s)DROP CONSTRAINT IF EXISTS browser_vm_placements_host_id_fkey.*ADD CONSTRAINT browser_vm_placements_host_id_fkey\s+FOREIGN KEY \(host_id\) REFERENCES hosts\(id\) ON DELETE CASCADE`,
		},
		{
			name:    "wraps the change in a transaction",
			pattern: `(?s)BEGIN;.*COMMIT;`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			re := regexp.MustCompile(tc.pattern)
			if !re.MatchString(text) {
				t.Fatalf("migration 073 is missing required clause — pattern %q not found", tc.pattern)
			}
		})
	}
}
