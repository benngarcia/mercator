package broker

import (
	"context"
	"fmt"
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
}

type fanoutResult[T any] struct {
	connection connection.Record
	items      []T
	err        error
}

func fanOut[T any](
	ctx context.Context,
	connections []connection.Record,
	query func(context.Context, connection.Record) ([]T, error),
) []fanoutResult[T] {
	results := make(chan fanoutResult[T], len(connections))
	var group sync.WaitGroup
	for _, record := range connections {
		if !record.Authorized {
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
	return collected
}
