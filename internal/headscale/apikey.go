package headscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const apiKeyLifetime = "87600h"

// EnsureAPIKey reuses a valid persistent Headscale API key, or creates one via
// the Headscale CLI and stores it with owner-only permissions.
func EnsureAPIKey(ctx context.Context, binary, configPath, baseURL, keyPath string, logger *slog.Logger) (string, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if data, err := os.ReadFile(keyPath); err == nil {
		key := strings.TrimSpace(string(data))
		if key != "" {
			valid, verifyErr := validAPIKey(ctx, baseURL, key)
			if verifyErr != nil {
				return "", verifyErr
			}
			if valid {
				return key, nil
			}
			logger.Warn("stored Headscale API key is no longer valid; creating a new key")
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read Headscale API key: %w", err)
	}

	key, err := createAPIKey(ctx, binary, configPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return "", fmt.Errorf("create API key directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(keyPath), ".apikey-")
	if err != nil {
		return "", fmt.Errorf("create temporary API key file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("set API key permissions: %w", err)
	}
	if _, err := temp.WriteString(key + "\n"); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("write API key: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close API key: %w", err)
	}
	if err := os.Rename(tempPath, keyPath); err != nil {
		return "", fmt.Errorf("persist API key: %w", err)
	}
	return key, nil
}

func validAPIKey(ctx context.Context, baseURL, key string) (bool, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, baseURL+"/api/v1/user", nil)
	if err != nil {
		return false, fmt.Errorf("build API key verification request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false, fmt.Errorf("verify Headscale API key: %w", err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return false, nil
	default:
		return false, fmt.Errorf("verify Headscale API key: unexpected status %s", response.Status)
	}
}

func createAPIKey(ctx context.Context, binary, configPath string) (string, error) {
	command := exec.CommandContext(ctx, binary,
		"--config", configPath,
		"apikeys", "create",
		"--expiration", apiKeyLifetime,
		"--output", "json",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("create Headscale API key: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseAPIKeyOutput(stdout.String())
}

func parseAPIKeyOutput(output string) (string, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return "", fmt.Errorf("Headscale API key command returned no output")
	}

	var encoded string
	if err := json.Unmarshal([]byte(output), &encoded); err == nil && encoded != "" {
		return encoded, nil
	}
	var object struct {
		APIKey string `json:"apiKey"`
	}
	if err := json.Unmarshal([]byte(output), &object); err == nil && object.APIKey != "" {
		return object.APIKey, nil
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "hskey-") {
			return line, nil
		}
	}
	return "", fmt.Errorf("could not parse Headscale API key output")
}
