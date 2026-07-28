package domain

import (
	"fmt"
	"slices"
	"time"
)

// Rental is the lease Mercator holds on one machine, and it is Mercator's own
// record rather than a provider's. The identity is minted before any provider is
// asked for anything, so capacity allocated by a request whose answer was lost is
// still reconcilable: the lease says what was asked for and what it belongs to,
// and a sweep that finds an unattributed machine has something to match it
// against.
//
// A lease is not a machine. Capacity that stops and resumes comes back as a
// different machine under the same lease, running a different runtime, and that
// is what a generation is. It is why a Node is bound to a generation rather than
// to the Rental: a command stamped with a generation that has ended is refused
// instead of applied to whatever is on the machine now.
//
// Generations are kept in order and never rewritten, so what a lease has been
// through is read off the record rather than inferred from what it is now.
type Rental struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	// ConnectionID is the provider connection this capacity was allocated
	// through. Every later command about the machine goes back down it, so a
	// lease that lost it is a lease nothing can observe, stop, or terminate.
	ConnectionID string `json:"connection_id"`
	// OwnershipToken proves the machine belongs to this workspace, so a sweep
	// never acts on capacity it merely resembles.
	OwnershipToken string `json:"ownership_token"`
	// Version counts the transitions this lease has been through. A store writes
	// only what follows the version it holds, which is what stops two controllers
	// from ending one generation twice or from resuming a lease another already
	// released.
	Version     uint64             `json:"version"`
	OpenedAt    time.Time          `json:"opened_at"`
	ReleasedAt  time.Time          `json:"released_at,omitzero"`
	Generations []RentalGeneration `json:"generations"`
}

// RentalGeneration is one lifecycle cycle of the machine a Rental leases: the
// runtime invited onto it, the machine the provider allocated for it, and the
// span it was Mercator's. A generation that has ended says how it ended, because
// a machine suspended under a lease that still stands and a machine destroyed are
// different facts about the same record.
type RentalGeneration struct {
	Number uint64 `json:"number"`
	// NodeID is the runtime invited for this generation. It is stated when the
	// generation opens rather than when an agent arrives, because the invitation
	// is what the bootstrap carries and the machine has to be reconcilable from
	// the moment Mercator asks for it.
	NodeID string `json:"node_id"`
	// NativeRef is the provider's own name for the machine. It is empty until the
	// provider answers, which is the difference between capacity Mercator asked
	// for and capacity it got.
	NativeRef string                 `json:"native_ref,omitempty"`
	BeganAt   time.Time              `json:"began_at"`
	EndedAt   time.Time              `json:"ended_at,omitzero"`
	Ending    RentalGenerationEnding `json:"ending,omitempty"`
}

// Open reports whether this generation is the one a command may still be sent
// to.
func (generation RentalGeneration) Open() bool { return generation.EndedAt.IsZero() }

// RentalGenerationEnding is why a generation stopped being the current one. The
// three are kept apart because they license different things: only one of them
// leaves a lease that can be resumed, and only one of them is capacity Mercator
// chose to give up.
type RentalGenerationEnding string

const (
	// RentalStopped is capacity suspended with its identity and, where the
	// provider keeps disks across a stop, its content. The lease stands and a
	// later generation resumes it.
	RentalStopped RentalGenerationEnding = "stopped"
	// RentalTerminated is capacity Mercator destroyed. Nothing survives it and
	// the lease is over.
	RentalTerminated RentalGenerationEnding = "terminated"
	// RentalReclaimed is capacity the provider took back, which is what happens
	// to interruptible capacity. Mercator did not choose it, and it ends the
	// lease exactly as a termination does.
	RentalReclaimed RentalGenerationEnding = "reclaimed"
)

func (ending RentalGenerationEnding) Valid() bool {
	switch ending {
	case RentalStopped, RentalTerminated, RentalReclaimed:
		return true
	default:
		return false
	}
}

// EndsTheLease reports whether the machine is gone for good. A stop is the one
// ending that leaves something to come back to.
func (ending RentalGenerationEnding) EndsTheLease() bool { return ending != RentalStopped }

// RentalIdentity is everything about a lease that never changes, stated together
// because it is one answer: which lease, whose, through which connection, and
// with what proof that the machine behind it is this workspace's.
type RentalIdentity struct {
	RentalID       string
	WorkspaceID    string
	ConnectionID   string
	OwnershipToken string
}

func (identity RentalIdentity) Validate() error {
	switch {
	case identity.RentalID == "":
		return fmt.Errorf("a Rental needs an identity Mercator minted before it asked a provider for anything")
	case identity.WorkspaceID == "":
		return fmt.Errorf("Rental %q belongs to no Workspace", identity.RentalID)
	case identity.ConnectionID == "":
		return fmt.Errorf("Rental %q names no connection, so nothing can observe or terminate the machine behind it", identity.RentalID)
	case identity.OwnershipToken == "":
		return fmt.Errorf("Rental %q carries no ownership proof, so a sweep could not tell its machine from one that resembles it", identity.RentalID)
	default:
		return nil
	}
}

