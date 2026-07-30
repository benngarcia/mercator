package eventlog

import (
	"context"
	"strings"
	"testing"
)

func TestReadFullStreamContinuesAfterShortPages(t *testing.T) {
	reader := shortPageReader{events: []StoredEvent{
		{ID: "evt_1", StreamVersion: 1, GlobalPosition: 1},
		{ID: "evt_2", StreamVersion: 2, GlobalPosition: 2},
		{ID: "evt_3", StreamVersion: 3, GlobalPosition: 3},
	}}

	history, err := ReadFullStream(context.Background(), reader, StreamKey{WorkspaceID: "ws_1", Type: "run", ID: "run_1"})
	if err != nil {
		t.Fatalf("read full stream: %v", err)
	}
	if len(history.Events) != 3 || history.LastVersion != 3 {
		t.Fatalf("history = %d events at version %d, want 3 events at version 3", len(history.Events), history.LastVersion)
	}
}

func TestScanAllContinuesAfterShortPages(t *testing.T) {
	reader := shortPageReader{events: []StoredEvent{
		{ID: "evt_1", GlobalPosition: 1},
		{ID: "evt_2", GlobalPosition: 2},
		{ID: "evt_3", GlobalPosition: 3},
	}}

	var eventIDs []string
	for event, err := range ScanAll(context.Background(), reader, 3, EventFilter{}) {
		if err != nil {
			t.Fatalf("scan all: %v", err)
		}
		eventIDs = append(eventIDs, event.ID)
	}
	if strings.Join(eventIDs, ",") != "evt_1,evt_2,evt_3" {
		t.Fatalf("scanned events = %v, want all three events", eventIDs)
	}
}

func TestCompleteScansRejectNonAdvancingReaders(t *testing.T) {
	reader := stalledReader{}
	_, streamErr := ReadFullStream(context.Background(), reader, StreamKey{WorkspaceID: "ws_1", Type: "run", ID: "run_1"})
	if streamErr == nil || !strings.Contains(streamErr.Error(), "did not advance") {
		t.Fatalf("stream error = %v, want cursor progress error", streamErr)
	}

	var globalErr error
	for _, err := range ScanAll(context.Background(), reader, 2, EventFilter{}) {
		if err != nil {
			globalErr = err
		}
	}
	if globalErr == nil || !strings.Contains(globalErr.Error(), "did not advance") {
		t.Fatalf("global error = %v, want cursor progress error", globalErr)
	}
}

func TestScanAllStopsAtSnapshotWhileReaderKeepsAppending(t *testing.T) {
	reader := &growingPageReader{}

	var positions []GlobalPosition
	for event, err := range ScanAll(context.Background(), reader, 3, EventFilter{}) {
		if err != nil {
			t.Fatalf("scan all: %v", err)
		}
		positions = append(positions, event.GlobalPosition)
	}

	if len(positions) != 3 || positions[2] != 3 {
		t.Fatalf("positions = %v, want snapshot through position 3", positions)
	}
}

type shortPageReader struct {
	events []StoredEvent
}

func (r shortPageReader) ReadStream(_ context.Context, _ StreamKey, afterVersion uint64, _ int) ([]StoredEvent, error) {
	for _, event := range r.events {
		if event.StreamVersion > afterVersion {
			return []StoredEvent{event}, nil
		}
	}
	return nil, nil
}

func (r shortPageReader) ReadAll(_ context.Context, after GlobalPosition, _ int, _ EventFilter) ([]StoredEvent, error) {
	for _, event := range r.events {
		if event.GlobalPosition > after {
			return []StoredEvent{event}, nil
		}
	}
	return nil, nil
}

type stalledReader struct{}

func (stalledReader) ReadStream(_ context.Context, _ StreamKey, _ uint64, _ int) ([]StoredEvent, error) {
	return []StoredEvent{{ID: "evt_stalled", StreamVersion: 1}}, nil
}

func (stalledReader) ReadAll(_ context.Context, _ GlobalPosition, _ int, _ EventFilter) ([]StoredEvent, error) {
	return []StoredEvent{{ID: "evt_stalled", GlobalPosition: 1}}, nil
}

type growingPageReader struct{}

func (r *growingPageReader) ReadAll(_ context.Context, after GlobalPosition, _ int, _ EventFilter) ([]StoredEvent, error) {
	next := after + 1
	return []StoredEvent{{ID: "evt_growing", GlobalPosition: next}}, nil
}
