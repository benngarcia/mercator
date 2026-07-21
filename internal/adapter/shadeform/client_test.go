package shadeform

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
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

func TestCreateClassifiesProviderFailures(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		transport   error
		wantKind    adapter.ProviderFailureKind
		wantRetry   bool
		wantEffect  adapter.SideEffectCertainty
		wantRetries int
	}{
		{name: "out of stock", status: 409, body: `{"error":{"code":"OUT_OF_STOCK"}}`, wantKind: adapter.ProviderFailureCapacityUnavailable, wantRetry: true, wantEffect: adapter.SideEffectNone},
		{name: "invalid request", status: 400, body: `{"code":"INVALID_ARGUMENT"}`, wantKind: adapter.ProviderFailureInvalidRequest, wantEffect: adapter.SideEffectNone},
		{name: "authentication", status: 401, body: `{"message":"Invalid API key"}`, wantKind: adapter.ProviderFailureAuthentication, wantEffect: adapter.SideEffectNone},
		{name: "exhausted throttling", status: 429, body: `{"code":"RATE_LIMITED"}`, wantKind: adapter.ProviderFailureRateLimited, wantRetry: true, wantEffect: adapter.SideEffectNone, wantRetries: 3},
		{name: "transport", transport: errors.New("connection reset"), wantKind: adapter.ProviderFailureTransport, wantRetry: true, wantEffect: adapter.SideEffectIndeterminate},
		{name: "provider internal", status: 503, body: `{"code":"UPSTREAM_UNAVAILABLE"}`, wantKind: adapter.ProviderFailureInternal, wantRetry: true, wantEffect: adapter.SideEffectIndeterminate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := newTestClient(func(*http.Request) (*http.Response, error) {
				if test.transport != nil {
					return nil, test.transport
				}
				return jsonResponse(test.status, test.body), nil
			})

			_, err := c.createInstance(t.Context(), createRequest{})

			var failure *adapter.ProviderFailure
			if !errors.As(err, &failure) {
				t.Fatalf("error = %v, want ProviderFailure", err)
			}
			if failure.Kind != test.wantKind || failure.Retryable != test.wantRetry || failure.SideEffect != test.wantEffect {
				t.Fatalf("failure = %+v", failure)
			}
			if failure.Status != test.status || failure.RetryCount != test.wantRetries {
				t.Fatalf("status/retries = %d/%d, want %d/%d", failure.Status, failure.RetryCount, test.status, test.wantRetries)
			}
		})
	}
}

func TestCreateFailureSanitizesAndBoundsResponseBody(t *testing.T) {
	request := createRequest{LaunchConfiguration: &launchConfiguration{
		Type: "docker",
		DockerConfiguration: &dockerConfiguration{
			Envs:                []envVar{{Name: "TOKEN", Value: "workload-secret"}},
			RegistryCredentials: &registryCredentials{Username: "registry-user", Password: "registry-secret"},
		},
	}}
	body := `{"code":"INVALID_ARGUMENT","message":"secret-key workload-secret registry-secret registry-user","request":{"launch_configuration":"provider request payload"},"detail":"` + strings.Repeat("x", maxProviderResponseBodyBytes*2) + `"}`
	c := newTestClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusBadRequest, body), nil
	})

	_, err := c.createInstance(t.Context(), request)

	var failure *adapter.ProviderFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want ProviderFailure", err)
	}
	if !failure.ResponseTruncated || len(failure.ResponseBody) > maxProviderResponseBodyBytes {
		t.Fatalf("response body was not bounded: len=%d truncated=%v", len(failure.ResponseBody), failure.ResponseTruncated)
	}
	for _, secret := range []string{"secret-key", "workload-secret", "registry-secret", "registry-user", "provider request payload"} {
		if strings.Contains(failure.ResponseBody, secret) {
			t.Fatalf("sanitized response contains %q: %s", secret, failure.ResponseBody)
		}
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
