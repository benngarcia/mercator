package webauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeIssuer is a minimal spec-compliant OIDC issuer: discovery, JWKS, and a
// token endpoint that signs RS256 ID tokens for whatever identity the test
// configures. It lets the real go-oidc verification path run end to end
// without a network.
type fakeIssuer struct {
	server   *httptest.Server
	key      *rsa.PrivateKey
	clientID string
	email    string
	verified bool
	nonce    string
}

func newFakeIssuer(t *testing.T, clientID string) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate issuer key: %v", err)
	}
	issuer := &fakeIssuer{key: key, clientID: clientID, verified: true}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                issuer.server.URL,
			"authorization_endpoint":                issuer.server.URL + "/authorize",
			"token_endpoint":                        issuer.server.URL + "/token",
			"jwks_uri":                              issuer.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := issuer.key.Public().(*rsa.PublicKey)
		writeJSON(w, map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test",
				"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"id_token":     issuer.signIDToken(t),
		})
	})
	issuer.server = httptest.NewServer(mux)
	t.Cleanup(issuer.server.Close)
	return issuer
}

func (f *fakeIssuer) signIDToken(t *testing.T) string {
	t.Helper()
	now := time.Now()
	header := map[string]any{"alg": "RS256", "kid": "test"}
	claims := map[string]any{
		"iss":            f.server.URL,
		"aud":            f.clientID,
		"sub":            "subject-1",
		"iat":            now.Unix(),
		"exp":            now.Add(time.Hour).Unix(),
		"nonce":          f.nonce,
		"email":          f.email,
		"email_verified": f.verified,
	}
	encode := func(v any) string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal jwt part: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	signingInput := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func testConfig(issuer *fakeIssuer) Config {
	return Config{
		Issuer:         issuer.server.URL,
		ClientID:       issuer.clientID,
		ClientSecret:   "client-secret",
		AllowedDomains: []string{"example.com"},
		AllowedEmails:  []string{"guest@partner.dev"},
		SessionKey:     []byte(strings.Repeat("k", 32)),
		PublicURL:      "http://127.0.0.1:8080",
	}
}

// driveLogin performs the full login round-trip against the fake issuer and
// returns the callback response and the login redirect it followed.
func driveLogin(t *testing.T, a *Authenticator, issuer *fakeIssuer, next string) *httptest.ResponseRecorder {
	t.Helper()
	loginTarget := "/auth/login"
	if next != "" {
		loginTarget += "?next=" + url.QueryEscape(next)
	}
	loginRec := httptest.NewRecorder()
	a.ServeHTTP(loginRec, httptest.NewRequest(http.MethodGet, loginTarget, nil))
	if loginRec.Code != http.StatusFound {
		t.Fatalf("login expected 302, got %d body=%s", loginRec.Code, loginRec.Body.String())
	}
	authorizeURL, err := url.Parse(loginRec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse authorize redirect: %v", err)
	}
	if got := authorizeURL.Query().Get("redirect_uri"); got != "http://127.0.0.1:8080/auth/callback" {
		t.Fatalf("unexpected redirect_uri %q", got)
	}
	// The issuer embeds the request's nonce in the ID token it will sign.
	issuer.nonce = authorizeURL.Query().Get("nonce")

	callback := httptest.NewRequest(http.MethodGet,
		"/auth/callback?code=test-code&state="+url.QueryEscape(authorizeURL.Query().Get("state")), nil)
	for _, cookie := range loginRec.Result().Cookies() {
		callback.AddCookie(cookie)
	}
	callbackRec := httptest.NewRecorder()
	a.ServeHTTP(callbackRec, callback)
	return callbackRec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.Value != "" {
			return cookie
		}
	}
	return nil
}

