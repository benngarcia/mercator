package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

type Adapter struct {
	mu            sync.Mutex
	now           func() time.Time
	launchOutcome adapter.ExternalPhase
	offers        []domain.OfferSnapshot
	objects       map[string]adapter.OwnedExternalObject
	ops           map[string]operationRecord
	// openObserves is the number of times Observe reports a non-terminal
	// "running" phase before reporting the configured launch outcome. It models
	// a run that stays open past one or more internal poll windows.
	openObserves int
	observeCount map[string]int
	// exitCode, when set, is the container exit code reported on a terminal
	// observation. It defaults to 0 (success). A non-zero value lets a test
	// drive the failed/non-zero exit path end-to-end. exitCodeSet distinguishes
	// "explicitly configured" from the implicit success default.
	exitCode    int
	exitCodeSet bool
	// releaseCount and terminateCount track which cleanup disposition path the
	// orchestrator invoked, for end-to-end assertions. Both paths are idempotent
	// and remove the owned object.
	releaseCount   int
	terminateCount int
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

// WithExitCode configures the container exit code reported on a terminal
// observation. Combine with WithLaunchOutcome(ExternalPhaseFailed) and a
// non-zero code to exercise the failed/non-zero exit path end-to-end.
func WithExitCode(code int) Option {
	return func(a *Adapter) {
		a.exitCode = code
		a.exitCodeSet = true
	}
}

// WithOpenObservations makes Observe report a non-terminal "running" phase for
// the first n observations of a launched object before reporting the configured
// launch outcome. This models a run that stays open past one or more internal
// poll windows so callers can exercise long-poll/wait behavior.
func WithOpenObservations(n int) Option {
	return func(a *Adapter) {
		a.openObserves = n
	}
}

func New(options ...Option) *Adapter {
	a := &Adapter{
		now:           time.Now,
		launchOutcome: adapter.ExternalPhaseRunning,
		objects:       map[string]adapter.OwnedExternalObject{},
		ops:           map[string]operationRecord{},
		observeCount:  map[string]int{},
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
	if existing, ok := a.objects[req.LaunchKey]; ok {
		if existing.OwnershipToken != req.OwnershipToken || existing.RequestHash != req.RequestHash {
			return adapter.LaunchReceipt{}, adapter.ErrIdempotencyConflict
		}
		receipt := adapter.LaunchReceipt{
			ExternalID:     existing.ExternalID,
			LaunchKey:      existing.LaunchKey,
			OwnershipToken: existing.OwnershipToken,
			CleanupLocator: existing.CleanupLocator,
			Phase:          existing.Phase,
			AcceptedAt:     a.now().UTC(),
			Duplicate:      true,
		}
		a.ops[req.OperationKey] = operationRecord{hash: req.RequestHash, receipt: receipt}
		return receipt, nil
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
		CleanupLocator: req.CleanupLocator,
		RequestHash:    req.RequestHash,
		Phase:          phase,
	}
	a.objects[req.LaunchKey] = object
	receipt := adapter.LaunchReceipt{
		ExternalID:     externalID,
		LaunchKey:      req.LaunchKey,
		OwnershipToken: req.OwnershipToken,
		CleanupLocator: req.CleanupLocator,
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
	if req.OwnershipToken != "" && object.OwnershipToken != req.OwnershipToken {
		return adapter.ExternalObservation{}, adapter.ErrIdempotencyConflict
	}
	if req.RequestHash != "" && object.RequestHash != req.RequestHash {
		return adapter.ExternalObservation{}, adapter.ErrIdempotencyConflict
	}
	phase := object.Phase
	if a.openObserves > 0 && isFakeTerminal(phase) {
		seen := a.observeCount[req.LaunchKey]
		a.observeCount[req.LaunchKey] = seen + 1
		if seen < a.openObserves {
			phase = adapter.ExternalPhaseRunning
		}
	}
	observation := adapter.ExternalObservation{
		ExternalID: object.ExternalID,
		LaunchKey:  object.LaunchKey,
		Phase:      phase,
		ObservedAt: a.now().UTC(),
		NativeJSON: fmt.Sprintf(`{"adapter":"fake","external_id":%q}`, object.ExternalID),
	}
	switch phase {
	case adapter.ExternalPhaseSucceeded:
		code := 0
		if a.exitCodeSet {
			code = a.exitCode
		}
		observation.ExitCode = &code
	case adapter.ExternalPhaseFailed:
		// A failed run surfaces a non-zero exit code. Default to 1 unless an
		// explicit code was configured.
		code := 1
		if a.exitCodeSet {
			code = a.exitCode
		}
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
	if object, ok := a.objects[req.LaunchKey]; ok {
		if req.OwnershipToken != "" && object.OwnershipToken != req.OwnershipToken {
			return adapter.ReleaseReceipt{}, adapter.ErrIdempotencyConflict
		}
		if req.LaunchRequestHash != "" && object.RequestHash != req.LaunchRequestHash {
			return adapter.ReleaseReceipt{}, adapter.ErrIdempotencyConflict
		}
	}
	delete(a.objects, req.LaunchKey)
	a.releaseCount++
	receipt := adapter.ReleaseReceipt{Released: true}
	a.ops[req.OperationKey] = operationRecord{hash: req.RequestHash, receipt: receipt}
	return receipt, nil
}

// Terminate destroys the owned object the same way Release removes it, but
// records that the TERMINATE disposition path was exercised. This lets the fake
// drive the provisionable->terminate path end-to-end with no network while
// keeping the same idempotency (OperationKey/RequestHash) and ownership
// machinery as Release.
func (a *Adapter) Terminate(_ context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := requireOperation(req.OperationKey, req.RequestHash); err != nil {
		return adapter.TerminateReceipt{}, err
	}
	if existing, ok := a.ops[req.OperationKey]; ok {
		if existing.hash != req.RequestHash {
			return adapter.TerminateReceipt{}, adapter.ErrIdempotencyConflict
		}
		receipt := existing.receipt.(adapter.TerminateReceipt)
		receipt.Duplicate = true
		return receipt, nil
	}
	if object, ok := a.objects[req.LaunchKey]; ok {
		if req.OwnershipToken != "" && object.OwnershipToken != req.OwnershipToken {
			return adapter.TerminateReceipt{}, adapter.ErrIdempotencyConflict
		}
		if req.LaunchRequestHash != "" && object.RequestHash != req.LaunchRequestHash {
			return adapter.TerminateReceipt{}, adapter.ErrIdempotencyConflict
		}
	}
	delete(a.objects, req.LaunchKey)
	a.terminateCount++
	receipt := adapter.TerminateReceipt{Terminated: true}
	a.ops[req.OperationKey] = operationRecord{hash: req.RequestHash, receipt: receipt}
	return receipt, nil
}

// ReleaseCount reports how many times the release disposition path was invoked.
func (a *Adapter) ReleaseCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.releaseCount
}

// TerminateCount reports how many times the terminate disposition path was
// invoked.
func (a *Adapter) TerminateCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.terminateCount
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

func isFakeTerminal(phase adapter.ExternalPhase) bool {
	return phase == adapter.ExternalPhaseSucceeded || phase == adapter.ExternalPhaseFailed || phase == adapter.ExternalPhaseCancelled
}

func requireOperation(key, hash string) error {
	if key == "" || hash == "" {
		return fmt.Errorf("adapter: operation key and request hash are required")
	}
	return nil
}
