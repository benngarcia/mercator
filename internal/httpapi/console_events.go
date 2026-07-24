package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/google/uuid"
)

const (
	consoleReplayPageSize = 100
	consoleHeartbeat      = 15 * time.Second
)

func (s *Server) StreamConsoleEvents(ctx context.Context, request StreamConsoleEventsRequestObject) (StreamConsoleEventsResponseObject, error) {
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return StreamConsoleEvents403JSONResponse(workspaceErr.Response), nil
		}
		return StreamConsoleEvents400JSONResponse(workspaceErr.Response), nil
	}
	if s.events == nil || s.offerCatalog == nil {
		return StreamConsoleEvents501JSONResponse(apiError("CONSOLE_EVENT_FEED_DISABLED", "Console event feed is not configured.")), nil
	}
	after, err := consoleCursor(request.Params.LastEventID)
	if err != nil {
		return StreamConsoleEvents400JSONResponse(apiError("INVALID_EVENT_CURSOR", err.Error())), nil
	}
	filter := eventlog.EventFilter{WorkspaceID: workspaceID, Visibility: eventlog.VisibilityPublic}
	head, err := s.events.LatestPosition(ctx, filter)
	if err != nil {
		return StreamConsoleEvents501JSONResponse(internalAPIError(501, "EVENT_LOG_UNAVAILABLE", err)), nil
	}
	if after > head {
		return StreamConsoleEvents400JSONResponse(apiError("EVENT_CURSOR_AHEAD", "Last-Event-ID is ahead of this Workspace event feed.")), nil
	}
	catalogUpdates := s.offerCatalog.Subscribe(ctx, workspaceID)
	initialCatalog, ok := <-catalogUpdates
	if !ok {
		return StreamConsoleEvents502JSONResponse(apiError("OFFER_CATALOG_UNAVAILABLE", "Offer catalog closed before its initial observation.")), nil
	}
	if initialCatalog.Err != nil {
		return StreamConsoleEvents502JSONResponse(internalAPIError(502, "OFFER_CATALOG_UNAVAILABLE", initialCatalog.Err)), nil
	}

	reader, writer := io.Pipe()
	go func() {
		if err := s.writeConsoleStream(ctx, writer, workspaceID, after, head, filter, initialCatalog, catalogUpdates); err != nil && ctx.Err() == nil {
			log.Printf("httpapi: console event feed for %s closed: %v", workspaceID, err)
		}
		_ = writer.Close()
	}()
	return StreamConsoleEvents200TexteventStreamResponse{Body: reader}, nil
}

func consoleCursor(value string) (eventlog.GlobalPosition, error) {
	if value == "" {
		return 0, nil
	}
	position, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("Last-Event-ID must be an unsigned event position")
	}
	return eventlog.GlobalPosition(position), nil
}

func (s *Server) writeConsoleStream(
	ctx context.Context,
	writer io.Writer,
	workspaceID string,
	after eventlog.GlobalPosition,
	head eventlog.GlobalPosition,
	filter eventlog.EventFilter,
	initialCatalog offerCatalogSnapshot,
	catalogUpdates <-chan offerCatalogSnapshot,
) error {
	if err := s.writeReplay(ctx, writer, after, head, filter); err != nil {
		return err
	}
	if err := writeConsoleMessage(writer, "offers_replaced", "", initialCatalog); err != nil {
		return err
	}
	if err := writeConsoleMessage(writer, "ready", "", map[string]eventlog.GlobalPosition{"through_global_position": head}); err != nil {
		return err
	}
	deliveries, err := s.events.Subscribe(ctx, eventlog.SubscriptionRequest{
		SubscriptionID: "console:" + workspaceID + ":" + uuid.NewString(),
		After:          head,
		Filter:         filter,
	})
	if err != nil {
		return err
	}
	heartbeat := time.NewTicker(consoleHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case delivery, open := <-deliveries:
			if !open {
				return nil
			}
			if err := writeDomainEvent(writer, delivery.Event); err != nil {
				return err
			}
			if strings.HasPrefix(delivery.Event.Type, "compute.connection.") {
				s.offerCatalog.Refresh(workspaceID)
			}
		case snapshot, open := <-catalogUpdates:
			if !open {
				return nil
			}
			if snapshot.Err != nil {
				if err := writeConsoleMessage(writer, "offers_unavailable", "", map[string]string{"workspace_id": workspaceID}); err != nil {
					return err
				}
				continue
			}
			if err := writeConsoleMessage(writer, "offers_replaced", "", snapshot); err != nil {
				return err
			}
		case <-heartbeat.C:
			if _, err := io.WriteString(writer, ": heartbeat\n\n"); err != nil {
				return err
			}
		}
	}
}

func (s *Server) writeReplay(ctx context.Context, writer io.Writer, after, head eventlog.GlobalPosition, filter eventlog.EventFilter) error {
	cursor := after
	for cursor < head {
		events, err := s.events.ReadAll(ctx, cursor, consoleReplayPageSize, filter)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return fmt.Errorf("event replay stopped before public head %d", head)
		}
		for _, event := range events {
			if event.GlobalPosition > head {
				return nil
			}
			if err := writeDomainEvent(writer, event); err != nil {
				return err
			}
			cursor = event.GlobalPosition
		}
	}
	return nil
}

func writeDomainEvent(writer io.Writer, event eventlog.StoredEvent) error {
	return writeConsoleMessage(writer, "domain_event", strconv.FormatUint(uint64(event.GlobalPosition), 10), event.CloudEvent())
}

func writeConsoleMessage(writer io.Writer, eventType, id string, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if id != "" {
		if _, err := fmt.Fprintf(writer, "id: %s\n", id); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", eventType, encoded)
	return err
}
