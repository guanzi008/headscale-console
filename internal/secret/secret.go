package secret

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreateToken returns a stable URL-safe token persisted at path. The
// returned value contains exactly length ASCII characters.
func LoadOrCreateToken(path string, length int) (string, error) {
	if length < 16 {
		return "", fmt.Errorf("secret length must be at least 16")
	}
	if data, err := os.ReadFile(path); err == nil {
		value := strings.TrimSpace(string(data))
		if len(value) >= length {
			return value[:length], nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read secret %s: %w", path, err)
	}

	raw := make([]byte, (length*3+3)/4+3)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	if len(value) < length {
		return "", fmt.Errorf("generated secret is too short")
	}
	value = value[:length]

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create secret directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".secret-")
	if err != nil {
		return "", fmt.Errorf("create temporary secret: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("set secret permissions: %w", err)
	}
	if _, err := temp.WriteString(value + "\n"); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("write secret: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close secret: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", fmt.Errorf("persist secret: %w", err)
	}
	return value, nil
}
