package webauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	sessionCookieName = "mercator_session"
	stateCookieName   = "mercator_oidc_state"

	// sessionTTL bounds how long a browser session lives before the operator
	// must re-authenticate with the issuer.
	sessionTTL = 24 * time.Hour
	// stateTTL bounds one login round-trip through the issuer.
	stateTTL = 10 * time.Minute
)

// session is the signed payload of the session cookie: who is signed in and
// until when. The signature (not the browser) is the integrity boundary.
type session struct {
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expires_at"`
}

// loginState is the signed payload of the short-lived cookie that carries one
// login attempt across the issuer redirect: the CSRF state, the ID-token nonce,
// and where to land after login.
type loginState struct {
	State     string    `json:"state"`
	Nonce     string    `json:"nonce"`
	Next      string    `json:"next"`
	ExpiresAt time.Time `json:"expires_at"`
}

// codec signs and verifies cookie payloads with HMAC-SHA256. Values are
// base64url(payload) + "." + base64url(mac); verification is constant-time.
type codec struct {
	key []byte
}

func (c codec) encode(v any) (string, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + c.sign(encoded), nil
}

func (c codec) decode(value string, v any) error {
	encoded, mac, found := strings.Cut(value, ".")
	if !found {
		return errors.New("webauth: malformed cookie value")
	}
	if !hmac.Equal([]byte(c.sign(encoded)), []byte(mac)) {
		return errors.New("webauth: cookie signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("webauth: decode cookie payload: %w", err)
	}
	return json.Unmarshal(payload, v)
}

func (c codec) sign(encoded string) string {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(encoded))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// requestIsTLS reports whether the client connection is effectively TLS: either
// terminated here or at a fronting proxy that sets X-Forwarded-Proto (as
// kamal-proxy and other TLS-terminating proxies do). Cookie Secure flags follow
// this, so deployed consoles behind a proxy get Secure cookies without
// hardcoding it in a way that would break plain-HTTP local evaluation.
func requestIsTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

func setCookie(w http.ResponseWriter, r *http.Request, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   requestIsTLS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   requestIsTLS(r),
		SameSite: http.SameSiteLaxMode,
	})
}
