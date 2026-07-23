package httpapi

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// A subscriber can drain its cap-1 buffer between publish's failed
// non-blocking send and the eviction receive. The eviction must not block:
// publish holds the catalog mutex, so a parked publish would wedge
// Subscribe, Refresh, and unsubscribe with it.
func TestOfferCatalogPublishSurvivesConcurrentSubscriberDrain(t *testing.T) {
	catalog := newOfferCatalog(nil, time.Hour)
	workspace := &offerCatalogWorkspace{
		cancel:      func() {},
		refresh:     make(chan struct{}, 1),
		subscribers: map[chan offerCatalogSnapshot]struct{}{},
	}
	catalog.workspaces["ws_drain"] = workspace
	updates := make(chan offerCatalogSnapshot, 1)
	workspace.subscribers[updates] = struct{}{}

	stop := make(chan struct{})
	var drainer sync.WaitGroup
	drainer.Add(1)
	go func() {
		defer drainer.Done()
		for {
			select {
			case <-stop:
				return
			case <-updates:
			}
		}
	}()

	published := make(chan struct{})
	go func() {
		defer close(published)
		for i := range 500_000 {
			catalog.publish("ws_drain", workspace, offerCatalogSnapshot{
				WorkspaceID: "ws_drain",
				Revision:    strconv.Itoa(i),
			})
		}
	}()

	select {
	case <-published:
	case <-time.After(30 * time.Second):
		t.Fatal("publish wedged while a subscriber drained concurrently")
	}
	close(stop)
	drainer.Wait()
}
