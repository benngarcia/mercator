package offers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

const EventOfferSnapshotsIngested = "compute.offers.snapshots_ingested.v1"

type Service struct {
	log eventlog.EventLog
	now func() time.Time
}

type IngestRequest struct {
	WorkspaceID  string
	ConnectionID string
	Offers       []domain.OfferSnapshot
}

type ingestData struct {
	ConnectionID string                 `json:"connection_id"`
	Offers       []domain.OfferSnapshot `json:"offers"`
}

func New(log eventlog.EventLog) *Service {
	return &Service{log: log, now: time.Now}
}

func (s *Service) Ingest(ctx context.Context, req IngestRequest) error {
	if req.WorkspaceID == "" || req.ConnectionID == "" {
		return fmt.Errorf("offers: workspace_id and connection_id are required")
	}
	offers := make([]domain.OfferSnapshot, 0, len(req.Offers))
	for _, offer := range req.Offers {
		offer.ConnectionID = req.ConnectionID
		offers = append(offers, offer)
	}
	data, err := json.Marshal(ingestData{ConnectionID: req.ConnectionID, Offers: offers})
	if err != nil {
		return err
	}
	hash, err := domain.CanonicalHash(ingestData{ConnectionID: req.ConnectionID, Offers: offers})
	if err != nil {
		return err
	}
	commandKey := fmt.Sprintf("offers:ingest:%s:%d", req.ConnectionID, s.now().UnixNano())
	_, err = s.log.Append(ctx, eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: req.WorkspaceID, Type: "offers", ID: req.ConnectionID},
		ExpectedStreamVersion: 0,
		CommandKey:            commandKey,
		RequestHash:           hash,
		CorrelationID:         req.ConnectionID,
		CausationID:           commandKey,
		Events: []eventlog.NewEvent{{
			ID:            fmt.Sprintf("evt_offers_%s_%d", req.ConnectionID, s.now().UnixNano()),
			Type:          EventOfferSnapshotsIngested,
			SchemaVersion: 1,
			OccurredAt:    s.now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          data,
		}},
	})
	return err
}

func (s *Service) ListCached(ctx context.Context, workspaceID string, now time.Time) ([]domain.OfferSnapshot, error) {
	events, err := s.log.ReadAll(ctx, 0, 1000, eventlog.EventFilter{WorkspaceID: workspaceID, StreamTypes: []string{"offers"}, EventTypes: []string{EventOfferSnapshotsIngested}})
	if err != nil {
		return nil, err
	}
	latest := map[string]domain.OfferSnapshot{}
	for _, event := range events {
		var data ingestData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, err
		}
		for _, offer := range data.Offers {
			if !offer.ExpiresAt.IsZero() && !offer.ExpiresAt.After(now) {
				delete(latest, offer.ID)
				continue
			}
			latest[offer.ID] = offer
		}
	}
	out := make([]domain.OfferSnapshot, 0, len(latest))
	for _, offer := range latest {
		out = append(out, offer)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
