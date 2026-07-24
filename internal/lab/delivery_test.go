package lab

import (
	"testing"
	"time"
)

func TestDeliveryNetworkModelsLossDelayDuplicationAndReordering(t *testing.T) {
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("lost", func(t *testing.T) {
		network := deliveryNetwork{}
		network.send(start, deliveryMessage{ID: "callback-a"}, deliveryRule{mode: deliveryLost})

		if delivered := network.deliver(start.Add(time.Hour)); len(delivered) != 0 {
			t.Fatalf("lost delivery produced %+v", delivered)
		}
	})

	t.Run("delayed", func(t *testing.T) {
		network := deliveryNetwork{}
		network.send(start, deliveryMessage{ID: "callback-a"}, deliveryRule{
			mode:  deliveryDelayed,
			delay: 5 * time.Minute,
		})

		if delivered := network.deliver(start.Add(4 * time.Minute)); len(delivered) != 0 {
			t.Fatalf("delayed delivery arrived early: %+v", delivered)
		}
		delivered := network.deliver(start.Add(5 * time.Minute))
		if len(delivered) != 1 || delivered[0].ID != "callback-a" {
			t.Fatalf("delayed delivery = %+v", delivered)
		}
	})

	t.Run("duplicated", func(t *testing.T) {
		network := deliveryNetwork{}
		network.send(start, deliveryMessage{ID: "callback-a"}, deliveryRule{mode: deliveryDuplicated})

		delivered := network.deliver(start)
		if len(delivered) != 2 ||
			delivered[0].Delivery != 1 ||
			delivered[1].Delivery != 2 {
			t.Fatalf("duplicate deliveries = %+v", delivered)
		}
	})

	t.Run("reordered", func(t *testing.T) {
		network := deliveryNetwork{}
		network.send(start, deliveryMessage{ID: "callback-a"}, deliveryRule{mode: deliveryReordered})
		network.send(start, deliveryMessage{ID: "callback-b"}, deliveryRule{mode: deliveryReordered})

		delivered := network.deliver(start)
		if len(delivered) != 2 ||
			delivered[0].ID != "callback-b" ||
			delivered[1].ID != "callback-a" {
			t.Fatalf("reordered deliveries = %+v", delivered)
		}
	})
}
