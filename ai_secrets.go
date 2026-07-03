package main

// AI secret storage.
//
// API keys for the "api" style providers are sensitive, so we never persist
// them in plaintext. They live in an encrypted blob (.mdviewer_ai_secrets.enc)
// sealed with AES-256-GCM. The 32-byte master key is kept out of the repo:
//
//   1. macOS Keychain (preferred) — a generic password item that only the
//      current user can read. This is the strongest option on macOS.
//   2. File fallback — <appRoot>/.mdviewer_ai.key with 0600 perms, used when
//      the Keychain is unavailable (non-macOS or `security` errors). This is
//      weaker (a local file) and is only a graceful degradation.
//
// The plaintext is a JSON map of providerID -> apiKey.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	aiSecretsFileName = ".mdviewer_ai_secrets.enc"
	aiKeyFileName     = ".mdviewer_ai.key"
	aiKeychainService = "mdviewer-ai"
	aiKeychainAccount = "master-key"
)

// aiSecrets maps a providerID to its plaintext API key.
type aiSecrets map[string]string

var aiMasterKeyMu sync.Mutex

func (s *webServer) aiSecretsPath() string {
	return filepath.Join(s.appRoot, aiSecretsFileName)
}

func (s *webServer) aiKeyFilePath() string {
	return filepath.Join(s.appRoot, aiKeyFileName)
}

// aiMasterKey returns the 32-byte master key, creating and persisting one on
// first use. It prefers the macOS Keychain and falls back to a 0600 file.
func (s *webServer) aiMasterKey() ([]byte, error) {
	aiMasterKeyMu.Lock()
	defer aiMasterKeyMu.Unlock()

	if runtime.GOOS == "darwin" {
		if key, err := keychainGetKey(); err == nil && len(key) == 32 {
			return key, nil
		}
		// Not present (or unreadable) — generate and try to store it.
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := keychainSetKey(key); err == nil {
			return key, nil
		}
		// Keychain write failed; fall through to file-based key.
	}

	return s.fileMasterKey()
}

// fileMasterKey reads (or creates) the fallback key file.
func (s *webServer) fileMasterKey() ([]byte, error) {
	path := s.aiKeyFilePath()
	if raw, err := os.ReadFile(path); err == nil {
		key, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if derr == nil && len(key) == 32 {
			return key, nil
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	enc := base64.StdEncoding.EncodeToString(key)
	if err := os.WriteFile(path, []byte(enc), 0o600); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}
	return key, nil
}

// keychainGetKey reads the base64 master key from the macOS Keychain.
func keychainGetKey() ([]byte, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-s", aiKeychainService, "-a", aiKeychainAccount, "-w")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, err
	}
	return key, nil
}

// keychainSetKey stores (or updates) the base64 master key in the Keychain.
func keychainSetKey(key []byte) error {
	enc := base64.StdEncoding.EncodeToString(key)
	// -U updates the item if it already exists.
	cmd := exec.Command("security", "add-generic-password",
		"-s", aiKeychainService, "-a", aiKeychainAccount, "-w", enc, "-U")
	return cmd.Run()
}

// aiSeal encrypts plaintext with AES-256-GCM and returns base64(nonce||ct).
func aiSeal(key, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// aiOpen reverses aiSeal.
func aiOpen(key []byte, encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

// loadAISecrets decrypts and returns the stored secrets. A missing file
// yields an empty map (not an error).
func (s *webServer) loadAISecrets() (aiSecrets, error) {
	data, err := os.ReadFile(s.aiSecretsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return aiSecrets{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return aiSecrets{}, nil
	}
	key, err := s.aiMasterKey()
	if err != nil {
		return nil, err
	}
	plain, err := aiOpen(key, string(data))
	if err != nil {
		return nil, fmt.Errorf("decrypt secrets: %w", err)
	}
	out := aiSecrets{}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// saveAISecrets encrypts and writes the secrets map (0600).
func (s *webServer) saveAISecrets(secrets aiSecrets) error {
	key, err := s.aiMasterKey()
	if err != nil {
		return err
	}
	plain, err := json.Marshal(secrets)
	if err != nil {
		return err
	}
	sealed, err := aiSeal(key, plain)
	if err != nil {
		return err
	}
	return os.WriteFile(s.aiSecretsPath(), []byte(sealed), 0o600)
}
