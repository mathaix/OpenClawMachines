//go:build linux

package network

import (
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"strings"
)

// Setup creates the bridge interface, assigns the gateway IP, and enables it.
func (b *Bridge) Setup() error {
	slog.Info("bridge.setup.starting",
		"bridge_name", b.Name,
		"subnet", b.Subnet,
		"gateway", b.Gateway)

	// Check if bridge already exists
	if err := run("ip", "link", "show", b.Name); err == nil {
		slog.Info("bridge.already_exists", "bridge_name", b.Name)
		return nil
	}

	// Create bridge
	if err := run("ip", "link", "add", b.Name, "type", "bridge"); err != nil {
		return fmt.Errorf("create bridge: %w", err)
	}

	// Assign gateway IP
	gatewayWithMask := b.Gateway + "/24"
	if err := run("ip", "addr", "add", gatewayWithMask, "dev", b.Name); err != nil {
		return fmt.Errorf("assign bridge IP: %w", err)
	}

	// Bring bridge up
	if err := run("ip", "link", "set", b.Name, "up"); err != nil {
		return fmt.Errorf("bring bridge up: %w", err)
	}

	slog.Info("bridge.setup.completed", "bridge_name", b.Name, "gateway", b.Gateway)
	return nil
}

// CreateTap creates a TAP device for a MicroVM and attaches it to the bridge.
func (b *Bridge) CreateTap(tapName string) error {
	slog.Info("tap.creating", "tap_name", tapName, "bridge_name", b.Name)

	if err := run("ip", "tuntap", "add", "dev", tapName, "mode", "tap"); err != nil {
		return fmt.Errorf("create tap: %w", err)
	}
	if err := run("ip", "link", "set", tapName, "master", b.Name); err != nil {
		return fmt.Errorf("attach tap to bridge: %w", err)
	}
	if err := run("ip", "link", "set", tapName, "up"); err != nil {
		return fmt.Errorf("bring tap up: %w", err)
	}

	return nil
}

// RemoveTap removes a TAP device.
func (b *Bridge) RemoveTap(tapName string) error {
	slog.Info("tap.removing", "tap_name", tapName)

	if err := run("ip", "link", "del", tapName); err != nil {
		return fmt.Errorf("remove tap: %w", err)
	}
	return nil
}

// ListTaps returns the names of all TAP devices attached to this bridge.
func (b *Bridge) ListTaps() ([]string, error) {
	out, err := exec.Command("ip", "-o", "link", "show", "master", b.Name, "type", "tun").Output()
	if err != nil {
		return nil, fmt.Errorf("list taps on %s: %w", b.Name, err)
	}
	var taps []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// Format: "8: tap69e0d67f-e0: <FLAGS> ..."
		parts := strings.SplitN(line, ":", 3)
		if len(parts) >= 2 {
			name := strings.TrimSpace(parts[1])
			if strings.HasPrefix(name, "tap") {
				taps = append(taps, name)
			}
		}
	}
	return taps, nil
}

// SetupNAT configures IP forwarding, masquerading, and security rules.
// Blocks GCP metadata access (169.254.169.254), VM-to-VM lateral movement,
// and VM access to host agent API ports (9090/9091).
func (b *Bridge) SetupNAT() error {
	slog.Info("nat.setup.starting", "bridge_name", b.Name)

	primaryIface, err := DetectPrimaryInterface()
	if err != nil {
		return fmt.Errorf("detect primary interface: %w", err)
	}

	// Enable IP forwarding
	if err := run("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return fmt.Errorf("enable ip forwarding: %w", err)
	}

	// POSTROUTING masquerade — VMs can reach the internet
	if err := run("iptables", "-t", "nat", "-A", "POSTROUTING",
		"-s", b.Subnet, "-o", primaryIface, "-j", "MASQUERADE"); err != nil {
		return fmt.Errorf("nat masquerade: %w", err)
	}

	// Block GCP metadata access from VMs (security: prevent SSRF to instance metadata)
	if err := run("iptables", "-I", "FORWARD",
		"-s", b.Subnet, "-d", "169.254.169.254", "-j", "DROP"); err != nil {
		return fmt.Errorf("block metadata access: %w", err)
	}

	// Block VM-to-VM lateral movement (each VM should only talk to host/internet)
	if err := run("iptables", "-I", "FORWARD",
		"-i", b.Name, "-o", b.Name, "-j", "DROP"); err != nil {
		return fmt.Errorf("block vm-to-vm: %w", err)
	}

	// Block VMs from reaching host agent API ports (control + proxy)
	// Without these rules, a compromised VM could attack the agent directly.
	for _, port := range []string{"9090", "9091"} {
		if err := run("iptables", "-I", "INPUT",
			"-i", b.Name, "-p", "tcp", "--dport", port, "-j", "DROP"); err != nil {
			return fmt.Errorf("block vm access to port %s: %w", port, err)
		}
	}

	// Allow established connections back through
	if err := run("iptables", "-A", "FORWARD",
		"-i", primaryIface, "-o", b.Name,
		"-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
		return fmt.Errorf("allow established: %w", err)
	}

	// Allow VMs to reach the internet
	if err := run("iptables", "-A", "FORWARD",
		"-i", b.Name, "-o", primaryIface, "-j", "ACCEPT"); err != nil {
		return fmt.Errorf("allow outbound: %w", err)
	}

	slog.Info("nat.setup.completed", "primary_iface", primaryIface, "subnet", b.Subnet)
	return nil
}

