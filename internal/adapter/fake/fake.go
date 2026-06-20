package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/domain"
)

type Adapter struct {
	mu            sync.Mutex
	now           func() time.Time
	launchOutcome adapter.ExternalPhase
	offers        []domain.OfferSnapshot
	objects       map[string]adapter.OwnedExternalObject
	ops           map[string]operationRecord
}

type operationRecord struct {
	hash    string
	receipt any
}

type Option func(*Adapter)

func WithLaunchOutcome(phase adapter.ExternalPhase) Option {
	return func(a *Adapter) {
		a.launchOutcome = phase
	}
}

func WithOffers(offers []domain.OfferSnapshot) Option {
	return func(a *Adapter) {
		a.offers = append([]domain.OfferSnapshot(nil), offers...)
	}
}

func New(options ...Option) *Adapter {
	a := &Adapter{
		now:           time.Now,
		launchOutcome: adapter.ExternalPhaseRunning,
		objects:       map[string]adapter.OwnedExternalObject{},
		ops:           map[string]operationRecord{},
	}
	for _, option := range options {
		option(a)
	}
	return a
}

func (a *Adapter) ListOffers(_ context.Context, _ adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]domain.OfferSnapshot(nil), a.offers...), nil
}

func (a *Adapter) Launch(_ context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := requireOperation(req.OperationKey, req.RequestHash); err != nil {
		return adapter.LaunchReceipt{}, err
	}
	if existing, ok := a.ops[req.OperationKey]; ok {
		if existing.hash != req.RequestHash {
			return adapter.LaunchReceipt{}, adapter.ErrIdempotencyConflict
		}
		receipt := existing.receipt.(adapter.LaunchReceipt)
		receipt.Duplicate = true
		return receipt, nil
	}
	if existing, ok := a.objects[req.LaunchKey]; ok && existing.OwnershipToken != req.OwnershipToken {
		return adapter.LaunchReceipt{}, adapter.ErrIdempotencyConflict
	}
	externalID := "fake-" + req.AttemptID
	phase := a.launchOutcome
	object := adapter.OwnedExternalObject{
		ExternalID:     externalID,
		WorkspaceID:    req.WorkspaceID,
		RunID:          req.RunID,
		AttemptID:      req.AttemptID,
		OwnershipToken: req.OwnershipToken,
		LaunchKey:      req.LaunchKey,
		Phase:          phase,
	}
	a.objects[req.LaunchKey] = object
	receipt := adapter.LaunchReceipt{
		ExternalID:     externalID,
		LaunchKey:      req.LaunchKey,
		OwnershipToken: req.OwnershipToken,
		Phase:          phase,
		AcceptedAt:     a.now().UTC(),
	}
	a.ops[req.OperationKey] = operationRecord{hash: req.RequestHash, receipt: receipt}
	return receipt, nil
}

func (a *Adapter) Observe(_ context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	object, ok := a.objects[req.LaunchKey]
	if !ok {
		return adapter.ExternalObservation{LaunchKey: req.LaunchKey, Phase: adapter.ExternalPhaseReleased, ObservedAt: a.now().UTC()}, nil
	}
	observation := adapter.ExternalObservation{
		ExternalID: object.ExternalID,
		LaunchKey:  object.LaunchKey,
		Phase:      object.Phase,
		ObservedAt: a.now().UTC(),
		NativeJSON: fmt.Sprintf(`{"adapter":"fake","external_id":%q}`, object.ExternalID),
	}
	if object.Phase == adapter.ExternalPhaseSucceeded {
		code := 0
		observation.ExitCode = &code
	}
	return observation, nil
}

func (a *Adapter) Cancel(_ context.Context, req adapter.CancelRequest) (adapter.CancelReceipt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := requireOperation(req.OperationKey, req.RequestHash); err != nil {
		return adapter.CancelReceipt{}, err
	}
	if existing, ok := a.ops[req.OperationKey]; ok {
		if existing.hash != req.RequestHash {
			return adapter.CancelReceipt{}, adapter.ErrIdempotencyConflict
		}
		receipt := existing.receipt.(adapter.CancelReceipt)
		receipt.Duplicate = true
		return receipt, nil
	}
	if object, ok := a.objects[req.LaunchKey]; ok {
		object.Phase = adapter.ExternalPhaseCancelled
		a.objects[req.LaunchKey] = object
	}
	receipt := adapter.CancelReceipt{Cancelled: true}
	a.ops[req.OperationKey] = operationRecord{hash: req.RequestHash, receipt: receipt}
	return receipt, nil
}

func (a *Adapter) Release(_ context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := requireOperation(req.OperationKey, req.RequestHash); err != nil {
		return adapter.ReleaseReceipt{}, err
	}
	if existing, ok := a.ops[req.OperationKey]; ok {
		if existing.hash != req.RequestHash {
			return adapter.ReleaseReceipt{}, adapter.ErrIdempotencyConflict
		}
		receipt := existing.receipt.(adapter.ReleaseReceipt)
		receipt.Duplicate = true
		return receipt, nil
	}
	delete(a.objects, req.LaunchKey)
	receipt := adapter.ReleaseReceipt{Released: true}
	a.ops[req.OperationKey] = operationRecord{hash: req.RequestHash, receipt: receipt}
	return receipt, nil
}

func (a *Adapter) ListOwned(_ context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var objects []adapter.OwnedExternalObject
	for _, object := range a.objects {
		if req.WorkspaceID == "" || object.WorkspaceID == req.WorkspaceID {
			objects = append(objects, object)
		}
	}
	return objects, nil
}

func requireOperation(key, hash string) error {
	if key == "" || hash == "" {
		return fmt.Errorf("adapter: operation key and request hash are required")
	}
	return nil
}
