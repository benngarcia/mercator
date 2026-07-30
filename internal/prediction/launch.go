package prediction

import (
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// Launch is one launch this control plane has a record of: what it took the
// machine to be, and the moments it observed on the way. It is the whole input
// to a history, and it is deliberately made of things Mercator itself wrote
// down: the candidate comes from the Booking Decision it recorded, and the
// moments come from its own Run stream, so a history is never the world's
// account of the world.
type Launch struct {
	// Candidate is what the decision took the machine it chose to be. A launch
	// whose candidate cannot recur teaches nothing, and levelKeys files it under
	// no key at all rather than under a listing ID.
	Candidate domain.CandidateIdentity
	// StartedAt is when the workload's process began, as the machine holding it
	// reported the moment and the control plane adopted it.
	StartedAt time.Time
	// ReadyAt is when the application said it could do work. Only the workload
	// knows, which is why this is a separate authority from the moment above.
	ReadyAt time.Time
}

// Observations is what this launch measured, by stage.
//
// One stage is measurable today and the record says so about the rest. Readiness
// is bounded by two moments Mercator observes from two independent authorities:
// the machine states when the process began, and the application states when it
// began serving, and the difference between them is what the workload spent
// coming up. Every other stage of a launch happens inside a span whose ends
// Mercator does not see: a provider reports a machine running from the moment it
// accepts the launch, so acquiring it, booting it, enrolling a runtime on it,
// pulling the image, unpacking it, reading the Run's inputs, and creating the
// container are seven durations inside one observed interval, and attributing
// that interval across them would be arithmetic wearing a measurement's clothes.
// They stay predicted from published claims and stated constants, which the
// record names as the prior it is, until a node reports the stages it performs.
//
// Three of them stay predicted even then. Pulling an image, assembling it, and
// reading the Run's inputs are a byte count over a throughput, the byte count is
// this launch's own rather than the candidate's, and pricedFromBytes is where that
// is stated: a timed fetch becomes evidence when the key names the bytes and the
// path, and the rate it crossed is already published as a fact of its own.
func (launch Launch) Observations() []Observation {
	if launch.StartedAt.IsZero() || launch.ReadyAt.IsZero() || launch.ReadyAt.Before(launch.StartedAt) {
		return nil
	}
	return []Observation{{
		Candidate:  launch.Candidate,
		Stage:      domain.StageApplicationReady,
		Seconds:    launch.ReadyAt.Sub(launch.StartedAt).Seconds(),
		ObservedAt: launch.ReadyAt,
	}}
}

// Observations is every launch's measurements, in the order the launches are
// given. A caller with no launches produces no observations, and NewHistory over
// none of them answers the prior for everything.
func Observations(launches []Launch) []Observation {
	var observations []Observation
	for _, launch := range launches {
		observations = append(observations, launch.Observations()...)
	}
	return observations
}
