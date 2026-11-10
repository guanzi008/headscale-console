package headplane

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Options struct {
	PersonalNetwork bool
	Listen          string
	PublicURL       string
	AdminPath       string
	DataDir         string
	HeadscaleURL    string
	HeadscaleConfig string
	APIKey          string
	CookieSecret    string
	SecureCookies   bool
	Issuer          string
	ClientID        string
	ClientSecret    string
	DefaultRole     string
	PostLogoutURI   string
}

func WriteConfig(path string, options Options) error {
	host, port, err := net.SplitHostPort(options.Listen)
	if err != nil {
		return fmt.Errorf("split Headplane listener: %w", err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("Headplane listener has an invalid port %q", port)
	}
	if len(options.CookieSecret) != 32 {
		return fmt.Errorf("Headplane cookie secret must be exactly 32 characters")
	}
	if options.PostLogoutURI == "" {
		return fmt.Errorf("Headplane post-logout redirect URI is required")
	}

	dataDir := filepath.Join(options.DataDir, "headplane", "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create Headplane data directory: %w", err)
	}

	cfg := map[string]any{
		"server": map[string]any{
			"host":          host,
			"port":          portNumber,
			"base_url":      options.PublicURL,
			"base_path":     options.AdminPath,
			"data_path":     dataDir,
			"cookie_secret": options.CookieSecret,
			"cookie_secure": options.SecureCookies,
		},
		"headscale": map[string]any{
			"url":         options.HeadscaleURL,
			"public_url":  options.PublicURL,
			"api_key":     options.APIKey,
			"config_path": options.HeadscaleConfig,
		},
		"oidc": map[string]any{
			"issuer":                   options.Issuer,
			"client_id":                options.ClientID,
			"client_secret":            options.ClientSecret,
			"default_role":             options.DefaultRole,
			"disable_api_key_login":    true,
			"post_logout_redirect_uri": options.PostLogoutURI,
			"use_end_session":          true,
			"use_pkce":                 true,
			"scope":                    "openid email profile",
		},
	}
	if runtime.GOOS == "linux" {
		cfg["integration"] = map[string]any{
			"proc": map[string]any{"enabled": true},
		}
	}
	if options.PersonalNetwork {
		cfg["integration"] = map[string]any{"proc": map[string]any{
			"enabled":            true,
			"console_reload_url": options.HeadscaleURL + "/api/console/tailnet/reload",
			"console_api_key":    options.APIKey,
		}}
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode Headplane config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Headplane config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write Headplane config: %w", err)
	}
	return nil
}
