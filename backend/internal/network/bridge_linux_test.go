//go:build linux

package network

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

type capturedCmd struct {
	name string
	args []string
}

func (c capturedCmd) String() string {
	return c.name + " " + strings.Join(c.args, " ")
}

// mockRun replaces the package run var for the test. Every call is appended
// to captured. If a key (full command string) is present in fails, that
// invocation returns the mapped error.
func mockRun(t *testing.T, captured *[]capturedCmd, fails map[string]error) {
	t.Helper()
	orig := run
	run = func(name string, args ...string) error {
		c := capturedCmd{name: name, args: append([]string(nil), args...)}
		*captured = append(*captured, c)
		if err, ok := fails[c.String()]; ok {
			return err
		}
		return nil
	}
	t.Cleanup(func() { run = orig })
}

// mockRunOutput replaces runOutput. Returns outputs[key] when the
// invocation key matches, "" otherwise.
func mockRunOutput(t *testing.T, outputs map[string]string) {
	t.Helper()
	orig := runOutput
	runOutput = func(name string, args ...string) (string, error) {
		key := name + " " + strings.Join(args, " ")
		return outputs[key], nil
	}
	t.Cleanup(func() { runOutput = orig })
}

func mockPrimaryInterface(t *testing.T, iface string) {
	t.Helper()
	orig := detectPrimaryInterface
	detectPrimaryInterface = func() (string, error) { return iface, nil }
	t.Cleanup(func() { detectPrimaryInterface = orig })
}

func TestBrowserChainName(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"550e8400-e29b-41d4-a716-446655440000", "OCM-BVM-550e8400e29b41d4a716"},
		{"aaaaaaaabbbbccccddddeeeeeeeeeeee", "OCM-BVM-aaaaaaaabbbbccccdddd"},
		{"short", "OCM-BVM-short"},
		{"", "OCM-BVM-"},
	}
	for _, tt := range tests {
		got := browserChainName(tt.id)
		if got != tt.want {
			t.Errorf("browserChainName(%q) = %q, want %q", tt.id, got, tt.want)
		}
		if len(got) > 28 {
			t.Errorf("browserChainName(%q) = %q exceeds iptables 28-char limit (got %d)", tt.id, got, len(got))
		}
		if !strings.HasPrefix(got, browserChainPrefix) {
			t.Errorf("browserChainName(%q) = %q missing prefix %q", tt.id, got, browserChainPrefix)
		}
	}
}

func TestInstallBrowserDNATChainSequence(t *testing.T) {
	var captured []capturedCmd
	mockRun(t, &captured, nil)
	mockPrimaryInterface(t, "eth0")

	b := DefaultBridge()
	err := b.InstallBrowserDNATChain("550e8400-e29b-41d4-a716-446655440000", "192.168.100.3", 56000, 56099)
	if err != nil {
		t.Fatalf("InstallBrowserDNATChain: %v", err)
	}

	chain := "OCM-BVM-550e8400e29b41d4a716"
	// Every captured command must be iptables with -w.
	for _, c := range captured {
		if c.name != "iptables" {
			t.Errorf("unexpected command %q", c.name)
		}
		if !contains(c.args, "-w") {
			t.Errorf("command missing -w: %s", c)
		}
	}

	// The install sequence (in order, at the end of captured[]) must be:
	wantInstall := [][]string{
		{"iptables", "-w", "-t", "nat", "-N", chain},
		{"iptables", "-w", "-t", "nat", "-A", chain, "-p", "udp", "--dport", "56000:56099", "-j", "DNAT", "--to-destination", "192.168.100.3"},
		{"iptables", "-w", "-t", "nat", "-A", "PREROUTING", "-j", chain},
		{"iptables", "-w", "-N", chain},
		{"iptables", "-w", "-A", chain, "-i", "eth0", "-o", "ocm-br0", "-p", "udp", "--dport", "56000:56099", "-j", "ACCEPT"},
		{"iptables", "-w", "-I", "FORWARD", "-j", chain},
	}

	if len(captured) < len(wantInstall) {
		t.Fatalf("expected at least %d commands, got %d: %v", len(wantInstall), len(captured), captured)
	}
	installStart := len(captured) - len(wantInstall)
	for i, want := range wantInstall {
		got := append([]string{captured[installStart+i].name}, captured[installStart+i].args...)
		if !equalStringSlices(got, want) {
			t.Errorf("install step %d:\n  got:  %v\n  want: %v", i, got, want)
		}
	}
}

