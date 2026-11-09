package idp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/guanzi008/headscale-console/internal/config"
)

const (
	sessionCookie    = "hsc_session"
	csrfCookie       = "hsc_csrf"
	oauthStateCookie = "hsc_oauth_state"
	sessionLifetime  = 7 * 24 * time.Hour
)

type Options struct {
	DataDir       string
	BaseURL       string
	Issuer        string
	AdminPath     string
	ClientID      string
	ClientSecret  string
	SecureCookies bool
	Registration  config.RegistrationConfig
	Providers     []config.OAuthProviderConfig
	Logger        *slog.Logger
}

type Server struct {
	opts       Options
	store      *Store
	signer     *signer
	secret     []byte
	templates  *template.Template
	providers  map[string]externalProvider
	httpServer *http.Server
}

type pageData struct {
	Title               string
	TitleKey            string
	Email               string
	DisplayName         string
	Error               string
	ErrorKey            string
	ErrorArgs           []any
	Notice              string
	NoticeKey           string
	NoticeArgs          []any
	ReturnTo            string
	CSRF                string
	RegistrationEnabled bool
	AdminPath           string
	User                *User
	Identities          map[string]Identity
	Providers           []providerView
	Locale              string
	Lang                string
	LanguageLinks       []languageLink
}

func (d pageData) T(key string, args ...any) string {
	return translate(d.Locale, key, args...)
}

type languageLink struct {
	Locale string
	Label  string
	URL    string
	Active bool
}

type translationError struct {
	key  string
	args []any
}

func (e *translationError) Error() string { return e.key }

type providerView struct {
	Name        string
	Linked      bool
	LinkedEmail string
}

