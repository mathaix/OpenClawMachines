//go:build linux && integration

package integration

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ============================================================================
// SSH Cert Auth Tests
//
// Boots a Firecracker VM with a test SSH CA configured, then verifies that
// sshd inside the VM correctly accepts/rejects SSH certificates signed by
// different CAs and with different principals.
//
// All subtests share a single VM (booted once by the parent).
// ============================================================================

// testSSHCA holds a generated SSH CA keypair for testing.
type testSSHCA struct {
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
	Signer     ssh.Signer
	PubKeyStr  string // OpenSSH authorized_keys format
}

// generateTestSSHCA creates a new Ed25519 SSH CA keypair for testing.
func generateTestSSHCA(t *testing.T) *testSSHCA {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate SSH CA keypair: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("Failed to create SSH signer: %v", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("Failed to convert public key: %v", err)
	}

	return &testSSHCA{
		PrivateKey: priv,
		PublicKey:  pub,
		Signer:     signer,
		PubKeyStr:  string(ssh.MarshalAuthorizedKey(sshPub)),
	}
}

// signSSHCert creates a signed SSH user certificate for the given principals.
func (ca *testSSHCA) signSSHCert(t *testing.T, userPub ssh.PublicKey, principals []string) *ssh.Certificate {
	t.Helper()

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		t.Fatalf("Failed to generate serial: %v", err)
	}

	now := time.Now()
	cert := &ssh.Certificate{
		CertType:        ssh.UserCert,
		Key:             userPub,
		Serial:          serial.Uint64(),
		KeyId:           fmt.Sprintf("test-cert-%d", serial.Uint64()),
		ValidPrincipals: principals,
		ValidAfter:      uint64(now.Add(-5 * time.Minute).Unix()),
		ValidBefore:     uint64(now.Add(1 * time.Hour).Unix()),
		Permissions: ssh.Permissions{
			Extensions: map[string]string{
				"permit-pty":              "",
				"permit-port-forwarding":  "",
				"permit-agent-forwarding": "",
			},
		},
	}

	if err := cert.SignCert(rand.Reader, ca.Signer); err != nil {
		t.Fatalf("Failed to sign certificate: %v", err)
	}

	return cert
}

