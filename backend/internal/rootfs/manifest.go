package rootfs

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Manifest describes a rootfs image stored in GCS.
type Manifest struct {
	Version             string    `json:"version"`
	SHA256              string    `json:"sha256"`
	SizeBytes           int64     `json:"size_bytes"`
	CompressedSizeBytes int64     `json:"compressed_size_bytes"`
	URL                 string    `json:"url"`
	BuiltAt             time.Time `json:"built_at"`
	Compression         string    `json:"compression"`
	OpenClawVersion     string    `json:"openclaw_version"`
	AgentVersion        string    `json:"agent_version"`
	DataVersion         int       `json:"data_version"`
}

// ParseManifest reads and validates a manifest from the given reader.
func ParseManifest(r io.Reader) (*Manifest, error) {
	var m Manifest
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if m.SHA256 == "" {
		return nil, fmt.Errorf("manifest missing required field: sha256")
	}
	if m.URL == "" {
		return nil, fmt.Errorf("manifest missing required field: url")
	}
	if m.Compression == "" {
		m.Compression = "zstd"
	}
	return &m, nil
}
