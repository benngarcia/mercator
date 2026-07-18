package eventlog

import "context"

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

type GlobalHistory struct {
	Events       []StoredEvent
	LastPosition GlobalPosition
}

// ReadFullStream returns every event in a stream and the last stored stream
// version. Callers use LastVersion for optimistic concurrency.
func ReadFullStream(ctx context.Context, reader StreamReader, stream StreamKey) (StreamHistory, error) {
	var history StreamHistory
	for {
		page, err := reader.ReadStream(ctx, stream, history.LastVersion, historyPageSize)
		if err != nil {
			return StreamHistory{}, err
		}
		history.Events = append(history.Events, page...)
		if len(page) == 0 {
			return history, nil
		}
		history.LastVersion = page[len(page)-1].StreamVersion
		if len(page) < historyPageSize {
			return history, nil
		}
	}
}

// ScanAll returns every event matching a global filter and the last stored
// global position in that result.
func ScanAll(ctx context.Context, reader GlobalReader, filter EventFilter) (GlobalHistory, error) {
	var history GlobalHistory
	for {
		page, err := reader.ReadAll(ctx, history.LastPosition, historyPageSize, filter)
		if err != nil {
			return GlobalHistory{}, err
		}
		history.Events = append(history.Events, page...)
		if len(page) == 0 {
			return history, nil
		}
		history.LastPosition = page[len(page)-1].GlobalPosition
		if len(page) < historyPageSize {
			return history, nil
		}
	}
}
