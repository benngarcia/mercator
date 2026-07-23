package webauth

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// LocalAuthenticator establishes one development identity without an external
// identity provider. The caller must enforce a loopback-only listener.
type LocalAuthenticator struct {
	email string
	codec codec
	mux   *http.ServeMux
	now   func() time.Time
}

// NewLocal builds the loopback development login surface. Its signing key is
// process-local, so restarting the server invalidates every development
// session and the next browser request establishes a fresh one.
func NewLocal(email string) (*LocalAuthenticator, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, fmt.Errorf("webauth: local email is required")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("webauth: generate local session key: %w", err)
	}
	a := &LocalAuthenticator{
		email: email,
		codec: codec{key: key},
		mux:   http.NewServeMux(),
		now:   time.Now,
	}
	a.mux.HandleFunc("GET /auth/login", a.login)
	a.mux.HandleFunc("POST /auth/logout", a.logout)
	a.mux.HandleFunc("GET /auth/session", a.session)
	return a, nil
}

func (a *LocalAuthenticator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, r)
}

func (a *LocalAuthenticator) SessionEmail(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}
	var value session
	if err := a.codec.decode(cookie.Value, &value); err != nil {
		return "", false
	}
	if value.Kind != kindSession || value.Email != a.email || a.now().After(value.ExpiresAt) {
		return "", false
	}
	return value.Email, true
}

func (a *LocalAuthenticator) VerifyCLIToken(string) (string, bool) {
	return "", false
}

func (a *LocalAuthenticator) login(w http.ResponseWriter, r *http.Request) {
	if err := a.establishSession(w, r); err != nil {
		http.Error(w, "failed to establish local session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, sanitizeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
}

func (a *LocalAuthenticator) logout(w http.ResponseWriter, r *http.Request) {
	clearCookie(w, r, sessionCookieName)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *LocalAuthenticator) session(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.SessionEmail(r); !ok {
		if err := a.establishSession(w, r); err != nil {
			http.Error(w, "failed to establish local session", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"mode":    "local",
		"enabled": true,
		"email":   a.email,
	})
}

func (a *LocalAuthenticator) establishSession(w http.ResponseWriter, r *http.Request) error {
	value, err := a.codec.encode(session{
		Kind:      kindSession,
		Email:     a.email,
		ExpiresAt: a.now().Add(sessionTTL),
	})
	if err != nil {
		return err
	}
	setCookie(w, r, sessionCookieName, value, sessionTTL)
	return nil
}
