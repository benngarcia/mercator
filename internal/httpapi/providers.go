package httpapi

import (
	"context"
	"log"
	"net/http"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/broker"
)

// ListAdapters serves the registered adapters' onboarding manifests. The list
// is static per process and sits behind the same /v1 auth gate as everything
// else.
func (s *Server) ListAdapters(context.Context, ListAdaptersRequestObject) (ListAdaptersResponseObject, error) {
	if s.manifests == nil {
		return ListAdapters200JSONResponse{Adapters: []adapter.Manifest{}}, nil
	}
	manifests := s.manifests()
	if manifests == nil {
		manifests = []adapter.Manifest{}
	}
	return ListAdapters200JSONResponse{Adapters: manifests}, nil
}

func (s *Server) ListOffers(ctx context.Context, request ListOffersRequestObject) (ListOffersResponseObject, error) {
	aggregation, err := s.offers.AggregateOffers(ctx, adapter.OfferRequest{})
	if err != nil {
		return ListOffers502JSONResponse(internalAPIError(http.StatusBadGateway, "LIST_OFFERS_FAILED", err)), nil
	}
	if len(aggregation.Failures) > 0 {
		log.Printf("list offers partial failure: %v", aggregation.Failures)
	}
	return ListOffers200JSONResponse{
		Offers:   aggregation.Offers,
		Failures: connectionFailureResponses(aggregation.Failures),
	}, nil
}

func connectionFailureResponses(failures broker.ConnectionErrors) []ConnectionFailure {
	responses := make([]ConnectionFailure, len(failures))
	for i, failure := range failures {
		responses[i] = ConnectionFailure{
			ConnectionId: failure.ConnectionID,
			AdapterType:  failure.AdapterType,
			Message:      "Provider offer query failed.",
		}
	}
	return responses
}