// AllowVMPair inserts iptables rules to allow bidirectional traffic between two VMs
// on the bridge. Must be called BEFORE the blanket vm-to-vm DROP rule takes effect
// (uses -I to insert at the top of the FORWARD chain).
func (b *Bridge) AllowVMPair(ip1, ip2 string) error {
	slog.Info("bridge.allow_vm_pair", "ip1", ip1, "ip2", ip2, "bridge", b.Name)
	if err := run("iptables", "-I", "FORWARD",
		"-s", ip1, "-d", ip2, "-i", b.Name, "-o", b.Name, "-j", "ACCEPT"); err != nil {
		return fmt.Errorf("allow %s -> %s: %w", ip1, ip2, err)
	}
	if err := run("iptables", "-I", "FORWARD",
		"-s", ip2, "-d", ip1, "-i", b.Name, "-o", b.Name, "-j", "ACCEPT"); err != nil {
		return fmt.Errorf("allow %s -> %s: %w", ip2, ip1, err)
	}
	return nil
}

// RemoveVMPair removes the iptables rules created by AllowVMPair.
func (b *Bridge) RemoveVMPair(ip1, ip2 string) {
	slog.Info("bridge.remove_vm_pair", "ip1", ip1, "ip2", ip2, "bridge", b.Name)
	_ = run("iptables", "-D", "FORWARD",
		"-s", ip1, "-d", ip2, "-i", b.Name, "-o", b.Name, "-j", "ACCEPT")
	_ = run("iptables", "-D", "FORWARD",
		"-s", ip2, "-d", ip1, "-i", b.Name, "-o", b.Name, "-j", "ACCEPT")
}

// browserChainPrefix identifies iptables chains owned by browser VM WebRTC
// DNAT programming. The suffix is a shortened browser-VM UUID; see
// browserChainName.
const browserChainPrefix = "OCM-BVM-"

// browserChainMaxUUIDLen is the number of hex characters we keep from the
// browser-VM UUID when forming the chain name. iptables limits chain names
// to 28 characters (XT_EXTENSION_MAXNAMELEN minus null terminator), and the
// "OCM-BVM-" prefix consumes 8, leaving 20 characters for the identifier —
// 80 bits of entropy, ample to avoid collisions inside the per-host
// browserWebRTCMaxSlots budget.
const browserChainMaxUUIDLen = 20

// browserChainName returns the iptables chain name for a browser VM's WebRTC
// DNAT programming. Chain names live in a single flat namespace per table,
// but the same name can safely be reused in nat and filter (different tables).
func browserChainName(browserVMID string) string {
	shortened := strings.ReplaceAll(browserVMID, "-", "")
	if len(shortened) > browserChainMaxUUIDLen {
		shortened = shortened[:browserChainMaxUUIDLen]
	}
	return browserChainPrefix + shortened
}

