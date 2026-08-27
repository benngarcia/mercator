package domain

import (
	"strconv"
	"strings"
)

// HostFact names one promise about the substrate a machine offers, as opposed
// to the resources it counts. The set is closed on purpose: a name is only
// worth stating if the machine stating it and the workload requiring it mean
// the same thing by it, and an open map of strings is how two spellings of one
// promise walk past each other with nothing able to say so.
type HostFact string

const (
	// HostFactSSH is shell access to the machine itself. It is a provider's fact
	// rather than a node's: Mercator reaches an enrolled machine over the
	// agent's own outbound session and needs no login on it, so what this
	// answers is whether the operator of a Run that wants a shell will get one.
	HostFactSSH HostFact = "ssh"
	// HostFactNvidiaDriver is a working NVIDIA driver on the host. The host
	// provides the driver, the image provides the workload's own accelerator
	// stack, and this is the coarse half of that contract: whether there is a
	// driver here at all. How old it is, which decides whether it can carry the
	// image's stack, is AcceleratorDriver beside it.
	HostFactNvidiaDriver HostFact = "nvidia_driver"
)

// KnownHostFacts is every fact a machine may state and a workload may require.
var KnownHostFacts = []HostFact{HostFactSSH, HostFactNvidiaDriver}

// Known reports whether this is a fact anything in Mercator can answer. A name
// outside the set is refused where it is submitted rather than published and
// silently matched by nobody.
func (fact HostFact) Known() bool {
	for _, known := range KnownHostFacts {
		if fact == known {
			return true
		}
	}
	return false
}

// HostFacts is what a machine, or the provider selling it, has established
// about the substrate under a workload. It is separate from the resources
// beside it because these are not quantities to compare: they are promises,
// and the interesting answer is which of three states each one is in.
//
// A fact stated true was established. A fact stated false was established
// false. A fact absent from the map was never established at all, and that is
// the state this type exists to keep distinguishable. A machine with no driver
// can never run an image that needs one, and a machine nobody asked is a
// machine nobody asked; Placement refuses both and records them under different
// codes, so an operator reading the decision knows whether to buy a different
// machine or go and find out about this one.
type HostFacts struct {
	// Attested is every fact somebody established about this machine. It is not
	// a set of the true ones: false is an answer, and dropping it would collapse
	// the two refusals this type keeps apart.
	Attested map[HostFact]bool `json:"attested,omitempty"`
	// Driver is the accelerator driver this host runs, in the vendor's own
	// versioning. It is what an image's declared accelerator stack is matched
	// against, and a host that never looked states none.
	Driver AcceleratorDriver `json:"driver,omitzero"`
}

// AcceleratorDriver is the host half of the accelerator stack: the driver the
// machine runs and the highest accelerator capability that driver supports. The
// image carries the workload's CUDA runtime and the host carries the driver, so
// these two are what decides whether an image can run here at all.
//
// Both versions are the vendor's own strings, unparsed. Mercator compares them
// as dotted numbers and says so out loud when it cannot.
type AcceleratorDriver struct {
	Vendor  string `json:"vendor,omitempty"`
	Version string `json:"version,omitempty"`
	// Capability is the highest accelerator capability the driver supports,
	// which for NVIDIA is the CUDA compute capability its cards report.
	Capability string `json:"capability,omitempty"`
}

// HostRequirements is what a workload declares it needs of the substrate under
// it: the promises it will not run without, and the driver its image's own
// accelerator stack was built against.
//
// It is declared rather than discovered. An image built against CUDA 13 fails
// on a CUDA 12 driver at the moment the process starts, on a machine already
// paid for, with a stack trace out of somebody else's runtime; declaring the
// floor is what turns that into a refusal in the Booking Decision. Mercator
// never answers it by installing a stack onto the host: the host's driver is
// the operator's, and a control plane that upgraded one would be changing a
// machine underneath every other workload on it.
type HostRequirements struct {
	// Facts are the promises this Run refuses to run without. A machine that
	// never stated one is refused as loudly as a machine that stated its
	// opposite.
	Facts []HostFact `json:"facts,omitempty"`
	// MinDriverVersion is the oldest driver this workload's image runs on, and
	// MinDriverCapability the lowest accelerator capability it was built for.
	// Both are the vendor's own versioning, because that is what the host reports
	// and translating either into a Mercator scale would put a guess between two
	// numbers that already compare.
	MinDriverVersion    string `json:"min_driver_version,omitempty"`
	MinDriverCapability string `json:"min_driver_capability,omitempty"`
}

// Stated reports whether this workload asked anything of its host at all.
func (required HostRequirements) Stated() bool {
	return len(required.Facts) > 0 || required.MinDriverVersion != "" || required.MinDriverCapability != ""
}

