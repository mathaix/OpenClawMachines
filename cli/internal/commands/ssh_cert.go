package commands

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// sshCertResponse matches the backend POST .../ssh-cert response.
type sshCertResponse struct {
	Certificate string `json:"certificate"`
	ExpiresAt   string `json:"expires_at"`
	Username    string `json:"username"`
	Hostname    string `json:"hostname"`
}

// sshCertResult holds the paths to temp files created by fetchSSHCert.
type sshCertResult struct {
	KeyPath  string // path to ephemeral private key
	CertPath string // path to signed certificate (<keypath>-cert.pub)
	Username string
	Hostname string
}

// fetchSSHCert generates an ephemeral Ed25519 keypair locally, sends the public
// key to the backend for signing, and writes the cert + private key to temp files.
// Returns paths that should be cleaned up by the caller.
func fetchSSHCert(machineID string) (*sshCertResult, error) {
	// Generate ephemeral Ed25519 keypair
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating keypair: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return nil, fmt.Errorf("converting public key: %w", err)
	}
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPub))

	// Call backend API
	client := newAPIClient()
	reqBody, err := json.Marshal(map[string]string{
		"public_key": pubKeyStr,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}

	path := fmt.Sprintf("/api/accounts/%d/machines/%s/ssh-cert", cfg.DefaultAccountID, machineID)
	resp, err := client.Post(path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("requesting SSH certificate: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("SSH certificate request failed (%d): %s", resp.StatusCode, string(body))
	}

	var certResp sshCertResponse
	if err := json.Unmarshal(body, &certResp); err != nil {
		return nil, fmt.Errorf("parsing SSH certificate response: %w", err)
	}
	if certResp.Certificate == "" {
		return nil, fmt.Errorf("SSH certificate response is missing the certificate field")
	}

	// Write private key to temp file
	tmpDir := filepath.Join(os.TempDir(), "ocm-ssh")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}

	keyFile, err := os.CreateTemp(tmpDir, "key-*")
	if err != nil {
		return nil, fmt.Errorf("creating key file: %w", err)
	}

	// Marshal private key to OpenSSH PEM format
	pemBlock, err := ssh.MarshalPrivateKey(privKey, "ocm-ephemeral")
	if err != nil {
		os.Remove(keyFile.Name())
		return nil, fmt.Errorf("marshaling private key: %w", err)
	}

	if _, err := keyFile.Write(pem.EncodeToMemory(pemBlock)); err != nil {
		os.Remove(keyFile.Name())
		return nil, fmt.Errorf("writing key file: %w", err)
	}
	keyFile.Close()

	// ssh requires strict permissions on the key file
	if err := os.Chmod(keyFile.Name(), 0o600); err != nil {
		os.Remove(keyFile.Name())
		return nil, fmt.Errorf("setting key file permissions: %w", err)
	}

	// Write certificate to <keypath>-cert.pub (ssh picks this up automatically)
	certPath := keyFile.Name() + "-cert.pub"
	if err := os.WriteFile(certPath, []byte(certResp.Certificate), 0o644); err != nil {
		os.Remove(keyFile.Name())
		return nil, fmt.Errorf("writing cert file: %w", err)
	}

	return &sshCertResult{
		KeyPath:  keyFile.Name(),
		CertPath: certPath,
		Username: certResp.Username,
		Hostname: certResp.Hostname,
	}, nil
}

// cleanupSSHCert removes the temp key and cert files.
func cleanupSSHCert(result *sshCertResult) {
	if result == nil {
		return
	}
	os.Remove(result.KeyPath)
	os.Remove(result.CertPath)
}
