package vast

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newFakeHTTPClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// newTestAdapter builds an Adapter whose API client uses a fake transport.
func newTestAdapter(t *testing.T, fn roundTripFunc) *Adapter {
	t.Helper()
	a, err := New("secret", map[string]string{"base_url": "https://vast.test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.api.http = newFakeHTTPClient(fn)
	return a
}

// launchRequest is the canonical launch fixture: launch key lk1 on secure ask
// 9001. Tests mutate the returned value for their scenario.
func launchRequest() adapter.LaunchRequest {
	return adapter.LaunchRequest{
		WorkspaceID:            "ws_1",
		RunID:                  "run_1",
		AttemptID:              "att_1",
		LaunchKey:              "lk1",
		OwnershipToken:         "own1",
		RequestHash:            "rh1",
		CleanupLocator:         "cl1",
		Image:                  "busybox",
		SelectedOfferNativeRef: "9001",
	}
}

func observeRequest() adapter.ObserveRequest {
	return adapter.ObserveRequest{LaunchKey: "lk1", OwnershipToken: "own1", RequestHash: "rh1"}
}

func terminateRequest() adapter.TerminateRequest {
	return adapter.TerminateRequest{LaunchKey: "lk1", OwnershipToken: "own1", LaunchRequestHash: "rh1"}
}

func cancelRequest() adapter.CancelRequest {
	return adapter.CancelRequest{LaunchKey: "lk1"}
}

func ownershipQuery(workspaceID string) adapter.OwnershipQuery {
	return adapter.OwnershipQuery{WorkspaceID: workspaceID}
}

func offerRequest() adapter.OfferRequest {
	return adapter.OfferRequest{WorkspaceID: "ws_1"}
}
