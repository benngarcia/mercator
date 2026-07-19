package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scheduler"
)

type stubOfferAggregator struct {
	aggregation broker.OfferAggregation
}

func (s stubOfferAggregator) AggregateOffers(context.Context, adapter.OfferRequest) (broker.OfferAggregation, error) {
	return s.aggregation, nil
}

func TestListOffersExposesPartialResultsAndConnectionFailures(t *testing.T) {
	offer := domain.OfferSnapshot{ID: "offer_good", ConnectionID: "conn_good", AdapterType: "fake"}
	failure := broker.ConnectionError{ConnectionID: "conn_bad", AdapterType: "runpod", Err: errors.New("provider unavailable")}
	handler := New(Deps{Offers: stubOfferAggregator{aggregation: broker.OfferAggregation{
		Offers:   []domain.OfferSnapshot{offer},
		Failures: broker.ConnectionErrors{failure},
	}}})
	req := httptest.NewRequest(http.MethodGet, "/v1/offers?workspace_id=ws_1", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var response offerListResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Offers) != 1 || response.Offers[0].ID != "offer_good" {
		t.Fatalf("offers = %#v, want offer_good", response.Offers)
	}
	if len(response.Failures) != 1 || response.Failures[0].ConnectionID != "conn_bad" || response.Failures[0].Message != "Provider offer query failed." {
		t.Fatalf("failures = %#v, want conn_bad provider failure", response.Failures)
	}
}

func TestPreviewPlacementRejectsPartialOfferSet(t *testing.T) {
	failure := broker.ConnectionError{ConnectionID: "conn_bad", AdapterType: "runpod", Err: errors.New("provider unavailable")}
	handler := New(Deps{
		Scheduler: scheduler.New(),
		Offers: stubOfferAggregator{aggregation: broker.OfferAggregation{
			Failures: broker.ConnectionErrors{failure},
		}},
	})
	body := mustMarshal(t, placementPreviewBody{RunID: "run_1", WorkspaceID: "ws_1", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/placements:preview", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
}
