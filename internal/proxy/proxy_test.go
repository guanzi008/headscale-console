package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestRouting(t *testing.T) {
	backend := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, name+":"+r.URL.Path)
		}))
	}
	idp := backend("idp")
	admin := backend("admin")
	headscale := backend("headscale")
	defer idp.Close()
	defer admin.Close()
	defer headscale.Close()

	router := New("/admin", mustURL(t, idp.URL), mustURL(t, admin.URL), mustURL(t, headscale.URL), slog.Default())
	tests := []struct {
		path string
		want string
	}{
		{"/idp/login", "idp:/idp/login"},
		{"/idp", "idp:/idp"},
		{"/admin", "admin:/admin"},
		{"/admin/oidc/callback", "admin:/admin/oidc/callback"},
		{"/administer", "headscale:/administer"},
		{"/health", "headscale:/health"},
		{"/oidc/callback", "headscale:/oidc/callback"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Body.String() != test.want {
				t.Fatalf("route %s = %d %q, want %q", test.path, response.Code, response.Body.String(), test.want)
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusTemporaryRedirect || response.Header().Get("Location") != "/admin/" {
		t.Fatalf("root redirect = %d %q", response.Code, response.Header().Get("Location"))
	}
}

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
