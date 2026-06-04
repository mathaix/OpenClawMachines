package network

import (
	"errors"
	"fmt"
	"net"
)

// ErrNotLinux is returned when Linux-only network operations are called on other platforms.
var ErrNotLinux = errors.New("network: bridge/TAP operations require Linux")

// Bridge manages the virtual bridge network on a Host for MicroVM connectivity.
type Bridge struct {
	Name    string
	Subnet  string
	Gateway string
}

func NewBridge(name, subnet, gateway string) *Bridge {
	return &Bridge{
		Name:    name,
		Subnet:  subnet,
		Gateway: gateway,
	}
}

// DefaultBridge returns the standard bridge config used by all Hosts.
func DefaultBridge() *Bridge {
	return NewBridge("ocm-br0", "192.168.100.0/24", "192.168.100.1")
}

// AllocateIP returns the next available IP for a MicroVM.
func (b *Bridge) AllocateIP(machineIndex int) string {
	return fmt.Sprintf("192.168.100.%d", 10+machineIndex)
}

// BrowserVMIP computes the companion browser VM IP from a main VM IP.
// The browser VM gets the same IP with the last octet incremented by 100.
// For example, 192.168.100.10 → 192.168.100.110.
func BrowserVMIP(mainVMIP string) (string, error) {
	ip := net.ParseIP(mainVMIP).To4()
	if ip == nil {
		return "", fmt.Errorf("invalid IPv4 address: %s", mainVMIP)
	}
	octet := int(ip[3]) + 100
	if octet > 254 {
		return "", fmt.Errorf("browser VM IP overflow: last octet %d + 100 = %d (max 254)", ip[3], octet)
	}
	return fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], octet), nil
}
