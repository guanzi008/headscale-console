package headscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const defaultPolicy = `{
  "acls": [
    {
      "action": "accept",
      "src": ["*"],
      "dst": ["*:*"]
    }
  ]
}`

// EnsureDefaultPolicy makes Headscale's implicit allow-all behavior explicit.
// Headscale 0.30 returns 500 from GET /api/v1/policy until a policy row exists,
// even though it otherwise behaves as allow-all. Persisting the equivalent
// policy keeps Headplane's ACL views functional without changing connectivity.
func EnsureDefaultPolicy(ctx context.Context, baseURL, apiKey string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	status, body, err := policyRequest(requestCtx, http.MethodGet, baseURL+"/api/v1/policy", apiKey, nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if status != http.StatusInternalServerError {
		return fmt.Errorf("read Headscale policy: unexpected status %d: %s", status, body)
	}

	payload, err := json.Marshal(map[string]string{"policy": defaultPolicy})
	if err != nil {
		return fmt.Errorf("encode default Headscale policy: %w", err)
	}
	status, body, err = policyRequest(requestCtx, http.MethodPut, baseURL+"/api/v1/policy", apiKey, payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("write default Headscale policy: unexpected status %d: %s", status, body)
	}
	logger.Info("initialized Headscale with an explicit allow-all ACL policy")
	return nil
}

func policyRequest(ctx context.Context, method, endpoint, apiKey string, payload []byte) (int, string, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, "", fmt.Errorf("build Headscale policy request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, "", fmt.Errorf("request Headscale policy: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, "", fmt.Errorf("read Headscale policy response: %w", err)
	}
	return response.StatusCode, string(data), nil
}