// InstallBrowserDNATChain creates a per-VM iptables chain in both the nat
// and filter tables, wires the PREROUTING/FORWARD jumps, and installs the
// UDP DNAT + ACCEPT rules that forward WebRTC media from the host's public
// IP range to the browser VM's bridge IP. Idempotent: any leftover state
// from a previous install (crash, replayed recovery, re-enrollment) is torn
// down before the fresh install proceeds.
//
// Every iptables invocation uses -w (wait for xtables-lock) so concurrent
// bridge mutations serialize correctly instead of racing.
//
// On failure partway through, the partial state is torn down before
// returning, so callers see either "fully installed" or "nothing installed".
func (b *Bridge) InstallBrowserDNATChain(browserVMID, targetIP string, portStart, portEnd int) error {
	chain := browserChainName(browserVMID)
	portRange := fmt.Sprintf("%d:%d", portStart, portEnd)
	primaryIface, err := detectPrimaryInterface()
	if err != nil {
		return fmt.Errorf("detect primary interface: %w", err)
	}

	slog.Info("bridge.browser_dnat.install",
		"browser_vm_id", browserVMID,
		"chain", chain,
		"target_ip", targetIP,
		"port_range", portRange,
		"primary_iface", primaryIface)

	// Clear any stale state so the fresh -N succeeds on a redeployed host.
	b.tearDownBrowserChain(chain)

	rollback := func(stage string, cause error) error {
		b.tearDownBrowserChain(chain)
		return fmt.Errorf("%s: %w", stage, cause)
	}

	// nat/<chain>: DNAT rule, then jump PREROUTING -> chain.
	if err := run("iptables", "-w", "-t", "nat", "-N", chain); err != nil {
		return fmt.Errorf("create nat chain %s: %w", chain, err)
	}
	if err := run("iptables", "-w", "-t", "nat", "-A", chain,
		"-p", "udp", "--dport", portRange,
		"-j", "DNAT", "--to-destination", targetIP); err != nil {
		return rollback("add DNAT rule", err)
	}
	if err := run("iptables", "-w", "-t", "nat", "-A", "PREROUTING", "-j", chain); err != nil {
		return rollback("jump PREROUTING", err)
	}

	// filter/<chain>: ACCEPT rule, then jump FORWARD -> chain (inserted at
	// the top of FORWARD so it precedes the bridge-wide DROP rules).
	if err := run("iptables", "-w", "-N", chain); err != nil {
		return rollback("create filter chain", err)
	}
	if err := run("iptables", "-w", "-A", chain,
		"-i", primaryIface, "-o", b.Name,
		"-p", "udp", "--dport", portRange,
		"-j", "ACCEPT"); err != nil {
		return rollback("add FORWARD accept", err)
	}
	if err := run("iptables", "-w", "-I", "FORWARD", "-j", chain); err != nil {
		return rollback("jump FORWARD", err)
	}
	return nil
}

// RemoveBrowserDNATChain is the idempotent teardown for
// InstallBrowserDNATChain. All six teardown commands are issued
// unconditionally; missing rules/chains are swallowed because iptables
// state may have been partially torn down already (previous failed
// install, prior destroy, reconcile sweep).
func (b *Bridge) RemoveBrowserDNATChain(browserVMID string) {
	chain := browserChainName(browserVMID)
	slog.Info("bridge.browser_dnat.remove",
		"browser_vm_id", browserVMID,
		"chain", chain)
	b.tearDownBrowserChain(chain)
}

// tearDownBrowserChain deletes the PREROUTING/FORWARD jumps and flushes +
// deletes the chain in both filter and nat tables. Errors are swallowed
// (best-effort); the caller always sees a "gone" outcome.
func (b *Bridge) tearDownBrowserChain(chain string) {
	// filter table first (reverse of install order)
	_ = run("iptables", "-w", "-D", "FORWARD", "-j", chain)
	_ = run("iptables", "-w", "-F", chain)
	_ = run("iptables", "-w", "-X", chain)
	// nat table
	_ = run("iptables", "-w", "-t", "nat", "-D", "PREROUTING", "-j", chain)
	_ = run("iptables", "-w", "-t", "nat", "-F", chain)
	_ = run("iptables", "-w", "-t", "nat", "-X", chain)
}

