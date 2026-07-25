package lab

import (
	"context"
	"testing"
)

// TestAMachineWithNoRoomRefusesTheWork is the world being a machine about disk.
// Content has to fit somewhere: a host asked to fetch fifty gigabytes onto
// twenty does not run the workload slowly, it fills up and fails with nothing to
// show, so this world refuses the launch instead of creating content its own
// ledger could not hold. Without the refusal the Rental ends up holding more
// than it has, which is the state safety.disk_reservation_respected exists to
// catch and which no Blueprint could otherwise reach.
func TestAMachineWithNoRoomRefusesTheWork(t *testing.T) {
	execution := openBlueprintExecution(t, "testdata/blueprints/a-machine-with-no-room-refuses-the-work.json", DefaultLimits())
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive the Blueprint: %v", err)
	}

	ledger := diskLedgerFor(t, execution, "cramped-rental")
	if len(ledger.Resident) > 0 {
		t.Fatalf("the machine that refused the work is holding %+v", ledger.Resident)
	}
	if ledger.ReservedBytes != 0 {
		t.Fatalf("the machine that refused the work reserved %d bytes for it", ledger.ReservedBytes)
	}
	if free := ledger.FreeBytes(); free != ledger.CapacityBytes {
		t.Fatalf("the machine offers %d of its %d bytes, and it took none of the work", free, ledger.CapacityBytes)
	}
}

func diskLedgerFor(t *testing.T, execution *Execution, offerID string) DiskLedger {
	t.Helper()
	for _, ledger := range execution.runtime.world.truthSnapshot().Disk {
		if ledger.OfferID == offerID {
			return ledger
		}
	}
	t.Fatalf("no machine %q in this world's disk ledgers", offerID)
	return DiskLedger{}
}
