// Package scenario owns the versioned Mercator Lab Blueprint catalog and the
// placement-runner adapter. Blueprints describe digest-pinned images,
// immutable Artifacts, mutable Cache Mounts, Rentals, provider capacity, Run
// arrivals, faults, and public evidence.
//
// Placement decision correctness runs against simulated capacity through the
// real orchestrator, Placement implementation, and SQLite event log. Later Lab
// slices execute the same catalog through deterministic in-process and
// process-backed worlds.
//
// Green classification fails on regression. Target classification records
// unbuilt semantics as pending, and a target that starts passing must be
// deliberately promoted.
package scenario

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Status string

const (
	// StatusGreen asserts behavior Mercator has today; a failure is a regression.
	StatusGreen Status = "green"
	// StatusTarget encodes the future contract; a failure is pending, not broken.
	StatusTarget Status = "target"
)

type Outcome string

const (
	OutcomePlace Outcome = "place"
	OutcomeFail  Outcome = "fail"
)

type BookingState string

const (
	BookingRunning BookingState = "running"
	BookingQueued  BookingState = "queued"
)

// Capability names one unbuilt semantic a target scenario is red for. The
// declaration keeps pending results attributable: a target fixture states
// exactly which capability its promotion waits on, and green scenarios may
// declare none.
type Capability string

const (
	// CapabilityRentalSchedule is the Broker ingesting, versioning, and
	// appending to ordered per-Rental schedules.
	CapabilityRentalSchedule Capability = "rental_schedule"
	// CapabilityScheduleAdvancement is the Broker advancing schedules over
	// time: dispatching the next Booking, expiring one past its latest
	// start, and re-placing its Run.
	CapabilityScheduleAdvancement Capability = "schedule_advancement"
	// CapabilityHostFacts is providers advertising SSH and NVIDIA-driver
	// facts on offers, rejected loudly when absent or false.
	CapabilityHostFacts Capability = "host_facts"
	// CapabilityArtifacts is immutable Artifact production, dependency, and
	// replica tracking.
	CapabilityArtifacts Capability = "artifacts"
	// CapabilityArtifactEvidence is per-candidate Artifact locality evidence.
	CapabilityArtifactEvidence Capability = "artifact_evidence"
	// CapabilityLabExecution is deterministic execution beyond one Placement
	// decision.
	CapabilityLabExecution Capability = "lab_execution"
	// CapabilityEffectLedger is inspectable external commands and
	// consequences.
	CapabilityEffectLedger Capability = "effect_ledger"
	// CapabilityControlPlaneRestart is deterministic restart with surviving
	// external resources.
	CapabilityControlPlaneRestart Capability = "control_plane_restart"
	// CapabilityRunBundle is portable export, normalization, and replay.
	CapabilityRunBundle Capability = "run_bundle"
	// CapabilityInvariants is the transition-aware safety and bounded-liveness
	// registry.
	CapabilityInvariants Capability = "invariants"
	// CapabilityLabUI is the Lab-backed normal HTTP/SSE and console path.
	CapabilityLabUI Capability = "lab_ui"
)

var knownCapabilities = map[Capability]bool{
	CapabilityRentalSchedule:      true,
	CapabilityScheduleAdvancement: true,
	CapabilityHostFacts:           true,
	CapabilityArtifacts:           true,
	CapabilityArtifactEvidence:    true,
	CapabilityLabExecution:        true,
	CapabilityEffectLedger:        true,
	CapabilityControlPlaneRestart: true,
	CapabilityRunBundle:           true,
	CapabilityInvariants:          true,
	CapabilityLabUI:               true,
}

// MaxQueuedBookings bounds every RentalSchedule: at most this many queued
// Bookings may wait behind the running Booking. A Run arriving at
// a full schedule must go elsewhere, whatever the score says.
const MaxQueuedBookings = 4

// defaultWorldStart is the scripted clock's origin when a fixture does not
// state one. Every relative moment ("+6m") resolves against it.
var defaultWorldStart = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

type Scenario struct {
	Name    string `json:"-"`
	Summary string `json:"summary"`
	Status  Status `json:"status"`
	// MissingCapabilities names the unbuilt semantics a target scenario is red
	// for. Required for target scenarios, forbidden for green ones.
	MissingCapabilities []Capability `json:"missing_capabilities,omitempty"`
	World               WorldSpec    `json:"world"`
	// Request/Expect is the single-decision shorthand; Timeline is the scripted
	// alternative for scenarios that advance the clock or submit several runs.
	Request  *RequestSpec `json:"request,omitempty"`
	Expect   *ExpectSpec  `json:"expect,omitempty"`
	Timeline []StepSpec   `json:"timeline,omitempty"`
}

type WorldSpec struct {
	Clock           time.Time              `json:"clock,omitzero"`
	Images          map[string]ImageSpec   `json:"images,omitempty"`
	Artifacts       []ArtifactSpec         `json:"artifacts,omitempty"`
	Rentals         []RentalSpec           `json:"rentals,omitempty"`
	RentalSchedules []RentalScheduleSpec   `json:"rental_schedules,omitempty"`
	Marketplace     []MarketplaceOfferSpec `json:"marketplace,omitempty"`
}

// ArtifactSpec declares immutable, versioned content available to Runs.
type ArtifactSpec struct {
	ID   string   `json:"id"`
	Size ByteSize `json:"size"`
}

// Start is the scripted clock's origin for this world.
func (w WorldSpec) Start() time.Time {
	if w.Clock.IsZero() {
		return defaultWorldStart
	}
	return w.Clock.UTC()
}

type ImageSpec struct {
	Layers []LayerSpec `json:"layers"`
}

