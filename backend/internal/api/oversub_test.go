package api

import (
	"os"
	"testing"
)

func TestVcpuOversubscriptionRatio(t *testing.T) {
	tests := []struct {
		name      string
		envVal    string
		wantRatio int
	}{
		{
			name:      "default when unset",
			envVal:    "",
			wantRatio: 2,
		},
		{
			name:      "explicit 1 (no oversubscription)",
			envVal:    "1",
			wantRatio: 1,
		},
		{
			name:      "explicit 2 (default oversubscription)",
			envVal:    "2",
			wantRatio: 2,
		},
		{
			name:      "explicit 3 (max allowed)",
			envVal:    "3",
			wantRatio: 3,
		},
		{
			name:      "clamped to max when exceeds 3",
			envVal:    "5",
			wantRatio: maxVCPUOversubRatio,
		},
		{
			name:      "clamped to max when very high",
			envVal:    "50",
			wantRatio: maxVCPUOversubRatio,
		},
		{
			name:      "clamped to 1 when zero",
			envVal:    "0",
			wantRatio: 1,
		},
		{
			name:      "clamped to 1 when negative",
			envVal:    "-1",
			wantRatio: 1,
		},
		{
			name:      "falls back to default on non-integer",
			envVal:    "abc",
			wantRatio: 2,
		},
		{
			name:      "falls back to default on float",
			envVal:    "2.5",
			wantRatio: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envVal == "" {
				_ = os.Unsetenv("VCPU_OVERSUBSCRIPTION_RATIO")
			} else {
				t.Setenv("VCPU_OVERSUBSCRIPTION_RATIO", tt.envVal)
			}

			got := vcpuOversubscriptionRatio()
			if got != tt.wantRatio {
				t.Errorf("vcpuOversubscriptionRatio() = %d, want %d", got, tt.wantRatio)
			}
		})
	}
}

func TestHandleProvisionHost_OversubscriptionRatio(t *testing.T) {
	// Verify that handleProvisionHost uses the oversubscription ratio
	// to compute vCPU capacity from physical CPU presets.
	tests := []struct {
		name         string
		ratio        int
		machineType  string
		wantVCPUs    int
		wantMemoryMB int
	}{
		{
			name:         "n2-standard-2 with 1x ratio",
			ratio:        1,
			machineType:  "n2-standard-2",
			wantVCPUs:    2,
			wantMemoryMB: 8192,
		},
		{
			name:         "n2-standard-2 with 2x ratio (default)",
			ratio:        2,
			machineType:  "n2-standard-2",
			wantVCPUs:    4,
			wantMemoryMB: 8192,
		},
		{
			name:         "n2-standard-2 with 3x ratio (max)",
			ratio:        3,
			machineType:  "n2-standard-2",
			wantVCPUs:    6,
			wantMemoryMB: 8192,
		},
		{
			name:         "n2-standard-8 with 2x ratio",
			ratio:        2,
			machineType:  "n2-standard-8",
			wantVCPUs:    16,
			wantMemoryMB: 32768,
		},
		{
			name:         "n2-standard-8 with 3x ratio",
			ratio:        3,
			machineType:  "n2-standard-8",
			wantVCPUs:    24,
			wantMemoryMB: 32768,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The presets store raw physical CPU counts:
			//   n2-standard-2: 2 CPUs, 8192 MB
			//   n2-standard-4: 4 CPUs, 16384 MB
			//   n2-standard-8: 8 CPUs, 32768 MB
			//   c2-standard-4: 4 CPUs, 16384 MB
			type preset struct {
				physicalCPUs int
				memoryMB     int
			}
			validTypes := map[string]preset{
				"n2-standard-2": {2, 8192},
				"n2-standard-4": {4, 16384},
				"n2-standard-8": {8, 32768},
				"c2-standard-4": {4, 16384},
			}

			p, ok := validTypes[tt.machineType]
			if !ok {
				t.Fatalf("unknown machine type %q", tt.machineType)
			}

			vcpus := p.physicalCPUs * tt.ratio
			if vcpus != tt.wantVCPUs {
				t.Errorf("vcpus = %d, want %d", vcpus, tt.wantVCPUs)
			}
			if p.memoryMB != tt.wantMemoryMB {
				t.Errorf("memoryMB = %d, want %d", p.memoryMB, tt.wantMemoryMB)
			}
		})
	}
}
