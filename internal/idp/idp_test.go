package idp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/guanzi008/headscale-console/internal/config"
)

var csrfPattern = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func TestLocaleSelectionAndSwitching(t *testing.T) {
	server, httpServer := newTestServer(t, nil)
	defer httpServer.Close()
	defer server.Close()

	client := newTestClient(t, httpServer.URL)
	request, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/idp/login", nil)
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.5")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET localized login: %v", err)
	}
	body := readBody(t, response)
	_ = response.Body.Close()
	if !strings.Contains(body, `<html lang="zh-CN">`) || !strings.Contains(body, "登录账号中心") {
		t.Fatalf("expected a Chinese login page, got: %s", body)
	}
	if response.Header.Get("Content-Language") != LocaleChinese {
		t.Fatalf("Content-Language = %q, want %q", response.Header.Get("Content-Language"), LocaleChinese)
	}

	switchRequest, _ := http.NewRequest(http.MethodGet,
		httpServer.URL+"/idp/language?lang=en&return_to=%2Fadmin%2Fmachines", nil)
	response, err = client.Do(switchRequest)
	if err != nil {
		t.Fatalf("switch locale: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/admin/machines" {
		t.Fatalf("language switch status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || cookies[0].Name != localeCookie || cookies[0].Value != LocaleEnglish || cookies[0].Path != "/" {
		t.Fatalf("unexpected locale cookie: %#v", cookies)
	}

	request, _ = http.NewRequest(http.MethodGet, httpServer.URL+"/idp/login", nil)
	request.Header.Set("Accept-Language", "zh-CN")
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("GET English login: %v", err)
	}
	body = readBody(t, response)
	_ = response.Body.Close()
	if !strings.Contains(body, `<html lang="en">`) || !strings.Contains(body, "Sign in to the account center") {
		t.Fatalf("expected the stored English locale to win, got: %s", body)
	}

	switchRequest, _ = http.NewRequest(http.MethodGet,
		httpServer.URL+"/idp/language?lang=zh-CN&return_to=https%3A%2F%2Fevil.example", nil)
	response, err = client.Do(switchRequest)
	if err != nil {
		t.Fatalf("switch locale with unsafe return URL: %v", err)
	}
	_ = response.Body.Close()
	if response.Header.Get("Location") != "/idp/login" {
		t.Fatalf("unsafe return URL was accepted: %q", response.Header.Get("Location"))
	}
	if cookies := response.Cookies(); len(cookies) != 1 || cookies[0].Value != LocaleChinese {
		t.Fatalf("locale was not updated for the unsafe redirect case: %#v", cookies)
	}
}

func TestRegistrationAndOIDCFlow(t *testing.T) {
	server, httpServer := newTestServer(t, nil)
	defer httpServer.Close()
	defer server.Close()

	client := newTestClient(t, httpServer.URL)
	text := getBody(t, client, httpServer.URL+"/idp/register", http.StatusOK)
	csrf := csrfToken(t, text)

	response := postForm(t, client, httpServer.URL+"/idp/register", url.Values{
		"csrf_token":       {csrf},
		"display_name":     {"Alice"},
		"email":            {"ALICE@example.com"},
		"password":         {"correct horse battery"},
		"password_confirm": {"correct horse battery"},
		"return_to":        {"/idp/account"},
	})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("register status = %d, want %d: %s", response.StatusCode, http.StatusSeeOther, readBody(t, response))
	}
	_ = response.Body.Close()

	user, err := server.store.UserByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("load registered user: %v", err)
	}
	account := getBody(t, client, httpServer.URL+"/idp/account", http.StatusOK)
	if !strings.Contains(account, "alice@example.com") || !strings.Contains(account, user.ID) {
		t.Fatalf("account page does not contain the registered identity")
	}

	verifier := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	redirectURI := httpServer.URL + "/admin/oidc/callback"
	authorizeURL := httpServer.URL + "/idp/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {"test-client"},
		"redirect_uri":          {redirectURI},
		"scope":                 {"openid email profile"},
		"state":                 {"state-value"},
		"nonce":                 {"nonce-value"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()
	response = getResponse(t, client, authorizeURL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("authorize status = %d, want %d: %s", response.StatusCode, http.StatusSeeOther, readBody(t, response))
	}
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse authorize redirect: %v", err)
	}
	_ = response.Body.Close()
	if location.Query().Get("state") != "state-value" || location.Query().Get("code") == "" {
		t.Fatalf("authorize redirect missing state or code: %s", location)
	}

	response = postForm(t, client, httpServer.URL+"/idp/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {location.Query().Get("code")},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
		"client_id":     {"test-client"},
		"client_secret": {"test-secret"},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("token status = %d: %s", response.StatusCode, readBody(t, response))
	}
	var tokens struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&tokens); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	_ = response.Body.Close()

	parsed, err := jwt.Parse(tokens.IDToken, func(token *jwt.Token) (any, error) {
		return &server.signer.key.PublicKey, nil
	}, jwt.WithIssuer(server.opts.Issuer), jwt.WithAudience("test-client"), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid {
		t.Fatalf("verify ID token: %v", err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if claims["sub"] != user.ID || claims["nonce"] != "nonce-value" || claims["email"] != "alice@example.com" {
		t.Fatalf("unexpected ID token claims: %#v", claims)
	}

	request, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/idp/userinfo", nil)
	request.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("userinfo request: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("userinfo status = %d: %s", response.StatusCode, readBody(t, response))
	}
	var claimsBody map[string]any
	if err := json.NewDecoder(response.Body).Decode(&claimsBody); err != nil {
		t.Fatalf("decode userinfo: %v", err)
	}
	_ = response.Body.Close()
	if claimsBody["sub"] != user.ID {
		t.Fatalf("userinfo sub = %v, want %s", claimsBody["sub"], user.ID)
	}
}

func TestExternalIdentityBindingAndLogin(t *testing.T) {
	provider := &fakeProvider{
		name:     "mock",
		subject:  "upstream-user-1",
		email:    "alice@example.com",
		authURL:  "http://upstream.invalid/authorize",
		identity: externalIdentity{Subject: "upstream-user-1", Email: "alice@example.com", Name: "Alice"},
	}
	providers := []config.OAuthProviderConfig{{
		Name:         "mock",
		Type:         "github",
		ClientID:     "mock-client",
		ClientSecret: "mock-secret",
	}}
	server, httpServer := newTestServer(t, providers)
	defer httpServer.Close()
	defer server.Close()
	server.providers[provider.Name()] = provider

	client := newTestClient(t, httpServer.URL)
	registerTestUser(t, client, httpServer.URL, "alice@example.com")
	user, err := server.store.UserByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("load user: %v", err)
	}

	account := getBody(t, client, httpServer.URL+"/idp/account", http.StatusOK)
	response := postFormWithCookies(t, client, httpServer.URL+"/idp/account/link", url.Values{
		"csrf_token": {csrfToken(t, account)},
		"provider":   {"mock"},
	})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("start link status = %d: %s", response.StatusCode, readBody(t, response))
	}
	state := cookieValue(t, response, httpServer.URL, oauthStateCookie)
	_ = response.Body.Close()

	response = getResponse(t, client, httpServer.URL+"/idp/oauth/mock/callback?state="+url.QueryEscape(state)+"&code=link-code")
	if response.StatusCode != http.StatusSeeOther || !strings.Contains(response.Header.Get("Location"), "/idp/account") {
		t.Fatalf("link callback status = %d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	_ = response.Body.Close()
	identity, err := server.store.IdentityBySubject(context.Background(), "mock", "upstream-user-1")
	if err != nil || identity.UserID != user.ID {
		t.Fatalf("identity was not linked: %#v, %v", identity, err)
	}

	freshClient := newTestClient(t, httpServer.URL)
	response = getResponse(t, freshClient, httpServer.URL+"/idp/login/mock?next=%2Fidp%2Faccount")
	if response.StatusCode != http.StatusSeeOther || !strings.HasPrefix(response.Header.Get("Location"), "http://upstream.invalid/") {
		t.Fatalf("external login start status = %d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	state = cookieValue(t, response, httpServer.URL, oauthStateCookie)
	_ = response.Body.Close()

	response = getResponse(t, freshClient, httpServer.URL+"/idp/oauth/mock/callback?state="+url.QueryEscape(state)+"&code=login-code")
	if response.StatusCode != http.StatusSeeOther || !strings.Contains(response.Header.Get("Location"), "/idp/account") {
		t.Fatalf("external login callback status = %d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	_ = response.Body.Close()
	getBody(t, freshClient, httpServer.URL+"/idp/account", http.StatusOK)

	provider.identity = externalIdentity{Subject: "unbound-user", Email: "other@example.com", Name: "Other"}
	unboundClient := newTestClient(t, httpServer.URL)
	response = getResponse(t, unboundClient, httpServer.URL+"/idp/login/mock")
	state = cookieValue(t, response, httpServer.URL, oauthStateCookie)
	_ = response.Body.Close()
	response = getResponse(t, unboundClient, httpServer.URL+"/idp/oauth/mock/callback?state="+url.QueryEscape(state)+"&code=unbound")
	if response.StatusCode != http.StatusSeeOther || !strings.Contains(response.Header.Get("Location"), "error=unbound") {
		t.Fatalf("unbound callback status = %d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	_ = response.Body.Close()
}

type fakeProvider struct {
	name     string
	subject  string
	email    string
	authURL  string
	identity externalIdentity
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) UsesPKCE() bool { return false }

func (p *fakeProvider) AuthCodeURL(state, _, _ string) string {
	value, _ := url.Parse(p.authURL)
	query := value.Query()
	query.Set("state", state)
	value.RawQuery = query.Encode()
	return value.String()
}

func (p *fakeProvider) Exchange(_ context.Context, _, _, _ string) (externalIdentity, error) {
	return p.identity, nil
}

func newTestServer(t *testing.T, providers []config.OAuthProviderConfig) (*Server, *httptest.Server) {
	t.Helper()
	httpServer := httptest.NewUnstartedServer(nil)
	server, err := New(Options{
		DataDir:       t.TempDir(),
		BaseURL:       "http://" + strings.TrimPrefix(httpServer.Listener.Addr().String(), ""),
		Issuer:        "http://" + httpServer.Listener.Addr().String() + "/idp",
		AdminPath:     "/admin",
		ClientID:      "test-client",
		ClientSecret:  "test-secret",
		SecureCookies: false,
		Registration: config.RegistrationConfig{
			Enabled:           true,
			PasswordMinLength: 8,
		},
		Providers: providers,
	})
	if err != nil {
		t.Fatalf("create IDP: %v", err)
	}
	httpServer.Config.Handler = server
	httpServer.Start()
	server.opts.BaseURL = httpServer.URL
	server.opts.Issuer = httpServer.URL + "/idp"
	return server, httpServer
}

func newTestClient(t *testing.T, serverURL string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	parsed, _ := url.Parse(serverURL)
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 10 * time.Second,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			request.URL.Scheme = parsed.Scheme
			request.URL.Host = parsed.Host
			return http.DefaultTransport.RoundTrip(request)
		}),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func registerTestUser(t *testing.T, client *http.Client, baseURL, email string) {
	t.Helper()
	text := getBody(t, client, baseURL+"/idp/register", http.StatusOK)
	response := postForm(t, client, baseURL+"/idp/register", url.Values{
		"csrf_token":       {csrfToken(t, text)},
		"display_name":     {"Alice"},
		"email":            {email},
		"password":         {"correct horse battery"},
		"password_confirm": {"correct horse battery"},
		"return_to":        {"/idp/account"},
	})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("register status = %d: %s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()
}

func getResponse(t *testing.T, client *http.Client, endpoint string) *http.Response {
	t.Helper()
	response, err := client.Get(endpoint)
	if err != nil {
		t.Fatalf("GET %s: %v", endpoint, err)
	}
	return response
}

func getBody(t *testing.T, client *http.Client, endpoint string, status int) string {
	t.Helper()
	response := getResponse(t, client, endpoint)
	defer response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("GET %s status = %d, want %d: %s", endpoint, response.StatusCode, status, readBody(t, response))
	}
	return readBody(t, response)
}

func postForm(t *testing.T, client *http.Client, endpoint string, values url.Values) *http.Response {
	t.Helper()
	return postFormWithCookies(t, client, endpoint, values)
}

func postFormWithCookies(t *testing.T, client *http.Client, endpoint string, values url.Values) *http.Response {
	t.Helper()
	response, err := client.PostForm(endpoint, values)
	if err != nil {
		t.Fatalf("POST %s: %v", endpoint, err)
	}
	return response
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(data)
}

func csrfToken(t *testing.T, body string) string {
	t.Helper()
	matches := csrfPattern.FindStringSubmatch(body)
	if len(matches) != 2 {
		t.Fatalf("CSRF token not found in page")
	}
	return matches[1]
}

func cookieValue(t *testing.T, response *http.Response, baseURL, name string) string {
	t.Helper()
	parsed, _ := url.Parse(baseURL)
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	for _, cookie := range response.Request.Response.Cookies() {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	t.Fatalf("cookie %s not found for %s", name, parsed.Host)
	return ""
}
