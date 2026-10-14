package hub

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// File under the data dir that holds the install id
const InstallIDFile = "install_id"

// Shape of an install id the hub accepts from this panel
var installIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// True for 32 lowercase hex characters
func ValidInstallID(id string) bool {
	return installIDPattern.MatchString(id)
}

// Draws a fresh install id from the system random source
func NewInstallID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("draw install id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Reads the persisted install id, minting one when the file is missing or malformed
// The file is written owner only
func LoadInstallID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, InstallIDFile)
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if err == nil {
		id := strings.TrimSpace(string(raw))
		if ValidInstallID(id) {
			if err := os.Chmod(path, 0o600); err != nil {
				return "", fmt.Errorf("chmod %s: %w", path, err)
			}
			return id, nil
		}
	}
	id, err := NewInstallID()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dataDir, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("persist %s: %w", path, err)
	}
	return id, nil
}