// ReconcileBrowserChains drops orphan OCM-BVM-* chains whose corresponding
// browser VM is no longer live and deletes legacy raw DNAT rules (pre-chain
// era) targeting the bridge subnet that are not wrapped in an OCM-BVM chain.
// Same for matching filter/FORWARD ACCEPTs.
//
// liveBrowserVMIDs is keyed by full browser-VM UUID; the reconciler maps
// UUIDs through browserChainName to match the names actually in iptables.
//
// Must be called during orchestrator init before any concurrent browser-VM
// create/destroy is in flight, since there is no lock coordinating the
// sweep with CreateBrowserVM.
func (b *Bridge) ReconcileBrowserChains(liveBrowserVMIDs map[string]bool) error {
	liveChains := make(map[string]bool, len(liveBrowserVMIDs))
	for id := range liveBrowserVMIDs {
		liveChains[browserChainName(id)] = true
	}

	orphansNat, err := listOrphanBrowserChains("nat", liveChains)
	if err != nil {
		return fmt.Errorf("list nat chains: %w", err)
	}
	orphansFilter, err := listOrphanBrowserChains("", liveChains)
	if err != nil {
		return fmt.Errorf("list filter chains: %w", err)
	}
	union := make(map[string]bool, len(orphansNat)+len(orphansFilter))
	for c := range orphansNat {
		union[c] = true
	}
	for c := range orphansFilter {
		union[c] = true
	}
	for chain := range union {
		slog.Info("bridge.browser_dnat.reconcile.orphan_chain", "chain", chain)
		b.tearDownBrowserChain(chain)
	}

	if err := b.removeLegacyBridgeDNATRules(); err != nil {
		return fmt.Errorf("remove legacy rules: %w", err)
	}
	return nil
}

// listOrphanBrowserChains returns OCM-BVM-* chains in the given table
// (empty string means filter) that do not correspond to any live VM.
func listOrphanBrowserChains(table string, liveChains map[string]bool) (map[string]bool, error) {
	args := []string{"-w"}
	if table != "" {
		args = append(args, "-t", table)
	}
	args = append(args, "-S")
	out, err := runOutput("iptables", args...)
	if err != nil {
		return nil, err
	}
	orphans := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "-N" {
			continue
		}
		name := fields[1]
		if !strings.HasPrefix(name, browserChainPrefix) {
			continue
		}
		if liveChains[name] {
			continue
		}
		orphans[name] = true
	}
	return orphans, nil
}

// removeLegacyBridgeDNATRules deletes raw DNAT rules in nat/PREROUTING that
// target an address inside the bridge subnet without being wrapped in an
// OCM-BVM-* chain. The new code always installs DNAT inside a chain, so an
// unwrapped bridge-subnet DNAT is pre-chain sediment by construction.
//
// For each such rule, the matching filter/FORWARD ACCEPT (identified by the
// same port range and bridge interface) is also removed best-effort.
func (b *Bridge) removeLegacyBridgeDNATRules() error {
	_, ipNet, err := net.ParseCIDR(b.Subnet)
	if err != nil {
		return fmt.Errorf("parse subnet %s: %w", b.Subnet, err)
	}
	out, err := runOutput("iptables", "-w", "-t", "nat", "-S", "PREROUTING")
	if err != nil {
		return err
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "-A" || fields[1] != "PREROUTING" {
			continue
		}
		// Jump-to-OCM-BVM chains are wrapped legitimately; skip.
		if idx := indexOf(fields, "-j"); idx >= 0 && idx+1 < len(fields) && strings.HasPrefix(fields[idx+1], browserChainPrefix) {
			continue
		}
		// Only DNAT rules matter here.
		if !containsString(fields, "DNAT") {
			continue
		}
		dest := extractDNATDestination(fields)
		if dest == "" {
			continue
		}
		// Destination may carry a :port suffix (TCP); keep only the address part.
		host := dest
		if idx := strings.Index(dest, ":"); idx >= 0 {
			host = dest[:idx]
		}
		targetIP := net.ParseIP(host)
		if targetIP == nil || !ipNet.Contains(targetIP) {
			continue
		}

		slog.Info("bridge.browser_dnat.reconcile.legacy_nat_rule",
			"dest", dest, "raw", line)

		deleteArgs := append([]string{"-w", "-t", "nat"}, rewriteAppendToDelete(fields)...)
		if err := run("iptables", deleteArgs...); err != nil {
			slog.Warn("bridge.browser_dnat.reconcile.delete_failed",
				"error", err, "line", line)
			continue
		}

		portRange := extractDPort(fields)
		if portRange != "" {
			b.removeLegacyFilterForwardAccept(portRange)
		}
	}
	return nil
}

