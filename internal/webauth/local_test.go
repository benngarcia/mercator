package webauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalSessionEstablishesDeveloperIdentity(t *testing.T) {
	// Arrange
	authenticator, err := NewLocal("developer@localhost")
	if err != nil {
		t.Fatalf("new local authenticator: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	recorder := httptest.NewRecorder()

	// Act
	authenticator.ServeHTTP(recorder, request)

	// Assert
	if recorder.Code != http.StatusOK {
		t.Fatalf("session expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Mode    string `json:"mode"`
		Enabled bool   `json:"enabled"`
		Email   string `json:"email"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if response.Mode != "local" || !response.Enabled || response.Email != "developer@localhost" {
		t.Fatalf("unexpected session response: %+v", response)
	}
	cookie := sessionCookie(t, recorder)
	if cookie == nil {
		t.Fatal("local session did not set a cookie")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("local session cookie must be HttpOnly and SameSite=Lax: %+v", cookie)
	}
	authenticated := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	authenticated.AddCookie(cookie)
	if email, ok := authenticator.SessionEmail(authenticated); !ok || email != "developer@localhost" {
		t.Fatalf("cookie should authenticate developer@localhost, got %q ok=%v", email, ok)
	}
}

func TestLocalLoginPreservesDeepLink(t *testing.T) {
	// Arrange
	authenticator, err := NewLocal("developer@localhost")
	if err != nil {
		t.Fatalf("new local authenticator: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/auth/login?next=%2Fcanvas%3Fworkspace_id%3Dws_local", nil)
	recorder := httptest.NewRecorder()

	// Act
	authenticator.ServeHTTP(recorder, request)

	// Assert
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("login expected 303, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); location != "/canvas?workspace_id=ws_local" {
		t.Fatalf("login should preserve deep link, got %q", location)
	}
	if sessionCookie(t, recorder) == nil {
		t.Fatal("local login did not establish a session")
	}
}

func TestLocalAuthenticatorRequiresIdentity(t *testing.T) {
	if _, err := NewLocal(" "); err == nil {
		t.Fatal("blank local identity should fail")
	}
}
