package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/prediction"
	"github.com/benngarcia/mercator/internal/runprojection"
)

// launchHistory is what this Workspace's earlier launches really spent, built
// from Mercator's own record of them and handed to Placement so a candidate this
// fleet has already used is predicted from what it did rather than from what
// somebody published about it.
//
// Both halves are read back rather than remembered. What each launch was about
// comes from the Booking Decision that chose it, which is where the candidate
// identity was written down at the moment of the decision; when it started and
// when its application came up come from the Run projection, which is the event
// log with the adoption rules already applied. Reading the raw report instead
// would file a moment the control plane refused: a host an hour ahead of
// Mercator publishes a readiness an hour in the future, and the projection is
// where that is already decided.
//
// It is rebuilt per placement. A Workspace pays one page walk of its Runs and
// one scan of its decisions to place a Run, which is honest and is not free; the
// cheaper form is a projection maintained on append, and it is worth building
// when a Workspace's history is long enough for this to show up rather than
// before.
func (o *Orchestrator) launchHistory(ctx context.Context, workspaceID string) (prediction.History, error) {
	candidates, err := o.launchedCandidates(ctx, workspaceID)
	if err != nil {
		return prediction.History{}, err
	}
	if len(candidates) == 0 {
		return prediction.History{}, nil
	}
	records, err := o.everyRunRecord(ctx, workspaceID)
	if err != nil {
		return prediction.History{}, err
	}
	launches := make([]prediction.Launch, 0, len(records))
	for _, record := range records {
		identity, decided := candidates[record.ID]
		if !decided || record.StartedAt == nil || record.ReadyAt == nil {
			continue
		}
		launches = append(launches, prediction.Launch{
			Candidate: identity,
			StartedAt: *record.StartedAt,
			ReadyAt:   *record.ReadyAt,
		})
	}
	return prediction.NewHistory(prediction.Observations(launches)), nil
}

// launchedCandidates is what Mercator took the machine it chose to be, by Run.
// The last decision on a Run stands: a Run that was replaced onto other capacity
// after a failed launch ran on the machine its final decision named, and the
// moments the projection holds are that launch's.
func (o *Orchestrator) launchedCandidates(ctx context.Context, workspaceID string) (map[string]domain.CandidateIdentity, error) {
	filter := eventlog.EventFilter{
		WorkspaceID: workspaceID,
		StreamTypes: []string{"run"},
		EventTypes:  []string{EventBookingDecided},
	}
	head, err := o.log.LatestPosition(ctx, filter)
	if err != nil {
		return nil, err
	}
	candidates := map[string]domain.CandidateIdentity{}
	for event, err := range eventlog.ScanAll(ctx, o.log, head, filter) {
		if err != nil {
			return nil, err
		}
		var data bookingDecisionData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, fmt.Errorf("orchestrator: decode Booking Decision %s for the launch history: %w", event.ID, err)
		}
		identity, chosen := selectedCandidate(data.Decision)
		if !chosen {
			continue
		}
		candidates[data.Decision.RunID] = identity
	}
	return candidates, nil
}

// selectedCandidate is what the decision took the machine it placed on to be. A
// decision that placed nowhere describes no launch.
//
// Capacity that cannot recur is filed anyway, and the estimator is where that
// stops mattering: a one-shot pool has no key of its own, so its launch lands
// only on the levels it does have and can never be read back as evidence about
// this exact candidate. Dropping it here would throw away the only thing anyone
// can ever learn about a lane whose capacity holds nothing.
func selectedCandidate(decision domain.BookingDecision) (domain.CandidateIdentity, bool) {
	if decision.SelectedOfferSnapshotID == "" {
		return domain.CandidateIdentity{}, false
	}
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID != decision.SelectedOfferSnapshotID {
			continue
		}
		return candidate.Candidate, true
	}
	return domain.CandidateIdentity{}, false
}

func (o *Orchestrator) everyRunRecord(ctx context.Context, workspaceID string) ([]domain.RunRecord, error) {
	var records []domain.RunRecord
	request := runprojection.PageRequest{Limit: runprojection.MaxPageSize}
	for {
		page, err := o.runs.List(ctx, workspaceID, request)
		if err != nil {
			return nil, err
		}
		records = append(records, page.Records...)
		if page.NextCursor == "" {
			return records, nil
		}
		request.After = page.NextCursor
	}
}
