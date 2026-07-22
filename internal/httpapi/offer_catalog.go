package httpapi

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

const offerObservationInterval = 10 * time.Second

type offerCatalogSnapshot struct {
	WorkspaceID string                 `json:"workspace_id"`
	Revision    string                 `json:"revision"`
	ObservedAt  time.Time              `json:"observed_at"`
	Offers      []domain.OfferSnapshot `json:"offers"`
	Failures    []ConnectionFailure    `json:"failures"`
	Err         error                  `json:"-"`
}

type offerCatalog struct {
	offers     OfferAggregator
	interval   time.Duration
	mu         sync.Mutex
	workspaces map[string]*offerCatalogWorkspace
}

type offerCatalogWorkspace struct {
	cancel      context.CancelFunc
	refresh     chan struct{}
	subscribers map[chan offerCatalogSnapshot]struct{}
	latest      *offerCatalogSnapshot
}

func newOfferCatalog(offers OfferAggregator, interval time.Duration) *offerCatalog {
	return &offerCatalog{
		offers:     offers,
		interval:   interval,
		workspaces: map[string]*offerCatalogWorkspace{},
	}
}

func (c *offerCatalog) Subscribe(ctx context.Context, workspaceID string) <-chan offerCatalogSnapshot {
	updates := make(chan offerCatalogSnapshot, 1)
	c.mu.Lock()
	workspace := c.workspaces[workspaceID]
	if workspace == nil {
		watchCtx, cancel := context.WithCancel(context.Background())
		workspace = &offerCatalogWorkspace{
			cancel:      cancel,
			refresh:     make(chan struct{}, 1),
			subscribers: map[chan offerCatalogSnapshot]struct{}{},
		}
		c.workspaces[workspaceID] = workspace
		go c.observe(watchCtx, workspaceID, workspace)
	}
	workspace.subscribers[updates] = struct{}{}
	if workspace.latest != nil {
		updates <- *workspace.latest
	}
	c.mu.Unlock()
	go func() {
		<-ctx.Done()
		c.unsubscribe(workspaceID, workspace, updates)
	}()
	return updates
}

func (c *offerCatalog) Refresh(workspaceID string) {
	c.mu.Lock()
	workspace := c.workspaces[workspaceID]
	c.mu.Unlock()
	if workspace == nil {
		return
	}
	select {
	case workspace.refresh <- struct{}{}:
	default:
	}
}

func (c *offerCatalog) observe(ctx context.Context, workspaceID string, workspace *offerCatalogWorkspace) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-workspace.refresh:
		case <-timer.C:
		}
		c.publish(workspaceID, workspace, c.snapshot(ctx, workspaceID))
		timer.Reset(c.interval)
	}
}

func (c *offerCatalog) snapshot(ctx context.Context, workspaceID string) offerCatalogSnapshot {
	aggregation, err := c.offers.AggregateOffers(ctx, adapter.OfferRequest{WorkspaceID: workspaceID})
	if err != nil {
		return offerCatalogSnapshot{WorkspaceID: workspaceID, Err: err}
	}
	offers := append([]domain.OfferSnapshot(nil), aggregation.Offers...)
	sort.Slice(offers, func(i, j int) bool { return offers[i].ID < offers[j].ID })
	failures := connectionFailureResponses(aggregation.Failures)
	revision, err := domain.CanonicalHash(struct {
		Offers   []domain.OfferSnapshot
		Failures []ConnectionFailure
	}{offers, failures})
	return offerCatalogSnapshot{
		WorkspaceID: workspaceID,
		Revision:    revision,
		ObservedAt:  time.Now().UTC(),
		Offers:      offers,
		Failures:    failures,
		Err:         err,
	}
}

func (c *offerCatalog) publish(workspaceID string, workspace *offerCatalogWorkspace, snapshot offerCatalogSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.workspaces[workspaceID] != workspace {
		return
	}
	if snapshot.Err == nil && workspace.latest != nil && workspace.latest.Err == nil && workspace.latest.Revision == snapshot.Revision {
		return
	}
	workspace.latest = &snapshot
	for subscriber := range workspace.subscribers {
		select {
		case subscriber <- snapshot:
		default:
			<-subscriber
			subscriber <- snapshot
		}
	}
}

func (c *offerCatalog) unsubscribe(workspaceID string, workspace *offerCatalogWorkspace, updates chan offerCatalogSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.workspaces[workspaceID] != workspace {
		return
	}
	delete(workspace.subscribers, updates)
	close(updates)
	if len(workspace.subscribers) == 0 {
		delete(c.workspaces, workspaceID)
		workspace.cancel()
	}
}
