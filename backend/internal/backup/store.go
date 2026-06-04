package backup

import (
	"context"
	"io"
	"time"
)

// BackupInfo contains metadata about a completed backup.
type BackupInfo struct {
	GCSPath         string
	SizeBytes       int64
	CompressedBytes int64
	ChecksumSHA256  string
	HMAC            []byte
	Nonce           []byte
	Timestamp       time.Time
}

// BackupStore handles backup upload/download to cloud storage.
type BackupStore interface {
	// Upload compresses, encrypts, and uploads a data volume to GCS.
	// dataVolumePath is the local path to the ext4 file.
	// Returns metadata about the uploaded backup.
	Upload(ctx context.Context, machineID string, dataVolumePath string, encryptionKey []byte) (*BackupInfo, error)

	// Download downloads, decrypts, and decompresses a backup from GCS to a local path.
	Download(ctx context.Context, gcsPath string, destPath string, encryptionKey, nonce, expectedHMAC []byte) error

	// StreamTarGz downloads a backup, decrypts, decompresses, mounts the ext4, and streams as tar.gz.
	StreamTarGz(ctx context.Context, gcsPath string, encryptionKey, nonce, expectedHMAC []byte, w io.Writer) error

	// StreamDecrypted downloads, decrypts, and decompresses a backup, streaming the raw ext4 to w.
	// Unlike StreamTarGz, this does NOT mount the filesystem — works without CAP_SYS_ADMIN.
	StreamDecrypted(ctx context.Context, gcsPath string, encryptionKey, nonce, expectedHMAC []byte, w io.Writer) error

	// Delete removes a backup from GCS.
	Delete(ctx context.Context, gcsPath string) error
}
