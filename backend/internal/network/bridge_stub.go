//go:build !linux

package network

// Setup is a no-op on non-Linux platforms.
func (b *Bridge) Setup() error {
	return ErrNotLinux
}

// CreateTap is a no-op on non-Linux platforms.
func (b *Bridge) CreateTap(tapName string) error {
	return ErrNotLinux
}

// RemoveTap is a no-op on non-Linux platforms.
func (b *Bridge) RemoveTap(tapName string) error {
	return ErrNotLinux
}

// AllowVMPair is a no-op on non-Linux platforms.
func (b *Bridge) AllowVMPair(ip1, ip2 string) error {
	return ErrNotLinux
}

// RemoveVMPair is a no-op on non-Linux platforms.
func (b *Bridge) RemoveVMPair(ip1, ip2 string) {}

// SetupNAT is a no-op on non-Linux platforms.
func (b *Bridge) SetupNAT() error {
	return ErrNotLinux
}

// TeardownNAT is a no-op on non-Linux platforms.
func (b *Bridge) TeardownNAT() error {
	return ErrNotLinux
}

// InstallBrowserDNATChain is a no-op on non-Linux platforms.
func (b *Bridge) InstallBrowserDNATChain(browserVMID, targetIP string, portStart, portEnd int) error {
	return ErrNotLinux
}

// RemoveBrowserDNATChain is a no-op on non-Linux platforms.
func (b *Bridge) RemoveBrowserDNATChain(browserVMID string) {}

// ReconcileBrowserChains is a no-op on non-Linux platforms.
func (b *Bridge) ReconcileBrowserChains(liveBrowserVMIDs map[string]bool) error {
	return nil
}
