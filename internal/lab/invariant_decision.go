package lab

import (
	"encoding/json"
	"fmt"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

// This file is the law on the decision record itself, as opposed to the laws on
// what a decision chose. Every other rule about Placement reads Booking
// Decisions, which makes the record they read the one thing none of them can
// check: a decision quietly replaced in place, or a Run answered twice with no
// statement of which answer stood in for which, breaks every reader downstream
// while each individual decision still looks perfectly well formed.

// decisionRecord is one Booking Decision as Mercator appended it, carrying the
// event that held it so a violation can name the record a reader has to go and
// look at.
type decisionRecord struct {
	eventID  string
	decision domain.BookingDecision
}

// recordedDecisionRecords is every Booking Decision Mercator recorded, in the
// order it appended them, each still carrying the event that held it. It is the
// one place the public log is read for decisions.
func recordedDecisionRecords(observation InvariantObservation) ([]decisionRecord, error) {
	var records []decisionRecord
	for _, event := range observation.MercatorEvents {
		if event.Type != orchestrator.EventBookingDecided {
			continue
		}
		var payload struct {
			Decision domain.BookingDecision `json:"decision"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return nil, fmt.Errorf("decode Booking Decision from %s: %w", event.ID, err)
		}
		records = append(records, decisionRecord{eventID: event.ID, decision: payload.Decision})
	}
	return records, nil
}

// recordedDecisionsByRun groups those records by the Run they are about, keeping
// each Run's order. Both rules here are about one Run's sequence of answers, and
// the log interleaves Runs.
func recordedDecisionsByRun(observation InvariantObservation) (map[string][]decisionRecord, error) {
	records, err := recordedDecisionRecords(observation)
	if err != nil {
		return nil, err
	}
	chains := map[string][]decisionRecord{}
	for _, record := range records {
		runID := record.decision.RunID
		chains[runID] = append(chains[runID], record)
	}
	return chains, nil
}

// decisionsAreNeverRewritten is the standing guard on an audited decision being
// added and never edited. Two things hold for every Run.
//
// One decision ID means one decision. Two records under one ID that disagree are
// a decision that was changed after the fact, and every account built on it,
// every prediction filed against it and every audit of why a Run went where it
// did, is then an account of something that never happened.
//
// And an answer that changed names the answer it stands in for, and why. The
// predecessor is checked as the record immediately before it rather than as any
// earlier record, because a chain that skips a link is one a reader cannot walk:
// they are back to taking the last entry and assuming nothing came before it,
// which is the state this rule exists to make impossible. The reason is required
// beside the name for the same reason the name is: a supersession nobody
// explained does not say whether the fleet changed or a machine refused the work,
// and those are the two answers an operator reads a chain to tell apart.
func decisionsAreNeverRewritten(observation InvariantObservation) error {
	chains, err := recordedDecisionsByRun(observation)
	if err != nil {
		return err
	}
	for runID, chain := range chains {
		if err := oneIDMeansOneDecision(runID, chain); err != nil {
			return err
		}
		if err := everyChangedAnswerNamesItsPredecessor(runID, chain); err != nil {
			return err
		}
	}
	return nil
}

func oneIDMeansOneDecision(runID string, chain []decisionRecord) error {
	recorded := map[string]decisionRecord{}
	for _, record := range chain {
		held, seen := recorded[record.decision.ID]
		if !seen {
			recorded[record.decision.ID] = record
			continue
		}
		same, err := sameDecisionContent(held.decision, record.decision)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf(
				"Run %q: decision %q was recorded by %s and again by %s saying something else, and an audited decision is added rather than edited",
				runID, record.decision.ID, held.eventID, record.eventID,
			)
		}
	}
	return nil
}

func everyChangedAnswerNamesItsPredecessor(runID string, chain []decisionRecord) error {
	for index, record := range chain {
		decision := record.decision
		if index == 0 {
			if decision.Supersedes != "" {
				return fmt.Errorf(
					"Run %q: its first decision %q supersedes %q, and there is no earlier decision on this Run for it to be replacing",
					runID, decision.ID, decision.Supersedes,
				)
			}
			continue
		}
		predecessor := chain[index-1].decision.ID
		if decision.Supersedes != predecessor {
			return fmt.Errorf(
				"Run %q: decision %q supersedes %q, and the decision recorded before it was %q",
				runID, decision.ID, decision.Supersedes, predecessor,
			)
		}
		if decision.SupersedesReason == "" {
			return fmt.Errorf(
				"Run %q: decision %q replaces %q and gives no reason, so the record cannot say what changed",
				runID, decision.ID, predecessor,
			)
		}
	}
	return nil
}

// sameDecisionContent compares two records of one decision by their canonical
// serialisation, which is the whole decision rather than the parts this rule
// happens to name. A rule that compared chosen fields would certify a decision
// whose candidate set, weights or evidence had been edited underneath a stable
// ID, and the candidates are exactly what the other laws read.
func sameDecisionContent(held, next domain.BookingDecision) (bool, error) {
	heldHash, err := domain.CanonicalHash(held)
	if err != nil {
		return false, err
	}
	nextHash, err := domain.CanonicalHash(next)
	if err != nil {
		return false, err
	}
	return heldHash == nextHash, nil
}

// decisionIsReproducible is the other half of never rewriting one: a decision ID
// is derived from the decision's own recorded content, so re-deriving it from the
// record has to produce the ID the record carries.
//
// It is what makes the rule above enforceable at all. Without it, an edit that
// changed a decision and its ID together is two decisions to any reader, and a
// chain of consistent-looking records can be assembled after the fact from
// content nothing ever decided. With it, the ID is a claim about the content that
// the content itself answers, and the two rules together say that a decision
// Mercator recorded is the decision Mercator took.
//
// It reads the whole record and not only the newest decision. A superseded
// decision is the part of the chain nobody is looking at any more, which is
// exactly where an edit would go.
func decisionIsReproducible(observation InvariantObservation) error {
	records, err := recordedDecisionRecords(observation)
	if err != nil {
		return err
	}
	for _, record := range records {
		identity, err := record.decision.Identity()
		if err != nil {
			return err
		}
		if identity != record.decision.ID {
			return fmt.Errorf(
				"Run %q: %s carries decision %q, and re-deriving that decision's own inputs yields %q",
				record.decision.RunID, record.eventID, record.decision.ID, identity,
			)
		}
	}
	return nil
}
