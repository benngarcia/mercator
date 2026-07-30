package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/scenario"
)

type ProofResult struct {
	Step     int                    `json:"step"`
	Evidence scenario.ProofEvidence `json:"evidence"`
	Passed   bool                   `json:"passed"`
	Detail   string                 `json:"detail"`
}

type ProofReport struct {
	Checkpoints      []ProofResult `json:"checkpoints"`
	NormalizedSHA256 string        `json:"normalized_sha256"`
}

type proofFacts struct {
	bundle     RunBundle
	events     []eventlog.CloudEvent
	effects    []EffectRecord
	invariants []InvariantResult
	decisions  map[string]domain.BookingDecision
}

func VerifyVerticalProof(ctx context.Context, bundle RunBundle) (ProofReport, error) {
	facts, err := readProofFacts(bundle)
	if err != nil {
		return ProofReport{}, err
	}
	results := []ProofResult{
		facts.result(1, scenario.EvidenceProducerSubmitted, facts.hasEvent("compute.run.requested.v1", "runs/run-producer"), "producer request entered the public event log"),
		facts.result(2, scenario.EvidenceExistingVsFreshCompared, facts.comparesExistingAndFresh("run-producer"), "producer decision compared standing and provisionable capacity"),
		facts.result(3, scenario.EvidencePartialImageReuse, facts.partialImageReuse("run-producer"), "standing capacity reused only part of the producer image"),
		facts.result(4, scenario.EvidenceCapacityPrepared, facts.hasAcceptedEffect(OperationProviderLaunch, "run-producer"), "the provider accepted the producer launch"),
		facts.result(5, scenario.EvidenceArtifactPublished, facts.hasAcceptedEffect(OperationArtifactPublished, "run-producer"), "the producer published an immutable Artifact to the object store"),
		facts.result(6, scenario.EvidenceConsumerUnblocked, facts.consumerFollowedArtifact(), "Mercator placed the consumer only after Artifact publication"),
		facts.result(7, scenario.EvidenceWarmthObserved, facts.consumerUsesArtifactReplica(), "the consumer selected a Rental holding a checked copy of one input and was priced the read it owed on the other"),
		facts.result(8, scenario.EvidenceQueueVsFreshCompared, facts.comparesQueueAndFresh("run-consumer"), "consumer scheduling compared standing queue delay with fresh provisioning"),
		facts.result(9, scenario.EvidenceAmbiguousDelivery, facts.hasLostAcceptedLaunch(), "the provider accepted a launch whose response was lost"),
		facts.result(10, scenario.EvidenceReconciledWithoutDuplicate, facts.oneAcceptedLaunchPerRun(), "reconciliation produced one accepted external launch per Run"),
		facts.result(11, scenario.EvidenceControlPlaneRestarted, facts.hasAcceptedEffect(OperationControlPlaneRestart, labWorkspace), "the control plane restarted while external state survived"),
	}
	restartEquivalent := verifyRestartEquivalence(ctx, bundle) == nil
	results = append(results, facts.result(12, scenario.EvidenceRestartEquivalent, restartEquivalent, "restart and no-extra-restart terminal semantics match"))
	results = append(results, facts.result(13, scenario.EvidenceUIRendered, facts.hasUIEvidence(), "the Run Bundle contains a Playwright trace and screenshot"))
	replayEquivalent := verifyBundleReplay(ctx, bundle) == nil
	results = append(results, facts.result(14, scenario.EvidenceBundleReplayed, replayEquivalent, "one Run Bundle replayed to the same normalized output"))
	results = append(results, facts.result(15, scenario.EvidenceInvariantsPassed, facts.allInvariantsPassed(), "every latest invariant result passed"))

	report := ProofReport{
		Checkpoints:      results,
		NormalizedSHA256: bundle.NormalizedSHA256(),
	}
	for _, result := range results {
		if !result.Passed {
			return report, fmt.Errorf("vertical proof checkpoint %d (%s) failed", result.Step, result.Evidence)
		}
	}
	return report, nil
}

