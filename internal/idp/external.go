package idp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/guanzi008/headscale-console/internal/config"
	"golang.org/x/oauth2"
)

type externalIdentity struct {
	Subject string
	Email   string
	Name    string
	Picture string
}

type externalProvider interface {
	Name() string
	UsesPKCE() bool
	AuthCodeURL(state, nonce, verifier string) string
	Exchange(ctx context.Context, code, verifier, nonce string) (externalIdentity, error)
}

type oidcExternalProvider struct {
	name     string
	oauth    oauth2.Config
	provider *oidc.Provider
}

func (p *oidcExternalProvider) Name() string   { return p.name }
func (p *oidcExternalProvider) UsesPKCE() bool { return true }

func (p *oidcExternalProvider) AuthCodeURL(state, nonce, verifier string) string {
	return p.oauth.AuthCodeURL(state,
		oauth2.AccessTypeOnline,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	)
}

func (p *oidcExternalProvider) Exchange(ctx context.Context, code, verifier, nonce string) (externalIdentity, error) {
	token, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return externalIdentity{}, fmt.Errorf("exchange OIDC code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return externalIdentity{}, fmt.Errorf("OIDC provider returned no id_token")
	}
	idToken, err := p.provider.Verifier(&oidc.Config{ClientID: p.oauth.ClientID}).Verify(ctx, rawIDToken)
	if err != nil {
		return externalIdentity{}, fmt.Errorf("verify upstream ID token: %w", err)
	}
	var claims struct {
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Picture           string `json:"picture"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return externalIdentity{}, fmt.Errorf("parse upstream ID token claims: %w", err)
	}
	if idToken.Subject == "" {
		return externalIdentity{}, fmt.Errorf("OIDC provider returned an empty subject")
	}

	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}
	email := claims.Email
	if email == "" || !claims.EmailVerified {
		if userInfo, err := p.provider.UserInfo(ctx, oauth2.StaticTokenSource(token)); err == nil {
			if email == "" && userInfo.Email != "" {
				email = userInfo.Email
			}
			if name == "" && userInfo.Profile != "" {
				name = userInfo.Profile
			}
		}
	}
	return externalIdentity{
		Subject: idToken.Subject,
		Email:   normalizeEmail(email),
		Name:    strings.TrimSpace(name),
		Picture: claims.Picture,
	}, nil
}

type githubProvider struct {
	name  string
	oauth oauth2.Config
}

func (p *githubProvider) Name() string   { return p.name }
func (p *githubProvider) UsesPKCE() bool { return false }

func (p *githubProvider) AuthCodeURL(state, _, _ string) string {
	return p.oauth.AuthCodeURL(state, oauth2.AccessTypeOnline)
}

func (p *githubProvider) Exchange(ctx context.Context, code, _, _ string) (externalIdentity, error) {
	token, err := p.oauth.Exchange(ctx, code)
	if err != nil {
		return externalIdentity{}, fmt.Errorf("exchange GitHub code: %w", err)
	}
	client := p.oauth.Client(ctx, token)
	var profile struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := githubJSON(ctx, client, "https://api.github.com/user", &profile); err != nil {
		return externalIdentity{}, err
	}
	if profile.ID == 0 {
		return externalIdentity{}, fmt.Errorf("GitHub returned no user id")
	}
	email := normalizeEmail(profile.Email)
	if email == "" {
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if err := githubJSON(ctx, client, "https://api.github.com/user/emails", &emails); err == nil {
			for _, candidate := range emails {
				if candidate.Primary && candidate.Verified {
					email = normalizeEmail(candidate.Email)
					break
				}
			}
		}
	}
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		name = profile.Login
	}
	return externalIdentity{
		Subject: strconv.FormatInt(profile.ID, 10),
		Email:   email,
		Name:    name,
		Picture: profile.AvatarURL,
	}, nil
}

func githubJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build GitHub request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "headscale-console")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("call GitHub API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("GitHub API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func newExternalProvider(ctx context.Context, cfg config.OAuthProviderConfig, baseURL string) (externalProvider, error) {
	redirectURL := fmt.Sprintf("%s/idp/oauth/%s/callback", baseURL, cfg.Name)
	switch cfg.Type {
	case "github":
		return &githubProvider{
			name: cfg.Name,
			oauth: oauth2.Config{
				ClientID:     cfg.ClientID,
				ClientSecret: cfg.ClientSecret,
				RedirectURL:  redirectURL,
				Endpoint: oauth2.Endpoint{
					AuthURL:  "https://github.com/login/oauth/authorize",
					TokenURL: "https://github.com/login/oauth/access_token",
				},
				Scopes: []string{"read:user", "user:email"},
			},
		}, nil
	case "oidc":
		discoveryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		provider, err := oidc.NewProvider(discoveryCtx, cfg.Issuer)
		if err != nil {
			return nil, fmt.Errorf("discover OIDC issuer: %w", err)
		}
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{oidc.ScopeOpenID, "profile", "email"}
		}
		return &oidcExternalProvider{
			name:     cfg.Name,
			provider: provider,
			oauth: oauth2.Config{
				ClientID:     cfg.ClientID,
				ClientSecret: cfg.ClientSecret,
				RedirectURL:  redirectURL,
				Endpoint:     provider.Endpoint(),
				Scopes:       scopes,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider type %q", cfg.Type)
	}
}

func (s *Server) handleStartExternalLogin(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")
	provider, ok := s.providers[providerName]
	if !ok {
		http.Redirect(w, r, "/idp/login?error=provider_unavailable", http.StatusSeeOther)
		return
	}
	s.startExternalFlow(w, r, provider, "", s.safeReturnTo(r.URL.Query().Get("next")))
}

func (s *Server) handleStartLink(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Redirect(w, r, "/idp/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil || !s.validateCSRF(r) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	providerName := r.FormValue("provider")
	provider, ok := s.providers[providerName]
	if !ok {
		http.Redirect(w, r, "/idp/account?error=provider_unknown", http.StatusSeeOther)
		return
	}
	s.startExternalFlow(w, r, provider, user.ID, "")
}

func (s *Server) startExternalFlow(w http.ResponseWriter, r *http.Request, provider externalProvider, userID, next string) {
	state := randomToken(32)
	verifier := ""
	if provider.UsesPKCE() {
		verifier = randomToken(48)
	}
	nonce := randomToken(24)
	if err := s.store.CreateOAuthState(r.Context(), OAuthState{
		State:     state,
		Provider:  provider.Name(),
		UserID:    userID,
		Next:      next,
		Verifier:  verifier,
		Nonce:     nonce,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}); err != nil {
		s.opts.Logger.Error("create external OAuth state", "provider", provider.Name(), "error", err)
		http.Error(w, "could not start external login", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    state,
		Path:     "/idp",
		HttpOnly: true,
		Secure:   s.opts.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	http.Redirect(w, r, provider.AuthCodeURL(state, nonce, verifier), http.StatusSeeOther)
}

func (s *Server) handleExternalCallback(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")
	provider, ok := s.providers[providerName]
	if !ok {
		s.renderMessage(w, r, http.StatusBadRequest, "err.oauth_not_found", "err.oauth_not_found_detail")
		return
	}
	if oauthError := r.URL.Query().Get("error"); oauthError != "" {
		description := r.URL.Query().Get("error_description")
		s.opts.Logger.Warn("external provider returned an error", "provider", providerName, "error", oauthError, "description", description)
		s.renderMessage(w, r, http.StatusBadRequest, "err.oauth_denied", "err.oauth_denied_detail")
		return
	}
	stateValue := r.URL.Query().Get("state")
	cookie, err := r.Cookie(oauthStateCookie)
	if err != nil || stateValue == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(stateValue)) != 1 {
		http.Redirect(w, r, "/idp/login?error=state", http.StatusSeeOther)
		return
	}
	s.clearCookie(w, oauthStateCookie)
	state, err := s.store.ConsumeOAuthState(r.Context(), stateValue, time.Now())
	if err != nil || state.Provider != providerName {
		http.Redirect(w, r, "/idp/login?error=state", http.StatusSeeOther)
		return
	}
	identity, err := provider.Exchange(r.Context(), r.URL.Query().Get("code"), state.Verifier, state.Nonce)
	if err != nil {
		s.opts.Logger.Warn("external identity exchange failed", "provider", providerName, "error", err)
		s.renderMessage(w, r, http.StatusBadGateway, "err.oauth_failed", "err.oauth_failed_detail")
		return
	}
	if identity.Email == "" {
		s.opts.Logger.Warn("external provider returned no email", "provider", providerName, "subject", identity.Subject)
	}

	if state.UserID != "" {
		user, ok := s.currentUser(r)
		if !ok || user.ID != state.UserID {
			http.Redirect(w, r, "/idp/login", http.StatusSeeOther)
			return
		}
		if err := s.store.LinkIdentity(r.Context(), providerName, identity.Subject, user.ID, identity.Email); err != nil {
			code := "link_failed"
			if errors.Is(err, ErrConflict) {
				code = "link_conflict"
			}
			http.Redirect(w, r, "/idp/account?error="+code, http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/idp/account?notice=linked", http.StatusSeeOther)
		return
	}

	linked, err := s.store.IdentityBySubject(r.Context(), providerName, identity.Subject)
	if errors.Is(err, ErrNotFound) {
		http.Redirect(w, r, "/idp/login?error=unbound", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.opts.Logger.Error("find linked identity", "error", err)
		http.Error(w, "could not load linked account", http.StatusInternalServerError)
		return
	}
	user, err := s.store.UserByID(r.Context(), linked.UserID)
	if err != nil {
		http.Error(w, "linked account no longer exists", http.StatusBadRequest)
		return
	}
	if err := s.createSession(w, r, user.ID); err != nil {
		s.opts.Logger.Error("create external login session", "error", err)
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	next := s.safeReturnTo(state.Next)
	if next == "" {
		next = "/idp/account"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}
