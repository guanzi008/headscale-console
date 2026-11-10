package headscale

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Options struct {
	DataDir        string
	PublicURL      string
	HTTPListen     string
	GRPCListen     string
	MetricsListen  string
	BaseDomain     string
	DERPURLs       []string
	DNSNameservers []string
	Issuer         string
	ClientID       string
	ClientSecret   string
}

type writtenConfig struct {
	Path         string
	DatabasePath string
	UnixSocket   string
	NoiseKeyPath string
	RecordsPath  string
}

// preservedSettings are the fields Headplane can edit through the Server
// settings page. They live in the generated Headscale configuration, which
// must keep them when the console rewrites its own managed fields at startup.
type preservedSettings struct {
	DisableCheckUpdates *bool `yaml:"disable_check_updates"`
	Node                *struct {
		Expiry any `yaml:"expiry"`
	} `yaml:"node"`
	Log *struct {
		Level  any `yaml:"level"`
		Format any `yaml:"format"`
	} `yaml:"log"`
	Taildrop *struct {
		Enabled *bool `yaml:"enabled"`
	} `yaml:"taildrop"`
	AutoUpdate *struct {
		Enabled *bool `yaml:"enabled"`
	} `yaml:"auto_update"`
}

func WriteConfig(path string, options Options) (writtenConfig, error) {
	root := filepath.Join(options.DataDir, "headscale")
	databasePath := filepath.Join(root, "db.sqlite")
	unixSocket := filepath.Join(root, "headscale.sock")
	noiseKey := filepath.Join(root, "noise_private.key")
	recordsPath := filepath.Join(root, "extra-records.json")

	if err := os.MkdirAll(root, 0o700); err != nil {
		return writtenConfig{}, fmt.Errorf("create Headscale data directory: %w", err)
	}
	if _, err := os.Stat(recordsPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(recordsPath, []byte("[]\n"), 0o600); err != nil {
			return writtenConfig{}, fmt.Errorf("create Headscale DNS records file: %w", err)
		}
	}

	preserved, err := readPreservedSettings(path)
	if err != nil {
		return writtenConfig{}, err
	}

	cfg := map[string]any{
		"server_url":            options.PublicURL,
		"listen_addr":           options.HTTPListen,
		"metrics_listen_addr":   options.MetricsListen,
		"grpc_listen_addr":      options.GRPCListen,
		"disable_check_updates": true,
		"trusted_proxies":       []string{"127.0.0.1/32", "::1/128"},
		"node": map[string]any{
			"ephemeral": map[string]any{"inactivity_timeout": "30m"},
		},
		"noise": map[string]any{
			"private_key_path": noiseKey,
		},
		"prefixes": map[string]any{
			"v4":         "100.64.0.0/10",
			"v6":         "fd7a:115c:a1e0::/48",
			"allocation": "sequential",
		},
		"derp": map[string]any{
			"server":              map[string]any{"enabled": false},
			"urls":                options.DERPURLs,
			"paths":               []string{},
			"auto_update_enabled": false,
		},
		"database": map[string]any{
			"type": "sqlite",
			"sqlite": map[string]any{
				"path":            databasePath,
				"write_ahead_log": true,
			},
		},
		"log": map[string]any{
			"level":  "info",
			"format": "text",
		},
		"policy": map[string]any{
			"mode": "database",
		},
		"dns": map[string]any{
			"magic_dns":          true,
			"base_domain":        options.BaseDomain,
			"override_local_dns": true,
			"nameservers":        map[string]any{"global": options.DNSNameservers},
			"search_domains":     []string{},
			"extra_records_path": recordsPath,
		},
		"unix_socket":            unixSocket,
		"unix_socket_permission": "0770",
		"logtail":                map[string]any{"enabled": false},
		"oidc": map[string]any{
			"only_start_if_oidc_is_available": true,
			"issuer":                          options.Issuer,
			"client_id":                       options.ClientID,
			"client_secret":                   options.ClientSecret,
			"scope":                           []string{"openid", "profile", "email"},
			"email_verified_required":         true,
			"pkce": map[string]any{
				"enabled": true,
				"method":  "S256",
			},
		},
	}
	applyPreservedSettings(cfg, preserved)

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return writtenConfig{}, fmt.Errorf("encode Headscale config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return writtenConfig{}, fmt.Errorf("write Headscale config: %w", err)
	}
	return writtenConfig{
		Path:         path,
		DatabasePath: databasePath,
		UnixSocket:   unixSocket,
		NoiseKeyPath: noiseKey,
		RecordsPath:  recordsPath,
	}, nil
}

func readPreservedSettings(path string) (preservedSettings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return preservedSettings{}, nil
	}
	if err != nil {
		return preservedSettings{}, fmt.Errorf("read existing Headscale config: %w", err)
	}

	var settings preservedSettings
	if err := yaml.Unmarshal(data, &settings); err != nil {
		return preservedSettings{}, fmt.Errorf("decode existing Headscale config: %w", err)
	}
	return settings, nil
}

func applyPreservedSettings(cfg map[string]any, settings preservedSettings) {
	if settings.DisableCheckUpdates != nil {
		cfg["disable_check_updates"] = *settings.DisableCheckUpdates
	}

	node, _ := cfg["node"].(map[string]any)
	if node != nil && settings.Node != nil && settings.Node.Expiry != nil {
		node["expiry"] = settings.Node.Expiry
	}

	logging, _ := cfg["log"].(map[string]any)
	if logging != nil && settings.Log != nil {
		if settings.Log.Level != nil {
			logging["level"] = settings.Log.Level
		}
		if settings.Log.Format != nil {
			logging["format"] = settings.Log.Format
		}
	}

	if settings.Taildrop != nil && settings.Taildrop.Enabled != nil {
		cfg["taildrop"] = map[string]any{"enabled": *settings.Taildrop.Enabled}
	}

	if settings.AutoUpdate != nil && settings.AutoUpdate.Enabled != nil {
		cfg["auto_update"] = map[string]any{"enabled": *settings.AutoUpdate.Enabled}
	}
}

func EnsureInitialConfig(path string, options Options) (writtenConfig, error) {
	info, err := os.Stat(path)
	if err == nil && info.Size() > 0 {
		root := filepath.Join(options.DataDir, "headscale")
		return writtenConfig{
			Path:         path,
			DatabasePath: filepath.Join(root, "db.sqlite"),
			UnixSocket:   filepath.Join(root, "headscale.sock"),
			NoiseKeyPath: filepath.Join(root, "noise_private.key"),
			RecordsPath:  filepath.Join(root, "extra-records.json"),
		}, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return writtenConfig{}, fmt.Errorf("inspect Headscale config: %w", err)
	}
	return WriteConfig(path, options)
}

func (c writtenConfig) Age() time.Duration {
	info, err := os.Stat(c.Path)
	if err != nil {
		return 0
	}
	return time.Since(info.ModTime())
}
