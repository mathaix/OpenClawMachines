package provisioner

import "testing"

func TestDiskSizes(t *testing.T) {
	tests := []struct {
		name     string
		memoryMB int
		wantBoot int64
		wantData int64
	}{
		{"n2-standard-2 (8GB)", 8192, 100, 30},
		{"n2-standard-4 (16GB)", 16384, 100, 50},
		{"n2-standard-8 (32GB)", 32768, 100, 90},
		{"n2-standard-16 (64GB)", 65536, 110, 170},
		{"minimum (2GB)", 2048, 100, 10},
		{"sub-minimum (1GB)", 1024, 100, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bootGB, dataGB := diskSizes(tt.memoryMB)
			if bootGB != tt.wantBoot {
				t.Errorf("bootGB = %d, want %d", bootGB, tt.wantBoot)
			}
			if dataGB != tt.wantData {
				t.Errorf("dataGB = %d, want %d", dataGB, tt.wantData)
			}
		})
	}
}