// LayerSpec identifies one image layer by its exact OCI content digest.
type LayerSpec struct {
	Digest string   `json:"digest"`
	Size   ByteSize `json:"size"`
}

// RentalSpec is reusable machine capacity the broker owns. Its schedule is
// broker state; the machine itself receives only the running Booking through
// its standard Docker endpoint.
type RentalSpec struct {
	ID string `json:"id"`
	// IdleLeaseExpiresIn bounds how long the rental survives idle, measured
	// from the world clock's start. Omitted means the lease outlives the
	// scenario.
	IdleLeaseExpiresIn *Duration `json:"idle_lease_expires_in,omitempty"`
	// CachedImages holds every layer of the named images; CachedLayers adds
	// individual layers (for a rental warm from a previous image version).
	CachedImages     []string       `json:"cached_images,omitempty"`
	CachedLayers     []string       `json:"cached_layers,omitempty"`
	ArtifactReplicas []string       `json:"artifact_replicas,omitempty"`
	CacheMounts      []string       `json:"cache_mounts,omitempty"`
	RatePerHourUSD   float64        `json:"rate_per_hour_usd"`
	Resources        *ResourcesSpec `json:"resources,omitempty"`
}

// RentalScheduleSpec is Mercator's ordered sequence of nonterminal Bookings
// assigned to one Rental. At most one Booking runs; any number may wait.
type RentalScheduleSpec struct {
	RentalID string              `json:"rental"`
	Version  uint64              `json:"version,omitempty"`
	Running  *RunningBookingSpec `json:"running,omitempty"`
	Queued   []QueuedBookingSpec `json:"queued,omitempty"`
}

type RunningBookingSpec struct {
	BookingID string `json:"booking"`
	RunID     string `json:"run"`
	// RemainingMaxRuntime is the recorded, enforced upper bound: the basis for
	// latest-start guarantees.
	RemainingMaxRuntime Duration `json:"remaining_max_runtime"`
	// RemainingExpectedRuntime is the p50 remaining runtime, the basis for
	// projected starts and queue-delay scoring. Defaults to the max bound.
	RemainingExpectedRuntime *Duration `json:"remaining_expected_runtime,omitempty"`
	// CompletesAfter is when completion is actually observed, defaulting to
	// the expected remaining runtime. Another value models a run finishing
	// early or overrunning its estimate up to the enforced bound.
	CompletesAfter *Duration `json:"completes_after,omitempty"`
}

func (p RunningBookingSpec) expectedRemaining() Duration {
	if p.RemainingExpectedRuntime != nil {
		return *p.RemainingExpectedRuntime
	}
	return p.RemainingMaxRuntime
}

type QueuedBookingSpec struct {
	BookingID  string   `json:"booking"`
	RunID      string   `json:"run"`
	MaxRuntime Duration `json:"max_runtime"`
	// ExpectedRuntime is the p50 runtime used for projected starts and
	// queue-delay scoring. Defaults to the max bound.
	ExpectedRuntime *Duration `json:"expected_runtime,omitempty"`
	// LatestStart is the last acceptable start time for this Booking. When it
	// expires, Mercator removes the Booking and re-evaluates its Run.
	LatestStart *Moment `json:"latest_start,omitempty"`
}

func (p QueuedBookingSpec) expected() Duration {
	if p.ExpectedRuntime != nil {
		return *p.ExpectedRuntime
	}
	return p.MaxRuntime
}

type MarketplaceOfferSpec struct {
	ID             string           `json:"id"`
	Provider       string           `json:"provider,omitempty"`
	RatePerHourUSD float64          `json:"rate_per_hour_usd"`
	Provisioning   ProvisioningSpec `json:"provisioning"`
	Resources      *ResourcesSpec   `json:"resources,omitempty"`
	// Facts are the hardware facts providers owe on the offer (SSH root
	// access, working NVIDIA driver). Omitted map entries are unknown facts;
	// an offer missing or failing one must be rejected loudly. Target
	// ontology: no offer field carries these yet.
	Facts map[string]bool `json:"facts,omitempty"`
}

type ProvisioningSpec struct {
	Expected Duration  `json:"expected"`
	P90      *Duration `json:"p90,omitempty"`
}

// ResourcesSpec describes machine inventory (rentals, marketplace offers) or
// run requirements (requests). Omitted fields default host-side to a generous
// GPU-box shape (8 CPUs, 32GB memory, 200GB disk) and request-side to the
// workload defaults, so fixtures state only what the scenario is about.
type ResourcesSpec struct {
	CPUMillis int64    `json:"cpu_millis,omitempty"`
	Memory    ByteSize `json:"memory,omitempty"`
	Disk      ByteSize `json:"disk,omitempty"`
	GPU       *GPUSpec `json:"gpu,omitempty"`
}

type GPUSpec struct {
	Model  string   `json:"model"`
	Count  int      `json:"count,omitempty"`
	Memory ByteSize `json:"memory,omitempty"`
}

type RequestSpec struct {
	Image           string         `json:"image"`
	Resources       *ResourcesSpec `json:"resources,omitempty"`
	MaxRuntime      *Duration      `json:"max_runtime,omitempty"`
	ExpectedRuntime *Duration      `json:"expected_runtime,omitempty"`
	Objective       string         `json:"objective,omitempty"`
	// CacheMounts declare mutable, application-owned caches by their
	// workspace-scoped names.
	CacheMounts []CacheMountSpec `json:"cache_mounts,omitempty"`
	// ConsumesArtifacts and ProducesArtifacts refer to immutable Artifact IDs
	// declared in the Blueprint world.
	ConsumesArtifacts []string `json:"consumes_artifacts,omitempty"`
	ProducesArtifacts []string `json:"produces_artifacts,omitempty"`
}

