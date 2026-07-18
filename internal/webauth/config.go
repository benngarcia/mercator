// Package webauth implements service-local human authentication for the
// Mercator console: a standard OIDC authorization-code flow against a
// configurable issuer, an email/domain allowlist, and a signed HTTP-only
// session cookie. Machine principals (the static API bearer token and the
// per-run report token) are untouched; webauth only adds a second way for a
// request to carry a principal.
package webauth

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Config carries the OIDC + session settings for human login. The zero value
// means "not configured": no human login surface exists and the server behaves
// exactly as a token-only deployment.
type Config struct {
	// Issuer is the OIDC issuer URL (e.g. https://accounts.google.com). Any
	// spec-compliant issuer works; there are no issuer-specific code paths.
	Issuer       string
	ClientID     string
	ClientSecret string
	// AllowedDomains and AllowedEmails form the sign-in allowlist. An
	// authenticated identity is admitted when its email's domain matches any
	// allowed domain OR the full address matches any allowed email.
	AllowedDomains []string
	AllowedEmails  []string
	// SessionKey signs the session and login-state cookies (HMAC-SHA256).
	SessionKey []byte
	// PublicURL is the externally reachable base URL of this server; the OIDC
	// redirect URI is PublicURL + "/auth/callback".
	PublicURL string
}

func (c Config) Enabled() bool {
	return c.Issuer != "" || c.ClientID != "" || c.ClientSecret != ""
}

// FromEnv builds a Config from the MERCATOR_OIDC_* environment. Configuration
// is fail-closed: when no OIDC variable is set the returned config is disabled
// and valid; when any is set, everything required for a safe deployment
// (issuer, client, allowlist, session key, public URL) must be present or an
// error describing every missing piece is returned.
func FromEnv(values map[string]string) (Config, error) {
	cfg := Config{
		Issuer:         values["MERCATOR_OIDC_ISSUER"],
		ClientID:       values["MERCATOR_OIDC_CLIENT_ID"],
		ClientSecret:   values["MERCATOR_OIDC_CLIENT_SECRET"],
		AllowedDomains: splitList(values["MERCATOR_OIDC_ALLOWED_DOMAIN"]),
		AllowedEmails:  splitList(values["MERCATOR_OIDC_ALLOWED_EMAILS"]),
		PublicURL:      strings.TrimRight(values["MERCATOR_PUBLIC_URL"], "/"),
	}
	if !cfg.Enabled() {
		if len(cfg.AllowedDomains) > 0 || len(cfg.AllowedEmails) > 0 || values["MERCATOR_SESSION_KEY"] != "" {
			return Config{}, errors.New("webauth: MERCATOR_OIDC_ALLOWED_DOMAIN/MERCATOR_OIDC_ALLOWED_EMAILS/MERCATOR_SESSION_KEY are set but MERCATOR_OIDC_ISSUER is not; set the full MERCATOR_OIDC_* config or unset them")
		}
		return Config{}, nil
	}

	var missing []string
	if cfg.Issuer == "" {
		missing = append(missing, "MERCATOR_OIDC_ISSUER")
	}
	if cfg.ClientID == "" {
		missing = append(missing, "MERCATOR_OIDC_CLIENT_ID")
	}
	if cfg.ClientSecret == "" {
		missing = append(missing, "MERCATOR_OIDC_CLIENT_SECRET")
	}
	if len(cfg.AllowedDomains) == 0 && len(cfg.AllowedEmails) == 0 {
		missing = append(missing, "MERCATOR_OIDC_ALLOWED_DOMAIN or MERCATOR_OIDC_ALLOWED_EMAILS")
	}
	if cfg.PublicURL == "" {
		missing = append(missing, "MERCATOR_PUBLIC_URL (needed for the OIDC redirect URI)")
	}
	key, keyErr := decodeSessionKey(values["MERCATOR_SESSION_KEY"])
	if keyErr != nil {
		missing = append(missing, keyErr.Error())
	}
	cfg.SessionKey = key
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("webauth: OIDC login is partially configured; fix: %s", strings.Join(missing, "; "))
	}
	return cfg, nil
}

// decodeSessionKey accepts a hex- or base64-encoded key and requires at least
// 32 decoded bytes, mirroring how MERCATOR_SECRET_KEY is supplied.
func decodeSessionKey(raw string) ([]byte, error) {
	if raw == "" {
		return nil, errors.New("MERCATOR_SESSION_KEY (32+ random bytes, hex or base64)")
	}
	var key []byte
	if b, err := hex.DecodeString(raw); err == nil {
		key = b
	} else if b, err := base64.StdEncoding.DecodeString(raw); err == nil {
		key = b
	} else {
		return nil, errors.New("MERCATOR_SESSION_KEY must be hex or base64")
	}
	if len(key) < 32 {
		return nil, errors.New("MERCATOR_SESSION_KEY must decode to at least 32 bytes")
	}
	return key, nil
}

// emailAllowed reports whether an authenticated email passes the allowlist.
// Matching is case-insensitive; domains match the part after the last "@".
func (c Config) emailAllowed(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return false
	}
	for _, allowed := range c.AllowedEmails {
		if email == strings.ToLower(allowed) {
			return true
		}
	}
	domain := email[at+1:]
	for _, allowed := range c.AllowedDomains {
		if domain == strings.ToLower(allowed) {
			return true
		}
	}
	return false
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
