package idp

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	authorizationCodeLifetime = 5 * time.Minute
	accessTokenLifetime       = time.Hour
)

func (s *Server) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	issuer := s.opts.Issuer
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/authorize",
		"token_endpoint":                        issuer + "/token",
		"userinfo_endpoint":                     issuer + "/userinfo",
		"jwks_uri":                              issuer + "/jwks.json",
		"response_types_supported":              []string{"code"},
		"response_modes_supported":              []string{"query"},
		"grant_types_supported":                 []string{"authorization_code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"claims_supported": []string{
			"iss", "sub", "aud", "exp", "iat", "auth_time", "nonce",
			"name", "preferred_username", "email", "email_verified",
		},
		"code_challenge_methods_supported": []string{"S256"},
	})
}

func (s *Server) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"keys": []any{s.signer.jwk()}})
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	clientID := query.Get("client_id")
	redirectURI := query.Get("redirect_uri")
	responseType := query.Get("response_type")
	scope := query.Get("scope")
	state := query.Get("state")
	nonce := query.Get("nonce")
	challenge := query.Get("code_challenge")
	challengeMethod := query.Get("code_challenge_method")

	if clientID != s.opts.ClientID {
		s.renderMessage(w, r, http.StatusBadRequest, "err.oidc_invalid", "err.unknown_client")
		return
	}
	if !s.allowedRedirectURI(redirectURI) {
		s.renderMessage(w, r, http.StatusBadRequest, "err.oidc_invalid", "err.redirect_uri")
		return
	}
	if responseType != "code" {
		redirectOAuthError(w, r, redirectURI, state, "unsupported_response_type", "only response_type=code is supported")
		return
	}
	if !scopeContains(scope, "openid") {
		redirectOAuthError(w, r, redirectURI, state, "invalid_scope", "the openid scope is required")
		return
	}
	if challenge != "" && (challengeMethod != "S256" || len(challenge) < 43) {
		redirectOAuthError(w, r, redirectURI, state, "invalid_request", "only S256 PKCE challenges are supported")
		return
	}

	user, ok := s.currentUser(r)
	if !ok {
		returnTo := r.URL.RequestURI()
		http.Redirect(w, r, "/idp/login?return_to="+url.QueryEscape(returnTo), http.StatusSeeOther)
		return
	}

	code := randomToken(32)
	if err := s.store.CreateAuthCode(r.Context(), AuthCode{
		CodeHash:            hashToken(code),
		ClientID:            clientID,
		UserID:              user.ID,
		RedirectURI:         redirectURI,
		Scope:               scope,
		Nonce:               nonce,
		CodeChallenge:       challenge,
		CodeChallengeMethod: challengeMethod,
		ExpiresAt:           time.Now().Add(authorizationCodeLifetime),
	}); err != nil {
		s.opts.Logger.Error("create authorization code", "error", err)
		http.Error(w, "could not create authorization code", http.StatusInternalServerError)
		return
	}

	target, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect URI", http.StatusInternalServerError)
		return
	}
	values := target.Query()
	values.Set("code", code)
	if state != "" {
		values.Set("state", state)
	}
	target.RawQuery = values.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if err := r.ParseForm(); err != nil {
		oauthJSONError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
		return
	}
	if r.FormValue("grant_type") != "authorization_code" {
		oauthJSONError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code is supported")
		return
	}
	clientID, clientSecret, ok := s.tokenClientCredentials(r)
	if !ok {
		oauthJSONError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}
	if subtle.ConstantTimeCompare([]byte(clientID), []byte(s.opts.ClientID)) != 1 ||
		subtle.ConstantTimeCompare([]byte(clientSecret), []byte(s.opts.ClientSecret)) != 1 {
		oauthJSONError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}

	codeValue := r.FormValue("code")
	code, err := s.store.ConsumeAuthCode(r.Context(), hashToken(codeValue), time.Now())
	if err != nil {
		switch {
		case errors.Is(err, ErrExpired):
			oauthJSONError(w, http.StatusBadRequest, "invalid_grant", "authorization code expired")
		case errors.Is(err, ErrConsumed):
			oauthJSONError(w, http.StatusBadRequest, "invalid_grant", "authorization code was already used")
		default:
			oauthJSONError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid")
		}
		return
	}
	if code.ClientID != clientID {
		oauthJSONError(w, http.StatusBadRequest, "invalid_grant", "authorization code belongs to another client")
		return
	}
	if redirectURI := r.FormValue("redirect_uri"); redirectURI != "" && redirectURI != code.RedirectURI {
		oauthJSONError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match")
		return
	}
	if code.CodeChallenge != "" {
		verifier := r.FormValue("code_verifier")
		if !verifyPKCE(verifier, code.CodeChallenge) {
			oauthJSONError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
			return
		}
	}

	user, err := s.store.UserByID(r.Context(), code.UserID)
	if err != nil {
		oauthJSONError(w, http.StatusBadRequest, "invalid_grant", "account no longer exists")
		return
	}
	accessToken := randomToken(32)
	if err := s.store.CreateAccessToken(r.Context(), hashToken(accessToken), user.ID, code.Scope, time.Now().Add(accessTokenLifetime)); err != nil {
		s.opts.Logger.Error("create OIDC access token", "error", err)
		oauthJSONError(w, http.StatusInternalServerError, "server_error", "could not create access token")
		return
	}
	idToken, err := s.issueIDToken(user, clientID, code.Nonce)
	if err != nil {
		s.opts.Logger.Error("sign OIDC ID token", "error", err)
		oauthJSONError(w, http.StatusInternalServerError, "server_error", "could not sign ID token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   int(accessTokenLifetime.Seconds()),
		"id_token":     idToken,
		"scope":        code.Scope,
	})
}

