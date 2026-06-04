package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
)

// GCSStore implements BackupStore using Google Cloud Storage.
type GCSStore struct {
	client *storage.Client
	bucket string
	prefix string // e.g. "backups"
}

// NewGCSStore creates a new GCS-backed backup store.
func NewGCSStore(client *storage.Client, bucket, prefix string) *GCSStore {
	return &GCSStore{client: client, bucket: bucket, prefix: prefix}
}

func (s *GCSStore) gcsPath(machineID string, ts time.Time) string {
	return fmt.Sprintf("%s/%s/%s.ext4.zst.enc", s.prefix, machineID, ts.UTC().Format("20060102T150405Z"))
}

func (s *GCSStore) Upload(ctx context.Context, machineID string, dataVolumePath string, encryptionKey []byte) (*BackupInfo, error) {
	fi, err := os.Stat(dataVolumePath)
	if err != nil {
		return nil, fmt.Errorf("stat data volume: %w", err)
	}

	ts := time.Now()
	objPath := s.gcsPath(machineID, ts)

	slog.Info("backup.upload.start", "machine_id", machineID, "gcs_path", objPath, "size_bytes", fi.Size())

	f, err := os.Open(dataVolumePath)
	if err != nil {
		return nil, fmt.Errorf("open data volume: %w", err)
	}
	defer func() { _ = f.Close() }()

	hasher := sha256.New()
	teeReader := io.TeeReader(f, hasher)

	// Compress with zstd via pipe
	pr, pw := io.Pipe()
	zstdEncoder, err := zstd.NewWriter(pw, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, fmt.Errorf("create zstd encoder: %w", err)
	}

	var compressErr error
	go func() {
		defer func() { _ = pw.Close() }()
		if _, err := io.Copy(zstdEncoder, teeReader); err != nil {
			compressErr = err
			pw.CloseWithError(err)
			return
		}
		if err := zstdEncoder.Close(); err != nil {
			compressErr = err
			pw.CloseWithError(err)
		}
	}()

	// Encrypt compressed stream and upload to GCS
	obj := s.client.Bucket(s.bucket).Object(objPath)
	gcsWriter := obj.NewWriter(ctx)
	gcsWriter.ContentType = "application/octet-stream"

	nonce, hmacHash, err := StreamEncrypt(pr, gcsWriter, encryptionKey)
	if err != nil {
		_ = gcsWriter.Close()
		return nil, fmt.Errorf("encrypt and upload: %w", err)
	}

	if err := gcsWriter.Close(); err != nil {
		return nil, fmt.Errorf("close GCS writer: %w", err)
	}
	if compressErr != nil {
		return nil, fmt.Errorf("compress: %w", compressErr)
	}

	attrs, err := obj.Attrs(ctx)
	if err != nil {
		return nil, fmt.Errorf("get object attrs: %w", err)
	}

	info := &BackupInfo{
		GCSPath:         objPath,
		SizeBytes:       fi.Size(),
		CompressedBytes: attrs.Size,
		ChecksumSHA256:  hex.EncodeToString(hasher.Sum(nil)),
		HMAC:            hmacHash,
		Nonce:           nonce,
		Timestamp:       ts,
	}

	slog.Info("backup.upload.complete", "machine_id", machineID, "gcs_path", objPath,
		"size_bytes", fi.Size(), "compressed_bytes", attrs.Size)

	return info, nil
}

