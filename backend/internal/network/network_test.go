package network

import (
	"testing"
)

func TestBrowserVMIP(t *testing.T) {
	tests := []struct {
		name    string
		mainIP  string
		want    string
		wantErr bool
	}{
		{name: "typical VM IP", mainIP: "192.168.100.10", want: "192.168.100.110"},
		{name: "second VM", mainIP: "192.168.100.11", want: "192.168.100.111"},
		{name: "high octet within range", mainIP: "192.168.100.154", want: "192.168.100.254"},
		{name: "overflow", mainIP: "192.168.100.155", wantErr: true},
		{name: "max overflow", mainIP: "192.168.100.254", wantErr: true},
		{name: "different subnet", mainIP: "10.0.0.5", want: "10.0.0.105"},
		{name: "invalid IP", mainIP: "not-an-ip", wantErr: true},
		{name: "empty string", mainIP: "", wantErr: true},
		{name: "IPv6 address", mainIP: "::1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BrowserVMIP(tt.mainIP)
			if tt.wantErr {
				if err == nil {
					t.Errorf("BrowserVMIP(%q) = %q, want error", tt.mainIP, got)
				}
				return
			}
			if err != nil {
				t.Errorf("BrowserVMIP(%q) error = %v, want %q", tt.mainIP, err, tt.want)
				return
			}
			if got != tt.want {
				t.Errorf("BrowserVMIP(%q) = %q, want %q", tt.mainIP, got, tt.want)
			}
		})
	}
}