type CacheMountSpec struct {
	Name string `json:"name"`
}

type ExpectSpec struct {
	// Outcome is the decision the event log must record: "place" (a selected
	// offer) or "fail" (a recorded decision with no feasible offers). Selecting
	// a busy Rental creates the Booking described by Booking.
	Outcome Outcome             `json:"outcome"`
	Offer   string              `json:"offer,omitempty"`
	Reasons []string            `json:"reasons,omitempty"`
	Booking *BookingExpectation `json:"booking,omitempty"`
	// Disposition asserts the recorded cleanup intent on the launch intent:
	// "release" for standing rentals, "terminate" for provisioned hosts.
	Disposition string `json:"disposition,omitempty"`
	// Candidates assert the per-candidate evidence the decision weighed,
	// keyed by rental or marketplace offer ID.
	Candidates map[string]CandidateExpectation `json:"candidates,omitempty"`
}

type BookingExpectation struct {
	BookingID       string       `json:"id"`
	RentalID        string       `json:"rental"`
	State           BookingState `json:"state"`
	AfterBooking    string       `json:"after,omitempty"`
	ProjectedStart  *Duration    `json:"projected_start_in,omitempty"`
	LatestStart     *Moment      `json:"latest_start,omitempty"`
	ScheduleVersion uint64       `json:"schedule_version"`
}

type CandidateExpectation struct {
	Feasible         *bool           `json:"feasible,omitempty"`
	Rejected         []RejectionSpec `json:"rejected,omitempty"`
	QueueSeconds     *Bound          `json:"queue_seconds,omitempty"`
	ProvisionSeconds *Bound          `json:"provision_seconds,omitempty"`
	PullSeconds      *Bound          `json:"pull_seconds,omitempty"`
	// Schedule asserts the ordered broker-owned schedule evidence weighed for
	// this Rental candidate.
	Schedule *ScheduleEvidenceExpectation `json:"rental_schedule,omitempty"`
	// Artifacts asserts recorded immutable-Artifact locality evidence.
	Artifacts map[string]string `json:"artifact_evidence,omitempty"`
}

type ScheduleEvidenceExpectation struct {
	Version        uint64                  `json:"version"`
	Running        *RunningBookingEvidence `json:"running,omitempty"`
	Preceding      []QueuedBookingEvidence `json:"preceding,omitempty"`
	ProjectedStart Duration                `json:"projected_start_in"`
}

type RunningBookingEvidence struct {
	BookingID           string   `json:"booking"`
	RunID               string   `json:"run"`
	RemainingMaxRuntime Duration `json:"remaining_max_runtime"`
	// RemainingExpectedRuntime is the recorded p50; defaults to the max bound.
	RemainingExpectedRuntime *Duration `json:"remaining_expected_runtime,omitempty"`
}

func (e RunningBookingEvidence) expectedRemaining() Duration {
	if e.RemainingExpectedRuntime != nil {
		return *e.RemainingExpectedRuntime
	}
	return e.RemainingMaxRuntime
}

type QueuedBookingEvidence struct {
	BookingID  string   `json:"booking"`
	RunID      string   `json:"run"`
	MaxRuntime Duration `json:"max_runtime"`
	// ExpectedRuntime is the recorded p50; defaults to the max bound.
	ExpectedRuntime *Duration `json:"expected_runtime,omitempty"`
}

func (e QueuedBookingEvidence) expected() Duration {
	if e.ExpectedRuntime != nil {
		return *e.ExpectedRuntime
	}
	return e.MaxRuntime
}

type RejectionSpec struct {
	Code string `json:"code"`
	Path string `json:"path"`
}

// StepSpec is one timeline entry: exactly one of Submit (a named Run with its
// request and expectation), Advance (move the scripted clock), or Reconcile
// (drive Broker advancement for a named Run after relevant world state changed).
type StepSpec struct {
	Submit    string       `json:"submit,omitempty"`
	Request   *RequestSpec `json:"request,omitempty"`
	Advance   *Duration    `json:"advance,omitempty"`
	Reconcile string       `json:"reconcile,omitempty"`
	Expect    *ExpectSpec  `json:"expect,omitempty"`
}

// Duration is a JSON string in Go duration syntax ("6m", "1h30m").
type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("durations are strings like \"6m\": %w", err)
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Duration() time.Duration { return time.Duration(d) }

// ByteSize is a JSON string with a decimal unit ("40GB", "512MB", "1.5TB")
// or a bare number of bytes.
type ByteSize int64

var byteSizePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*(B|KB|MB|GB|TB)$`)
var ociDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var ociImageRefPattern = regexp.MustCompile(`^(?:[^@\s]+@)?sha256:[0-9a-f]{64}$`)

var byteSizeUnits = map[string]float64{"B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12}

func (b *ByteSize) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] != '"' {
		var raw int64
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		*b = ByteSize(raw)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	match := byteSizePattern.FindStringSubmatch(strings.TrimSpace(text))
	if match == nil {
		return fmt.Errorf("byte sizes look like \"40GB\" or \"512MB\", got %q", text)
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return err
	}
	*b = ByteSize(int64(math.Round(value * byteSizeUnits[match[2]])))
	return nil
}

// Moment is an instant written relative to the world clock's start: "+6m".
type Moment struct {
	Offset time.Duration
}

func (m *Moment) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	if !strings.HasPrefix(text, "+") {
		return fmt.Errorf("moments are offsets from the world clock start like \"+6m\", got %q", text)
	}
	offset, err := time.ParseDuration(text[1:])
	if err != nil {
		return err
	}
	m.Offset = offset
	return nil
}

// Resolve returns the absolute instant for a world started at start.
func (m Moment) Resolve(start time.Time) time.Time { return start.Add(m.Offset) }

// Bound is a numeric expectation: a bare number asserts exact equality (to a
// millionth), an object asserts {"at_least": x} and/or {"at_most": y}.
type Bound struct {
	Exactly *float64 `json:"-"`
	AtLeast *float64 `json:"at_least,omitempty"`
	AtMost  *float64 `json:"at_most,omitempty"`
}

func (b *Bound) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] != '{' {
		var value float64
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		b.Exactly = &value
		return nil
	}
	type bare Bound
	return strictUnmarshal(data, (*bare)(b))
}

// Check reports "" when actual satisfies the bound, else a description.
func (b Bound) Check(actual float64) string {
	if b.Exactly != nil && math.Abs(actual-*b.Exactly) > 1e-6 {
		return fmt.Sprintf("want exactly %v, got %v", *b.Exactly, actual)
	}
	if b.AtLeast != nil && actual < *b.AtLeast {
		return fmt.Sprintf("want at least %v, got %v", *b.AtLeast, actual)
	}
	if b.AtMost != nil && actual > *b.AtMost {
		return fmt.Sprintf("want at most %v, got %v", *b.AtMost, actual)
	}
	return ""
}

func strictUnmarshal(data []byte, v any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("trailing content after document")
	}
	return nil
}

// Load adapts one canonical Blueprint or legacy fixture for the placement
// runner.
func Load(path string) (Scenario, error) {
	blueprint, err := LoadBlueprint(path)
	if err != nil {
		return Scenario{}, err
	}
	scenario, ok := blueprint.PlacementScenario()
	if !ok {
		return Scenario{}, fmt.Errorf("%s: Blueprint is not a placement fixture", path)
	}
	return scenario, nil
}

// LoadCorpus reads every *.json scenario in dir, sorted by name.
func LoadCorpus(dir string) ([]Scenario, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	scenarios := make([]Scenario, 0, len(paths))
	for _, path := range paths {
		sc, err := Load(path)
		if err != nil {
			return nil, err
		}
		scenarios = append(scenarios, sc)
	}
	return scenarios, nil
}

// Steps returns the scenario's timeline, synthesizing a single submit step
// from the request/expect shorthand.
func (sc Scenario) Steps() []StepSpec {
	if len(sc.Timeline) > 0 {
		return sc.Timeline
	}
	return []StepSpec{{Submit: "run", Request: sc.Request, Expect: sc.Expect}}
}

func (sc Scenario) validate() error {
	if sc.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	if err := validateClassification(sc.Status, sc.MissingCapabilities); err != nil {
		return err
	}
	if err := sc.World.validate(); err != nil {
		return err
	}
	hasShorthand := sc.Request != nil || sc.Expect != nil
	if hasShorthand && len(sc.Timeline) > 0 {
		return fmt.Errorf("use request/expect or timeline, not both")
	}
	if hasShorthand && (sc.Request == nil || sc.Expect == nil) {
		return fmt.Errorf("request and expect are both required")
	}
	if !hasShorthand && len(sc.Timeline) == 0 {
		return fmt.Errorf("a scenario needs a request/expect or a timeline")
	}
	submitted := map[string]bool{}
	for i, step := range sc.Steps() {
		if err := step.validate(submitted); err != nil {
			return fmt.Errorf("timeline[%d]: %w", i, err)
		}
	}
	for i, step := range sc.Steps() {
		if step.Request != nil {
			if err := sc.World.validRequest(*step.Request); err != nil {
				return fmt.Errorf("timeline[%d]: %w", i, err)
			}
		}
		if step.Expect != nil {
			if err := sc.World.validExpect(*step.Expect); err != nil {
				return fmt.Errorf("timeline[%d]: %w", i, err)
			}
		}
	}
	return sc.validateScheduleTimeline()
}

func validateClassification(status Status, missing []Capability) error {
	if status != StatusGreen && status != StatusTarget {
		return fmt.Errorf("classification status must be %q or %q, got %q", StatusGreen, StatusTarget, status)
	}
	if status == StatusTarget && len(missing) == 0 {
		return fmt.Errorf("target scenarios declare the missing_capabilities their promotion waits on")
	}
	if status == StatusGreen && len(missing) > 0 {
		return fmt.Errorf("green scenarios declare no missing_capabilities")
	}
	for _, capability := range missing {
		if !knownCapabilities[capability] {
			return fmt.Errorf("unknown capability %q", capability)
		}
	}
	return nil
}

// validateScheduleTimeline proves that each fixture's expected schedule
// versions, predecessors, and projected starts follow from the decisions
// before it. A target scenario may be red in execution; its model must still
// be internally coherent.
func (sc Scenario) validateScheduleTimeline() error {
	schedules := make(map[string]RentalScheduleSpec, len(sc.World.Rentals))
	for _, rental := range sc.World.Rentals {
		schedules[rental.ID] = RentalScheduleSpec{RentalID: rental.ID}
	}
	for _, schedule := range sc.World.RentalSchedules {
		schedules[schedule.RentalID] = schedule
	}
	bookingIDs, runIDs := scheduleIdentities(schedules)
	requests := map[string]RequestSpec{}
	var elapsed time.Duration
	for i, step := range sc.Steps() {
		if step.Advance != nil {
			elapsed += step.Advance.Duration()
			continue
		}
		if step.Reconcile != "" {
			if expired, ok := expireQueuedBooking(schedules, "run-"+step.Reconcile, sc.World.Start(), sc.World.Start().Add(elapsed)); ok {
				delete(bookingIDs, expired.BookingID)
				delete(runIDs, expired.RunID)
			}
		}
		runName := step.Submit
		request := step.Request
		if step.Submit != "" {
			requests[step.Submit] = *step.Request
		} else if step.Reconcile != "" {
			runName = step.Reconcile
			original := requests[step.Reconcile]
			request = &original
		}
		for rentalID, candidate := range step.Expect.Candidates {
			if candidate.Schedule == nil {
				continue
			}
			if err := validateScheduleEvidence(schedules[rentalID], elapsed, *candidate.Schedule); err != nil {
				return fmt.Errorf("timeline[%d]: candidate %q: %w", i, rentalID, err)
			}
		}
		if step.Expect.Booking == nil {
			continue
		}
		booking := *step.Expect.Booking
		runID := "run-" + runName
		if bookingIDs[booking.BookingID] {
			return fmt.Errorf("timeline[%d]: Booking %q already exists", i, booking.BookingID)
		}
		if runIDs[runID] {
			return fmt.Errorf("timeline[%d]: Run %q already has a nonterminal Booking", i, runID)
		}
		schedule := schedules[booking.RentalID]
		if err := validateBookingDecision(schedule, elapsed, request, booking); err != nil {
			return fmt.Errorf("timeline[%d]: %w", i, err)
		}
		schedule.Version = booking.ScheduleVersion
		if booking.State == BookingRunning {
			schedule.Running = &RunningBookingSpec{
				BookingID:                booking.BookingID,
				RunID:                    runID,
				RemainingMaxRuntime:      *request.MaxRuntime,
				RemainingExpectedRuntime: request.ExpectedRuntime,
			}
		} else {
			schedule.Queued = append(schedule.Queued, QueuedBookingSpec{
				BookingID:       booking.BookingID,
				RunID:           runID,
				MaxRuntime:      *request.MaxRuntime,
				ExpectedRuntime: request.ExpectedRuntime,
				LatestStart:     booking.LatestStart,
			})
		}
		bookingIDs[booking.BookingID] = true
		runIDs[runID] = true
		schedules[booking.RentalID] = schedule
	}
	return nil
}

func scheduleIdentities(schedules map[string]RentalScheduleSpec) (map[string]bool, map[string]bool) {
	bookingIDs := map[string]bool{}
	runIDs := map[string]bool{}
	for _, schedule := range schedules {
		if schedule.Running != nil {
			bookingIDs[schedule.Running.BookingID] = true
			runIDs[schedule.Running.RunID] = true
		}
		for _, booking := range schedule.Queued {
			bookingIDs[booking.BookingID] = true
			runIDs[booking.RunID] = true
		}
	}
	return bookingIDs, runIDs
}

func validateBookingDecision(schedule RentalScheduleSpec, elapsed time.Duration, request *RequestSpec, booking BookingExpectation) error {
	if request == nil || request.MaxRuntime == nil {
		return fmt.Errorf("Booking %q requires its submitted Run's max_runtime", booking.BookingID)
	}
	if want := schedule.Version + 1; booking.ScheduleVersion != want {
		return fmt.Errorf("Booking %q schedule_version is %d, want %d", booking.BookingID, booking.ScheduleVersion, want)
	}
	if booking.State == BookingRunning {
		if schedule.Running != nil {
			return fmt.Errorf("RunningBooking %q requires an empty RentalSchedule", booking.BookingID)
		}
		return nil
	}
	if schedule.Running == nil {
		return fmt.Errorf("QueuedBooking %q requires a RunningBooking", booking.BookingID)
	}
	if len(schedule.Queued) >= MaxQueuedBookings {
		return fmt.Errorf("QueuedBooking %q appends to a full RentalSchedule; at most %d Bookings may wait", booking.BookingID, MaxQueuedBookings)
	}
	if want := schedule.tailBookingID(); booking.AfterBooking != want {
		return fmt.Errorf("QueuedBooking %q follows %q, want current tail %q", booking.BookingID, booking.AfterBooking, want)
	}
	wait := schedule.projectedWait(elapsed)
	if booking.ProjectedStart == nil || booking.ProjectedStart.Duration() != wait {
		return fmt.Errorf("QueuedBooking %q projected_start_in is %v, want %v from preceding expected runtimes", booking.BookingID, durationValue(booking.ProjectedStart), wait)
	}
	return nil
}

func validateScheduleEvidence(schedule RentalScheduleSpec, elapsed time.Duration, expect ScheduleEvidenceExpectation) error {
	if expect.Version != schedule.Version {
		return fmt.Errorf("schedule version is %d, want %d", expect.Version, schedule.Version)
	}
	if schedule.Running == nil || expect.Running == nil ||
		expect.Running.BookingID != schedule.Running.BookingID ||
		expect.Running.RunID != schedule.Running.RunID ||
		expect.Running.RemainingMaxRuntime.Duration() != schedule.runningMaxRemaining(elapsed) ||
		expect.Running.expectedRemaining().Duration() != schedule.runningExpectedRemaining(elapsed) {
		return fmt.Errorf("RunningBooking evidence does not match the current schedule")
	}
	if len(expect.Preceding) != len(schedule.Queued) {
		return fmt.Errorf("preceding has %d Bookings, want %d", len(expect.Preceding), len(schedule.Queued))
	}
	for i, booking := range schedule.Queued {
		actual := expect.Preceding[i]
		if actual.BookingID != booking.BookingID || actual.RunID != booking.RunID ||
			actual.MaxRuntime.Duration() != booking.MaxRuntime.Duration() ||
			actual.expected().Duration() != booking.expected().Duration() {
			return fmt.Errorf("preceding[%d] does not match QueuedBooking %q", i, booking.BookingID)
		}
	}
	if want := schedule.projectedWait(elapsed); expect.ProjectedStart.Duration() != want {
		return fmt.Errorf("projected_start_in is %v, want %v", expect.ProjectedStart.Duration(), want)
	}
	return nil
}

func expireQueuedBooking(schedules map[string]RentalScheduleSpec, runID string, start, now time.Time) (QueuedBookingSpec, bool) {
	for rentalID, schedule := range schedules {
		for i, booking := range schedule.Queued {
			if booking.RunID != runID || booking.LatestStart == nil || booking.LatestStart.Resolve(start).After(now) {
				continue
			}
			schedule.Queued = slices.Delete(schedule.Queued, i, i+1)
			schedule.Version++
			schedules[rentalID] = schedule
			return booking, true
		}
	}
	return QueuedBookingSpec{}, false
}

func (schedule RentalScheduleSpec) tailBookingID() string {
	if len(schedule.Queued) > 0 {
		return schedule.Queued[len(schedule.Queued)-1].BookingID
	}
	return schedule.Running.BookingID
}

// projectedWait is the p50 wait for the next arriving Booking: the running
// Booking's expected remaining runtime plus every waiting Booking's
// expected runtime. Max runtimes stay the enforced ceiling behind
// latest-start guarantees; expectations drive projections and scoring.
func (schedule RentalScheduleSpec) projectedWait(elapsed time.Duration) time.Duration {
	wait := schedule.runningExpectedRemaining(elapsed)
	for _, booking := range schedule.Queued {
		wait += booking.expected().Duration()
	}
	return wait
}

func (schedule RentalScheduleSpec) runningMaxRemaining(elapsed time.Duration) time.Duration {
	if schedule.Running == nil {
		return 0
	}
	return max(0, schedule.Running.RemainingMaxRuntime.Duration()-elapsed)
}

func (schedule RentalScheduleSpec) runningExpectedRemaining(elapsed time.Duration) time.Duration {
	if schedule.Running == nil {
		return 0
	}
	return max(0, schedule.Running.expectedRemaining().Duration()-elapsed)
}

func durationValue(value *Duration) time.Duration {
	if value == nil {
		return 0
	}
	return value.Duration()
}

func (step StepSpec) validate(submitted map[string]bool) error {
	actions := 0
	if step.Submit != "" {
		actions++
	}
	if step.Advance != nil {
		actions++
	}
	if step.Reconcile != "" {
		actions++
	}
	if actions != 1 {
		return fmt.Errorf("each step is exactly one of submit, advance, or reconcile")
	}
	switch {
	case step.Submit != "":
		if step.Request == nil || step.Expect == nil {
			return fmt.Errorf("submit %q requires a request and an expect", step.Submit)
		}
		if submitted[step.Submit] {
			return fmt.Errorf("run %q submitted twice", step.Submit)
		}
		submitted[step.Submit] = true
	case step.Advance != nil:
		if step.Request != nil || step.Expect != nil {
			return fmt.Errorf("advance carries no request or expect")
		}
	case step.Reconcile != "":
		if step.Expect == nil {
			return fmt.Errorf("reconcile %q requires an expect", step.Reconcile)
		}
		if step.Request != nil {
			return fmt.Errorf("reconcile carries no request")
		}
		if !submitted[step.Reconcile] {
			return fmt.Errorf("reconcile %q references a run never submitted", step.Reconcile)
		}
	}
	return nil
}

func (w WorldSpec) validate() error {
	for ref, image := range w.Images {
		if !ociImageRefPattern.MatchString(ref) {
			return fmt.Errorf("image %q must be digest-pinned", ref)
		}
		if len(image.Layers) == 0 {
			return fmt.Errorf("image %q needs at least one layer", ref)
		}
		for _, layer := range image.Layers {
			if !ociDigestPattern.MatchString(layer.Digest) || layer.Size <= 0 {
				return fmt.Errorf("image %q: layers need an exact sha256 digest and a positive size", ref)
			}
		}
	}
	artifactIDs := map[string]bool{}
	for _, artifact := range w.Artifacts {
		if artifact.ID == "" || artifact.Size <= 0 {
			return fmt.Errorf("Artifacts need an id and a positive size")
		}
		if artifactIDs[artifact.ID] {
			return fmt.Errorf("duplicate Artifact %q", artifact.ID)
		}
		artifactIDs[artifact.ID] = true
	}
	layerDigests := w.layerDigests()
	ids := map[string]bool{}
	for _, rental := range w.Rentals {
		if rental.ID == "" {
			return fmt.Errorf("rentals need an id")
		}
		if ids[rental.ID] {
			return fmt.Errorf("duplicate id %q", rental.ID)
		}
		ids[rental.ID] = true
		for _, ref := range rental.CachedImages {
			if _, ok := w.Images[ref]; !ok {
				return fmt.Errorf("rental %q caches undefined image %q", rental.ID, ref)
			}
		}
		for _, digest := range rental.CachedLayers {
			if !layerDigests[digest] {
				return fmt.Errorf("rental %q caches undefined layer %q", rental.ID, digest)
			}
		}
		for _, artifactID := range rental.ArtifactReplicas {
			if !artifactIDs[artifactID] {
				return fmt.Errorf("rental %q holds undefined Artifact %q", rental.ID, artifactID)
			}
		}
		cacheMounts := map[string]bool{}
		for _, name := range rental.CacheMounts {
			if name == "" {
				return fmt.Errorf("rental %q has a Cache Mount without a name", rental.ID)
			}
			if cacheMounts[name] {
				return fmt.Errorf("rental %q has duplicate Cache Mount %q", rental.ID, name)
			}
			cacheMounts[name] = true
		}
		if rental.RatePerHourUSD <= 0 {
			return fmt.Errorf("rental %q needs a positive rate_per_hour_usd", rental.ID)
		}
	}
	rentalsWithSchedules := map[string]bool{}
	bookingOwners := map[string]string{}
	runOwners := map[string]string{}
	for _, schedule := range w.RentalSchedules {
		if !ids[schedule.RentalID] {
			return fmt.Errorf("RentalSchedule references unknown Rental %q", schedule.RentalID)
		}
		if rentalsWithSchedules[schedule.RentalID] {
			return fmt.Errorf("Rental %q has more than one RentalSchedule", schedule.RentalID)
		}
		rentalsWithSchedules[schedule.RentalID] = true
		if err := schedule.validate(w.Start()); err != nil {
			return err
		}
		if err := validateScheduleOwnership(schedule, bookingOwners, runOwners); err != nil {
			return err
		}
		if schedule.Running != nil && w.rental(schedule.RentalID).IdleLeaseExpiresIn != nil {
			return fmt.Errorf("rental %q: only an empty RentalSchedule may carry an idle lease", schedule.RentalID)
		}
	}
	for _, offer := range w.Marketplace {
		if offer.ID == "" {
			return fmt.Errorf("marketplace offers need an id")
		}
		if ids[offer.ID] {
			return fmt.Errorf("duplicate id %q", offer.ID)
		}
		ids[offer.ID] = true
		if offer.RatePerHourUSD <= 0 {
			return fmt.Errorf("marketplace offer %q needs a positive rate_per_hour_usd", offer.ID)
		}
		if offer.Provisioning.Expected.Duration() <= 0 {
			return fmt.Errorf("marketplace offer %q needs a provisioning estimate", offer.ID)
		}
	}
	return nil
}

func validateScheduleOwnership(schedule RentalScheduleSpec, bookingOwners, runOwners map[string]string) error {
	check := func(bookingID, runID string) error {
		if owner := bookingOwners[bookingID]; owner != "" {
			return fmt.Errorf("Booking %q belongs to both Rental %q and Rental %q", bookingID, owner, schedule.RentalID)
		}
		if owner := runOwners[runID]; owner != "" {
			return fmt.Errorf("Run %q has nonterminal Bookings on both Rental %q and Rental %q", runID, owner, schedule.RentalID)
		}
		bookingOwners[bookingID] = schedule.RentalID
		runOwners[runID] = schedule.RentalID
		return nil
	}
	if schedule.Running != nil {
		if err := check(schedule.Running.BookingID, schedule.Running.RunID); err != nil {
			return err
		}
	}
	for _, booking := range schedule.Queued {
		if err := check(booking.BookingID, booking.RunID); err != nil {
			return err
		}
	}
	return nil
}

func (schedule RentalScheduleSpec) validate(start time.Time) error {
	rentalID := schedule.RentalID
	if schedule.Running == nil && len(schedule.Queued) > 0 {
		return fmt.Errorf("rental %q: QueuedBookings require a RunningBooking", rentalID)
	}
	if schedule.Running == nil {
		if schedule.Version != 0 {
			return fmt.Errorf("rental %q: an empty RentalSchedule omits its version", rentalID)
		}
		return nil
	}
	if schedule.Version == 0 {
		return fmt.Errorf("rental %q: a nonempty RentalSchedule needs a positive version", rentalID)
	}
	ids := map[string]bool{}
	runs := map[string]bool{}
	if err := validateBookingIdentity(rentalID, schedule.Running.BookingID, schedule.Running.RunID, ids, runs); err != nil {
		return err
	}
	if schedule.Running.RemainingMaxRuntime.Duration() <= 0 {
		return fmt.Errorf("rental %q: RunningBooking %q needs a positive remaining_max_runtime", rentalID, schedule.Running.BookingID)
	}
	if expected := schedule.Running.RemainingExpectedRuntime; expected != nil &&
		(expected.Duration() <= 0 || expected.Duration() > schedule.Running.RemainingMaxRuntime.Duration()) {
		return fmt.Errorf("rental %q: RunningBooking %q remaining_expected_runtime must be positive and within the max bound", rentalID, schedule.Running.BookingID)
	}
	if completes := schedule.Running.CompletesAfter; completes != nil && completes.Duration() <= 0 {
		return fmt.Errorf("rental %q: RunningBooking %q needs a positive completes_after", rentalID, schedule.Running.BookingID)
	}
	if len(schedule.Queued) > MaxQueuedBookings {
		return fmt.Errorf("rental %q: at most %d QueuedBookings may wait, got %d", rentalID, MaxQueuedBookings, len(schedule.Queued))
	}
	for _, booking := range schedule.Queued {
		if err := validateBookingIdentity(rentalID, booking.BookingID, booking.RunID, ids, runs); err != nil {
			return err
		}
		if booking.MaxRuntime.Duration() <= 0 {
			return fmt.Errorf("rental %q: QueuedBooking %q needs a positive max_runtime", rentalID, booking.BookingID)
		}
		if expected := booking.ExpectedRuntime; expected != nil &&
			(expected.Duration() <= 0 || expected.Duration() > booking.MaxRuntime.Duration()) {
			return fmt.Errorf("rental %q: QueuedBooking %q expected_runtime must be positive and within the max bound", rentalID, booking.BookingID)
		}
		if booking.LatestStart != nil && !booking.LatestStart.Resolve(start).After(start) {
			return fmt.Errorf("rental %q: QueuedBooking %q latest_start must be after the world start", rentalID, booking.BookingID)
		}
	}
	return nil
}

func (w WorldSpec) rental(id string) RentalSpec {
	for _, rental := range w.Rentals {
		if rental.ID == id {
			return rental
		}
	}
	return RentalSpec{}
}

func (w WorldSpec) rentalSchedule(rentalID string) RentalScheduleSpec {
	for _, schedule := range w.RentalSchedules {
		if schedule.RentalID == rentalID {
			return schedule
		}
	}
	return RentalScheduleSpec{RentalID: rentalID}
}

func validateBookingIdentity(rentalID, bookingID, runID string, bookingIDs, runIDs map[string]bool) error {
	if bookingID == "" || runID == "" {
		return fmt.Errorf("rental %q: Bookings need stable booking and run IDs", rentalID)
	}
	if bookingIDs[bookingID] {
		return fmt.Errorf("rental %q: duplicate Booking %q", rentalID, bookingID)
	}
	if runIDs[runID] {
		return fmt.Errorf("rental %q: Run %q appears in more than one Booking", rentalID, runID)
	}
	bookingIDs[bookingID] = true
	runIDs[runID] = true
	return nil
}

func (w WorldSpec) layerDigests() map[string]bool {
	digests := map[string]bool{}
	for _, image := range w.Images {
		for _, layer := range image.Layers {
			digests[layer.Digest] = true
		}
	}
	return digests
}

func (w WorldSpec) candidateIDs() map[string]bool {
	ids := map[string]bool{}
	for _, rental := range w.Rentals {
		ids[rental.ID] = true
	}
	for _, offer := range w.Marketplace {
		ids[offer.ID] = true
	}
	return ids
}

func (w WorldSpec) validRequest(req RequestSpec) error {
	if req.Image == "" {
		return fmt.Errorf("requests need an image")
	}
	if len(w.Images) > 0 {
		if _, ok := w.Images[req.Image]; !ok {
			return fmt.Errorf("request image %q is not defined in the world", req.Image)
		}
	}
	artifactIDs := w.artifactIDs()
	for _, artifactID := range append(slices.Clone(req.ConsumesArtifacts), req.ProducesArtifacts...) {
		if !artifactIDs[artifactID] {
			return fmt.Errorf("request references undefined Artifact %q", artifactID)
		}
	}
	cacheMounts := map[string]bool{}
	for _, mount := range req.CacheMounts {
		if mount.Name == "" {
			return fmt.Errorf("Cache Mounts need a name")
		}
		if cacheMounts[mount.Name] {
			return fmt.Errorf("request has duplicate Cache Mount %q", mount.Name)
		}
		cacheMounts[mount.Name] = true
	}
	return nil
}

func (w WorldSpec) validExpect(expect ExpectSpec) error {
	ids := w.candidateIDs()
	switch expect.Outcome {
	case OutcomePlace:
		if expect.Offer == "" {
			return fmt.Errorf("outcome \"place\" names the winning offer")
		}
		if !ids[expect.Offer] {
			return fmt.Errorf("expected offer %q is not in the world", expect.Offer)
		}
	case OutcomeFail:
		if expect.Offer != "" || expect.Booking != nil {
			return fmt.Errorf("outcome \"fail\" selects no offer and creates no Booking")
		}
	default:
		return fmt.Errorf("outcome must be \"place\" or \"fail\", got %q", expect.Outcome)
	}
	if booking := expect.Booking; booking != nil {
		if booking.BookingID == "" || booking.RentalID == "" || booking.ScheduleVersion == 0 {
			return fmt.Errorf("expected Booking needs id, rental, and a positive schedule_version")
		}
		if !slices.ContainsFunc(w.Rentals, func(r RentalSpec) bool { return r.ID == booking.RentalID }) {
			return fmt.Errorf("expected Booking Rental %q is not in the world", booking.RentalID)
		}
		if expect.Offer != booking.RentalID {
			return fmt.Errorf("expected Booking Rental %q must be the winning offer", booking.RentalID)
		}
		switch booking.State {
		case BookingRunning:
			if booking.AfterBooking != "" || booking.ProjectedStart != nil {
				return fmt.Errorf("a running Booking has no predecessor or projected_start_in")
			}
		case BookingQueued:
			if booking.AfterBooking == "" || booking.ProjectedStart == nil {
				return fmt.Errorf("a QueuedBooking needs after and projected_start_in")
			}
		default:
			return fmt.Errorf("Booking state must be \"running\" or \"queued\", got %q", booking.State)
		}
	}
	if expect.Disposition != "" && expect.Disposition != "release" && expect.Disposition != "terminate" {
		return fmt.Errorf("disposition must be \"release\" or \"terminate\", got %q", expect.Disposition)
	}
	for id, candidate := range expect.Candidates {
		if !ids[id] {
			return fmt.Errorf("candidate %q is not in the world", id)
		}
		for artifactID, want := range candidate.Artifacts {
			if !w.artifactIDs()[artifactID] {
				return fmt.Errorf("candidate %q references undefined Artifact %q", id, artifactID)
			}
			if want != "hit" && want != "miss" {
				return fmt.Errorf("candidate %q Artifact %q expects \"hit\" or \"miss\", got %q", id, artifactID, want)
			}
		}
		if candidate.Schedule != nil {
			if !slices.ContainsFunc(w.Rentals, func(r RentalSpec) bool { return r.ID == id }) {
				return fmt.Errorf("candidate %q is not a Rental and cannot carry RentalSchedule evidence", id)
			}
			if candidate.Schedule.Version == 0 || candidate.Schedule.Running == nil {
				return fmt.Errorf("candidate %q schedule evidence needs a version and RunningBooking", id)
			}
		}
	}
	return nil
}

func (w WorldSpec) artifactIDs() map[string]bool {
	ids := make(map[string]bool, len(w.Artifacts))
	for _, artifact := range w.Artifacts {
		ids[artifact.ID] = true
	}
	return ids
}