func (s *GCSStore) Download(ctx context.Context, gcsPath string, destPath string, encryptionKey, nonce, expectedHMAC []byte) error {
	slog.Info("backup.download.start", "gcs_path", gcsPath, "dest", destPath)

	obj := s.client.Bucket(s.bucket).Object(gcsPath)
	reader, err := obj.NewReader(ctx)
	if err != nil {
		return fmt.Errorf("open GCS object: %w", err)
	}
	defer func() { _ = reader.Close() }()

	// Decrypt
	decPR, decPW := io.Pipe()
	var decryptErr error
	go func() {
		defer func() { _ = decPW.Close() }()
		if err := StreamDecrypt(reader, decPW, encryptionKey, nonce, expectedHMAC); err != nil {
			decryptErr = err
			decPW.CloseWithError(err)
		}
	}()

	// Decompress zstd
	zr, err := zstd.NewReader(decPR)
	if err != nil {
		return fmt.Errorf("create zstd reader: %w", err)
	}
	defer zr.Close()

	// Write to temp file, then atomic rename
	tmpPath := destPath + ".tmp"
	outFile, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if _, err := io.Copy(outFile, zr); err != nil {
		_ = outFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("decompress to file: %w", err)
	}
	_ = outFile.Close()

	if decryptErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("decrypt: %w", decryptErr)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	slog.Info("backup.download.complete", "gcs_path", gcsPath, "dest", destPath)
	return nil
}

func (s *GCSStore) StreamTarGz(ctx context.Context, gcsPath string, encryptionKey, nonce, expectedHMAC []byte, w io.Writer) error {
	tmpDir, err := os.MkdirTemp("", "backup-export-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	ext4Path := filepath.Join(tmpDir, "volume.ext4")
	if err := s.Download(ctx, gcsPath, ext4Path, encryptionKey, nonce, expectedHMAC); err != nil {
		return fmt.Errorf("download backup: %w", err)
	}

	mountDir := filepath.Join(tmpDir, "mnt")
	if err := os.MkdirAll(mountDir, 0o755); err != nil {
		return fmt.Errorf("create mount dir: %w", err)
	}

	cmd := exec.CommandContext(ctx, "mount", "-o", "loop,ro,noload", ext4Path, mountDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount ext4: %s: %w", string(out), err)
	}
	defer func() { _ = exec.Command("umount", mountDir).Run() }()

	gzw := gzip.NewWriter(w)
	defer func() { _ = gzw.Close() }()
	tw := tar.NewWriter(gzw)
	defer func() { _ = tw.Close() }()

	return filepath.WalkDir(mountDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(mountDir, path)
		if relPath == "." {
			return nil
		}

		// Use Lstat to detect symlinks (Walk/WalkDir follows them by default on info)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}

		// Resolve symlink target for tar header
		var linkTarget string
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, _ = os.Readlink(path)
		}

		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		header.Name = relPath

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		// Don't copy content for directories or symlinks
		if d.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(tw, f)
		return err
	})
}

func (s *GCSStore) StreamDecrypted(ctx context.Context, gcsPath string, encryptionKey, nonce, expectedHMAC []byte, w io.Writer) error {
	slog.Info("backup.stream_decrypted.start", "gcs_path", gcsPath)

	obj := s.client.Bucket(s.bucket).Object(gcsPath)
	reader, err := obj.NewReader(ctx)
	if err != nil {
		return fmt.Errorf("open GCS object: %w", err)
	}
	defer func() { _ = reader.Close() }()

	// Decrypt
	decPR, decPW := io.Pipe()
	var decryptErr error
	go func() {
		defer func() { _ = decPW.Close() }()
		if err := StreamDecrypt(reader, decPW, encryptionKey, nonce, expectedHMAC); err != nil {
			decryptErr = err
			decPW.CloseWithError(err)
		}
	}()

	// Decompress zstd → write directly to output
	zr, err := zstd.NewReader(decPR)
	if err != nil {
		return fmt.Errorf("create zstd reader: %w", err)
	}
	defer zr.Close()

	if _, err := io.Copy(w, zr); err != nil {
		if decryptErr != nil {
			return fmt.Errorf("decrypt: %w", decryptErr)
		}
		return fmt.Errorf("stream decrypted: %w", err)
	}
	if decryptErr != nil {
		return fmt.Errorf("decrypt: %w", decryptErr)
	}

	slog.Info("backup.stream_decrypted.complete", "gcs_path", gcsPath)
	return nil
}

func (s *GCSStore) Delete(ctx context.Context, gcsPath string) error {
	slog.Info("backup.delete", "gcs_path", gcsPath)
	return s.client.Bucket(s.bucket).Object(gcsPath).Delete(ctx)
}
