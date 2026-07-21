package broker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
)

var ErrConnectionNotFound = errors.New("broker: connection not found")

// Connections lists the registered connections for a workspace.
// *connection.Service satisfies it directly.
type Connections interface {
	List(ctx context.Context, workspaceID string) ([]connection.Record, error)
}

type Resolver interface {
	Resolve(ctx context.Context, workspaceID string, c credential.Credential) (string, error)
}

// ProviderFailureReporter receives the same typed private diagnostic that the
// Broker writes to its process log.
type ProviderFailureReporter interface {
	CaptureProviderFailure(context.Context, adapter.ProviderFailureDiagnostic)
}

type Broker struct {
	conns    Connections
	factory  *Factory
	resolver Resolver
	logger   *slog.Logger
	reporter ProviderFailureReporter
}

type Option func(*Broker)

func WithLogger(logger *slog.Logger) Option {
	return func(b *Broker) {
		if logger != nil {
			b.logger = logger
		}
	}
}

func WithFailureReporter(reporter ProviderFailureReporter) Option {
	return func(b *Broker) {
		b.reporter = reporter
	}
}

func NewBroker(conns Connections, factory *Factory, resolver Resolver, opts ...Option) *Broker {
	b := &Broker{conns: conns, factory: factory, resolver: resolver, logger: slog.Default()}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Manifests exposes the registered adapters' onboarding manifests for the
// HTTP API's GET /v1/adapters.
func (b *Broker) Manifests() []adapter.Manifest { return b.factory.Manifests() }

// build constructs the adapter for one connection (no caching yet — YAGNI).
func (b *Broker) build(ctx context.Context, workspaceID string, c connection.Record) (adapter.Provider, error) {
	secret := ""
	if c.Credential.Source != "" {
		s, err := b.resolver.Resolve(ctx, workspaceID, c.Credential)
		if err != nil {
			return nil, fmt.Errorf("broker: resolve credential for %s: %w", c.ID, err)
		}
		secret = s
	}
	return b.factory.Build(c.AdapterType, c.Config, secret)
}

// connByID retrieves a connection by ID and builds its adapter.
// Unlike ListOffers and ListOwned, this intentionally does NOT filter on Authorized.
// Post-launch operations (Observe/Cancel/Release/Terminate) must still reach a run that was
// launched on a connection which has since been de-authorized, so cleanup is never stranded.
func (b *Broker) connByID(ctx context.Context, workspaceID, connectionID string) (connection.Record, adapter.Provider, error) {
	recs, err := b.conns.List(ctx, workspaceID)
	if err != nil {
		return connection.Record{}, nil, err
	}
	for _, c := range recs {
		if c.ID == connectionID {
			ad, err := b.build(ctx, workspaceID, c)
			return c, ad, err
		}
	}
	return connection.Record{}, nil, fmt.Errorf("%w: %s", ErrConnectionNotFound, connectionID)
}

func (b *Broker) ListOffers(ctx context.Context, req adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	aggregation, err := b.AggregateOffers(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := aggregation.Failures.OrNil(); err != nil {
		return nil, err
	}
	return aggregation.Offers, nil
}

func (b *Broker) AggregateOffers(ctx context.Context, req adapter.OfferRequest) (OfferAggregation, error) {
	recs, err := b.conns.List(ctx, req.WorkspaceID)
	if err != nil {
		return OfferAggregation{}, err
	}
	results := fanOut(ctx, recs, func(ctx context.Context, c connection.Record) ([]domain.OfferSnapshot, error) {
		provider, err := b.build(ctx, req.WorkspaceID, c)
		if err != nil {
			return nil, err
		}
		return provider.ListOffers(ctx, req)
	})
	aggregation := OfferAggregation{
		Offers:   []domain.OfferSnapshot{},
		Failures: ConnectionErrors{},
	}
	for _, result := range results {
		if result.err != nil {
			b.reportProviderFailure(ctx, failureDiagnostic(req.DiagnosticContext, req.WorkspaceID, result.connection.ID, result.connection.AdapterType, "list_offers"), result.err)
			aggregation.Failures = append(aggregation.Failures, connectionError(result))
			continue
		}
		for i := range result.items {
			result.items[i].ConnectionID = result.connection.ID
			result.items[i].AdapterType = result.connection.AdapterType
			id, err := offerSnapshotID(result.connection.ID, result.items[i].ID)
			if err != nil {
				return OfferAggregation{}, err
			}
			result.items[i].ID = id
		}
		aggregation.Offers = append(aggregation.Offers, result.items...)
	}
	sort.Slice(aggregation.Offers, func(i, j int) bool {
		if aggregation.Offers[i].ConnectionID != aggregation.Offers[j].ConnectionID {
			return aggregation.Offers[i].ConnectionID < aggregation.Offers[j].ConnectionID
		}
		return aggregation.Offers[i].ID < aggregation.Offers[j].ID
	})
	sortConnectionErrors(aggregation.Failures)
	return aggregation, nil
}

func offerSnapshotID(connectionID, adapterOfferID string) (string, error) {
	hash, err := domain.CanonicalHash(struct {
		ConnectionID   string
		AdapterOfferID string
	}{connectionID, adapterOfferID})
	if err != nil {
		return "", fmt.Errorf("broker: derive offer snapshot id: %w", err)
	}
	return "off_" + hash[len("sha256:"):], nil
}

func (b *Broker) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.SelectedOfferConnectionID)
	if err != nil {
		b.logLaunchFailure(ctx, req, err)
		return adapter.LaunchReceipt{}, err
	}
	receipt, err := ad.Launch(ctx, req)
	if err != nil {
		b.logLaunchFailure(ctx, req, err)
	}
	return receipt, err
}

