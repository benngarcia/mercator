package domain

import (
	"testing"
	"time"
)

// economicsStart is the moment every case in this file is decided at.
var economicsStart = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

// TestAPublisherIsPaidForTheBlocksItSells is the increment every adapter in the
// tree has always published and nothing read until the economics needed it. A
// machine sold by the minute charges a whole minute for a second of use, and a
// machine whose publisher names no increment bills continuously.
func TestAPublisherIsPaidForTheBlocksItSells(t *testing.T) {
	for _, test := range []struct {
		name        string
		granularity int64
		seconds     float64
		want        float64
	}{
		{"a second of a machine sold by the minute", 60, 1, 60},
		{"twenty minutes of a machine sold by the hour", 3600, 1200, 3600},
		{"an exact hour of a machine sold by the hour", 3600, 3600, 3600},
		{"an hour and a second of a machine sold by the hour", 3600, 3601, 7200},
		{"nothing at all", 3600, 0, 0},
		{"a machine whose publisher names no increment", 0, 1201.5, 1201.5},
	} {
		t.Run(test.name, func(t *testing.T) {
			price := PriceModel{Currency: "USD", GranularitySeconds: test.granularity, Known: true}

			if billed := price.BilledSeconds(test.seconds); billed != test.want {
				t.Fatalf("%v seconds of a machine sold in blocks of %ds is billed as %v, want %v",
					test.seconds, test.granularity, billed, test.want)
			}
		})
	}
}

// TestOneCommittedHourIsSpentByWhoeverOccupiesIt is the rule that keeps a
// commitment from being sold twice. The seconds a Run spends of an interval
// Mercator already owes for are counted from the Run's own start, so two Runs on
// one machine spend different stretches of one hour.
func TestOneCommittedHourIsSpentByWhoeverOccupiesIt(t *testing.T) {
	terms := CapacityTerms{CommittedUntil: economicsStart.Add(time.Hour)}
	first := Occupancy{At: economicsStart, StartSeconds: 0, RuntimeSeconds: 1800}
	second := Occupancy{At: economicsStart, StartSeconds: 1800, RuntimeSeconds: 1800}

	spent := terms.CommittedSeconds(first) + terms.CommittedSeconds(second)

	if spent != 3600 {
		t.Fatalf("two half-hour Runs one after the other spend %v seconds of one committed hour", spent)
	}
}

// TestARunThatOverrunsTheCommitmentSpendsOnlyWhatIsLeftOfIt is the other half:
// seconds past the end of the interval are not committed, so they are not
// discounted as though they were.
func TestARunThatOverrunsTheCommitmentSpendsOnlyWhatIsLeftOfIt(t *testing.T) {
	terms := CapacityTerms{CommittedUntil: economicsStart.Add(10 * time.Minute)}
	held := Occupancy{At: economicsStart, RuntimeSeconds: 1800}

	if committed := terms.CommittedSeconds(held); committed != 600 {
		t.Fatalf("a half-hour Run inside a ten-minute commitment spends %v committed seconds", committed)
	}
}

// TestARunThatStartsAfterTheCommitmentSpendsNoneOfIt is the case a start
// prediction produces on its own: a Run that will not have the machine until
// after the interval ends spends none of it, and nothing about the interval makes
// it cheaper.
func TestARunThatStartsAfterTheCommitmentSpendsNoneOfIt(t *testing.T) {
	terms := CapacityTerms{CommittedUntil: economicsStart.Add(5 * time.Minute)}
	held := Occupancy{At: economicsStart, StartSeconds: 600, RuntimeSeconds: 1800}

	if committed := terms.CommittedSeconds(held); committed != 0 {
		t.Fatalf("a Run that gets the machine five minutes after its commitment ended spends %v committed seconds", committed)
	}
}

// TestAWindowIsJudgedAgainstTheRuntimeMercatorEnforces is why the window reads the
// bound rather than the expectation. A caller's guess is not what Mercator would
// allow, so admitting on the guess puts work on a machine that goes away
// underneath it whenever the guess is short.
func TestAWindowIsJudgedAgainstTheRuntimeMercatorEnforces(t *testing.T) {
	terms := CapacityTerms{AvailableUntil: economicsStart.Add(30 * time.Minute)}
	held := Occupancy{At: economicsStart, RuntimeSeconds: 600, MaxRuntimeSeconds: 3600}

	if !terms.OutlivesWindow(held) {
		t.Fatal("a Run Mercator would let hold the machine for an hour fits inside a window closing in half of one")
	}
	if inside := (Occupancy{At: economicsStart, RuntimeSeconds: 600, MaxRuntimeSeconds: 1200}); terms.OutlivesWindow(inside) {
		t.Fatal("a Run Mercator would stop after twenty minutes outlives a window closing in thirty")
	}
}

// TestCapacityHeldForNobodyInParticularAdmitsEveryClass is the default a
// reservation is stated against. Every machine in the fleet is held for nobody
// until an operator says otherwise, and a machine that is held refuses the rest.
func TestCapacityHeldForNobodyInParticularAdmitsEveryClass(t *testing.T) {
	unreserved := CapacityTerms{}
	reserved := CapacityTerms{EligibleClasses: []ServiceClass{ClassInteractive, ClassStandard}}

	for _, class := range KnownServiceClasses {
		if !unreserved.Admits(class) {
			t.Fatalf("capacity reserved for nobody refuses %q", class)
		}
	}
	if !reserved.Admits(ClassStandard) || reserved.Admits(ClassBatch) {
		t.Fatalf("a machine held for %v admits batch work and refuses standard work", reserved.EligibleClasses)
	}
}
