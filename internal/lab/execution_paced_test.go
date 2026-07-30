package lab

import (
	"context"
	"errors"
	"testing"
)

// A paced drive exists so a human can watch a Blueprint execute. It is only worth
// having if it produces the same execution the headless drive produces, because a
// slower kind of run that nobody could replay would be a second implementation of
// what completion means, wearing the first one's name.
func TestPacingChangesNothingTheExecutionRecorded(t *testing.T) {
	unpaced := driveDemoToCompletion(t, nil)

	rounds := 0
	paced := driveDemoToCompletion(t, func(context.Context, Checkpoint) error {
		rounds++
		return nil
	})

	if rounds == 0 {
		t.Fatal("the wait was never called, so this compared two unpaced drives and would pass with pacing deleted")
	}
	if paced != unpaced {
		t.Fatalf("paced drive exported %s, unpaced exported %s: pacing changed the recorded execution", paced, unpaced)
	}
}

// The wait is where a viewer going away shows up, so its error ends the drive
// rather than being swallowed into a world that keeps advancing for nobody.
func TestAWaitThatFailsEndsTheDrive(t *testing.T) {
	execution := openDemoExecution(t)
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	stop := errors.New("the viewer went away")
	_, err := execution.DriveToCompletionPaced(context.Background(), func(context.Context, Checkpoint) error {
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("drive returned %v, want the wait's own error", err)
	}
}

// driveDemoToCompletion drives the demonstration Blueprint and returns the
// exported bundle's normalized hash, which is what two executions being the same
// execution means here.
func driveDemoToCompletion(t *testing.T, wait func(context.Context, Checkpoint) error) string {
	t.Helper()
	execution := openDemoExecution(t)
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()
	if _, err := execution.DriveToCompletionPaced(context.Background(), wait); err != nil {
		t.Fatalf("drive: %v", err)
	}
	bundle, err := execution.Export(context.Background())
	if err != nil {
		t.Fatalf("export bundle: %v", err)
	}
	return bundle.NormalizedSHA256()
}