func readProofFacts(bundle RunBundle) (proofFacts, error) {
	events, err := decodeCloudEvents(bundle.entry("events/mercator.jsonl"))
	if err != nil {
		return proofFacts{}, err
	}
	effects, err := bundle.Effects()
	if err != nil {
		return proofFacts{}, err
	}
	var invariants []InvariantResult
	if err := decodeBundleJSON("invariants.json", bundle.entry("invariants.json"), &invariants); err != nil {
		return proofFacts{}, err
	}
	facts := proofFacts{
		bundle:     bundle,
		events:     events,
		effects:    effects,
		invariants: invariants,
		decisions:  map[string]domain.BookingDecision{},
	}
	for _, event := range events {
		if event.Type != "compute.run.booking_decided.v1" {
			continue
		}
		var data struct {
			Decision domain.BookingDecision `json:"decision"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return proofFacts{}, fmt.Errorf("decode booking decision proof: %w", err)
		}
		facts.decisions[data.Decision.RunID] = data.Decision
	}
	return facts, nil
}

func decodeCloudEvents(data []byte) ([]eventlog.CloudEvent, error) {
	var events []eventlog.CloudEvent
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event eventlog.CloudEvent
		if err := decodeBundleJSON("events/mercator.jsonl", line, &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (facts proofFacts) result(step int, evidence scenario.ProofEvidence, passed bool, detail string) ProofResult {
	return ProofResult{Step: step, Evidence: evidence, Passed: passed, Detail: detail}
}

func (facts proofFacts) hasEvent(eventType, subject string) bool {
	for _, event := range facts.events {
		if event.Type == eventType && event.Subject == subject {
			return true
		}
	}
	return false
}

func (facts proofFacts) hasAcceptedEffect(operation, correlationID string) bool {
	for _, effect := range facts.effects {
		if effect.Operation == operation &&
			effect.CorrelationID == correlationID &&
			effect.Command == EffectCommandAccepted {
			return true
		}
	}
	return false
}

func (facts proofFacts) comparesExistingAndFresh(runID string) bool {
	decision, exists := facts.decisions[runID]
	if !exists {
		return false
	}
	return candidateWithDisposition(decision, domain.CandidateDispositionRunNow) != nil &&
		candidateWithDisposition(decision, domain.CandidateDispositionProvision) != nil
}

func (facts proofFacts) partialImageReuse(runID string) bool {
	decision := facts.decisions[runID]
	existing := candidateWithDisposition(decision, domain.CandidateDispositionRunNow)
	fresh := candidateWithDisposition(decision, domain.CandidateDispositionProvision)
	return existing != nil &&
		fresh != nil &&
		existing.Estimates.PullSeconds.Expected > 0 &&
		existing.Estimates.PullSeconds.Expected < fresh.Estimates.PullSeconds.Expected
}

// comparesQueueAndFresh is the decision weighing capacity Mercator already holds
// against capacity it would have to create. Either standing disposition is that
// comparison: a machine free now carries no queue delay to weigh, and a machine
// with a Run on it carries the delay this checkpoint is named after, so naming
// only run_now accepted the case with nothing to compare and refused the case
// with something. What has to be on the record is a standing candidate whose
// queue delay was established and a fresh candidate priced to provision.
func (facts proofFacts) comparesQueueAndFresh(runID string) bool {
	decision := facts.decisions[runID]
	standing := candidateWithDisposition(decision, domain.CandidateDispositionRunNow)
	if standing == nil {
		standing = candidateWithDisposition(decision, domain.CandidateDispositionQueue)
	}
	fresh := candidateWithDisposition(decision, domain.CandidateDispositionProvision)
	return standing != nil &&
		fresh != nil &&
		standing.Estimates.QueueSeconds.Source != "" &&
		fresh.Estimates.ProvisionSeconds.Expected > 0
}

func candidateWithDisposition(decision domain.BookingDecision, disposition domain.CandidateDisposition) *domain.CandidateDecision {
	for index := range decision.Candidates {
		if decision.Candidates[index].Disposition == disposition {
			return &decision.Candidates[index]
		}
	}
	return nil
}

// consumerFollowedArtifact is the durability gate read out of Mercator's own
// record. A Run enters the control plane when it arrives, whatever its inputs
// are worth; what waits for a publication is the placement, which is the
// decision admission governs. Reading the request time instead would pass on any
// Blueprint whose consumer simply arrives late.
func (facts proofFacts) consumerFollowedArtifact() bool {
	var publishedAt time.Time
	for _, effect := range facts.effects {
		if effect.Operation == OperationArtifactPublished &&
			effect.CorrelationID == "run-producer" &&
			effect.Command == EffectCommandAccepted {
			publishedAt = effect.At
			break
		}
	}
	if publishedAt.IsZero() {
		return false
	}
	for _, event := range facts.events {
		if event.Type != "compute.run.booking_decided.v1" || event.Subject != "runs/run-consumer" {
			continue
		}
		decidedAt, err := time.Parse(time.RFC3339Nano, event.Time)
		return err == nil && !decidedAt.Before(publishedAt)
	}
	return false
}

// consumerUsesArtifactReplica is Placement having chosen the host holding the
// content the Run reads, on the strength of holding it. It reads the decision
// Mercator recorded: the selected candidate cites a copy something checked of an
// Artifact this Run reads and owes no read for it, and owes the whole read of
// every input nothing verified there.
//
// Both halves are the claim. The consumer reads its producer's checkpoint and a
// dataset a fetch put on this machine before the world started, and only the
// second is warmth: a workload writes its output inside its own container and no
// runtime enumerates, hashes, or files that content, so the machine that computed
// the checkpoint owes the same read for it as any other. A rule that asked only
// for warmth would pass on a decision that had invented some.
//
// The rule this replaced asked only whether the producer's output landed on the
// offer the consumer was selected on. That passed on a Blueprint with one
// standing Rental whatever Placement weighed, which was every execution of this
// demo before Artifact locality was scored anywhere: two facts about the same
// machine agreeing is not evidence that either caused the other.
func (facts proofFacts) consumerUsesArtifactReplica() bool {
	decision, exists := facts.decisions["run-consumer"]
	if !exists {
		return false
	}
	selected := selectedCandidate(decision)
	if selected == nil || len(selected.ArtifactEvidence) < 2 {
		return false
	}
	checked := 0
	for _, found := range selected.ArtifactEvidence {
		if found.Locality == domain.LocalityHot && found.FetchBytes == 0 {
			checked++
			continue
		}
		if found.FetchBytes == 0 {
			return false
		}
	}
	return checked > 0
}

func (facts proofFacts) hasLostAcceptedLaunch() bool {
	for _, effect := range facts.effects {
		if effect.Operation == OperationProviderLaunch &&
			effect.Command == EffectCommandAccepted &&
			effect.Response == EffectResponseLost {
			return true
		}
	}
	return false
}

func (facts proofFacts) oneAcceptedLaunchPerRun() bool {
	counts := map[string]int{}
	for _, effect := range facts.effects {
		if effect.Operation == OperationProviderLaunch && effect.Command == EffectCommandAccepted {
			counts[effect.CorrelationID]++
		}
	}
	return counts["run-producer"] == 1 && counts["run-consumer"] == 1
}

func (facts proofFacts) hasUIEvidence() bool {
	hasTrace := bytes.HasPrefix(facts.bundle.entry("ui/trace.zip"), []byte("PK"))
	hasScreenshot := false
	for _, name := range facts.bundle.EntryNames() {
		hasScreenshot = hasScreenshot ||
			strings.HasPrefix(name, "ui/screenshots/") &&
				bytes.HasPrefix(facts.bundle.entry(name), []byte("\x89PNG\r\n\x1a\n"))
	}
	return hasTrace && hasScreenshot
}

func (facts proofFacts) allInvariantsPassed() bool {
	if len(facts.invariants) == 0 {
		return false
	}
	for _, invariant := range facts.invariants {
		if invariant.Status != InvariantPassed {
			return false
		}
	}
	return true
}

func verifyRestartEquivalence(ctx context.Context, bundle RunBundle) error {
	archive, err := bundle.Bytes()
	if err != nil {
		return err
	}
	execution, err := Replay(ctx, archive)
	if err != nil {
		return err
	}
	defer execution.Close()
	return CheckRestartPreservesTerminalBehavior(ctx, execution.config, 1)
}

func verifyBundleReplay(ctx context.Context, bundle RunBundle) error {
	execution, err := Reconstruct(ctx, bundle)
	if err != nil {
		return err
	}
	defer execution.Close()
	replayed, err := execution.Export(ctx)
	if err != nil {
		return err
	}
	if replayed.NormalizedSHA256() != bundle.NormalizedSHA256() {
		return fmt.Errorf("replay normalized hash differs")
	}
	return nil
}