// OpenRental takes a lease and opens its first generation. The runtime is named
// here because Mercator invites the node before it asks the provider for a
// machine: the invitation is what the bootstrap carries, so a lease whose first
// generation had no node would be a machine with no way to reach the control
// plane.
func OpenRental(identity RentalIdentity, nodeID string, at time.Time) (Rental, error) {
	if err := identity.Validate(); err != nil {
		return Rental{}, err
	}
	if nodeID == "" {
		return Rental{}, fmt.Errorf("Rental %q opens a generation with no runtime invited onto it", identity.RentalID)
	}
	if at.IsZero() {
		return Rental{}, fmt.Errorf("Rental %q opens at no moment", identity.RentalID)
	}
	return Rental{
		ID:             identity.RentalID,
		WorkspaceID:    identity.WorkspaceID,
		ConnectionID:   identity.ConnectionID,
		OwnershipToken: identity.OwnershipToken,
		Version:        1,
		OpenedAt:       at,
		Generations:    []RentalGeneration{{Number: 1, NodeID: nodeID, BeganAt: at}},
	}, nil
}

// Held reports whether this lease is still capacity Mercator has. A released
// lease is history: it names what was rented and what became of it, and nothing
// may be placed on it.
func (rental Rental) Held() bool { return rental.ReleasedAt.IsZero() }

// Current is the generation a command may still be sent to, and whether there is
// one. A lease between generations has none: the machine was stopped and nothing
// has resumed it yet.
func (rental Rental) Current() (RentalGeneration, bool) {
	if len(rental.Generations) == 0 {
		return RentalGeneration{}, false
	}
	latest := rental.Generations[len(rental.Generations)-1]
	return latest, latest.Open()
}

// Acquire records the provider's own name for the machine this generation runs
// on. It is a separate act from opening the lease because Mercator mints the
// identity before it asks: until the provider answers, the lease says what was
// asked for and cannot say what was got.
//
// Answering twice with the same machine changes nothing, which is what makes a
// retried provision safe. Answering with a second machine is refused, because a
// generation names one machine and a lease that quietly took the second would
// leave the first one billing with nothing to reclaim it by.
func (rental Rental) Acquire(nativeRef string) (Rental, error) {
	if nativeRef == "" {
		return Rental{}, fmt.Errorf("Rental %q acquired a machine the provider did not name", rental.ID)
	}
	current, open := rental.Current()
	if !open {
		return Rental{}, fmt.Errorf("Rental %q has no open generation to acquire a machine for", rental.ID)
	}
	if current.NativeRef == nativeRef {
		return rental, nil
	}
	if current.NativeRef != "" {
		return Rental{}, fmt.Errorf(
			"Rental %q generation %d already holds machine %q and cannot also hold %q",
			rental.ID, current.Number, current.NativeRef, nativeRef,
		)
	}
	next := rental.Clone()
	next.Generations[len(next.Generations)-1].NativeRef = nativeRef
	next.Version++
	return next, nil
}

// EndGeneration closes the generation a command could still be sent to and
// returns it, so the caller knows which runtime this ending retires. An ending
// that leaves nothing to come back to releases the lease in the same act: a
// destroyed machine is not capacity waiting to be resumed.
func (rental Rental) EndGeneration(ending RentalGenerationEnding, at time.Time) (Rental, RentalGeneration, error) {
	if !ending.Valid() {
		return Rental{}, RentalGeneration{}, fmt.Errorf("%q is not a way a Rental generation ends", ending)
	}
	if at.IsZero() {
		return Rental{}, RentalGeneration{}, fmt.Errorf("Rental %q ends a generation at no moment", rental.ID)
	}
	current, open := rental.Current()
	if !open {
		return Rental{}, RentalGeneration{}, fmt.Errorf("Rental %q has no open generation to end", rental.ID)
	}
	if at.Before(current.BeganAt) {
		return Rental{}, RentalGeneration{}, fmt.Errorf(
			"Rental %q generation %d began at %s and cannot end at %s",
			rental.ID, current.Number, current.BeganAt.Format(time.RFC3339), at.Format(time.RFC3339),
		)
	}
	next := rental.Clone()
	ended := &next.Generations[len(next.Generations)-1]
	ended.EndedAt = at
	ended.Ending = ending
	if ending.EndsTheLease() {
		next.ReleasedAt = at
	}
	next.Version++
	return next, *ended, nil
}

