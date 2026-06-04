package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// GenerateKey creates a 32-byte random AES-256 key.
func GenerateKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return key, nil
}

// EncryptKey encrypts a machine backup key with the platform master key using AES-256-GCM.
func EncryptKey(machineKey, masterKey []byte) ([]byte, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, machineKey, nil), nil
}

// DecryptKey decrypts a machine backup key with the platform master key.
func DecryptKey(encrypted, masterKey []byte) ([]byte, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(encrypted) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	return gcm.Open(nil, encrypted[:nonceSize], encrypted[nonceSize:], nil)
}

// StreamEncrypt reads plaintext from r, encrypts with AES-256-CTR, and writes to w.
// Returns the nonce and HMAC-SHA256 of the ciphertext for verification.
// Uses CTR mode (not GCM) for streaming — GCM requires knowing full size upfront.
func StreamEncrypt(r io.Reader, w io.Writer, key []byte) (nonce []byte, hmacHash []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}

	nonce = make([]byte, aes.BlockSize) // 16 bytes for CTR
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}

	stream := cipher.NewCTR(block, nonce)
	mac := hmac.New(sha256.New, key)
	mac.Write(nonce) // authenticate the nonce — prevents silent corruption

	// Stream: read plaintext -> encrypt -> write to both output and HMAC
	buf := make([]byte, 32*1024)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			encrypted := make([]byte, n)
			stream.XORKeyStream(encrypted, buf[:n])
			if _, err := w.Write(encrypted); err != nil {
				return nil, nil, fmt.Errorf("write encrypted: %w", err)
			}
			mac.Write(encrypted)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, nil, fmt.Errorf("read plaintext: %w", readErr)
		}
	}

	return nonce, mac.Sum(nil), nil
}

// StreamDecrypt reads ciphertext from r, verifies HMAC, decrypts with AES-256-CTR, writes to w.
// Uses two-pass streaming: first pass computes HMAC (spilling to temp file), second pass decrypts.
// This avoids buffering the entire backup in memory.
func StreamDecrypt(r io.Reader, w io.Writer, key, nonce, expectedHMAC []byte) error {
	// First pass: tee ciphertext to a temp file while computing HMAC
	tmpFile, err := os.CreateTemp("", "backup-decrypt-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	mac := hmac.New(sha256.New, key)
	mac.Write(nonce) // must match StreamEncrypt: nonce is part of the authenticated data
	tee := io.TeeReader(r, mac)
	if _, err := io.Copy(tmpFile, tee); err != nil {
		return fmt.Errorf("read ciphertext: %w", err)
	}

	if !hmac.Equal(mac.Sum(nil), expectedHMAC) {
		return fmt.Errorf("HMAC verification failed: backup may be corrupted or tampered")
	}

	// Second pass: decrypt from temp file
	if _, err := tmpFile.Seek(0, 0); err != nil {
		return fmt.Errorf("seek temp file: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	stream := cipher.NewCTR(block, nonce)

	buf := make([]byte, 32*1024)
	for {
		n, readErr := tmpFile.Read(buf)
		if n > 0 {
			decrypted := make([]byte, n)
			stream.XORKeyStream(decrypted, buf[:n])
			if _, err := w.Write(decrypted); err != nil {
				return fmt.Errorf("write decrypted: %w", err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read temp file: %w", readErr)
		}
	}

	return nil
}
