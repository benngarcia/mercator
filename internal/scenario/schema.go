// Package scenario is the placement-scenario harness: a corpus of
// fixture-defined worlds (rentals with running work, cached image layers and
// named data caches, marketplace offers) plus incoming run requests, asserted
// against the placement decisions Mercator records in its event log.
//
// One scenario contract, two backends: decision correctness runs against
// simulated capacity (the fake adapter's World); mechanism correctness against
// real daemons is a later backend behind the same Session seam.
//
// Scenarios carry a status. Green scenarios assert current behavior and fail
// CI on regression. Target scenarios encode the contract of unbuilt semantics
// (Rental schedules, cache evidence, host facts); the runner reports their failures as
// pending-red, and a target scenario that starts passing must be promoted.
package scenario

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
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

type PlacementState string

const (
	PlacementRunning   PlacementState = "running"
	PlacementScheduled PlacementState = "scheduled"
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
	// time: dispatching the next Placement, expiring one past its latest
	// start, and re-placing its Run.
	CapabilityScheduleAdvancement Capability = "schedule_advancement"
	// CapabilityCacheEvidence is named-cache hit/miss evidence recorded and
	// scored per candidate.
	CapabilityCacheEvidence Capability = "cache_evidence"
	// CapabilityCacheMounts is the container spec carrying content-keyed
	// cache mount declarations.
	CapabilityCacheMounts Capability = "cache_mounts"
	// CapabilityHostFacts is providers advertising SSH and NVIDIA-driver
	// facts on offers, rejected loudly when absent or false.
	CapabilityHostFacts Capability = "host_facts"
)

var knownCapabilities = map[Capability]bool{
	CapabilityRentalSchedule:      true,
	CapabilityScheduleAdvancement: true,
	CapabilityCacheEvidence:       true,
	CapabilityCacheMounts:         true,
	CapabilityHostFacts:           true,
}

// MaxScheduledPlacements bounds every RentalSchedule: at most this many
// ScheduledPlacements may wait behind the RunningPlacement. A Run arriving at
// a full schedule must go elsewhere, whatever the score says.
const MaxScheduledPlacements = 4

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
	Rentals         []RentalSpec           `json:"rentals,omitempty"`
	RentalSchedules []RentalScheduleSpec   `json:"rental_schedules,omitempty"`
	Marketplace     []MarketplaceOfferSpec `json:"marketplace,omitempty"`
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

// LayerSpec names one image layer. Layer names are content identity: two
// images listing the same layer name share that layer.
type LayerSpec struct {
	Name string   `json:"name"`
	Size ByteSize `json:"size"`
}

// RentalSpec is reusable machine capacity the broker owns. Its schedule is
// broker state; the machine itself receives only the running Placement through
// its standard Docker endpoint.
type RentalSpec struct {
	ID string `json:"id"`
	// IdleLeaseExpiresIn bounds how long the rental survives idle, measured
	// from the world clock's start. Omitted means the lease outlives the
	// scenario.
	IdleLeaseExpiresIn *Duration `json:"idle_lease_expires_in,omitempty"`
	// CachedImages holds every layer of the named images; CachedLayers adds
	// individual layers (for a rental warm from a previous image version).
	CachedImages []string `json:"cached_images,omitempty"`
	CachedLayers []string `json:"cached_layers,omitempty"`
	// NamedCaches maps content-keyed data cache keys (e.g. a dataset GID) to
	// the bytes materialized on the rental's local disk.
	NamedCaches    map[string]ByteSize `json:"named_caches,omitempty"`
	RatePerHourUSD float64             `json:"rate_per_hour_usd"`
	Resources      *ResourcesSpec      `json:"resources,omitempty"`
}

// RentalScheduleSpec is Mercator's ordered sequence of nonterminal Placements
// assigned to one Rental. At most one Placement runs; any number may wait.
type RentalScheduleSpec struct {
	RentalID  string                   `json:"rental"`
	Version   uint64                   `json:"version,omitempty"`
	Running   *RunningPlacementSpec    `json:"running,omitempty"`
	Scheduled []ScheduledPlacementSpec `json:"scheduled,omitempty"`
}