func TestLoginCallbackEstablishesSessionForAllowedDomain(t *testing.T) {
	issuer := newFakeIssuer(t, "client-1")
	issuer.email = "operator@example.com"
	a, err := New(context.Background(), testConfig(issuer))
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	rec := driveLogin(t, a, issuer, "/runs/run_1")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback expected 303, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/runs/run_1" {
		t.Fatalf("callback should land on the deep link, got %q", got)
	}
	cookie := sessionCookie(t, rec)
	if cookie == nil {
		t.Fatalf("callback did not set a session cookie")
	}
	if !cookie.HttpOnly {
		t.Fatalf("session cookie must be HttpOnly")
	}

	authed := httptest.NewRequest(http.MethodGet, "/", nil)
	authed.AddCookie(cookie)
	email, ok := a.SessionEmail(authed)
	if !ok || email != "operator@example.com" {
		t.Fatalf("expected session for operator@example.com, got %q ok=%v", email, ok)
	}
}

func TestLoginCallbackAdmitsExplicitlyAllowedEmail(t *testing.T) {
	issuer := newFakeIssuer(t, "client-1")
	issuer.email = "guest@partner.dev"
	a, err := New(context.Background(), testConfig(issuer))
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	rec := driveLogin(t, a, issuer, "")
	if rec.Code != http.StatusSeeOther || sessionCookie(t, rec) == nil {
		t.Fatalf("allowlisted email should sign in, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLoginCallbackRejectsIdentityOffAllowlist(t *testing.T) {
	issuer := newFakeIssuer(t, "client-1")
	issuer.email = "intruder@evil.net"
	a, err := New(context.Background(), testConfig(issuer))
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	rec := driveLogin(t, a, issuer, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("off-allowlist identity expected 403, got %d", rec.Code)
	}
	if sessionCookie(t, rec) != nil {
		t.Fatalf("rejected identity must not receive a session cookie")
	}
}

func TestLoginCallbackRejectsUnverifiedEmail(t *testing.T) {
	issuer := newFakeIssuer(t, "client-1")
	issuer.email = "operator@example.com"
	issuer.verified = false
	a, err := New(context.Background(), testConfig(issuer))
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	rec := driveLogin(t, a, issuer, "")
	if rec.Code != http.StatusForbidden || sessionCookie(t, rec) != nil {
		t.Fatalf("unverified email expected 403 and no session, got %d", rec.Code)
	}
}

func TestCallbackRejectsStateMismatch(t *testing.T) {
	issuer := newFakeIssuer(t, "client-1")
	issuer.email = "operator@example.com"
	a, err := New(context.Background(), testConfig(issuer))
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	loginRec := httptest.NewRecorder()
	a.ServeHTTP(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	callback := httptest.NewRequest(http.MethodGet, "/auth/callback?code=x&state=forged", nil)
	for _, cookie := range loginRec.Result().Cookies() {
		callback.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, callback)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("forged state expected 400, got %d", rec.Code)
	}
}

func TestSessionEmailRejectsTamperedAndExpiredCookies(t *testing.T) {
	issuer := newFakeIssuer(t, "client-1")
	a, err := New(context.Background(), testConfig(issuer))
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	valid, err := a.codec.encode(session{Email: "operator@example.com", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("encode session: %v", err)
	}
	// Swap in a forged payload while keeping the original signature.
	_, mac, _ := strings.Cut(valid, ".")
	forgedPayload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"attacker@example.com","expires_at":"2999-01-01T00:00:00Z"}`))
	tampered := httptest.NewRequest(http.MethodGet, "/", nil)
	tampered.AddCookie(&http.Cookie{Name: sessionCookieName, Value: forgedPayload + "." + mac})
	if _, ok := a.SessionEmail(tampered); ok {
		t.Fatalf("tampered cookie must not authenticate")
	}

	expired, err := a.codec.encode(session{Email: "operator@example.com", ExpiresAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("encode expired session: %v", err)
	}
	stale := httptest.NewRequest(http.MethodGet, "/", nil)
	stale.AddCookie(&http.Cookie{Name: sessionCookieName, Value: expired})
	if _, ok := a.SessionEmail(stale); ok {
		t.Fatalf("expired cookie must not authenticate")
	}
}

func TestLogoutClearsSession(t *testing.T) {
	issuer := newFakeIssuer(t, "client-1")
	a, err := New(context.Background(), testConfig(issuer))
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/logout", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout expected 303, got %d", rec.Code)
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.MaxAge >= 0 {
			t.Fatalf("logout must expire the session cookie, got MaxAge=%d", cookie.MaxAge)
		}
	}
}

func TestSecureCookieFollowsForwardedProto(t *testing.T) {
	issuer := newFakeIssuer(t, "client-1")
	a, err := New(context.Background(), testConfig(issuer))
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("login should set the state cookie")
	}
	for _, cookie := range cookies {
		if !cookie.Secure {
			t.Fatalf("cookies behind a TLS-terminating proxy must be Secure, got %+v", cookie)
		}
	}
}

func TestSessionEndpointReportsIdentity(t *testing.T) {
	issuer := newFakeIssuer(t, "client-1")
	issuer.email = "operator@example.com"
	a, err := New(context.Background(), testConfig(issuer))
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	login := driveLogin(t, a, issuer, "")
	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(sessionCookie(t, login))
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	var body struct {
		Mode    string `json:"mode"`
		Enabled bool   `json:"enabled"`
		Email   string `json:"email"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if body.Mode != "oidc" || !body.Enabled || body.Email != "operator@example.com" {
		t.Fatalf("unexpected session response: %+v", body)
	}
}

func TestFromEnvIsFailClosed(t *testing.T) {
	sessionKey := strings.Repeat("ab", 32)

	if cfg, err := FromEnv(map[string]string{}); err != nil || cfg.Enabled() {
		t.Fatalf("empty env should disable login cleanly, got cfg=%+v err=%v", cfg, err)
	}

	partial := map[string]string{"MERCATOR_OIDC_ISSUER": "https://issuer.example"}
	if _, err := FromEnv(partial); err == nil {
		t.Fatalf("partial OIDC config must refuse to boot")
	}

	orphaned := map[string]string{"MERCATOR_OIDC_ALLOWED_DOMAIN": "example.com"}
	if _, err := FromEnv(orphaned); err == nil {
		t.Fatalf("allowlist without an issuer must refuse to boot")
	}

	complete := map[string]string{
		"MERCATOR_OIDC_ISSUER":         "https://issuer.example",
		"MERCATOR_OIDC_CLIENT_ID":      "client",
		"MERCATOR_OIDC_CLIENT_SECRET":  "secret",
		"MERCATOR_OIDC_ALLOWED_DOMAIN": "example.com",
		"MERCATOR_SESSION_KEY":         sessionKey,
		"MERCATOR_PUBLIC_URL":          "https://mercator.example.com/",
	}
	cfg, err := FromEnv(complete)
	if err != nil || !cfg.Enabled() {
		t.Fatalf("complete config should enable login, got err=%v", err)
	}
	if cfg.PublicURL != "https://mercator.example.com" {
		t.Fatalf("public URL should be normalized without a trailing slash, got %q", cfg.PublicURL)
	}

	weak := map[string]string{}
	for k, v := range complete {
		weak[k] = v
	}
	weak["MERCATOR_SESSION_KEY"] = "abcd"
	if _, err := FromEnv(weak); err == nil {
		t.Fatalf("a short session key must refuse to boot")
	}
}

func TestSanitizeNextConfinesRedirects(t *testing.T) {
	cases := map[string]string{
		"":                      "/",
		"/runs":                 "/runs",
		"//evil.example":        "/",
		"/\\evil.example":       "/",
		"https://evil.example/": "/",
		"runs":                  "/",
	}
	for input, want := range cases {
		if got := sanitizeNext(input); got != want {
			t.Fatalf("sanitizeNext(%q) = %q, want %q", input, got, want)
		}
	}
}

// driveCLILogin runs the login round-trip as `mercator login` would: the
// browser hits /auth/login with the CLI's loopback port and state, and after
// issuer auth the callback redirects to 127.0.0.1 with a single-use code.
func driveCLILogin(t *testing.T, a *Authenticator, issuer *fakeIssuer) (code, echoedState string) {
	t.Helper()
	loginRec := httptest.NewRecorder()
	a.ServeHTTP(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login?cli_port=43110&cli_state=cli-csrf-1", nil))
	if loginRec.Code != http.StatusFound {
		t.Fatalf("cli login expected 302, got %d body=%s", loginRec.Code, loginRec.Body.String())
	}
	authorizeURL, err := url.Parse(loginRec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse authorize redirect: %v", err)
	}
	issuer.nonce = authorizeURL.Query().Get("nonce")

	callback := httptest.NewRequest(http.MethodGet,
		"/auth/callback?code=test-code&state="+url.QueryEscape(authorizeURL.Query().Get("state")), nil)
	for _, cookie := range loginRec.Result().Cookies() {
		callback.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, callback)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("cli callback expected 303, got %d body=%s", rec.Code, rec.Body.String())
	}
	target, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse loopback redirect: %v", err)
	}
	if target.Scheme != "http" || target.Host != "127.0.0.1:43110" {
		t.Fatalf("cli callback must redirect to the loopback listener, got %q", rec.Header().Get("Location"))
	}
	if cookie := sessionCookie(t, rec); cookie != nil {
		t.Fatalf("cli login must not set a browser session cookie")
	}
	return target.Query().Get("code"), target.Query().Get("state")
}

func exchangeCode(t *testing.T, a *Authenticator, code string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code})
	req := httptest.NewRequest(http.MethodPost, "/auth/cli/exchange", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	return rec
}

