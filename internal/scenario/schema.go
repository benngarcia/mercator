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
// (deferral, cache evidence, host facts); the runner reports their failures as
// pending-red, and a target scenario that starts passing must be promoted.
package scenario

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
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

// defaultWorldStart is the scripted clock's origin when a fixture does not
// state one. Every relative moment ("+6m") resolves against it.
var defaultWorldStart = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

type Scenario struct {
	Name    string    `json:"-"`
	Summary string    `json:"summary"`
	Status  Status    `json:"status"`
	World   WorldSpec `json:"world"`
	// Request/Expect is the single-decision shorthand; Timeline is the scripted
	// alternative for scenarios that advance the clock or submit several runs.
	Request  *RequestSpec `json:"request,omitempty"`
	Expect   *ExpectSpec  `json:"expect,omitempty"`
	Timeline []StepSpec   `json:"timeline,omitempty"`
}

type WorldSpec struct {
	Clock       time.Time              `json:"clock,omitzero"`
	Images      map[string]ImageSpec   `json:"images,omitempty"`
	Rentals     []RentalSpec           `json:"rentals,omitempty"`
	Marketplace []MarketplaceOfferSpec `json:"marketplace,omitempty"`
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

// RentalSpec is a standing machine the broker owns: what it is running and
// for how much longer, which image layers and named data caches it holds, and
// how long its idle lease has left.
type RentalSpec struct {
	ID    string    `json:"id"`
	State string    `json:"state"` // "idle" | "busy"
	Busy  *BusySpec `json:"busy,omitempty"`
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

type BusySpec struct {
	// RemainingMaxRuntime is how much of the running run's maximum runtime is
	// left: the recorded fact a deferral deadline derives from.
	RemainingMaxRuntime Duration `json:"remaining_max_runtime"`
	// FreesAfter is when the rental is actually observed free, defaulting to
	// RemainingMaxRuntime. A later value models enforcement or observation
	// lag, holding the rental busy past a deferral deadline.
	FreesAfter *Duration `json:"frees_after,omitempty"`
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
	// offer), "defer" (a reason and deadline, no selection), or "fail" (a
	// recorded decision with no feasible offers).
	Outcome string     `json:"outcome"`
	Offer   string     `json:"offer,omitempty"`
	Reasons []string   `json:"reasons,omitempty"`
	Defer   *DeferSpec `json:"defer,omitempty"`
	// Disposition asserts the recorded cleanup intent on the launch intent:
	// "release" for standing rentals, "terminate" for provisioned hosts.
	Disposition string `json:"disposition,omitempty"`
	// Candidates assert the per-candidate evidence the decision weighed,
	// keyed by rental or marketplace offer ID.
	Candidates map[string]CandidateExpectation `json:"candidates,omitempty"`
}

type DeferSpec struct {
	Reason string `json:"reason"`
	// Deadline is when waiting must give up, written relative to the world
	// clock's start ("+6m").
	Deadline Moment `json:"deadline"`
}

type CandidateExpectation struct {
	Feasible         *bool           `json:"feasible,omitempty"`
	Rejected         []RejectionSpec `json:"rejected,omitempty"`
	QueueSeconds     *Bound          `json:"queue_seconds,omitempty"`
	ProvisionSeconds *Bound          `json:"provision_seconds,omitempty"`
	PullSeconds      *Bound          `json:"pull_seconds,omitempty"`
	// Caches asserts recorded named-cache evidence, key to "hit" or "miss".
	// Target ontology: decisions record no cache evidence yet.
	Caches map[string]string `json:"caches,omitempty"`
}

type RejectionSpec struct {
	Code string `json:"code"`
	Path string `json:"path"`
}

// StepSpec is one timeline entry: exactly one of Submit (a named run with its
// request and expectation), Advance (move the scripted clock), or Reevaluate
// (drive a named run's next advancement and assert the latest decision).
type StepSpec struct {
	Submit     string       `json:"submit,omitempty"`
	Request    *RequestSpec `json:"request,omitempty"`
	Advance    *Duration    `json:"advance,omitempty"`
	Reevaluate string       `json:"reevaluate,omitempty"`
	Expect     *ExpectSpec  `json:"expect,omitempty"`
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
	return nil
}

func (step StepSpec) validate(submitted map[string]bool) error {
	actions := 0
	if step.Submit != "" {
		actions++
	}
	if step.Advance != nil {
		actions++
	}
	if step.Reevaluate != "" {
		actions++
	}
	if actions != 1 {
		return fmt.Errorf("each step is exactly one of submit, advance, or reevaluate")
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
	case step.Reevaluate != "":
		if step.Expect == nil {
			return fmt.Errorf("reevaluate %q requires an expect", step.Reevaluate)
		}
		if step.Request != nil {
			return fmt.Errorf("reevaluate carries no request")
		}
		if !submitted[step.Reevaluate] {
			return fmt.Errorf("reevaluate %q references a run never submitted", step.Reevaluate)
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
		switch rental.State {
		case "idle":
			if rental.Busy != nil {
				return fmt.Errorf("rental %q: idle rentals carry no busy block", rental.ID)
			}
		case "busy":
			if rental.Busy == nil {
				return fmt.Errorf("rental %q: busy rentals state their remaining max runtime", rental.ID)
			}
		default:
			return fmt.Errorf("rental %q: state must be \"idle\" or \"busy\", got %q", rental.ID, rental.State)
		}
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
	case "place":
		if expect.Offer == "" {
			return fmt.Errorf("outcome \"place\" names the winning offer")
		}
		if !ids[expect.Offer] {
			return fmt.Errorf("expected offer %q is not in the world", expect.Offer)
		}
		if expect.Defer != nil {
			return fmt.Errorf("outcome \"place\" carries no defer block")
		}
	case "defer":
		if expect.Defer == nil {
			return fmt.Errorf("outcome \"defer\" states a reason and deadline")
		}
		if expect.Offer != "" {
			return fmt.Errorf("outcome \"defer\" selects no offer")
		}
	case "fail":
		if expect.Offer != "" || expect.Defer != nil {
			return fmt.Errorf("outcome \"fail\" selects no offer and carries no defer block")
		}
	default:
		return fmt.Errorf("outcome must be \"place\", \"defer\", or \"fail\", got %q", expect.Outcome)
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
	}
	return nil
}
