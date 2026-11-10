package headplane

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWriteConfigEnablesRPInitiatedLogout(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "headplane.yaml")
	postLogoutURI := "https://hs.example.com/admin/login?s=logout"

	err := WriteConfig(path, Options{
		Listen:          "127.0.0.1:18090",
		PublicURL:       "https://hs.example.com",
		AdminPath:       "/admin",
		DataDir:         root,
		HeadscaleURL:    "http://127.0.0.1:18081",
		HeadscaleConfig: filepath.Join(root, "headscale.yaml"),
		APIKey:          "api-key",
		CookieSecret:    "0123456789abcdef0123456789abcdef",
		Issuer:          "https://hs.example.com/idp",
		ClientID:        "headscale-console",
		ClientSecret:    "secret",
		DefaultRole:     "member",
		PostLogoutURI:   postLogoutURI,
	})
	if err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg struct {
		OIDC struct {
			UseEndSession      bool   `yaml:"use_end_session"`
			PostLogoutRedirect string `yaml:"post_logout_redirect_uri"`
		} `yaml:"oidc"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if !cfg.OIDC.UseEndSession || cfg.OIDC.PostLogoutRedirect != postLogoutURI {
		t.Fatalf("unexpected OIDC logout config: %+v", cfg.OIDC)
	}
}