func TestInstallBrowserDNATChainRollsBackOnFailure(t *testing.T) {
	var captured []capturedCmd
	// Make the PREROUTING jump step fail.
	fails := map[string]error{
		"iptables -w -t nat -A PREROUTING -j OCM-BVM-abc": errors.New("boom"),
	}
	mockRun(t, &captured, fails)
	mockPrimaryInterface(t, "eth0")

	b := DefaultBridge()
	err := b.InstallBrowserDNATChain("abc", "192.168.100.3", 56000, 56099)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// After failure, the nat chain must have been flushed + deleted.
	// Look for an -X OCM-BVM-abc invocation (chain deletion) in the tail.
	saw := false
	for _, c := range captured {
		if c.name == "iptables" && containsAll(c.args, []string{"-t", "nat", "-X", "OCM-BVM-abc"}) {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected rollback to call -t nat -X OCM-BVM-abc, captured: %v", captured)
	}
}

func TestRemoveBrowserDNATChainIsIdempotent(t *testing.T) {
	var captured []capturedCmd
	// Make everything succeed — teardown should swallow errors anyway.
	mockRun(t, &captured, nil)

	b := DefaultBridge()
	// Call twice; neither should panic or return error (signature returns void).
	b.RemoveBrowserDNATChain("abc")
	b.RemoveBrowserDNATChain("abc")

	// Every command must use -w.
	for _, c := range captured {
		if !contains(c.args, "-w") {
			t.Errorf("teardown missing -w: %s", c)
		}
	}

	// Teardown must touch both tables (filter and nat).
	sawFilter, sawNat := false, false
	for _, c := range captured {
		if !containsAll(c.args, []string{"-X", "OCM-BVM-abc"}) {
			continue
		}
		if contains(c.args, "-t") {
			sawNat = true
		} else {
			sawFilter = true
		}
	}
	if !sawFilter || !sawNat {
		t.Errorf("teardown did not touch both tables: filter=%v, nat=%v, cmds=%v", sawFilter, sawNat, captured)
	}
}

// TestReconcileBrowserChainsDropsOrphanChains — seeds iptables -S output with
// three OCM-BVM chains (one live, two orphan) and verifies the orphans are
// torn down while the live one is left alone.
func TestReconcileBrowserChainsDropsOrphanChains(t *testing.T) {
	liveID := "550e8400-e29b-41d4-a716-446655440000"
	orphan1 := "11111111-aaaa-bbbb-cccc-000000000001"
	orphan2 := "22222222-aaaa-bbbb-cccc-000000000002"
	liveChain := browserChainName(liveID)
	orphan1Chain := browserChainName(orphan1)
	orphan2Chain := browserChainName(orphan2)

	natOut := fmt.Sprintf(`-P PREROUTING ACCEPT
-N %s
-N %s
-N %s
-A PREROUTING -j %s
-A PREROUTING -j %s
-A PREROUTING -j %s
`, liveChain, orphan1Chain, orphan2Chain, liveChain, orphan1Chain, orphan2Chain)
	filterOut := fmt.Sprintf(`-P FORWARD ACCEPT
-N %s
-N %s
-N %s
-A FORWARD -j %s
-A FORWARD -j %s
-A FORWARD -j %s
`, liveChain, orphan1Chain, orphan2Chain, liveChain, orphan1Chain, orphan2Chain)

	mockRunOutput(t, map[string]string{
		"iptables -w -t nat -S":            natOut,
		"iptables -w -S":                   filterOut,
		"iptables -w -t nat -S PREROUTING": "-P PREROUTING ACCEPT\n",
	})

	var captured []capturedCmd
	mockRun(t, &captured, nil)

	b := DefaultBridge()
	live := map[string]bool{liveID: true}
	if err := b.ReconcileBrowserChains(live); err != nil {
		t.Fatalf("ReconcileBrowserChains: %v", err)
	}

	// Orphans must be torn down; live must not.
	for _, chain := range []string{orphan1Chain, orphan2Chain} {
		sawDelete := false
		for _, c := range captured {
			if c.name == "iptables" && containsAll(c.args, []string{"-X", chain}) {
				sawDelete = true
				break
			}
		}
		if !sawDelete {
			t.Errorf("orphan chain %s was not deleted", chain)
		}
	}
	for _, c := range captured {
		if c.name == "iptables" && containsAll(c.args, []string{"-X", liveChain}) {
			t.Errorf("live chain %s should not be deleted", liveChain)
		}
	}
}

// TestReconcileBrowserChainsDeletesLegacyRawRules — seeds a raw PREROUTING
// DNAT rule targeting a bridge IP (not wrapped in any chain). The reconcile
// sweep must delete it. A DNAT to a non-bridge IP must be left alone.
func TestReconcileBrowserChainsDeletesLegacyRawRules(t *testing.T) {
	preroutingOut := `-P PREROUTING ACCEPT
-A PREROUTING -p udp -m udp --dport 56000:56099 -j DNAT --to-destination 192.168.100.3
-A PREROUTING -p udp -m udp --dport 56000:56100 -j DNAT --to-destination 192.168.100.2
-A PREROUTING -p tcp -m tcp --dport 8080 -j DNAT --to-destination 10.0.0.5:80
`
	mockRunOutput(t, map[string]string{
		"iptables -w -t nat -S":            "-P PREROUTING ACCEPT\n",
		"iptables -w -S":                   "-P FORWARD ACCEPT\n",
		"iptables -w -t nat -S PREROUTING": preroutingOut,
	})

	var captured []capturedCmd
	mockRun(t, &captured, nil)

	b := DefaultBridge()
	if err := b.ReconcileBrowserChains(map[string]bool{}); err != nil {
		t.Fatalf("ReconcileBrowserChains: %v", err)
	}

	// Expect exactly two -D PREROUTING calls, one per legacy bridge-subnet DNAT.
	deletes := 0
	sawForeign := false
	for _, c := range captured {
		if c.name != "iptables" {
			continue
		}
		if !containsAll(c.args, []string{"-D", "PREROUTING"}) {
			continue
		}
		if contains(c.args, "10.0.0.5:80") {
			sawForeign = true
		}
		if contains(c.args, "192.168.100.3") || contains(c.args, "192.168.100.2") {
			deletes++
		}
	}
	if deletes != 2 {
		t.Errorf("expected 2 legacy DNAT deletes, got %d (captured=%v)", deletes, captured)
	}
	if sawForeign {
		t.Errorf("foreign-subnet DNAT should not be deleted")
	}
}

func TestReconcileBrowserChainsIgnoresNonBrowserChains(t *testing.T) {
	natOut := `-P PREROUTING ACCEPT
-N DOCKER
-N LIBVIRT_PRT
-A PREROUTING -j DOCKER
`
	mockRunOutput(t, map[string]string{
		"iptables -w -t nat -S":            natOut,
		"iptables -w -S":                   "-P FORWARD ACCEPT\n-N DOCKER-USER\n",
		"iptables -w -t nat -S PREROUTING": "-P PREROUTING ACCEPT\n",
	})
	var captured []capturedCmd
	mockRun(t, &captured, nil)

	b := DefaultBridge()
	if err := b.ReconcileBrowserChains(map[string]bool{}); err != nil {
		t.Fatalf("ReconcileBrowserChains: %v", err)
	}

	for _, c := range captured {
		if c.name == "iptables" && (containsAll(c.args, []string{"-X", "DOCKER"}) || containsAll(c.args, []string{"-X", "DOCKER-USER"}) || containsAll(c.args, []string{"-X", "LIBVIRT_PRT"})) {
			t.Errorf("reconcile should not touch non-OCM chains: %s", c)
		}
	}
}

// Helpers

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}

// containsAll returns true if subset appears as a contiguous sub-slice of xs.
func containsAll(xs []string, subset []string) bool {
	if len(subset) == 0 {
		return true
	}
	for i := 0; i+len(subset) <= len(xs); i++ {
		match := true
		for j := range subset {
			if xs[i+j] != subset[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
