package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/guanzi008/headscale-console/internal/headplane"
	"github.com/guanzi008/headscale-console/internal/idp"
)

type personalNetwork struct {
	mu      sync.Mutex
	ready   bool
	retired bool
}

func (a *application) networkEntry(id string) *personalNetwork {
	value, _ := a.networks.LoadOrStore(id, &personalNetwork{})
	return value.(*personalNetwork)
}

func (a *application) ensurePersonalNetwork(ctx context.Context, user idp.User) error {
	entry := a.networkEntry(user.ID)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.retired {
		return fmt.Errorf("personal network has been deleted")
	}
	if entry.ready {
		return nil
	}
	body, err := json.Marshal(map[string]string{"id": user.ID, "email": user.Email, "name": user.DisplayName})
	if err != nil {
		return err
	}
	response, err := a.networkRequest(ctx, http.MethodPost, "/api/console/tailnets", body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("create personal network: HTTP %d", response.StatusCode)
	}
	var info struct {
		ID         string `json:"id"`
		APIKey     string `json:"api_key"`
		ConfigPath string `json:"config_path"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&info); err != nil {
		return err
	}
	if info.ID != user.ID || info.APIKey == "" || info.ConfigPath == "" {
		return fmt.Errorf("invalid personal network response")
	}
	mac := hmac.New(sha256.New, []byte(a.cookieSecret))
	_, _ = mac.Write([]byte(user.ID))
	cookieSecret := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))[:32]
	root := filepath.Join(a.dataDir, "tailnets", user.ID)
	if err := headplane.WriteConfig(filepath.Join(root, "generated", "headplane.yaml"), headplane.Options{
		PersonalNetwork: true,
		Listen:          a.cfg.Server.Internal.Headplane,
		PublicURL:       a.cfg.Server.BaseURL,
		AdminPath:       a.cfg.Server.AdminPath,
		DataDir:         root,
		HeadscaleURL:    a.cfg.HeadscaleURL(),
		HeadscaleConfig: info.ConfigPath,
		APIKey:          info.APIKey,
		CookieSecret:    cookieSecret,
		SecureCookies:   a.cfg.SecureCookies(),
		Issuer:          a.cfg.Issuer(),
		ClientID:        oidcClientID,
		ClientSecret:    a.clientSecret,
		DefaultRole:     "admin",
		PostLogoutURI:   a.cfg.PublicURL(a.cfg.Server.AdminPath + "/login?s=logout"),
	}); err != nil {
		return err
	}
	entry.ready = true
	return nil
}

func (a *application) networkRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	key, _ := a.rootAPIKey.Load().(string)
	if key == "" {
		return nil, fmt.Errorf("control server is starting")
	}
	request, err := http.NewRequestWithContext(ctx, method, a.cfg.HeadscaleURL()+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(request)
}

func (a *application) guardPersonalHeadplane(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == a.cfg.Server.AdminPath+"/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		// The provider ends the account session before returning here. A new
		// sign-in must resume at the dashboard rather than replay this marker.
		if r.Method == http.MethodGet && strings.TrimSuffix(r.URL.Path, "/") == a.cfg.Server.AdminPath+"/login" && r.URL.Query().Get("s") == "logout" {
			http.Redirect(w, r, "/idp/login?return_to="+url.QueryEscape(a.cfg.Server.AdminPath+"/"), http.StatusSeeOther)
			return
		}
		user, ok := a.idpServer.CurrentUser(r)
		if !ok {
			returnTo := a.cfg.Server.AdminPath + "/"
			if r.Method == http.MethodGet && !strings.HasSuffix(r.URL.Path, "/oidc/callback") {
				returnTo = r.URL.RequestURI()
			}
			http.Redirect(w, r, "/idp/login?return_to="+url.QueryEscape(returnTo), http.StatusSeeOther)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if err := a.ensurePersonalNetwork(ctx, user); err != nil {
			a.logger.Error("prepare personal network", "user", user.ID, "error", err)
			http.Error(w, "个人网络暂时不可用，请稍后重试。 / Personal network temporarily unavailable.", http.StatusServiceUnavailable)
			return
		}
		// Overwrite browser-supplied routing headers. The selected network always
		// comes from the current account session, never from a form or URL.
		r.Header.Set("X-HSC-Tailnet", user.ID)
		r.Header.Set("X-HSC-Proxy-Secret", a.proxySecret)
		if user.IsAdmin {
			r.Header.Set("X-HSC-Platform-Admin", "1")
		} else {
			r.Header.Set("X-HSC-Platform-Admin", "0")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *application) deletePersonalNetwork(ctx context.Context, id string) error {
	entry := a.networkEntry(id)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.retired = true
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, a.cfg.HeadplaneURL()+"/__console/tailnets/"+id, nil)
	if err != nil {
		return err
	}
	request.Header.Set("X-HSC-Proxy-Secret", a.proxySecret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("close personal network console: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("close personal network console: HTTP %d", response.StatusCode)
	}
	response, err = a.networkRequest(ctx, http.MethodDelete, "/api/console/tailnets/"+id, nil)
	if err != nil {
		return err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("delete personal network: HTTP %d", response.StatusCode)
	}
	entry.ready = false
	return nil
}