// removeLegacyFilterForwardAccept deletes any filter/FORWARD rule matching
// the shape AllowUDPPortRangeDNAT used to install — `-i <primary> -o
// <bridge> -p udp --dport <range> -j ACCEPT` — independent of whether a
// chain wraps it. Best-effort; called after a matching raw DNAT is removed.
func (b *Bridge) removeLegacyFilterForwardAccept(portRange string) {
	primaryIface, err := detectPrimaryInterface()
	if err != nil {
		return
	}
	_ = run("iptables", "-w", "-D", "FORWARD",
		"-i", primaryIface, "-o", b.Name,
		"-p", "udp", "--dport", portRange,
		"-j", "ACCEPT")
}

// extractDNATDestination returns the argument of --to-destination, or "".
func extractDNATDestination(fields []string) string {
	for i, f := range fields {
		if f == "--to-destination" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// extractDPort returns the argument of --dport, or "".
func extractDPort(fields []string) string {
	for i, f := range fields {
		if f == "--dport" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// rewriteAppendToDelete flips the -A prefix of a rule spec to -D so the
// same args stream can be passed to iptables to remove the rule that was
// printed by iptables-save / iptables -S.
func rewriteAppendToDelete(fields []string) []string {
	out := make([]string, len(fields))
	copy(out, fields)
	if len(out) > 0 && out[0] == "-A" {
		out[0] = "-D"
	}
	return out
}

func indexOf(xs []string, target string) int {
	for i, x := range xs {
		if x == target {
			return i
		}
	}
	return -1
}

func containsString(xs []string, target string) bool {
	return indexOf(xs, target) >= 0
}

// TeardownNAT removes all iptables rules created by SetupNAT for this bridge.
func (b *Bridge) TeardownNAT() error {
	slog.Info("nat.teardown.starting", "bridge_name", b.Name)

	primaryIface, err := DetectPrimaryInterface()
	if err != nil {
		return fmt.Errorf("detect primary interface: %w", err)
	}

	// Remove in reverse order of creation. Ignore errors since rules may
	// already have been removed (e.g. after a crash).

	// FORWARD allow outbound
	_ = run("iptables", "-D", "FORWARD",
		"-i", b.Name, "-o", primaryIface, "-j", "ACCEPT")

	// FORWARD allow established
	_ = run("iptables", "-D", "FORWARD",
		"-i", primaryIface, "-o", b.Name,
		"-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")

	// INPUT port blocks
	for _, port := range []string{"9090", "9091"} {
		_ = run("iptables", "-D", "INPUT",
			"-i", b.Name, "-p", "tcp", "--dport", port, "-j", "DROP")
	}

	// FORWARD vm-to-vm block
	_ = run("iptables", "-D", "FORWARD",
		"-i", b.Name, "-o", b.Name, "-j", "DROP")

	// FORWARD GCP metadata block
	_ = run("iptables", "-D", "FORWARD",
		"-s", b.Subnet, "-d", "169.254.169.254", "-j", "DROP")

	// POSTROUTING masquerade
	_ = run("iptables", "-t", "nat", "-D", "POSTROUTING",
		"-s", b.Subnet, "-o", primaryIface, "-j", "MASQUERADE")

	slog.Info("nat.teardown.completed", "bridge_name", b.Name)
	return nil
}

// DetectPrimaryInterface returns the name of the primary network interface.
func DetectPrimaryInterface() (string, error) {
	out, err := exec.Command("ip", "route", "show", "default").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ip route: %w", err)
	}

	// Parse "default via X.X.X.X dev eth0 ..."
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("could not detect primary interface from: %s", string(out))
}

// run executes a command. Variable-backed so tests can substitute it.
var run = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s: %w", name, strings.Join(args, " "), string(out), err)
	}
	return nil
}

// runOutput executes a command and returns its combined stdout+stderr.
// Variable-backed so tests can substitute it.
var runOutput = func(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %s: %w", name, strings.Join(args, " "), string(out), err)
	}
	return string(out), nil
}

// detectPrimaryInterface is a package variable so tests can substitute it
// without running `ip route`.
var detectPrimaryInterface = DetectPrimaryInterface
