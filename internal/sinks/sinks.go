package sinks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bengarcia/mercator/internal/eventlog"
)

type EventLog interface {
	ReadAll(ctx context.Context, after eventlog.GlobalPosition, limit int, filter eventlog.EventFilter) ([]eventlog.StoredEvent, error)
	Offset(ctx context.Context, subscriptionID string) (eventlog.GlobalPosition, bool, error)
	Ack(ctx context.Context, subscriptionID string, position eventlog.GlobalPosition) error
}

type Sink interface {
	Deliver(context.Context, eventlog.StoredEvent) error
}

type DiscardSink struct{}

func (DiscardSink) Deliver(context.Context, eventlog.StoredEvent) error {
	return nil
}

type Manager struct {
	log       EventLog
	sinks     map[string]Sink
	batchSize int
}

type Result struct {
	SinkID        string                  `json:"sink_id"`
	Delivered     int                     `json:"delivered"`
	LastPosition  eventlog.GlobalPosition `json:"last_position"`
	FailedEventID string                  `json:"failed_event_id,omitempty"`
	ReplayID      string                  `json:"replay_id,omitempty"`
}

type Status struct {
	SinkID    string                  `json:"sink_id"`
	Cursor    eventlog.GlobalPosition `json:"cursor"`
	HasCursor bool                    `json:"has_cursor"`
}

type ReplayRequest struct {
	SinkID        string                  `json:"sink_id"`
	FromExclusive eventlog.GlobalPosition `json:"from_exclusive"`
	Limit         int                     `json:"limit"`
	ReplayID      string                  `json:"replay_id"`
}

func NewManager(log EventLog, configured map[string]Sink) *Manager {
	copied := map[string]Sink{}
	for id, sink := range configured {
		copied[id] = sink
	}
	return &Manager{log: log, sinks: copied, batchSize: 100}
}

func (m *Manager) DeliverOnce(ctx context.Context, sinkID string) (Result, error) {
	sink, err := m.sink(sinkID)
	if err != nil {
		return Result{}, err
	}
	after, ok, err := m.log.Offset(ctx, cursorID(sinkID))
	if err != nil {
		return Result{}, err
	}
	result := Result{SinkID: sinkID}
	if ok {
		result.LastPosition = after
	}
	events, err := m.log.ReadAll(ctx, after, m.limit(0), eventlog.EventFilter{})
	if err != nil {
		return result, err
	}
	for _, event := range events {
		result.LastPosition = event.GlobalPosition
		if event.Visibility == eventlog.VisibilityPrivate {
			if err := m.log.Ack(ctx, cursorID(sinkID), event.GlobalPosition); err != nil {
				return result, err
			}
			continue
		}
		if err := sink.Deliver(ctx, event); err != nil {
			result.FailedEventID = event.ID
			return result, err
		}
		result.Delivered++
		if err := m.log.Ack(ctx, cursorID(sinkID), event.GlobalPosition); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (m *Manager) Replay(ctx context.Context, req ReplayRequest) (Result, error) {
	sinkID := req.SinkID
	if sinkID == "" {
		return Result{}, fmt.Errorf("sinks: sink_id is required")
	}
	sink, err := m.sink(sinkID)
	if err != nil {
		return Result{}, err
	}
	events, err := m.log.ReadAll(ctx, req.FromExclusive, m.limit(req.Limit), eventlog.EventFilter{})
	if err != nil {
		return Result{}, err
	}
	result := Result{SinkID: sinkID, LastPosition: req.FromExclusive, ReplayID: req.ReplayID}
	for _, event := range events {
		result.LastPosition = event.GlobalPosition
		if event.Visibility == eventlog.VisibilityPrivate {
			continue
		}
		if err := sink.Deliver(ctx, event); err != nil {
			result.FailedEventID = event.ID
			return result, err
		}
		result.Delivered++
	}
	return result, nil
}

func (m *Manager) Status(ctx context.Context, sinkID string) (Status, error) {
	if _, err := m.sink(sinkID); err != nil {
		return Status{}, err
	}
	position, ok, err := m.log.Offset(ctx, cursorID(sinkID))
	if err != nil {
		return Status{}, err
	}
	return Status{SinkID: sinkID, Cursor: position, HasCursor: ok}, nil
}

func (m *Manager) sink(sinkID string) (Sink, error) {
	if m == nil || m.log == nil {
		return nil, fmt.Errorf("sinks: manager is not configured")
	}
	if sinkID == "" {
		return nil, fmt.Errorf("sinks: sink_id is required")
	}
	sink, ok := m.sinks[sinkID]
	if !ok || sink == nil {
		return nil, fmt.Errorf("sinks: unknown sink %q", sinkID)
	}
	return sink, nil
}

func (m *Manager) limit(limit int) int {
	if limit > 0 {
		return limit
	}
	if m.batchSize > 0 {
		return m.batchSize
	}
	return 100
}

func cursorID(sinkID string) string {
	return "sink:" + sinkID
}

type WebhookSink struct {
	endpoint string
	client   *http.Client
}

func NewWebhookSink(endpoint string, client *http.Client) *WebhookSink {
	if client == nil {
		client = http.DefaultClient
	}
	return &WebhookSink{endpoint: endpoint, client: client}
}

func (s *WebhookSink) Deliver(ctx context.Context, event eventlog.StoredEvent) error {
	if s.endpoint == "" {
		return fmt.Errorf("sinks: webhook endpoint is required")
	}
	body, err := json.Marshal(event.CloudEvent())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("sinks: webhook returned status %d", resp.StatusCode)
	}
	return nil
}

type KafkaProducer interface {
	Produce(ctx context.Context, topic string, event eventlog.CloudEvent) error
}

type KafkaSink struct {
	topic    string
	producer KafkaProducer
}

func NewKafkaSink(topic string, producer KafkaProducer) *KafkaSink {
	return &KafkaSink{topic: topic, producer: producer}
}

func (s *KafkaSink) Deliver(ctx context.Context, event eventlog.StoredEvent) error {
	if s.topic == "" || s.producer == nil {
		return fmt.Errorf("sinks: kafka topic and producer are required")
	}
	return s.producer.Produce(ctx, s.topic, event.CloudEvent())
}

type PostgresWriter interface {
	InsertEvent(ctx context.Context, event eventlog.StoredEvent) error
}

type PostgresSink struct {
	writer PostgresWriter
}

func NewPostgresSink(writer PostgresWriter) *PostgresSink {
	return &PostgresSink{writer: writer}
}

func (s *PostgresSink) Deliver(ctx context.Context, event eventlog.StoredEvent) error {
	if s.writer == nil {
		return fmt.Errorf("sinks: postgres writer is required")
	}
	return s.writer.InsertEvent(ctx, event)
}

type Config struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Endpoint  string    `json:"endpoint,omitempty"`
	Topic     string    `json:"topic,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
