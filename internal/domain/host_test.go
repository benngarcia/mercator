package domain_test

import (
	"testing"

	"github.com/benngarcia/mercator/internal/domain"
)

// TestAHostThatNeverStatedAFactIsRefusedAsASilence is the distinction the whole
// type exists for. A machine that answered "no driver" is one to stop buying,
// and a machine nobody established a driver for is one to go and ask, so the
// two carry different codes and only the second is counted a silence. Read as
// one answer, a fleet nobody had measured said "this work can never run here".
func TestAHostThatNeverStatedAFactIsRefusedAsASilence(t *testing.T) {
	unasked := domain.HostFacts{}

	violations := unasked.Violations(domain.HostRequirements{Facts: []domain.HostFact{domain.HostFactSSH}})

	if len(violations) != 1 {
		t.Fatalf("a machine nobody asked was refused %+v", violations)
	}
	if violations[0].Code != "UNKNOWN_FACT" || violations[0].Path != "facts.ssh" {
		t.Errorf("refusal = %s at %s, want UNKNOWN_FACT at facts.ssh", violations[0].Code, violations[0].Path)
	}
	if !violations[0].Unstated {
		t.Error("a fact nobody stated was recorded as a fact that refuses the Run")
	}
}

// TestAHostThatStatedANoIsRefusedForWhatItIs is the other half. The machine
// looked, established that it has no driver, and that is a refusal an operator
// can act on rather than an answer nobody gave.
func TestAHostThatStatedANoIsRefusedForWhatItIs(t *testing.T) {
	driverless := domain.HostFacts{Attested: map[domain.HostFact]bool{domain.HostFactNvidiaDriver: false}}

	violations := driverless.Violations(domain.HostRequirements{Facts: []domain.HostFact{domain.HostFactNvidiaDriver}})

	if len(violations) != 1 {
		t.Fatalf("a machine with no driver was refused %+v", violations)
	}
	if violations[0].Code != "CAPABILITY_MISMATCH" || violations[0].Path != "facts.nvidia_driver" {
		t.Errorf("refusal = %s at %s, want CAPABILITY_MISMATCH at facts.nvidia_driver", violations[0].Code, violations[0].Path)
	}
	if violations[0].Unstated {
		t.Error("a machine that established it has no driver was recorded as a machine nobody asked")
	}
}

// TestADriverOlderThanTheImageNeedsRefusesTheHost is the compatibility contract
// itself. The host provides the driver and the image provides the accelerator
// stack, so an image built against CUDA 13 cannot start on a CUDA 12 driver, and
// nothing in Mercator may answer that by installing a stack onto the host.
func TestADriverOlderThanTheImageNeedsRefusesTheHost(t *testing.T) {
	cuda12 := domain.HostFacts{Driver: domain.AcceleratorDriver{
		Vendor: "nvidia", Version: "525.85.12", Capability: "12.0",
	}}

	violations := cuda12.Violations(domain.HostRequirements{MinDriverCapability: "13.0"})

	if len(violations) != 1 || violations[0].Code != "CAPABILITY_MISMATCH" || violations[0].Path != "host.driver_capability" {
		t.Fatalf("a CUDA 12 driver under a CUDA 13 image was refused %+v", violations)
	}
}

// TestADriverNewerThanTheImageNeedsCarriesIt states the direction, which a rule
// written the other way round would pass every other case here. Missing
// components count as zero, so a floor of 535 is met by 535.0.0 and by
// everything after it.
func TestADriverNewerThanTheImageNeedsCarriesIt(t *testing.T) {
	current := domain.HostFacts{Driver: domain.AcceleratorDriver{Vendor: "nvidia", Version: "595.71.05", Capability: "13.2"}}

	for _, floor := range []domain.HostRequirements{
		{MinDriverVersion: "535"},
		{MinDriverVersion: "595"},
		{MinDriverVersion: "595.71.05"},
		{MinDriverCapability: "12.4"},
	} {
		if violations := current.Violations(floor); len(violations) > 0 {
			t.Errorf("driver 595.71.05 supporting 13.2 was refused %+v against %+v", violations, floor)
		}
	}
}

// TestADriverNobodyCanOrderIsASilenceRatherThanAnAnswer is why the comparison
// answers twice. A component neither side can parse read as zero refuses
// machines that are fine and read as large admits machines that are not, so
// Placement records that nothing ordered them.
//
// It is asked of a floor that reaches the component nobody can read, because a
// difference settled before then is settled: a distribution's patched
// 550.54.15-ubuntu3 is unambiguously past a floor of 535 whatever the suffix
// means, and calling that unknown would refuse a machine that is plainly fine.
func TestADriverNobodyCanOrderIsASilenceRatherThanAnAnswer(t *testing.T) {
	vendorString := domain.HostFacts{Driver: domain.AcceleratorDriver{Vendor: "nvidia", Version: "550.54.15-ubuntu3"}}

	violations := vendorString.Violations(domain.HostRequirements{MinDriverVersion: "550.54.16"})

	if len(violations) != 1 || violations[0].Code != "UNKNOWN_FACT" || !violations[0].Unstated {
		t.Fatalf("a driver nobody can order was refused %+v", violations)
	}
	if _, comparable := domain.CompareDottedVersions("550.54.15-ubuntu3", "550.54.16"); comparable {
		t.Error("a version with a non-numeric component was ordered anyway")
	}
	if order, comparable := domain.CompareDottedVersions("550.54.15-ubuntu3", "535"); !comparable || order <= 0 {
		t.Errorf("a difference settled before the unreadable component was refused an order: %d, %v", order, comparable)
	}
}

// TestAWorkloadThatAsksForAFactNothingEstablishesIsRefusedWhereItEnters keeps
// the caller's typo out of every Booking Decision it would otherwise spoil. A
// fact outside the closed set is matched by nothing, so such a Run would be
// refused on every machine in the fleet and read as a fleet that cannot serve it.
func TestAWorkloadThatAsksForAFactNothingEstablishesIsRefusedWhereItEnters(t *testing.T) {
	revision := workloadRequiringHost(domain.HostRequirements{
		Facts:            []domain.HostFact{"infiniband"},
		MinDriverVersion: "twelve",
	})

	violations := domain.ValidateWorkloadRevision(revision)

	if !refusesWith(violations, "UNKNOWN_HOST_FACT", "spec.resources.host.facts[0]") {
		t.Errorf("a Run asking for a fact nothing establishes was admitted: %+v", violations)
	}
	if !refusesWith(violations, "MALFORMED_VERSION", "spec.resources.host.min_driver_version") {
		t.Errorf("a Run stating a driver floor nothing can order was admitted: %+v", violations)
	}
}

// workloadRequiringHost is the smallest Run that declares something of its host.
// Everything else about it is whatever else validation has to say, which this
// case does not read: what it asks is whether the host declaration was checked
// at all.
func workloadRequiringHost(required domain.HostRequirements) domain.WorkloadRevision {
	return domain.WorkloadRevision{
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{
				Name:     "main",
				Image:    "ghcr.io/acme/trainer@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
			}},
			Resources: domain.ResourceRequirements{Host: required},
		},
	}
}

func refusesWith(violations []domain.Violation, code, path string) bool {
	for _, violation := range violations {
		if violation.Code == code && violation.Path == path {
			return true
		}
	}
	return false
}