func (b *Broker) logLaunchFailure(ctx context.Context, req adapter.LaunchRequest, err error) {
	diagnostic := failureDiagnostic(req.ProviderOperationContext(), req.WorkspaceID, req.SelectedOfferConnectionID, req.SelectedOfferAdapterType, "launch")
	diagnostic.AlternativesExhausted = req.FinalPreStartAttempt
	b.reportProviderFailure(ctx, diagnostic, err)
}

func (b *Broker) reportProviderFailure(ctx context.Context, diagnostic adapter.ProviderFailureDiagnostic, err error) {
	diagnostic.Failure = adapter.ProviderFailure{
		Kind:       adapter.ProviderFailureKind("unclassified"),
		SideEffect: adapter.SideEffectNone,
	}
	var failure *adapter.ProviderFailure
	typedFailure := errors.As(err, &failure)
	if typedFailure {
		diagnostic.Failure = *failure
	}
	b.logger.ErrorContext(ctx, "provider operation failed",
		"workspace_id", diagnostic.WorkspaceID,
		"run_id", diagnostic.RunID,
		"attempt_id", diagnostic.AttemptID,
		"connection_id", diagnostic.ConnectionID,
		"adapter_type", diagnostic.AdapterType,
		"operation", diagnostic.Operation,
		"offer_snapshot_id", diagnostic.OfferSnapshotID,
		"offer_native_ref", diagnostic.OfferNativeRef,
		"failure_kind", diagnostic.Failure.Kind,
		"http_status", diagnostic.Failure.Status,
		"provider_code", diagnostic.Failure.ProviderCode,
		"response_body", diagnostic.Failure.ResponseBody,
		"retryable", diagnostic.Failure.Retryable,
		"side_effect", diagnostic.Failure.SideEffect,
		"retry_count", diagnostic.Failure.RetryCount,
		"response_truncated", diagnostic.Failure.ResponseTruncated,
		"alternatives_exhausted", diagnostic.AlternativesExhausted,
	)
	if typedFailure && b.reporter != nil {
		b.reporter.CaptureProviderFailure(ctx, diagnostic)
	}
}

func (b *Broker) Observe(ctx context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	connection, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		b.reportProviderFailure(ctx, failureDiagnostic(req.DiagnosticContext, req.WorkspaceID, req.ConnectionID, connection.AdapterType, "observe"), err)
		return adapter.ExternalObservation{}, err
	}
	observation, err := ad.Observe(ctx, req)
	if err != nil {
		b.reportProviderFailure(ctx, failureDiagnostic(req.DiagnosticContext, req.WorkspaceID, req.ConnectionID, connection.AdapterType, "observe"), err)
	}
	return observation, err
}