func TestCLILoginMintsSingleUseExchangeableToken(t *testing.T) {
	issuer := newFakeIssuer(t, "client-1")
	issuer.email = "operator@example.com"
	a, err := New(context.Background(), testConfig(issuer))
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	code, echoedState := driveCLILogin(t, a, issuer)
	if echoedState != "cli-csrf-1" {
		t.Fatalf("callback must echo the CLI state, got %q", echoedState)
	}
	if code == "" {
		t.Fatalf("callback did not deliver a code")
	}

	rec := exchangeCode(t, a, code)
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var granted struct {
		Token     string `json:"token"`
		Email     string `json:"email"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &granted); err != nil {
		t.Fatalf("decode exchange: %v", err)
	}
	if granted.Email != "operator@example.com" {
		t.Fatalf("unexpected email %q", granted.Email)
	}
	email, ok := a.VerifyCLIToken(granted.Token)
	if !ok || email != "operator@example.com" {
		t.Fatalf("minted token must verify, got %q ok=%v", email, ok)
	}

	// A code is single-use: replaying the exchange must fail.
	if rec := exchangeCode(t, a, code); rec.Code != http.StatusUnauthorized {
		t.Fatalf("code replay expected 401, got %d", rec.Code)
	}
}

func TestCLIExchangeRejectsForgedAndCrossKindTokens(t *testing.T) {
	issuer := newFakeIssuer(t, "client-1")
	issuer.email = "operator@example.com"
	a, err := New(context.Background(), testConfig(issuer))
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	if rec := exchangeCode(t, a, "not-a-code"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("garbage code expected 401, got %d", rec.Code)
	}

	// A session cookie value must not work as a CLI token or exchange code,
	// and a CLI code must not authenticate as a session: kinds are checked.
	sessionValue, err := a.codec.encode(session{Kind: kindSession, Email: "operator@example.com", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("encode session: %v", err)
	}
	if _, ok := a.VerifyCLIToken(sessionValue); ok {
		t.Fatalf("a session cookie value must not verify as a CLI token")
	}
	if rec := exchangeCode(t, a, sessionValue); rec.Code != http.StatusUnauthorized {
		t.Fatalf("session value as exchange code expected 401, got %d", rec.Code)
	}

	code, _ := driveCLILogin(t, a, issuer)
	stale := httptest.NewRequest(http.MethodGet, "/", nil)
	stale.AddCookie(&http.Cookie{Name: sessionCookieName, Value: code})
	if _, ok := a.SessionEmail(stale); ok {
		t.Fatalf("a CLI code must not authenticate as a browser session")
	}
}

func TestCLILoginRejectsInvalidPort(t *testing.T) {
	issuer := newFakeIssuer(t, "client-1")
	a, err := New(context.Background(), testConfig(issuer))
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	for _, port := range []string{"80", "abc", "-1", "70000"} {
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/login?cli_port="+port, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("cli_port=%s expected 400, got %d", port, rec.Code)
		}
	}
}