func New(options Options) (*Server, error) {
	if err := os.MkdirAll(options.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create account data directory: %w", err)
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	store, err := OpenStore(filepath.Join(options.DataDir, "accounts.sqlite"))
	if err != nil {
		return nil, err
	}
	secret, err := loadOrCreateSecret(filepath.Join(options.DataDir, "cookie-secret"), 32)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	signingKey, err := loadOrCreateSigner(filepath.Join(options.DataDir, "signing-key.pem"))
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	tpl, err := parseTemplates()
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	server := &Server{
		opts:      options,
		store:     store,
		signer:    signingKey,
		secret:    secret,
		templates: tpl,
		providers: make(map[string]externalProvider),
	}
	for _, provider := range options.Providers {
		external, err := newExternalProvider(context.Background(), provider, options.BaseURL)
		if err != nil {
			options.Logger.Warn("external identity provider disabled", "provider", provider.Name, "error", err)
			continue
		}
		server.providers[provider.Name] = external
	}
	return server, nil
}

func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if s.httpServer != nil {
		_ = s.httpServer.Shutdown(ctx)
	}
	return s.store.Close()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.routes().ServeHTTP(w, r)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /idp/healthz", s.handleHealth)
	mux.HandleFunc("GET /idp/", s.handleIndex)
	mux.HandleFunc("GET /idp/.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("GET /idp/jwks.json", s.handleJWKS)
	mux.HandleFunc("GET /idp/authorize", s.handleAuthorize)
	mux.HandleFunc("POST /idp/token", s.handleToken)
	mux.HandleFunc("GET /idp/userinfo", s.handleUserInfo)
	mux.HandleFunc("POST /idp/userinfo", s.handleUserInfo)
	mux.HandleFunc("GET /idp/login", s.handleLoginPage)
	mux.HandleFunc("POST /idp/login", s.handleLogin)
	mux.HandleFunc("GET /idp/language", s.handleLanguage)
	mux.HandleFunc("GET /idp/register", s.handleRegisterPage)
	mux.HandleFunc("POST /idp/register", s.handleRegister)
	mux.HandleFunc("POST /idp/logout", s.handleLogout)
	mux.HandleFunc("GET /idp/account", s.handleAccount)
	mux.HandleFunc("POST /idp/account/password", s.handlePasswordChange)
	mux.HandleFunc("POST /idp/account/link", s.handleStartLink)
	mux.HandleFunc("POST /idp/account/unlink", s.handleUnlink)
	mux.HandleFunc("GET /idp/login/{provider}", s.handleStartExternalLogin)
	mux.HandleFunc("GET /idp/oauth/{provider}/callback", s.handleExternalCallback)
	return mux
}

func (s *Server) handleLanguage(w http.ResponseWriter, r *http.Request) {
	locale := normalizeLocale(r.URL.Query().Get("lang"))
	if locale == "" {
		locale = LocaleEnglish
	}
	http.SetCookie(w, &http.Cookie{
		Name:     localeCookie,
		Value:    locale,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.opts.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   365 * 24 * 60 * 60,
	})
	returnTo := s.safeReturnTo(r.URL.Query().Get("return_to"))
	if returnTo == "" {
		returnTo = "/idp/login"
	}
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/idp/" {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.currentUser(r); ok {
		http.Redirect(w, r, "/idp/account", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/idp/login", http.StatusSeeOther)
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	returnTo := s.safeReturnTo(r.URL.Query().Get("return_to"))
	if _, ok := s.currentUser(r); ok {
		if returnTo != "" {
			http.Redirect(w, r, returnTo, http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/idp/account", http.StatusSeeOther)
		}
		return
	}
	s.render(w, r, "login", pageData{
		TitleKey:            "login.title",
		ErrorKey:            loginErrorKey(r.URL.Query().Get("error")),
		ReturnTo:            returnTo,
		CSRF:                s.ensureCSRF(w, r),
		RegistrationEnabled: s.opts.Registration.Enabled,
		Providers:           s.providerViews(nil),
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if !s.validateCSRF(r) {
		s.render(w, r, "login", pageData{
			TitleKey: "login.title",
			ErrorKey: "err.csrf_expired",
			CSRF:     s.ensureCSRF(w, r),
		})
		return
	}
	email := normalizeEmail(r.FormValue("email"))
	password := r.FormValue("password")
	returnTo := s.safeReturnTo(r.FormValue("return_to"))

	user, err := s.store.UserByEmail(r.Context(), email)
	if err != nil || !VerifyPassword(password, user.PasswordHash) {
		s.render(w, r, "login", pageData{
			TitleKey:            "login.title",
			ErrorKey:            "err.invalid_credentials",
			Email:               email,
			ReturnTo:            returnTo,
			CSRF:                s.ensureCSRF(w, r),
			RegistrationEnabled: s.opts.Registration.Enabled,
			Providers:           s.providerViews(nil),
		})
		return
	}
	if err := s.createSession(w, r, user.ID); err != nil {
		s.opts.Logger.Error("create login session", "error", err)
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	if returnTo == "" {
		returnTo = "/idp/account"
	}
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

func (s *Server) handleRegisterPage(w http.ResponseWriter, r *http.Request) {
	if !s.opts.Registration.Enabled {
		s.renderMessage(w, r, http.StatusForbidden, "err.registration_closed", "err.registration_closed_detail")
		return
	}
	if _, ok := s.currentUser(r); ok {
		http.Redirect(w, r, "/idp/account", http.StatusSeeOther)
		return
	}
	s.render(w, r, "register", pageData{
		TitleKey:            "register.title",
		ReturnTo:            s.safeReturnTo(r.URL.Query().Get("return_to")),
		CSRF:                s.ensureCSRF(w, r),
		RegistrationEnabled: true,
	})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.opts.Registration.Enabled {
		s.renderMessage(w, r, http.StatusForbidden, "err.registration_closed", "err.registration_closed_detail")
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	returnTo := s.safeReturnTo(r.FormValue("return_to"))
	email := normalizeEmail(r.FormValue("email"))
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	password := r.FormValue("password")
	confirm := r.FormValue("password_confirm")
	data := pageData{
		TitleKey:            "register.title",
		Email:               email,
		DisplayName:         displayName,
		ReturnTo:            returnTo,
		CSRF:                s.ensureCSRF(w, r),
		RegistrationEnabled: true,
	}
	if !s.validateCSRF(r) {
		data.ErrorKey = "err.csrf_expired"
		s.render(w, r, "register", data)
		return
	}
	if err := validateRegistration(email, displayName, password, confirm, s.opts.Registration); err != nil {
		var localized *translationError
		if errors.As(err, &localized) {
			data.ErrorKey = localized.key
			data.ErrorArgs = localized.args
		} else {
			data.Error = err.Error()
		}
		s.render(w, r, "register", data)
		return
	}
	hash, err := HashPassword(password)
	if err != nil {
		s.opts.Logger.Error("hash password", "error", err)
		http.Error(w, "could not create account", http.StatusInternalServerError)
		return
	}
	user, err := s.store.CreateUser(r.Context(), email, displayName, hash)
	if errors.Is(err, ErrConflict) {
		data.ErrorKey = "err.email_registered"
		s.render(w, r, "register", data)
		return
	}
	if err != nil {
		s.opts.Logger.Error("create account", "error", err)
		http.Error(w, "could not create account", http.StatusInternalServerError)
		return
	}
	if err := s.createSession(w, r, user.ID); err != nil {
		s.opts.Logger.Error("create registration session", "error", err)
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	if returnTo == "" {
		returnTo = "/idp/account"
	}
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !s.validateCSRF(r) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		_ = s.store.DeleteSession(r.Context(), hashToken(cookie.Value))
	}
	s.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/idp/login", http.StatusSeeOther)
}

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Redirect(w, r, "/idp/login?return_to=%2Fidp%2Faccount", http.StatusSeeOther)
		return
	}
	identities, err := s.store.ListIdentities(r.Context(), user.ID)
	if err != nil {
		s.opts.Logger.Error("list linked identities", "error", err)
		http.Error(w, "could not load account", http.StatusInternalServerError)
		return
	}
	byProvider := make(map[string]Identity, len(identities))
	for _, identity := range identities {
		byProvider[identity.Provider] = identity
	}
	errorKey, errorArgs, errorText := accountMessage(r.URL.Query().Get("error"))
	noticeKey, noticeArgs, noticeText := accountMessage(r.URL.Query().Get("notice"))
	if errorKey == "err.new_password_min" {
		errorArgs = []any{s.opts.Registration.PasswordMinLength}
	}
	s.render(w, r, "account", pageData{
		TitleKey:   "account.title",
		AdminPath:  s.opts.AdminPath,
		User:       &user,
		Identities: byProvider,
		Providers:  s.providerViews(byProvider),
		CSRF:       s.ensureCSRF(w, r),
		NoticeKey:  noticeKey,
		NoticeArgs: noticeArgs,
		Notice:     noticeText,
		ErrorKey:   errorKey,
		ErrorArgs:  errorArgs,
		Error:      errorText,
	})
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Redirect(w, r, "/idp/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil || !s.validateCSRF(r) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	confirm := r.FormValue("new_password_confirm")
	if !VerifyPassword(current, user.PasswordHash) {
		http.Redirect(w, r, "/idp/account?error=current_password", http.StatusSeeOther)
		return
	}
	if len(next) < s.opts.Registration.PasswordMinLength {
		http.Redirect(w, r, "/idp/account?error=new_password_short", http.StatusSeeOther)
		return
	}
	if next != confirm {
		http.Redirect(w, r, "/idp/account?error=new_password_mismatch", http.StatusSeeOther)
		return
	}
	hash, err := HashPassword(next)
	if err != nil {
		http.Error(w, "could not update password", http.StatusInternalServerError)
		return
	}
	if err := s.store.SetPassword(r.Context(), user.ID, hash); err != nil {
		http.Error(w, "could not update password", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/idp/account?notice=password_updated", http.StatusSeeOther)
}

func (s *Server) handleUnlink(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Redirect(w, r, "/idp/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil || !s.validateCSRF(r) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	provider := r.FormValue("provider")
	if _, exists := s.providers[provider]; !exists {
		http.Redirect(w, r, "/idp/account?error=provider_unknown", http.StatusSeeOther)
		return
	}
	if err := s.store.UnlinkIdentity(r.Context(), provider, user.ID); err != nil && !errors.Is(err, ErrNotFound) {
		http.Error(w, "could not unlink account", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/idp/account?notice=unlinked", http.StatusSeeOther)
}

func (s *Server) currentUser(r *http.Request) (User, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return User{}, false
	}
	user, err := s.store.UserBySession(r.Context(), hashToken(cookie.Value), time.Now())
	if err != nil {
		return User{}, false
	}
	return user, true
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request, userID string) error {
	if cookie, err := r.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		_ = s.store.DeleteSession(r.Context(), hashToken(cookie.Value))
	}
	token := randomToken(32)
	if err := s.store.CreateSession(r.Context(), hashToken(token), userID, time.Now().Add(sessionLifetime)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/idp",
		HttpOnly: true,
		Secure:   s.opts.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionLifetime.Seconds()),
	})
	return nil
}

func (s *Server) ensureCSRF(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie(csrfCookie); err == nil {
		if token, ok := s.verifySignedValue(cookie.Value); ok {
			return token
		}
	}
	token := randomToken(24)
	signed := token + "." + s.signValue(token)
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    signed,
		Path:     "/idp",
		HttpOnly: true,
		Secure:   s.opts.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   3600,
	})
	return token
}

func (s *Server) validateCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookie)
	if err != nil {
		return false
	}
	expected, ok := s.verifySignedValue(cookie.Value)
	if !ok {
		return false
	}
	actual := r.FormValue("csrf_token")
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func (s *Server) signValue(value string) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) verifySignedValue(value string) (string, bool) {
	payload, signature, ok := strings.Cut(value, ".")
	if !ok || payload == "" || signature == "" {
		return "", false
	}
	expected := s.signValue(payload)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return "", false
	}
	return payload, true
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/idp",
		HttpOnly: true,
		Secure:   s.opts.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *Server) providerViews(linked map[string]Identity) []providerView {
	result := make([]providerView, 0, len(s.providers))
	for _, provider := range s.opts.Providers {
		if _, ok := s.providers[provider.Name]; !ok {
			continue
		}
		view := providerView{Name: provider.Name}
		if identity, ok := linked[provider.Name]; ok {
			view.Linked = true
			view.LinkedEmail = identity.Email
		}
		result = append(result, view)
	}
	return result
}

func accountMessage(value string) (key string, args []any, text string) {
	if value == "" {
		return "", nil, ""
	}
	keys := map[string]string{
		"current_password":      "err.current_password",
		"new_password_short":    "err.new_password_min",
		"new_password_mismatch": "err.new_password_mismatch",
		"provider_unknown":      "err.provider_unknown",
		"link_failed":           "err.link_failed",
		"link_conflict":         "err.link_conflict",
		"password_updated":      "notice.password_updated",
		"unlinked":              "notice.unlinked",
		"linked":                "notice.linked",
	}
	if key, ok := keys[value]; ok {
		return key, nil, ""
	}
	return "", nil, value
}

func (s *Server) languageLinks(r *http.Request, data pageData, current string) []languageLink {
	returnTo := s.languageReturnTo(r, data)
	links := make([]languageLink, 0, 2)
	for _, locale := range []string{LocaleEnglish, LocaleChinese} {
		labelKey := "language.english"
		if locale == LocaleChinese {
			labelKey = "language.chinese"
		}
		query := url.Values{}
		query.Set("lang", locale)
		query.Set("return_to", returnTo)
		links = append(links, languageLink{
			Locale: locale,
			Label:  translate(current, labelKey),
			URL:    "/idp/language?" + query.Encode(),
			Active: locale == current,
		})
	}
	return links
}

func (s *Server) languageReturnTo(r *http.Request, data pageData) string {
	path := r.URL.Path
	switch path {
	case "/idp/login", "/idp/register":
		if data.ReturnTo != "" {
			query := url.Values{}
			query.Set("return_to", data.ReturnTo)
			return path + "?" + query.Encode()
		}
	case "/idp/account":
	default:
		return "/idp/login"
	}
	if r.Method == http.MethodGet && r.URL.RawQuery != "" {
		query := r.URL.Query()
		query.Del("lang")
		if encoded := query.Encode(); encoded != "" {
			return path + "?" + encoded
		}
	}
	return path
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data pageData) {
	s.renderStatus(w, r, http.StatusOK, name, data)
}

func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, status int, name string, data pageData) {
	locale := requestLocale(r)
	data.Locale = locale
	data.Lang = locale
	if data.TitleKey != "" {
		data.Title = data.T(data.TitleKey)
	}
	if data.ErrorKey != "" {
		data.Error = data.T(data.ErrorKey, data.ErrorArgs...)
	}
	if data.NoticeKey != "" {
		data.Notice = data.T(data.NoticeKey, data.NoticeArgs...)
	}
	data.LanguageLinks = s.languageLinks(r, data, locale)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Language", locale)
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		s.opts.Logger.Error("render identity page", "template", name, "error", err)
	}
}

func (s *Server) renderMessage(w http.ResponseWriter, r *http.Request, status int, titleKey, messageKey string, args ...any) {
	s.renderStatus(w, r, status, "message", pageData{
		TitleKey:  titleKey,
		ErrorKey:  messageKey,
		ErrorArgs: args,
	})
}

func (s *Server) safeReturnTo(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
		return ""
	}
	allowed := strings.HasPrefix(parsed.Path, "/idp/") || parsed.Path == s.opts.AdminPath || strings.HasPrefix(parsed.Path, s.opts.AdminPath+"/")
	if !allowed {
		return ""
	}
	return parsed.String()
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func loginErrorKey(code string) string {
	switch code {
	case "unbound":
		return "err.unbound"
	case "state":
		return "err.state_expired"
	case "provider_unavailable":
		return "err.provider_unavailable"
	default:
		return ""
	}
}

func validateRegistration(email, displayName, password, confirm string, cfg config.RegistrationConfig) error {
	if email == "" || !strings.Contains(email, "@") || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") || strings.ContainsAny(email, " \t\r\n") {
		return &translationError{key: "err.email_invalid"}
	}
	if displayName == "" || len(displayName) > 80 {
		return &translationError{key: "err.display_name_invalid"}
	}
	if len(password) < cfg.PasswordMinLength {
		return &translationError{key: "err.password_min", args: []any{cfg.PasswordMinLength}}
	}
	if password != confirm {
		return &translationError{key: "err.password_mismatch"}
	}
	if len(cfg.AllowedEmailDomains) > 0 {
		_, domain, ok := strings.Cut(email, "@")
		if !ok {
			return &translationError{key: "err.email_invalid"}
		}
		allowed := false
		for _, candidate := range cfg.AllowedEmailDomains {
			if strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(candidate), "@"), domain) {
				allowed = true
				break
			}
		}
		if !allowed {
			return &translationError{key: "err.email_domain"}
		}
	}
	return nil
}

func loadOrCreateSecret(path string, length int) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil && len(data) >= length {
		return data[:length], nil
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read secret: %w", err)
	}
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	if err := os.WriteFile(path, value, 0o600); err != nil {
		return nil, fmt.Errorf("write secret: %w", err)
	}
	return value, nil
}
