package selfupdate

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/mathaix/openclawmachines/backend/pkg/gcsutil"
)

// TrustedBucket is the only GCS bucket the agent will download binaries from.
const TrustedBucket = "openclawmachines"

// Manifest describes an agent binary stored in GCS.
type Manifest struct {
	Version         string    `json:"version"`
	SHA256          string    `json:"sha256"`
	SizeBytes       int64     `json:"size_bytes"`
	URL             string    `json:"url"`
	BuiltAt         time.Time `json:"built_at"`
	OpenClawVersion string    `json:"openclaw_version,omitempty"`
}

// ParseManifest reads and validates a manifest from the given reader.
func ParseManifest(r io.Reader) (*Manifest, error) {
	var m Manifest
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if m.Version == "" {
		return nil, fmt.Errorf("manifest missing required field: version")
	}
	if m.SHA256 == "" {
		return nil, fmt.Errorf("manifest missing required field: sha256")
	}
	if err := validateSHA256(m.SHA256); err != nil {
		return nil, fmt.Errorf("manifest has invalid sha256: %w", err)
	}
	if m.URL == "" {
		return nil, fmt.Errorf("manifest missing required field: url")
	}
	if err := validateTrustedURL(m.URL); err != nil {
		return nil, err
	}
	return &m, nil
}

// validateSHA256 checks that s is a valid hex-encoded SHA256 hash (64 chars).
func validateSHA256(s string) error {
	if len(s) != 64 {
		return fmt.Errorf("expected 64 hex chars, got %d", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return fmt.Errorf("not valid hex: %w", err)
	}
	return nil
}

// validateTrustedURL ensures the manifest URL points to the trusted GCS bucket.
func validateTrustedURL(url string) error {
	if !strings.HasPrefix(url, "gs://") {
		return fmt.Errorf("manifest url must be a gs:// URI: %s", url)
	}
	bucket, _, err := gcsutil.ParseGCSURI(url)
	if err != nil {
		return fmt.Errorf("manifest url invalid: %w", err)
	}
	if bucket != TrustedBucket {
		return fmt.Errorf("manifest url points to untrusted bucket %q (expected %q)", bucket, TrustedBucket)
	}
	return nil
}

// FetchManifest downloads and parses a manifest from GCS.
func FetchManifest(ctx context.Context, client *storage.Client, manifestURI string) (*Manifest, error) {
	bucket, object, err := gcsutil.ParseGCSURI(manifestURI)
	if err != nil {
		return nil, err
	}

	r, err := client.Bucket(bucket).Object(object).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("open manifest object: %w", err)
	}
	defer func() { _ = r.Close() }()

	return ParseManifest(r)
}