type RunningPlacementSpec struct {
	PlacementID string `json:"placement"`
	RunID       string `json:"run"`
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

func (p RunningPlacementSpec) expectedRemaining() Duration {
	if p.RemainingExpectedRuntime != nil {
		return *p.RemainingExpectedRuntime
	}
	return p.RemainingMaxRuntime
}

type ScheduledPlacementSpec struct {
	PlacementID string   `json:"placement"`
	RunID       string   `json:"run"`
	MaxRuntime  Duration `json:"max_runtime"`
	// ExpectedRuntime is the p50 runtime used for projected starts and
	// queue-delay scoring. Defaults to the max bound.
	ExpectedRuntime *Duration `json:"expected_runtime,omitempty"`
	// LatestStart is the last acceptable start time for this Placement. When it
	// expires, Mercator removes the Placement and re-evaluates its Run.
	LatestStart *Moment `json:"latest_start,omitempty"`
}

func (p ScheduledPlacementSpec) expected() Duration {
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
	// CacheMounts declare the content-keyed data caches the run wants
	// materialized, scored as avoided transfer bytes when a rental already
	// holds them. Target ontology: the container spec cannot carry these yet.
	CacheMounts []CacheMountSpec `json:"cache_mounts,omitempty"`
}

type CacheMountSpec struct {
	Name string   `json:"name"`
	Key  string   `json:"key"`
	Size ByteSize `json:"size"`
}

type ExpectSpec struct {
	// Outcome is the decision the event log must record: "place" (a selected
	// offer) or "fail" (a recorded decision with no feasible offers). Selecting
	// a busy Rental creates the Placement described by Placement.
	Outcome   Outcome               `json:"outcome"`
	Offer     string                `json:"offer,omitempty"`
	Reasons   []string              `json:"reasons,omitempty"`
	Placement *PlacementExpectation `json:"placement,omitempty"`
	// Disposition asserts the recorded cleanup intent on the launch intent:
	// "release" for standing rentals, "terminate" for provisioned hosts.
	Disposition string `json:"disposition,omitempty"`
	// Candidates assert the per-candidate evidence the decision weighed,
	// keyed by rental or marketplace offer ID.
	Candidates map[string]CandidateExpectation `json:"candidates,omitempty"`
}

type PlacementExpectation struct {
	PlacementID     string         `json:"id"`
	RentalID        string         `json:"rental"`
	State           PlacementState `json:"state"`
	AfterPlacement  string         `json:"after,omitempty"`
	ProjectedStart  *Duration      `json:"projected_start_in,omitempty"`
	LatestStart     *Moment        `json:"latest_start,omitempty"`
	ScheduleVersion uint64         `json:"schedule_version"`
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
	// Caches asserts recorded named-cache evidence, key to "hit" or "miss".
	// Target ontology: decisions record no cache evidence yet.
	Caches map[string]string `json:"caches,omitempty"`
}

type ScheduleEvidenceExpectation struct {
	Version        uint64                       `json:"version"`
	Running        *RunningPlacementEvidence    `json:"running,omitempty"`
	Preceding      []ScheduledPlacementEvidence `json:"preceding,omitempty"`
	ProjectedStart Duration                     `json:"projected_start_in"`
}

type RunningPlacementEvidence struct {
	PlacementID         string   `json:"placement"`
	RunID               string   `json:"run"`
	RemainingMaxRuntime Duration `json:"remaining_max_runtime"`
	// RemainingExpectedRuntime is the recorded p50; defaults to the max bound.
	RemainingExpectedRuntime *Duration `json:"remaining_expected_runtime,omitempty"`
}

func (e RunningPlacementEvidence) expectedRemaining() Duration {
	if e.RemainingExpectedRuntime != nil {
		return *e.RemainingExpectedRuntime
	}
	return e.RemainingMaxRuntime
}

type ScheduledPlacementEvidence struct {
	PlacementID string   `json:"placement"`
	RunID       string   `json:"run"`
	MaxRuntime  Duration `json:"max_runtime"`
	// ExpectedRuntime is the recorded p50; defaults to the max bound.
	ExpectedRuntime *Duration `json:"expected_runtime,omitempty"`
}

func (e ScheduledPlacementEvidence) expected() Duration {
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

// Load reads and validates one scenario fixture. The scenario's name is the
// file's base name without extension.
func Load(path string) (Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	var sc Scenario
	if err := strictUnmarshal(data, &sc); err != nil {
		return Scenario{}, fmt.Errorf("%s: %w", path, err)
	}
	sc.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if err := sc.validate(); err != nil {
		return Scenario{}, fmt.Errorf("%s: %w", path, err)
	}
	return sc, nil
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
	if sc.Status != StatusGreen && sc.Status != StatusTarget {
		return fmt.Errorf("status must be %q or %q, got %q", StatusGreen, StatusTarget, sc.Status)
	}
	if sc.Status == StatusTarget && len(sc.MissingCapabilities) == 0 {
		return fmt.Errorf("target scenarios declare the missing_capabilities their promotion waits on")
	}
	if sc.Status == StatusGreen && len(sc.MissingCapabilities) > 0 {
		return fmt.Errorf("green scenarios declare no missing_capabilities")
	}
	for _, capability := range sc.MissingCapabilities {
		if !knownCapabilities[capability] {
			return fmt.Errorf("unknown capability %q", capability)
		}
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
	placementIDs, runIDs := scheduleIdentities(schedules)
	requests := map[string]RequestSpec{}
	var elapsed time.Duration
	for i, step := range sc.Steps() {
		if step.Advance != nil {
			elapsed += step.Advance.Duration()
			continue
		}
		if step.Reconcile != "" {
			if expired, ok := expireScheduledPlacement(schedules, "run-"+step.Reconcile, sc.World.Start(), sc.World.Start().Add(elapsed)); ok {
				delete(placementIDs, expired.PlacementID)
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
		if step.Expect.Placement == nil {
			continue
		}
		placement := *step.Expect.Placement
		runID := "run-" + runName
		if placementIDs[placement.PlacementID] {
			return fmt.Errorf("timeline[%d]: Placement %q already exists", i, placement.PlacementID)
		}
		if runIDs[runID] {
			return fmt.Errorf("timeline[%d]: Run %q already has a nonterminal Placement", i, runID)
		}
		schedule := schedules[placement.RentalID]
		if err := validatePlacementDecision(schedule, elapsed, request, placement); err != nil {
			return fmt.Errorf("timeline[%d]: %w", i, err)
		}
		schedule.Version = placement.ScheduleVersion
		if placement.State == PlacementRunning {
			schedule.Running = &RunningPlacementSpec{
				PlacementID:              placement.PlacementID,
				RunID:                    runID,
				RemainingMaxRuntime:      *request.MaxRuntime,
				RemainingExpectedRuntime: request.ExpectedRuntime,
			}
		} else {
			schedule.Scheduled = append(schedule.Scheduled, ScheduledPlacementSpec{
				PlacementID:     placement.PlacementID,
				RunID:           runID,
				MaxRuntime:      *request.MaxRuntime,
				ExpectedRuntime: request.ExpectedRuntime,
				LatestStart:     placement.LatestStart,
			})
		}
		placementIDs[placement.PlacementID] = true
		runIDs[runID] = true
		schedules[placement.RentalID] = schedule
	}
	return nil
}

func scheduleIdentities(schedules map[string]RentalScheduleSpec) (map[string]bool, map[string]bool) {
	placementIDs := map[string]bool{}
	runIDs := map[string]bool{}
	for _, schedule := range schedules {
		if schedule.Running != nil {
			placementIDs[schedule.Running.PlacementID] = true
			runIDs[schedule.Running.RunID] = true
		}
		for _, placement := range schedule.Scheduled {
			placementIDs[placement.PlacementID] = true
			runIDs[placement.RunID] = true
		}
	}
	return placementIDs, runIDs
}

func validatePlacementDecision(schedule RentalScheduleSpec, elapsed time.Duration, request *RequestSpec, placement PlacementExpectation) error {
	if request == nil || request.MaxRuntime == nil {
		return fmt.Errorf("Placement %q requires its submitted Run's max_runtime", placement.PlacementID)
	}
	if want := schedule.Version + 1; placement.ScheduleVersion != want {
		return fmt.Errorf("Placement %q schedule_version is %d, want %d", placement.PlacementID, placement.ScheduleVersion, want)
	}
	if placement.State == PlacementRunning {
		if schedule.Running != nil {
			return fmt.Errorf("RunningPlacement %q requires an empty RentalSchedule", placement.PlacementID)
		}
		return nil
	}
	if schedule.Running == nil {
		return fmt.Errorf("ScheduledPlacement %q requires a RunningPlacement", placement.PlacementID)
	}
	if len(schedule.Scheduled) >= MaxScheduledPlacements {
		return fmt.Errorf("ScheduledPlacement %q appends to a full RentalSchedule; at most %d Placements may wait", placement.PlacementID, MaxScheduledPlacements)
	}
	if want := schedule.tailPlacementID(); placement.AfterPlacement != want {
		return fmt.Errorf("ScheduledPlacement %q follows %q, want current tail %q", placement.PlacementID, placement.AfterPlacement, want)
	}
	wait := schedule.projectedWait(elapsed)
	if placement.ProjectedStart == nil || placement.ProjectedStart.Duration() != wait {
		return fmt.Errorf("ScheduledPlacement %q projected_start_in is %v, want %v from preceding expected runtimes", placement.PlacementID, durationValue(placement.ProjectedStart), wait)
	}
	return nil
}

func validateScheduleEvidence(schedule RentalScheduleSpec, elapsed time.Duration, expect ScheduleEvidenceExpectation) error {
	if expect.Version != schedule.Version {
		return fmt.Errorf("schedule version is %d, want %d", expect.Version, schedule.Version)
	}
	if schedule.Running == nil || expect.Running == nil ||
		expect.Running.PlacementID != schedule.Running.PlacementID ||
		expect.Running.RunID != schedule.Running.RunID ||
		expect.Running.RemainingMaxRuntime.Duration() != schedule.runningMaxRemaining(elapsed) ||
		expect.Running.expectedRemaining().Duration() != schedule.runningExpectedRemaining(elapsed) {
		return fmt.Errorf("RunningPlacement evidence does not match the current schedule")
	}
	if len(expect.Preceding) != len(schedule.Scheduled) {
		return fmt.Errorf("preceding has %d Placements, want %d", len(expect.Preceding), len(schedule.Scheduled))
	}
	for i, placement := range schedule.Scheduled {
		actual := expect.Preceding[i]
		if actual.PlacementID != placement.PlacementID || actual.RunID != placement.RunID ||
			actual.MaxRuntime.Duration() != placement.MaxRuntime.Duration() ||
			actual.expected().Duration() != placement.expected().Duration() {
			return fmt.Errorf("preceding[%d] does not match ScheduledPlacement %q", i, placement.PlacementID)
		}
	}
	if want := schedule.projectedWait(elapsed); expect.ProjectedStart.Duration() != want {
		return fmt.Errorf("projected_start_in is %v, want %v", expect.ProjectedStart.Duration(), want)
	}
	return nil
}

func expireScheduledPlacement(schedules map[string]RentalScheduleSpec, runID string, start, now time.Time) (ScheduledPlacementSpec, bool) {
	for rentalID, schedule := range schedules {
		for i, placement := range schedule.Scheduled {
			if placement.RunID != runID || placement.LatestStart == nil || placement.LatestStart.Resolve(start).After(now) {
				continue
			}
			schedule.Scheduled = slices.Delete(schedule.Scheduled, i, i+1)
			schedule.Version++
			schedules[rentalID] = schedule
			return placement, true
		}
	}
	return ScheduledPlacementSpec{}, false
}

func (schedule RentalScheduleSpec) tailPlacementID() string {
	if len(schedule.Scheduled) > 0 {
		return schedule.Scheduled[len(schedule.Scheduled)-1].PlacementID
	}
	return schedule.Running.PlacementID
}

// projectedWait is the p50 wait for the next arriving Placement: the running
// Placement's expected remaining runtime plus every waiting Placement's
// expected runtime. Max runtimes stay the enforced ceiling behind
// latest-start guarantees; expectations drive projections and scoring.
func (schedule RentalScheduleSpec) projectedWait(elapsed time.Duration) time.Duration {
	wait := schedule.runningExpectedRemaining(elapsed)
	for _, placement := range schedule.Scheduled {
		wait += placement.expected().Duration()
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
		if len(image.Layers) == 0 {
			return fmt.Errorf("image %q needs at least one layer", ref)
		}
		for _, layer := range image.Layers {
			if layer.Name == "" || layer.Size <= 0 {
				return fmt.Errorf("image %q: layers need a name and a positive size", ref)
			}
		}
	}
	layerNames := w.layerNames()
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
		for _, name := range rental.CachedLayers {
			if !layerNames[name] {
				return fmt.Errorf("rental %q caches undefined layer %q", rental.ID, name)
			}
		}
		if rental.RatePerHourUSD <= 0 {
			return fmt.Errorf("rental %q needs a positive rate_per_hour_usd", rental.ID)
		}
	}
	scheduledRentals := map[string]bool{}
	placementOwners := map[string]string{}
	runOwners := map[string]string{}
	for _, schedule := range w.RentalSchedules {
		if !ids[schedule.RentalID] {
			return fmt.Errorf("RentalSchedule references unknown Rental %q", schedule.RentalID)
		}
		if scheduledRentals[schedule.RentalID] {
			return fmt.Errorf("Rental %q has more than one RentalSchedule", schedule.RentalID)
		}
		scheduledRentals[schedule.RentalID] = true
		if err := schedule.validate(w.Start()); err != nil {
			return err
		}
		if err := validateScheduleOwnership(schedule, placementOwners, runOwners); err != nil {
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

func validateScheduleOwnership(schedule RentalScheduleSpec, placementOwners, runOwners map[string]string) error {
	check := func(placementID, runID string) error {
		if owner := placementOwners[placementID]; owner != "" {
			return fmt.Errorf("Placement %q belongs to both Rental %q and Rental %q", placementID, owner, schedule.RentalID)
		}
		if owner := runOwners[runID]; owner != "" {
			return fmt.Errorf("Run %q has nonterminal Placements on both Rental %q and Rental %q", runID, owner, schedule.RentalID)
		}
		placementOwners[placementID] = schedule.RentalID
		runOwners[runID] = schedule.RentalID
		return nil
	}
	if schedule.Running != nil {
		if err := check(schedule.Running.PlacementID, schedule.Running.RunID); err != nil {
			return err
		}
	}
	for _, placement := range schedule.Scheduled {
		if err := check(placement.PlacementID, placement.RunID); err != nil {
			return err
		}
	}
	return nil
}

func (schedule RentalScheduleSpec) validate(start time.Time) error {
	rentalID := schedule.RentalID
	if schedule.Running == nil && len(schedule.Scheduled) > 0 {
		return fmt.Errorf("rental %q: ScheduledPlacements require a RunningPlacement", rentalID)
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
	if err := validatePlacementIdentity(rentalID, schedule.Running.PlacementID, schedule.Running.RunID, ids, runs); err != nil {
		return err
	}
	if schedule.Running.RemainingMaxRuntime.Duration() <= 0 {
		return fmt.Errorf("rental %q: RunningPlacement %q needs a positive remaining_max_runtime", rentalID, schedule.Running.PlacementID)
	}
	if expected := schedule.Running.RemainingExpectedRuntime; expected != nil &&
		(expected.Duration() <= 0 || expected.Duration() > schedule.Running.RemainingMaxRuntime.Duration()) {
		return fmt.Errorf("rental %q: RunningPlacement %q remaining_expected_runtime must be positive and within the max bound", rentalID, schedule.Running.PlacementID)
	}
	if completes := schedule.Running.CompletesAfter; completes != nil && completes.Duration() <= 0 {
		return fmt.Errorf("rental %q: RunningPlacement %q needs a positive completes_after", rentalID, schedule.Running.PlacementID)
	}
	if len(schedule.Scheduled) > MaxScheduledPlacements {
		return fmt.Errorf("rental %q: at most %d ScheduledPlacements may wait, got %d", rentalID, MaxScheduledPlacements, len(schedule.Scheduled))
	}
	for _, placement := range schedule.Scheduled {
		if err := validatePlacementIdentity(rentalID, placement.PlacementID, placement.RunID, ids, runs); err != nil {
			return err
		}
		if placement.MaxRuntime.Duration() <= 0 {
			return fmt.Errorf("rental %q: ScheduledPlacement %q needs a positive max_runtime", rentalID, placement.PlacementID)
		}
		if expected := placement.ExpectedRuntime; expected != nil &&
			(expected.Duration() <= 0 || expected.Duration() > placement.MaxRuntime.Duration()) {
			return fmt.Errorf("rental %q: ScheduledPlacement %q expected_runtime must be positive and within the max bound", rentalID, placement.PlacementID)
		}
		if placement.LatestStart != nil && !placement.LatestStart.Resolve(start).After(start) {
			return fmt.Errorf("rental %q: ScheduledPlacement %q latest_start must be after the world start", rentalID, placement.PlacementID)
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

func validatePlacementIdentity(rentalID, placementID, runID string, placementIDs, runIDs map[string]bool) error {
	if placementID == "" || runID == "" {
		return fmt.Errorf("rental %q: Placements need stable placement and run IDs", rentalID)
	}
	if placementIDs[placementID] {
		return fmt.Errorf("rental %q: duplicate Placement %q", rentalID, placementID)
	}
	if runIDs[runID] {
		return fmt.Errorf("rental %q: Run %q appears in more than one Placement", rentalID, runID)
	}
	placementIDs[placementID] = true
	runIDs[runID] = true
	return nil
}

func (w WorldSpec) layerNames() map[string]bool {
	names := map[string]bool{}
	for _, image := range w.Images {
		for _, layer := range image.Layers {
			names[layer.Name] = true
		}
	}
	return names
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
		if expect.Offer != "" || expect.Placement != nil {
			return fmt.Errorf("outcome \"fail\" selects no offer and creates no Placement")
		}
	default:
		return fmt.Errorf("outcome must be \"place\" or \"fail\", got %q", expect.Outcome)
	}
	if placement := expect.Placement; placement != nil {
		if placement.PlacementID == "" || placement.RentalID == "" || placement.ScheduleVersion == 0 {
			return fmt.Errorf("expected Placement needs id, rental, and a positive schedule_version")
		}
		if !slices.ContainsFunc(w.Rentals, func(r RentalSpec) bool { return r.ID == placement.RentalID }) {
			return fmt.Errorf("expected Placement Rental %q is not in the world", placement.RentalID)
		}
		if expect.Offer != placement.RentalID {
			return fmt.Errorf("expected Placement Rental %q must be the winning offer", placement.RentalID)
		}
		switch placement.State {
		case PlacementRunning:
			if placement.AfterPlacement != "" || placement.ProjectedStart != nil {
				return fmt.Errorf("a running Placement has no predecessor or projected_start_in")
			}
		case PlacementScheduled:
			if placement.AfterPlacement == "" || placement.ProjectedStart == nil {
				return fmt.Errorf("a ScheduledPlacement needs after and projected_start_in")
			}
		default:
			return fmt.Errorf("Placement state must be \"running\" or \"scheduled\", got %q", placement.State)
		}
	}
	if expect.Disposition != "" && expect.Disposition != "release" && expect.Disposition != "terminate" {
		return fmt.Errorf("disposition must be \"release\" or \"terminate\", got %q", expect.Disposition)
	}
	for id, candidate := range expect.Candidates {
		if !ids[id] {
			return fmt.Errorf("candidate %q is not in the world", id)
		}
		for key, want := range candidate.Caches {
			if want != "hit" && want != "miss" {
				return fmt.Errorf("candidate %q cache %q expects \"hit\" or \"miss\", got %q", id, key, want)
			}
		}
		if candidate.Schedule != nil {
			if !slices.ContainsFunc(w.Rentals, func(r RentalSpec) bool { return r.ID == id }) {
				return fmt.Errorf("candidate %q is not a Rental and cannot carry RentalSchedule evidence", id)
			}
			if candidate.Schedule.Version == 0 || candidate.Schedule.Running == nil {
				return fmt.Errorf("candidate %q schedule evidence needs a version and RunningPlacement", id)
			}
		}
	}
	return nil
}
