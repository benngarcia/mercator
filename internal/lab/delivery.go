package lab

import (
	"encoding/json"
	"slices"
	"sort"
	"time"
)

type deliveryMode uint8

const (
	deliveryInOrder deliveryMode = iota
	deliveryLost
	deliveryDelayed
	deliveryDuplicated
	deliveryReordered
)

type deliveryRule struct {
	mode  deliveryMode
	delay time.Duration
}

type deliveryMessage struct {
	ID            string          `json:"id"`
	Channel       string          `json:"channel,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	CausationID   string          `json:"causation_id,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Delivery      int             `json:"delivery"`
}

type pendingDelivery struct {
	message     deliveryMessage
	availableAt time.Time
	order       int64
}

type deliveryNetwork struct {
	sequence int64
	pending  []pendingDelivery
}

func (network *deliveryNetwork) send(at time.Time, message deliveryMessage, rule deliveryRule) {
	network.sequence++
	switch rule.mode {
	case deliveryLost:
		return
	case deliveryDelayed:
		network.enqueue(at.Add(rule.delay), network.sequence, message, 1)
	case deliveryDuplicated:
		network.enqueue(at, network.sequence*2, message, 1)
		network.enqueue(at, network.sequence*2+1, message, 2)
	case deliveryReordered:
		network.enqueue(at, -network.sequence, message, 1)
	default:
		network.enqueue(at, network.sequence, message, 1)
	}
}

func (network *deliveryNetwork) enqueue(at time.Time, order int64, message deliveryMessage, delivery int) {
	message.Payload = slices.Clone(message.Payload)
	message.Delivery = delivery
	network.pending = append(network.pending, pendingDelivery{
		message:     message,
		availableAt: at,
		order:       order,
	})
}

func (network *deliveryNetwork) deliver(now time.Time) []deliveryMessage {
	var ready []pendingDelivery
	waiting := network.pending[:0]
	for _, pending := range network.pending {
		if pending.availableAt.After(now) {
			waiting = append(waiting, pending)
			continue
		}
		ready = append(ready, pending)
	}
	network.pending = waiting
	sort.SliceStable(ready, func(i, j int) bool {
		if ready[i].availableAt.Equal(ready[j].availableAt) {
			return ready[i].order < ready[j].order
		}
		return ready[i].availableAt.Before(ready[j].availableAt)
	})
	delivered := make([]deliveryMessage, len(ready))
	for index, pending := range ready {
		delivered[index] = pending.message
		delivered[index].Payload = slices.Clone(pending.message.Payload)
	}
	return delivered
}
