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
	Revision   string                 `json:"revision"`
	ObservedAt time.Time              `json:"observed_at"`
	Offers     []domain.OfferSnapshot `json:"offers"`
	Failures   []ConnectionFailure    `json:"failures"`
	Err        error                  `json:"-"`
}

type offerCatalog struct {
	offers   OfferAggregator
	interval time.Duration
	mu       sync.Mutex
	state    *offerCatalogState
}

type offerCatalogState struct {
	cancel      context.CancelFunc
	refresh     chan struct{}
	subscribers map[chan offerCatalogSnapshot]struct{}
	latest      *offerCatalogSnapshot
}

func newOfferCatalog(offers OfferAggregator, interval time.Duration) *offerCatalog {
	return &offerCatalog{
		offers: offers, interval: interval,
	}
}

func (c *offerCatalog) Subscribe(ctx context.Context) <-chan offerCatalogSnapshot {
	updates := make(chan offerCatalogSnapshot, 1)
	c.mu.Lock()
	state := c.state
	if state == nil {
		watchCtx, cancel := context.WithCancel(context.Background())
		state = &offerCatalogState{
			cancel:      cancel,
			refresh:     make(chan struct{}, 1),
			subscribers: map[chan offerCatalogSnapshot]struct{}{},
		}
		c.state = state
		go c.observe(watchCtx, state)
	}
	state.subscribers[updates] = struct{}{}
	if state.latest != nil {
		updates <- *state.latest
	}
	c.mu.Unlock()
	go func() {
		<-ctx.Done()
		c.unsubscribe(state, updates)
	}()
	return updates
}

func (c *offerCatalog) Refresh() {
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	if state == nil {
		return
	}
	select {
	case state.refresh <- struct{}{}:
	default:
	}
}

func (c *offerCatalog) observe(ctx context.Context, state *offerCatalogState) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-state.refresh:
		case <-timer.C:
		}
		c.publish(state, c.snapshot(ctx))
		timer.Reset(c.interval)
	}
}

func (c *offerCatalog) snapshot(ctx context.Context) offerCatalogSnapshot {
	aggregation, err := c.offers.AggregateOffers(ctx, adapter.OfferRequest{})
	if err != nil {
		return offerCatalogSnapshot{Err: err}
	}
	offers := append([]domain.OfferSnapshot{}, aggregation.Offers...)
	sort.Slice(offers, func(i, j int) bool { return offers[i].ID < offers[j].ID })
	failures := connectionFailureResponses(aggregation.Failures)
	revision, err := domain.CanonicalHash(struct {
		Offers   []domain.OfferSnapshot
		Failures []ConnectionFailure
	}{offers, failures})
	return offerCatalogSnapshot{

		Revision:   revision,
		ObservedAt: time.Now().UTC(),
		Offers:     offers,
		Failures:   failures,
		Err:        err,
	}
}

func (c *offerCatalog) publish(state *offerCatalogState, snapshot offerCatalogSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != state {
		return
	}
	if snapshot.Err == nil && state.latest != nil && state.latest.Err == nil && state.latest.Revision == snapshot.Revision {
		return
	}
	state.latest = &snapshot
	for subscriber := range state.subscribers {
		select {
		case subscriber <- snapshot:
		default:
			// The subscriber may drain its buffer between the failed send above
			// and this eviction, so the receive must not block: publish holds
			// c.mu, and a parked publish would wedge Subscribe, Refresh, and
			// unsubscribe with it.
			select {
			case <-subscriber:
			default:
			}
			subscriber <- snapshot
		}
	}
}

func (c *offerCatalog) unsubscribe(state *offerCatalogState, updates chan offerCatalogSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != state {
		return
	}
	delete(state.subscribers, updates)
	close(updates)
	if len(state.subscribers) == 0 {
		c.state = nil
		state.cancel()
	}
}
