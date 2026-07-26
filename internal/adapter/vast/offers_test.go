package vast

import (
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

func pricePtr(v float64) *float64 { return &v }

func TestSecureOfferQueryHardCodesTheSecureTier(t *testing.T) {
	q := secureOfferQuery([]string{"RTX 4090"}, 2, 75, 20)
	verified, ok := q["verified"].(map[string]any)
	if !ok {
		t.Fatalf("verified filter = %v", q["verified"])
	}
	if got := verified["eq"]; got != true {
		t.Errorf("verified filter = %v", got)
	}
	if got := q["datacenter"].(map[string]any)["eq"]; got != true {
		t.Errorf("datacenter filter = %v", got)
	}
	if got := q["external"].(map[string]any)["eq"]; got != false {
		t.Errorf("external filter = %v", got)
	}
	if got := q["type"]; got != "ondemand" {
		t.Errorf("type = %v", got)
	}
	if got := q["num_gpus"].(map[string]any)["eq"]; got != 2 {
		t.Errorf("num_gpus = %v", got)
	}
	if got := q["allocated_storage"]; got != float64(75) {
		t.Errorf("allocated_storage = %v", got)
	}
	if got := q["gpu_name"].(map[string]any)["in"].([]string); len(got) != 1 || got[0] != "RTX 4090" {
		t.Errorf("gpu_name = %v", got)
	}
}

func TestBuildOffersMapsMarketplaceFacts(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	offers := []offer{{
		ID: 9001, GPUName: "RTX 4090", GPUArch: "nvidia", NumGPUs: 2, GPURAMMb: 24576,
		CPUCoresEffective: 16, CPURAMMb: 65536, DiskSpaceGB: 500,
		DPHTotal: pricePtr(0.72), Reliability: pricePtr(0.98), Verification: "verified",
	}}
	got := buildOffers(offers, 2, 75, now)
	if len(got) != 1 {
		t.Fatalf("expected 1 offer, got %d: %+v", len(got), got)
	}
	o := got[0]
	if o.ID != "off_vast_9001" || o.NativeRef != "9001" {
		t.Errorf("id/native ref = %q/%q", o.ID, o.NativeRef)
	}
	if o.Kind != domain.OfferKindProvisionable {
		t.Errorf("kind = %q, want provisionable", o.Kind)
	}
	// $0.72/hour all-in => 2e-4/second.
	if o.Pricing.RatePerSecondUSD < 1.9e-4 || o.Pricing.RatePerSecondUSD > 2.1e-4 {
		t.Errorf("rate per second = %v", o.Pricing.RatePerSecondUSD)
	}
	acc := o.Resources.Accelerators[0]
	if acc.Vendor != "NVIDIA" || acc.Model != "RTX 4090" || acc.Count != 2 {
		t.Errorf("accelerator = %+v", acc)
	}
	if acc.CanonicalModel != "nvidia-rtx-4090" {
		t.Errorf("canonical model = %q", acc.CanonicalModel)
	}
	if acc.MemoryBytes != 24576*mib {
		t.Errorf("gpu memory = %d", acc.MemoryBytes)
	}
	if o.Resources.CPUMillis != 16000 || o.Resources.MemoryBytes != 65536*mib {
		t.Errorf("cpu/mem = %d/%d", o.Resources.CPUMillis, o.Resources.MemoryBytes)
	}
	if o.Resources.EphemeralDiskBytes != 75*gib {
		t.Errorf("disk = %d", o.Resources.EphemeralDiskBytes)
	}
	// reliability2=0.98 => interruption rate 0.02, stated at full confidence
	// because Mercator read it off the publisher's own catalog.
	if o.Reliability.Interruptions.Rate < 0.019 || o.Reliability.Interruptions.Rate > 0.021 || o.Reliability.Interruptions.Confidence != 1 {
		t.Errorf("interruption history = %+v", o.Reliability.Interruptions)
	}
	// Vast measures nothing about refused starts, so the history states none. A
	// rate of zero at full confidence here would put "this machine has never
	// refused a start" into every Vast candidate's decision record as a fact its
	// publisher stated.
	if o.Reliability.StartFailures.Stated() {
		t.Errorf("start failure history = %+v, and Vast publishes no such measurement", o.Reliability.StartFailures)
	}
	if !o.Pricing.Known || !o.Capacity.Available {
		t.Errorf("price and capacity facts must be known: %+v", o)
	}
	// A machine that does not exist yet cannot enumerate what it holds.
	if o.Images.Known {
		t.Errorf("a fresh instance cannot report an inventory, got %+v", o.Images)
	}
}

func TestBuildOffersDropsNonSecureUnpricedAndWrongSizeOffers(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	offers := []offer{
		// Server-side filter regression must not leak unverified capacity.
		{ID: 1, GPUName: "RTX 4090", NumGPUs: 1, DPHTotal: pricePtr(0.2), Verification: "unverified"},
		{ID: 2, GPUName: "RTX 4090", NumGPUs: 1, DPHTotal: pricePtr(0.2), Verification: "deverified"},
		{ID: 3, GPUName: "RTX 4090", NumGPUs: 1, DPHTotal: nil, Verification: "verified"},           // no price
		{ID: 4, GPUName: "RTX 4090", NumGPUs: 4, DPHTotal: pricePtr(0.8), Verification: "verified"}, // wrong GPU count
	}
	if got := buildOffers(offers, 1, 20, now); len(got) != 0 {
		t.Fatalf("expected all offers dropped, got %+v", got)
	}
}

// TestAnAskThatPublishesNoUptimeScorePublishesNoHistory is silence read as
// silence. reliability2 is a share of the machine's uptime, so an ask that omits
// it decoded as zero says this machine drops every run it is given, and Mercator
// stated that at full confidence on the publisher's behalf: the worst answer in
// the catalog, invented out of a missing field, on a machine that may be perfect.
func TestAnAskThatPublishesNoUptimeScorePublishesNoHistory(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	offers := []offer{{
		ID: 9002, GPUName: "RTX 4090", GPUArch: "nvidia", NumGPUs: 1, GPURAMMb: 24576,
		CPUCoresEffective: 8, CPURAMMb: 32768, DiskSpaceGB: 500,
		DPHTotal: pricePtr(0.35), Verification: "verified",
	}}

	got := buildOffers(offers, 1, 75, now)

	if len(got) != 1 {
		t.Fatalf("expected 1 offer, got %d: %+v", len(got), got)
	}
	if got[0].Reliability.Measured() {
		t.Fatalf("recorded the history %+v for an ask that published no uptime score", got[0].Reliability)
	}
}

// TestTwoSearchesOfOneMachineAreOneCandidate is why this adapter states a region
// at all. A Vast ask ID is a fresh integer for every search of a machine that was
// already there, so a launch history keyed on anything Vast numbered would be a
// growing pile of keys holding one sample each, each of them reported as evidence
// about this exact candidate. What recurs is the place and the card, and this
// adapter is the only thing that knows Vast publishes them.
func TestTwoSearchesOfOneMachineAreOneCandidate(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	machine := offer{
		GPUName: "RTX 4090", GPUArch: "nvidia", NumGPUs: 2, GPURAMMb: 24576,
		CPUCoresEffective: 16, CPURAMMb: 65536, DiskSpaceGB: 500,
		Geolocation: "US-CA", DPHTotal: pricePtr(0.72), Verification: "verified",
	}
	first, second := machine, machine
	first.ID, second.ID = 9001, 12345

	found := buildOffers([]offer{first, second}, 2, 75, now)

	if len(found) != 2 {
		t.Fatalf("expected the machine under both ask IDs, got %d: %+v", len(found), found)
	}
	earlier := domain.CandidateIdentityOf(aggregated(found[0]), "sha256:image")
	later := domain.CandidateIdentityOf(aggregated(found[1]), "sha256:image")
	if earlier.Candidate(true) != later.Candidate(true) {
		t.Fatalf("two searches of one machine keyed differently:\n%s\n%s",
			earlier.Candidate(true), later.Candidate(true))
	}
	if earlier.ProviderAndRegion() != "provider=vast;region=US-CA" {
		t.Fatalf("the region rung of the ladder is %q, and Vast published US-CA", earlier.ProviderAndRegion())
	}
	for _, ask := range []string{"9001", "12345"} {
		if strings.Contains(earlier.Candidate(true), ask) {
			t.Fatalf("key %q names ask %s, which never comes back", earlier.Candidate(true), ask)
		}
	}
}

// aggregated is the offer as a scheduler receives it. The Broker stamps the
// adapter type from the connection the offer came through rather than letting an
// adapter name itself, so an offer straight out of buildOffers has no provider on
// it yet and the candidate identity it derives has no level above the machine.
func aggregated(offer domain.OfferSnapshot) domain.OfferSnapshot {
	offer.AdapterType = "vast"
	return offer
}