// BeginGeneration resumes a stopped lease onto a fresh generation with a fresh
// runtime. The runtime is fresh because the previous one was retired when its
// generation ended: node identity carries the generation it was invited for, so
// a resumed machine that reused it could not be told from the machine before the
// stop.
func (rental Rental) BeginGeneration(nodeID string, at time.Time) (Rental, error) {
	if nodeID == "" {
		return Rental{}, fmt.Errorf("Rental %q opens a generation with no runtime invited onto it", rental.ID)
	}
	if !rental.Held() {
		return Rental{}, fmt.Errorf("Rental %q was released at %s and has nothing left to resume", rental.ID, rental.ReleasedAt.Format(time.RFC3339))
	}
	current, open := rental.Current()
	if open {
		return Rental{}, fmt.Errorf("Rental %q generation %d is still open, so nothing follows it yet", rental.ID, current.Number)
	}
	if at.Before(current.EndedAt) {
		return Rental{}, fmt.Errorf(
			"Rental %q generation %d ended at %s and cannot be followed at %s",
			rental.ID, current.Number, current.EndedAt.Format(time.RFC3339), at.Format(time.RFC3339),
		)
	}
	next := rental.Clone()
	next.Generations = append(next.Generations, RentalGeneration{Number: current.Number + 1, NodeID: nodeID, BeganAt: at})
	next.Version++
	return next, nil
}

// Validate refuses a lease Mercator could not have reached. It is what a store
// asks before it writes, in the same discipline a Rental Schedule seed is held
// to: every generation took a transition to get here, only the newest one can
// still be open, and a lease is released exactly when the ending of its last
// generation left nothing to resume.
func (rental Rental) Validate() error {
	identity := RentalIdentity{
		RentalID:       rental.ID,
		WorkspaceID:    rental.WorkspaceID,
		ConnectionID:   rental.ConnectionID,
		OwnershipToken: rental.OwnershipToken,
	}
	if err := identity.Validate(); err != nil {
		return err
	}
	if len(rental.Generations) == 0 {
		return fmt.Errorf("Rental %q holds no generation, so it is a lease over nothing", rental.ID)
	}
	if rental.Version < uint64(len(rental.Generations)) {
		return fmt.Errorf(
			"Rental %q holds %d generations at version %d, and each of them took a transition to get there",
			rental.ID, len(rental.Generations), rental.Version,
		)
	}
	if err := rental.generationsRunInOrder(); err != nil {
		return err
	}
	return rental.releaseFollowsTheLastEnding()
}

// generationsRunInOrder holds the shape of the sequence: they are numbered from
// one without a gap, and every generation but the newest has already ended,
// because a lease can only have one machine under it at a time.
func (rental Rental) generationsRunInOrder() error {
	for index, generation := range rental.Generations {
		switch {
		case generation.Number != uint64(index+1):
			return fmt.Errorf("Rental %q holds generation %d in position %d", rental.ID, generation.Number, index+1)
		case generation.NodeID == "":
			return fmt.Errorf("Rental %q generation %d has no runtime invited onto it", rental.ID, generation.Number)
		case generation.BeganAt.IsZero():
			return fmt.Errorf("Rental %q generation %d began at no moment", rental.ID, generation.Number)
		case generation.Open() && index != len(rental.Generations)-1:
			return fmt.Errorf(
				"Rental %q generation %d is still open and generation %d already followed it",
				rental.ID, generation.Number, generation.Number+1,
			)
		case !generation.Open() && !generation.Ending.Valid():
			return fmt.Errorf("Rental %q generation %d ended and does not say how", rental.ID, generation.Number)
		}
	}
	return nil
}

// releaseFollowsTheLastEnding holds the one thing the lease says about itself
// beyond its generations. A released lease has to be released by something, and
// capacity whose machine was destroyed cannot still be held.
func (rental Rental) releaseFollowsTheLastEnding() error {
	latest := rental.Generations[len(rental.Generations)-1]
	released := latest.Ending.EndsTheLease() && !latest.Open()
	switch {
	case released && rental.Held():
		return fmt.Errorf("Rental %q generation %d was %s and the lease is still held", rental.ID, latest.Number, latest.Ending)
	case !released && !rental.Held():
		return fmt.Errorf("Rental %q was released at %s and no generation of it ended that way", rental.ID, rental.ReleasedAt.Format(time.RFC3339))
	default:
		return nil
	}
}

// Clone copies the generations so a caller holding the previous value keeps
// holding what it had. Every transition here returns a new lease, and a slice
// shared between the two would rewrite history in place. A store hands it out on
// both sides of a write for the same reason: what it holds is what it was told,
// and not whatever the caller did to the slice afterwards.
func (rental Rental) Clone() Rental {
	cloned := rental
	cloned.Generations = slices.Clone(rental.Generations)
	return cloned
}
