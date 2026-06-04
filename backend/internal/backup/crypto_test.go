package backup

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestGenerateKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Fatalf("key length = %d, want 32", len(key))
	}
	// Two keys should be different
	key2, _ := GenerateKey()
	if bytes.Equal(key, key2) {
		t.Fatal("two generated keys should not be equal")
	}
}

func TestEncryptDecryptMasterKey(t *testing.T) {
	masterKey := make([]byte, 32)
	_, _ = rand.Read(masterKey)
	machineKey, _ := GenerateKey()

	encrypted, err := EncryptKey(machineKey, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := DecryptKey(encrypted, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(machineKey, decrypted) {
		t.Fatal("decrypted key does not match original")
	}
}

func TestStreamEncryptDecrypt(t *testing.T) {
	key, _ := GenerateKey()
	plaintext := make([]byte, 1024*1024) // 1MB
	_, _ = rand.Read(plaintext)

	// Encrypt
	var cipherBuf bytes.Buffer
	nonce, hmacHash, err := StreamEncrypt(bytes.NewReader(plaintext), &cipherBuf, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(nonce) != 16 {
		t.Fatalf("nonce length = %d, want 16", len(nonce))
	}
	if len(hmacHash) != 32 {
		t.Fatalf("hmac length = %d, want 32", len(hmacHash))
	}

	// Decrypt
	var plainBuf bytes.Buffer
	err = StreamDecrypt(&cipherBuf, &plainBuf, key, nonce, hmacHash)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plaintext, plainBuf.Bytes()) {
		t.Fatal("decrypted data does not match original")
	}
}

func TestStreamDecryptTamperedNonce(t *testing.T) {
	key, _ := GenerateKey()
	plaintext := []byte("hello world backup data")

	var cipherBuf bytes.Buffer
	nonce, hmacHash, _ := StreamEncrypt(bytes.NewReader(plaintext), &cipherBuf, key)

	// Tamper with nonce — flip one bit
	nonce[0] ^= 0x01

	var plainBuf bytes.Buffer
	err := StreamDecrypt(bytes.NewReader(cipherBuf.Bytes()), &plainBuf, key, nonce, hmacHash)
	if err == nil {
		t.Fatal("expected HMAC error for tampered nonce, got nil")
	}
}

func TestStreamDecryptTamperedData(t *testing.T) {
	key, _ := GenerateKey()
	plaintext := []byte("hello world backup data")

	var cipherBuf bytes.Buffer
	nonce, hmacHash, _ := StreamEncrypt(bytes.NewReader(plaintext), &cipherBuf, key)

	// Tamper with ciphertext
	cipherBytes := cipherBuf.Bytes()
	cipherBytes[len(cipherBytes)/2] ^= 0xFF

	var plainBuf bytes.Buffer
	err := StreamDecrypt(bytes.NewReader(cipherBytes), &plainBuf, key, nonce, hmacHash)
	if err == nil {
		t.Fatal("expected error for tampered data")
	}
}
