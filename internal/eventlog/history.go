package eventlog

import (
	"context"
	"fmt"
	"iter"
)

const historyPageSize = 1000

type StreamReader interface {
	ReadStream(ctx context.Context, stream StreamKey, afterVersion uint64, limit int) ([]StoredEvent, error)
}

type GlobalReader interface {
	ReadAll(ctx context.Context, after GlobalPosition, limit int, filter EventFilter) ([]StoredEvent, error)
}

type StreamHistory struct {
	Events      []StoredEvent
	LastVersion uint64
}

// ReadFullStream returns every event in a stream and the last stored stream
// version. Callers use LastVersion for optimistic concurrency.
func ReadFullStream(ctx context.Context, reader StreamReader, stream StreamKey) (StreamHistory, error) {
	var history StreamHistory
	for event, err := range ScanStream(ctx, reader, stream) {
		if err != nil {
			return StreamHistory{}, err
		}
		history.Events = append(history.Events, event)
		history.LastVersion = event.StreamVersion
	}
	return history, nil
}

// ScanStream yields every event in a stream without retaining its full history.
func ScanStream(ctx context.Context, reader StreamReader, stream StreamKey) iter.Seq2[StoredEvent, error] {
	return func(yield func(StoredEvent, error) bool) {
		var after uint64
		for {
			page, err := reader.ReadStream(ctx, stream, after, historyPageSize)
			if err != nil {
				yield(StoredEvent{}, err)
				return
			}
			if len(page) == 0 {
				return
			}
			for _, event := range page {
				if event.StreamVersion <= after {
					yield(StoredEvent{}, fmt.Errorf("eventlog: stream scan did not advance beyond version %d", after))
					return
				}
				after = event.StreamVersion
				if !yield(event, nil) {
					return
				}
			}
		}
	}
}

// ScanAll yields every event matching a global filter without retaining the
// complete result in memory.
func ScanAll(ctx context.Context, reader GlobalReader, filter EventFilter) iter.Seq2[StoredEvent, error] {
	return func(yield func(StoredEvent, error) bool) {
		var after GlobalPosition
		for {
			page, err := reader.ReadAll(ctx, after, historyPageSize, filter)
			if err != nil {
				yield(StoredEvent{}, err)
				return
			}
			if len(page) == 0 {
				return
			}
			for _, event := range page {
				if event.GlobalPosition <= after {
					yield(StoredEvent{}, fmt.Errorf("eventlog: global scan did not advance beyond position %d", after))
					return
				}
				after = event.GlobalPosition
				if !yield(event, nil) {
					return
				}
			}
		}
	}
}
