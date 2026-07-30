package lab

import (
	"encoding/json"
	"fmt"

	"github.com/benngarcia/mercator/internal/domain"
)

// hostSupportsTheImageItWasGiven is the standing rule that compatibility is
// decided before a machine is paid for. The host provides the driver and the
// image provides the accelerator stack that talks to it, so an image built
// against a driver newer than the machine runs cannot start there, and nothing
// in Mercator may answer that by installing a stack onto somebody's host.
//
// It is asked in two places because the two catch different failures.
//
// The launches are the act. A launch accepted on a machine whose published
// facts cannot carry this workload's declared stack is the failure this rule
// exists for, whatever the record says about it, and it is read from the world's
// own ledger rather than from Mercator's belief about where the work went.
//
// The Booking Decisions are the judgment. A candidate the record called feasible
// on a machine that cannot support the image is one placement away from that
// launch: it would be selected the moment the machine the Run actually took
// became busy, and a rule that watched only launches would call the fleet
// correct until the day it was unlucky.
//
// A machine that published nothing fails both clauses, and it fails them as a
// silence. domain.HostFacts answers an unstated fact with UNKNOWN_FACT rather
// than with a refusal, which is the distinction the whole type exists to keep:
// a host with no driver is one to stop buying, and a host nobody established a
// driver for is one to go and ask.
//
// Three sources meet here and none of them is checking itself. What the machine
// said is the World Tape's; what the workload declared is Mercator's own public
// event log; and whether a launch happened is the world's ledger.
func hostSupportsTheImageItWasGiven(observation InvariantObservation) error {
	if err := everyLaunchLandedOnAHostThatSupportsIt(observation); err != nil {
		return err
	}
	return noUnsupportableHostWasCalledFeasible(observation)
}

func everyLaunchLandedOnAHostThatSupportsIt(observation InvariantObservation) error {
	for _, effect := range observation.Effects {
		if effect.Operation != OperationProviderLaunch || effect.Command != EffectCommandAccepted {
			continue
		}
		var request struct {
			RunID   string `json:"run_id"`
			OfferID string `json:"offer_id"`
		}
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			return fmt.Errorf("decode the launch at effect %d: %w", effect.Sequence, err)
		}
		required := observation.Workloads[request.RunID].Spec.Resources.Host
		if !required.Stated() {
			continue
		}
		facts := observation.World.PublishedHostFacts[request.OfferID]
		if violations := facts.Violations(required); len(violations) > 0 {
			return fmt.Errorf(
				"Run %q was launched on machine %q at effect %d, and that machine %s",
				request.RunID, request.OfferID, effect.Sequence, describeHostRefusals(violations),
			)
		}
	}
	return nil
}

func noUnsupportableHostWasCalledFeasible(observation InvariantObservation) error {
	decisions, err := recordedDecisions(observation)
	if err != nil {
		return err
	}
	for _, decision := range decisions {
		required := observation.Workloads[decision.RunID].Spec.Resources.Host
		if !required.Stated() {
			continue
		}
		for _, candidate := range decision.Candidates {
			if !candidate.Feasible {
				continue
			}
			facts := observation.World.PublishedHostFacts[candidate.OfferSnapshotID]
			if violations := facts.Violations(required); len(violations) > 0 {
				return fmt.Errorf(
					"the decision for Run %q weighed machine %q as feasible, and that machine %s",
					decision.RunID, candidate.OfferSnapshotID, describeHostRefusals(violations),
				)
			}
		}
	}
	return nil
}

// describeHostRefusals says what the machine's own facts answered, in the words
// the Booking Decision would have used, so a violation names the fact rather
// than the rule.
func describeHostRefusals(violations []domain.Violation) string {
	described := ""
	for index, violation := range violations {
		if index > 0 {
			described += "; "
		}
		described += fmt.Sprintf("%s %s (%v offered against %v)", violation.Code, violation.Path, violation.Offered, violation.Required)
	}
	return described
}