func (s *Server) handleUserInfo(w http.ResponseWriter, r *http.Request) {
	header := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="headscale-console"`)
		oauthJSONError(w, http.StatusUnauthorized, "invalid_token", "a bearer token is required")
		return
	}
	user, err := s.store.UserByAccessToken(r.Context(), hashToken(strings.TrimSpace(token)), time.Now())
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		oauthJSONError(w, http.StatusUnauthorized, "invalid_token", "access token is invalid or expired")
		return
	}
	writeJSON(w, http.StatusOK, userClaims(user))
}

func (s *Server) issueIDToken(user User, audience, nonce string) (string, error) {
	now := time.Now().UTC()
	claims := jwt.MapClaims(userClaims(user))
	claims["iss"] = s.opts.Issuer
	claims["aud"] = audience
	claims["iat"] = now.Unix()
	claims["exp"] = now.Add(time.Hour).Unix()
	claims["auth_time"] = now.Unix()
	claims["jti"] = randomToken(16)
	if nonce != "" {
		claims["nonce"] = nonce
	}
	return s.signer.sign(claims)
}

func userClaims(user User) map[string]any {
	username := user.Email
	if local, _, ok := strings.Cut(user.Email, "@"); ok && local != "" {
		username = local
	}
	return map[string]any{
		"sub":                user.ID,
		"name":               user.DisplayName,
		"preferred_username": username,
		"email":              user.Email,
		"email_verified":     true,
	}
}

func (s *Server) tokenClientCredentials(r *http.Request) (string, string, bool) {
	if username, password, ok := r.BasicAuth(); ok {
		decodedID, err := url.QueryUnescape(username)
		if err == nil {
			username = decodedID
		}
		decodedSecret, err := url.QueryUnescape(password)
		if err == nil {
			password = decodedSecret
		}
		return username, password, true
	}
	clientID := r.FormValue("client_id")
	clientSecret := r.FormValue("client_secret")
	return clientID, clientSecret, clientID != "" && clientSecret != ""
}

func (s *Server) allowedRedirectURI(value string) bool {
	return value == s.opts.BaseURL+s.opts.AdminPath+"/oidc/callback" ||
		value == s.opts.BaseURL+"/oidc/callback"
}

func verifyPKCE(verifier, challenge string) bool {
	if verifier == "" || len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	digest := sha256.Sum256([]byte(verifier))
	actual := base64.RawURLEncoding.EncodeToString(digest[:])
	return subtle.ConstantTimeCompare([]byte(actual), []byte(challenge)) == 1
}

func scopeContains(scope, expected string) bool {
	for _, item := range strings.Fields(scope) {
		if item == expected {
			return true
		}
	}
	return false
}

func redirectOAuthError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, description string) {
	target, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect URI", http.StatusBadRequest)
		return
	}
	values := target.Query()
	values.Set("error", code)
	values.Set("error_description", description)
	if state != "" {
		values.Set("state", state)
	}
	target.RawQuery = values.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func oauthJSONError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func debugRedirectURI(baseURL, adminPath string) string {
	return fmt.Sprintf("%s%s/oidc/callback", baseURL, adminPath)
}
