package gcsutil

import (
	"fmt"
	"strings"
)

// ParseGCSURI splits a gs://bucket/path URI into bucket and object components.
func ParseGCSURI(uri string) (bucket, object string, err error) {
	if !strings.HasPrefix(uri, "gs://") {
		return "", "", fmt.Errorf("invalid GCS URI (must start with gs://): %s", uri)
	}
	trimmed := strings.TrimPrefix(uri, "gs://")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid GCS URI (must be gs://bucket/path): %s", uri)
	}
	return parts[0], parts[1], nil
}