// writeSSHKeyAndCert writes an ephemeral private key and signed cert to temp
// files suitable for passing to ssh -i. Returns (keyPath, certPath).
func writeSSHKeyAndCert(t *testing.T, privKey ed25519.PrivateKey, cert *ssh.Certificate) (string, string) {
	t.Helper()

	tmpDir := t.TempDir()

	// Write private key in OpenSSH PEM format
	keyPath := filepath.Join(tmpDir, "id_ed25519")
	pemBlock, err := ssh.MarshalPrivateKey(privKey, "test-ephemeral")
	if err != nil {
		t.Fatalf("Failed to marshal private key: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0o600); err != nil {
		t.Fatalf("Failed to write key file: %v", err)
	}

	// Write certificate as <keypath>-cert.pub (ssh picks this up automatically)
	certPath := keyPath + "-cert.pub"
	if err := os.WriteFile(certPath, ssh.MarshalAuthorizedKey(cert), 0o644); err != nil {
		t.Fatalf("Failed to write cert file: %v", err)
	}

	return keyPath, certPath
}

// sshCommand runs ssh with the given key, connecting to vmIP as the given user,
// and executes the remote command. Returns combined output and error.
func sshCommand(t *testing.T, keyPath, user, vmIP, remoteCmd string, timeout time.Duration) (string, error) {
	t.Helper()

	args := []string{
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", int(timeout.Seconds())),
		"-l", user,
		vmIP,
		remoteCmd,
	}

	cmd := exec.Command("ssh", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestSSHSuite(t *testing.T) {
	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)

	// Check ssh binary is available on the host
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("Skipping: ssh binary not found in PATH")
	}

	// Generate a test SSH CA — this will be pushed to the VM via metadata
	ca := generateTestSSHCA(t)
	t.Logf("Generated test SSH CA: %s", ca.PubKeyStr[:40]+"...")

	bridge := setupTestBridge(t, cfg)
	if err := bridge.SetupNAT(); err != nil {
		t.Fatalf("Failed to setup NAT: %v", err)
	}

	orch := setupTestOrchestrator(t, cfg, bridge)
	metaSrv := setupTestMetadataServer(t, bridge.Gateway)
	orch.SetMetadataRegistrar(metaSrv)

	// Create VM config WITH the test SSH CA public key
	vmCfg := generateTestVMConfig(0)
	vmCfg.CfCaPubKey = ca.PubKeyStr

	if err := orch.Create(t.Context(), vmCfg); err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}
	waitForVMReady(t, orch, vmCfg.MachineID, vmCreationTimeout)

	// Wait for sshd to be ready (init script starts it after configuring cert auth).
	// We poll by attempting a TCP connection to port 22.
	waitForSSHD(t, vmCfg.VMIP, 60*time.Second)

	t.Run("CertAuthAccepted", func(t *testing.T) {
		// Generate ephemeral user keypair
		userPub, userPriv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("Failed to generate user keypair: %v", err)
		}
		sshUserPub, err := ssh.NewPublicKey(userPub)
		if err != nil {
			t.Fatalf("Failed to convert user public key: %v", err)
		}

		// Sign cert with correct principal (matches OwnerEmails in VM config)
		cert := ca.signSSHCert(t, sshUserPub, []string{"test@openclawmachines.com", "openclaw"})
		keyPath, _ := writeSSHKeyAndCert(t, userPriv, cert)

		// SSH into VM and run a command
		marker := fmt.Sprintf("SSH_TEST_%s", randomID())
		output, err := sshCommand(t, keyPath, "openclaw", vmCfg.VMIP, fmt.Sprintf("echo %s", marker), 15*time.Second)
		if err != nil {
			t.Fatalf("SSH with valid cert failed: %v\nOutput: %s", err, output)
		}

		if !strings.Contains(output, marker) {
			t.Errorf("Expected marker %q in output, got: %s", marker, output)
		} else {
			t.Logf("SSH cert auth accepted — echoed marker successfully")
		}
	})

	t.Run("CertAuthRejectedWrongPrincipal", func(t *testing.T) {
		userPub, userPriv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("Failed to generate user keypair: %v", err)
		}
		sshUserPub, err := ssh.NewPublicKey(userPub)
		if err != nil {
			t.Fatalf("Failed to convert user public key: %v", err)
		}

		// Sign cert with WRONG principal — not in OwnerEmails
		cert := ca.signSSHCert(t, sshUserPub, []string{"hacker@evil.com"})
		keyPath, _ := writeSSHKeyAndCert(t, userPriv, cert)

		_, err = sshCommand(t, keyPath, "openclaw", vmCfg.VMIP, "echo should-not-work", 10*time.Second)
		if err == nil {
			t.Error("SSH with wrong principal should have been rejected, but succeeded")
		} else {
			t.Logf("SSH correctly rejected cert with wrong principal: %v", err)
		}
	})

	t.Run("CertAuthRejectedWrongCA", func(t *testing.T) {
		// Generate a DIFFERENT CA (not trusted by sshd)
		rogueCA := generateTestSSHCA(t)

		userPub, userPriv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("Failed to generate user keypair: %v", err)
		}
		sshUserPub, err := ssh.NewPublicKey(userPub)
		if err != nil {
			t.Fatalf("Failed to convert user public key: %v", err)
		}

		// Sign with rogue CA but correct principal
		cert := rogueCA.signSSHCert(t, sshUserPub, []string{"test@openclawmachines.com", "openclaw"})
		keyPath, _ := writeSSHKeyAndCert(t, userPriv, cert)

		_, err = sshCommand(t, keyPath, "openclaw", vmCfg.VMIP, "echo should-not-work", 10*time.Second)
		if err == nil {
			t.Error("SSH with cert signed by wrong CA should have been rejected, but succeeded")
		} else {
			t.Logf("SSH correctly rejected cert from untrusted CA: %v", err)
		}
	})
}

// waitForSSHD polls TCP port 22 on the VM until sshd accepts connections.
func waitForSSHD(t *testing.T, vmIP string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("ssh",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "BatchMode=yes",
			"-o", "ConnectTimeout=2",
			"-l", "openclaw",
			vmIP,
			"true",
		)
		// We expect auth failure (exit code 255) but a TCP connection.
		// If we get connection refused, sshd isn't up yet.
		out, err := cmd.CombinedOutput()
		if err == nil {
			// Shouldn't succeed without a cert, but sshd is up
			t.Log("sshd is ready (unexpectedly accepted connection)")
			return
		}
		outStr := string(out)
		// "Permission denied" means sshd is running but (correctly) rejecting us
		if strings.Contains(outStr, "Permission denied") || strings.Contains(outStr, "permission denied") {
			t.Logf("sshd is ready (rejecting unauthenticated connections as expected)")
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("sshd did not become ready within %v", timeout)
}
