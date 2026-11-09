package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultAdminPath = "/admin"
	DefaultIDPPath   = "/idp"
)

type Config struct {
	Server         ServerConfig          `yaml:"server"`
	Registration   RegistrationConfig    `yaml:"registration"`
	Headplane      HeadplaneConfig       `yaml:"headplane"`
	Headscale      HeadscaleConfig       `yaml:"headscale"`
	OAuthProviders []OAuthProviderConfig `yaml:"oauth_providers"`
}

type ServerConfig struct {
	Listen        string `yaml:"listen"`
	BaseURL       string `yaml:"base_url"`
	AdminPath     string `yaml:"admin_path"`
	TLSCertFile   string `yaml:"tls_cert_file,omitempty"`
	TLSKeyFile    string `yaml:"tls_key_file,omitempty"`
	SecureCookies *bool  `yaml:"secure_cookies,omitempty"`

	Internal InternalListeners `yaml:"internal"`
}

type InternalListeners struct {
	IDP             string `yaml:"idp"`
	Headplane       string `yaml:"headplane"`
	HeadscaleHTTP   string `yaml:"headscale_http"`
	HeadscaleGRPC   string `yaml:"headscale_grpc"`
	HeadscaleMetric string `yaml:"headscale_metrics"`
}

type RegistrationConfig struct {
	Enabled             bool     `yaml:"enabled"`
	AllowedEmailDomains []string `yaml:"allowed_email_domains,omitempty"`
	PasswordMinLength   int      `yaml:"password_min_length"`
}

type HeadplaneConfig struct {
	DefaultRole string `yaml:"default_role"`
}

type HeadscaleConfig struct {
	BaseDomain     string   `yaml:"base_domain"`
	DERPURLs       []string `yaml:"derp_urls"`
	DNSNameservers []string `yaml:"dns_nameservers,omitempty"`
}

type OAuthProviderConfig struct {
	Name         string   `yaml:"name"`
	Type         string   `yaml:"type"`
	Issuer       string   `yaml:"issuer,omitempty"`
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"`
	Scopes       []string `yaml:"scopes,omitempty"`
}

func Default() Config {
	return Config{
		Server: ServerConfig{
			Listen:    "0.0.0.0:8080",
			BaseURL:   "http://localhost:8080",
			AdminPath: DefaultAdminPath,
			Internal: InternalListeners{
				IDP:             "127.0.0.1:18070",
				Headplane:       "127.0.0.1:18090",
				HeadscaleHTTP:   "127.0.0.1:18081",
				HeadscaleGRPC:   "127.0.0.1:18082",
				HeadscaleMetric: "127.0.0.1:18083",
			},
		},
		Registration: RegistrationConfig{
			Enabled:           true,
			PasswordMinLength: 10,
		},
		Headplane: HeadplaneConfig{
			DefaultRole: "member",
		},
		Headscale: HeadscaleConfig{
			BaseDomain:     "tailnet.internal",
			DERPURLs:       []string{"https://controlplane.tailscale.com/derpmap/default"},
			DNSNameservers: []string{"1.1.1.1", "1.0.0.1"},
		},
	}
}

