package domain

import "testing"

// refusedForItsStart is one machine struck out on the same bound for the same
// reason every time, holding whatever the caller says it holds. The refusal is
// the constant: what varies between the fixtures below is the evidence beside
// it, which is what the laws about Placement read and what the verdict had to
// start carrying.
func refusedForItsStart(locality LocalityState, pullConfidence float64) BookingDecision {
	return BookingDecision{
		Candidates: []CandidateDecision{{
			OfferSnapshotID: "off_only_machine",
			ImageLocality:   locality,
			Rejections: []Violation{{
				Code:    "LATENCY_SLO_EXCEEDED",
				Path:    "placement.max_p90_start_seconds",
				Message: "Offer is known to start later than the requested p90 start latency.",
			}},
			Confidences: []Confidence{
				{Answer: AnswerCapacity, Value: 1},
				{Answer: StageImageFetch.ConfidenceAnswer(), Value: pullConfidence},
			},
		}},
	}
}

// TestAFleetSayingTheSameThingTwiceHasOneVerdict is the suppression the verdict
// exists for. A Run waiting an hour on a fleet that keeps answering the same way
// must not write sixty identical decisions, and nothing here moved.
func TestAFleetSayingTheSameThingTwiceHasOneVerdict(t *testing.T) {
	first := refusedForItsStart(LocalityHot, 1)
	second := refusedForItsStart(LocalityHot, 1)

	if first.FleetVerdict() != second.FleetVerdict() {
		t.Fatalf("one fleet answered the same question twice and the verdicts differ:\n%s\n%s",
			first.FleetVerdict(), second.FleetVerdict())
	}
}

// TestALocalityThatWentSilentIsADifferentVerdict is the audit hole the refusals
// alone left open. Image locality is priced and never refused, by design, so a
// machine whose `docker image inspect` starts failing produces the same refusal
// on the same path against the same bound while the evidence under it changed
// completely. Compared on refusals alone the decision was suppressed, the
// deferral it explained was suppressed with it, and
// safety.locality_is_never_infeasibility reads recorded decisions: the violation
// was never written down and so could never be caught.
func TestALocalityThatWentSilentIsADifferentVerdict(t *testing.T) {
	known := refusedForItsStart(LocalityHot, 1)
	silent := refusedForItsStart(LocalityUnknown, 0.5)

	if known.FleetVerdict() == silent.FleetVerdict() {
		t.Fatalf("a machine that stopped saying what it holds gave the same verdict as one that said: %s",
			known.FleetVerdict())
	}
}

// TestAFleetVerdictIsOrderedByMachineAndNotByArrival keeps the verdict a
// statement about a set. Two searches that return one fleet in two orders said
// the same thing, and a verdict that changed with the order would record a
// decision on every tick that shuffled.
func TestAFleetVerdictIsOrderedByMachineAndNotByArrival(t *testing.T) {
	warm := refusedForItsStart(LocalityHot, 1).Candidates[0]
	cold := refusedForItsStart(LocalityCold, 1).Candidates[0]
	cold.OfferSnapshotID = "off_second_machine"

	forwards := BookingDecision{Candidates: []CandidateDecision{warm, cold}}
	backwards := BookingDecision{Candidates: []CandidateDecision{cold, warm}}

	if forwards.FleetVerdict() != backwards.FleetVerdict() {
		t.Fatalf("one fleet in two orders gave two verdicts:\n%s\n%s",
			forwards.FleetVerdict(), backwards.FleetVerdict())
	}
}