// Violations is what one machine's facts answer to one workload's host
// requirements. It lives here rather than in the scheduler because two readers
// ask it: Placement, deciding, and the Lab, judging what Placement decided. Two
// implementations of one rule is how the judge comes to agree with the accused.
func (facts HostFacts) Violations(required HostRequirements) []Violation {
	var violations []Violation
	for _, fact := range required.Facts {
		stated, attested := facts.Attested[fact]
		switch {
		case !attested:
			violations = append(violations, Violation{
				Code:     "UNKNOWN_FACT",
				Path:     "facts." + string(fact),
				Required: true,
				Offered:  "unstated",
				Message:  "Nobody has established " + string(fact) + " on this machine, and this Run will not run where it is unknown.",
				Unstated: true,
			})
		case !stated:
			violations = append(violations, Violation{
				Code:     "CAPABILITY_MISMATCH",
				Path:     "facts." + string(fact),
				Required: true,
				Offered:  false,
				Message:  "This machine has established that it does not offer " + string(fact) + ".",
			})
		}
	}
	return append(violations, facts.Driver.violations(required)...)
}

// driverFloor is one version the host states weighed against one the image
// declares. The two floors are the same question asked of two numbers, so they
// are one loop rather than two branches that drift.
type driverFloor struct {
	path    string
	subject string
	floor   string
	stated  string
}

func (driver AcceleratorDriver) violations(required HostRequirements) []Violation {
	var violations []Violation
	for _, floor := range []driverFloor{
		{path: "host.driver_version", subject: "driver", floor: required.MinDriverVersion, stated: driver.Version},
		{path: "host.driver_capability", subject: "driver capability", floor: required.MinDriverCapability, stated: driver.Capability},
	} {
		if violation, refused := floor.violation(); refused {
			violations = append(violations, violation)
		}
	}
	return violations
}

func (floor driverFloor) violation() (Violation, bool) {
	if floor.floor == "" {
		return Violation{}, false
	}
	if floor.stated == "" {
		return Violation{
			Code:     "UNKNOWN_FACT",
			Path:     floor.path,
			Required: floor.floor,
			Offered:  "unstated",
			Message:  "This machine has not stated the " + floor.subject + " its accelerators run on, and the image declares one it needs.",
			Unstated: true,
		}, true
	}
	order, comparable := CompareDottedVersions(floor.stated, floor.floor)
	if !comparable {
		return Violation{
			Code:     "UNKNOWN_FACT",
			Path:     floor.path,
			Required: floor.floor,
			Offered:  floor.stated,
			Message:  "Mercator cannot order this machine's stated " + floor.subject + " against the one the image declares.",
			Unstated: true,
		}, true
	}
	if order < 0 {
		return Violation{
			Code:     "CAPABILITY_MISMATCH",
			Path:     floor.path,
			Required: floor.floor,
			Offered:  floor.stated,
			Message:  "The host's " + floor.subject + " is older than the image's accelerator stack needs. The host provides the driver and the image provides the stack, and Mercator installs neither onto the other.",
		}, true
	}
	return Violation{}, false
}

// CompareDottedVersions orders two vendor version strings by their dotted
// numeric components, reporting separately whether they could be ordered at
// all. Missing trailing components count as zero, so 595 and 595.0.0 are the
// same driver.
//
// The second answer is the point. A component neither side can parse is a
// version Mercator does not understand, and returning some order anyway would
// place a Run on a driver nobody compared: read as zero, an unparseable string
// sorts under every floor and refuses machines that are fine; read as large it
// admits machines that are not. Placement records the silence instead.
//
// A difference settled before the unreadable component is still settled, which
// is deliberate rather than an accident of the loop. A distribution's patched
// 550.54.15-ubuntu3 is unambiguously past a floor of 535 whatever the suffix
// means, and calling that unknown would refuse a machine that is plainly fine.
// The suffix only makes the answer unknown when the comparison reaches it.
func CompareDottedVersions(left, right string) (int, bool) {
	leftParts := strings.Split(strings.TrimSpace(left), ".")
	rightParts := strings.Split(strings.TrimSpace(right), ".")
	for index := range max(len(leftParts), len(rightParts)) {
		leftValue, leftOK := dottedComponent(leftParts, index)
		rightValue, rightOK := dottedComponent(rightParts, index)
		if !leftOK || !rightOK {
			return 0, false
		}
		if leftValue != rightValue {
			return leftValue - rightValue, true
		}
	}
	return 0, true
}

func dottedComponent(parts []string, index int) (int, bool) {
	if index >= len(parts) {
		return 0, true
	}
	value, err := strconv.Atoi(parts[index])
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}
