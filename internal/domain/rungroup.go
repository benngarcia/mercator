package domain

// This file is the group half of the dependency model, and deliberately nothing
// more than that. A group is a label the work carries: it names the family a Run
// arrived with and states how wide that family may run at once. Nothing here
// orders one member against another, names an edge between them, or waits for a
// member to finish, because that is a workflow language and Mercator is not one.
//
// The bound belongs to the caller rather than to Mercator, which is why every
// member states it. A group is not an object the control plane owns: there is no
// stream for it, no lifecycle, and nothing to create before the work arrives. What
// Mercator holds is the members, and the width they all declare is read off them.

// RunGroup is the family this Run belongs to and how wide that family may run.
type RunGroup struct {
	// ID is the family's name, as its caller chose it. It is scoped to the Run's
	// own workspace exactly as a cache name is: two tenants naming one sweep are
	// running two sweeps, and neither may hold the other's members back.
	ID string `json:"id,omitempty"`
	// MaxParallel is the most members of this group that may hold capacity at
	// once. Zero is the absence of a group rather than a group of nothing, which
	// is why a name without a bound is refused where the Run enters instead of
	// being read as either.
	MaxParallel int `json:"max_parallel,omitempty"`
}

// Declared reports whether this Run named a family at all. A Run that named none
// is a family of one and is bounded by nothing.
func (group RunGroup) Declared() bool { return group.ID != "" }

// Full reports whether this family is already as wide as it declared, counting
// the members that hold capacity now.
func (group RunGroup) Full(holding int) bool {
	return group.Declared() && holding >= group.MaxParallel
}

// Stated reports whether this declaration says one thing. A name with no bound
// declares a family whose width nobody stated, and a bound with no name states a
// width for a family that does not exist.
func (group RunGroup) Stated() bool {
	return group.Declared() == (group.MaxParallel > 0)
}
