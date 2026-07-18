package broker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/connection"
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

type connectionResult[T any] struct {
	connection connection.Record
	items      []T
}

type fanoutResult[T any] struct {
	connection connection.Record
	items      []T
	err        error
}

func fanOut[T any](
	ctx context.Context,
	broker *Broker,
	workspaceID string,
	connections []connection.Record,
	query func(context.Context, adapter.Provider) ([]T, error),
) ([]connectionResult[T], ConnectionErrors) {
	results := make(chan fanoutResult[T], len(connections))
	var group sync.WaitGroup
	for _, record := range connections {
		if !record.Authorized {
			continue
		}
		group.Go(func() {
			provider, err := broker.build(ctx, workspaceID, record)
			if err == nil {
				var items []T
				items, err = query(ctx, provider)
				results <- fanoutResult[T]{connection: record, items: items, err: err}
				return
			}
			results <- fanoutResult[T]{connection: record, err: err}
		})
	}
	group.Wait()
	close(results)

	var successes []connectionResult[T]
	var failures ConnectionErrors
	for result := range results {
		if result.err != nil {
			failures = append(failures, ConnectionError{
				ConnectionID: result.connection.ID,
				AdapterType:  result.connection.AdapterType,
				Err:          result.err,
			})
			continue
		}
		successes = append(successes, connectionResult[T]{connection: result.connection, items: result.items})
	}
	sort.Slice(successes, func(i, j int) bool { return successes[i].connection.ID < successes[j].connection.ID })
	sort.Slice(failures, func(i, j int) bool { return failures[i].ConnectionID < failures[j].ConnectionID })
	return successes, failures
}
