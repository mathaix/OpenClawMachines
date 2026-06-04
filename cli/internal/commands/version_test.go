package commands

import (
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		gitCommit string
		buildTime string
		wantParts []string
	}{
		{
			name:      "default dev version",
			version:   "dev",
			gitCommit: "unknown",
			buildTime: "unknown",
			wantParts: []string{"ocm version dev", "commit:  unknown", "built:   unknown"},
		},
		{
			name:      "injected version",
			version:   "abc1234-20260224T120000Z",
			gitCommit: "abc1234",
			buildTime: "20260224T120000Z",
			wantParts: []string{"ocm version abc1234-20260224T120000Z", "commit:  abc1234", "built:   20260224T120000Z"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore version vars
			oldVersion, oldCommit, oldBuild := Version, GitCommit, BuildTime
			Version = tt.version
			GitCommit = tt.gitCommit
			BuildTime = tt.buildTime
			defer func() {
				Version = oldVersion
				GitCommit = oldCommit
				BuildTime = oldBuild
			}()

			output, err := executeCommand("version")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			for _, part := range tt.wantParts {
				if !strings.Contains(output, part) {
					t.Errorf("output missing %q\nGot: %s", part, output)
				}
			}
		})
	}
}
