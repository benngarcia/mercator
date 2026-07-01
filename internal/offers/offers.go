package offers

import (
	"context"
	"encoding/json"
	"errors"
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
	stream := eventlog.StreamKey{WorkspaceID: req.WorkspaceID, Type: "offers", ID: req.ConnectionID}
	commandKey := fmt.Sprintf("offers:ingest:%s:%d", req.ConnectionID, s.now().UnixNano())
	// Append at the stream's CURRENT version (retrying once on a concurrent
	// ingest); a fixed expectation of 0 would make every ingest after the
	// first fail with a concurrency conflict.
	for attempt := 0; attempt < 2; attempt++ {
		version, err := s.streamVersion(ctx, stream)
		if err != nil {
			return err
		}
		_, err = s.log.Append(ctx, eventlog.AppendRequest{
			Stream:                stream,
			ExpectedStreamVersion: version,
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
		if err == nil {
			return nil
		}
		if errors.Is(err, eventlog.ErrConcurrencyConflict) && attempt == 0 {
			continue
		}
		return err
	}
	return fmt.Errorf("offers: ingest concurrency conflict after retry")
}

// streamVersion returns the stream version of the last stored event, reading
// past the page size so long-lived offer streams never wedge.
func (s *Service) streamVersion(ctx context.Context, stream eventlog.StreamKey) (uint64, error) {
	const page = 1000
	var version uint64
	for {
		batch, err := s.log.ReadStream(ctx, stream, version, page)
		if err != nil {
			return 0, err
		}
		if len(batch) == 0 {
			return version, nil
		}
		version = batch[len(batch)-1].StreamVersion
		if len(batch) < page {
			return version, nil
		}
	}
}

func (s *Service) ListCached(ctx context.Context, workspaceID string, now time.Time) ([]domain.OfferSnapshot, error) {
	// Paginate so the newest ingests are always applied: reading only the
	// oldest page would freeze the cache once the stream outgrows it.
	const page = 1000
	var events []eventlog.StoredEvent
	var after eventlog.GlobalPosition
	for {
		batch, err := s.log.ReadAll(ctx, after, page, eventlog.EventFilter{WorkspaceID: workspaceID, StreamTypes: []string{"offers"}, EventTypes: []string{EventOfferSnapshotsIngested}})
		if err != nil {
			return nil, err
		}
		events = append(events, batch...)
		if len(batch) < page {
			break
		}
		after = batch[len(batch)-1].GlobalPosition
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
