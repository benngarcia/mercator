package broker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/domain"
)

type ConnectionError struct {
	ConnectionID string
	AdapterType  string
	Err          error
}

func (e ConnectionError) Error() string {
	return fmt.Sprintf("connection %s (%s): %v", e.ConnectionID, e.AdapterType, e.Err)
}

func (e ConnectionError) Unwrap() error { return e.Err }

type ConnectionErrors []ConnectionError

func (errs ConnectionErrors) Error() string {
	messages := make([]string, len(errs))
	for i, err := range errs {
		messages[i] = err.Error()
	}
	return strings.Join(messages, "; ")
}

func (errs ConnectionErrors) Unwrap() []error {
	unwrapped := make([]error, len(errs))
	for i, err := range errs {
		unwrapped[i] = err
	}
	return unwrapped
}

func (errs ConnectionErrors) OrNil() error {
	if len(errs) == 0 {
		return nil
	}
	return errs
}

type OfferAggregation struct {
	Offers   []domain.OfferSnapshot
	Failures ConnectionErrors
	// Queried is every connection this aggregation asked, whatever it answered,
	// and Excluded is every connection it did not ask. They are the census the
	// offers cannot carry: a connection that answered with nothing and a
	// connection nobody contacted both publish no offer, and Placement reads an
	// empty answer as the strongest thing a fleet can say about an ask.
	Queried  []string
	Excluded []string
}

type fanoutResult[T any] struct {
	connection connection.Record
	items      []T
	err        error
}

// fanOut asks every connection this workspace holds and reports which ones it
// asked. A connection an operator de-authorised is not asked, and it is named
// rather than dropped: an answer nobody was asked for is not an answer, and a
// reader of the record cannot otherwise tell one from a fleet that published
// nothing.
func fanOut[T any](
	ctx context.Context,
	connections []connection.Record,
	query func(context.Context, connection.Record) ([]T, error),
) ([]fanoutResult[T], []string) {
	results := make(chan fanoutResult[T], len(connections))
	excluded := []string{}
	var group sync.WaitGroup
	for _, record := range connections {
		if !record.Authorized {
			excluded = append(excluded, record.ID+": not authorized")
			continue
		}
		group.Go(func() {
			items, err := query(ctx, record)
			results <- fanoutResult[T]{connection: record, items: items, err: err}
		})
	}
	group.Wait()
	close(results)

	collected := make([]fanoutResult[T], 0, len(results))
	for result := range results {
		collected = append(collected, result)
	}
	sort.Strings(excluded)
	return collected, excluded
}
