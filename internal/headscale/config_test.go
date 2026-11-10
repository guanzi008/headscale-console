package headscale

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWriteConfigPreservesHeadplaneServerSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headscale.yaml")
	existing := []byte(`
server_url: https://old.example.com
disable_check_updates: false
node:
  expiry: 30d
log:
  level: debug
  format: json
taildrop:
  enabled: false
auto_update:
  enabled: true
`)
	if err := os.WriteFile(path, existing, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := WriteConfig(path, Options{
		DataDir:        t.TempDir(),
		PublicURL:      "https://hs.example.com",
		HTTPListen:     "127.0.0.1:8081",
		GRPCListen:     "127.0.0.1:50443",
		MetricsListen:  "127.0.0.1:9090",
		BaseDomain:     "tailnet.example.com",
		DERPURLs:       []string{"https://controlplane.tailscale.com/derpmap/default"},
		DNSNameservers: []string{"1.1.1.1"},
		Issuer:         "https://hs.example.com/idp",
		ClientID:       "headscale-console",
		ClientSecret:   "secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got["server_url"] != "https://hs.example.com" {
		t.Fatalf("server_url was not regenerated: %v", got["server_url"])
	}
	if got["disable_check_updates"] != false {
		t.Fatalf("disable_check_updates was not preserved: %v", got["disable_check_updates"])
	}
	node := got["node"].(map[string]any)
	if node["expiry"] != "30d" {
		t.Fatalf("node.expiry was not preserved: %v", node["expiry"])
	}
	logging := got["log"].(map[string]any)
	if logging["level"] != "debug" {
		t.Fatalf("log.level was not preserved: %v", logging["level"])
	}
	if logging["format"] != "json" {
		t.Fatalf("log.format was not preserved: %v", logging["format"])
	}
	taildrop := got["taildrop"].(map[string]any)
	if taildrop["enabled"] != false {
		t.Fatalf("taildrop.enabled was not preserved: %v", taildrop["enabled"])
	}
	autoUpdate := got["auto_update"].(map[string]any)
	if autoUpdate["enabled"] != true {
		t.Fatalf("auto_update.enabled was not preserved: %v", autoUpdate["enabled"])
	}
	if _, ok := node["ephemeral"]; !ok {
		t.Fatal("managed node.ephemeral settings were lost")
	}
}
