package shadeform

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestClient(fn roundTripFunc) *client {
	c := newClient("secret-key", "https://shadeform.test/v1", &http.Client{Transport: fn})
	c.backoff = 0
	return c
}

func TestClientSendsAPIKeyHeader(t *testing.T) {
	var gotKey string
	c := newTestClient(func(r *http.Request) (*http.Response, error) {
		gotKey = r.Header.Get("X-API-KEY")
		return jsonResponse(200, `{"instances":[]}`), nil
	})
	if _, err := c.listInstances(context.Background()); err != nil {
		t.Fatalf("list: %v", err)
	}
	if gotKey != "secret-key" {
		t.Fatalf("X-API-KEY = %q", gotKey)
	}
}

func TestIdempotentCallsRetryOn429And5xx(t *testing.T) {
	statuses := []int{429, 500, 200}
	calls := 0
	c := newTestClient(func(r *http.Request) (*http.Response, error) {
		status := statuses[calls]
		calls++
		if status != 200 {
			return jsonResponse(status, `{"error":"transient"}`), nil
		}
		return jsonResponse(200, `{"instances":[]}`), nil
	})
	if _, err := c.listInstances(context.Background()); err != nil {
		t.Fatalf("list after retries: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (429 and 500 each retried)", calls)
	}
}

func TestIdempotentCallsGiveUpAfterMaxAttempts(t *testing.T) {
	calls := 0
	c := newTestClient(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(503, `{"error":"down"}`), nil
	})
	if _, err := c.listInstances(context.Background()); err == nil {
		t.Fatal("want error after exhausting retries")
	}
	if calls != 4 {
		t.Fatalf("calls = %d, want 4 attempts", calls)
	}
}

func TestCreateRetriesOnlyOn429(t *testing.T) {
	statuses := []int{429, 429, 200}
	calls := 0
	c := newTestClient(func(r *http.Request) (*http.Response, error) {
		status := statuses[calls]
		calls++
		if status != 200 {
			return jsonResponse(status, `{"error":"throttled"}`), nil
		}
		return jsonResponse(200, `{"id":"inst_1"}`), nil
	})
	id, err := c.createInstance(context.Background(), createRequest{})
	if err != nil || id != "inst_1" {
		t.Fatalf("create: id=%q err=%v", id, err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestCreateDoesNotRetryOn5xx(t *testing.T) {
	calls := 0
	c := newTestClient(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(502, `{"error":"bad gateway"}`), nil
	})
	if _, err := c.createInstance(context.Background(), createRequest{}); err == nil {
		t.Fatal("want error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d; a 5xx create is indeterminate and must not be retried", calls)
	}
}

func TestDeleteTreats404AsGone(t *testing.T) {
	c := newTestClient(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(404, `{"error":"not found"}`), nil
	})
	if err := c.deleteInstance(context.Background(), "inst_1"); err != nil {
		t.Fatalf("404 delete must be idempotent success, got %v", err)
	}
}

func TestErrorsNeverIncludeAPIKeyAndAreTruncated(t *testing.T) {
	c := newTestClient(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(400, strings.Repeat("x", 1000)), nil
	})
	_, err := c.listInstances(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatal("error must never contain the API key")
	}
	if len(err.Error()) > 400 {
		t.Fatalf("error must truncate the body, got %d bytes", len(err.Error()))
	}
}

func TestShellJoinEdgeCases(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"python", "train.py"}, "python train.py"},
		{[]string{""}, "''"},
		{[]string{"a b"}, "'a b'"},
		{[]string{"$HOME"}, "'$HOME'"},
		{[]string{`he said "hi"`}, `'he said "hi"'`},
		{[]string{"don't"}, `'don'\''t'`},
		{[]string{"--flag=value", "path/to/file:ro"}, "--flag=value path/to/file:ro"},
	}
	for _, tc := range cases {
		if got := shellJoin(tc.args); got != tc.want {
			t.Errorf("shellJoin(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}