func (b *Broker) Cancel(ctx context.Context, req adapter.CancelRequest) (adapter.CancelReceipt, error) {
	connection, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		b.reportProviderFailure(ctx, failureDiagnostic(req.DiagnosticContext, req.WorkspaceID, req.ConnectionID, connection.AdapterType, "cancel"), err)
		return adapter.CancelReceipt{}, err
	}
	receipt, err := ad.Cancel(ctx, req)
	if err != nil {
		b.reportProviderFailure(ctx, failureDiagnostic(req.DiagnosticContext, req.WorkspaceID, req.ConnectionID, connection.AdapterType, "cancel"), err)
	}
	return receipt, err
}

func (b *Broker) Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	connection, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		b.reportProviderFailure(ctx, failureDiagnostic(req.DiagnosticContext, req.WorkspaceID, req.ConnectionID, connection.AdapterType, "release"), err)
		return adapter.ReleaseReceipt{}, err
	}
	receipt, err := ad.Release(ctx, req)
	if err != nil {
		b.reportProviderFailure(ctx, failureDiagnostic(req.DiagnosticContext, req.WorkspaceID, req.ConnectionID, connection.AdapterType, "release"), err)
	}
	return receipt, err
}

func (b *Broker) Terminate(ctx context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	connection, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		b.reportProviderFailure(ctx, failureDiagnostic(req.DiagnosticContext, req.WorkspaceID, req.ConnectionID, connection.AdapterType, "terminate"), err)
		return adapter.TerminateReceipt{}, err
	}
	receipt, err := ad.Terminate(ctx, req)
	if err != nil {
		b.reportProviderFailure(ctx, failureDiagnostic(req.DiagnosticContext, req.WorkspaceID, req.ConnectionID, connection.AdapterType, "terminate"), err)
	}
	return receipt, err
}

func failureDiagnostic(correlation adapter.ProviderOperationContext, workspaceID, connectionID, adapterType, operation string) adapter.ProviderFailureDiagnostic {
	diagnostic := correlation.FailureDiagnostic(operation)
	diagnostic.WorkspaceID = workspaceID
	diagnostic.ConnectionID = connectionID
	diagnostic.AdapterType = adapterType
	return diagnostic
}

func (b *Broker) ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	recs, err := b.conns.List(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	results := fanOut(ctx, recs, func(ctx context.Context, c connection.Record) ([]adapter.OwnedExternalObject, error) {
		provider, err := b.build(ctx, req.WorkspaceID, c)
		if err != nil {
			return nil, err
		}
		return provider.ListOwned(ctx, req)
	})
	var all []adapter.OwnedExternalObject
	var failures ConnectionErrors
	for _, result := range results {
		if result.err != nil {
			b.reportProviderFailure(ctx, failureDiagnostic(req.DiagnosticContext, req.WorkspaceID, result.connection.ID, result.connection.AdapterType, "list_owned"), result.err)
			failures = append(failures, connectionError(result))
			continue
		}
		for i := range result.items {
			result.items[i].ConnectionID = result.connection.ID
			result.items[i].AdapterType = result.connection.AdapterType
		}
		all = append(all, result.items...)
	}
	sortConnectionErrors(failures)
	if err := failures.OrNil(); err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ConnectionID != all[j].ConnectionID {
			return all[i].ConnectionID < all[j].ConnectionID
		}
		return all[i].ExternalID < all[j].ExternalID
	})
	return all, nil
}

func connectionError[T any](result fanoutResult[T]) ConnectionError {
	return ConnectionError{
		ConnectionID: result.connection.ID,
		AdapterType:  result.connection.AdapterType,
		Err:          result.err,
	}
}

func sortConnectionErrors(failures ConnectionErrors) {
	sort.Slice(failures, func(i, j int) bool { return failures[i].ConnectionID < failures[j].ConnectionID })
}

// VerifyConnection builds the adapter for one connection (regardless of its
// current Authorized state — authorize runs before the flag is set) and calls
// its cheap Verify check. Used by the connection authorize flow.
func (b *Broker) VerifyConnection(ctx context.Context, workspaceID, connectionID string) error {
	connection, ad, err := b.connByID(ctx, workspaceID, connectionID)
	if err != nil {
		b.reportProviderFailure(ctx, failureDiagnostic(adapter.ProviderOperationContext{}, workspaceID, connectionID, connection.AdapterType, "verify"), err)
		return err
	}
	err = ad.Verify(ctx)
	if err != nil {
		b.reportProviderFailure(ctx, failureDiagnostic(adapter.ProviderOperationContext{}, workspaceID, connectionID, connection.AdapterType, "verify"), err)
	}
	return err
}
