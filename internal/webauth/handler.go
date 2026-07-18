package webauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Authenticator owns the OIDC login flow and the session cookie lifecycle. It
// serves the /auth/* endpoints and answers "which human is this request?" for
// the API auth gate.
type Authenticator struct {
	cfg      Config
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
	codec    codec
	mux      *http.ServeMux
	now      func() time.Time
}

// New discovers the issuer and builds the /auth/* handler. Discovery failure is
// an error: a deployment that configured OIDC must not silently boot without a
// human login surface.
func New(ctx context.Context, cfg Config) (*Authenticator, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("webauth: discover OIDC issuer %s: %w", cfg.Issuer, err)
	}
	a := &Authenticator{
		cfg:      cfg,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.PublicURL + "/auth/callback",
			Scopes:       []string{oidc.ScopeOpenID, "email"},
		},
		codec: codec{key: cfg.SessionKey},
		mux:   http.NewServeMux(),
		now:   time.Now,
	}
	a.mux.HandleFunc("GET /auth/login", a.login)
	a.mux.HandleFunc("GET /auth/callback", a.callback)
	a.mux.HandleFunc("POST /auth/logout", a.logout)
	a.mux.HandleFunc("GET /auth/session", a.session)
	return a, nil
}

func (a *Authenticator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, r)
}

// SessionEmail returns the signed-in email for a request carrying a valid,
// unexpired session cookie.
func (a *Authenticator) SessionEmail(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}
	var s session
	if err := a.codec.decode(cookie.Value, &s); err != nil {
		return "", false
	}
	if s.Email == "" || a.now().After(s.ExpiresAt) {
		return "", false
	}
	return s.Email, true
}

func (a *Authenticator) login(w http.ResponseWriter, r *http.Request) {
	state := randomHex()
	nonce := randomHex()
	value, err := a.codec.encode(loginState{
		State:     state,
		Nonce:     nonce,
		Next:      sanitizeNext(r.URL.Query().Get("next")),
		ExpiresAt: a.now().Add(stateTTL),
	})
	if err != nil {
		http.Error(w, "failed to start login", http.StatusInternalServerError)
		return
	}
	setCookie(w, r, stateCookieName, value, stateTTL)
	http.Redirect(w, r, a.oauth.AuthCodeURL(state, oidc.Nonce(nonce)), http.StatusFound)
}

func (a *Authenticator) callback(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(stateCookieName)
	if err != nil {
		a.loginFailed(w, r, http.StatusBadRequest, "login flow expired or was not started; retry via /auth/login", err)
		return
	}
	var st loginState
	if err := a.codec.decode(cookie.Value, &st); err != nil {
		a.loginFailed(w, r, http.StatusBadRequest, "login state cookie is invalid; retry via /auth/login", err)
		return
	}
	if a.now().After(st.ExpiresAt) {
		a.loginFailed(w, r, http.StatusBadRequest, "login flow expired; retry via /auth/login", nil)
		return
	}
	if r.URL.Query().Get("state") != st.State {
		a.loginFailed(w, r, http.StatusBadRequest, "login state mismatch; retry via /auth/login", nil)
		return
	}
	token, err := a.oauth.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		a.loginFailed(w, r, http.StatusBadGateway, "code exchange with the OIDC issuer failed", err)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		a.loginFailed(w, r, http.StatusBadGateway, "OIDC issuer returned no id_token", nil)
		return
	}
	idToken, err := a.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		a.loginFailed(w, r, http.StatusUnauthorized, "ID token verification failed", err)
		return
	}
	if idToken.Nonce != st.Nonce {
		a.loginFailed(w, r, http.StatusUnauthorized, "ID token nonce mismatch", nil)
		return
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified *bool  `json:"email_verified"`
	}
	if err := idToken.Claims(&claims); err != nil || claims.Email == "" {
		a.loginFailed(w, r, http.StatusForbidden, "ID token carries no email claim", err)
		return
	}
	if claims.EmailVerified != nil && !*claims.EmailVerified {
		a.loginFailed(w, r, http.StatusForbidden, "email is not verified with the issuer", nil)
		return
	}
	if !a.cfg.emailAllowed(claims.Email) {
		a.loginFailed(w, r, http.StatusForbidden, "this identity is not on the sign-in allowlist", nil)
		return
	}
	value, err := a.codec.encode(session{Email: claims.Email, ExpiresAt: a.now().Add(sessionTTL)})
	if err != nil {
		a.loginFailed(w, r, http.StatusInternalServerError, "failed to establish session", err)
		return
	}
	clearCookie(w, r, stateCookieName)
	setCookie(w, r, sessionCookieName, value, sessionTTL)
	http.Redirect(w, r, st.Next, http.StatusSeeOther)
}

func (a *Authenticator) logout(w http.ResponseWriter, r *http.Request) {
	clearCookie(w, r, sessionCookieName)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// session reports login availability and the current identity so the console
// can decide between the OIDC flow and the token fallback without guessing.
func (a *Authenticator) session(w http.ResponseWriter, r *http.Request) {
	response := map[string]any{"enabled": true}
	if email, ok := a.SessionEmail(r); ok {
		response["email"] = email
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(response)
}

// loginFailed reports a failed login attempt. The detailed error goes to the
// server log; the browser gets the short reason (these are human-read pages,
// not API responses).
func (a *Authenticator) loginFailed(w http.ResponseWriter, r *http.Request, status int, reason string, err error) {
	if err != nil {
		log.Printf("webauth: %d %s: %v", status, reason, err)
	} else {
		log.Printf("webauth: %d %s", status, reason)
	}
	clearCookie(w, r, stateCookieName)
	http.Error(w, reason, status)
}

// sanitizeNext confines the post-login redirect to a local path so the login
// endpoint cannot be used as an open redirect.
func sanitizeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

func randomHex() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// The system RNG failing is not recoverable for a security token.
		panic(fmt.Sprintf("webauth: system RNG unavailable: %v", err))
	}
	return hex.EncodeToString(buf)
}
