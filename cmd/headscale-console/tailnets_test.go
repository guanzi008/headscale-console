package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/guanzi008/headscale-console/internal/config"
	"github.com/guanzi008/headscale-console/internal/idp"
)

func TestLogoutLandingStartsANewLoginWithoutTheLogoutMarker(t *testing.T) {
	for _, prefix := range []string{"/admin", "/tools/web"} {
		t.Run(prefix, func(t *testing.T) {
			identityProvider, err := idp.New(idp.Options{
				DataDir: t.TempDir(), BaseURL: "https://console.example", Issuer: "https://console.example/idp",
				AdminPath: prefix, ClientID: "test-client", ClientSecret: strings.Repeat("x", 48),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer identityProvider.Close()
			app := &application{cfg: config.Config{Server: config.ServerConfig{AdminPath: prefix}}, idpServer: identityProvider}
			handler := app.guardPersonalHeadplane(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("a signed-out account reached a private dashboard")
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, prefix+"/login?s=logout", nil))
			location, err := url.Parse(response.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusSeeOther || location.Path != "/idp/login" || location.Query().Get("return_to") != prefix+"/" {
				t.Fatalf("logout landing preserves logout state: status=%d location=%q", response.Code, response.Header().Get("Location"))
			}
		})
	}
}
