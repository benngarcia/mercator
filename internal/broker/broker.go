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

type Broker struct {
	conns    Connections
	factory  *Factory
	resolver Resolver
	logger   *slog.Logger
}

type Option func(*Broker)

func WithLogger(logger *slog.Logger) Option {
	return func(b *Broker) {
		if logger != nil {
			b.logger = logger
		}
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
	kind := "unclassified"
	status := 0
	providerCode := ""
	responseBody := ""
	retryable := false
	sideEffect := adapter.SideEffectNone
	retryCount := 0
	truncated := false
	var failure *adapter.ProviderFailure
	if errors.As(err, &failure) {
		kind = string(failure.Kind)
		status = failure.Status
		providerCode = failure.ProviderCode
		responseBody = failure.ResponseBody
		retryable = failure.Retryable
		sideEffect = failure.SideEffect
		retryCount = failure.RetryCount
		truncated = failure.ResponseTruncated
	}
	b.logger.ErrorContext(ctx, "provider operation failed",
		"workspace_id", req.WorkspaceID,
		"run_id", req.RunID,
		"attempt_id", req.AttemptID,
		"connection_id", req.SelectedOfferConnectionID,
		"adapter_type", req.SelectedOfferAdapterType,
		"operation", "launch",
		"offer_snapshot_id", req.SelectedOfferSnapshotID,
		"offer_native_ref", req.SelectedOfferNativeRef,
		"failure_kind", kind,
		"http_status", status,
		"provider_code", providerCode,
		"response_body", responseBody,
		"retryable", retryable,
		"side_effect", sideEffect,
		"retry_count", retryCount,
		"response_truncated", truncated,
	)
}

func (b *Broker) Observe(ctx context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	return ad.Observe(ctx, req)
}

func (b *Broker) Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		return adapter.ReleaseReceipt{}, err
	}
	return ad.Release(ctx, req)
}

func (b *Broker) Terminate(ctx context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	_, ad, err := b.connByID(ctx, req.WorkspaceID, req.ConnectionID)
	if err != nil {
		return adapter.TerminateReceipt{}, err
	}
	return ad.Terminate(ctx, req)
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
			failures = append(failures, connectionError(result))
			continue
		}
		for i := range result.items {
			result.items[i].ConnectionID = result.connection.ID
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
	_, ad, err := b.connByID(ctx, workspaceID, connectionID)
	if err != nil {
		return err
	}
	return ad.Verify(ctx)
}