func DefaultDataDir() (string, error) {
	if value := os.Getenv("HEADSCALE_CONSOLE_DATA_DIR"); value != "" {
		return filepath.Abs(value)
	}
	if os.Geteuid() == 0 {
		return "/var/lib/headscale-console", nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(base, "headscale-console"), nil
}

func LoadOrCreate(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := Default()
		if err := cfg.Validate(); err != nil {
			return Config{}, fmt.Errorf("validate default config: %w", err)
		}
		if err := Save(path, cfg); err != nil {
			return Config{}, err
		}
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %s: %w", path, err)
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	content := append([]byte("# Headscale Console configuration\n# See README.md for provider examples.\n\n"), data...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func (c *Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
		return fmt.Errorf("server.listen must be host:port: %w", err)
	}
	base, err := url.Parse(c.Server.BaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return fmt.Errorf("server.base_url must be an absolute http(s) URL")
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return fmt.Errorf("server.base_url must use http or https")
	}
	if strings.TrimSpace(base.Path) != "" || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("server.base_url must not contain a path, query, or fragment")
	}
	c.Server.BaseURL = strings.TrimRight(c.Server.BaseURL, "/")
	if (c.Server.TLSCertFile == "") != (c.Server.TLSKeyFile == "") {
		return fmt.Errorf("server.tls_cert_file and server.tls_key_file must be set together")
	}

	if !strings.HasPrefix(c.Server.AdminPath, "/") ||
		strings.HasSuffix(c.Server.AdminPath, "/") ||
		strings.Contains(c.Server.AdminPath, "..") {
		return fmt.Errorf("server.admin_path must start with / and have no trailing slash")
	}
	if c.Server.AdminPath == DefaultIDPPath || strings.HasPrefix(c.Server.AdminPath+"/", DefaultIDPPath+"/") {
		return fmt.Errorf("server.admin_path cannot overlap %s", DefaultIDPPath)
	}
	if c.Registration.PasswordMinLength < 8 {
		return fmt.Errorf("registration.password_min_length must be at least 8")
	}
	c.Headplane.DefaultRole = strings.ToLower(strings.TrimSpace(c.Headplane.DefaultRole))
	if c.Headplane.DefaultRole == "" {
		c.Headplane.DefaultRole = "member"
	}
	switch c.Headplane.DefaultRole {
	case "admin", "network_admin", "it_admin", "auditor", "viewer", "member":
	default:
		return fmt.Errorf("headplane.default_role must be admin, network_admin, it_admin, auditor, viewer, or member")
	}
	if c.Headscale.BaseDomain == "" {
		return fmt.Errorf("headscale.base_domain is required")
	}
	if len(c.Headscale.DERPURLs) == 0 {
		return fmt.Errorf("headscale.derp_urls cannot be empty")
	}
	internalNames := []string{
		"idp",
		"headplane",
		"headscale_http",
		"headscale_grpc",
		"headscale_metrics",
	}
	internalListeners := []string{
		c.Server.Internal.IDP,
		c.Server.Internal.Headplane,
		c.Server.Internal.HeadscaleHTTP,
		c.Server.Internal.HeadscaleGRPC,
		c.Server.Internal.HeadscaleMetric,
	}
	seenListeners := make(map[string]string, len(internalListeners))
	for i, listen := range internalListeners {
		host, _, err := net.SplitHostPort(listen)
		if err != nil {
			return fmt.Errorf("server.internal.%s is not host:port: %w", internalNames[i], err)
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("server.internal.%s must use a loopback IP address", internalNames[i])
		}
		if previous, exists := seenListeners[listen]; exists {
			return fmt.Errorf("server.internal.%s duplicates server.internal.%s (%s)", internalNames[i], previous, listen)
		}
		seenListeners[listen] = internalNames[i]
	}
	for _, value := range c.Headscale.DERPURLs {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("headscale.derp_urls contains an invalid URL %q", value)
		}
	}
	if len(c.Headscale.DNSNameservers) == 0 {
		return fmt.Errorf("headscale.dns_nameservers cannot be empty")
	}
	for _, server := range c.Headscale.DNSNameservers {
		if net.ParseIP(strings.TrimSpace(server)) == nil {
			return fmt.Errorf("headscale.dns_nameservers contains an invalid IP address %q", server)
		}
	}

	seenProviders := make(map[string]struct{}, len(c.OAuthProviders))
	for i := range c.OAuthProviders {
		provider := &c.OAuthProviders[i]
		provider.Name = strings.ToLower(strings.TrimSpace(provider.Name))
		provider.Type = strings.ToLower(strings.TrimSpace(provider.Type))
		if provider.Name == "" || !validSlug(provider.Name) {
			return fmt.Errorf("oauth_providers[%d].name must contain only letters, digits, or hyphens", i)
		}
		if _, exists := seenProviders[provider.Name]; exists {
			return fmt.Errorf("duplicate oauth provider %q", provider.Name)
		}
		seenProviders[provider.Name] = struct{}{}
		if provider.ClientID == "" || provider.ClientSecret == "" {
			return fmt.Errorf("oauth provider %q needs client_id and client_secret", provider.Name)
		}
		switch provider.Type {
		case "github":
		case "oidc":
			if provider.Issuer == "" {
				return fmt.Errorf("OIDC provider %q needs issuer", provider.Name)
			}
			issuer, err := url.Parse(provider.Issuer)
			if err != nil || issuer.Host == "" || (issuer.Scheme != "http" && issuer.Scheme != "https") {
				return fmt.Errorf("OIDC provider %q has an invalid http(s) issuer", provider.Name)
			}
		default:
			return fmt.Errorf("oauth provider %q has unsupported type %q (use github or oidc)", provider.Name, provider.Type)
		}
		provider.ClientID = strings.TrimSpace(provider.ClientID)
		provider.ClientSecret = strings.TrimSpace(provider.ClientSecret)
	}
	return nil
}

func (c Config) SecureCookies() bool {
	if c.Server.SecureCookies != nil {
		return *c.Server.SecureCookies
	}
	return strings.HasPrefix(c.Server.BaseURL, "https://")
}

func (c Config) PublicURL(path string) string {
	if path == "" {
		return c.Server.BaseURL
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.Server.BaseURL + path
}

func (c Config) HeadscaleURL() string {
	return "http://" + c.Server.Internal.HeadscaleHTTP
}

func (c Config) HeadplaneURL() string {
	return "http://" + c.Server.Internal.Headplane
}

func (c Config) IDPURL() string {
	return "http://" + c.Server.Internal.IDP
}

func (c Config) Issuer() string {
	return c.PublicURL(DefaultIDPPath)
}

func (c Config) HeadplaneCallback() string {
	return c.PublicURL(c.Server.AdminPath + "/oidc/callback")
}

func (c Config) HeadscaleCallback() string {
	return c.PublicURL("/oidc/callback")
}

func validSlug(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
			return false
		}
	}
	return true
}
